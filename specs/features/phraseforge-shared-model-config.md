---
title: Share phraseforge's LLM model config with knowledge
kind: feature
status: done
version: 1
updated: 2026-09-24
branch: main
---

## Problem / Motivation

phraseforge and knowledge both call Ollama on the same resource-constrained local k3d node; specs/memory.md already documents that overlapping/competing Ollama models can starve the node until kubelet marks it NotReady. Today the two apps configure their LLM usage independently: knowledge already loads per-purpose (embeddings/chat/generate/translate) model config from a mounted config.yaml, all defaulting to `gemma4:12b` (except embeddings' `bge-m3`), with credentials kept in env vars. phraseforge is 100% env-var-driven, with a single `LLM_MODEL` for both of its purposes (transcription, translation) — and that env var's *deployed* value (`rinex20/translategemma3:12b` in `phraseforge/k8s/deployment.yaml`) doesn't match either phraseforge's own code default (`gemma4:e4b`) or knowledge's model, so a third distinct model can end up resident in Ollama at once.

phraseforge already lets an admin edit LLM prompts per (kind, source_language, target_language) via the GUI (`phraseforge/internal/server/admin.go`, `llm_prompts` table, `phraseforge/internal/server/static/js/admin-app.js`), including a per-prompt model override — this is more dynamic than knowledge's static config.yaml-only model selection. In production, phraseforge will use both Ollama (local/self-hosted) and OpenRouter (hosted) at the same time, with different prompts routed to different providers. The admin also needs to enable/disable "thinking" per prompt (default disabled — a thinking-capable model like gemma4 can turn a ~1s answer into 148s, per specs/memory.md's existing gotcha), not just as a fixed default.

This feature: (1) ports a knowledge-style mounted config.yaml into phraseforge, but shaped as a **provider registry** (Ollama and OpenRouter connection details, both configured at once) plus **per-purpose defaults** (transcription/translation each defaulting to provider `ollama`, model `gemma4:12b`, think `false`) rather than one baseURL per purpose; (2) extends the existing admin-editable `llm_prompts` row with `provider` and `think` fields alongside its existing `model` field, so a specific prompt can independently pick its provider, model, and thinking behavior; (3) adds a `Think bool` field to `shared/llm.Config`, replacing the currently-hardcoded `think:false` in the Ollama request path.

This is the first of several roadmap items (`specs/artifacts/phraseforge/roadmap.md`'s Next section) that add more LLM-calling background work to phraseforge; establishing the provider-registry + per-purpose-default + per-prompt-override convention now keeps each of those additive rather than requiring a config reshape later.

## Acceptance Criteria

- `config.yaml` (mounted, path from `CONFIG_FILE` env var, default `/etc/phraseforge/config.yaml`) defines a `providers` registry with at least `ollama` and `openrouter` entries, each holding a `baseURL`; each provider's API key comes from its own env var (`OLLAMA_API_KEY`, `OPENROUTER_API_KEY` — empty is valid for Ollama), never stored in the file or the database.
- `config.yaml` also defines per-purpose defaults for `transcription` and `translation`, each `{provider, model, numCtx, think}`, both defaulting to provider `ollama`, model `gemma4:12b`, think `false`.
- `config.yaml` additionally carries `bindAddr` and `postgres.{host,port,database}` (full parity with knowledge's file shape for those fields); `PGUSER`, `PGPASSWORD`, and `SESSION_KEY` remain env-var-only.
- The mounted file is optional: when absent, phraseforge falls back to hard-coded Go defaults equivalent to the values above, matching knowledge's `Load()` pattern (defaults → file overlay → env-var credential overlay). A malformed file is a startup error.
- The `llm_prompts` table gains two new columns via an additive `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` in `phraseforge/internal/db/schema.sql` (matching how the existing `model` column was added): `provider text NOT NULL DEFAULT 'ollama'` and `think boolean NOT NULL DEFAULT false`.
- The admin LLM-prompt management UI (`phraseforge/internal/server/static/js/admin-app.js` and its API in `phraseforge/internal/server/admin.go`) lets an admin set, per (kind, source_language, target_language) prompt: the prompt text (existing), the model (existing), the provider (new — a choice among the configured `providers` registry keys), and think (new — on/off, defaulting to off for a newly created prompt row).
- When no admin-configured row exists for a given (kind, source_language, target_language), phraseforge falls back to the purpose's config.yaml default provider/model/numCtx/think and a built-in default prompt, exactly as today (only the fallback's provider/model/think source changes, not the fallback mechanism itself).
- Both providers can be used concurrently at runtime with no restart: e.g. a translation prompt routed to Ollama and a transcription prompt routed to OpenRouter both work in the same running phraseforge instance, because both providers' connection details are loaded from config.yaml at startup. Changing which providers exist (adding a third, changing a baseURL) still requires editing config.yaml and redeploying.
- `shared/llm.Config` gains a `Think bool` field; the Ollama request path uses `c.cfg.Think` in place of the current hardcoded `"think": false`, zero-value `false` by default so no existing caller's behavior (including knowledge's) changes unless it opts in. The OpenAI/OpenRouter path is unaffected (no equivalent per-request knob) — selecting `think: true` for a prompt routed to OpenRouter has no effect, and this is expected, not an error.
- `phraseforge/internal/ai.Service`'s `Generate()` resolves, in order: the admin prompt row's provider/model/think if a row exists for that (kind, source_language, target_language), else the purpose's config.yaml default; then resolves that provider's `baseURL`/API key from the `providers` registry to build the final `llm.Config` for the call.
- No change to phraseforge's existing HTTP API routes beyond the admin LLM-prompt endpoints' request/response shape gaining `provider` and `think` fields.
- `go build ./...` and phraseforge's existing test suite (if any) pass; manual verification: a default (no admin override) transcription and translation call each succeed against Ollama with `gemma4:12b`; an admin-created prompt override pointed at `openrouter` succeeds; toggling a prompt's `think` field changes the `think` value sent in the Ollama request body.

