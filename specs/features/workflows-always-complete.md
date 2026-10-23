---
title: Every workflow can complete, and a conversation can always be ended
kind: feature
status: done
version: 3
updated: 2026-10-23
branch: main
---

## Problem / Motivation

In the prod cluster jdpct101 (pod log read 2026-10-23, image assistant:2026.10.3) the orchestrator task 66c09fa7 went through 6 start/waiting cycles between 22:04 and 22:13 UTC and never finished; the user could not end the conversation and had to delete the conversation and the task. `ui-compose` failed in 4 of 7 runs because a small model could not write valid JSON/A2UI (see specs/features/fix-ui-compose-invalid-replies.md); the user said the model is not capable of producing a UI and accepts a fall back to text, as long as every workflow can complete, orchestrator included.

Facts from the code:

- Deleting a conversation only runs `DELETE FROM conversations` (assistant/internal/chat/store.go:178); the task keeps running or waiting.
- The chat service's `Tasks` interface has only Create, Get and Resume (internal/chat/service.go), and its only command is `/help`.
- `tasks.Store.Cancel` ends a pending or waiting task at once and returns ErrNotCancellable for a running one, which only the in-process `Worker.Cancel(id)` stops (internal/server/task_actions.go:15-35).
- After a cancelled task the next message starts a new run (chat `currentRun` treats it as no current run).
- `on_error: <state>` exists: for retries_exhausted, llm_error and max_visits the run moves to that state once per run with {"error":{"reason","message"}} as the observation (internal/workflow/engine.go:225,300-318).
- Today only learn-topic.yaml uses on_error (4 uses); ui-compose, orchestrator, hello-live and ui-demo have none (hello is the demo model's deterministic workflow).

Assumptions from the user's answers: waiting tasks do not expire on their own; `/end` only cancels the run and the conversation stays open.

## Acceptance Criteria

1. `/end` in a conversation (any channel, handled in chat.Service.Send like `/help`) cancels the conversation's current task: a pending or waiting one through the task store, a running one through the worker's cancel; it posts an `info` message: `Ended the current run. Send a message to start again.` for a pending/waiting task, `Stopping the current run; it ends in a moment. Send a message to start again.` for a running one, and `Nothing is running.` when there is no task or it already finished. The conversation stays open; the next message starts a new run. `/help` lists `/end`.
2. One shared cancel routine is used by the HTTP task cancel and by the chat, so both behave the same (the HTTP responses stay as they are).
3. Deleting a conversation (DELETE /api/v1/conversations/{id}) first cancels its task the same way, keeps the task record (status cancelled), then deletes the conversation; 204 as today; a task that already finished needs no cancel; if the cancel fails for another reason the answer is 500 and the conversation is NOT deleted.
4. ui-compose: `compose` declares `on_error: plain`; a new state `plain` asks the user the original request as plain text (ask_user without `ui`) and, when the answer arrives, records it in `received` and goes to `summarise`, which is unchanged; the run ends `succeeded`. The data key for the recorded answer is `answer` (received.answer), approved by the user on 2026-10-23. Test with the scripted model: three invalid replies in `compose` lead to `plain`, a text question, the user's answer, and a succeeded run whose trace shows the on_error edge. The engine's on_error observation now also carries what the failed step had been given, under the key `observation` (constant workflow.FailedObservationKey), so that `plain` can ask the user's request in plain words; this key and its name are a deviation from the approved Approach (which said no engine change) and are listed for the user's confirmation at final approval.
5. orchestrator: `route` declares `on_error: give_up`; a new state `give_up` records a short apology saying what went wrong, in plain words, in `reply` and goes to `done` (done itself cannot be the target because `reply` is required). Scripted test: three invalid replies lead to give_up and a succeeded run with `reply` set. If the fallback state itself fails, the run still ends as failed (on_error is taken once per run).
6. hello-live and ui-demo each declare `on_error: fallback` with a state `fallback` that finishes the run in plain words (the engineer writes the instructions from each workflow's own purpose); scripted tests per workflow.
7. A guard test over the shipped workflow files in assistant/workflows fails for a workflow that neither declares `on_error` nor is on an explicit allow-list (`hello`, and any workflow named with a reason in the test).
8. assistant/README.md (chat commands) and assistant/CHANGELOG.md [Unreleased] are updated; the changed workflow YAML reaches prod only when the chart that supplies the workflow ConfigMap is updated (UNVERIFIED where the chart takes the files from).

## Approach

Memory consulted: cancel|/end|conversation|chat, on_error|fallback|orchestrator, waiting|ask_user — 3 relevant entries (a cancel stops a running task by cancelling its run context in the worker; a 12B local model skips ask_user; constrained decoding fixes syntax, not choices)

Small slices, each shippable. Ordered plan:

1. The shared cancel routine (tasks or server package, used by task_actions.go and the chat); the chat's Tasks interface gains Cancel and a stop-running hook wired to the Worker in assistant/app.go; `/end` and `/help`; tests.
2. Service.Remove cancels then deletes; tests (fake worker for the running case).
3. ui-compose fallback and test.
4. orchestrator fallback and test.
5. hello-live and ui-demo fallbacks and the guard test.
6. README, CHANGELOG.

Deviation found in step 3: the fallback state could not know the user's request, because on_error replaced the observation with only the error; the engine's redirect (internal/workflow/engine.go) now adds the failed step's observation under `observation`. Steps 4 and 5 were done together.

Key decisions: `/end` ends the run, not the conversation; cancel keeps the task record; no timeout for waiting tasks; fallbacks reuse the existing on_error mechanism, no engine change.

Risks:

- UNVERIFIED whether the task-finished notifier tolerates a deleted conversation when a running task ends after the delete (must be checked in step 2; it must ignore it quietly).
- A message sent while a running task is still stopping gets the existing 409 busy answer.
- The chat API has no authentication today (the user id comes from the request body), so any caller can cancel or delete a conversation: not changed here, tracked by the roadmap item assistant-auth.
- A fallback state is itself a model step and can fail, then the run fails (still a finished task).

Testing strategy: scripted models and fakes, no real model calls; database-backed tests run with `task test-assistant`.

## Affected Areas

assistant/internal/chat/ (service.go, facade.go, store.go as needed), assistant/internal/server/ (chat_api.go, task_actions.go), assistant/internal/tasks/ (shared cancel routine), assistant/app.go, assistant/workflows/ui-compose.yaml, orchestrator.yaml, hello-live.yaml, ui-demo.yaml, a guard test (internal/workflows or internal/workflow), assistant/README.md, assistant/CHANGELOG.md, tests next to each.

## Out of Scope

Expiry of waiting tasks; authentication of the chat API (roadmap: assistant-auth); the NATS message envelope; typing the ui-compose reply further; a typed error from internal/workflows; the topic-conversation workflow that remembers answers; the production chart that supplies the workflows; Telegram delivery beyond the existing channel interface.

## Implementation Notes

Step 1: new internal/tasks/cancel.go `CancelOrStop(ctx, store CancelStore, worker Stopper, id) (Task, CancelOutcome, error)`, outcomes CancelledNow and Stopping, small interfaces CancelStore and Stopper; the HTTP handler internal/server/task_actions.go now uses it with identical responses; chat.Service gets `Canceller` (app.go `runCanceller`, wired at app.go:215), `/end` handled in command() via new end(); texts: `Ended the current run. Send a message to start again.` (pending/waiting), `Stopping the current run; it ends in a moment. Send a message to start again.` (running), `Nothing is running.` (no task, not found, finished, or finished meanwhile), `Ending a run is not available here.` (no Canceller); `/help` lists `/end: end the current run`; if the post-stop Get fails the error is wrapped with %v so the HTTP handler keeps answering 500 as before. Tests: internal/tasks/cancel_test.go (6, no database), internal/chat/end_test.go (8, database-backed).

Step 2: internal/chat/facade.go Service.Remove: Store.Get, then Canceller.Cancel when the conversation has a task (ErrNotCancellable and ErrNotFound tolerated; any other error returned wrapped `cancel the conversation's run before removing it: %w` and the conversation is NOT deleted), then Store.Remove; the task record stays; Store.ForTask already reports no conversation as ok=false so TaskUpdated returns quietly (pinned by a test); schemas/api/openapi.yaml DELETE description updated (it said the task is not touched). Tests: internal/chat/remove_test.go (8), internal/server/chat_remove_test.go (2), app_engine_test.go (1), all database-backed.

Step 3: engine.go redirect adds `observation` (FailedObservationKey) when the failed step had one; ui-compose.yaml: compose `on_error: plain`, new state `plain` (asks the request as a plain question without ui; records the answer in received.answer; goes to summarise). Tests: engine_test.go (2), ui_compose_test.go (end-to-end with the three prod invalid replies, and a drift test between the YAML text and the constant), export_test.go exposes ProdInvalidReplies.

Steps 4-5: orchestrator.yaml `route` `on_error: give_up` and state give_up (short apology into reply, then done); hello-live.yaml `on_error: fallback` on ask and greet with state fallback; ui-demo.yaml `on_error: fallback` on tour, device and form with state fallback; hello.yaml unchanged (deterministic). Tests in internal/workflow/fallbacks_test.go: the guard TestEveryShippedWorkflowDeclaresOnErrorOrIsAllowListed (allow-list: hello) and three fallback tests.

Deviations: none beyond the engine key above and the openapi edit.

## Validation

Baseline before this feature (state after the previous bugfix, database supplied, -race, -v): 626 pass, 0 fail, 1 skip. After each step, same command: 640 (step 1), 651 (step 2), 655 (step 3), 659 (steps 4-5): final 659 top-level passes, 0 fail, 1 skip (TestARealModelComposesAnInterface, needs UI_TRIAL_OLLAMA, a real model call, deliberately not run); the database was the dev cluster's Postgres, database assistant_test, through a port-forward with explicit kubeconfig .k3d/kubeconfig (context k3d-k8s-lab), stopped afterwards with the port free; `task test-assistant` exits 0 after each step; gofmt and go vet clean; `assistant workflow validate` ok for all 6 workflow files.

Fail-before checks: step 2 with the cancel disabled, 3 new tests failed (TestRemoveCancelsAWaitingOrPendingRunAndKeepsItsRecord, TestRemoveOfARunningRunAsksTheCancellerOnceAndDeletes, TestRemovingAConversationCancelsItsWaitingRunAndKeepsTheTask) and the file was restored byte-identical; step 3 with the key disabled, 2 tests failed; step 5 with both on_error lines removed from hello-live.yaml the guard test failed, file restored.

Real artifact against realistic input (2026-10-23): this build was deployed to the dev cluster (`task deploy-assistant`, context k3d-k8s-lab, exit 0, rollout ok) and exercised through the real HTTP API with the demo model (no Ollama): a conversation on the orchestrator, message `hello` routed to workflow hello, which asked `What is your name?` and waited; `/end` ended it (info `Ended the current run. Send a message to start again.`, task status cancelled); a second `/end` said `Nothing is running.`; `/help` listed `/end`; the next message started a new run (a different task, waiting); DELETE of the conversation returned 204, a GET of it 404, and its waiting task was cancelled and still present; the three tasks the test created were then deleted, leaving the 7 waiting tasks of the dev database unchanged.

Not validated: the behaviour of the small real models with the plain, give_up and fallback instructions (scripted models only); the on_error edges of ui-demo `device` and `form` and hello-live `greet` (only the first model-driven state of each is tested); what happens if a fallback state itself fails (the engine takes the edge once per run, covered by earlier engine tests); a running task stopped through a real worker by /end or delete (fakes only; the worker's own cancel is covered by existing tests); the prod chart picking up the changed workflow files (UNVERIFIED); the dev cluster now runs this build.

## Documentation Review

Documentation checked with the `doc` skill against this feature:

- assistant/README.md: drift at :86-88 (chat commands: `/end` missing) and :89-96 (deleting a conversation, not described) and :347-366 (Workflow engine: `on_error` not described). Fixed, see Updates.
- assistant/CHANGELOG.md:5-65 ([Unreleased]): no entries for `/end`, the fallbacks or the delete change. Fixed, see Updates.
- assistant/schemas/workflow.schema.json:135 (`on_error` description): "State to go to, once per run, when a step in this state fails for good." Still true; it does not mention the `observation` the target receives. Low, not edited (not in the brief's edit list); suggested addition: "The observation carries the error and what the failed step was given."
- specs/mission.md: no mention of chat commands, cancelling or workflow fallbacks; no drift.
- specs/tech-stack.md: no mention of them; no drift.
- specs/artifacts/assistant/roadmap.md: only the `## Now` line of this feature is affected (removed, see Updates); no other line describes `/end`, conversation deletion or fallbacks.
- Drift in other specs (report only, not edited): specs/features/assistant-workflow-engine.md:123 and :40 describe `on_error` ("with the failure as its observation") without the `observation` key now added; specs/features/workflows-always-complete.md:20 (this spec's Problem section) records the observation as {"error":{...}} only, which was true before the feature and is kept as the historical fact. assistant/schemas/api/openapi.yaml was updated in step 2 (DELETE description).

## Documentation Updates

- assistant/README.md: the Chat bullet in "Chat and tidying the queue" now says `/end` ends the conversation's run (a pending or waiting one at once, a running one in a moment) and the conversation stays open, and that deleting a conversation cancels its run first and keeps the task record; the "Workflow engine" section has one new bullet on `on_error: <state>` and its observation `{"error": {...}, "observation": <what the failed step was given>}`, the shipped workflows' use of it and the guard test.
- assistant/CHANGELOG.md [Unreleased]: two `### Added` lines (`/end`; `on_error` fallbacks and the `observation` key) and one `### Changed` line (deleting a conversation cancels its run first).
- specs/artifacts/assistant/roadmap.md: this feature's `## Now` line removed; no version bump.
- specs/memory.md: two entries appended ([convention], [gotcha]).
- No constitution file (mission.md, tech-stack.md) changed; no constitution change was needed.
- Not edited, proposed: workflow.schema.json:135 description (see Review).
