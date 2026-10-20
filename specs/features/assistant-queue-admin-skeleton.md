---
title: Assistant skeleton with task queue and admin view
kind: feature
status: done
version: 1
updated: 2026-10-16
branch: main
---

## Problem / Motivation

The planned `assistant` is a generic background agent: tasks arrive from Telegram, NATS, a scheduler and REST, each names a workflow and a user, and an LLM walks a state machine to produce an answer. None of it exists yet. Before any LLM or channel is added, the foundation has to exist and be visible: a Go module that runs, a task queue that records who asked, what ran and how long it waited and ran, a schemas directory that is the readable source of truth, an admin page to watch the queue, and Prometheus metrics.

This slice makes the smallest real version of that foundation. It makes no LLM calls and talks to no channel except REST, so the queue, timings and admin view can be seen working before the model router (next slice) is built.

Decisions confirmed by the user (2026-10-16):
- Slice 1 has no authentication. It runs only in the dev cluster, reachable through the local ingress. Authentication is planned as an external microservice shared by other apps, and comes before any deployment to the production cluster.
- The admin interface is a JavaScript single-page app built from native Web Components, consistent with the other applications. It is not server-rendered HTML.
- Tests are extensive, and none of them ever calls a real LLM: any LLM interaction in the suite uses mocked responses, and the whole suite runs with no LLM reachable.

## Acceptance Criteria

1. `assistant/` is its own Go module. `CGO_ENABLED=0 go build -ldflags="-s -w"` succeeds, and the image built from its multi-stage Dockerfile (`golang` then `scratch`) is under 50 MB.
2. Configuration comes from the environment. `ASSISTANT_DATABASE_URL` is required, and startup fails at once with a message naming the variable when it is missing. `assistant/.env.example` is committed with placeholder values, and `.env` is gitignored.
3. `assistant/schemas/db/schema.sql` is idempotent and is the only source of DDL. `assistant migrate` applies it, and running it twice leaves the database unchanged.
4. Table `users` has columns `id` (uuid, primary key), `telegram_id` (bigint, unique, nullable), `display_name` (text, not null), `created_at` (timestamptz, not null).
5. Table `tasks` has columns `id` (uuid, primary key), `user_id` (uuid, not null, references `users`), `workflow_id` (text, not null), `source` (text, not null, one of `telegram`, `nats`, `scheduler`, `api`), `status` (text, not null, one of `pending`, `running`, `succeeded`, `failed`, `cancelled`, default `pending`), `payload` (jsonb, not null, default `{}`), `result` (jsonb), `error` (text), `created_at`, `started_at`, `finished_at` (timestamptz; the last two nullable). Queue time and execution time are derived from these timestamps and are not stored.
6. REST under `/api/v1`, errors always `{"error": {"code": "...", "message": "..."}}`:
   - `POST /users` (body `display_name`, optional `telegram_id`) returns 201 with the user.
   - `GET /users` lists users.
   - `POST /tasks` (body `user_id`, `workflow_id`, optional `payload`) returns 201 with the task, `source` set to `api` by the server and `status` `pending`. An unknown `user_id` returns 404, and a missing field returns 400.
   - `GET /tasks` lists tasks newest first with paging, `GET /tasks/{id}` returns one.
7. `assistant/schemas/api/openapi.yaml` describes exactly these endpoints. A test fails when the router has a route missing from the file or the file has a route the router lacks.
8. One worker claims the oldest pending task, sets `running` and `started_at`, runs the handler registered for its `workflow_id`, then sets `succeeded` or `failed`, `result` or `error`, and `finished_at`. Only one task runs at a time. A task left `running` by a crashed process is set to `failed` with an explanatory `error` at the next startup.
9. The only handler is the built-in workflow `noop`. It returns the task payload as `result` and honors an optional `payload.sleep_ms` capped at 10000. A task whose `workflow_id` has no handler fails with the error `unknown workflow: <id>`.
10. The admin interface is a single-page app served from the Go binary (`go:embed`, no bundler, no build step) at `/admin/`. It uses native Web Components with light DOM, tag prefix `as-`, one directory per component under `static/components/<tag-name>/` (`component.js` required, `component.html` and `component.css` optional), following the repo convention. It calls only the JSON API of criterion 6. Views:
    - Task list, newest first, with columns id, user display name, workflow, source, status, created at, queue time, execution time, paged, with a status filter.
    - Task detail with payload, result and error.
    - A pending task shows its queue time so far, and a running task its execution time so far. The list refreshes while any task is pending or running.
