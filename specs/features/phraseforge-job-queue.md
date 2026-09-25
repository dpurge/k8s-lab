---
title: Port knowledge's job queue into phraseforge
kind: feature
status: done
version: 1
updated: 2026-09-24
branch: main
---

## Problem / Motivation

phraseforge's LLM calls (`/llm/generate`, covering transcription and translation) are synchronous and blocking in the HTTP handler today — the handler calls `ai.Service.Generate(...)` directly, which calls `llm.New(...).Complete(...)` and waits for Ollama. specs/memory.md already documents that overlapping/concurrent Ollama load on this resource-constrained dev machine can starve the k3d node until kubelet marks it NotReady — exactly the problem knowledge's `internal/queue` package exists to prevent (a single global worker, one LLM call in flight app-wide, with interactive requests prioritized over background work).

This feature ports that pattern into phraseforge, but as a **single unified `jobs` table** rather than knowledge's split `jobs` (ingest-specific tracker) + `operations` (general LLM queue) tables — this follows a prior architecture decision made when planning phraseforge's roadmap: a future "Jobs" menu (the next roadmap item, `phraseforge-jobs-menu`) should show every submitted operation, interactive and background alike, in one place, rather than only a subset being visible the way knowledge's split design leaves its `operations` queue invisible to any UI.

`/llm/generate`'s external HTTP contract does not change — the same request/response shape, still synchronous from the caller's point of view — but internally it now goes through `EnqueueAndAwait`, so it's serialized against any future background job (ingest, vocabulary/models generation, etc.) instead of running fully concurrently with it. This feature also adds structured `slog` logging for every LLM call (kind, provider, model, duration, outcome — never request/response content), matching knowledge's telemetry convention, extended to include `provider` since phraseforge now supports multiple providers (Ollama, OpenRouter) as of the just-shipped `phraseforge-shared-model-config` feature.

This feature is infrastructure only: no Jobs UI, and no new job *kinds* beyond wrapping the existing transcription/translation generate path under one `llm_generate` kind. Future features (ingest, vocabulary/models generation, grammar-from-knowledge) each register their own kind against this same infrastructure when they're built — this feature does not anticipate their payload shapes.

## Acceptance Criteria

