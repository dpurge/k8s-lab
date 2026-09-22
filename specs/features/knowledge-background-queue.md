---
title: Global background operation queue for LLM calls
kind: feature
status: done
version: 1
updated: 2026-09-22
branch: main
---

## Problem / Motivation

Every LLM-calling code path in this app either blocks its HTTP request or runs an unbounded per-job goroutine, and multiple such calls can overlap:

- `chat.Send` is fully synchronous: it inserts the user message, does a Qdrant search, then blocks on the LLM call, then inserts the assistant reply, before the HTTP handler returns.
- `generate.Title`/`generate.Summary` call the LLM inline from their HTTP handlers.
- `ingest` starts one goroutine per submitted job with no cross-job sequencing, so N concurrently submitted ingest jobs can mean N concurrent Ollama calls.

This is a known risk on this project's resource-constrained dev machine: `specs/memory.md` already records that overlapping concurrent Ollama requests can starve the k3d node until kubelet marks it NotReady (2026-09-21T17:30:36Z), and that every new synchronous/unbounded-concurrent LLM-calling code path is a fresh way to trigger that (2026-09-22T23:04:00Z decision entry).

The user's stated goals for this feature:
- At most one LLM operation should run at a time, app-wide, queued rather than concurrent.
- The queue must still let interactive work (chat replies, manual generate) jump ahead of background work (ingest), so the GUI stays responsive even while a large ingest job is running.
- Chat should feel synchronous to the user even though the reply is produced asynchronously: posting a message should add it to the chat immediately, and the user should be able to fire an operation, navigate away, and come back later to read the result — nothing should be held only in an HTTP response or in-memory state.
- Retrying a URL-sourced ingest job currently always re-fetches the URL, even when the content was already fetched and stored (in `source_text`) on a prior attempt; this is wasted work and inconsistent with text/file-upload retries, which already reuse `source_text` correctly.
- There is no structured, queryable operational logging today, only scattered `log.Printf` calls. Every LLM call, every ingestion job, every data import, and every knowledge item edit should be logged (without logging full content bodies, per this project's security conventions).
- Importing data currently requires title, summary, and body to all be non-empty (the shared `validate()` helper used by both `qdrant.Import` and single-item Create/Update). The user wants import to require only `body`; `title` and `summary` should be optional on import, backfilled later by a background-priority generate operation if missing. `tags` should also stay optional on import (the user explicitly said not to enforce tags yet). Create/Update's own validation is unchanged — title+summary+body stay required there.

## Acceptance Criteria

1. A single global background worker processes at most one LLM-calling "operation" at a time across the whole app (chat replies, title/summary generation, ingest chunk processing) — never two concurrent Ollama calls from this app.
2. Operations have two priorities: `interactive` (`chat_reply`, `generate_title`, `generate_summary`) and `background` (`ingest_chunk`). The worker always picks the oldest pending `interactive` operation over any pending `background` operation; background operations only run when no interactive operation is pending.
3. Sending a chat message returns as soon as the user's message is persisted, without waiting for the LLM. The assistant's reply is produced by a queued operation and becomes visible via the existing `GET /chats/{id}` polling — no new endpoint needed.
4. The manual "Generate title/summary" HTTP endpoints keep their current synchronous request/response contract (the client still gets the generated text back in the same response), but internally the work is queued and awaited rather than calling the LLM inline, so it still participates in the single-worker, priority-ordered queue.
5. Ingest jobs no longer run one goroutine per job independently. Each chunk's processing is a queued `background`-priority operation; completing chunk N enqueues chunk N+1 (or finishes the job) rather than looping in a goroutine. At most one ingest chunk is ever running at a time app-wide, and it yields to any pending interactive operation between chunks.
6. Retrying a URL-sourced ingest job reuses the job's previously fetched `source_text` instead of re-fetching the URL, matching what text/file-upload retries already do correctly.
7. Every LLM call (chat, generate, translate), every ingest job start/finish, every data import, and every knowledge item create/update/delete is logged via structured (`log/slog`) stdout logging, including duration and outcome, and never including full content bodies (title/summary/body text, prompts, or LLM responses).
8. Importing data requires only a non-empty `body`; `title`, `summary`, and `tags` are all optional. If `title` or `summary` is missing after import, a `background`-priority generate operation is enqueued for that item, and the item is visible in the knowledge list immediately (with a placeholder shown for whichever field is still being generated — the exact placeholder UI is out of scope for this feature, see below, but the backend must make the in-progress state distinguishable from "field is genuinely empty").
9. A user who fires an interactive operation (chat send, or generate) and navigates away or closes the tab can come back later and see the completed result — it must be durably persisted, not held only in an HTTP response or server-process memory.
10. No behavior regresses for existing single-item Create/Update: title, summary, and body all remain required there, unchanged.

## Approach

**New `knowledge/internal/queue` package.** An `operations` table: `id, kind, priority ('interactive'|'background'), status ('pending'|'running'|'done'|'failed'), payload JSONB, result JSONB, error, created_at, updated_at`. One global partial unique index enforces at most one `'running'` row app-wide — deliberately not per-*kind* like `jobs.jobs_one_running_idx`, since the whole point is one shared LLM-call slot for the entire app. A single worker goroutine (`queue.Service.Run(ctx)`), started from `main.go`, loops: claim the oldest eligible pending row with one atomic statement —

```sql
UPDATE operations SET status = 'running', updated_at = now()
WHERE id = (
  SELECT id FROM operations
  WHERE status = 'pending'
  ORDER BY (priority = 'interactive') DESC, created_at ASC
  LIMIT 1
  FOR UPDATE SKIP LOCKED
)
RETURNING *;
```

— dispatch to a registered handler for that `kind`, then update `status`/`result`/`error` on completion, and loop again (a small fixed poll interval, e.g. 500ms, between checks when the queue is empty is acceptable given this queue's low throughput and single-worker nature).

**Handler registration mirrors this codebase's existing import-graph-avoidance convention** (`specs/memory.md`, 2026-09-21T21:32:12Z: the `ingest.Promoter`/`main.go`'s `qdrantPromoter` pattern). `queue.Service` exposes `Register(kind string, fn HandlerFunc)` where `HandlerFunc = func(ctx context.Context, payload json.RawMessage) (result json.RawMessage, err error)`. `main.go` registers chat's, generate's, and ingest's actual handler methods after constructing all services, so `queue` never imports `chat`/`generate`/`ingest`, and those packages only import `queue` for `Enqueue`/`EnqueueAndAwait` — no reverse coupling.

**Chat.** `chat.Send(ctx, chatID, text)` persists the user message, enqueues a `chat_reply` operation (`payload: {chat_id, text}`), and returns immediately — no operation id needs to be returned to the caller, since the registered handler writes the assistant's reply directly into `chat_messages` once it runs, and the client already polls `GET /chats/{id}`.

**Generate.** `generate.Title`/`generate.Summary` keep their existing exported signatures and synchronous behavior from the caller's point of view: internally each calls `queue.EnqueueAndAwait(ctx, kind, payload)`, which enqueues then polls the operation's row until `done`/`failed` and returns the result/error, bounded by the calling request's own context (so a disconnected HTTP client doesn't leave an orphaned poll loop). The payload optionally carries `knowledge_item_id`; when present, the handler — after generating — also writes the text back into that item directly (via the same kind of adapter used for `ingest.Promoter`), in addition to returning it as the operation's `result`. This single handler covers both the manual/awaited "Generate" button (no item id, no write-back, caller reads `result`) and the import-triggered background case (item id present, write-back happens, nothing awaits `result`).

