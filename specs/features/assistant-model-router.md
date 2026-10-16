---
title: Assistant model router for Ollama
kind: feature
status: done
version: 2
updated: 2026-10-16
branch: main
---

## Problem / Motivation

The workflow engine asks a model for every step, but nothing connects it to a real model: its `LLM` interface has only a scripted fake. The assistant must mix and match models per task: a configuration with tags and priorities, the highest-priority model that has the needed tags and is available, a fall back to lower priorities when it is not, and a record of every call (model, tokens, cost, time) attributed to the user the task runs for, with metrics for dashboards and a view in the admin interface.

This task builds that router with one provider, Ollama, reusing the shared client that already handles Ollama's streaming, timeouts, token counts and structured output. OpenAI-compatible providers (OpenRouter, OpenAI), Anthropic and Vertex AI are not built now, since they cannot be tested at the moment; the provider interface is kept small so each is one new file later.

The queue still runs only `noop`, and the engine is not connected to the worker. To give the router a real, testable path, a built-in workflow `ask` makes one model call from its payload and returns the text. It exercises routing, fallback and the call records end to end without waiting for the engine's wiring.

Decisions confirmed by the user (2026-10-16): defaults for slicing, provider reuse, cost from a price table, the circuit breaker and metric labels; `model_tags` as the key name; only Ollama for now. An Ollama endpoint may be on localhost or on another machine in the network, so several Ollama providers with different `base_url` values can be configured, and a machine that is down must be skipped cheaply.

## Acceptance Criteria

Configuration
1. `assistant/schemas/models.schema.json` describes `models.yaml`: `providers` (list of `{id, type, base_url}`, `type` limited to `ollama` for now), `models` (list of `{id, provider, model, tags, priority, options, price}`) and `routing.breaker` (`failures`, `cooldown_seconds`, `rate_limited_cooldown_seconds`, with defaults 3, 60 and 30). A model's `options` are Ollama's: `num_ctx`, `num_predict`, `think` and the three timeouts. `price` is `input_per_mtok` and `output_per_mtok` in US dollars per million tokens, default 0. A higher `priority` number is tried first.
2. The loader rejects a file that breaks the schema, naming the key and line, and reports in one list: a duplicate model or provider id, a model naming an unknown provider, an empty `tags` list or a blank tag, a negative price, a `base_url` that is not an http or https URL with a host, and a `base_url` that carries credentials (user and password in the URL), which would end up in a ConfigMap. Several providers of type `ollama` with different `base_url` values are allowed, such as one on localhost and one on another machine. `assistant models validate <file>` prints them and exits 1, or exits 0 with a one-line summary.
3. The file is read from the path in `ASSISTANT_MODELS_FILE` (default `/etc/assistant/models.yaml`). Startup fails at once, naming the variable and the file, when it is missing or invalid. In the cluster it is a ConfigMap; a sample `assistant/models.example.yaml` is committed.

Routing
4. `Select(tags)` returns the available models that have every requested tag, highest priority first, ties broken by the order in the file. A request with no tags matches every model. A request no model can satisfy fails with an error naming the tags and the models checked.
5. A call tries the first candidate; on a failure it goes to the next, and so on. The error returned after all fail lists each model and why. A canceled context stops the chain at once and is not counted as a model failure.
6. A circuit breaker marks a model unavailable after `failures` consecutive failures (network error, timeout, 5xx) for `cooldown_seconds`, and a 429 for `rate_limited_cooldown_seconds`. A success resets the count. After the cooldown the model is tried again by the next call; there are no pre-flight pings. A model that is unavailable is skipped without a call. A client error (4xx other than 429) is a failure of that call but does not count toward the breaker, since the model itself is healthy. A connection failure (the endpoint cannot be reached: refused, no route, DNS, dial timeout) is a fault of the provider's machine, not of one model, so it makes every model of that provider unavailable for `cooldown_seconds` at once, and the other models on that machine are not tried in the meantime.
7. The router implements the engine's `workflow.LLM`. `workflow.Request` gains `Tags []string`, and the engine fills it from `model_tags`.
8. The workflow YAML gets an optional `model_tags` list at workflow level and per state, the state's list replacing the workflow's. The schema, the loader and `workflow validate` handle it (a blank or duplicate tag is a finding). Existing workflows without it keep working and match every model.

