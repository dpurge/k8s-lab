---
title: Model router for phraseforge
kind: feature
status: done
version: 1
updated: 2026-10-20
branch: main
---

## Problem / Motivation

Phraseforge has a static provider + model configuration per purpose, with no way to test multiple models or route by capability tags. The user wants to run a bigger model on a LAN machine alongside the in-cluster Ollama and compare their performance. This task extracts and adapts the model router from the assistant (which already has tag-based routing, fallback, and call recording) into a shared module used by both apps, then configures phraseforge to route by tags and priority, record every attempt with performance metrics, and display results in the admin.

Design decisions confirmed by the user (2026-10-20): router code extracted to `shared/router` for reuse; old `provider`+`model` keys are replaced outright by `model_tags` (breaking change in config); providers are openai-compatible (OpenRouter, OpenAI), anthropic, vertex (Gemini), and ollama; API keys read from env vars, never in YAML; performance per attempt shown in admin and exposed as Prometheus metrics; OpenRouter, Anthropic and Vertex cannot be tested from the dev cluster (fake-server tests only), Ollama tested locally with at most 1–3 real calls.

Assumptions to confirm:
- A purpose keeps `numCtx` as its chunking budget and startup validation fails if any model matching the purpose's tags has `options.num_ctx` smaller than it (so the router can never be handed a chunk too big for a fallback).
- OpenAI and Anthropic are non-streaming with an overall timeout; streaming deferred to a later task.
- Anthropic structured output is done via `output_config.format` of type json_schema (forced tool_choice is rejected with a 400 by the newest models) and cannot be verified against the real API from this dev machine (fake server only).
- Dropping `llm_prompts` columns `provider` and `model` loses existing override values; the risk and scope are flagged in AC 4 and the implementation plan.
- Breaking config change: new phraseforge image and new Helm chart repository values must deploy together; startup fails with a named message on mismatch.

## Acceptance Criteria

Configuration
1. Models are configured in a `models.yaml` file (path from `PHRASEFORGE_MODELS_FILE`, default `/etc/phraseforge/models.yaml`); startup fails naming the variable and file when missing or invalid. Providers define type (ollama, openai, anthropic, vertex) and base_url (must not contain `?` or `#`); every provider entry has a compulsory key `api_key_from` whose value may be empty string ("" — no credential), `env:NAME`, or `file:/absolute/path`, read at call time (a file is re-read on every call); a file containing a line break counts as missing. Per-provider optional `headers` (name → literal, `env:NAME`, or `file:/path`) applied after the provider's defaults; reserved names (authorization, x-api-key, content-type, content-length, host, connection, transfer-encoding) are rejected at lint with a pointer to `api_key_from`. Lint rejects a malformed `api_key_from` value or missing key, naming the provider and never echoing the value; duplicate provider or model ids, unknown provider refs, empty or blank tags, and negative priorities also rejected. Model `priority` must be 0 or more; model `tags` match the pattern `^[a-z][a-z0-9-]*$` (lowercase letters, digits and hyphens, starting with a letter). A missing credential is never fatal: a provider that requires one (anthropic, vertex) and has none is disabled — its models are skipped, a Warn is logged once per transition, the admin Models table shows `unavailable_reason` (cooldown or missing_credentials) without naming any variable or path, and calls fall back to the next model. An unresolved `env:` or `file:` header value disables the provider like a missing credential. Header names and values never appear in admin/API responses, metrics, records, or logs except the credential_source text in the transition Warn/Info. A purpose whose tags match no model stays a fatal error; a purpose whose matching models are all unusable logs a startup Warn. The Vertex token comes from `api_key_from` (earlier PHRASEFORGE_VERTEX_TOKEN_FILE and ASSISTANT_VERTEX_TOKEN_FILE environment variables are removed). Unknown keys in `config.yaml` are rejected at startup, naming the file and key. Retention days are capped at 36500 (99.9 years).
2. Purposes gain required `model_tags` (non-empty list); old `provider` and `model` keys are rejected at load with a message naming them and the replacement. Loader rejects empty or duplicate tags.
3. Per-model options (`num_ctx`, `think`, `timeout_seconds`) live on the model in `models.yaml`. A purpose keeps `num_ctx` (note: legacy PhraseForge purposes use camelCase `numCtx`; new purposes use `num_ctx`) as its chunking budget; startup validation fails if any model matching the purpose's tags has `options.num_ctx` smaller than the purpose's context window.