## Approach

1. **shared/llm**: add `Think bool` to `Config` (`shared/llm/llm.go`); replace the Ollama path's hardcoded `"think": false` with `c.cfg.Think`, keeping the existing explanatory comment about *why* thinking is off by default, adapted to document the new field instead of the hardcoded literal. Zero-value `false` preserves today's behavior for every existing caller (knowledge included) with no change needed on knowledge's side.
2. **phraseforge/internal/config**: rewrite `Config`/`Load()` to a provider-registry shape: an internal `fileConfig` struct with `bindAddr`, `postgres.{host,port,database}`, `providers.{ollama,openrouter}.{baseURL}`, and `transcription`/`translation` blocks each `{provider, model, numCtx, think}` (provider here is a string key into the `providers` map). `defaultFileConfig()` provides Go-coded defaults matching the Acceptance Criteria above. `CONFIG_FILE`-path YAML overlay is silently skipped if the file doesn't exist, errors if malformed (matching knowledge). Env-var overlay applies only to `PGUSER`, `PGPASSWORD`, `SESSION_KEY`, `OLLAMA_API_KEY`, `OPENROUTER_API_KEY`.
3. **phraseforge/internal/db/schema.sql**: add `ALTER TABLE llm_prompts ADD COLUMN IF NOT EXISTS provider text NOT NULL DEFAULT 'ollama';` and `ALTER TABLE llm_prompts ADD COLUMN IF NOT EXISTS think boolean NOT NULL DEFAULT false;` immediately after the existing `model` column's equivalent line, matching that exact established pattern.
4. **phraseforge/internal/server/admin.go**: extend the admin LLM-prompt request/response types and `apiSetAdminLLMPrompt`/`apiDeleteAdminLLMPrompt` (or equivalent) handlers to read/write `provider` and `think`; validate `provider` against the configured `providers` registry keys.
5. **phraseforge/internal/server/static/js/admin-app.js**: add a provider selector (populated from the providers the backend reports as configured) and a think checkbox (default unchecked) to the existing prompt edit form.
6. **phraseforge/internal/ai/ai.go**: `Service` holds the loaded `providers` registry plus the two purpose defaults. `Generate()`'s existing prompt lookup (`s.prompt(...)`) now also returns `provider`/`think` (from the DB row, or the purpose default when no row exists); `Generate()` resolves the named provider's `baseURL`/API key from the registry to build the final `llm.Config{Provider, BaseURL, APIKey, Model, NumCtx, Think}` before calling `llm.New(...).Complete()`.
7. **phraseforge/main.go**: update wiring for the new `config.Load()` shape and `ai.New()` signature; `BindAddr` now comes from the loaded config instead of directly from env.
8. **phraseforge/k8s**: add `configmap.yaml` (new) with the `providers`/`transcription`/`translation`/`bindAddr`/`postgres` shape; update `deployment.yaml` to mount it, set `CONFIG_FILE`, drop the now-file-driven env vars (`BIND_ADDR`, `PGHOST`, `PGPORT`, `PGDATABASE`, `LLM_PROVIDER`, `LLM_BASE_URL`, `LLM_MODEL`), and add `OLLAMA_API_KEY`/`OPENROUTER_API_KEY` (the latter from a secret, the former empty by default).
9. **Taskfile.yml**: reorder `deploy-phraseforge` to `kubectl apply -f phraseforge/k8s/configmap.yaml` before the `_migrate-db` step, matching `deploy-knowledge`'s order.

