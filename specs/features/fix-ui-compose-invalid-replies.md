---
title: Fix ui-compose invalid model replies
kind: bugfix
status: done
version: 2
updated: 2026-10-23
branch: main
---

## Problem / Motivation

In the prod cluster jdpct101 (namespace frontend, image `ghcr.io/dpurge/assistant:2026.10.3` = git tag `assistant-v2026.10.3` = commit 7ac5cb0), the `ui-compose` workflow failed in 4 of 7 runs on 2026-10-22/23: tasks 0eac8be8, ca487df1, c9d2a01b, 7c8b9a63. Each failed as `retries_exhausted: model gave no valid reply in 3 attempts; last problem: reply is not a valid JSON object: invalid cha…` (model gemma4-31b-gpu).

The pod log (142 lines, read 2026-10-23) records every LLM call as outcome ok and has no WARN/ERROR line giving the reason. The failure reason is not logged; it is only in the task record.

Evidence from the step trace of one failed task (three attempts, all refused):

- Attempt 1: invalid JSON ("invalid character '}' after array element"; Python json: Expecting ',' delimiter at char 510, an extra `}` in the closing run).
- Attempt 2: valid JSON but refused by the A2UI check ("a message has "version": "v1.0" and exactly one of createSurface, ... (found createSurface, dataModel, version)"), because createSurface was closed right after components so dataModel became a sibling.
- Attempt 3: invalid JSON (Expecting ',' delimiter at char 493, a stray `}`).

So the model is not wrapping the JSON in prose (an earlier hypothesis, wrong). It loses count of closing brackets in about 150 tokens of single-line JSON nested seven levels deep, and each retry breaks it somewhere else.

The A2UI shape the guide teaches (components and dataModel inside createSurface) is valid: the vendored v1.0 schema `assistant/schemas/a2ui/v1_0/agent_to_renderer.json` (google/A2UI commit 059b8d3) allows surfaceId, catalogId, sendDataModel, components, dataModel, metadata in createSurface with additionalProperties false.

The error text the model gets back is poor: it has no position, no excerpt of the reply and no hint about which brackets were open (`assistant/internal/workflow/reply.go:71`, `state.go:157` DecodeObject). The retry does already show the model its previous reply and the error (`engine.go` Retry{Reply, Error}).

The reply JSON schema is already sent to Ollama as `format` (`workflow/request.go:100` sets Schema; `shared/router/ollama/ollama.go:126`; added in commit 931ee23, 2026-10-21, which the prod image includes). But ReplySchema (`request.go:163-247`) leaves state_patch and the ask_user payload as bare {"type":"object"}, so the interface is unconstrained. Yet prod still produced invalid JSON. UNVERIFIED: why. Whether the ollama-gpu host's Ollama version enforces `format` for gemma4:31b is not known.

Dev replay (2026-10-23, dev Ollama 0.40.8, gemma4:12b, think false, num_predict 2048, the real compose request, 2285 prompt tokens vs 2281 in the prod trace; one call each, so weak evidence):

- With the schema as `format`: the reply was valid JSON but chose action goto summarise and skipped the interface (the schema allows it).
- Without `format`: the reply was invalid JSON (escaped quotes `variant\":\"primary\"`, bracket errors, and a child "btn_text" that no component defines).

`memory.md` has one relevant entry (2026-10-16 [gotcha]: gemma4:12b without constrained decoding returned malformed JSON on 1 of 3 assistant controller steps; send the reply schema as Ollama format and retry in the engine).

## Acceptance Criteria