11. `GET /metrics` serves Prometheus metrics: counter `assistant_tasks_total{source,workflow,status}`, histograms `assistant_task_queue_seconds{workflow}` and `assistant_task_execution_seconds{workflow}`, and HTTP request count and latency by method, route template and status.
12. Logging uses `log/slog` with a JSON handler on stdout, configured once in `main.go`. Each task start and finish produces one line with task id, workflow, outcome and duration, and never the payload or result.
13. `GET /healthz` returns 200 only when the database answers, otherwise 503.
14. `task deploy-assistant` builds and pushes the image to the local registry, applies the schema, and deploys to the `app` namespace. The admin interface is then reachable at `http://assistant.localhost:8080/admin/`.
15. Tests are isolated, deterministic and extensive: every criterion above is covered by at least one test, and the edge cases listed in the Approach each have their own. No test calls a real LLM or needs network access beyond the test database. The suite passes with no Ollama reachable. Database tests use `ASSISTANT_TEST_DATABASE_URL` and print a visible skip reason when it is unset. `task test-assistant` runs the Go tests with it set from the dev cluster, and `task test-assistant-frontend` runs the component tests.
16. Each Web Component has a `component.test.js` run under Node and `jsdom` (a dev-time-only dependency; a small per-app `package.json`), covering rendering, empty and error states, paging, the status filter and the live refresh, with `fetch` mocked.
17. A task created through `POST /tasks` with the `noop` workflow reaches `succeeded` on the dev cluster, and the admin page shows it with a queue time and execution time.

## Approach

Go 1.26 and the repo's existing stack: `go-chi/chi` v5 for routing, `jackc/pgx` v5 with `pgxpool` for Postgres (shared helpers in `shared/postgres` where they fit), no ORM. The only new library is the Prometheus client (`prometheus/client_golang`).