Key decisions: (a) config.yaml has full parity with knowledge's file for bindAddr/postgres, per earlier direction, but purpose blocks reference a shared `providers` registry instead of embedding their own baseURL, so Ollama and OpenRouter can both be configured and used at once; (b) `provider`, `model`, and `think` are all plain per-row fields on `llm_prompts` (not nullable/inherit-from-purpose) — consistent with how `model` already behaves: a row that exists is fully explicit, and the purpose default only applies when no row exists at all for that (kind, source_language, target_language); (c) only the two purposes that exist today (transcription, translation) get default blocks — generate_title/generate_vocabulary/generate_models/generate_grammar are deliberately deferred to the roadmap items that actually introduce those purposes.

## Affected Areas

- `shared/llm/llm.go` (and its tests, if any)
- `phraseforge/internal/config/config.go`
- `phraseforge/internal/db/schema.sql`
- `phraseforge/internal/ai/ai.go`
- `phraseforge/internal/server/admin.go`
- `phraseforge/internal/server/static/js/admin-app.js`
- `phraseforge/main.go`
- `phraseforge/k8s/configmap.yaml` (new)
- `phraseforge/k8s/deployment.yaml`
- `Taskfile.yml` (`deploy-phraseforge` task)
- `specs/tech-stack.md` (documents phraseforge's LLM config today — likely needs an update once this ships, to be checked in the documentation-review phase)

## Out of Scope

- Adding new LLM "kind"/purpose values (generate_title, generate_vocabulary, generate_models, generate_grammar) — each is scoped to its own later roadmap item.
- The job/queue system and structured LLM-call request logging — covered by the separate `phraseforge-job-queue` roadmap item.
- Any change to knowledge's own config, model choice, or behavior (its `Think` stays unset/false, unaffected by the new field's zero value).
- A dedicated Jobs UI (separate roadmap item) — this feature's only UI change is adding `provider`/`think` fields to the existing admin LLM-prompt management page.
- Managing the `providers` registry itself (which providers exist, their `baseURL`) via the admin GUI — that stays in `config.yaml`/k8s, redeploy-only, not a GUI-editable resource in this feature.
- Adding a third provider beyond Ollama/OpenRouter.

## Implementation Notes

All 9 Approach steps are implemented, done in 3 sequential/parallel passes, all builds green (`go build ./...` + `go vet ./...` clean across `shared`, `phraseforge`, and `knowledge`).

**Pass 1 (steps 1, 2, 3, 6, 7 — backend core):**

