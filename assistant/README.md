# assistant

A generic background assistant: tasks name a workflow and a user, and an LLM
walks the workflow's state machine, calling tools, until it reaches an end
state. What exists today is the task queue with its API and admin interface,
a worker that runs the built-in `noop` and `ask` workflows and the YAML
workflows through the engine (parking a run that asks the user and resuming it
with the answer), and the model router with Ollama, OpenAI-compatible (such as
OpenRouter), Anthropic and Vertex AI providers and a demo provider. Channels
and memory come in later slices (see
`specs/artifacts/assistant/roadmap.md`).

## Task queue

- **Tasks** (`tasks` table) name a workflow, a user and a source (`telegram`,
  `nats`, `scheduler`, `api`). One worker takes the oldest pending task, runs it
  and records `succeeded` or `failed` with a result or an error. Queue time and
  execution time are derived from the timestamps. A task left running by a
  process that died is failed at the next start.
- **API** (`/api/v1`, described in `schemas/api/openapi.yaml`): `POST` and `GET`
  `/users`, `POST` and `GET` `/tasks` (paged, newest first, `status` filter),
  `GET /tasks/{id}` (with `usage`: calls, tokens, cost, models),
  `GET /tasks/{id}/llm-calls`, `GET /llm-calls?since=&limit=&offset=` (every attempt, a page at a time), `GET /usage?since=` (per user, provider and model), `GET /models`; also
  `/healthz` and `/metrics` (no login, shows model ids, provider ids, call counts, cost and Go/process info, never credentials or content; in production block it at the ingress or scrape the pod address directly; each scrape evaluates provider availability by reading credential and header files). Errors are always
  `{"error": {"code", "message"}}`. There is no authentication yet.
- **Admin interface** at `/` (its files under `/static/`; the former `/admin/` redirects): a single-page app of native Web Components
  (`as-*`, in `internal/server/static/`), embedded in the binary. It shows the
  queue with status filter and paging, counts waiting and running times up
  live, shows each task's model, tokens and cost, and on a task's page its
  payload, result, error and every model call it made. The top menu is Chat,
  Tasks and Usage; Usage has a side panel with three pages. Usage
  (`#/usage?period=&group=`) shows user, provider, model, calls, input tokens,
  output tokens and cost for a period, grouped by user, provider or model.
  Models (`#/models`) lists provider, model, tags, priority and availability.
  Attempts (`#/attempts?period=&page=`) lists every model call, newest first,
  failed ones included, 25 per page with a pager and a period filter. The three
  tables are the shared components `dp-usage-table`, `dp-model-table` and
  `dp-attempt-table` from `shared/ui`; the page only loads and hands them rows.
- **Metrics** for Prometheus: `assistant_tasks_total{source,workflow,status}`,
  `assistant_task_queue_seconds`, `assistant_task_execution_seconds` and
  `assistant_http_requests_total` / `_duration_seconds` by route template.
- **Logs** are JSON on stdout; a task's start and finish are one line each with
  its id, workflow, outcome and duration, never its payload or result.