**Ingest.** Replace the per-job goroutine loop: `StartURL`/`StartText` build the job row as today, then enqueue one `ingest_chunk` operation for the first chunk (payload carries `job_id` plus whatever `processChunk` needs for that chunk). The queue's `ingest_chunk` handler runs one chunk (translate → title/summary → save draft, as `processChunk` does today), then either enqueues the next chunk's operation or calls `jobs.Finish`. Fix the URL-retry gap while restructuring: `acquireText`'s `"url"` branch must check for existing `job.SourceText` before calling `fetchURL`, and `Retry`'s dispatch must thread cached text through consistently for URL-sourced jobs the same way it already does for text/file-upload jobs (today `StartURL` never accepts a `text` parameter at all, so `Retry` unconditionally re-fetches for URL sources specifically).

**Import validation.** A new validator used only by `qdrant.Import`, distinct from the shared `validate()` that `Create`/`Update` keep using unchanged: on import, only `body` is required; `title`, `summary`, and `tags` are all optional. After import, for each item missing `title` or `summary`, enqueue a `generate_title`/`generate_summary` operation carrying that item's `knowledge_item_id` (per the Generate approach above).

**Logging.** Adopt `log/slog` with a stdout handler configured once in `main.go` (`slog.SetDefault`). Add `slog` calls at: chat/generate/translate's actual Ollama call sites (kind, model, duration, outcome — never the prompt or response text), ingest job start/finish (job id, kind, source_kind, duration, outcome — never source text or body), `qdrant.Import` (item count, duration, outcome), and knowledge item Create/Update/Delete (item id, which fields changed — not their values — outcome). This follows this project's existing security convention against logging content bodies or PII.