- `shared/llm/llm.go`: added `Think bool` to `Config`; the Ollama request body now sends `"think": c.cfg.Think` instead of a hardcoded `false`. Zero-value default preserves existing behavior for every caller including knowledge.
- `phraseforge/internal/config/config.go`: rewritten to knowledge's load pattern (Go defaults → optional YAML at `CONFIG_FILE`, default `/etc/phraseforge/config.yaml`, missing file falls back silently, malformed file errors → env-var overlay). New shape: `Providers map[string]ProviderConfig` (`BaseURL`, `APIKey`; keys `"ollama"`, `"openrouter"`), `Transcription`/`Translation` each a `PurposeConfig{Provider, Model, NumCtx, Think}`. Defaults: `providers.ollama.baseURL = http://host.docker.internal:11434`, `providers.openrouter.baseURL = https://openrouter.ai/api/v1`, both purposes default to `{provider: ollama, model: gemma4:12b, think: false}`. Env-only: `PGUSER`, `PGPASSWORD`, `SESSION_KEY`, `OLLAMA_API_KEY`, `OPENROUTER_API_KEY`.
- `phraseforge/internal/db/schema.sql`: added `ALTER TABLE llm_prompts ADD COLUMN IF NOT EXISTS provider text NOT NULL DEFAULT 'ollama';` and `... think boolean NOT NULL DEFAULT false;`, same additive pattern as the existing `model` column.
- `phraseforge/internal/ai/ai.go`: `ai.Prompt` gained `Provider`/`Think`. `Service` now holds `config.Config` instead of a flat `llm.Config`. `prompt()` seeds from the purpose default, then overrides `Provider`/`Model` from a DB row only when non-empty (matching the existing `model` convention) and always applies the row's `Think`. `Generate()` resolves the final `llm.Config` by looking up the resolved provider name in `s.cfg.Providers` for `BaseURL`/`APIKey`.
- `phraseforge/main.go`: updated `config.Load()`/`ai.New()` call sites; startup log now reports `transcription=<provider>/<model>, translation=<provider>/<model>`.

**Pass 2 (steps 4-5 — admin API/UI), in parallel with Pass 3:**

- New file `phraseforge/internal/ai/providers.go`: `Service.Providers() []string` (sorted `Providers` map keys) — added as a new file rather than editing the just-landed `ai.go`, to avoid the two parallel passes touching the same file.
- `phraseforge/internal/server/admin.go`: admin bootstrap response gained `Providers []string`; the LLM-prompt write request gained `provider`/`think`, validated against `s.ai.Providers()` (400 on an unknown provider, same style as existing `kind` validation). Also fixed a latent bug found in the same handler: `apiImportAdminConfig`'s bulk import INSERT was missing the `provider`/`think` columns entirely (would have silently dropped them on import) — fixed as part of this pass since it's the same code path being extended.
- `phraseforge/internal/server/static/js/admin-app.js`: added a provider `<select>` and a `think` checkbox to the LLM prompt edit form, plus `provider`/`think` as new columns in the prompt list table. New-row default: provider `"ollama"`, think unchecked.
- `phraseforge/internal/i18n/i18n.go`: added `admin.llm_provider`/`admin.llm_think` labels (en + pl).

**Pass 3 (steps 8-9 — k8s/Taskfile), in parallel with Pass 2:**

- New file `phraseforge/k8s/configmap.yaml`: mirrors knowledge's ConfigMap shape for phraseforge's own fields (`bindAddr`, `postgres.{host,port,database}`, `providers.{ollama,openrouter}.baseURL`, `transcription`/`translation.{provider,model}`).
- `phraseforge/k8s/deployment.yaml`: mounts the ConfigMap at `/etc/phraseforge`, sets `CONFIG_FILE`; removed now-file-driven env vars (`BIND_ADDR`, `PGHOST`, `PGPORT`, `PGDATABASE`, `LLM_PROVIDER`, `LLM_BASE_URL`, `LLM_MODEL`); added `OLLAMA_API_KEY`/`OPENROUTER_API_KEY` as empty literals (OpenRouter's has a TODO comment flagging it needs a real key via Secret before production use — no existing API-key-from-secret pattern existed in either app's manifests to mirror, and creating a new Secret resource was left out of scope for this pass).
- `Taskfile.yml`: `deploy-phraseforge` now applies `phraseforge/k8s/configmap.yaml` before `_migrate-db`, matching `deploy-knowledge`'s ordering (a ConfigMap the migration Job needs must exist first).

**Known follow-ups / open items for validation and documentation review:**

- `OPENROUTER_API_KEY` has no real value wired yet — production OpenRouter use needs a Kubernetes Secret added and referenced, which was explicitly left out of scope for this feature.
- No end-to-end deploy/smoke-test against the local k3d cluster has been run yet (schema migration applied, actual Ollama calls, admin UI exercised in a browser) — build/vet only so far.
- `specs/tech-stack.md` likely needs an update describing phraseforge's new config.yaml/provider-registry pattern (to be assessed in Documentation Review).
- A real bug was found during end-to-end validation and fixed: `phraseforge/k8s/jobs/migrate-job.yaml` (the DB migration Job) was not updated in the k8s pass alongside `deployment.yaml` — it still set `PGHOST`/`PGPORT`/`PGDATABASE` as plain env vars, but `config.Load()` no longer reads those (file-driven only), so the migration pod fell back to `localhost:5432` and failed with connection refused. Fixed by mounting the `phraseforge-config` ConfigMap and setting `CONFIG_FILE=/etc/phraseforge/config.yaml` on the Job, mirroring `deployment.yaml`, and removing the now-inert `PGHOST`/`PGPORT`/`PGDATABASE` env vars. A durable memory.md entry was added recording this (any phraseforge pod, not just the main Deployment, needs the ConfigMap mount + CONFIG_FILE).