- **Troubleshooting LLM calls**: every model call leaves JSON log lines that
  share a `call_id` and carry `task_id` and `user_id`: `llm candidates` (debug),
  one `llm attempt` per model tried (model, provider, outcome, failure kind,
  HTTP status, the provider's request id, bounded error text), breaker and
  provider transitions, and one `llm call done`. Prompts, replies, schemas and
  keys are never logged. Follow one call with
  `jq -c 'select(.call_id == "<id>")'`; the full list of messages, fields and
  more one-liners is the Troubleshooting section of the `shared/router`
  package documentation (`shared/router/doc.go`).
- **Schemas** (`schemas/`) are the contracts: `db/schema.sql` (the only DDL),
  `api/openapi.yaml`, `workflow.schema.json`. Tests hold the code to them.

## Run it

```sh
cp .env.example .env     # set ASSISTANT_DATABASE_URL
go run . migrate         # apply the schema
go run .                 # serve on :8080, admin at http://localhost:8080/
```

In the dev cluster: `task deploy-assistant` (from the repository root) builds
the image, creates the `assistant` database and its Secret, the `assistant-models`
ConfigMap (from `models.dev.yaml`; the Ollama on the laptop), applies the schema
and deploys to `http://assistant.localhost:8080/`. Try it:

```sh
curl -H 'Host: assistant.localhost' localhost:8080/api/v1/users \
  -d '{"display_name":"Ada"}'
curl -H 'Host: assistant.localhost' localhost:8080/api/v1/tasks \
  -d '{"user_id":"<id from above>","workflow_id":"noop","payload":{"sleep_ms":2000}}'
```

## Chat and tidying the queue

- **Chat** (`#/chat` in the admin, `/api/v1/conversations`): a user can have
  several conversations, each bound to one workflow (a YAML workflow, or `ask`).
  The first message starts a run (a task with source `telegram`); while the run
  waits for an answer the next message is the answer; once it has finished,
  failed or been cancelled the next message starts a new run; while it works a
  new message is refused (409). The assistant's side is the run speaking: its
  questions, its result, and its failures (in red). `/help` lists the commands
  and the workflows; an unknown command says so. The channel is behind an
  interface (`internal/chat`), a mock now and Telegram later.
- **Tidying**: cancel a pending or waiting task, or stop a running one
  (`POST /tasks/{id}/cancel`), retry a failed or cancelled one as a new task
  (`POST /tasks/{id}/retry`), remove one (`DELETE /tasks/{id}`, not a running
  one) or all of one finished status (`DELETE /tasks?status=failed`). A removed
  task takes its trace with it, but its model calls stay (until the retention
  above), so usage and cost do not change. Removing a task a conversation is in leaves the conversation
  usable. The task page has the buttons; the list has "Remove all" for a
  finished status filter.
- `GET /api/v1/workflows` lists what a task or conversation can name, and the
  forms are filled from it.

## Running workflows

- A task whose `workflow_id` is not a built-in runs the YAML workflow of that
  id (`workflows/*.yaml`, a ConfigMap in the cluster, read from
  `ASSISTANT_WORKFLOWS_DIR`; startup fails with file and line if one is
  invalid). The payload is `{"message", "state"?, "language"?}`; the run id is
  the task id. The tools are stand-ins (`internal/tools`) until memory, RAG and
  web search exist; each says in its result that it is a stub.
- When a run asks the user (`ask_user`) the task becomes `waiting`, keeping its
  run and question, and the queue goes on. `POST /api/v1/tasks/{id}/messages`
  `{"message": ...}` queues it again; the worker resumes the run with the
  answer. A finished or failed task keeps its final run.
- `GET /api/v1/tasks/{id}/steps` is the trace: one row per attempt of a step
  with the model's raw reply (cut at 8 KB), the action, the tool, tokens and the
  error of a refused reply. It holds replies on purpose and is never logged.
- The admin task page shows a failure as an alert at the top, the question and
  an answer box for a waiting run, the run state, the trace (a step that went
  wrong is shown open, in red) and the model calls. The task list has a "New
  task" form.
- `hello` (the demo model) and `hello-live` (a real chat model) are tiny
  workflows to try all of this by hand: they ask for a name, then greet.

## Model router (`internal/llm`)

- `models.yaml` (format in `../shared/modelcfg/models.schema.json`, sample in
  `models.example.yaml`, dev cluster's in `models.dev.yaml`) lists providers
  (an Ollama endpoint each: localhost or another machine) and models with tags,
  a priority, Ollama options and a price per million tokens. The path is
  `ASSISTANT_MODELS_FILE` (default `/etc/assistant/models.yaml`); startup fails
  naming the variable and the file if it is missing or invalid. Check a file with
  `go run . models validate models.yaml`.
- A call names tags (a workflow, or one of its states, lists `model_tags`; a
  state's list replaces the workflow's). It goes to the available model with all
  the tags and the highest priority, then falls down the list on failure.
- A circuit breaker sets a model aside after `failures` consecutive failures for
  `cooldown_seconds` (a 429 for its own cooldown), with no pings. A connection
  failure holds every model of that provider, since the machine is the fault.
  A client error (4xx) does not count against the model.
- Every attempt is a row of `llm_calls` with the task and user, tokens, cost,
  duration and outcome; a fallback leaves the failed row and the successful one.
  The cost is attributed to the task's user. Metrics:
  `assistant_llm_calls_total`, `_tokens_total{user,...}`, `_cost_usd_total`,
  `_call_duration_seconds` and `assistant_llm_model_available{model}`.
  No prompt or reply is logged or stored.
- **Chat interfaces (A2UI)**: the page can draw interfaces that an agent
  describes in A2UI v1.0 messages (renderer in `shared/ui`, loaded only when a
  page draws one). `#/gallery` shows every example of the reference with the
  messages beside it and what the page sent back (a development page).
  Images, video and audio may come from this server and from `data:` URLs;
  `ASSISTANT_A2UI_MEDIA_HOSTS` (comma-separated bare hosts such as
  `images.example.com` or `media.example.com:8443`, https only, no wildcards,
  empty by default) adds hosts to the page's `img-src` and `media-src`. An
  entry that is not a bare host name stops startup, because it is written into
  the Content-Security-Policy. `GET /a2ui-config.json` tells the page the hosts,
  so a blocked image is explained instead of broken.
- **Interfaces in a conversation**: a workflow shows an interface by putting
  A2UI v1.0 messages under `"ui"` in the payload of an `ask_user` action (with
  or without a `question`). They are validated (`internal/a2ui`: the vendored
  schemas, the basic catalog, ids, references, cycles, depth and size) and stored
  as a message of kind `ui`; an invalid interface is not stored and the
  conversation says why. The page draws it and sends what the user does back
  with `POST /api/v1/conversations/{id}/ui-events` (an A2UI action must come
  from a component of a surface the conversation drew); the action is stored as
  a `ui_action` message, shown as the user's turn, and handed to the run as
  `{"ui_action": <the action>}`, the answer to the question it waits on. An
  interface that a later action answered stays visible but is read-only. Function
  calls go both ways, with the assistant's own catalog (`schemas/a2ui/assistant_catalog.json`):
  a run may put a `callRendererFunction` for `viewport` (the window's size and
  colour scheme) under `ui`, which gets a fresh id from the assistant, is run by
  the page without a word to the user, and whose answer is the run's next
  message, `{"ui_function_response": ...}` (or a `TIMEOUT` error after 15
  seconds); and the page may ask the assistant for `listWorkflows`, answered in
  the conversation. The values a user entered travel with an action and are put
  back when the conversation is opened again (a surface with a password field is
  never saved). Refused A2UI messages are counted in
  `assistant_a2ui_rejected_total{source="interface"|"event"}`. The workflow
  `ui-demo` (scripted by the demo model, so it needs no real model) shows every
  component, asks the page for its window's size, and then shows a form:
  start a conversation with it to try the interface.
- **A model composes the interface**: a workflow that declares `ui: {catalogs: [basic]}`
  gets a guide to the A2UI catalog in its system prompt (generated from the catalog,
  about 5 KB: the rules, every component with its properties, one complete reply),
  and an interface in the `ask_user` payload (`payload.ui`) is validated before the
  reply is accepted; an invalid one is sent back with its problems (the first eight,
  by path) and the model retries within `max_retries`. A surface id the model reuses
  (it writes "main" every time) is renamed in the conversation. `ui-compose` is the
  workflow for trying it (tag `ui`, which only the dev model `gemma4-12b-ui` has: the
  same model with 2048 output tokens, because a form is much longer than a chat
  reply). `go test ./internal/llm -run TestARealModelComposes` with
  `UI_TRIAL_OLLAMA=http://localhost:11434` runs one workflow through a real model
  in-process, with no cluster and no database.
- **Delegation and the orchestrator**: a workflow that declares `intents`
  (a description and example requests) is offered to the orchestrator; one without
  is never offered (`GET /api/v1/workflows` shows each workflow's `intents`, null
  when none). A state with `delegate: true` may reply with the `delegate` action,
  `{"type": "delegate", "workflow": "<id>", "message": "<the request>", "reason":
  "<why>"}`; its prompt then lists the offered workflows with their intents. The
  workflow it names runs as a child of the run (a subagent), with its own state
  and limits, in the same conversation: its questions and interfaces come out of
  the run, the user's answers and actions go to it, and when it ends its final
  state (or its failure) comes back to the parent as the next observation,
  `{"delegate_result": ...}` or `{"delegate_error": ...}`. The child's steps
  continue the numbering of the parent's trace (each step says its workflow and
  depth), its tokens count against the parent's budget (it may use at most what
  the parent had left), and delegation is nested at most 2 deep. The `orchestrator`
  workflow (tag `orchestrator`) makes one model call per message to hand it to a
  workflow, to answer itself, or to ask one short question; the conversation shows
  a line `→ hello: <reason>` when it routes, and its header says `Handled by
  <workflow>` while a delegated workflow has the turn. In the dev cluster the demo
  model plays the orchestrator by keywords ("hello", "form", "interface"), so it
  can be tried without a real model.