**Frontend is explicitly deferred.** Surfacing any of this (status-bar wording during a queued wait, chat's own optimistic message rendering, ingest job pending/active/done/failed badges, a "(generating…)" placeholder for items with a missing title/summary, and knowledge-list pagination) needs this backend contract to exist first, and is tracked as separate follow-up roadmap items rather than bundled into this one.

## Affected Areas

- `knowledge/internal/queue/` (new package: `queue.go`, `queue_test.go`)
- `knowledge/internal/db/db.go` (new `operations` table + partial unique index in the schema/migration)
- `knowledge/internal/chat/chat.go` (`Send` split into enqueue + async handler)
- `knowledge/internal/generate/generate.go` (`Title`/`Summary` routed through `queue.EnqueueAndAwait`; optional item-id write-back)
- `knowledge/internal/ingest/ingest.go` (goroutine loop replaced by queue chaining; `acquireText`/`Retry`/`StartURL` text-caching fix)
- `knowledge/internal/qdrant/qdrant.go` (new import-specific validator; `Import` behavior for optional title/summary/tags)
- `knowledge/internal/server/server.go` (handler wiring only — endpoint contracts stay the same per Acceptance Criteria 3 and 4)
- `knowledge/main.go` (construct `queue.Service`, register handlers, start the worker goroutine)
- `specs/memory.md` (record the queue-claim SQL pattern, the handler-registration adapter convention, and the URL-retry caching fix as durable gotchas/decisions once implemented)

## Out of Scope

- Frontend component extraction (login-form, signup-form, main-nav, knowledge-tab, knowledge-detail-view, chat-tab, ingest-tab, jobs-list, drafts-list as separate files/components), splitting drafts and jobs into separate workspaces, chat's own optimistic-rendering UI, the ingest tab redesign, visual separation in the knowledge aside between search and export/import blocks, job status badges (pending/active/done/failed) in the UI, and knowledge list pagination — all tracked as follow-up roadmap items after this backend piece lands.
- Making `tags` required on import (explicitly rejected by the user).
- True mid-operation preemption/cancellation of a running LLM call when an interactive operation arrives — the worker only checks priority between operations, at chunk/operation boundaries; an in-flight Ollama call is allowed to finish.
- Any change to Create/Update's validation requirements (title, summary, and body all stay required there).
- A persisted/browsable audit-log UI — the logging target for this feature is structured stdout only.

## Implementation Notes

Implemented directly on `main`, in this order:

1. **`knowledge/internal/db/db.go`**: added the `operations` table (`id, kind, priority, status, payload, result, error, created_at, updated_at`), a global `operations_one_running_idx` partial unique index, and a `operations_claim_idx` supporting index.
2. **New `knowledge/internal/queue` package** (`queue.go`): `Service` with `Register`, `Enqueue`, `EnqueueAndAwait`, `Get`, `Run` (the worker loop), and `FailStale`. `claim()` uses a single atomic `UPDATE ... WHERE id = (SELECT ... FOR UPDATE SKIP LOCKED) RETURNING ...` preferring `priority = 'interactive'` over `created_at` order.
3. **`knowledge/internal/chat/chat.go`**: `Send` now persists the user message, computes retrieval query/history/tags as a payload snapshot, enqueues `chat_reply` (interactive), and returns immediately. `HandleReply` (the new queue handler) does the retrieval + LLM call + persistence that `Send` used to do inline. `answer()`'s signature changed from taking a `Chat` to taking `[]Message` directly.
4. **`knowledge/internal/generate/generate.go`**: `Title`/`Summary` now call `queue.EnqueueAndAwait` (interactive) and keep their synchronous signatures. Added `TitleDirect`/`SummaryDirect` (bypass the queue entirely) for ingest's internal use — see the deadlock note below. Added `EnqueueBackgroundTitle`/`EnqueueBackgroundSummary` (background priority, fire-and-forget, with item-id write-back) for the import-triggered path, and `HandleTitle`/`HandleSummary` as the registered queue handlers.
5. **`knowledge/internal/translate/translate.go`**: added `slog` logging (model, duration, outcome) to `Translate`; no queue involvement (see below).
6. **`knowledge/internal/ingest/ingest.go`**: replaced the per-job goroutine (`start`/`run`/`acquireText`) with two queue kinds, `ingest_acquire` and `ingest_chunk` (both background priority). `start` now only creates the job row and enqueues `ingest_acquire`, then returns — no goroutine, no `baseTimeout`/`perChunkTimeout` deadline math (removed entirely; each LLM call already has its own bound via `shared/llm`'s client timeout). `HandleAcquire` fetches/extracts a URL source only if `job.SourceText` is still empty, then enqueues chunk 0. `HandleChunk` re-derives the chunk list by re-chunking `job.SourceText` (never storing chunk text in the operation payload) and chains to the next chunk or finishes.
7. **`knowledge/internal/jobs/jobs.go`**: added `SetSourceText`, used by `HandleAcquire` to persist fetched URL content onto the job row.
8. **`Retry` fix (the URL-retry caching bug)**: `Retry` now calls `s.start` directly instead of dispatching through `StartURL`/`StartText`, so a retried job's `source.text` (already cached from a prior successful fetch, via `retrySource`) survives into the new job row unconditionally — `HandleAcquire` then sees non-empty `SourceText` and skips the re-fetch. Previously `StartURL` had no `text` parameter at all, so a URL retry silently discarded any cached content.
9. **`knowledge/internal/qdrant/qdrant.go`**: added `validateImport` (body-only required) used by `Import` instead of the shared `validate`; `Create`/`Update` unchanged. On an existing id, an omitted title/summary now falls back to the existing item's value instead of blanking it (a safety refinement beyond the literal spec wording, to avoid an import silently erasing an existing title). Added `BackgroundGenerator` interface + `SetGenerator`, and `enqueueMissingFields`, called after a successful upsert. Added `SetTitle`/`SetSummary` (satisfying `generate.ItemWriteback` directly — no adapter type needed, since `generate` defines the interface and `*qdrant.Client` structurally satisfies it). Added `slog` logging to `Create`/`Update`/`Delete`/`Import` (ids and changed-field names only, never content).
10. **`knowledge/internal/server/server.go`**: `sendMessageResponse` changed from `{assistant_message, sources}` to `{user_message}`, matching `chat.Send`'s new immediate-return contract.
11. **`knowledge/main.go`**: added `slog.SetDefault` (JSON handler to stdout), constructed `queue.Service`, called `queueSvc.FailStale` before anything else touches the queue, wired `kb`/`generateSvc` as each other's interface dependencies (no adapter types needed — see note below), registered all five handler kinds, and started the worker via `go queueSvc.Run(ctx)`.
12. **`knowledge/internal/server/static/app.js`**: minimal compatibility fix to `sendChat()` so the app keeps working end-to-end now that the reply is asynchronous — it polls (up to 30s) for the reply to appear after sending, rather than assuming the old response already contained it. Full status-bar/optimistic-render UI is deliberately deferred to the `knowledge-async-ui-wiring` roadmap item, per Out of Scope.

**Design deviation from the approved Approach, with reason:** the Approach's wording ("StartURL/StartText build the job row then enqueue one ingest_chunk operation for the first chunk") was refined during implementation into two queue kinds (`ingest_acquire` then `ingest_chunk`), not one. A single `ingest_chunk` kind would have required the URL fetch to happen synchronously inside `start` (blocking the HTTP request on network I/O) to have text ready before enqueuing — which would have reintroduced exactly the "don't block the caller" problem this feature exists to fix. Splitting acquisition into its own background-priority operation preserves `start`'s instant return while keeping the fetch inside the same single-worker queue.

**Real bug found and fixed during live validation (not present in the approved design, an implementation gap):** `queue.Service` had no equivalent of `jobs.Service.FailStale`. A process restart (observed live: a transient Qdrant connectivity hiccup during deploy caused a few crash-loop restarts) leaves a claimed operation's row stuck at `status='running'` forever, and since `operations_one_running_idx` is a *global* single-row constraint (not per-kind like jobs), that orphaned row permanently blocks every future claim — confirmed live via repeated `operations_one_running_idx` unique-violation errors in the pod logs after a restart. Fixed by adding `queue.Service.FailStale`, called in `main.go` immediately after constructing `queueSvc`, before any handler registration or `Run`. Redeployed and confirmed the orphaned row was correctly marked `failed` and the queue resumed processing normally.

**Deadlock avoided by design, not discovered live:** ingest's chunk pipeline calls `generate.TitleDirect`/`SummaryDirect`, not the queued `Title`/`Summary` — an `ingest_chunk` operation handler already runs inside the queue's one worker slot, so calling `EnqueueAndAwait` from within it would wait on an operation the same (single, busy) worker could never reach. `translate.Translate` is likewise always called directly, never through the queue.

## Validation

- `go build ./...`, `go vet ./...`: clean throughout every step.
- `go test ./...`: all pre-existing tests continue to pass (`internal/config`, `internal/ingest`, `internal/translate`), plus 6 new tests added in `internal/qdrant/qdrant_test.go` covering the import-validation relaxation (body-only required; empty body still rejected; an existing item's title is preserved when omitted on reimport; `Create` still requires title+summary+body, unchanged; background generate is enqueued exactly when title/summary is missing, and skipped when both are provided).
- Live end-to-end validation against the real deployed app (curl against `http://knowledge.localhost:8080`, real Postgres, real Qdrant, real Ollama — no mocks):
  - Sent a chat message: response returned immediately with only `user_message` (no LLM wait, confirming AC 3); the assistant reply appeared ~20s later via `GET /chats/{id}` polling, with sources attached, confirming AC 3 and AC 9 (durable, not tied to the original request).
  - Manual `generate/title` and `generate/summary`: confirmed both keep their synchronous contract, returning generated text directly in the HTTP response once Ollama had warmed up (confirming AC 4).
  - Ingest via `StartText`: job completed successfully end-to-end through the new `ingest_acquire`/`ingest_chunk` queue chain (confirmed via `GET /jobs`, status `done`).
  - Import with only `body` (no title/summary/tags): succeeded (`{"imported":1,...,"errors":[]}`), confirming AC 8's core relaxation.
  - Found and fixed the `FailStale` gap above via a real crash-restart that occurred during deployment — not a synthetic test.
  - Per explicit user instruction mid-session, further LLM-triggering live testing (ingest via URL/retry-caching re-verification, and waiting out the background-generate fill-in after import) was stopped short and **not completed** — see below.

**Explicitly not yet live-verified** (build/vet/test all pass; logic was implemented and code-reviewed, but the live round-trip was not exercised before testing was cut short):
- The URL-retry caching fix itself (`Retry` → `start` → `HandleAcquire` skip-fetch-when-cached path) was not exercised live end-to-end (only via reasoning about the code path).
- The import-triggered background generate actually completing and writing a title/summary back into the item (the enqueue side was confirmed; the fill-in completing was not watched to completion).
- `slog` JSON output was seen live only for `chat_reply`/`queue operation completed`/migrate lines during this session; the ingest job start/finish, translate, and qdrant Create/Update/Delete/Import log lines were added but not individually eyeballed in the live pod logs.

## Documentation Review

Checked `knowledge/README.md`, `CHANGELOG.md`, `specs/roadmap.md`, and `specs/tech-stack.md` against the actual shipped behavior. Found real drift, all fixed (see Documentation Updates):

- README's "Chat with knowledge" section still described the old synchronous contract (assistant reply in the same response) — now wrong per this feature's AC 3.
- README's "Bulk import" section still said title/summary/body were all required — now wrong per AC 8.
- README's "Job history, retry, and delete" section said a URL retry "re-fetches a URL fresh" — this is the exact bug this feature fixed, so the doc was describing a bug as intended behavior.
- No README section documented the queue/priority/logging model at all — added one ("Background operation queue").
- `specs/tech-stack.md`'s "Key conventions" had no record of the new background-queue pattern or the structured-logging convention, even though both are meant to apply to any future LLM-calling code path, not just this feature. Added, with explicit human approval (AskUserQuestion, this session).
- `specs/roadmap.md`'s `## Now` line for this feature was stale once the CHANGELOG entry was added — removed per the constitution-format skill's Now-line lifecycle rule (removed at delivery, not carried forward).

No drift found in `mission.md` or `specs/memory.md` from this feature specifically (memory entries for this feature were not yet added — see note below).

## Documentation Updates

- `knowledge/README.md`: rewrote the chat-send description (async, points at polling `GET /chats/{id}`), rewrote the bulk-import requirements (body-only required, background-fill behavior), fixed the retry description (reuses cached content, doesn't always re-fetch), and added a new "Background operation queue" section explaining the priority model, the durability guarantee, and the logging convention.
- `CHANGELOG.md`: added `[Unreleased]` entries for the queue itself, relaxed import validation, pagination, job status badges, the aside divider, structured logging, and the URL-retry fix (done in an earlier turn this session, confirmed still accurate here).
- `specs/tech-stack.md`: added "Background operation queue" and "Structured logging" to Key Conventions (version 3 → 4), approved by the user this session.
- `specs/roadmap.md`: removed this feature's `## Now` line (version 21 → 22); `knowledge-list-pagination` was removed earlier this session as shipped, and `knowledge-workspace-redesign`'s description was narrowed to what's still actually outstanding (component extraction, split jobs/drafts workspaces, ingest tab redesign) after the aside-divider and job-status-badge parts of it shipped.
- `specs/memory.md`: this was initially skipped during B2 under time pressure — caught during this doc review and fixed by appending three entries: the `operations_one_running_idx`-needs-`FailStale` gotcha, the queue-handler-reentrancy deadlock rule behind `TitleDirect`/`SummaryDirect`, and the `ingest_acquire`/`ingest_chunk` two-kind-split convention.