1. A JSON syntax refusal tells the model: the character offset, a quoted excerpt of about 40 characters either side of it with a marker, which containers were open there (path such as reply > action > payload > ui[0]), and the instruction to resend the whole reply as one JSON object. Regression tests use the three prod replies (above, from the trace) and the dev reply; each test asserts the message contains the offset and the open-container path. Whether the JSON library's error exposes the offset is UNVERIFIED; if not, a small scanner of our own supplies it.
2. When a reply is valid JSON but its A2UI message has dataModel (or components) next to createSurface instead of inside it, the refusal says so in words: "dataModel must be inside createSurface; createSurface was closed after components". Test with the attempt-2 reply.
3. Errors about a component or property keep their existing path-based form and add the expected keys of the named component.
4. When a run fails, the assistant's "task finished" log line (outcome failed) carries the failure reason and message in fixed words (never a provider's raw error text beyond what the task record already holds, never secrets). PROPOSED field names, to confirm with the user before implementing: `failure_reason` and `failure_message`. Test: a failing run logs both.
5. ReplySchema types payload.ui for a state that declares ui: an array of one createSurface message with surfaceId, a catalogId const, and components as an array of objects with id and component. Offline test: the schema accepts the guide's example and rejects the dev and prod invalid shapes it can express. Whether Ollama's grammar converter handles the typed schema is UNVERIFIED and is checked with at most 3 real calls on dev Ollama (a hard budget: the laptop cannot take more).
6. Withdrawn by the user's decision of 2026-10-23: the stronger compose wording stays and a small model may still skip the interface; falling back to text so that every workflow can complete moves to a separate spec, workflows-always-complete.
7. `assistant/CHANGELOG.md` has an entry for the user-visible parts (better refusals sent to the model, failure reason in the log).

## Approach

Memory consulted: a2ui|ui-compose, ollama.*format|structured output|format, retries_exhausted|ReplySchema|retry — 1 relevant entry (2026-10-16 [gotcha] constrained decoding / Ollama format)

Chosen approach: defence in depth in small slices, each shippable alone. Because prod already sent `format` and still failed, better refusals and the retry stay even if the schema works.

Ordered plan:

1. Better JSON refusals (criteria 1-3), offline, no model calls.
2. Failure reason in the log (criterion 4).
3. Typed payload.ui in ReplySchema (criterion 5) plus the <=3-call Ollama check.
4. Stop the interface being skipped (criterion 6). Open design question for the approval gate: ReplySchema cannot force ask_user in compose because the same state also legitimately goes `goto` after the user's ui_action observation. Options:
   - (a) Split ui-compose into a state that only asks (ask_user only) and a state that handles the answer. This needs the engine to leave an asking state after the answer, which is UNVERIFIED and may need an engine change.
   - (b) Strengthen the compose instructions and the guide only (cheap, weakest).
   - (c) Narrow the schema by the observation type in the engine (workflow-specific logic in the engine, least preferred).
   - Recommendation: try (b) first, measured with the 3-call budget, and escalate to (a) if it fails.
   - Resolved 2026-10-23: option (b) kept (wording only); (a) and (c) not done. The user accepts a text fallback instead of a guaranteed interface.

Alternative not chosen (kept in reserve): "option 1". The model writes only {"components":[...],"data_model":{...}} and the runtime builds the full createSurface message and validates it with CheckInterface as today, so the protocol output is unchanged. Use only if constrained decoding turns out not to hold up. Bracket repair in the parser is rejected: guessing intent is riskier than refusing.

Names: new log fields are snake_case per tech-stack.md and must be confirmed with the user as their own decision before code.

## Affected Areas

- `assistant/internal/workflow/reply.go`, `state.go`, `request.go`, `engine.go` (as needed)
- `assistant/internal/a2ui/check.go` and `guide.go` (hint, guide example)
- `assistant/internal/tasks/` (the task-finished log line; exact file UNVERIFIED, find it)
- `assistant/workflows/ui-compose.yaml`
- `assistant/CHANGELOG.md`
- tests next to each

## Out of Scope

- The shared NATS message envelope.
- The `chat` task source distinct from `telegram`.
- The topic-conversation workflow with state memory.
- Porting knowledge's chat.
- Ending a stuck conversation from the chat (the orchestrator task 66c09fa7 never finished in the same log; separate bugfix).
- Prod Ollama host configuration.
- Bracket repair.
- Option 1, unless step 3 fails.
- A text fallback so every workflow can complete (on_error states for ui-compose and orchestrator), a /end chat command that cancels the conversation's run, and cancelling the background task when a conversation is deleted: spec workflows-always-complete.

## Implementation Notes

Step 1 (criteria 1-2): new `assistant/internal/workflow/jsonrefusal.go` (`invalidJSONMessage`: byte offset from `*json.SyntaxError` via `errors.As`, quoted excerpt of up to 40 bytes either side with `<<HERE>>`, a string-aware scanner giving the open-container path such as `reply > action > payload > ui[0]`, "(position unknown)" when the library gives no offset, the resend instruction; message under 700 bytes). `reply.go` now builds the refusal from the fence-stripped text. `assistant/internal/a2ui/check.go` appends a hint when `dataModel`/`components` sits next to `createSurface`. Offsets 510 and 493 match Python for prod replies 1 and 3. Tests: `jsonrefusal_test.go` (3 prod/dev replies and edge cases), `ui_test.go`, `a2ui_test.go`.

