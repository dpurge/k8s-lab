---
title: Workflow engine in the worker, with a run trace and a run view
kind: feature
status: done
version: 1
updated: 2026-10-16
branch: main
---

## Problem / Motivation

The workflow engine is a library that nothing runs: the queue's worker knows only `noop` and `ask`. Nothing in the deployed assistant can walk a YAML workflow, suspend it on a question to the user, or resume it with the answer, so the core of the assistant cannot be tried by hand. This task connects the engine to the worker and the router, stores a suspended run so a later message resumes it, records a trace of every step, and shows all of it in the admin interface, where the user can answer a waiting run. Telegram, conversations and the chat page build on it later (`assistant-telegram`).

Decisions confirmed by the user (2026-10-16): the workflow engine is the core, and the first deliverable is a deployed interface to test workflows by hand; several conversations per user later; no database backups for the new app; tests must be written but not tuned for speed.

## Acceptance Criteria

Running
1. A task whose `workflow_id` is not a built-in (`noop`, `ask`) runs the YAML workflow of that id through the engine, with the router as the model and the registered tools. A task naming neither is failed with `unknown workflow: <id>`.
2. Workflows are loaded at startup from the directory in `ASSISTANT_WORKFLOWS_DIR` (default `/etc/assistant-workflows`): every `*.yaml` is validated against the schema and linted with the registered tool names. Startup fails naming the variable, the file and the problems. A workflow id that collides with a built-in is refused.
3. The task payload is `{"message": any, "state"?: object, "language"?: string}`, passed to the engine as its input; a payload that is not that shape fails the task with a clear message. The engine's run id is the task's id.
4. When the run succeeds the task succeeds with result `{"state": <final run state>}`. When it fails the task fails with `<reason>: <message>` of the engine's failure. The final run snapshot is kept on the task either way.

Waiting and resuming
5. A new task status `waiting`: the run asked the user something. The task keeps the run snapshot (`run`) and the question (`ask`, the `ask_user` payload), is not running (so the queue is not blocked), and survives a restart of the process.
6. `POST /api/v1/tasks/{id}/messages` with `{"message": <any JSON>}` on a waiting task queues it again as pending with the message; the worker resumes the run with it, and the task becomes running, then succeeded, failed or waiting again. Another status is a 409 `conflict`, an unknown task a 404, a missing message a 400. Answering twice before the worker has picked it up is a 409.
7. Queue time stays the time to first start. Resuming does not reset `started_at`.

Trace
8. Table `task_steps` holds one row per engine step attempt: task, step, attempt, state, outcome, the engine's error, the action, the state patch, the tool and a short summary of its result, tokens, duration and time, and the model's raw reply cut at 8 KB. Unlike `llm_calls` it holds replies, on purpose: it is the debugging trace of the engine, stored in the database only, never logged.