- A new `phraseforge/internal/jobs` package provides: `Enqueue(ctx, kind, priority, payload) (id, error)`, `EnqueueAndAwait(ctx, kind, priority, payload) (result, error)`, `Register(kind, handlerFunc)`, `Run(ctx)` (worker loop), `Get(ctx, id) (Job, error)`, `List(ctx) ([]Job, error)`, `Retry(ctx, id) (error)`, `FailStale(ctx) (error)`.
- A single unified `jobs` table in `phraseforge/internal/db/schema.sql` (not a split jobs+operations design): columns `id` (uuid, primary key), `kind` (text), `priority` (text, check `interactive`/`background`), `status` (text, check `pending`/`running`/`done`/`failed`), `payload` (jsonb), `result` (jsonb), `error` (text), `step` (text, nullable — reserved for a future multi-step job kind like ingest; unused by this feature), `created_at`/`updated_at` (timestamptz).
- Two indexes matching knowledge's `operations` table exactly in intent: `jobs_one_running_idx` — a `UNIQUE` index on `((true)) WHERE status='running'`, enforcing exactly one job in flight app-wide (the single-worker/single-Ollama-call-at-a-time constraint); `jobs_claim_idx` — `(created_at ASC) WHERE status='pending'`, for efficient claiming.
- The claim operation is one atomic SQL statement: `UPDATE jobs SET status='running', updated_at=now() WHERE id = (SELECT id FROM jobs WHERE status='pending' ORDER BY (priority='interactive') DESC, created_at ASC LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING ...` — interactive-priority jobs are always claimed before any pending background job, regardless of arrival order.
- `FailStale()` is called once at startup (before `Run()` starts), marking any row stuck at `status='running'` as `failed` — recovers from a prior crash where a claimed row would otherwise permanently block the single-running-slot index.
- Exactly one handler kind is registered in this feature: `llm_generate`, wrapping the existing `ai.Service.Generate(...)` call. The handler unmarshals a JSON payload (`kind`, `source_language`, `target_language`, `content_type`, `content` — the same fields `/llm/generate`'s request already carries), calls `Generate`, and marshals the text result (or error) back.
- `phraseforge/internal/server/llm.go`'s `/llm/generate` handler calls `jobsSvc.EnqueueAndAwait(ctx, "llm_generate", jobs.PriorityInteractive, payload)` instead of calling `ai.Service.Generate(...)` directly. The HTTP request/response shape is unchanged from today.
- Every actual LLM call (inside the `llm_generate` handler, around the `llm.New(...).Complete(...)` call) logs one `slog.Info` line with fields `kind`, `provider`, `model`, `duration_ms`, `outcome` — never the request content or the response text.
- `phraseforge/main.go` wires: construct the jobs service against the DB pool, call `FailStale(ctx)`, register the `llm_generate` handler, start `go jobsSvc.Run(ctx)`, all before the HTTP server starts listening — mirroring knowledge's exact startup order in `knowledge/main.go`.
- `List`/`Get`/`Retry` exist as Go methods on the jobs service but are not exposed via any new HTTP route and not called from any handler in this feature — `phraseforge-jobs-menu` (the next roadmap item) is expected to add the API/UI that calls them.
- No change to `ai.Service.Generate`'s own signature or behavior, no change to `shared/llm`.
- `go build ./...`, `go vet ./...`, and `go test ./...` pass. Manual verification: a transcription call and a translation call via `/llm/generate` both still succeed with the same response shape as before, and each produces one `slog` line as described above.

## Approach

1. **Schema**: add the `jobs` table plus its two indexes to `phraseforge/internal/db/schema.sql`, in the same idempotent (`CREATE TABLE IF NOT EXISTS`) style as the rest of that file, modeled on `knowledge/internal/db/db.go`'s `operations` table DDL but as a single table (adds the `step` column knowledge's `operations` table doesn't have, reserved for future use).
2. **New `phraseforge/internal/jobs/jobs.go`**: port `Enqueue`/`EnqueueAndAwait`/`Register`/`Run`/`Get`/`List`/`Retry`/`FailStale` from `knowledge/internal/queue/queue.go`, adapted to the single `jobs` table and this package's naming (`Job`/`PriorityInteractive`/`PriorityBackground` instead of `Operation`). `EnqueueAndAwait` polls `Get()` until the job reaches `done`/`failed`, exactly matching knowledge's approach (no new synchronization primitive). `Retry(ctx, id)` re-reads a `failed` job's original `kind`/`payload` and calls `Enqueue` again with them.
3. **`phraseforge/internal/ai/ai.go`**: add a small handler function matching the jobs package's handler signature (`func(ctx, json.RawMessage) (json.RawMessage, error)`) that unmarshals the payload, calls the existing `Generate(...)`, and marshals the result. Add the `slog.Info` telemetry line (kind/provider/model/duration_ms/outcome) around the actual `llm.New(...).Complete(...)` call inside `Generate` (or in the new handler wrapping it — whichever keeps `Generate`'s existing callers/behavior otherwise unchanged).
4. **`phraseforge/internal/server/llm.go`**: replace the direct `s.ai.Generate(...)` call with `s.jobs.EnqueueAndAwait(ctx, "llm_generate", jobs.PriorityInteractive, payload)`, unmarshal the result, write the same JSON response shape as today. Errors from a failed job surface the same way a direct `Generate` error would today.
5. **`phraseforge/main.go`**: construct the jobs service, call `FailStale(ctx)`, `Register("llm_generate", ...)`, `go jobsSvc.Run(ctx)` — before the HTTP listener starts, mirroring `knowledge/main.go`'s exact ordering.

Key decisions: (a) a single unified `jobs` table/package, not knowledge's jobs+operations split, because the planned Jobs menu (next roadmap item) is meant to show every submitted operation, not leave a queue invisible; (b) no `Direct`-bypass variant (knowledge's `TitleDirect`/`SummaryDirect` pattern) in this feature — nothing in phraseforge today calls `Generate` from inside another queue handler, so the deadlock this pattern avoids doesn't yet exist here; it gets added when a future feature (e.g. ingest) actually needs a nested call; (c) `List`/`Get`/`Retry` are built now as Go API but deliberately not wired to any HTTP route yet, keeping this feature backend-infrastructure-only and letting `phraseforge-jobs-menu` add the API/UI surface on top without touching this package's core logic.

## Affected Areas

- `phraseforge/internal/db/schema.sql`
- `phraseforge/internal/jobs/jobs.go` (new)
- `phraseforge/internal/ai/ai.go`
- `phraseforge/internal/server/llm.go`
- `phraseforge/main.go`

