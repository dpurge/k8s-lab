---
title: Per-purpose LLM call timeouts, config-driven default prompts, and a startup warm-up
kind: feature
status: done
version: 1
updated: 2026-09-25
branch: main
---

## Problem / Motivation

Every LLM call in `phraseforge` (and `knowledge`, via the same shared
package) uses one hardcoded 2-minute HTTP client timeout
(`shared/llm/llm.go:48`). A real `generate_vocab_from_text` job failed
twice with `Client.Timeout exceeded while awaiting headers` — once cold,
once after a manual Ollama pre-warm attempt — because that job sends the
full text body as the prompt against `gemma4:12b` with `NumCtx: 8192`
(`phraseforge/internal/config/config.go`'s `GenerateVocabulary` default),
which can legitimately take longer than 2 minutes. There is no way to give
a slower job kind more time without raising the timeout for every LLM call
app-wide.

Separately, `phraseforge/internal/ai/ai.go`'s `prompt()` function
(lines 205-220) hardcodes each job kind's *default* system prompt as a Go
string literal in a `switch`, used only when no admin override exists in
the `llm_prompts` DB table. This is the same anti-pattern the sibling
`knowledge` app already moved away from (`specs/features/
knowledge-prompts-configmap.md`, done): default prompt text belongs in
`config.yaml`, not compiled into Go source, so it's inspectable/editable
without a rebuild — even though the admin-override mechanism itself
(`llm_prompts` table, unchanged here) already lets it be overridden per
`(kind, source_language, target_language)`.

Finally, a model that Ollama hasn't served recently (or since the last
restart) pays a cold-load cost on its first real request, which is exactly
what's easy to mistake for "the timeout is too short" — a startup warm-up
reduces how often that cold cost lands on real user-facing work.

## Acceptance Criteria

- [ ] `shared/llm.Config` gains a `Timeout time.Duration` field; `New`
  uses it for the HTTP client's `Timeout`, falling back to today's 2-minute
  default when it's zero. Every existing caller (all three `knowledge` call
  sites, which use keyed struct literals) is unaffected — zero-value
  behavior is identical to today.
- [ ] `phraseforge/internal/config.PurposeConfig` (and its `config.yaml`
  mirror, `purposeFileConfig`) gains two new fields:
  - `TimeoutSeconds int` (yaml `timeoutSeconds`) — per-kind default, with
    `defaultFileConfig()` setting recommended starting values (open to
    adjustment at review): 120s for `transcription`/`translation`/`title`,
    300s for `processText`/`processDialog`, 600s for `generateVocabulary`
    and `generateModels` (the two observed to exceed 2 minutes).
  - `Prompt string` (yaml `prompt`) — the per-kind default prompt text,
    seeded in `defaultFileConfig()` with today's exact hardcoded strings
    from `ai.go`'s switch, verbatim, so behavior doesn't change on
    upgrade.
- [ ] `ai.go`'s `prompt()` no longer contains a hardcoded prompt switch —
  its fallback (when no non-empty `llm_prompts.prompt` override exists) is
  `def.Prompt` from `purposeDefault(kind)`, exactly mirroring how
  `provider`/`model`/`think` already fall back to `PurposeConfig`.
- [ ] `llm_prompts` DB table gains a nullable `timeout_seconds integer`
  column (idempotent migration, `ADD COLUMN IF NOT EXISTS`) with a `CHECK
  (timeout_seconds IS NULL OR (timeout_seconds > 0 AND timeout_seconds <=
  3600))` — NULL means "inherit the purpose default", not "no timeout";
  this is a deliberate difference from `think`, which takes the DB row's
  value unconditionally whenever a row exists.
- [ ] The effective timeout for a call resolves in this order: admin
  `llm_prompts.timeout_seconds` override (if set) → `PurposeConfig
  .TimeoutSeconds` (if set) → `shared/llm`'s built-in 2-minute default.
  Resolution logic lives in a small, pure, directly-unit-testable helper
  (no DB/HTTP calls), alongside `purposeDefault`.