Step 2 (criterion 4): `worker.go` `finishedLogAttrs` builds the "task finished" attributes; failed tasks with a KNOWN workflow reason code add `failure_reason` and `failure_message` (message cut at 600 bytes; the model-reply excerpt replaced by "[excerpt omitted]" by the new exported `workflow.RedactExcerpt` in `jsonrefusal.go`; new `FailReason.Known()` in `state.go`). The reason is parsed from the error text ("reason: message", produced at `internal/workflows/engine.go:219`) because the worker only receives an error string; a typed error would need a change in `internal/workflows` (not done). Deviation found by the database-backed tests: the first version defined `finishedLogAttrs` but did not call it, and logged the message of every failure; the existing test `TestLogsCarryIdWorkflowOutcomeAndDurationButNeverPayloadOrResult` (the privacy rule: a payload or result must never reach the log) failed. Fixed: the log call now uses `finishedLogAttrs`, and an error WITHOUT a known reason code logs neither attribute. `task.Error` in the database is unchanged and still holds the full text. Tests: `finishlog_test.go`, `worker_test.go` (`TestLogsOfAFailedTaskSayWhyWithoutQuotingTheReply`, database-backed), `jsonrefusal_test.go` (`RedactExcerpt`).

Step 3 (criterion 5): a2ui `(*Validator).InterfaceSchema()` (`guide.go`) builds the typed `ui` schema (array of exactly one createSurface message with version const "v1.0", surfaceId, catalogId const, components with id and a component enum from the catalog, optional dataModel, sendDataModel, metadata; additionalProperties false on the message and on createSurface). `workflow.Prompts` gets `UISchema` (set in `app.go` next to `UIGuide`). `ReplySchemaWithUI` in `request.go` (`ReplySchema` still works and is unchanged for workflows without ui, proved by test). The ask_user payload gets the optional typed `ui`. New keywords vs the old schema: minItems, maxItems, minLength. Also set in `internal/llm/ollama_ui_trial_test.go`. Tests: `uischema_test.go` (guide example accepted; 10 broken shapes rejected, incl. dataModel beside createSurface). Real-model check, dev Ollama 0.40.8, gemma4:12b, 1 call: Ollama accepted the 1.4 KB compact typed schema and returned valid JSON in 36 s; the model still chose `goto summarise` (see step 4).

Criterion 3: `guide.go` `withAcceptedProperties`: a refusal about a component or one of its properties appends `Accepted for <Component>: <signature>.` once per component (the signature is the same text the guide prints), for at most 2 distinct components per refusal; worst case measured 1042 bytes. Deviation: an unknown component type gets no extra `Components:` line because the existing message already lists the catalog. Tests in `a2ui_test.go`.

Step 4 (criterion 6) - resolved by the user on 2026-10-23 (criterion withdrawn): option (b) was tried: `assistant/workflows/ui-compose.yaml` `compose` instructions now say the observation decides the action and "Never use the goto action for a text observation". One real call on dev Ollama (gemma4:12b, typed schema) still returned `goto summarise` (4 s); wording alone did not change it. Option (a) (a new workflow state key and an engine change; the engine cannot leave a state after an ask_user answer) was offered and not chosen; the user prefers a text fallback (see Out of Scope). State keys today: instructions, tools, next, end, on_error, output, delegate, model_tags. Real dev-Ollama calls: 4 in total: the 2 replay calls made before the spec (with and without format), 1 for criterion 5 (typed schema) and 1 for criterion 6 (stronger wording); of the 6 calls budgeted for criteria 5 and 6, 2 were used.

Note for delivery: prod loads its workflows from a ConfigMap (the pod log says "workflows loaded ... dir /etc/assistant-workflows"; the chart is outside this repository), so the changed `ui-compose.yaml` reaches prod only when that chart's values are updated; UNVERIFIED where exactly the chart takes the file from.

## Validation