Ollama provider
9. The Ollama provider uses the shared client (`k8s-lab/shared/llm`) without changing it. It sends the request's system prompt and input as the two messages, the request's reply schema as `format`, and the model's `options`, and returns the text and the prompt and reply token counts Ollama reports. Failures are classified as timeout, rate limit, server error or client error for the breaker.

Records and metrics
10. Table `llm_calls` (one row per attempt, so a fallback leaves a failed row and a successful one) has columns `id` (uuid, primary key), `task_id` (uuid, nullable, references `tasks`), `user_id` (uuid, not null, references `users`), `model_id` (text), `provider_id` (text), `model_name` (text, the provider's own name for the model), `tags` (text array), `input_tokens` (integer), `output_tokens` (integer), `cost_usd` (numeric), `started_at` (timestamptz), `duration_ms` (integer), `outcome` (text: `ok`, `error`, `timeout`, `rate_limited`, `canceled`) and `error` (text, nullable). The task and user come from the context the worker sets; a call without them is refused. Cost is `input_tokens` and `output_tokens` times the model's prices.
11. Metrics: `assistant_llm_calls_total{provider,model,outcome}`, `assistant_llm_tokens_total{user,provider,model,direction}` (direction `input` or `output`), `assistant_llm_cost_usd_total{user,provider,model}`, `assistant_llm_call_duration_seconds{provider,model}` and the gauge `assistant_llm_model_available{model}` (1 or 0). The `user` label is the user's id.
12. No log line, record or metric holds a prompt, a reply or a payload; the `error` column holds the provider's error text only.

Workflow `ask`, API and admin
13. The built-in workflow `ask` takes payload `{"prompt": string, "system": string (optional), "tags": [string] (optional)}`, makes one model call through the router, and returns `{"text", "model_id"}` as its result. A missing or blank prompt fails the task with a clear error.
14. `GET /api/v1/tasks/{id}/llm-calls` returns the task's attempts, oldest first, as `{"items": [...]}`; `GET /api/v1/usage` returns input tokens, output tokens, cost and call count grouped by user and model, optionally from `since` (a timestamp); `GET /api/v1/models` returns each configured model with its provider id, tags, priority and whether it is currently available; it never returns a provider's URL. Task responses gain `usage` (`calls`, `input_tokens`, `output_tokens`, `cost_usd`, `models`), zeros and an empty list for a task that made no calls. All are in `openapi.yaml`, held to it by the existing tests.
15. The admin task list shows the model, tokens and cost of each task; the task page lists its attempts (model, tags, tokens, cost, duration, outcome, error); a Usage page shows cost and tokens by user and model. They follow the existing components and tests.

Deployment and tests
16. `deploy-assistant` builds with the shared module, creates the ConfigMap from `models.yaml`, and the Deployment mounts it; the image stays under 50 MB. A task `ask` with a prompt runs on the dev cluster through the host's Ollama and the admin shows its model, tokens and cost.
17. All tests are free of any real model: providers are tested against a local fake Ollama HTTP server that records requests and returns scripted streams, and the router against a scripted provider with an injected clock, so no test sleeps for a breaker cooldown. The engine runs end to end through the router and the fake server. `task test-assistant` and `task test-assistant-frontend` pass.
18. After deploying, at most one real call is made through the deployed `ask` workflow, under the bounds of the earlier smoke check (precheck of Ollama and load first, `timeout 90`, model unloaded afterwards), and reported.

## Approach

New packages under `assistant/internal/`: `models` (config types, loader, lint), `llm` (the router, the `Provider` interface, the breaker, the call recorder) and `llm/ollama` (the provider). The router is built in `app.go` from the config and handed to the workflows registry and, later, the engine.

Provider interface, deliberately small: `Complete(ctx, ModelSpec, Request) (Response, error)` plus an error classification (`Failure` with a kind: `timeout`, `rate_limited`, `server`, `client`, `canceled`). A new provider implements this one method and is added to the `type` enum of the schema.

Attribution: the worker puts the task id and user id into the context before running a workflow; the router reads them to write `llm_calls` and to label metrics. Recording never blocks or fails a call: a failed insert is logged and counted.

Time: the breaker takes a clock, so tests move time without sleeping.

Key decisions:
- Ollama through `shared/llm` unchanged, via a `replace` to `../shared` like the other apps (the Dockerfile and the Taskfile already pass the shared build context). Nothing under `shared/` changes, so phraseforge and knowledge are untouched.
- One row per attempt keeps failures visible; usage sums rows with outcome `ok` only (a failed attempt has zero tokens and cost).
- `GET /api/v1/models` shows only configuration and availability, never keys or provider URLs.
- Where Ollama runs is configuration, not code: `localhost` works for a process on the same machine, `host.docker.internal` for the dev cluster reaching the laptop's Ollama, and a LAN address for another machine. The example file shows a local and a remote provider.
- The `user` metric label has one value per user; with a single user for now this is safe, and the spec flags it should be revisited if users multiply.

Test strategy (all LLM-free): schema and loader tests (each bad file, each lint finding, line numbers); selection tests (tags, priority, ties, no match, empty tags); fallback and breaker tests (counts, cooldown by clock, 429 vs 5xx vs 4xx, a connection failure taking down every model of that provider but not the models of another provider, canceled context, recovery after cooldown, all fail error text), and a configuration with two Ollama providers, one reachable and one not; provider tests against the fake Ollama (request shape: messages, `format`, options; token counts; each failure class; timeouts); recorder tests with a database (rows per attempt, cost arithmetic, refused call without a task); metrics tests; engine integration (a scripted `learn-topic` run through the router and the fake server, with a fallback in the middle); API and OpenAPI tests; frontend component tests for the new columns and pages; deployment checked on the cluster.

Ordered plan (each step ends green with a short report):
1. `models.schema.json`, loader, lint, `assistant models validate`, `models.example.yaml`.
2. The router core with a scripted provider: selection, fallback, breaker, clock.
3. The Ollama provider over `shared/llm`, with the fake server.
4. `llm_calls`, the recorder, metrics and context attribution.
5. `Request.Tags`, `model_tags` in the workflow schema, loader and lint; the router as `workflow.LLM`; engine integration test.
6. Workflow `ask`, the API and OpenAPI additions, admin pages.
7. ConfigMap, Deployment, Taskfile, deploy, one bounded real call, documentation.

## Affected Areas

- New: `assistant/internal/models`, `assistant/internal/llm`, `assistant/internal/llm/ollama`, `assistant/schemas/models.schema.json`, `assistant/models.example.yaml`, `assistant/k8s/` (ConfigMap, Deployment mount).
- Changed: `assistant/internal/workflow` (`Request.Tags`, `model_tags`), `assistant/schemas/workflow.schema.json`, `assistant/schemas/db/schema.sql`, `assistant/internal/workflows`, `assistant/internal/tasks` (context attribution), `assistant/internal/server` (API, static admin components), `assistant/schemas/api/openapi.yaml`, `assistant/app.go`, `assistant/main.go`, `assistant/go.mod`, `assistant/Dockerfile`, `Taskfile.yml`, README and CHANGELOG.
- Constitution (separate gated update): `specs/tech-stack.md` (the shared LLM client is now also consumed by the assistant, Ollama path only, unchanged), the roadmap (this item narrowed to Ollama, three provider items added).

## Out of Scope

- OpenAI-compatible providers (OpenRouter, OpenAI), Anthropic and Vertex AI: each a follow-up task.
- Connecting the engine to the worker, the real tools, memory and RAG, and engine-owned `pending`.
- API keys and a secret store (nothing needs one for Ollama), hot reload of `models.yaml`, per-user budgets and limits, streaming a reply to the user, and the response review loop.
- Changing `shared/llm` or the two other apps.

## Implementation Notes

Done in the planned seven steps (plus a slice split of step 6), on `main`, nothing committed.
- Configuration: `schemas/models.schema.json`, `internal/models` (loader and lint), `assistant models validate`, `models.example.yaml`, and `models.dev.yaml` for the dev cluster. The YAML-versus-schema checker the workflow loader had is now the shared `internal/yamlschema`.
- Router: `internal/llm` (selection by tags and priority, fallback, circuit breaker with a clock, a connection failure holding every model of the provider, `RequireAttribution`) and `internal/llm/llmtest` (scripted provider, fake clock, observer).
- Ollama: `internal/llm/ollama` over `shared/llm`, which is unchanged; tested against a fake Ollama HTTP server.
- Records: `llm_calls` table, `internal/callctx`, `internal/llmcalls` (store and recorder), `assistant_llm_*` metrics including a per-scrape availability gauge. The worker puts the task and user into the run's context.
- Engine: `model_tags` on workflows and states (a state's list replaces the workflow's; `[]` on a state means any model), `Request.Tags`, linted.
- `ask` workflow (`internal/workflows/ask.go`), API (`/tasks/{id}/llm-calls`, `/usage`, `/models`, `usage` on tasks, all in OpenAPI), admin (model, tokens and cost columns, a model-calls table on the task page, a Usage page with the models and their availability: new components `as-usage` and `as-model-list`).
- Deployment: the `assistant-models` ConfigMap is created by `task deploy-assistant` from `models.dev.yaml` after validating it (not kept in `assistant/k8s/`, which `kubectl apply` takes whole); the Deployment mounts it and sets `ASSISTANT_MODELS_FILE`.

Deviations and additions, none of which change an acceptance criterion:
- `workflow.Response` gained `ModelID`, set by the router, so `ask` can return `model_id`.
- The Ollama provider sends no system message when the request has none (`ask` without `system`).
- `GET /api/v1/models` also returns `unavailable_until` (null when available), which the Usage page shows.
- The Usage page also lists the configured models; the status bar keeps an error until the component that raised it loads well (two components load independently on that page).
- Found only by deploying: the Dockerfile copied `main.go` and `app.go` by name, so `models_cmd.go` was missing from the image build; it now copies `*.go`.
- Admin table at 1024 px: the source column is hidden under 1200 px, headers wrap, and the created time omits seconds, because eleven columns did not fit.

## Validation

Tests: `task test-assistant` passes (16 packages, with the dev cluster's database, `-race`); `task test-assistant-frontend` 197 pass. No test calls a model: the Ollama provider runs against a fake HTTP server, the router against a scripted provider with a fake clock, and the engine runs end to end through both with a fallback in the middle.

Mutation checks, each caught by a test and restored: insert with the caller's context; failed-insert counter removed; output tokens left out of cost; state tags not replacing the workflow's; `Tags` not filled by `BuildRequest` (this exposed a first fallback test that still passed, which was strengthened with a model only the `chat` step can reach); task usage not mapped; `unavailable_until` dropped.

Deployed to the dev cluster (`task deploy-assistant`): the image is 19.1 MB uncompressed (limit 50 MB); `/api/v1/models` shows the model without any address; `llm_calls` exists; the pod is ready.

One real call (AC 18), through the deployed `ask` workflow: precheck passed (no model loaded, load 3.5, nothing running in the queue); the prompt was a one-word reply; task 7790a128 succeeded in 21.5 s of execution (0.35 s queued) with model `gemma-laptop` (`gemma4:12b`), 20 input and 2 output tokens, cost $0, reply "ready". One `llm_calls` row, outcome `ok`. The model was unloaded afterwards (`/api/ps` empty). Screenshots at 1280, 1024 and 420 px, light and dark: no sideways scroll, no console errors, no failed requests.

Coverage gaps, stated plainly:
- Only one real call was made, to one model, on the local machine. A fallback, the breaker and a connection failure were exercised only against fakes; a second Ollama machine in the network was never reached.
- The reply schema sent as Ollama's `format` was checked in requests to the fake server only. `ask` sends no schema, so constrained decoding against a real model with the engine's `oneOf` reply schema is still unverified.
- The database tests ran against the dev cluster's Postgres; without it they are skipped (visibly).
- The call counters are per process, so they restart at zero on a redeploy; `llm_calls` holds the history.

## Documentation Review

Checked against this change: `assistant/README.md` (new Model router section; admin, API, run and test sections out of date), `assistant/CHANGELOG.md`, `specs/tech-stack.md` (already updated at v15 with the shared client note and the model routing convention, still accurate), `specs/mission.md` (no drift), `specs/artifacts/assistant/roadmap.md` (the `## Now` line for this task is removed at delivery), `assistant/models.example.yaml`.

## Documentation Updates

Done: README (model router section and updated overview, API, admin, deployment and test notes), CHANGELOG (seven entries under Unreleased), five entries in `specs/memory.md`. The constitution was updated earlier in this task (tech-stack v15, roadmap v3). The `## Now` line of the roadmap is removed when the user approves delivery.