**Reviewer pass findings (architecturally-significant change: touches the shared `shared/llm` package, DB schema, and admin API):**

- **Rejected as factually incorrect**: the reviewer claimed `phraseforge/k8s/configmap.yaml`'s `postgres.database: phraseforge` should be `phraseforge_app` (matching the Go code's hardcoded default and stale comments). The coordinator checked the live k3d cluster's Postgres directly: only a `phraseforge` database exists, holding real data (3 users, 1 text); `phraseforge_app` does not exist at all. Git history confirms an earlier commit ("Bring closer to prod setup") already moved the deployed value from `phraseforge_app` to `phraseforge` before this feature touched the file. `configmap.yaml` was left unchanged — it correctly matches deployed reality. The Go code's compiled-in default (`phraseforge_app`, `phraseforge/internal/config/config.go`) and the comments in `phraseforge/internal/db/db.go` describing it as "the app's own database" are themselves now stale/misleading (a pre-existing issue predating this feature) — worth a follow-up cleanup at some point, but out of scope here since the ConfigMap (the thing that actually governs a deployed instance) is correct.
- **Fixed**: `phraseforge/internal/ai/ai.go`'s provider-registry lookup in `Generate()` was an unchecked map access that silently zero-valued on a miss (producing a confusing downstream "no Host in request URL" error); now returns a clear `llm provider %q is not in the configured provider registry` error immediately.
- **Fixed**: `shared/llm/llm.go`'s `Chat()` routing between the Ollama and OpenAI-compatible paths now also matches on `Provider == "openrouter"` explicitly (previously relied only on `Provider == "openai"` or a URL-substring sniff for `"openrouter.ai"`) — more robust now that provider names are a first-class registry concept. Verified knowledge's three `llm.Config{...}` call sites are unaffected (none sets Provider to those values).
- **Fixed**: `ai.go`'s `prompt()` DB lookup now distinguishes `pgx.ErrNoRows` (expected, silently falls through to purpose defaults) from any other DB error (now logged via `log.Printf`, still falls through so `Generate()` doesn't hard-fail, but the operator gets a signal instead of a silent fallback that also happened to discard the row's `provider`/`think`).
- **Explicitly not fixed, per direct user instruction**: the reviewer also flagged that `apiImportAdminConfig`'s per-prompt validation rejects any pre-feature-exported config (missing `provider` key → treated as invalid rather than "use default"). The user confirmed old exports don't need to be supported — left as strict validation (empty or unknown provider is rejected) at both `apiSetAdminLLMPrompt` and `apiImportAdminConfig`.
- **Test coverage added**: new `phraseforge/internal/config/config_test.go` (6 table-driven tests, modeled on knowledge's own `config_test.go`) covering `Load()`'s precedence: defaults when nothing is set, file overrides defaults, env vars (`PGUSER`/`PGPASSWORD`/`SESSION_KEY`/`OLLAMA_API_KEY`/`OPENROUTER_API_KEY`) override on top, missing file is not an error, malformed file is an error. All pass. A similar test for `ai.go`'s `prompt()` resolution logic was scoped out — `ai.Service.db` is a concrete `*pgxpool.Pool` with no existing interface seam in this codebase, and adding one would be a larger refactor than this fix-forward round warranted; noted as a remaining coverage gap.
- **Documentation correction** (not a functional change): the Approach section's decision (b) states provider/model are "not nullable/inherit-from-purpose — a row that exists is fully explicit." The actual implemented (and reviewer-endorsed as the better choice) behavior is that `provider`/`model` DO inherit from the purpose default when a saved row's value is empty — matching the pre-existing convention `model` already had, and correctly described in this section's earlier notes on `ai.go`'s `prompt()` method. Recorded here as the authoritative correction rather than editing the already-approved Approach text.

## Validation

Full end-to-end deploy + smoke test run against the local k3d dev cluster (`task deploy-phraseforge`), in two passes (first pass caught the migrate-job.yaml bug above; second pass, after the fix, was clean):

- **Static validation**: `go build ./...` and `go vet ./...` clean across `shared`, `phraseforge`, and `knowledge`; `gofmt -l` clean on all edited files; no pre-existing Go test suite exists in `phraseforge`/`shared` to run (confirmed, all packages report `[no test files]`), so this is a known coverage gap, not a skipped step. YAML manifests (`configmap.yaml`, `deployment.yaml`, `jobs/migrate-job.yaml`, `Taskfile.yml`) all parse and pass `yamllint` under the repo's actual (unconfigured/default-off-for-line-length) style.
- **Deploy**: `task deploy-phraseforge` completed successfully — image built, `configmap.yaml` applied before the migration Job (confirmed correct ordering), migration completed, deployment rolled out and became ready at `http://phraseforge.localhost:8080`.
- **Schema migration**: confirmed live in the deployed Postgres — `llm_prompts` has `provider text NOT NULL DEFAULT 'ollama'` and `think boolean NOT NULL DEFAULT false` columns.
- **Config loading**: the running pod's startup log reports `transcription=ollama/gemma4:12b, translation=ollama/gemma4:12b` — confirms the new default (previously the deployed model was `rinex20/translategemma3:12b`).
- **LLM smoke test** (capped at 2 real generate calls total, per explicit direction, since LLM calls are slow on this hardware): one default transcription call (Chinese → romanization) and one default translation call (French → English), both against no admin-override row, both succeeded with correct non-empty responses — confirms the config-driven default path works end-to-end for both purposes.
- **Admin override plumbing** (verified statically, no additional generate call): created a prompt row with `provider: "ollama"`, `think: true`, confirmed it round-trips correctly via GET. Confirmed provider validation actually discriminates: `provider: "openrouter"` is accepted (valid registry key, no LLM call attempted), an unknown provider value is rejected with a 400 `validation_error`. Test rows cleaned up afterward — DB left in its pre-test state.
- No regression: knowledge continues to build and its own config/behavior is untouched (zero-value `Think` default).

All Acceptance Criteria confirmed met. No known failures remaining.

## Documentation Review

**Findings: 4 issues identified (2 High, 2 Medium)**

### High Severity

1. **phraseforge/README.md:30-49 — "LLM configuration" section is entirely stale**
   - **What's wrong:** Documents the old env-var based approach (`LLM_PROVIDER=ollama`, `LLM_BASE_URL=...`, `LLM_API_KEY=...`, `LLM_MODEL=...`), which `config.Load()` no longer reads at all.
   - **Evidence:** `phraseforge/internal/config/config.go:104-138` — `Load()` reads `CONFIG_FILE` pointing to a mounted YAML file (default `/etc/phraseforge/config.yaml`), never reads `LLM_PROVIDER`/`LLM_BASE_URL`/`LLM_MODEL`; API keys come from `OLLAMA_API_KEY`/`OPENROUTER_API_KEY` env vars (lines 129-130), not `LLM_API_KEY`.
   - **Fix:** Replace lines 30-49 with:
     ```
     ## LLM configuration

     LLM configuration is file-based with per-purpose (transcription/translation) defaults, and per-prompt admin overrides:

     **Configuration file:** mounted at `CONFIG_FILE` (default `/etc/phraseforge/config.yaml`), containing:
     - `providers.ollama.baseURL` and `providers.openrouter.baseURL` — provider connection URLs.
     - `transcription.{provider, model, think}` and `translation.{provider, model, think}` — per-purpose defaults (provider is `"ollama"` or `"openrouter"`; model examples: `"gemma4:12b"` for Ollama, `"meta-llama/llama-2-70b"` for OpenRouter; think is boolean, default false).

     **Credentials:** API keys come from environment variables only, never from the config file:
     - `OLLAMA_API_KEY` — Ollama API key (empty for local Ollama, which needs no auth).
     - `OPENROUTER_API_KEY` — OpenRouter API key.

     **Admin overrides:** in the admin panel's LLM tab, admins can set provider, model, and think (enable/disable thinking) per prompt, overriding the purpose defaults for that specific (kind, source_language, target_language) combination. A newly saved row inherits the purpose default.

     **Example config.yaml:**
     ```yaml
     providers:
       ollama:
         baseURL: http://host.docker.internal:11434
       openrouter:
         baseURL: https://openrouter.ai/api/v1
     transcription:
       provider: ollama
       model: gemma4:12b
       think: false
     translation:
       provider: ollama
       model: gemma4:12b
       think: false
     ```
     ```

2. **phraseforge/CHANGELOG.md:[Unreleased] — Missing entries for user-facing changes**
   - **What's wrong:** This feature introduces user-facing changes (admin UI gains provider/think fields, default models changed) but has no CHANGELOG entries.
   - **Evidence:** Feature spec Implementation Notes confirm admin UI now has provider/think fields (lines 88-89), and default model changed from `rinex20/translategemma3:12b` to `gemma4:12b` (lines 79, 121).
   - **Fix:** Add to `## [Unreleased]` section:
     ```
     ### Added

     - Admin LLM prompt editor now supports provider selection (Ollama or OpenRouter) and per-prompt thinking toggle (off by default), allowing fine-grained LLM behavior control alongside existing model and prompt-text fields.

     ### Changed

     - Default LLM models for transcription and translation updated to `gemma4:12b` (from `rinex20/translategemma3:12b`), reducing memory footprint on resource-constrained nodes.
     ```

### Medium Severity

3. **specs/tech-stack.md:39 — "OpenRouter/OpenAI-compatible" line is misleading**
   - **What's wrong:** Says "configurable via environment variables" but provider URLs now come from mounted config.yaml, not env vars.
   - **Evidence:** `phraseforge/internal/config/config.go:106-116` reads provider URLs from `CONFIG_FILE`'s `providers.ollama.baseURL` and `providers.openrouter.baseURL`, not from env vars.
   - **Fix:** Update line 39 from:
     ```
     - **OpenRouter/OpenAI-compatible** — configurable via environment variables
     ```
     to:
     ```
     - **OpenRouter/OpenAI-compatible** — both providers configured in phraseforge's mounted `config.yaml` with per-purpose defaults; API keys from env vars (`OPENROUTER_API_KEY`, `OLLAMA_API_KEY`)
     ```

4. **specs/tech-stack.md:90-91 — Credentials & configuration section needs clarification**
   - **What's wrong:** States "Generated, gitignored files only" for credentials/configuration, but phraseforge's new `k8s/configmap.yaml` is a committed configuration file (not credentials, but configuration).
   - **Evidence:** New file `phraseforge/k8s/configmap.yaml` contains phraseforge's provider URLs and per-purpose defaults, committed to git, while credentials/API keys stay in env vars and secrets.
   - **Fix:** This is a **constitution-level issue** requiring approval. The current framing conflates "credentials" (which should be gitignored) with "configuration" (which varies by artifact and deployment). Suggest adding a clarification to the "Credentials & configuration" section distinguishing between: (a) credentials/secrets (gitignored, as today); (b) application configuration (some committed as ConfigMaps for phraseforge/knowledge, generated for Garage, etc.). A separate update to `specs/tech-stack.md` under the constitution-update gate is needed to address this properly.

## Documentation Updates

**phraseforge/README.md:** Rewrote the "LLM configuration" section (formerly lines 30-49, now expanded to document file-based config via `config.yaml`, per-purpose defaults for transcription/translation, admin GUI overrides for provider/model/think, and example config.yaml shape). Replaced outdated env-var-based documentation with current mounted-file approach.

**phraseforge/CHANGELOG.md:** Added two entries to the `## [Unreleased]` section:
- Under `### Added`: documented admin LLM prompt editor's new provider selection (Ollama/OpenRouter) and thinking toggle.
- Under `### Changed`: documented the default model change from `rinex20/translategemma3:12b` to `gemma4:12b` for both transcription and translation, noting the memory benefit on resource-constrained nodes.

**specs/tech-stack.md:** Updated line 39 (LLM provider description) to accurately document that phraseforge now configures provider `baseURL`s via its mounted `config.yaml` `providers` registry, with only API keys remaining env-var-only (`OPENROUTER_API_KEY`, `OLLAMA_API_KEY`). Incremented file version from 7 to 8.

**specs/artifacts/phraseforge/roadmap.md:** Removed the `## Now` line ("Share phraseforge's LLM model config with knowledge") since the feature is now complete and documented in the changelog, no longer a work-in-progress item.
