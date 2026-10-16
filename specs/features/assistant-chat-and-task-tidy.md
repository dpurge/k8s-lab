---
title: Chat to test workflows by hand, and tidying the task queue
kind: feature
status: done
version: 1
updated: 2026-10-16
branch: main
---

## Problem / Motivation

With the engine in the worker, the only way to try a workflow by hand was the task page and `curl`. The user needs a chat interface to test conversations with the assistant (the shape Telegram will take) and a way to clean up the queue that testing fills: cancel, retry and remove tasks. A real Telegram is not available yet, so the channel is a mock and the page is the user's side.

Decisions confirmed by the user (2026-10-16): the interface to test is the first priority; several conversations per user; a `/help` command; a way to cancel, remove and retry tasks.

## Acceptance Criteria

Chat
1. Tables `conversations` (user, workflow, title, the run it is in, a Telegram chat id for later) and `messages` (conversation, role user or assistant, kind text, question, result, error or info, text, the run it came from). Removing a task clears the conversation's run and keeps the history; removing a conversation removes its messages and leaves its task.
2. A conversation can run a YAML workflow or `ask`, nothing else; it is refused with a clear message otherwise. A user can have several.
3. A message: starts a run (task of source `telegram`, payload `{"message": text}`, or `{"prompt": text}` for `ask`) when the conversation has no run or the run has finished, failed or been cancelled or its task removed; is the answer when the run waits; is refused with 409 and not stored while the run is pending or running. A blank message is refused (400).
4. The assistant's side is spoken by the run: when it waits, the question (with numbered options); when it succeeds, the result (a greeting or summary, or the model's text for `ask`, or the final state); when it fails, the error in full; when it is cancelled, a note. If a message cannot be stored or delivered it is logged and counted (`assistant_chat_notify_errors_total`), never dropped silently. A failure to start or answer a run is said in the conversation and returned.
5. `/help` is answered at once, even while the run works, and lists the commands and the workflows a conversation can run; an unknown command says so. Commands start no run.
6. `GET/POST /api/v1/conversations`, `GET/DELETE /api/v1/conversations/{id}`, `GET/POST /api/v1/conversations/{id}/messages` (with `after`), each conversation showing the state of its run and its last words; all in `openapi.yaml`. The channel is an interface with a mock.
7. Admin page `#/chat`: the conversations (start one: user, workflow, title), the open thread with the messages by kind, the state of the run in words, a link to the task, a message box (Enter sends, Shift+Enter adds a line) that is closed while the assistant works, and delete. It refreshes quickly while the run works. Every failure (load, send, start, delete) is shown next to where it happened.

Tidying
8. Cancel (`POST /tasks/{id}/cancel`): pending and waiting tasks at once (200); a running task is told to stop and ends as cancelled (202); anything else is 409. A run stopped by a cancel is recorded as cancelled however it ended.
9. Retry (`POST /tasks/{id}/retry`): a failed or cancelled task gives a new pending task with the same user, workflow, source and payload; the original stays; others are 409.
10. Remove (`DELETE /tasks/{id}`, 204; 409 if running) and remove all of one finished status (`DELETE /tasks?status=`, 400 for anything else). A removed task takes its trace; its model calls stay without a task, so usage and cost do not change.
11. The task page has the buttons for its status (Cancel or Stop, Retry, Remove; Remove takes two clicks) and shows a refusal beside them; the list has "Remove all N <status> tasks" for a finished filter, with the same two clicks.
12. `GET /api/v1/workflows` lists the built-in and YAML workflows with a description and a payload to start from; the New task form is a dropdown of them (it showed only the workflows matching what was typed before), and the chat form offers those a conversation can run.
13. Tests: no real model; `task test-assistant` and `task test-assistant-frontend` pass.

## Approach

`internal/chat`: a store, a service that turns messages into runs and a worker `Notifier` that turns task changes into the assistant's messages, a `Channel` interface and a mock. The worker tells the notifier after a task waits, finishes, fails or is cancelled. Cancel of a running task cancels the run's context in the worker. The `hello` workflow records `asked` so that any first message works. Frontend: `as-chat-list`, `as-chat-thread`, `as-task-actions`, and changes to `as-new-task`, `as-task-list`, `as-task-detail`.

## Affected Areas

- `schemas/db/schema.sql`, `internal/chat`, `internal/tasks`, `internal/workflows`, `internal/server`, `internal/metrics`, `app.go`, `schemas/api/openapi.yaml`, the admin components, `workflows/hello*.yaml`, `internal/llm/demo`, README and CHANGELOG.

## Out of Scope

- A Telegram-shaped mock endpoint (`POST /telegram/updates`) and the real Telegram channel; mapping a chat command such as `/learn` to a workflow; streaming replies; typing indicators; authentication.

## Implementation Notes

Done as specified, on `main`, nothing committed. Notes:
- A cancelled pending or waiting task is cancelled in the store; the chat is told only for runs the worker ends, so a task cancelled from the task page shows in the conversation's state line, not as a message.
- Execution time of a task still includes the time it waited for the user.
- `hello` and `hello-live` now keep an `asked` flag in the run state (a model that took the first message as the name made this necessary).

## Validation

Tests: Go `-race` passes (21 packages), frontend 320 pass. Browser run against the deployed assistant (Playwright): a `hello` conversation on the demo model (question, answer, greeting, `/help`), a `hello-live` conversation on `gemini-2.5-flash-lite` on Vertex (the same, about 10 s), the page at 1280, 1024 and 420 px in light and dark with no sideways scroll and no console errors, a waiting task cancelled, retried and removed from its page, and the bulk button shown for the cancelled filter. Mutation checks were limited to the engine and worker earlier; the chat and tidy code were not mutation-tested. `learn-topic` through the chat was not run to its end.

## Documentation Review

`assistant/README.md` (new Chat and tidying section), `assistant/CHANGELOG.md`, the roadmap's `assistant-telegram` item (the chat page and mock channel are delivered; the Telegram-shaped endpoint and the real channel are not).

## Documentation Updates

Done: README, CHANGELOG, three entries in `specs/memory.md`.