## Out of Scope

- A Jobs menu/UI or any new HTTP route for listing/retrying/deleting jobs — covered by the separate `phraseforge-jobs-menu` roadmap item.
- Ingest, vocabulary generation, models generation, or any other new job kind — each is scoped to its own later roadmap item and registers its own kind against this infrastructure when built.
- A `Direct`-bypass variant (like knowledge's `TitleDirect`/`SummaryDirect`) — not needed until a future feature calls a queue handler from inside another queue handler.
- Any change to knowledge's own `internal/queue`/`internal/jobs` implementation.
- Any change to `ai.Service.Generate`'s signature, `shared/llm`, or the admin LLM-prompt management feature.

## Implementation Notes

All 5 Approach steps implemented in one pass. `go build ./...`, `go vet ./...`, `go test ./...` clean in `phraseforge` (no new test files added for the `jobs` package itself — no existing pattern in this repo for testing a DB-backed queue without a live Postgres; knowledge's own `internal/queue` has none either). `gofmt -l` clean.

**Files changed/created:**
- `phraseforge/internal/db/schema.sql`: new `jobs` table (`id` uuid PK — client-generated via crypto/rand, matching knowledge's own UUID generation rather than assuming a Postgres extension like pgcrypto that isn't used anywhere in this repo; `kind`, `priority` check(interactive/background), `status` check(pending/running/done/failed) default 'pending', `payload`/`result` jsonb, `error` text, `step` text nullable/reserved for future multi-step jobs, `created_at`/`updated_at`), plus `jobs_one_running_idx` (unique on `((true)) WHERE status='running'`) and `jobs_claim_idx` (`(created_at ASC) WHERE status='pending'`).
- New `phraseforge/internal/jobs/jobs.go`: `Service` with `Enqueue`/`EnqueueAndAwait`/`Register`/`Run`/`Get`/`List`/`Retry`/`FailStale`, ported from `knowledge/internal/queue/queue.go`. `EnqueueAndAwait` polls on a 500ms ticker (matching knowledge's interval). `Run`'s worker loop drains all claimable work before waiting on a wake channel or the next tick, so a newly-enqueued interactive job doesn't wait a full tick if the worker is already awake between background jobs. `complete()` uses `context.Background()` for the terminal-state write, not the job's own context, matching the documented gotcha from `knowledge/internal/ingest/ingest.go`'s `finish` helper (a worker-shutdown context must never be used to record completion, or a job in flight at shutdown could never record its own outcome).
- `phraseforge/internal/ai/ai.go`: added `KindLLMGenerate = "llm_generate"` constant, wrapped the `llm.New(clientCfg).Complete(...)` call with `slog.Info("llm call", "kind", ..., "provider", ..., "model", ..., "duration_ms", ..., "outcome", "success"|"error")` (never logs content or the response), added `HandleGenerate` matching `jobs.HandlerFunc`'s signature.
- `phraseforge/internal/server/llm.go`: `/llm/generate`'s handler now calls `s.jobs.EnqueueAndAwait(r.Context(), ai.KindLLMGenerate, jobs.PriorityInteractive, payload)` instead of calling `ai.Service.Generate` directly; same request/response JSON shape and validation as before.
- `phraseforge/internal/server/server.go`: `Server` gained a `jobs *jobs.Service` field and `New(...)` a trailing `jobsSvc *jobs.Service` parameter.
- `phraseforge/main.go`: after `db.Connect`, before `server.New`: constructs the jobs service, calls `FailStale(ctx)` (fatal on error, matching this file's existing bootstrap style), registers `ai.KindLLMGenerate` → `aiSvc.HandleGenerate`, starts `go jobsSvc.Run(ctx)`.

**Noted deviation from the spec's literal `Retry(ctx, id) (error)` signature**: implemented as `Retry(ctx, id string) (string, error)`, returning the new job's id — matches knowledge's own `Enqueue`-returns-id convention and is more useful for the next feature's UI (jump to the new job after retrying), not a functional gap.

**Coordinator's follow-up note (not yet independently verified)**: `jobColumns`'s `coalesce(result, 'null')` against a `jsonb` column, and similarly `coalesce(error, '')`/`coalesce(step, '')` against `text` columns, are a correct and common Postgres idiom on inspection, but haven't yet been exercised against a live database — this will be confirmed in the Validation phase via an actual deploy and job execution.

**Reviewer pass (this introduces new concurrency machinery — a single-worker DB-backed queue — so warranted extra scrutiny)**: found several issues. Two were fixed, several deliberately deferred:

- **Fixed (blocking)**: `phraseforge/k8s/deployment.yaml`'s `RollingUpdate` strategy had no explicit `maxSurge`/`maxUnavailable`, so Kubernetes' default (`maxSurge: 25%` → rounds up to 1 extra pod) meant a `kubectl rollout restart` briefly ran two live pods. Each pod's `FailStale()` at startup unconditionally marks every `status='running'` row `failed` — including a row the OTHER, still-healthy pod's worker was actively processing — corrupting an in-flight job and briefly freeing the single-running-slot index for two concurrent Ollama calls (the exact contention this feature exists to prevent). Fixed by adding an explicit `rollingUpdate: {maxSurge: 0, maxUnavailable: 1}`, forcing a strict one-pod-at-a-time swap. Verified live: a redeploy after this fix showed the old pod terminate before the new one became ready ("0 of 1 updated replicas are available" during the swap), with a clean single-pod startup afterward.
- **Fixed (high)**: `jobs.go`'s `complete()` had two related gaps: (1) its `UPDATE` had no `AND status='running'` guard, so it could unconditionally overwrite a row's status even if something else (e.g. a concurrent `FailStale`) had already moved it out of `running` — now guarded, and a zero-rows-affected outcome logs at Info level rather than being silently ignored or treated as an error; (2) it used an undeadlined `context.Background()`, so a hung DB connection could block the single worker goroutine forever — now wrapped in a 10-second timeout, so a hung write eventually gives up (the job's row is left `running` and gets recovered by the next `FailStale` at the next restart, rather than wedging the whole queue permanently).
- **Fixed (nitpick)**: `jobs.go`'s worker-loop log line used `outcome="ok"` while `ai.go`'s LLM-call log line used `outcome="success"` — standardized both to `"success"`/`"error"`.
- **Deliberately deferred, not fixed in this feature**:
  - Adding a `user_id`/owner column to the `jobs` table for future per-user authorization — the reviewer's own reasoning was to make `phraseforge-jobs-menu` (the next roadmap item) cheaper to build, but that feature hasn't decided whether it needs per-user scoping at all (the roadmap describes it as showing "every submitted job," which may mean an admin-wide view, not a per-user one) — adding this column now would presume a design decision that belongs to that feature. Flagged for that feature's own B1 research/design phase.
  - HTTP error status-code mapping (`llm.go` currently maps every job failure to HTTP 500, including what should arguably be a 400 for "content is required") — this is pre-existing behavior carried through unchanged from the direct-call path, not a regression introduced by this feature, and the spec's own Approach explicitly said to preserve existing error handling as closely as possible.
  - Upstream provider error bodies being echoed to the client and now persisted in the `jobs.error` column — pre-existing behavior in `shared/llm`, not touched or worsened in kind by this feature (only made durable instead of transient); flagged as a `shared/llm` follow-up, out of scope here.
  - Bounding `EnqueueAndAwait`'s wait time or limiting queue depth — legitimate future hardening for a busier deployment, but explicit scope creep beyond "port the existing pattern and route today's calls through it" for what's still a single-developer local dev tool.
  - Several smaller nitpicks (claim index not matching the exact `ORDER BY`, the unregistered-kind path failing rather than releasing a job back to `pending` during a rollback race, `Enqueue`'s INSERT using the caller's cancelable context, `Get` surfacing a raw Postgres error on a malformed id) — all low-severity/edge-case, left as-is.

All builds/tests re-verified green after this round: `go build ./...` + `go vet ./...` + `go test ./...` clean in `phraseforge`; `shared` and `knowledge` still build. A full redeploy (`task deploy-phraseforge`) succeeded cleanly with the new rollout strategy, confirmed via `kubectl get pods` (exactly one pod) and startup logs (no errors, correct `transcription=ollama/gemma4:12b, translation=ollama/gemma4:12b` line) — no additional LLM generate calls were needed or made for this re-verification, since none of the three fixes touch the actual LLM call path.

## Validation

Full end-to-end deploy + smoke test against the local k3d dev cluster (`task deploy-phraseforge`), single clean pass (no bugs found, unlike the prior feature's first attempt):

- **Static validation**: `go build ./...`, `go vet ./...`, `go test ./...` clean in `phraseforge`; `gofmt -l` clean.
- **Deploy**: `task deploy-phraseforge` completed cleanly — image built, ConfigMap/migration/rollout all succeeded, ready at `http://phraseforge.localhost:8080`.
- **Schema migration**: confirmed live — `jobs` table has all expected columns and constraints (`jobs_priority_check`, `jobs_status_check`) plus both required indexes (`jobs_claim_idx`, `jobs_one_running_idx`).
- **LLM smoke test** (capped at 2 real generate calls total, per explicit direction): one transcription call and one translation call via `/llm/generate`, both succeeded with the unchanged `{"text": "..."}` response shape.
- **Job row verification** (the key correctness check): confirmed exactly two `jobs` rows were created for the two calls above, both `kind='llm_generate'`, `priority='interactive'`, `status='done'`, no error — proving the requests went through the real queue (atomic claim, `coalesce(result,'null')`/`coalesce(error,'')` read-write against live `jsonb`/`text` columns, completion write), not a bypass. This also confirms the `coalesce(...)`-against-`jsonb` SQL idiom flagged as unverified-by-inspection in Implementation Notes works correctly against live Postgres.
- **slog verification**: confirmed both calls produced a `"llm call"` log line with the exact fields specified (`kind`, `provider`, `model`, `duration_ms`, `outcome`), `outcome=success` for both; confirmed by grep that no request/response content leaked into the logs.
- **FailStale ordering**: confirmed statically in `phraseforge/main.go` that `FailStale(ctx)` runs before `Register`/`Run`, and all three run before the HTTP server starts listening, matching the required startup order. (Actually triggering a stale-job recovery would require killing the pod mid-job, judged not worth the disruption for this validation — this is a known, accepted gap, not a failure.)

All Acceptance Criteria confirmed met. No regressions, no bugs found in this pass.

**Coverage gap noted for future work** (not a defect, carried over from a pre-existing repo-wide pattern): no automated tests exist for `phraseforge/internal/jobs` (or knowledge's equivalent `internal/queue`) since there's no existing pattern in this repo for testing a DB-backed queue without a live Postgres — this live deploy/smoke-test is the only coverage this logic has. Worth revisiting if `phraseforge-jobs-menu` (the next roadmap item) builds more logic on top of `jobs.Service` without adding a testable seam.

## Documentation Review

Three constitution and project docs were checked against this feature's changes:

1. **`specs/tech-stack.md`** — Found drift: the section `**Background operation queue (knowledge app):**` (version 9, lines 117–123) was incomplete and app-specific. It described only knowledge's queue pattern and did not account for phraseforge's parallel implementation. **Drift severity: Medium** — it is a gap in the project's constitution rather than a contradiction, but the convention it documents now spans two apps and needs to reflect both.

2. **`phraseforge/CHANGELOG.md`** — No drift. This feature is a purely internal infrastructure change: `/llm/generate`'s request/response shape is byte-identical to before, no new UI, no new API surface. Only the internal routing (through a queue instead of direct call) and structured logging of LLM-call telemetry (an operational/ops-facing detail, not user-facing) changed. Per this project's changelog policy (user-facing features only), this change does not require a changelog entry.

3. **`phraseforge/README.md`** — No drift. The README documents only user-facing features and architecture overview, not internal request-handling patterns (synchronous vs. queued). No update needed.

## Documentation Updates

**Files updated:**

1. **`specs/tech-stack.md`** — Version 10, updated 2026-09-24. Rewrote the `**Background operation queue (knowledge, phraseforge):**` section (was app-specific, now covers both apps):
   - Changed title from `(knowledge app)` to `(knowledge, phraseforge)`.
   - Added two new bullet points explaining the difference in table design: knowledge's split `jobs` + `operations` tables vs. phraseforge's unified `jobs` table, and the reasoning (phraseforge's future Jobs UI will show all jobs in one place rather than leaving a queue invisible).
   - Kept the existing bullet about handler registration (applies to both apps identically).

2. **`specs/artifacts/phraseforge/roadmap.md`** — Removed the feature's `## Now` line (`- Port knowledge's job queue into phraseforge — branch \`main\` — specs/features/phraseforge-job-queue.md`) since the work is complete. Per constitution-format rules, this mechanical bookkeeping removal does not require a version bump or approval gate.

**No changelog entry added to `phraseforge/CHANGELOG.md`** — This feature is a purely internal infrastructure change (no user-facing behavior change, no new API surface) and therefore does not meet this project's changelog policy (user-facing features only).