Package layout, one responsibility each:
- `main.go`: flags `migrate` and default serve, slog setup.
- `internal/config`: reads and validates the environment.
- `internal/db`: embeds `schemas/db/schema.sql` and applies it.
- `internal/users`, `internal/tasks`: stores. `tasks` also holds the worker. The claim query is `SELECT ... WHERE status = 'pending' ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, in the same single-running-task spirit as phraseforge's `jobs` table.
- `internal/workflows`: a small registry mapping `workflow_id` to a handler function; slice 1 registers `noop` only. The state-machine engine replaces this in a later slice.
- `internal/server`: JSON API handlers, metrics and health, and the embedded static tree for the admin SPA (`static/index.html`, `static/app.js`, `static/components/as-*/`).

Key decisions:
- **Schemas directory first.** `assistant/schemas/` is created now with `db/` and `api/`. `messages/`, `documents/`, `workflow.schema.json` and `models.schema.json` arrive with the slices that use them, so no schema exists without code behind it.
- **Timings derived.** Storing only timestamps keeps one source of truth.
- **Web Components SPA for the admin interface**, as in knowledge and phraseforge: native Custom Elements, light DOM, self-injecting styles, deliberate per-app duplication of components rather than shared files. The page layout is modelled on phraseforge: a header with the app name and navigation, content in cards, a pager under tables, and a status bar for messages, using phraseforge's theme tokens and light and dark themes. Components are `as-nav`, `as-card`, `as-pager`, `as-status-bar`, `as-task-list` and `as-task-detail`, wired by a small `app.js` with hash-based routing. The API is the only contract between page and server.
- **No authentication in this slice.** It will be replaced by a shared external auth service, so the server keeps authorization out of handlers for now rather than inventing a throwaway scheme.
- **No real LLM in any test, ever.** Slice 1 has no LLM code. The convention applies from slice 2: LLM access sits behind an interface, and tests use a scripted fake that returns canned responses. This is recorded as a key convention in `tech-stack.md`.

Test strategy (extensive, all LLM-free):
- **Config:** missing and malformed variables, error messages name the variable.
- **Schema and migration:** `assistant migrate` is idempotent; constraints reject a bad `status` or `source`; foreign key rejects an unknown user; `telegram_id` uniqueness.
- **Stores (database):** create and list users; create, get and list tasks; paging and ordering; derived queue and execution times.
- **Worker (database):** claims oldest pending first; never two running at once; `succeeded` and `failed` transitions set the right timestamps; `noop` echoes payload and honors the `sleep_ms` cap (using an injected clock, never real sleeping); unknown workflow fails with the exact message; a crashed `running` task is failed at startup; concurrent claim attempts yield one winner.
- **API (`httptest`):** each endpoint's success and every 400, 404 and 500 path; error body shape; paging parameters; `source` forced to `api`.
- **OpenAPI sync:** router routes equal the file's routes in both directions.
- **Metrics:** counters and histograms move by the expected amounts after a task runs, using a fresh registry per test.
- **Logging:** a task's log lines carry id, workflow, outcome and duration and never contain payload or result text.
- **Health:** 200 with a working database, 503 when the pool is closed.
- **Frontend (jsdom, `fetch` mocked):** each component's rendering, empty, loading and error states, paging, status filter, live refresh start and stop, and escaping of task text so payloads cannot inject markup.

Ordered plan (each step ends with build and tests green):
1. Module, config, `slog`, `/healthz`, `/metrics`, Dockerfile, `.env.example`, with tests.
2. `schemas/db/schema.sql`, `assistant migrate`, users and tasks stores, with tests.
3. REST API for users and tasks, plus `openapi.yaml` and the route-versus-file test.
4. Worker, `noop` handler, task metrics and logs, with tests.
5. Admin SPA: static shell with phraseforge-style layout, `as-nav`, `as-card`, `as-pager`, `as-status-bar`, `as-task-list`, `as-task-detail`, `task test-assistant-frontend`.
6. Taskfile targets and Kubernetes manifests, then the dev-cluster acceptance check (criterion 17).

## Affected Areas

- New: `assistant/` (module, `schemas/`, embedded `static/` SPA with `package.json` for component tests, `Dockerfile`, `k8s/`, `README.md`, `CHANGELOG.md`, `.env.example`).
- `Taskfile.yml`: new build, deploy and test targets for `assistant`, including `test-assistant` and `test-assistant-frontend`.
- `.gitignore`: `.env`.
- Constitution (separate gated update, see Documentation Updates): `specs/tech-stack.md` (artifact row, new Postgres image, Prometheus client, schemas convention), `specs/mission.md` (the three-apps wording), `specs/artifacts/assistant/roadmap.md` (new).

## Out of Scope

- Any LLM call, model router, or cost and token columns in the admin view (they need the `llm_calls` table in the model-router slice).
- Workflow YAML, the state-machine engine, the response reviewer and fix loop.
- Telegram, NATS, Garage, the scheduler, memory, RAG and web search.
- Authentication and authorization (to come from a shared external service).
- Server-rendered admin pages.
- Cancelling or retrying tasks from the admin page.
- Release workflow in GitHub Actions and deployment to the production cluster.
- Per-user metric labels (cardinality) and cost metrics.

## Implementation Notes

All six steps were done in order, on `main`, nothing committed. The workflow engine's module (`assistant/`, built earlier) is the base; this slice added:
- `internal/config` (environment and `.env`, errors that never echo a secret), `internal/metrics` (a registry per instance), `internal/server` (router, `/healthz`, `/metrics`, the JSON API, the embedded admin interface), `internal/db`, `internal/dbtest` (a schema of its own per test), `internal/users`, `internal/tasks` (stores, queue operations and the worker), `internal/workflows` (the `noop` registry), `app.go` and `main.go` (`serve`, `migrate`, `workflow validate`).
- `schemas/db/schema.sql`, `schemas/api/openapi.yaml`.
- The admin SPA in `internal/server/static/`: `index.html`, `theme.css`, `theme.js`, `app.css`, `app.js` (hash router), `format.js`, `api.js`, and the components `as-nav`, `as-card`, `as-status-bar`, `as-pager`, `as-task-list`, `as-task-detail`, each with its own `component.test.js`.
- `Dockerfile`, `.dockerignore`, `k8s/` (Deployment, Service, Ingress, migrate Job), `.env.example`, `.gitignore` entries, and in `Taskfile.yml` the targets `test-assistant`, `test-assistant-frontend`, `deploy-assistant`, `delete-assistant`, `migrate-assistant-db`.

Names confirmed by the user before the API was written: the list envelope `{items, total, limit, offset}`, query parameters `limit`, `offset`, `status`, the derived `queue_seconds` and `execution_seconds`, and the error codes `invalid_request`, `not_found`, `conflict`, `internal`.

Where it went beyond or differs from the Approach:
- `as-task-list` and `as-task-detail` load their own data and refresh themselves (rather than a separate view layer), so the live refresh is a tested component behaviour; `app.js` only routes and updates the status bar. Shared helpers are `format.js` and `api.js`.
- The derived times are computed by the server (`queue_seconds`, `execution_seconds`), so a browser's clock cannot skew them.
- A source or status in a `POST /tasks` body is refused as an unknown field, so an API task cannot claim to come from Telegram. Request bodies are limited to 1 MB.
- `OpenAPI` is a tested contract: every response in the API tests is validated against its documented schema and status, and the router and the file must list the same routes in both directions; a further test checks that the SPA calls only documented operations.
- The admin files are served with a strict Content-Security-Policy (no inline script or style, nothing from elsewhere, no framing); a test checks that `index.html` has no inline script, style or event handler.
- The Deployment's liveness probe is a TCP check and its readiness probe is `/healthz`, so a database outage takes the pod out of service without restarting it. Strategy is Recreate: one worker, one pod.
- `assistant migrate` applies the schema; the Taskfile creates the `assistant` database and its Secret (built from the generated Postgres credentials) before the migrate Job.
- A panic in a handler is answered 500 in the API's error shape and logged, instead of chi's default recoverer; a panic in a workflow fails its task and the worker goes on.
- The database used while spiking pgvector (`assistant`) held a leftover `memories` table of mine; I dumped it to the scratchpad and dropped it before the first migration.

Bugs found by the tests and by looking at the running page, and fixed: the task list's `as-load` event said it was not refreshing when it was (it fired before the refresh was scheduled); the task page requested `/api/v1/tasks/` with no id because the router set the id after connecting the element; and on a phone the page scrolled sideways because a bare grid column grew to the table's width.

## Validation

- `task test-assistant` (vet, `go test -race`, with the dev cluster's Postgres): all 10 packages pass. 469 tests and subtests, none skipped, with the database tests running (the test database's commit counter grew by about 900). Coverage: `internal/config` 96%, `metrics` 100%, `server` 96%, `tasks` 95%, `users` 91%, `workflows` 98%, `workflow` 94%, `db` 67%, `assistant` (main package) 54%, the last two because `serve()` and the graceful shutdown are exercised by the deployed run, not by a test.
- `task test-assistant-frontend`: 146 tests pass (jsdom, `fetch` and timers mocked).
- The test task's cleanup was checked: no port-forward is left behind after a passing or a failing run, and a failing test still fails the task.
- Mutations, each caught: claiming the newest task first, `Finish` ignoring the status, logging the result, dropping the one-running index, tolerating an empty error message, removing an operation from the spec, returning 200 for 201, demanding a field the server does not send, an undocumented route, a wrong endpoint in the SPA (43 JS failures and the Go contract test), `innerHTML` in the list, refreshing finished tasks, and trusting any task id.
- On the dev cluster (`task deploy-assistant`): the migrate Job completed (and ran again on the second deploy without error), the pod is ready, and a `noop` task with `sleep_ms` 1500 queued through the ingress went pending, running, succeeded with result equal to its payload, queue time 0.45 s and execution time 1.51 s; a task with an unknown workflow failed with exactly `unknown workflow: does-not-exist`; `/metrics` shows the counters and histograms; the pod log has one start line and one finish line per task with no payload.
- The admin page was driven in Chromium (Playwright) at 1280, 1024, 420 and 360 px, light and dark: no console error, page error, failed request or CSP violation; waiting and running times count up (`0.2 s…`, `2.8 s…`) and the status bar says it is refreshing; the page no longer scrolls sideways on a phone; a missing task shows "Task not found." Screenshots were inspected.
- Image: about 4 MB compressed in the registry (the static binary is 13.3 MB), under the 50 MB threshold.
- Not covered: authentication (planned as a shared service), behaviour under load, and a two-pod deployment (the worker assumes one).

## Documentation Review

- `assistant/README.md` (rewritten for the queue, the API, the admin interface, running, deploying and testing), `assistant/CHANGELOG.md` (four entries under Added).
- Constitution: `specs/tech-stack.md` already records the schemas-as-contracts and no-real-LLM conventions, the `as-*` prefix and the per-app jsdom tests; no change is needed. The `## Now` roadmap line is removed at delivery.
- Durable knowledge appended to `specs/memory.md` (5 entries).

## Documentation Updates

Done: README, CHANGELOG, five memory entries. The `## Now` line of `specs/artifacts/assistant/roadmap.md` was removed at delivery (approved by the user).
