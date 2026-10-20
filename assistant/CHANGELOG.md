# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added

- Retention: model calls older than `ASSISTANT_LLM_CALLS_RETENTION_DAYS` days (default 90, 0 keeps forever, capped at 36500) are purged automatically, so usage and call history cover only that period; counter `assistant_llm_calls_purged_total`. Tasks and their steps are not purged.
- Task queue: tasks name a workflow and a user and are run one at a time by a worker, with the built-in `noop` workflow; tasks left running by a crashed process are failed at the next start. Tables `users` and `tasks` (`assistant migrate`).
- REST API under `/api/v1` for users and tasks, described in `schemas/api/openapi.yaml` and held to it by tests; `/healthz` and `/metrics` (task counts, queue and execution time histograms, HTTP request metrics).
- Admin interface at `/admin/`: the queue with status filter, paging and live refresh, and a page per task with its payload, result and error, built from `as-*` Web Components with phraseforge's layout and light and dark themes.
- `task deploy-assistant`, `task test-assistant`, `task test-assistant-frontend`; the image is about 4 MB.
- Workflow engine: a workflow is a YAML state machine; a model walks it one validated step at a time (tools, moves between states, questions to the user that suspend the run until the answer arrives), with prompts specialised per language and per output kind. Not wired to any real model yet.
- `assistant workflow validate <file>...`: checks workflow files against the workflow schema and reports unreachable states, dead ends, dangling references and unknown tools, with the line of each problem.
- Workflow `learn-topic`: intake, learning program, level assessment and lessons for a language or a technology. It records the question it just asked (`pending`), so the answer can be judged by a step that does not see the question.
- The step input shows the model the run state's schema (`run_state_schema`), so it knows every key it may record, including keys not yet in the run state.
- Controller prompt with language rules (`arb`, `eng`) and output rules (`lesson`, `quiz`), developed and evaluated in `prompt-eval` and synced into the assistant.
- Model router: `models.yaml` (path `ASSISTANT_MODELS_FILE`, required) lists providers (ollama, openai, anthropic, vertex) and models with tags, priority (0 or more), options and prices; tags match `^[a-z][a-z0-9-]*$`; provider `base_url` must not contain `?` or `#`; every provider entry has the compulsory `api_key_from` key (`""`, `env:NAME` or `file:/abs/path`, read per call) and optional `headers` (custom HTTP headers, each re-read per call); a call goes to the highest-priority available model that has the tags and falls back down the list; missing credentials disable only that provider (unavailable in admin, warn in log), never halt startup; circuit breaker holds failing models or unreachable providers out for `cooldown_seconds`. `assistant models validate <file>...` checks a models file.
- Every model call is recorded in `llm_calls` for its task and user, with tokens, cost, duration and outcome, and exposed as Prometheus metrics (`assistant_llm_*`, including per-user tokens and cost and per-model availability).
- Workflows and states can list `model_tags`; a state's list replaces the workflow's.
- Built-in workflow `ask`: one model call from a prompt, to exercise routing and records.
- API: `GET /api/v1/tasks/{id}/llm-calls`, `GET /api/v1/usage`, `GET /api/v1/models` (never a provider's address), and `usage` on every task.
- Admin: model, tokens and cost in the task list, a table of model calls on each task page, and a Usage page with the configured models and their availability.
- `task deploy-assistant` creates the `assistant-models` ConfigMap from `models.dev.yaml` and mounts it.
- The workflow engine runs in the worker: a task can name a YAML workflow (loaded at startup from `ASSISTANT_WORKFLOWS_DIR`, a ConfigMap), a run that asks the user parks the task as `waiting` with its run and question, and `POST /api/v1/tasks/{id}/messages` resumes it. Tools are labelled stubs for now.
- Workflow trace: `task_steps` and `GET /api/v1/tasks/{id}/steps`, one row per step attempt with the model's raw reply, the action, tokens and the error of a refused reply; `assistant_trace_record_errors_total` counts rows that could not be written.
- Admin: a failed task shows its error in the list and as an alert on its page; a waiting task shows its question and an answer box; the task page shows the run state and the trace; the list has a "New task" form and a `waiting` status.
- Model providers: `ollama` (localhost or any machine), `openai` (OpenAI-compatible endpoint: OpenRouter, OpenAI, etc.), `anthropic` (Anthropic Claude API), `vertex` (Gemini on Vertex AI via REST API, authenticated with an OAuth token from `api_key_from: file:...`; `task refresh-assistant-vertex-token` writes a fresh token into the Secret), and `demo` (deterministic stand-in).
- Router logging: structured log lines for every attempt (model, provider, tags, outcome, HTTP status, provider request id, bounded error text), breaker state changes, provider availability changes (Warn when unavailable, Info when recovered), with task_id and user_id added by the assistant's wiring; documented in the router package doc with jq/grep examples. No prompt, reply, schema or key appears in logs.
- Dev deployment: provider `laptop` renamed `ollama-cpu`; model priorities set CPU-based Ollama (priority 10) before Vertex (priority 5), so dev testing uses local model first. README sections added: "Where to configure API keys" and "Adding another provider" (matching phraseforge).
- Unavailable models: a model whose provider lacks a required credential is skipped (unavailable in `GET /api/v1/models` with `unavailable_reason: missing_credentials`), a badge shows on the admin models page, and a call falls back to the next model (never a startup error).
- Chat: conversations and messages (several per user), a chat page in the admin to talk to a workflow (questions, results and failures in the thread, a live state line, `/help`), the assistant's side spoken by the run, and a mock channel behind an interface for the Telegram to come.
- Tidying the queue: cancel (also stops a running task), retry, remove one task or all of a finished status, from the task page and the list; removing keeps the model calls so cost does not change.
- `GET /api/v1/workflows`; the New task and New conversation forms offer exactly what the server has.
- Workflows `hello` (demo model) and `hello-live` (a real chat model) for trying conversations by hand.

### Changed

- **Breaking change:** `models.yaml`: every provider entry now requires the compulsory `api_key_from` key (`""`, `env:NAME` or `file:/abs/path`). Old `models.yaml` files without this key will fail at startup with a message naming the provider.
- **Breaking change:** Vertex token authentication: the token no longer comes from the `ASSISTANT_VERTEX_TOKEN_FILE` environment variable. Instead, use `api_key_from: file:/path/to/token` in the provider entry in `models.yaml`. The old env var is removed.