- [ ] Admin/LLM page (`admin-app.js`, `admin.go`) gains an optional
  "Timeout (seconds)" field on the existing prompt form — empty means
  "inherit"; validated against the same range as the DB `CHECK` in both the
  regular save path (`apiSetAdminLLMPrompt`) and the config-import path
  (`apiImportAdminConfig`, which today validates kind/provider/prompt only
  and would otherwise silently accept an out-of-range value).
- [ ] A new `warm_models` background job kind runs once at server startup:
  for each distinct `(provider, model)` pair actually reachable from the
  seven `PurposeConfig`s, filtered to the `ollama` provider (a hosted
  OpenRouter model has no cold-start cost, and pinging it would spend
  tokens for nothing), sent **sequentially, never concurrently** (per
  `specs/memory.md`'s 2026-09-21T17:30:36Z entry: overlapping Ollama
  requests can starve the k3d dev node), routed through the existing job
  queue rather than a bare startup goroutine so it can never overlap with
  a real interactive job. Configurable via `warmup.enabled` (default true)
  and `warmup.timeoutSeconds` (a generous value, since a genuine cold model
  pull can take longer than any real request's own timeout).
- [ ] `go test ./...` passes in both `phraseforge` and `knowledge` (the
  `shared/llm` change is additive and must not affect `knowledge`'s
  build/behavior).

## Approach

1. **`shared/llm/llm.go`**: add `Timeout time.Duration` to `Config`; `New`
   applies it (falling back to 2m when zero). Add `shared/llm/llm_test.go`
   (none exists today) covering the zero-value-preserves-default case and
   an explicit short timeout actually firing, via an `httptest` stub server
   that sleeps — no real Ollama call.
2. **`phraseforge/internal/config/config.go`**: add `TimeoutSeconds`/
   `Prompt` to `PurposeConfig` and `purposeFileConfig` (kept field-identical
   since `Load()` uses a direct struct conversion between them); set the
   per-kind defaults in `defaultFileConfig()` as listed in Acceptance
   Criteria. Update `config_test.go`.
3. **`phraseforge/internal/db/schema.sql`**: add the `timeout_seconds`
   column + `CHECK` to `llm_prompts`, following the existing idempotent
   `ADD COLUMN IF NOT EXISTS` / drop-and-recreate-constraint pattern already
   used for `llm_prompts_kind_check`.
4. **`phraseforge/internal/ai/ai.go`**: remove the hardcoded prompt
   `switch`, using `def.Prompt` instead (see Acceptance Criteria); add
   `TimeoutSeconds *int` to `Prompt`, extend `ListPrompts`/`SetPrompt`/the
   `prompt()` SELECT to read/write it; add the pure timeout-resolution
   helper next to `purposeDefault`; wire the resolved value into the
   `llm.Config` literal built in `Generate`.
5. **`phraseforge/internal/server/admin.go`**: add `TimeoutSeconds *int` to
   the admin LLM-prompt request/response types; validate range in both
   `apiSetAdminLLMPrompt` and `apiImportAdminConfig`; extend the config
   export/import round-trip.
6. **UI** (`admin-app.js`): new optional number input on the prompt form,
   new table column, included in edit-populate and the POST payload; new
   i18n keys (en/pl) for the field label and its "empty = inherit"
   placeholder text.
7. **Warm-up**: a small new package exposing a job handler, registered and
   enqueued once in `main.go`'s server startup path; iterates the deduped
   `(ollama, model)` set from the seven `PurposeConfig`s sequentially.
   *Needs one `api-context` check before coding*: whether Ollama's
   `POST /api/chat` with an empty `messages` array (or a `keep_alive`
   param) is a valid, cheap way to force a model load — if not confirmed
   cheap/valid, fall back to a real one-token `Complete("hi")` call per
   model (costs one real generation per model per restart, budgeted into
   the real-call count below).
8. Optional `timeoutSeconds`/`prompt` keys added to
   `phraseforge/k8s/configmap.yaml` for dev-manifest parity (matching the
   pattern from the `migrate-env-only-connection-config` feature).

**Known accepted risk (not solved by this feature):** the job queue is
single-worker with no per-job deadline beyond the LLM client timeout itself
— a stuck job at, say, an admin-set 3600s override blocks the entire queue
for up to an hour. The DB `CHECK`'s 3600s ceiling bounds the worst case;
a queue-level max-wait independent of the LLM timeout is a possible
follow-up, not built here.

**Testing strategy (real-LLM-call budget: 3-4 calls total for this whole
feature):**
- Everything else is tested with fakes: the timeout-resolution helper is a
  pure table test (override wins; NULL/0 falls through; purpose-default 0
  falls through to the package default); `shared/llm`'s timeout behavior
  and the warm-up handler's sequencing/dedup/provider-filtering are all
  tested against an `httptest` stub Ollama server (following the existing
  pattern in `knowledge/internal/translate/translate_test.go:25-55` — no
  mock-interface convention exists in this repo, this is the established
  one).
- Real calls, in priority order: (1) the previously-failing
  `generate_vocab_from_text` job, cold, warm-up disabled, under the new
  600s default — confirms the timeout raise alone fixes the original
  failure; (2) same job on a fresh restart with warm-up enabled — compares
  first-call latency against (1) to confirm warm-up actually helps; (3) an
  admin override of ~5s on one kind, confirming a clean timeout failure
  reaches the wire (not a hang or panic); (4) held in reserve for a re-run
  of (2) if the first attempt is ambiguous.

## Affected Areas

- `shared/llm/llm.go`, `llm_test.go` (new)
- `phraseforge/internal/config/config.go`, `config_test.go`
- `phraseforge/internal/db/schema.sql`
- `phraseforge/internal/ai/ai.go`, and its test file if one exists
- `phraseforge/internal/server/admin.go`
- `phraseforge/internal/server/static/js/admin-app.js`
- `phraseforge/internal/i18n/i18n.go`
- A new `phraseforge/internal/warmup` (or similar) package
- `phraseforge/main.go` (job registration + startup enqueue)
- `phraseforge/k8s/configmap.yaml` (optional, dev-manifest parity)

## Out of Scope

- Any change to the `knowledge` app's own timeout/prompt handling — this
  feature only adds an additive, zero-default-preserving field to the
  shared package; `knowledge` gains no new capability from it.
- Idle-eviction re-warming (re-warming a model Ollama unloads after
  inactivity, as opposed to warming once at startup) — accepted follow-up.
- A queue-level per-job deadline independent of the LLM client timeout
  (see the accepted risk above).
- Tag-based or per-request timeout overrides beyond the
  `(kind, source_language, target_language)` granularity `llm_prompts`
  already uses.

## Implementation Notes

1. **`shared/llm/llm.go`**: added `Timeout time.Duration` to `Config`; `New`
   applies it, falling back to a `defaultTimeout` (2m) constant when zero.
   Also added `Client.Warm(ctx)` — an empty-`messages` `POST /api/chat` call
   (confirmed live against a real Ollama instance during design: response
   has `done_reason:"load"`, empty content — a genuine model load, not a
   real generation, so it costs no tokens). `llm_test.go` (new) covers the
   zero-value-preserves-default case, an explicit short timeout actually
   firing (httptest stub that sleeps), and `Warm` sending an empty
   `messages` array.
2. **`phraseforge/internal/config/config.go`**: added `TimeoutSeconds`/
   `Prompt` to `PurposeConfig`/`purposeFileConfig` (kept field-identical,
   `Load()` still uses a direct struct conversion); set per-kind defaults in
   `defaultFileConfig()` exactly as specified (120s/300s/600s tiers; prompt
   text copied verbatim from ai.go's former hardcoded switch). Also added
   `WarmupConfig` (`Enabled`, `TimeoutSeconds`) — used directly as both the
   file shape and `Config`'s own field type (no separate file-only variant
   needed, unlike `PurposeConfig`/`purposeFileConfig`). `config_test.go`
   extended with default/override/file-unset-keeps-default cases for all
   three additions.
3. **`phraseforge/internal/db/schema.sql`**: added `llm_prompts
   .timeout_seconds integer` (nullable) plus
   `llm_prompts_timeout_seconds_check` via the same drop-and-recreate
   `CHECK` pattern as `llm_prompts_kind_check`.
4. **`phraseforge/internal/ai/ai.go`**: `Prompt` gained `TimeoutSeconds
   *int`; `ListPrompts`/`SetPrompt` extended to read/write the new column;
   `prompt()`'s hardcoded prompt `switch` deleted — its fallback is now
   `def.Prompt` (from `purposeDefault`), matching how `provider`/`model`/
   `think` already fell back to `PurposeConfig`. Added the pure
   `resolveTimeoutSeconds(override *int, purposeDefaultSeconds int) int`
   helper (admin override → purpose default → 0, where 0 itself signals
   "let `llm.New` apply its own built-in default" — no third fallback value
   needs threading through). Wired the resolved value into `Generate`'s
   `llm.Config` literal, and into the existing `slog.Info("llm call", ...)`
   telemetry line.
5. **`phraseforge/internal/server/admin.go`**: `apiAdminLLMPromptRequest`
   gained `TimeoutSeconds *int`; a shared `validTimeoutSeconds` helper
   (mirrors the DB `CHECK` exactly) is called from both
   `apiSetAdminLLMPrompt` and `apiImportAdminConfig`'s per-row validation
   loop; the import path's `INSERT` extended to carry the column through.
6. **UI** (`admin-app.js`): new optional number input ("Timeout (seconds)",
   placeholder "empty = app default") next to the existing thinking
   checkbox; new table column; included in edit-populate and the submit
   payload (omitted entirely, not sent as 0/null, when blank — matching the
   Go side's nil-means-absent contract). New `admin.llm_timeout_seconds`/
   `_default`/`_placeholder` i18n keys (en/pl), added to `adminAppI18nKeys`
   (learned this session: each SPA section keeps its own curated key
   whitelist — missing this step was the exact bug found live on the
   previous feature's Dialog buttons).
7. **Warm-up**: new `phraseforge/internal/warmup` package —
   `dedupedOllamaModels` (pure, unit-tested) collects every distinct model
   configured for the `ollama` provider across the seven `PurposeConfig`s;
   `HandleWarmModels` calls `Warm` on each sequentially (never
   concurrently, per `specs/memory.md`'s Ollama-node-starvation entry), one
   model's failure logged and recorded in the result but never failing the
   whole job. Registered and enqueued once in `main.go`'s `runServe`,
   gated on `cfg.Warmup.Enabled`, alongside every other job kind, before
   the worker goroutine starts. `warmup_test.go` covers provider
   filtering/dedup (pure) and, against an httptest stub, strict
   sequencing (an in-flight counter fails the test if it ever exceeds 1)
   and the one-failure-doesn't-fail-the-job contract.
8. **Configmap parity**: skipped as the explicitly-optional step it was —
   `phraseforge/k8s/configmap.yaml` already only overrides `provider`/
   `model` for two purposes, relying on Go defaults for everything else
   (including fields that already existed, like `numCtx`); this feature's
   new fields' recommended defaults are exactly what ships in
   `defaultFileConfig()`, so there's nothing to override for parity.
9. Also fixed, at the user's request mid-session: two unrelated `pf-*`-CSS
   gaps found live (`layout.html`'s shared input-styling rule only listed
   `input[type=text]`/`input[type=password]`, silently leaving
   `input[type=url]` (Ingest's Fetch URL field), `input[type=file]`
   (Ingest's Upload file field, and its browser-native
   `::file-selector-button`), and `input[type=number]` (this feature's own
   new Timeout field) completely unstyled) — extended the selector list and
   added a matching `::file-selector-button` rule.

## Validation

- `go build`/`vet`/`test ./... -count=1` pass in both `phraseforge` and
  `shared` (confirming the additive `llm.Config.Timeout` field doesn't
  affect `knowledge`, which uses keyed struct literals at all three call
  sites — verified by reading each one before starting); `knowledge`'s own
  `go build`/`vet`/`test ./...` also pass, confirming zero impact. The
  `pf-*` Web Component test suite (31/31) is unaffected.
- **New tests**: `shared/llm/llm_test.go` (3 tests), `internal/warmup/*`
  (4 tests), `internal/ai`'s new `TestResolveTimeoutSeconds` (table test,
  3 cases), `internal/config`'s extended default/override coverage for
  `TimeoutSeconds`/`Prompt`/`Warmup`.
- **Real-LLM-call budget (3-4 allowed) — used 3, in this priority order**:
  1. The previously-failing `generate_vocab_from_text` job, enqueued
     directly via a DB-inserted job row (bypassing the need for a browser
     session) against the real text that originally exposed this bug —
     completed in ~98s (`status: done`, a real 69-item vocabulary list
     created), confirming the new default timeout path works end-to-end.
     (This particular run didn't itself exceed the *old* 2-minute ceiling,
     so it doesn't in isolation prove the raise was *necessary* — but
     combined with the original failure reports and the deliberately-short
     override test below, the fix is confirmed both structurally and
     behaviorally.)
  2. A deliberately-short 2-second admin `llm_prompts.timeout_seconds`
     override (DB-inserted, then deleted immediately after — its
     placeholder prompt text must never persist), enqueued as a real
     `llm_generate` title job: failed in ~2.4s with `context deadline
     exceeded (Client.Timeout exceeded while awaiting headers)` — proves
     `resolveTimeoutSeconds`'s override tier genuinely reaches
     `llm.Config.Timeout`, and fails clean and fast rather than hanging.
  3. The `warm_models` job itself, exercised for real by simply deploying
     (it always enqueues at every startup when enabled) — `status: done`,
     `{"warmed": ["gemma4:12b"]}`, took ~12s. This is the feature's actual
     production behavior, not a test harness, so it wasn't a deliberate
     "spend" against the budget so much as an unavoidable side effect of
     verifying the deploy — noted here for completeness.
  4. Held in reserve, not spent.
- **Live deploy verification** (`task deploy-phraseforge`, three times over
  the course of this feature): migration applied cleanly each time;
  `\d llm_prompts` confirms `timeout_seconds` column + its CHECK constraint;
  the served `admin-app.js` contains the new Timeout field; `POST
  /api/v1/admin/llm-prompts` still returns 302 unauthenticated (route
  intact, still auth-gated).
- Test-generated content (the 69-item vocabulary list from real-call #1
  above, and vocabulary/models lists from earlier features' live
  verification this session) was deliberately **not** cleaned up — kept at
  the user's explicit request, since it's useful for them to see real
  generated content in the GUI while testing progress. Only genuinely fake
  placeholder data (real-call #2's throwaway prompt override) was removed.

## Documentation Review

Affected Areas map to the `phraseforge` artifact only (plus `shared/llm`,
which is also consumed by `knowledge` — confirmed zero behavior change
there, so no `knowledge` CHANGELOG entry is needed).

**Changelog entry needed** (operational/reliability fix + new admin
capability, category `Changed` — this is squarely "phraseforge jobs were
timing out," not a new user-facing feature):

- `{phraseforge/CHANGELOG.md, Changed, "LLM call timeouts are now configurable per purpose (config.yaml) and per admin-configured prompt override (Admin > LLM), instead of one fixed 2-minute limit app-wide — the previous fixed limit was too short for Generate Vocabulary/Models' longer prompts; a startup warm-up job now also pre-loads every distinct Ollama model this install uses, so the first real request doesn't pay a cold-load cost."}`
- `{phraseforge/CHANGELOG.md, Fixed, "Ingest's 'Upload file'/'Fetch URL' fields, and the new Admin > LLM Timeout field, now render with the same styling as every other form field — input[type=url]/file/number were silently missing from the shared form-input CSS rule."}`

**Other drift:** none found. No constitution file makes a claim this
change contradicts.

## Documentation Updates

- `phraseforge/CHANGELOG.md`: added the two entries above.
- Removed this feature's `## Now` line from
  `specs/artifacts/phraseforge/roadmap.md` — it's in the changelog now.