- **Retention of model calls**: `llm_calls` rows older than
  `ASSISTANT_LLM_CALLS_RETENTION_DAYS` days (default 90, capped at 36500; 0 keeps them forever; a
  negative or non-numeric value stops startup) are deleted automatically, in
  batches, a minute after start and then every 6 hours. The log line is `rows
  purged` (fields `rule`, `deleted`, `older_than`, `retention_days`) and the
  counter is `assistant_llm_calls_purged_total`. So usage (`GET /usage`, the
  Usage page, a task's `usage`) and call history (`GET /tasks/{id}/llm-calls`)
  cover only the retained period. `tasks` and `task_steps` are not purged.
- Providers: `ollama`; `vertex` (Gemini on Vertex AI, REST `generateContent`),
  which authenticates with an OAuth access token that the provider's
  `api_key_from: file:/path` in `models.yaml` names, re-read on every call, so a
  refreshed token needs no restart (to try it in the dev cluster, put
  `LLM_VERTEX_ACCOUNT` and `LLM_VERTEX_PROJECT` in `.env` and run `task llm-enable`,
  which adds the `vertex` provider and writes the token from your gcloud account
  into the Secret `assistant-vertex`, mounted at `/etc/assistant-vertex/token`;
  the token is valid about an hour, `task llm-refresh-vertex-token` writes a new
  one; Vertex AI in production is not implemented, a known gap described in the
  [root README](../README.md#known-gaps): nothing there mints or renews the
  token). Without a token the
  Vertex provider is disabled (reason `missing_credentials`, one warning in the
  log), never a startup error; and `demo`, a deterministic stand-in that acts out `hello` instantly,
  to try the interface without a real model. `openai` (any OpenAI-compatible
  chat API) and `anthropic` are also registered. Every provider except `demo`
  may have `headers` in `models.yaml`: extra request headers, each a literal,
  `env:NAME` or `file:/abs/path` (read on every call, sent after the provider's
  own headers; `Authorization` and `x-api-key` are refused: use `api_key_from`).
  An `env:` or `file:` header with no value disables the provider like a missing
  credential (reason `missing_credentials`). Names and values are never in the
  API, metrics, `llm_calls` or logs (the warning names only the source). The
  rules are in the phraseforge README, "Custom request headers", and in the
  package documentation of `../shared/modelcfg`.
- The built-in workflow `ask` makes one call: payload `{"prompt", "system"?,
  "tags"?}`, result `{"text", "model_id"}`.

```sh
curl -H 'Host: assistant.localhost' localhost:8080/api/v1/tasks \
  -d '{"user_id":"<id>","workflow_id":"ask","payload":{"prompt":"Say ready","tags":["chat"]}}'
```

### Where to configure API keys

Every provider entry in `models.yaml` except `demo` has the **compulsory** key
`api_key_from`; its value may be empty:

| `api_key_from` | Meaning |
|---|---|
| `""` | no credential |
| `env:NAME` | the environment variable `NAME`, read on every call; the pod's environment only changes on a restart, so a new value needs one |
| `file:/absolute/path` | the content of the file, trimmed of whitespace, re-read on every call, so a rotated secret or a refreshed token needs no restart |

A provider without the key is a configuration error, so a forgotten credential
is not a silent default. The key itself is never written in `models.yaml` or
the database: `api_key_from` only says where to find it. A missing credential is
never a startup error. Which provider type needs one:

| Provider type | `api_key_from` | Credential | Sent as | Other fields |
|---|---|---|---|---|
| `ollama` | `""`, `env:NAME` or `file:/path` | not needed (a local server or one behind a proxy); sent when present | `Authorization: Bearer` | `base_url` required |
| `openai` (any OpenAI-compatible API) | same (for example `env:OPENROUTER_API_KEY`) | not needed for a local server; sent when present | `Authorization: Bearer` | `base_url` required |
| `anthropic` | same | **needed**: without one the provider is skipped | `x-api-key` header | `base_url` optional (default `https://api.anthropic.com`) |
| `vertex` (Google) | same, usually `file:/path` to a token file | **needed**: an OAuth access token, not an API key; without one the provider is skipped | `Authorization: Bearer` | `project` and `location` required, no `base_url` |
| `demo` | not allowed | none | answers in-process | none |

A provider that needs a credential and has none (empty or unset variable,
unreadable or empty file) is skipped by the router (reason `missing_credentials`),
so calls fall back to the next model; with `file:` it returns by itself once the
file has content, with `env:` set the variable and restart. Every provider
except `demo` may also have `headers` (a literal, `env:NAME` or `file:/abs/path`
per header, read on every call); the rules are in the phraseforge README,
"Custom request headers", and in the package documentation of `../shared/modelcfg`.

**How the dev deployment supplies them** (`k8s/deployment.yaml`, `models.dev.yaml`):

- **Ollama on the laptop (`ollama`):** `api_key_from: ""`, no credential. This is
  the dev models file's only real provider, the same as phraseforge's dev
  cluster: model `gemma4-12b` (`gemma4:12b`), tags `default`, `chat`, `review`
  and `cheap`, priority 10. The GPU machine (`ollama-gpu`, preferred), Vertex
  and OpenRouter are the prod cluster's in the committed file;
  `models.example.yaml` shows them. To try a LAN Ollama, OpenRouter or Vertex in
  the dev cluster, use `task llm-enable` (see "LLM providers in the dev cluster"
  in the [root README](../README.md#llm-providers-in-the-dev-cluster)).
- **Vertex, if you enable it to try it:** `task llm-enable` (with
  `LLM_VERTEX_ACCOUNT` and `LLM_VERTEX_PROJECT` in `.env`) writes an access token
  from your gcloud account into the Secret `assistant-vertex` (key `token`, valid
  about an hour) and adds the `vertex` provider to the models file. The Deployment
  mounts that Secret, optional, at `/etc/assistant-vertex`, so the provider's
  `api_key_from: file:/etc/assistant-vertex/token` reads it. Run
  `task llm-refresh-vertex-token` when the token expires; no restart is needed.
  A Secret of that name that `llm-enable` did not create is never overwritten.
- **`env:` keys:** besides `ASSISTANT_MODELS_FILE`, `ASSISTANT_WORKFLOWS_DIR` and
  `ASSISTANT_DATABASE_URL`, the Deployment's `env` list has an optional
  `OPENROUTER_API_KEY` from the Secret `assistant-llm-keys`, key
  `OPENROUTER_API_KEY` (the pod starts without it). `task llm-enable` creates
  that Secret from `OPENROUTER_API_KEY` in `.env` and adds the `openrouter`
  provider (`api_key_from: env:OPENROUTER_API_KEY`). A Secret of that name made by
  hand is still allowed and is never overwritten: `llm-enable` stops and names it.
  For any other key, create a Secret with it in the `app` namespace (with the
  lab's kubeconfig), add an entry such as this to the container's `env` in
  `k8s/deployment.yaml`, name it in the provider in the models ConfigMap source,
  and run `task deploy-assistant` (it applies both and restarts the pod):

  ```yaml
  - name: OPENROUTER_API_KEY
    valueFrom:
      secretKeyRef:
        name: assistant-llm-keys   # created by `task llm-enable`, or by hand
        key: OPENROUTER_API_KEY
        optional: true             # the pod starts without it
  ```

  Alternatively mount the Secret as a volume and use `file:`, as for Vertex; a
  mounted file needs no restart when it changes.

### Adding another provider

Add an entry to `providers` and one or more to `models` in `models.yaml`, and
restart (the file is read at startup; credentials set with `file:` or `env:` are
re-read on every call).

- `id` is free: lowercase letters, digits and hyphens, starting with a letter,
  unique. Models refer to it in `provider`.
- `type` picks the API: `ollama`, `openai`, `anthropic`, `vertex` (or `demo`).
- Several providers of one type are fine: several Ollama hosts, or several
  OpenAI-compatible endpoints such as OpenRouter and OpenAI, each with its own
  `base_url` and `api_key_from`.
- `openai` means any endpoint that implements `POST {base_url}/chat/completions`
  with a Bearer key (for OpenRouter, `base_url: https://openrouter.ai/api/v1`).
- Decide the tags with care: a workflow's `model_tags` select every model that
  has all of them, so a model that costs money or receives the prompts should
  carry only a tag of its own (as `openrouter-reference` does in
  `models.example.yaml`).
- Known limits: no per-provider connect timeout, and the timeouts are per model
  (`options`). `first_token_timeout_seconds` and `idle_timeout_seconds` apply to
  `ollama` models only; `overall_timeout_seconds` and `num_predict` apply to every
  type, `num_ctx` and `think` to `ollama` only.

## Workflow engine (`internal/workflow`)

- A workflow is a YAML file (`workflows/`, format in
  `schemas/workflow.schema.json`): states, the tools and next states each
  state allows, a JSON Schema for the run state, and limits.
- At each step the model sees only the workflow, the current state, the run
  state with its schema and the latest observation, never a history, and answers
  `{"state_patch": {...}, "action": {...}}`. The runtime validates the reply,
  merges the patch into the run state, and carries the action out
  (`tool`, `goto` or `ask_user`). Invalid replies are retried with the reason.
- `ask_user` suspends the run. A run is plain JSON (`Run`, `RestoreRun`), so
  it can be stored and resumed later, by another process.
- Prompts are a template with `{{language}}`, `{{output}}`, `{{languageRules}}`
  and `{{outputRules}}`; snippets live in `internal/workflow/prompts/lang/<code>/`
  and `internal/workflow/prompts/output/<kind>/`.
- The prompts are not edited here. They are developed and evaluated in the
  `prompt-eval` repository (`prompt/assistant/`, suite `assistant-step/`) and
  copied in with `npm run sync-assistant` there. The files under
  `internal/workflow/prompts/` and `internal/workflow/testdata/prompt-eval/`
  are copies; `npm run check-sync-assistant` in `prompt-eval` verifies them.

## Checking a workflow file

```sh
go run . workflow validate --tools memory_search,rag_search workflows/learn-topic.yaml
```

Exit code 0 means valid, 1 means problems (printed as `file:line: path:
message`), 2 means bad usage. Without `--tools` the tool names are not checked.

## Tests

No test calls a real LLM: every model reply is scripted.

```sh
task test-assistant            # from the repository root; vet + tests with the dev cluster's Postgres
task test-assistant-frontend   # the as-* component tests (Node + jsdom)
go test ./...                  # from this directory
```

Database tests use a schema of their own inside the database named by
`ASSISTANT_TEST_DATABASE_URL` and are skipped, with a visible reason, when it is
not set. `task test-assistant` sets it from the dev cluster (database
`assistant_test`, created if missing). Providers are tested against a fake
Ollama HTTP server, the router against a scripted provider with a fake clock.

`internal/workflow/testdata/prompt-eval/` holds the render vectors, the
evaluation cases and the reply schemas synced from `prompt-eval`. Go tests
check that each case is exactly the request the engine builds, that each
schema is the one the engine builds, and that each example reply is accepted.

`internal/workflow/testdata/learn_topic.golden.json` is a recorded run of the
`learn-topic` workflow. The test replays it and fails if the engine's
requests change. After an intended change, regenerate it and review the diff:

```sh
UPDATE_GOLDEN=1 go test ./internal/workflow -run TestLearnTopicGolden
```
