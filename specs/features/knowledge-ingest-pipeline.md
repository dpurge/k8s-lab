---
title: Ingest pipeline for URL/file → draft knowledge items
kind: feature
status: done
version: 1
updated: 2026-09-21
branch: main
---

## Problem / Motivation

Today the only way a knowledge item enters the `knowledge` app is by writing it directly (title/summary/body/tags) through the existing item-creation path, optionally with LLM-assisted title/summary generation. There is no way to pull in content from an external URL or a local file — a developer who wants to add a web page or a saved document to the knowledge base has to manually copy the text out, clean it up, and paste it in. This feature adds a backend pipeline that fetches a URL or accepts an uploaded plain text/markdown file, extracts and chunks the text, translates each chunk into the configured knowledge language if needed, generates a title and summary per chunk via the LLM, and stores each chunk as a **draft** row pending human review — never written directly into the searchable knowledge base. Reviewing, editing, approving, or discarding those drafts is the next roadmap item (`knowledge-ingest-ui`) and is explicitly not part of this feature.

## Acceptance Criteria

- `POST /api/v1/ingest/url` accepts `{"url", "tags"}`, validates the URL is `http`/`https` with a non-empty host, starts an async job via the existing `jobs.Service`, and returns `202` with the job.
- `POST /api/v1/ingest/text` accepts `{"filename", "content", "tags"}`, validates the extension (`.txt`/`.md`/`.markdown`), valid UTF-8, and size (≤ 2 MiB), starts an async job, and returns `202` with the job.
- Both endpoints return `409 job_running` if another ingest job is already running (single concurrent job, matching `jobs.Current`'s single-job design).
- The async job: fetches/reads the source; strips HTML tags when the source is a URL; chunks the resulting text (paragraph-based, ~4000-rune target, 5000-rune hard cap, 50-chunk cap, short-tail merge); for each chunk, translates via `translate.Service.Translate` if needed, generates a title and summary via `generate.Service`, and inserts one row into a new `knowledge_drafts` Postgres table; and reports progress via `jobs.SetStep` at each stage (fetch/extract/chunk, then per-chunk translate/title/summary/save) so `GET /api/v1/jobs/current` reflects live progress.
- `GET /api/v1/ingest/drafts` returns all rows currently in `knowledge_drafts` (read-only; no edit/approve/discard in this feature).
- Draft rows are never written to Qdrant and never appear in existing search/chat results — enforced structurally: the new `internal/ingest` package does not import `internal/qdrant`.
- A job that fails partway (e.g. a fetch error, an oversized source) is marked failed via `jobs.Finish(ctx, id, err)`; any draft rows already written before the failure remain (not rolled back).
- A job stuck in `running` status because of a server restart mid-run is cleared to `failed` at next server startup (`jobs.FailStale`), so it doesn't permanently block new ingests with a `409`.
- New unit tests cover chunking edge cases, HTML stripping, URL/content validation guard clauses, and job-guard clauses (empty URL, bad scheme, empty content, bad extension, invalid UTF-8) — no test depends on a live LLM or a live Postgres connection.
- `task test` (or the project's real Go test command for the `knowledge` module) passes with the new tests included, with no regression in existing tests.

## Approach

New package `knowledge/internal/ingest`, following the existing `chat`/`generate`/`translate` service-package shape (constructor takes pre-built dependencies, not raw config).

Data flow: `POST /api/v1/ingest/{url,text}` → handler validates and normalizes tags (reusing `qdrant.NormalizeTags`, already used this way in `chat.go`) → `ingest.Service.StartURL`/`StartText` → `jobs.Start(ctx, "ingest")` → `202` + job JSON immediately → a detached goroutine (using `context.WithoutCancel(r.Context())` plus a 30-minute timeout, since the goroutine must outlive the HTTP request) does: fetch or accept the source → strip HTML (URL/HTML sources only) → chunk → per chunk: translate → generate title → generate summary → insert into `knowledge_drafts` → `jobs.SetStep` at every stage → `jobs.Finish` at the end (success or first error, fail-fast — no partial rollback of already-written drafts).

Key decisions:

- **Chunking** (`chunk.go`, pure function, no dependencies): paragraph-based, target 4000 runes (~1000 tokens, sized for Ollama's 4096-token default context since `generate`/`translate` don't set `numCtx`), hard cap 5000 runes (oversized single paragraphs are pre-split at the last space/newline before the cap), 50-chunk cap (bounds a run at ~150 LLM calls), short final chunks (<500 runes) merged into the previous chunk. Counted in runes, not bytes, so non-Latin text isn't unfairly shrunk. Whitespace-only input is rejected (`ErrNoContent`).
- **HTML stripping** (`html.go`, pure function): hand-rolled tag stripper rather than adding the `golang.org/x/net/html` dependency (not currently in `go.mod`) — drops `<script>`/`<style>`/comments, maps block-level closing tags to newlines, unescapes entities, collapses whitespace. Accepted trade-off: imperfect on pathological markup, acceptable because every output is human-reviewed before becoming a real knowledge item.
- **Draft storage**: a new `knowledge_drafts` Postgres table (schema below), not Qdrant — this keeps unreviewed content structurally invisible to existing search/chat (the `internal/ingest` package imports no `qdrant` code at all, so this is enforced by the import graph, not by convention). No `status` column — presence in this table *is* the pending-review state; a status column can be added later (`ALTER TABLE ... ADD COLUMN IF NOT EXISTS`) if `knowledge-ingest-ui` needs one.
- **Async job**: kind `"ingest"` (the first real caller of `jobs.Start` in the codebase), with fine-grained `SetStep` calls so the existing `/api/v1/jobs/current` polling UI shows real movement during a long-running fetch+multi-chunk-LLM run.
- **Single concurrent job**: `409 job_running` if `jobs.Current(ctx)` is non-nil, since `jobs.Current` is single-job by design and stacking concurrent LLM-heavy runs against one Ollama instance has no benefit.
- **Stale job recovery**: new `jobs.Service.FailStale(ctx)` (one `UPDATE jobs SET status='failed', error=..., updated_at=now() WHERE status='running'`), called once at server startup, so a pod restart mid-run can't permanently pin the `409` guard.
- **Request shape**: JSON only, not multipart — the only client is the project's own JS, which will read a file as text and post a JSON string; body size enforced via `http.MaxBytesReader` before decoding.
- **No new config, no new dependency**: chunk/size/timeout constants live in the `ingest` package; `maxSourceBytes` = 2 MiB, fetch timeout 30s (matching `qdrant.New`'s client timeout).

New table:

```sql
CREATE TABLE IF NOT EXISTS knowledge_drafts (
    id UUID PRIMARY KEY,
    job_id UUID REFERENCES jobs(id) ON DELETE SET NULL,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('url', 'text')),
    source_ref TEXT NOT NULL,
    chunk_index INT NOT NULL,
    title TEXT NOT NULL,
    summary TEXT NOT NULL,
    body TEXT NOT NULL,
    tags TEXT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Ordered implementation plan (one engineer step at a time, build+test green before the next):

1. Chunking (`internal/ingest/chunk.go` + test) — pure, no dependencies.
2. HTML stripping (`internal/ingest/html.go` + test) — pure, no dependencies.
3. Schema + draft persistence — append `knowledge_drafts` to the schema const in `internal/db/db.go`; new `internal/ingest/drafts.go` (struct + insert/list). Verify DDL with a live migrate.
4. Source acquisition (`internal/ingest/source.go` + test) — URL fetch with scheme/host/content-type/size validation using `httptest.Server` in tests; text validation (extension/UTF-8/size).
5. Pipeline service (`internal/ingest/ingest.go` + test) — `Service`, `StartURL`, `StartText`, the `run()` orchestration with `SetStep`/`Finish`; tests cover guard clauses only, no live LLM.
6. HTTP + wiring — three routes/handlers in `internal/server/server.go`, dependency construction in `main.go` (`translate.New`, `ingest.New`).
7. Stale-job recovery — `jobs.Service.FailStale` in `internal/jobs/jobs.go`, called from `main.go` at startup.
8. Docs — README architecture tree + new `### Ingest` API section, `CHANGELOG.md` entry, `roadmap.md` update (handled by the /code loop's own B4/B5 phases, not this Approach section — just note it's expected).

Risks and testing strategy:

- **Security (Medium) — SSRF-shaped risk**: an authenticated user can make the server fetch cluster-internal addresses (Qdrant, Postgres, Headlamp, the k3d host) and read the response back via `/ingest/drafts`. Accepted risk: this is the feature's entire purpose (fetch arbitrary URLs), the only caller is an authenticated local developer, and `specs/mission.md` already states this is a dev tool with no production auth story. Mitigations applied: auth-gated route, http/https-only scheme check, 30s timeout, 2 MiB cap, content-type allowlist, and error messages that carry the HTTP status but never the response body (unlike the existing `llm.post` pattern) so an internal endpoint's response can't be exfiltrated via an error string. Not applying private-IP blocking, since the tool needs to reach `host.docker.internal` and in-cluster services as part of normal use.
- **Security (Medium) — resource exhaustion**: bounded by `maxSourceBytes` (2 MiB via `io.LimitReader`/`MaxBytesReader`), `maxChunks` (50), a 30-minute job timeout, and the single-concurrent-job `409`.
- **Security (Low) — prompt injection / future stored-XSS**: fetched content flows into LLM prompts (bounded impact — output lands in a human-reviewed draft) and draft bodies may contain unescaped markup (not exploitable now since the API is JSON-only; flagged for `knowledge-ingest-ui` to render with `textContent`, never `innerHTML`).
- **Correctness**: goroutine must use `context.WithoutCancel` plus its own timeout, not the request context (which is cancelled once the response is written); `FailStale` guards against a permanently stuck job after a restart; chunking edge cases (single word, no blank lines, one giant paragraph, non-Latin text) are unit tested.
- **Test plan**: hermetic unit tests only for chunk/html/source-validation/guard-clauses (no Postgres, no Ollama — CI has neither); live validation via `task deploy-knowledge`, a real `POST /ingest/url`, watching `/jobs/current` progress, checking `/ingest/drafts` output, a `POST /ingest/text` run, a `409` check during a running job, a bad-scheme `400` check, and a restart-mid-run check that `FailStale` clears the stale row.

## Affected Areas

New:
- `knowledge/internal/ingest/chunk.go`, `chunk_test.go`
- `knowledge/internal/ingest/html.go`, `html_test.go`
- `knowledge/internal/ingest/source.go`, `source_test.go`
- `knowledge/internal/ingest/drafts.go`
- `knowledge/internal/ingest/ingest.go`, `ingest_test.go`

Modified:
- `knowledge/internal/db/db.go` (schema const)
- `knowledge/internal/jobs/jobs.go` (`FailStale`)
- `knowledge/internal/server/server.go` (routes, handlers, types)
- `knowledge/main.go` (wire `translate` + `ingest`, call `FailStale`)
- `knowledge/README.md`, `CHANGELOG.md`, `specs/roadmap.md` (documentation phases)

Explicitly untouched: `internal/qdrant`, `internal/chat`, `internal/embeddings`, `internal/auth`, `internal/generate`, `internal/translate`, `internal/config`, `internal/server/static/*`, `knowledge/k8s/*`.

## Out of Scope

- Promotion of a draft into a real Qdrant knowledge item (a later feature).
- Review/approve/discard UI and endpoints, and any change to `internal/server/static/*` (`knowledge-ingest-ui`, the next roadmap item).
- PDF, DOCX, or any binary/non-text source format.
- Re-ingestion detection, deduplication, or content hashing — ingesting the same URL twice produces a second, independent set of drafts.
- Concurrent ingest jobs or job history — one job at a time, enforced by `409`.
- Embeddings during ingest — drafts are never embedded; embedding happens only at promotion time, later.
- Config-file knobs for chunk size, fetch timeout, or size caps — package constants for this slice.
- Retries, resumability, or exactly-once semantics — the job fails fast and any drafts already written stay.
- Crawling, following in-page links, or multi-page fetch — exactly one URL per request.

## Implementation Notes

All 7 planned implementation steps completed in order.

**Built steps:**

1. **Chunking** — `knowledge/internal/ingest/chunk.go` + `chunk_test.go` (10 tests). Paragraph-based chunking with ~4000-rune target, 5000-rune hard cap, 50-chunk cap, short-tail merge (final chunks <500 runes merged into previous). Counted in runes for fair handling of non-Latin text. Rejects whitespace-only input (`ErrNoContent`).

2. **HTML stripping** — `knowledge/internal/ingest/html.go` + `html_test.go` (9 tests). Hand-rolled stdlib-only tag scanner: drops `<script>`/`<style>`/comments, maps block-level closing tags to newlines, unescapes entities, collapses whitespace. No new dependency (`golang.org/x/net/html` not added).

3. **Schema + draft persistence** — `knowledge_drafts` table appended to `knowledge/internal/db/db.go`'s schema const. New `knowledge/internal/ingest/drafts.go` with `Draft` struct (snake_case JSON tags: `job_id`, `source_kind`, `source_ref`, `chunk_index`, `created_at`, `updated_at`), and functions `createDraft`, `listDrafts`.

4. **Source acquisition** — `knowledge/internal/ingest/source.go` + `source_test.go` (13 tests). `fetchURL(ctx, url)` with synchronous scheme/host validation (http/https only, non-empty host), content-type allowlist, 2 MiB size cap via `io.LimitReader`, 30s timeout. `validateText(filename, content)` validates extension (`.txt`/`.md`/`.markdown`), UTF-8 (via `utf8.ValidateString`), and size.

5. **Pipeline service** — `knowledge/internal/ingest/ingest.go` + `ingest_test.go`. `Service` struct (holds `jobs.Service`, `translate.Service`, `generate.Service`, `db.Service`). Methods: `StartURL(ctx, url, tags)`, `StartText(ctx, filename, content, tags)`, `ListDrafts(ctx)`. Private `run(ctx, id, kind, source)` orchestrates fetch/extract/chunk → per-chunk translate/generate title/generate summary/save with `jobs.SetStep` progress reporting at every stage.

6. **HTTP + wiring** — Three routes added to `knowledge/internal/server/server.go`'s authenticated `/api/v1` group: `POST /api/v1/ingest/url` (payload size cap 64 KiB via `http.MaxBytesReader`), `POST /api/v1/ingest/text` (payload size cap 2.064 MiB to allow headroom over the 2 MiB content limit; `decode` returns `413 payload_too_large` for `*http.MaxBytesError` instead of generic `400`), `GET /api/v1/ingest/drafts` (defensive `LIMIT 500`). Dependency construction in `knowledge/main.go`: `translate.New` (previously built but never wired — first real caller) and `ingest.New`.

7. **Stale-job recovery** — `jobs.Service.FailStale(ctx)` in `knowledge/internal/jobs/jobs.go`: one `UPDATE jobs SET status='failed', error='...' WHERE status='running'`. Called once at server startup in `main.go` (gated on `!migrateOnly`).

**Deviation from approved Approach (implementation fidelity correction, not design change):**

`StartURL`'s scheme/host validation was moved to run synchronously (before the job starts), not deferred into the goroutine. This was necessary to satisfy the Approach's own HTTP-shape decision: a bad scheme or missing host must be a synchronous `400`, not an async job failure visible only via `/api/v1/jobs/current`.

**Security review and fixes:**

A dedicated security-review pass (per the `security-review` skill) was run against the completed `internal/ingest` package and its integration points before validation. The spec's already-accepted risks (SSRF-shaped, resource exhaustion, prompt injection, future stored-XSS) are mitigated as designed. The review surfaced 8 findings beyond what the spec's risk analysis anticipated. Six were fixed in this implementation:

- **High — permanent job-guard pinning on timeout**: A run that hit its own 30-minute timeout used the same (now-expired) context to call `jobs.Finish`, so it could never record its own failure, permanently pinning the single-job guard until a pod restart. Fixed: a `finish` helper in `ingest.go` now derives a fresh 10-second context via `context.WithoutCancel` + `context.WithTimeout` for the terminal write, logging if even that fails.

- **Medium — missing request-body size limit on `/ingest/url`**: Unlike `/ingest/text`, `POST /api/v1/ingest/url` had no request-body size limit. Fixed: `http.MaxBytesReader` added (64 KiB cap — a URL plus tags needs kilobytes, not megabytes).

- **Medium — TOCTOU race in single-job guard**: Two near-simultaneous requests could both pass the `Current()` pre-check and both start a job. Fixed: a Postgres partial unique index (`jobs_one_running_idx ON jobs (kind) WHERE status = 'running'`) now enforces this at the DB layer, permitting one running job per kind. `jobs.Start` translates the resulting unique-violation (`*pgconn.PgError` with Code "23505") into `jobs.ErrAlreadyRunning`, which `ingest.Service.start` maps to the existing `ErrJobRunning` so callers see one consistent error regardless of which guard caught the race.

- **Medium — misleading boundary enforcement on `/ingest/text` size cap**: The 2 MiB cap was applied to the whole JSON envelope (including escaping overhead), making the documented content limit unreachable at the boundary and surfaced as a misleading `400 invalid_json`. Fixed: the reader now has 64 KiB of headroom over the content limit (so `validateText`'s check remains the real boundary), and `decode` now returns `413 payload_too_large` for a `*http.MaxBytesError` instead of the generic `400`.

- **Low — unbounded Content-Type reflection**: A fetched server's raw `Content-Type` header value was reflected unbounded into a persisted, UI-visible job error. Fixed: truncated to 64 characters before inclusion.

- **Low — unbounded draft listing**: `GET /api/v1/ingest/drafts` had no limit and would grow unbounded (no delete path exists yet). Fixed: a defensive `LIMIT 500` (not real pagination — that belongs to the future `knowledge-ingest-ui` feature).

**Two findings deliberately NOT fixed in this pass** (recorded as known limitations):

- `shared/llm/llm.go`'s error path reads and formats an unbounded response body from the configured LLM provider into error text ultimately reaching `jobs.error`/the UI. This is shared code used by `chat`/`generate`/`translate`, outside this feature's declared Affected Areas; the LLM base URL is operator-configured, not attacker-controlled, so impact is limited to unbounded (not attacker-directed) error text. Flagged as a follow-up for whoever next touches `shared/llm`.

- The Acceptance Criteria's "valid UTF-8" check on uploaded text content (`validateText`) is unreachable via the actual HTTP path: `encoding/json` silently replaces invalid UTF-8 with U+FFFD during decode, before `validateText` ever sees the string. The check still guards any future non-HTTP/direct caller of `StartText`, and existing unit tests exercise it by calling the service directly, but no HTTP request can currently trigger the "invalid UTF-8" rejection path. Known, accepted gap: fixing it properly would mean validating raw bytes before JSON decoding (a different transport shape), which is a design change beyond this pass's scope.

**Risk-framing notes for future revisits** (informational, not defects):

- The "only an authenticated local developer can call this" trust assumption underlying the accepted SSRF risk is weaker than it reads: `POST /api/v1/auth/signup` is open, unauthenticated self-registration (pre-existing, not introduced by this feature) — anyone reaching the service can create an account and then drive the fetcher.

- `FailStale` clears every stale `running` job regardless of `kind`, not just `"ingest"`. Safe today (single replica, `ingest` is the only job producer), but would need a `WHERE kind='ingest'` scoping if either changes.

- The existing job-status UI (`knowledge/internal/server/static/index.html`) already renders job step text via `textContent`, not `innerHTML` — confirmed safe today even though a job step can embed a user-supplied URL host. This constraint must be preserved by the future `knowledge-ingest-ui` feature when it renders draft bodies (which may contain unescaped markup from stripped HTML).

**Validation summary:** All steps validated with `go build`/`go vet`/`go test`/`gofmt` after every change. Final state: all green, no regressions. See Validation section.

**Architecture review (architecture-review skill) and fixes applied:**

A dedicated architectural review of the completed `internal/ingest` package found the design fundamentally sound (file split, constructor shape, error-sentinel style, and the import-graph enforcement of "drafts never reach search" were all confirmed as correct, matching existing codebase conventions) but surfaced 9 findings about sizing, projection shape, and testability. Fixed in this pass:

- **The 30-minute run timeout was fixed before the chunk count was known**, but a max-size run (50 chunks × 3 LLM calls) could need far longer than 30 minutes against the shared LLM client's 2-minute per-call timeout — the documented maximum input was effectively unreachable. Fixed: the per-chunk-processing deadline is now derived inside `run()`, after `chunk()` returns, as `baseTimeout (5 min, covering fetch/chunk) + perChunkTimeout (8 min) * chunk count` — so the budget scales with the actual amount of work instead of being guessed upfront.
- **`ListDrafts` returned every draft's full body**, unlike the codebase's existing `qdrant.Item`/`qdrant.ListItem` list-vs-detail split. Fixed: added `DraftSummary` (all `Draft` fields except `Body`) as the list endpoint's actual return type; `Draft` (with `Body`) remains for internal use by `createDraft`. `GET /api/v1/ingest/drafts` now returns summaries, not full bodies — cheaper, and avoids a breaking response-shape change once a detail view is added later.
- **Three adjacent string parameters (`sourceKind, sourceRef, rawText`) plus `tags` were threaded positionally** through `start`/`run`/`acquireText`/`processChunk`, risking a silent swap at a call site. Fixed: introduced a `source` struct (`kind`, `ref`, `text`, `tags`) threaded through all four functions instead.
- **`knowledge_drafts` had no index on `job_id` or `created_at`**, which would force full scans once `knowledge-ingest-ui` needs "drafts from this run" or the list grows. Fixed: added `knowledge_drafts_job_id_idx` and `knowledge_drafts_created_at_idx`.
- **The single-running-job guard's partial unique index was scoped globally** (any two jobs of any kind), not to the `"ingest"` kind specifically, though the rationale (LLM contention) is ingest-specific. Fixed: rescoped to `ON jobs (kind) WHERE status = 'running'` — one running job per kind, not one running job total.
- **`%w: %s` error wrapping flattened the inner error**, making `ErrEmptyContent` unreachable via `errors.Is` through `StartText`'s wrapping. Fixed: changed to `%w: %w` (both `StartURL` and `StartText`) so the inner sentinel stays matchable.
- **Hygiene**: deduplicated URL scheme/host validation (`fetchURL` now calls the same `validateURL` `StartURL` uses, instead of repeating the checks); dropped a redundant `SetStep` call in the text-source path that immediately overwrote itself with no observable effect; unexported `Chunk`/`StripHTML` (now `chunk`/`stripHTML`) since nothing outside the package ever called them and their tests are in-package.
- **A `SetStep` progress-label write failure was treated as fatal**, same as a real translate/generate/save failure — a transient DB blip while writing a cosmetic status string could discard up to the (now larger) timeout's worth of completed LLM work. Fixed: `SetStep` failures are now logged and non-fatal; only `fetchURL`/`chunk`/`translate.Translate`/`generate.Title`/`generate.Summary`/`createDraft` failures still abort the run.

**Deferred as follow-ups, not implemented in this feature** (genuine scope expansion or touching already-shipped code outside this feature's declared Affected Areas — noted for whoever picks up related work next, not acted on here):
- Making the orchestration (`run`/`processChunk`/`acquireText`/`finish`) unit-testable would require introducing consumer-side interfaces over `*pgxpool.Pool`/`*jobs.Service`/`*translate.Service`/`*generate.Service` — a real design change beyond this pass's approved testing scope (which was guard-clauses-only, matching this codebase's existing convention of not unit-testing DB/LLM-dependent code).
- Every chunk's body is LLM-regenerated via `translate.Translate` even when the source is already in the configured knowledge language (by the approved single-call-does-both design), and no original pre-translation text is retained anywhere — so a future reviewer has nothing to diff a translated draft against. Adding a `body_original` column (cheap while the table is empty) would enable that later; not added now since it's outside this feature's Acceptance Criteria.
- `uuid()` is now independently duplicated a fourth time (`jobs.go`, `auth.go`, `chat.go`, `drafts.go`). Promoting it to a shared helper would touch already-shipped files (`auth.go`, `chat.go`) outside this feature's declared Affected Areas — a candidate for a separate, dedicated cleanup task, not this feature.

## Validation

- **Automated (this session, hermetic, no live infra):** `go build ./...`, `go vet ./...`, `go test ./...`, `gofmt -l .` — all pass, zero regressions, run repeatedly after every implementation and fix round. Pre-existing tests (`internal/config`'s 4 tests, `internal/translate`'s 1 test) all still pass unchanged. New `internal/ingest` package: 37 tests across `chunk_test.go` (10), `html_test.go` (9), `source_test.go` (13), `ingest_test.go` (5) — all pass. No test file exists for `drafts.go`'s `createDraft`/`listDrafts` or for the async `run`/`processChunk` orchestration, consistent with this codebase's existing convention of not unit-testing Postgres/LLM-dependent code (`chat.go`, `jobs.go`, `db.go`, `server.go` have no test files either).
- **Coverage gap, explicitly not a defect:** the `FailStale`/`ErrAlreadyRunning`/unique-index-violation path, and the full `run()` orchestration, have no automated test coverage — they require a live Postgres connection, which this environment doesn't have. This is the same gap the baseline already had for every other DB-dependent package in this codebase.
- **Live-infrastructure validation — required, NOT YET PERFORMED, must be done by the user against a real cluster before considering this feature fully proven:**
  1. `task deploy-knowledge` (or the project's equivalent) — confirms the `knowledge_drafts` table, its two new indexes, and the corrected `jobs_one_running_idx` all apply cleanly via the real migration path.
  2. `POST /api/v1/ingest/url` against a real page — confirms a `202` + job, then watch `GET /api/v1/jobs/current` advance through the documented steps (fetching → extracting text → chunking → per-chunk translate/title/summary/save) to completion.
  3. `GET /api/v1/ingest/drafts` — confirms it returns summaries (no `body` field) with sane titles/summaries in the configured `knowledgeLanguage`.
  4. `POST /api/v1/ingest/text` with a real markdown file.
  5. A `409 job_running` check while a job is already running, and confirm the per-kind index (`jobs (kind)` scoping) doesn't block an unrelated job kind from running concurrently, if one exists to test with.
  6. A bad-scheme URL → synchronous `400`.
  7. A restart-mid-run check that `FailStale` clears a stale `running` row.
  8. A large/slow input (approaching the 50-chunk cap) to confirm the new chunk-count-proportional timeout actually covers it in practice, not just in the constant's arithmetic.

## Documentation Review

**Summary:** 5 issues (0 High, 5 Medium, 0 Low) across 2 files.

### High

None.

### Medium

1. `knowledge/README.md:19-40` — Architecture tree missing `internal/ingest` package
   - **Why:** The new ingest package is a core part of this feature's implementation and is imported directly by `main.go` and `server.go`; developers reading the architecture overview will not discover it exists, leading to confusion about how ingestion is implemented.
   - **Evidence:** The architecture section (lines 19-40) lists 11 internal packages; `internal/ingest` with 9 files exists (verified: `ls knowledge/internal/ingest/`) but is not mentioned.
   - **Fix:** Add `├── internal/ingest/ingest.go` (and mention chunk.go, html.go, drafts.go, source.go as key modules) after the `internal/jobs/jobs.go` line in the tree, before `internal/server/server.go`.

2. `knowledge/README.md:133` — Outdated claim about translate service
   - **Why:** The comment now contradicts the actual wiring; a reader following the config docs will believe translate is not yet used, when it is core to the ingest pipeline's per-chunk translation flow.
   - **Evidence:** Feature spec line 31 and Implementation Notes line 125 confirm translate is wired and used by `ingest.Service.run()` (called at `knowledge/main.go:59`). The README comment still says "not yet wired into any feature".
   - **Fix:** Change line 133 from `# translation-specialized; not yet wired into any feature —` to `# translation-specialized; used by the ingest pipeline to translate chunks —`. Update line 134 from `# a building block for future ingestion` to nothing or just `# numCtx: 0` (removing the "future" reference since it's now present).

3. `knowledge/README.md:535-536` — Outdated claim about job creation
   - **Why:** The Job status API section states "Nothing creates real jobs yet", which is now false; this misleads readers about what the job-polling endpoint does.
   - **Evidence:** Implementation Notes (spec line 125) confirms the ingest pipeline uses `jobs.Start(ctx, "ingest")` and `jobs.SetStep` to drive the `/api/v1/jobs/current` polling. Code evidence: `knowledge/internal/server/server.go:152-154` defines three ingest routes that start async jobs; `knowledge/internal/ingest/ingest.go` implements the job orchestration.
   - **Fix:** Replace the parenthetical comment "a building block for planned ingestion work" with a statement like "used by the ingest pipeline to report fetch/chunk/translate/generate progress during long-running imports". Remove "Nothing creates real jobs yet as of this writing" and replace with "Ingest jobs (started by POST /api/v1/ingest/url or POST /api/v1/ingest/text) drive this endpoint during a fetch+process run."

4. `knowledge/README.md` — Missing API documentation for ingest endpoints
   - **Why:** The API section documents 12 knowledge/chat/generate/job endpoints but omits the three new ingest endpoints entirely; users and developers have no reference for how to call them.
   - **Evidence:** Spec Acceptance Criteria (line 16-20) and Implementation Notes (line 126) confirm three endpoints exist: `POST /api/v1/ingest/url`, `POST /api/v1/ingest/text`, `GET /api/v1/ingest/drafts`. Code: `knowledge/internal/server/server.go:152-154` routes them; `handlers at lines 435-482` implement them. The README has no section documenting their request/response shapes or error codes.
   - **Fix:** Add a new `### Ingest` section after the "Generate title/summary" section (around line 530) documenting: (a) POST /ingest/url with `url` and `tags` payload, 202 + job response, 409 if already running, 400 for invalid scheme; (b) POST /ingest/text with `filename`, `content`, `tags`, same response; (c) GET /ingest/drafts returning a list of DraftSummary objects (no `body` field). Include curl examples.

5. `knowledge/README.md` — No mention of `knowledge_drafts` table in data model section
   - **Why:** The "Data and indexing model" section describes Qdrant knowledge items but omits the new draft-staging table; readers don't understand where draft items live or how they're distinct from searchable knowledge.
   - **Evidence:** Spec Approach (lines 44-59) and Implementation Notes (line 121) describe `knowledge_drafts` table schema. Code: `knowledge/internal/db/db.go:78-92` defines the table with `knowledge_drafts_job_id_idx` and `knowledge_drafts_created_at_idx`. The README never mentions drafts.
   - **Fix:** Add a paragraph after the "One knowledge item = one Qdrant point" JSON example (around line 60) explaining that draft items pending review are stored in a separate `knowledge_drafts` Postgres table (id, job_id, source_kind, source_ref, chunk_index, title, summary, body, tags, timestamps); drafts are never embedded or searchable until promoted by a future feature (`knowledge-ingest-ui`).

### Low

None.

## Documentation Updates

### README fixes (5 findings from Documentation Review applied):

1. **Architecture tree** (`knowledge/README.md:28`) — Added `internal/ingest/ingest.go` entry with note on key modules (chunk.go, html.go, drafts.go, source.go).
2. **Translate config comment** (`knowledge/README.md:133`) — Updated from "not yet wired into any feature" to "used by the ingest pipeline to translate chunks".
3. **Job status section** (`knowledge/README.md:532-536`) — Updated from "Nothing creates real jobs yet" to "Ingest jobs ... drive this endpoint during a fetch+process run, reporting fetch/chunk/translate/generate/save progress".
4. **New "Ingest" API subsection** (`knowledge/README.md:~533-600`) — Added comprehensive documentation for three endpoints:
   - `POST /api/v1/ingest/url` with request/response shape, error codes
   - `POST /api/v1/ingest/text` with request/response shape, 2 MiB cap, extension validation, error codes
   - `GET /api/v1/ingest/drafts` with response shape showing `DraftSummary` (no `body` field)
5. **Data model: draft items** (`knowledge/README.md:~62-69`) — Added paragraph explaining `knowledge_drafts` table, its columns, and that drafts never appear in search/chat until promoted.

### CHANGELOG.md updates:

- Modified two existing entries under "### Added" to drop the "not yet used by any feature; a building block for planned ingestion work" clauses:
  - Translate entry (line 14): now says "used by the ingest pipeline to translate chunks"
  - Job status entry (line 19): now says "used by the ingest pipeline to report ... progress"
- Added new entry under "### Added" documenting the complete ingest pipeline feature.

### Roadmap update:

- Removed `## Now` line for `knowledge-ingest-pipeline` from `specs/roadmap.md` (line 9); work is done, permanent record is the CHANGELOG entry.