Baseline before changes (`go test ./... -count=1 -v` in `assistant/`): 382 pass, 0 fail, 213 skip (212 need ASSISTANT_TEST_DATABASE_URL, 1 needs UI_TRIAL_OLLAMA); `go vet` clean.

After the change, without the database: 413 pass, 0 fail, 214 skip.

With the dev cluster's Postgres (database assistant_test via a port-forward from the dev k3d cluster, explicit kubeconfig `.k3d/kubeconfig`; command `go test -count=1 -race -v ./...` in `assistant/`): 626 top-level passes, 178 subtest passes, 0 fail, 1 skip: TestARealModelComposesAnInterface (needs UI_TRIAL_OLLAMA, a real model call, deliberately not run); 18 packages ok. `task test-assistant` exits 0.

Findings during validation: the database-backed tests failed twice and exposed (1) `finishedLogAttrs` defined but not called, (2) the privacy test TestLogsCarryIdWorkflowOutcomeAndDurationButNeverPayloadOrResult failing because every failure's message was logged. Both were fixed (see Implementation Notes step 2), then green.

Regression check: every test of the baseline still passes unchanged. TestFinishedLogAttrsOfAPlainErrorHaveAMessageButNoReason, written in this change, was rewritten as TestFinishedLogAttrsOfAPlainErrorSayNothingAboutTheFailure after the privacy test failed; no baseline test was edited.

Real artifact against realistic input: the real compose request (BuildRequest output, 2285 prompt tokens) was sent to dev Ollama 0.40.8 with gemma4:12b. Without `format` the reply was invalid JSON (reproduces the prod failure class). With the old loose schema: valid JSON but goto summarise. With the typed schema: valid JSON, accepted by the grammar converter, but goto summarise (also with the stronger wording). 4 real calls in total on dev: 2 before the spec (without and with the old loose schema), 1 for criterion 5 (typed schema) and 1 for criterion 6 (stronger wording plus typed schema).

Not validated: prod behaviour (prod image 2026.10.3 = tag assistant-v2026.10.3 does not include this change and the model there is gemma4:31b); the workflow ConfigMap used by prod; criterion 6 (withdrawn).

Coverage gaps: the effect of the new refusal text on real model retries is unmeasured; a typed error from `internal/workflows` (instead of parsing the reason from text) was not done.

## Documentation Review

Checked against the change (log fields of the "task finished" line, refusal messages, reply schema sent as `format`, ui-compose instructions, workflow YAML format):

- `assistant/README.md`: drift found at line 42-43 (Logs bullet said a task's finish line carries only id, workflow, outcome and duration; a failed task with a known reason now also carries `failure_reason` and `failure_message`, `assistant/internal/tasks/worker.go:160-169`). Fixed directly in the README (routine fix). The remaining mentions (retries with the reason, line 354; `payload.ui` validated and sent back with its problems, line 180; `ui-compose`, line 183; workflow format, line 347) still match the code; no other drift.
- `specs/mission.md`: no drift found (no match for the log line, refusals, reply schema, ui-compose or the workflow format).
- `specs/tech-stack.md`: no drift found (same search, no match).
- `specs/artifacts/assistant/roadmap.md`: no drift found; line 9 lists this spec under Now, which is correct while the work is not done.
- `assistant/CHANGELOG.md`: the "task finished" entry (line 9) matches `finishedLogAttrs` (reason and message only for a known reason; quoted reply left out). The `ui-compose` instructions entry (line 53) says "ask with `ask_user` and never `goto` before the user pressed the button", which matches `assistant/workflows/ui-compose.yaml` ("Never use the goto action for a text observation"). The refusal entry (line 51) and the `format` entry (line 52) match Implementation Notes steps 1, 3 and criterion 3. No change needed.

## Documentation Updates

`assistant/README.md:42-44` (Logs bullet): a failed task whose workflow gave a known reason adds `failure_reason` and `failure_message`, the quoted model reply left out. `assistant/CHANGELOG.md` [Unreleased]: Added (line 9, failure fields in the task finished log line) and Changed (lines 51-53: refusal text and hints, the typed `payload.ui` reply schema, the ui-compose wording). No constitution file changed; no drift found in `mission.md`, `tech-stack.md` or the assistant roadmap. `specs/artifacts/assistant/roadmap.md`: this feature's `## Now` line removed.