llm_prompts migration
4. Columns `provider` and `model` in `llm_prompts` are replaced by `model_tags text[]` (NULL means inherit the purpose's default tags); `think` and `timeout_seconds` overrides stay. Admin > LLM edits tags (chosen from the tags in models.yaml) instead of a provider list. Migration is one phase: both columns are dropped in the same release (no deprecated-column intermediate state). RISK: dropping `provider` and `model` discards existing override values. Plan step 4 was completed 2026-10-20: dev DB read-only count found 0 rows with overrides, nothing lost there; the human verifies prod read-only before the migration runs.

Routing and fallback
5. `Select(tags)` returns models that have every requested tag, highest priority first, ties broken by file order. An empty tag list matches every model. No match returns an error naming the tags and models checked.
6. A call tries the first candidate; on failure it tries the next. Errors after all fail list each model and why. A canceled context stops the chain at once. Circuit breaker marks a model unavailable for `cooldown_seconds` after `failures` consecutive failures (network, timeout, 5xx), and `rate_limited_cooldown_seconds` after 429. A success resets the count. A connection failure (the provider's machine is down) makes every model of that provider unavailable at once.

Performance recording and admin
7. Table `llm_calls` records one row per attempt: id, job_id (nullable), kind, source_language, target_language, model_id, provider_id, model_name, tags (array), input_tokens, output_tokens, cost_usd, started_at, duration_ms, outcome (ok|error|timeout|rate_limited|canceled), error (nullable), http_status (integer, nullable), request_id (string, nullable). Cost is input and output tokens times the model's prices (default 0). No prompt or reply stored. No table grows forever: the `llm_calls` table purges rows older than `llm_calls.retention_days` (default 30, 0 keeps forever, negative rejected at config load) automatically in batched deletes, every 6 hours and shortly after start. PhraseForge finished jobs are purged automatically (status done older than `jobs.retention_days_done`, default 7; failed and cancelled older than `jobs.retention_days_failed`, default 30; 0 keeps forever; pending and running jobs are never deleted) with the same batched schedule. The assistant's llm_calls table is purged after `ASSISTANT_LLM_CALLS_RETENTION_DAYS` days (default 90, 0 keeps forever); the assistant's tasks and task_steps are out of scope.
8. Admin: POST/DELETE `/api/v1/admin/llm-prompts` edits tags (chosen from models.yaml) and overrides; GET `/api/v1/admin/` returns the bootstrap including llm_prompts. New routes: GET `/api/v1/admin/models` lists id, provider, tags, priority, availability; GET `/api/v1/admin/llm-calls` shows attempt history (newest first), and GET `/api/v1/admin/llm-aggregates` shows per-model aggregates (call count, success rate, mean duration, tokens/sec, cost) with optional since-filter. Static UI adds a models page and aggregates page.
9. Metrics with prefix `phraseforge_llm_` exposed at `/metrics` on the main listener without login: `_calls_total{provider,model,outcome}`, `_tokens_total{provider,model,direction}` (direction: input|output), `_cost_usd_total{provider,model}`, `_call_duration_seconds{provider,model}` (histogram), gauge `_model_available{model}` (1 or 0), `_record_errors_total` (insert failures), and `_calls_purged_total`. Plus `phraseforge_jobs_purged_total{rule}` for job purge outcomes (jobs_done|jobs_failed). Labels are configured ids only.

Logging and troubleshooting
10. The router writes structured slog lines for every call so a failure can be diagnosed later: one line per attempt (call id, model and provider ids, provider type, tags, outcome, failure kind, duration, tokens, HTTP status, the provider's request id, bounded provider error text), breaker open/closed and provider-down events, a Warn when a provider becomes unavailable (missing credentials, with the credential source named, never a value) and an Info when it recovers, one summary line per call (fell back, failure chain), caller-added attributes (kind and languages in PhraseForge; task and user ids in the assistant) through an option; no prompt, reply, schema, request body, header value or API key ever appears in any log line. Documented in the router package doc with example jq/grep one-liners.

Deployment and testing
11. Deployment example: `phraseforge/k8s/configmap.yaml` carries one local Ollama provider and model (the dev cluster has no GPU host). The Helm chart `models.yaml` key provides three example models per purpose with final provider ids: `ollama-cpu` (in-cluster CPU Ollama with gemma4, lowest priority, always available), `ollama-gpu` (LAN GPU host, usually offline, used when available, falls back to CPU model, high priority), and `openrouter` (reference model tagged `openrouter` only, never used by default, tried per job via Admin > LLM tag override). The OpenRouter provider is always rendered in the chart; its `api_key_from` field stays optional (defaults to empty string). Model names, addresses, and API keys are placeholders the user replaces. The README has sections 'Where to configure API keys' per provider type showing the `api_key_from` forms (empty, env:NAME, file:/path) and 'Adding another provider', and documents the optional per-provider `headers`.
12. All tests are LLM-free: openai, anthropic, vertex tested against fake HTTP servers (request shape, headers, structured output, token counts, each failure class); router tested with a scripted provider and injected clock. Real calls: at most 1–3 through the dev laptop's Ollama at host.docker.internal:11434, after precheck and with timeout, model unloaded afterwards.

## Approach

Extract `shared/router` (request, response, provider interface, failure kinds, router, breaker, observer) and `shared/modelcfg` (models.yaml schema, loader, lint, validate) from assistant, along with `ollama` and `vertex` providers (vertex is already in assistant/internal/llm/vertex/vertex.go and moves unchanged). Assistant keeps a thin adapter implementing `workflow.LLM`; assistant schema and tests stay green. Phraseforge loads `models.yaml` from a second ConfigMap key, rejects old config keys, validates chunking budgets, and routes through the shared router in `callLLM`. The `shared/llm` package gains one additive field (`Config.Headers`) so that custom headers work for Ollama too; nil value preserves existing behaviour.

New phraseforge code: `internal/config` updated for `model_tags` and rejection of old keys; `internal/ai/ai.go` `callLLM` now calls the router; `internal/db/schema.sql` gains `llm_calls` table and migrates `llm_prompts`; `internal/llmcalls` records attempts and updates metrics; `internal/server/admin.go` extends `/api/v1/admin` routes and adds `/metrics` (step 5 adds prometheus/client_golang dependency); static UI components add models page and performance aggregates.

Key decisions:
- Shared router in k8s-lab/shared so assistant and phraseforge (and later knowledge, dictionary) all use the same code.
- One row per attempt: failures are visible; usage sums rows with outcome `ok`.
- OpenAI and Anthropic are non-streaming with overall timeout (streaming deferred).
- Phraseforge chunks before routing, so chunk size never depends on which model the router picks; startup validates each model's num_ctx ≥ purpose numCtx.
- Breaking change: every purpose in `phraseforge/k8s/configmap.yaml` and the Helm chart repository values must change together — mismatch fails at startup.

Test strategy (all LLM-free): config schema and loader (bad files, each lint finding); router selection, fallback, breaker (with clock); fake HTTP servers for each provider (request shape, token counts, all failure classes); phraseforge config validation (old keys rejected, chunking budget check); recorder tests with test DB; metrics; admin API; helm `helm template` renders the example values. Real calls: only dev Ollama at most 1–3 after precheck.

Ordered plan (each step ends green; step 1 is a pure move and the riskiest):
1. Extract shared/router, shared/modelcfg, ollama and vertex providers from assistant; assistant adapter implementing workflow.LLM; assistant schema and all tests unchanged and green.
2. OpenAI provider (fake server; optional real check against dev Ollama's /v1 if available).
3. Anthropic provider (fake server).
4. Phraseforge config (model_tags required, old keys rejected), models.yaml load, validation; llm_prompts schema migration and read-only count check.
5. callLLM through router; llm_calls table and recorder; Prometheus metrics with /metrics endpoint; admin API and UI for models, aggregates, and attempts.
6. Dev cluster configmap, example Helm chart repository values, ExternalSecret and Deployment env setup; 1–3 real calls; docs and CHANGELOG.

## Affected Areas

- New: `shared/router`, `shared/modelcfg`, `shared/ollama` (moved from assistant), `shared/vertex` (moved from assistant), `shared/retention`, `phraseforge/internal/llmcalls`, `phraseforge/internal/metrics`, `phraseforge/internal/jobs` (job id in handler context, retention rules), `phraseforge/k8s/models.yaml`.
- Changed: `phraseforge/internal/config`, `phraseforge/internal/ai/ai.go`, `phraseforge/internal/db/schema.sql` (llm_calls table, llm_prompts migration), `phraseforge/internal/server/admin.go` (`/api/v1/admin/llm-prompts` routes, `/metrics` endpoint, static models and aggregates UI components), `assistant/internal/llm` (adapter layer), `assistant` (retention config, purger, metrics, openapi/README/Usage page note), the frontend chart's values.yaml (CRLF; note at end), the chart's phraseforge template (ConfigMap models.yaml key, ExternalSecret entries for openrouter_api_key and anthropic_api_key, Deployment env vars), README, CHANGELOG.

## Out of Scope

- Streaming for openai or anthropic (non-streaming first).
- Per-user or cost-based routing, hot reload of models.yaml, editing models.yaml from admin.
- Claude on Vertex (rawPredict), bench CLI.
- Changes to knowledge, dictionary.

## Implementation Notes

**Naming convention decision 2026-10-20:** This feature applies one unified naming convention (`snake_case` per tech-stack.md Naming conventions). The keys introduced by this feature were renamed to `snake_case` before release: `model_tags` (replaces earlier `modelTags`), `llm_default_model_tags`, `llm_calls.retention_days` (was `llmCalls.retentionDays`), `jobs.retention_days_done` (was `jobs.retentionDaysDone`), `jobs.retention_days_failed` (was `jobs.retentionDaysFailed`), and the Helm chart's secrets flags `openrouter_api_key` and `anthropic_api_key` (were `openrouterApiKey` and `anthropicApiKey`). The old camelCase spellings are rejected with a message naming the replacement where silent ignoring would mis-route (config.yaml keys, the POST body).

**Assistant alignment decision 2026-10-20 — option A chosen:** The assistant keeps selecting models by `model_tags` in workflows. Its `models.example.yaml` is aligned with PhraseForge's three-model example (ollama-cpu, ollama-gpu, openrouter, plus gcp and demo). Its dev provider `laptop` was renamed to `ollama-cpu`. The README sections 'Where to configure API keys' and 'Adding another provider' were added to the assistant README too.

**Plan step 1 — done and verified 2026-10-20**

### 1a: Extract and move core schema and config modules

Moved `assistant/internal/yamlschema` → `shared/yamlschema` and `assistant/internal/models` → `shared/modelcfg` (with package renamed to `modelcfg`). The `models.schema.json` file is now embedded by `shared/modelcfg`; still includes the `demo` type for backward compatibility. Updated `shared/go.mod` with `jsonschema/v6` and `yaml/v3` dependencies. Renamed all imports in the assistant accordingly.

### 1b-i: Extract and move router and breaker to shared

Created new `shared/router` with:
- Core types: `Router`, `Breaker`, `Failure` kinds, `Provider` interface
- Utilities: `Attempt`, `Observer`, neutral `Request`/`Response` types
- Configuration helpers: `WithClock`, `WithObserver`, `WithCallCheck`

Created `shared/router/routertest` with scripted provider and fake clock for deterministic testing (no sleep-based waits).

Updated `assistant/internal/llm` to become a thin alias layer built on top of `shared/router`. Added `RequireAttribution` and `ErrNoAttribution` built on the `WithCallCheck` integration point.

Moved all router and breaker tests from assistant to shared (injected clock, no sleeps). Added four new `WithCallCheck` tests. Attribution-specific tests remain in the assistant.

Made `workflow.Request` and `workflow.Response` type aliases of the shared types.

### 1b-ii: Extract and move providers to shared

Moved `ollama` provider to `shared/router/ollama` (New with opts, config per call from modelcfg).

Moved `vertex` provider to `shared/router/vertex` (New with tokenFile and opts).

The two workflow-engine integration tests that exercise both the provider and the workflow engine stay in `assistant/internal/llm/ollama_engine_test.go` (created new file for this) because they need the full workflow engine context.

Demo provider and llmtest stay in the assistant.

### Verification

- `shared`: `go vet` and `go test -race` pass
- Assistant: `task test-assistant` passes (database tests skipped due to dev cluster being down; recorder tests that depend on a running DB were not exercised)
- Phraseforge, knowledge, dictionary build successfully
- Code formatting: `gofmt` clean across all changed files
- `shared/llm` unchanged
- Baseline before the move was the same green state

 - Step 2 (openai provider): shared/router/openai (POST {base_url}/chat/completions, Bearer key read at call time from the env var named by api_key_env, response_format json_schema strict when a schema is given, max_tokens from num_predict, usage tokens from the reply; failure classes: 429 rate limited, 5xx or bad body server, other non-2xx incl. 401/400 client). modelcfg: type openai (base_url required, api_key_env optional), api_key_env name lint, MissingKeys startup check; the assistant registers the provider and fails fast on a missing key variable. Assumptions only a real OpenAI/OpenRouter call can confirm: max_tokens accepted (newer reasoning models may want max_completion_tokens); strict json_schema needs additionalProperties:false and all properties required, some OpenRouter models ignore response_format; usage field names; OpenRouter may return HTTP 200 with an error object (would classify as server failure 'no choices').
 - Step 3a (anthropic provider): shared/router/anthropic (POST {base_url}/v1/messages, headers x-api-key and anthropic-version 2023-06-01, max_tokens required: num_predict else 16000, system omitted when empty, structured output via output_config.format json_schema, never tools/tool_choice/sampling params; text blocks concatenated, thinking blocks ignored; refusal stop reason is a client failure so the router falls back without tripping the breaker; 429 and 529 rate limited, 5xx server, other non-2xx client; request id from the request-id header or the error body). router.Failure gained HTTPStatus and RequestID; Attempt.Error now carries the status and request id. modelcfg: type anthropic (api_key_env required, base_url optional defaulting to https://api.anthropic.com). Raw HTTP, no SDK dependency (deliberate: matches the other providers; the Claude API reference's default is the SDK for Go — the human can veto). Assumptions only a real call can confirm: output_config.format accepted without a strict field; refusal shape (stop_reason refusal, category at stop_details.category); the 16000 default max_tokens may exceed a model's cap (then set num_predict lower in models.yaml).
 - Step 3b (logging): shared/router/logging.go and doc.go; options WithLogger, WithLogAttrs, WithCallIDs; messages: llm candidates (debug), llm no model (warn), llm attempt (info ok/canceled, warn failed), llm breaker open (warn), llm provider down (warn), llm breaker closed (info), llm call done (info, warn when failed); Attempt gained HTTPStatus and RequestID; ollama, vertex and openai providers fill HTTPStatus (openai also RequestID from x-request-id, unverified for OpenRouter); the assistant adds task_id and user_id from its call context and has a Troubleshooting note in its README. A security test asserts a distinctive prompt, schema, reply and API key never appear in the log (covers the router with a scripted provider and the openai and anthropic providers; the vertex and ollama providers are not covered by that test).

### Verification for steps 2-3

- `shared`: `go vet` + `go test -race` pass (packages llm, modelcfg, router, router/anthropic, router/ollama, router/openai, router/vertex, yamlschema)
- task test-assistant passes with database tests skipped (dev cluster down; DB-backed recorder tests NOT exercised)
- phraseforge, knowledge, dictionary build
- gofmt clean
- shared/llm unchanged

### Open follow-ups

- the `demo` type stays in the shared schema enum
- the modelcfg example-file test still reaches into the assistant
- an `llm_calls` DB column for HTTP status/request id was NOT added to the assistant (out of scope)

**Plan step 4a — done and verified 2026-10-20**

### 4a-i: Per-call request overrides (shared)

Extended `router.Request` with per-call override fields: `MaxTokens` (overrides model option `num_predict`), `Think` pointer to bool (overrides model option `think`), and `Timeout` (overrides model option `overall_timeout_seconds`). All four providers (openai, anthropic, vertex, ollama) apply overrides when present; override wins over the model option. Added `api_key_env` support to ollama provider: the provider now reads the API key from the env var named by `api_key_env` at call time and sends it as a Bearer token in the Authorization header; previously PhraseForge read OLLAMA_API_KEY from an environment variable and the router's ollama provider could not, so `api_key_env` was extended to ollama providers. Updated shared/modelcfg (its models.schema.json and lint) to allow `api_key_env` for ollama providers.

### 4a-ii: Multi-turn message history and validation (shared)

Extended `router.Request.Messages` to support multi-turn conversation history with standard roles (system, user, assistant). Added `Conversation()` method that returns the system prompt (Request.System plus the contents of any system-role messages, joined by a blank line, empty parts skipped) and the turns (the non-system messages in order; when Messages is empty, a single user turn made from Input). Added `Validate()` method that enforces message format rules: the turns (system-role messages excluded, they are merged into the system prompt) must be non-empty, start with a user turn and alternate user/assistant; unknown roles are rejected; an invalid request is a client failure returned before the key check and before any network call, and its error never contains message content. All four providers now build their wire request from `Conversation()` instead of building it internally. Behaviour change: the openai provider now omits an empty system message (previously always sent one); the assistant always sets a system prompt, so it is unaffected.

### 4a-iii: Context window management, PhraseForge config refactor, and routing

Added `router.Request.NumCtx` field for per-call context window override (Ollama-specific). Updated router.Select and router.New to accept purpose configuration with model tag matching. Refactored PhraseForge config: removed `provider`, `model`, and `think` keys from purpose definitions and replaced with `model_tags` (list of strings, default `["default"]`). Old keys are rejected at config load with a single error message naming the file, purpose and replacement (`model_tags`). 

New `models.yaml` file (path from `PHRASEFORGE_MODELS_FILE`, default `/etc/phraseforge/models.yaml`, loaded as a second ConfigMap key) defines providers and models. Startup validation fails with all findings reported together when: missing or invalid `models.yaml` file, missing api_key_env variables that are referenced by configured models, a purpose whose `model_tags` match no model in `models.yaml`, or an ollama model whose `options.num_ctx` is smaller than the purpose's effective `num_ctx` (non-ollama providers skip this check). Each purpose configuration has `model_tags` with default `["default"]`; the purpose's chunking budget is sent as `NumCtx` to the router so chunk size and context window match as before.

Router now handles `Tags`, `MaxTokens`, `Timeout`, and `NumCtx` overrides (applied when an `llm_prompts` row exists for the specific job). Retry classification updated: keeps `llm.IsRetryable` semantics for Ollama client errors and uses router failure kinds for other providers (connection/server/rate_limited retryable; timeout/client/canceled not retryable; all-models-failed retryable if any attempt is; no-model error not retryable).

Admin provider list now returns provider ids from `models.yaml`. Added `phraseforge/models.example.yaml` with example provider and model definitions. Updated `phraseforge/k8s/configmap.yaml` (dev): every purpose now has `model_tags: [default]`, old `provider`/`model`/`think`/`providers` blocks removed, and a new `models.yaml` key added with example provider definitions. The Deployment already mounts the whole ConfigMap at `/etc/phraseforge` so no deployment manifest change was needed.

Job id is not included in router log attributes (the jobs handler receives it as an argument, not in context; adding it would require changing every handler). Only `kind`, `source_language` and `target_language` are included in router logs.

### Verification for step 4a

- `shared`: `go vet` + `go test -race` pass
- `phraseforge`: `go vet` + `go test` pass
- `task test-assistant` passes with DB tests skipped
- `knowledge` and `dictionary` build successfully
- `gofmt` clean
- `shared/llm` unchanged
- Startup of `serve` verified end-to-end with real dev ConfigMap content: config and models load correctly, router builds successfully, then stops at unreachable database (as expected)
- Startup verified with old config keys and missing models file: named errors reported correctly

NOT exercised (dev cluster down): DB-backed tests (177 skips) including ai tests (check, review, review_models, review_text, itemjob_db), server tests on newVocabAPI and admin config round-trip, and generate tests on newGenerateDB. Real prompt() query and admin provider validation. ConfigMap parsed but not applied to cluster. Container image not rebuilt (go.mod gained jsonschema/v6 and yaml/v3 indirect dependencies). All of them were run later against the dev database once the cluster was started (see Validation).

### Open follow-ups (step 4a extensions)

- `phraseforge/README.md` and `CHANGELOG` still describe old provider/model/think/providers keys (documentation step)
- Deployment still sets empty `OLLAMA_API_KEY` and `OPENROUTER_API_KEY` env vars, now unused by the app (Helm chart secret entries need a decision at the deployment step)
- Dockerfile builds shared via replace directive and was not re-checked in a container build

**Plan step 4c — completed and verified 2026-10-20**

### 4c-i: llm_prompts schema migration

**Schema:** The llm_prompts table migration from `{provider, model, think, timeout_seconds}` to `{model_tags, think, timeout_seconds}` adds a new `model_tags text[]` column with a CHECK constraint enforcing NULL (inherit default tags) or a non-empty array without blank or NULL elements. All ALTER statements are idempotent, ending with `DROP COLUMN IF EXISTS` for the two deleted columns (values discarded by design). Migration tested on a scratch database: previous schema applied, a row with provider/model/think/timeout inserted, new schema applied twice (second run a no-op), old columns gone, think and timeout preserved, tags NULL, CHECK constraint rejects '{}' and '{""}'. Go migration test using a throwaway schema verified the same. Scratch databases were dropped afterwards. The dev phraseforge database was NOT migrated (deployment is a later step; it still has the old columns).

### 4c-ii: AI package (phraseforge/internal/ai)

**Prompt model:**
- Renamed field from `Provider` and `Model` to `ModelTags` (type `[]string`).
- A row with tags replaces the purpose's default `model_tags` for that kind/source/target pair.
- Removed `Providers()` method (no longer needed).
- Added `Models()` returning model IDs, provider IDs, tags, priority, and availability from the router (never includes URL, key, key variable name, or upstream model name).
- Added `KnownTags()` returning the set of all tags defined in models.yaml.
- Added `ValidateModelTags(tags []string)` to validate a tag array against KnownTags and model assignment (all tags must be known and at least one model must have ALL the tags).

### 4c-iii: Admin API (phraseforge/internal/server/admin.go)

All endpoints listed below are behind `requireAdmin()`:

- **Bootstrap replacement**: `providers` key replaced with `models` (list of model objects with id, provider_id, tags, priority, availability), `tags` (list of known tag strings), and `llm_default_model_tags` (mapping of kind to default tags). llm_prompts rows now carry `model_tags` instead of provider/model.
- **POST /api/v1/admin/llm-prompts**: Now takes `model_tags` in the request body alongside `timeout_seconds` and `think`. Omitted or empty tags are stored as NULL (inherit default). Validation: every tag must be non-blank and known; at least one model must have ALL the tags. A body carrying `provider` or `model` keys is rejected with 400 naming them and explaining they were replaced by `model_tags`.
- **GET /api/v1/admin/models**: New endpoint returning `{models:[{id, provider_id, tags, priority, available}]}` listing all configured models.
- **Config export/import (format version 1 unchanged)**: llm_prompts in the export carry `model_tags` instead of provider/model. Import validates using the same rules and rejects an import file with provider/model keys with a message naming them.

### 4c-iv: Frontend (phraseforge React components)

- **Admin LLM prompt editor**: The provider dropdown and model input fields were replaced with a new `pf-tag-picker` component (checkbox control for model tags).
- **Read-only hint**: Shows the kind's default tags and one 'Model tags' column.
- **i18n keys**: Replaced old keys with new keys `admin.llm_model_tags`, `admin.llm_model_tags_default`, `admin.llm_model_tags_hint`, `admin.llm_model_tags_none` in both the English and Polish catalogs.
- **Polish catalog placeholder review needed**: The Polish catalog has the new keys in ENGLISH as placeholders. One Polish sentence (`admin.llm_placeholders_hint`) was reworded to 'tagi modeli' — both items need the human's review before deployment.
- **Frontend tests**: pf-tag-picker component has jsdom tests.

### 4c-v: Bug fixes during step 4c

Fixed: The sample admin config of the DB-backed round-trip test used script 'Hans' and IME 'pinyin', which do not exist in the schema's seed data ('hans', 'cmn', 'cxs'). That test could never have passed against a real database. The test now uses valid seed data values.

### Verification for step 4c

- phraseforge: `go vet` and `go test` pass (internal/ai: all DB-backed tests ran, 0 skipped; internal/server: 1 skipped = the destructive admin round-trip test, which was run separately on a scratch database and passed).
- Frontend: 66 tests pass with jsdom.
- Shared: `go vet` and `go test -race` pass.
- Task `test-assistant` passes (assistant DB tests still skipped due to dev cluster down).
- Knowledge and dictionary build.
- gofmt clean.
- shared/llm unchanged.

**NOT verified (planned for later phases):**
- The admin LLM tab was never rendered in a browser (check the 1280 px and 1024 px layouts after deploy, with the English placeholders visible in the Polish catalog for now).
- phraseforge/README.md sections about the old providers and per-purpose provider/model keys still describe the old config (documentation step).

### Housekeeping

An abandoned empty schema `pf_test_12955_1` (0 tables) from an earlier test run remains in the dev database. Harmless, left alone pending the human's decision.

### Open follow-ups (step 4c extensions)

- Human review of Polish i18n catalog (new keys as English placeholders; one reworded sentence).
- Browser verification at deploy (1280 px and 1024 px layouts, English placeholders visible in Polish UI).
- Production llm_prompts row count (human to verify read-only before migration).
- phraseforge/README.md and CHANGELOG (old provider/model/think config sections still describe the old schema).

**Plan step 5b — endpoints under /api/v1/admin**

New endpoints (all behind `requireAdmin`, no extra CSRF/Origin check exists on the admin routes): GET /api/v1/admin/llm-aggregates (since default 24 h, group_by model|kind_model; per model calls by outcome, success rate, mean/p50/p95 duration over OK attempts, tokens, output tokens per second, cost), GET /api/v1/admin/llm-calls (limit default 50 max 200, filters since/model_id/outcome/kind, newest first), DELETE /api/v1/admin/llm-calls?older_than_days=N (required, 0..36500, 0 = everything; the on-request purge does not increment the purged-rows metric). UI components pf-llm-models, pf-llm-performance, pf-llm-attempts, pf-llm-purge with inline confirmation (no purge call without it). 58 new i18n keys; the Polish catalog holds English placeholders (human review). Responses verified to contain no URL/key/variable name. Verification: phraseforge `go test` with dev DB all packages ok (internal/server 140 PASS 1 SKIP = the destructive round-trip test, internal/llmcalls 13/0), frontend 104 pass. NOT verified: the pages were never rendered in a browser (check 1280 px and 1024 px after deploy), the admin-app.js wiring has only syntax checks.

**Plan step 6-pre — credentials and availability**

The single `api_key_from` model replaces api_key_env/api_key_file; shared resolver modelcfg.ResolveCredential used by all four providers. router.WithAvailability + CredentialAvailability; ModelStatus.Reason; Warn 'llm provider unavailable' / Info 'llm provider available'; Warn 'purpose has no usable model'. Admin unavailable_reason badge (i18n keys admin.llm_reason_cooldown and admin.llm_reason_no_credentials, Polish placeholders). The assistant's /api/v1/models gained unavailable_reason (openapi updated) and its models page a badge. vertex.New() takes no path; ASSISTANT_VERTEX_TOKEN_FILE and PHRASEFORGE_VERTEX_TOKEN_FILE removed; assistant/models.dev.yaml uses api_key_from: file:/etc/assistant-vertex/token and its deployment keeps the secret mount. The JSON schema does not list api_key_from as required (lint enforces it with a message naming the provider and showing the three forms). Verified: shared, phraseforge (dev DB: internal/server 140/1, config 42/0), assistant (DB tests ran, task test-assistant exit 0, 17 packages), frontend 105 and 322 pass; the validator rejects missing/malformed/old keys and never echoes a pasted key.

**Plan step 6c — Helm chart repository**

Local edits only (nothing applied to any cluster): values.yaml (CRLF preserved) and the phraseforge template: all 14 purposes `model_tags` [default], retention keys; a models.yaml key rendered from values with the three example models. ExternalSecret entries for OPENROUTER_API_KEY and ANTHROPIC_API_KEY are rendered only when `phraseforge.secrets.openrouter_api_key` / `anthropic_api_key` are true (default false) because an External Secrets entry cannot be optional and a missing remote property would fail the whole sync including the database credentials. Deployment env entries are optional. The chart has no checksum annotation, so a changed models.yaml needs a pod restart. Dev manifest: phraseforge/k8s/deployment.yaml reads optional OLLAMA_API_KEY, OPENROUTER_API_KEY, ANTHROPIC_API_KEY, OPENAI_API_KEY from a Secret named phraseforge-llm-keys (not created by the deploy task, so unset by default). READMEs rewritten (phraseforge) and aligned (assistant). Verified independently by the coordinator: helm template exit 0, CRLF preserved on all 698 lines, rendered config.yaml and models.yaml accepted by the real binary (stops only at the unreachable database). NOT verified: nothing applied to a cluster; ExternalSecret behaviour against a live operator; the README statement that OpenRouter rejects a request without an Authorization header is an inference; whether first_token_timeout_seconds bounds a connect to a host that silently drops packets (an off host that answers 'no route' fails fast and holds the provider out for the cooldown).

### Open follow-ups (extend)

- The assistant deploy needs the human's own shell (the Vertex token step needs gcloud and ASSISTANT_VERTEX_ACCOUNT).
- The Polish placeholders (new i18n keys in 5b and 6-pre).
- Browser check at 1280/1024 px after deploy.
- Production override-row count and PostgreSQL version check for gen_random_uuid().

**Plan step 5a — completed and verified 2026-10-20**

### 5a: Job id in handler context, llm_calls table, recorder, and metrics

Job id is put into the handler context at the single call site in `jobs.runOne` (functions `jobs.WithJobID`/`JobID`; ai already imports jobs, no cycle). Table `llm_calls` (uuid id, job_id uuid nullable without FK so history outlives jobs, kind, source/target language, model_id, provider_id, model_name, tags, input/output tokens, cost_usd numeric, started_at, duration_ms, outcome check, error, http_status, request_id; indexes on started_at, (model_id, started_at), job_id); no column holds a prompt, reply or payload. Recorder `phraseforge/internal/llmcalls` implements `router.Observer`: cost = tokens x the model's price, insert with a 5 s timeout on a context that survives caller cancellation (canceled attempts are recorded), a failed insert is logged and counted (`phraseforge_llm_record_errors_total`) and never fails the model call; with the database down each attempt can wait up to 5 s for the synchronous insert. Metrics in an own registry, labels are configured provider/model ids only (never kind, language, job id, user or error text); the availability gauge is read from `Router.Status()` at scrape time. `/metrics` is mounted before the login middleware. `newRouter`'s signature changed to take the models config and the observer; `runServe` loads the models first, then connects the pool, then builds the recorder and router. `prometheus/client_golang` v1.25.0 added to phraseforge (offline via the module cache). `gen_random_uuid()` is used (built into PostgreSQL 13+; the dev server is 18.6; the production PostgreSQL version was NOT checked — a pre-deploy check; an older server would fail at migrate).

**Retention — completed and verified 2026-10-20**

New `shared/retention` package (Rule, Purger, DeleteOlderThan; batched deletes, identifiers validated and quoted, condition is a code constant, 6-hour interval and 1-minute start delay, logs 'rows purged' per rule with deleted, older_than, retention_days; 0 keeps forever). Used for PhraseForge `llm_calls` (`llm_calls.retention_days`, 30), PhraseForge jobs (`jobs.retention_days_done` 7 for status done, `jobs.retention_days_failed` 30 for failed and cancelled; timestamp column updated_at, which is last written when a job finishes; nothing references the jobs table, a job's checkpoint is a column of its own row; llm_calls.job_id keeps its old value; Retry of a purged job now fails as job not found) and the assistant's llm_calls (ASSISTANT_LLM_CALLS_RETENTION_DAYS, 90; tasks and task_steps are not purged). Metrics `phraseforge_llm_calls_purged_total`, `phraseforge_jobs_purged_total{rule jobs_done|jobs_failed}`, `assistant_llm_calls_purged_total`. Assistant docs: openapi descriptions of `GET /api/v1/usage` and `GET /api/v1/tasks/{id}/llm-calls`, README and CHANGELOG state that usage covers only the retained period; the Usage page has a one-line note with 90 hardcoded in the text. The CHANGELOG lines for phraseforge and assistant were added by the engineer during the step (to be reviewed in the documentation step).

**Verification for plan steps 5a and retention**

- `shared` go vet + go test -race pass (incl. retention)
- `phraseforge` go vet + go test pass with PHRASEFORGE_TEST_DSN and a scratch PHRASEFORGE_TEST_DB (internal/jobs 14 PASS 0 SKIP, llmcalls 13/0, config 39/0, metrics 6/0)
- assistant DB-backed tests ran for the first time against the dev Postgres (root package 49 PASS 0 SKIP, llmcalls 21/0, db 32/0, config 15/0, metrics 11/0)
- assistant frontend tests 321 pass
- knowledge and dictionary build
- gofmt clean
- `shared/llm` unchanged
- real `serve` against a scratch database returned HTTP 200 on /metrics without a cookie and 302 on /api/v1/profile
- scratch databases dropped

NOT verified: the serve purge loop was only tested with short schedules and a fake DB, not over its real 6-hour interval; the jobs purge query orders by updated_at, which has no index (the table is small); the docker image build with the new dependencies.

**Open follow-ups (extend the list)**

- assistant/.env.example does not list ASSISTANT_LLM_CALLS_RETENTION_DAYS (reading that file is blocked by a permission rule; the human adds the line)
- the production PostgreSQL version check for gen_random_uuid()
- the production override-row count before the llm_prompts migration

### Review fixes (code and security review, 2026-10-20)

Reviewer verdict: approve with changes. Fixes applied:
- **M1 (Retry waits for held-out model):** NoModelError now carries EarliestAvailable, RetryAfter, SkippedForOtherReason, and Recoverable() so callers can wait without a clock. Retry.retryable() waits: wait = max(backoff, RetryAfter) capped at 2 minutes (120s), permanent for no-tags error and missing credentials.
- **M2 (Overflow check only for Ollama):** Context-overflow check (chunk halving) now applies only to Ollama models; openai, anthropic, vertex handle context dynamically.
- **M3 (Provider canceled with live caller context):** Provider reports 'canceled' with a live caller context no longer returns empty success; falls to next candidate, AllFailedError at end.
- **M4 (Retention cap 36500):** retention.MaxDays = 36500 (99.9 years); exceeding this at config load is a validation error.
- **M5 (Router.TruncateText, UTF-8 safe):** Applied in openai, anthropic, vertex providers to bound error text.
- **M6 (Attempt.Error bounded at 1000 bytes):** Bounded once in router (Ollama client still reads whole body into memory; only stored text is bounded).
- **M7 (Priority minimum 0):** Schema validation: priority must be >= 0; finding names the path and index, not model id.
- **M8 (Tag pattern):** Schema validation: tags match `^[a-z][a-z0-9-]*$`.
- **M9 (Strict config.yaml decoding):** Unknown keys rejected at startup, naming file and key; proven not to reject dev ConfigMap, chart-rendered config or defaults.
- **M14 (ValidateModelTags for admin overrides):** num_ctx check runs for admin tag overrides; ValidateModelTags takes the kind parameter.
- **M15 (Credentials with CR/LF/NUL):** File containing line break (CR, LF, CRLF, NUL) counts as missing; provider disabled.
- **M17 (schema.sql comment):** Documented.
- **M18 (env vs file restart wording):** env:NAME values need pod restart after Secret change; file: contents re-read per call.
- **M19 (base_url query/fragment rejected):** Provider base_url must not contain `?` or `#`; parse error not echoed.
- **Cheap win (router.HasAllTags):** Added.

Findings consciously NOT applied (with reasons):
- Dev ConfigMap carries one model: AC 11 amended, no code change.
- Purpose timeout precedence (timeoutSeconds overrides model's overall_timeout_seconds; 0 lets model decide): documented in README; feature's design intent.
- Synchronous recorder insert (up to 5s wait per attempt): documented as trade-off; async writer deferred.
- `/metrics` exposure through ingress: documented; blocking is a deployment decision.
- Shared request-helper de-duplication across four providers: refactor, deferred.

## Validation

**Date: 2026-10-20**

Shared packages:
- `shared`: `go vet` + `go test -race` pass (packages llm, modelcfg, router, router/anthropic, router/ollama, router/openai, router/vertex, yamlschema, retention; 9 packages ok)

PhraseForge:
- `go vet` + `go test` pass with dev database: 625 PASS 0 FAIL (internal/ai 246, internal/server 122 + 1 SKIP = destructive round-trip test run separately on scratch database and passed, internal/config 44, internal/jobs 14 with scratch database, internal/llmcalls 13)
- Helm chart `helm template` renders (default and with optional openrouter_api_key/anthropic_api_key secret entries)
- Rendered config.yaml and models.yaml accepted by real binary (stops at unreachable database)
- Config validation: a provider entry without the compulsory `api_key_from` key, a malformed `api_key_from` value and the removed `api_key_env`/`api_key_file` keys are rejected by lint with messages naming the provider (the value is never echoed); a missing credential at runtime disables the provider with a Warn instead of stopping startup (tested with dummy values); purpose with no matching model reported; model num_ctx < purpose numCtx reported
- Dev database migration applied: llm_prompts has model_tags column, llm_calls table with all fields created
- Frontend: phraseforge 110 pass
- Real model call: PhraseForge title generation through ollama-cpu (81 in / 6 out tokens, 14.0 s, row in llm_calls with job_id, model_id, provider_id, tags, outcome=ok)
- Admin > LLM tab checked in headless Chromium against dev deployment. First screenshots (1280 px and 1024 px) showed Performance and Recent attempts tables cut off at 1024 px; after layout fix and redeploy, a 1024 px screenshot with synthetic stress rows (long model id, long error with HTTP status and request id, 1,234,567 / 123,456 tokens, per-purpose grouping) showed no hidden columns, and tables' scrollWidth equalled clientWidth at both 1280 px and 1024 px in both groupings (no horizontal overflow). Synthetic rows deleted afterwards.
- Three dev test users deleted from dev database
- **phraseforge**: entries added to `## [Unreleased] ### Added` (model routing line 11), `### Removed` (three breaking-change entries lines 57-60)
- **assistant**: entries added to `## [Unreleased] ### Added` (model router line rewritten 19, four new entries 29-32 for provider types, logging, dev config, unavailable models; formerly line 29 expanded to multiple bullets), `### Changed` added (two breaking-change entries for api_key_from requirement and Vertex token source)
- **knowledge** and **dictionary**: no entry — the only touch is the internal additive Headers field of shared/llm (backward compatible, not user-facing)
- **root CHANGELOG.md**: no entry — verified with `git diff --stat -- k8s Taskfile.yml CHANGELOG.md README.md` showing no changes; dev k8s/ manifests belong to the phraseforge artifact

All other documentation verified as correct:
- phraseforge/README.md — correctly uses snake_case config names and includes "Where to configure API keys" and "Adding another provider" sections
- assistant/README.md — correctly uses ASSISTANT_LLM_CALLS_RETENTION_DAYS and has API key and provider sections; references ollama-cpu provider name
- assistant/CHANGELOG.md — correctly states ASSISTANT_LLM_CALLS_RETENTION_DAYS
- assistant/models.dev.yaml — correct priorities: gemma-laptop 10, gemini-flash-lite 5
- shared/llm — confirmed additive change: Headers field added to Config struct, nil default, backward compatible
- Query parameters — correctly named older_than_days in code

Constitution drift (proposed, then approved by the human and applied on 2026-10-20):
1. **specs/tech-stack.md lines 68-70 "Built from" note is stale.** Evidence: The note says "shared/llm is consumed by phraseforge, knowledge and assistant (Ollama path only)" but now shared also contains modelcfg, yamlschema, router (with providers ollama, openai, anthropic, vertex and routertest), retention — all consumed by phraseforge and assistant. The note does not mention that shared/llm gained an additive Headers field (backward compatible). Also missing: phraseforge now depends on prometheus/client_golang v1.25.0 in "Frameworks & libraries" section. **Proposed change:** Update the "Built from" line to document the new shared packages and their consumers, and add prometheus/client_golang to the Frameworks & libraries section with a note it's used by phraseforge for /metrics and admin aggregates.

2. **Assistant roadmap items** — The following three items in `specs/artifacts/assistant/roadmap.md ## Next` were delivered by this feature and should be removed (removed on 2026-10-20 with the human's approval): `assistant-provider-openai`, `assistant-provider-anthropic`, `assistant-provider-vertex` (lines 13-15).

## Documentation Review

**Documents audited:** phraseforge/README.md, phraseforge/CHANGELOG.md, assistant/README.md, assistant/CHANGELOG.md, shared/llm, root README.md, specs/tech-stack.md

Drift found and fixed:
- **phraseforge/CHANGELOG.md:10** — Used camelCase config keys (`llmCalls.retentionDays`, `jobs.retentionDaysDone`, `jobs.retentionDaysFailed`) instead of snake_case. Fixed to `llm_calls.retention_days`, `jobs.retention_days_done`, `jobs.retention_days_failed`. Evidence: config.go shows YAML tags as snake_case; k8s/configmap.yaml uses snake_case; feature spec implementation notes (line 86) document the naming decision 2026-10-20.

CHANGELOG resolution per artifact:
- **phraseforge**: entries added to `## [Unreleased] ### Added` (model routing), `### Changed` (8 validation fixes), `### Removed` (two breaking-change entries)
- **assistant**: entries modified in `## [Unreleased] ### Added` (model router rewritten, model providers expanded, logging, dev config, unavailable models), `### Changed` added (two breaking-change entries)
- **knowledge** and **dictionary**: no entry — the only touch is the internal additive Headers field of shared/llm (backward compatible, not user-facing)
- **root CHANGELOG.md**: no entry — verified with `git diff --stat -- k8s Taskfile.yml CHANGELOG.md README.md` showing no changes; dev k8s/ manifests belong to the phraseforge artifact

All other documentation verified as correct:
- phraseforge/README.md — correctly uses snake_case config names and includes "Where to configure API keys" and "Adding another provider" sections
- assistant/README.md — correctly uses ASSISTANT_LLM_CALLS_RETENTION_DAYS and has API key and provider sections; references ollama-cpu provider name
- assistant/CHANGELOG.md — correctly states ASSISTANT_LLM_CALLS_RETENTION_DAYS
- assistant/models.dev.yaml — correct priorities: gemma-laptop 10, gemini-flash-lite 5
- shared/llm — confirmed additive change: Headers field added to Config struct, nil default, backward compatible
- Query parameters — correctly named older_than_days in code

## Documentation Updates

**Changes made:**

1. **phraseforge/CHANGELOG.md**:
   - Line 10 (Retention bullet): Changed camelCase to snake_case.
     - **Before:** `Retention: no table grows forever. Model-call history (`llmCalls.retentionDays`, default 30) and finished jobs (`jobs.retentionDaysDone`, default 7, for status done; `jobs.retentionDaysFailed`, default 30, for failed and cancelled) are purged automatically in batches; 0 keeps forever, pending and running jobs are never purged. Metrics `phraseforge_llm_calls_purged_total` and `phraseforge_jobs_purged_total{rule}`.'
     - **After:** `Retention: model-call history (`llm_calls.retention_days`, default 30) and finished jobs (`jobs.retention_days_done`, default 7 for status done, `jobs.retention_days_failed`, default 30 for failed and cancelled) are purged automatically in batches; 0 keeps forever, pending and running jobs never purged. Metrics `phraseforge_llm_calls_purged_total` and `phraseforge_jobs_purged_total{rule}`.'
   - Line 11 (new Model routing entry): Added detailed description of router, models.yaml, api_key_from, headers, availability behavior.
   - Lines 57-60 (new ### Removed section): Added three breaking-change entries for (1) config.yaml keys (provider, model, think, providers block) replaced by model_tags and models.yaml; (2) llm_prompts table schema (provider/model columns dropped); (3) old api_key_env/api_key_file keys replaced by api_key_from.

2. **assistant/CHANGELOG.md**:
   - Line 19 (Model router entry): Rewritten to final behavior.
     - **Before:** `Model router: \`models.yaml\` lists Ollama providers (localhost or another machine) and models with tags, priority, options and prices; a call goes to the highest-priority available model that has the tags and falls back down the list, with a circuit breaker that holds a whole machine out when it cannot be reached. \`assistant models validate <file>...\` checks a models file.`
     - **After:** `Model router: \`models.yaml\` (path \`ASSISTANT_MODELS_FILE\`, required) lists providers (ollama, openai, anthropic, vertex) and models with tags, priority, options and prices; every provider entry has the compulsory \`api_key_from\` key (\`""\`, \`env:NAME\` or \`file:/abs/path\`, read per call) and optional \`headers\` (custom HTTP headers, each re-read per call); a call goes to the highest-priority available model that has the tags and falls back down the list; missing credentials disable only that provider (unavailable in admin, warn in log), never halt startup; circuit breaker holds failing models or unreachable providers out for \`cooldown_seconds\`. \`assistant models validate <file>...\` checks a models file.`
   - Lines 29-32 (Model providers entry expanded): Expanded and rewritten.
     - **Before:** `Model providers \`vertex\` (Gemini on Vertex AI, authenticated with an access token read from a file; \`task refresh-assistant-vertex-token\`) and \`demo\` (a deterministic stand-in for trying the interface).`
     - **After:** Four separate entries added: (1) providers list (ollama, openai, anthropic, vertex with Vertex using api_key_from:file:, demo); (2) router logging with task_id/user_id; (3) dev deployment (laptop → ollama-cpu, priorities, README sections); (4) unavailable models behavior
   - Lines 36-37 (new ### Changed section): Added two breaking-change entries for api_key_from requirement and Vertex token source change.

3. **specs/features/phraseforge-model-router.md**:
   - Filled Validation section (dated 2026-10-20) with test results, real calls, browser verification wording, NOT-verified list
   - Filled Documentation Review section with drift findings, CHANGELOG resolution per artifact, constitution proposals
   - Filled Documentation Updates section with before/after CHANGELOG changes
   - Updated status from `implementing` to `documenting`

The constitution edits proposed in the review were approved by the human and applied on 2026-10-20 (tech-stack.md version 19: the Built from note, the Prometheus line, the shared LLM client line, the naming conventions and the kubeconfig rule); the delivered assistant-provider-* items were removed from the assistant roadmap.

Delivery approved by the human on 2026-10-20; the `## Now` roadmap lines of phraseforge and assistant were removed; nothing is committed (the human commits manually).