API, admin, deployment
9. `GET /api/v1/tasks/{id}` returns `run` and `ask` (null when there is none); the list returns them as null to stay small. `GET /api/v1/tasks/{id}/steps` returns the trace, oldest first. `waiting` is a valid status filter. All of it is in `openapi.yaml`.
10. Stub tools `memory_search`, `rag_search` and `web_search` return canned, clearly labelled results, so `learn-topic` can run before the real tools exist.
11. The admin task page shows the current state, the run state, the question when the run is waiting with a box to answer it, and the trace (each step's action, tool, outcome, retries, model tokens and duration, and the error of a refused reply). The task list shows `waiting` with its own badge and filter.
12. `assistant-workflows` ConfigMap built from `assistant/workflows/*.yaml` by `task deploy-assistant`, mounted at `/etc/assistant-workflows`. A `learn-topic` task is run on the dev cluster through the host's Ollama, answered once from the page, within the bounds of the earlier real-model checks, and reported.
13. Errors are visible, never swallowed: a failed task shows its error in the list (an error badge with the first line on hover or in a column) and as a prominent block at the top of its page, with the engine's reason (`retries_exhausted`, `llm_error`, `max_steps`, ...); every refused reply, failed tool call and failed model attempt shows in the trace with its error text, in red; a failed resume (409, 404, 400, 500) shows its message next to the answer box; a failed load of any admin component stays in the status bar until it recovers; workflow loading problems stop startup with the file, line and message; a trace row that cannot be written is logged and counted (`assistant_trace_record_errors_total`), not silently dropped.
14. No test calls a real model; engine and router run against scripted fakes. `task test-assistant` and `task test-assistant-frontend` pass.

## Approach

`tasks.Runner.Run` returns an `Outcome`: a result, or a suspension (run and ask) when the run waits. `workflows.Registry` keeps the built-ins and adds an `EngineRunner` for YAML workflows: it starts a run, or restores `task.run` and resumes it with `task.resume_message`, then maps the engine's result to an outcome. The store gains `Suspend` (running to waiting) and `Resume` (waiting to pending); `Claim` keeps an existing `started_at`. Replies to a waiting task go back through the queue so the slow model calls happen in the worker and the one-running-task rule holds. A DB `Recorder` writes `task_steps`.

Known limit: execution time is finished minus first start, so it includes the time a task waited for the user. A separate active time can come later.

Ordered plan (each step ends green with a short report): 1 store, schema, runner and worker, with the resume endpoint; 2 stub tools, workflow loading, trace recorder; 3 steps endpoint, run view and waiting status in the admin; 4 ConfigMap, deploy, one bounded real run of `learn-topic`, docs.

## Affected Areas

- `assistant/schemas/db/schema.sql`, `internal/tasks`, `internal/workflows`, `internal/workflow` (a DB recorder lives beside the stores), `internal/server` (API and admin components), `internal/config`, `app.go`, `schemas/api/openapi.yaml`, `k8s/`, `Taskfile.yml`, README and CHANGELOG.

## Out of Scope

- Telegram, conversations and the chat page (`assistant-telegram`), real memory, RAG and web search tools, engine-owned `pending`, constrained decoding, an active-time measure, cancelling a waiting task, expiring waiting tasks.

## Implementation Notes

Done on `main`, nothing committed, in the planned order with these additions and deviations:
- `tasks`: status `waiting`, columns `run`, `ask`, `resume_message`; `Store.Suspend`, `Store.Resume`; `Claim` keeps the first `started_at`; `Finish` also takes the final run; the list leaves `run` out. The schema upgrades an existing database (drop and re-add the status check, `ADD COLUMN IF NOT EXISTS`), covered by a test that migrates an old table.
- `workflows`: `Runner` (built-ins first, then YAML workflows), `EngineRunner` (start or restore and resume, outcome mapping, the run snapshot kept on failure), `LoadDir` (every problem with file and line; built-in ids reserved). `internal/tools`: four labelled stub tools. `internal/tasksteps`: trace store and recorder.
- API: `POST /tasks/{id}/messages`, `GET /tasks/{id}/steps`, `run` and `ask` on tasks, `waiting` as a status; OpenAPI updated and held by the existing contract tests.
- Admin: `as-answer-box`, `as-run-trace`, `as-new-task`; failure alert, run section, trace and question in the task page and list; the status bar keeps an error until the component that raised it recovers (done earlier for the Usage page).
- Added beyond the spec, at the user's request while testing: the `demo` provider and workflow `hello` (so the interface can be tried without a model), the `vertex` provider with `task refresh-assistant-vertex-token` and workflow `hello-live`, and a "New task" form. `ASSISTANT_WORKFLOWS_DIR` defaults to `/etc/assistant-workflows` (a separate mount from the models ConfigMap). The `assistant-provider-vertex` roadmap item is partly delivered: user-token authentication only.

## Validation

Tests: `go test -race ./...` passes (20 packages, with the dev cluster's database) and `npm test` in the components directory 255 pass. No test calls a real model. Mutation checks, each caught and restored: run id not the task id; failure reason dropped; the worker's waiting path disabled (four tests failed); the engine's tags earlier.

Deployed to the dev cluster. Real runs, each with the model unloaded or not involved afterwards:
- `learn-topic` on `gemma4:12b` (Ollama, laptop): ran 4 steps in about 7 minutes, never asked the user, looped `assess` to `assess`, froze the laptop (health check 503, `kubectl` timeouts); stopped by restarting the pod, the task failed visibly as interrupted.
- `hello` on the same model took the starting message "hi" as the name and finished without asking.
- `hello` with the demo model, driven from the browser: waited, took the answer typed in the page, finished with three clean trace steps.
- `hello-live` and `learn-topic` on `gemini-2.5-flash-lite` (Vertex AI, project `np-te-core-ai`): `hello-live` asked, took the answer and greeted in about 10 s with no retries; `learn-topic` went `intake`, `plan`, `assess` and asked a question in about 6 s, then took an answer from the page and asked the next one (about 1 s per step).
- Screenshots at 1280, 1024 and 420 px, light and dark, no console errors; the list's columns were reduced under 1200 and 1100 px to fit.

Coverage gaps: the Vertex provider is tested against a fake server and by a handful of real calls, not under failure (expired token, 429); its prices are not set, so cost shows as $0. Execution time includes the time a task waited for the user. The demo model covers only `hello`. `learn-topic` was not run to its end on any model. The real-model reliability findings above are the open problem: the engine accepts a goto to the same state until `max_visits`, and nothing makes a model ask.

## Documentation Review

`assistant/README.md` (new Running workflows section; overview, router providers, admin and API notes updated), `assistant/CHANGELOG.md`, `specs/tech-stack.md` (no change needed: the Vertex provider is a new dependency-free HTTP client inside the assistant, and the tech stack's model routing convention still holds), `specs/mission.md` (no drift), the roadmap (this task's `## Now` line stays until delivery; the `assistant-provider-vertex` item stays in `Next` for service-account authentication).

## Documentation Updates

Done: README, CHANGELOG, four entries in `specs/memory.md`. Nothing in the constitution changed.
