---
title: Assistant controller prompt, first iteration
kind: feature
status: done
version: 1
updated: 2026-10-16
branch: main
---

## Problem / Motivation

The smoke check of the controller prompt against a real model (`assistant-engine-ollama-smoke`) gave one pass, one schema-valid reply that missed the intent, and one reply that was not valid JSON. Two of the misses have one root cause that is not wording: the model is never shown the run-state schema. The step input holds only `workflow`, `state` and `run_state`, so a key that is not in `run_state` yet (such as `pending`) is known only from free text in the state's instructions, and the model invented `expected_answer` for `expected`. The controller prompt and the quiz rules do not mention `pending` either, although the workflow depends on it.

This task makes one iteration: fix the input and the prompt for what the smoke check showed, check that the tests still agree with the engine, and re-run the same three cases once against the real model, under the same bounds as the smoke check, to see whether the changes helped. It is one iteration, not a tuning campaign.

Decisions confirmed by the user (2026-10-16): one iteration to try to improve the prompt.

## Acceptance Criteria

1. The step input gains a key `run_state_schema`: the workflow's `state_schema`, so the model sees every key and type the run state may hold. `BuildRequest` adds it; no other key of the input changes.
2. The controller prompt (`prompt/assistant/step.system.txt` in `prompt-eval`) is changed to: describe `run_state_schema` and tell the model to use exactly its key names, never to add keys, and to write the whole reply as compact JSON on one line with nothing before or after it; include one short complete example reply for each action type; and say that whenever it asks the user something with `ask_user` it records the question in `pending` if the schema has that key. The wording stays application agnostic where the prompt is generic, and refers to `pending` only in the quiz and lesson output rules, which are the places that use it.
3. The `quiz` and `lesson` output rules say how `pending` is recorded and cleared, with the exact key names `question` and `expected`.
4. The eval case `assess-last-answer` accepts the other valid behaviour: either the model concludes (level set, `goto teach`, `pending` null) or it asks another question and records it in `pending`. Both alternatives require the count of correct answers to equal 4.
5. The eval cases' inputs are regenerated from the engine so they contain `run_state_schema`, and all existing sync checks still pass: the Go tests that compare each case with `BuildRequest`, the schemas, the example replies, the render vectors, and `check-sync-assistant`. The learn-topic golden is regenerated and reviewed.
6. `task test-assistant` and `npm test` in `prompt-eval` pass, with no test calling a model.
7. The three smoke cases (`intake-empty`, `assess-last-answer`, `teach-lesson-fetched`) are run once each against the real model under the smoke check's bounds (precheck, `timeout 90`, `REQUEST_TIMEOUT_MS=80000`, `-j 1`, `--no-cache`, `num_ctx: 8192`, one at a time, model unloaded afterwards, no retries). The result is reported next to the smoke check's: score, reason, wall time, reply. If the machine fails the precheck, no call is made.
8. The outcome is recorded in this spec and, if durable, in `specs/memory.md`. A result that is worse than before is reported as it is; the changes are not reverted or tuned further in this task.

## Approach

`BuildRequest` already builds the step input from the workflow; adding `run_state_schema` is one more field of the input struct filled from `wf.StateSchema`, converted so numbers stay exact. It costs prompt tokens (the learn-topic schema is a few hundred), which the report notes.

The prompt text is edited in `prompt-eval` and synced to the assistant, as agreed. The cases' input blocks are regenerated from the engine with the same temporary dump used before, since the Go tests require case inputs to equal the engine's request exactly.

The evaluation uses the same three cases, the same model and the same bounds as the smoke check so the two results are comparable. One call per case is a sample, not a measurement; the report says so and does not claim an improvement or a regression beyond what three replies show.

Ordered plan (each step ends green and with a short report):
1. Add `run_state_schema` to the step input in Go, with tests; regenerate the golden.
2. Edit the prompt and the output rules in `prompt-eval`; sync.
3. Regenerate the cases' inputs and update `assess-last-answer`; run all tests in both repositories.
4. Precheck, then the three real calls, then unload, then report and record.

## Affected Areas

- `assistant/internal/workflow/request.go` and its tests, `testdata/learn_topic.golden.json`.
- `prompt-eval`: `prompt/assistant/` (controller prompt, `output/quiz`, `output/lesson`), `assistant-step/cases.yaml`, tests that mention the input keys.
- Synced copies in `assistant/internal/workflow/prompts/` and `testdata/prompt-eval/`.
- `assistant/README.md`, `assistant/CHANGELOG.md`, `specs/memory.md`.

## Out of Scope

- Sending the reply schema as Ollama's `format`, an Ollama client, and engine-level handling of malformed JSON (the retry loop already exists).
- A second iteration, other models, more than three real calls, and changing the workflow's state schema or instructions beyond what the cases need.

## Implementation Notes

Done in the planned four steps, on `main`, nothing committed.
- `assistant/internal/workflow/request.go`: the step input has a new key `run_state_schema` (placed before `run_state`), with two new tests and the existing tests updated; the learn-topic golden was regenerated.
- `prompt-eval/prompt/assistant/step.system.txt`: describes `run_state_schema`, requires exactly its key names and a compact one-line reply, and shows three short example replies with made-up names. `output/quiz/rules.txt` and `output/lesson/rules.txt` say how `pending` is recorded and cleared, with the exact keys `question` and `expected`. Synced to the assistant.
- `prompt-eval/assistant-step/cases.yaml`: every input gained `run_state_schema` (taken from the engine's own request); `assess-last-answer` accepts either concluding or asking another question recorded in `pending`, and a new node test covers both and the two ways to get it wrong.
- `assistant/README.md` and `assistant/CHANGELOG.md` updated.

## Validation

Tests: all assistant tests pass (including the ones that compare each case input with the engine's request, which failed first, as intended, until the cases were regenerated); `prompt-eval` `npm test` 177 pass; `check-sync-assistant` and the suite config validation pass. No test called a model.

Real calls (same model, bounds, cases and precheck as the smoke check; one call per case, no retries; precheck passed with no model loaded and load 4.33; model unloaded afterwards, free memory 1.0 GB back to 8.4 GB):

| Case | Before: score, tokens in/out, time | After: score, tokens in/out, time | After: reply |
| --- | --- | --- | --- |
| `intake-empty` | 1.0, 839/83, 27 s (with model load) | 0, 1278/25, 20 s | Valid compact JSON, `goto plan` at once, without a memory search or a question |
| `assess-last-answer` | 0.2, 1078/151, 19 s | 0.5, 1575/30, 21 s | Valid compact JSON, `correct` 4, `pending` null, `goto assess` (into the same state) |
| `teach-lesson-fetched` | 0 (invalid JSON), 1175/226, 36 s | 0 (invalid JSON), 1670/181, 41 s | Duplicate `meaning` key and a missing closing brace; `pending` not recorded |

Caveat: the `assess-last-answer` expectations were relaxed between the two rounds, so its scores are not strictly comparable; and one reply per case is a sample.

What the three replies show:
- Better: every reply was compact one-line JSON with no prose or fence; the `expected_answer` key invented before did not recur; the model used the schema's key names.
- Not better: JSON validity is unchanged at two of three, and the failing reply is the long Arabic lesson both times; `pending` is still not recorded when a lesson asks the exercise; the model now took shortcuts that the workflow allows (`goto plan` without recalling memory, `goto assess` into itself) and that the cases did not anticipate.
- Cost: about 440-500 more input tokens per step and a few seconds more per call.
- So this iteration is a mixed result, not an improvement to claim. The run-state schema is right in principle and stays; the prompt wording did not fix the two problems the smoke check found.

Options for the user to decide (nothing changed here): constrained decoding through Ollama's `format` in the model client (the reply schema is already in every request); letting the engine own the open question (the `ask_user` action would carry what is expected and the engine would store it in `pending`), so correctness does not depend on the model remembering two things; and widening the cases to accept the legitimate shortcuts.

## Documentation Review

`assistant/README.md` and `assistant/CHANGELOG.md` were updated for the new input key. No constitution change. Two durable findings appended to `specs/memory.md`.

## Documentation Updates

Done: README, CHANGELOG, two memory entries. The `## Now` line of `specs/artifacts/assistant/roadmap.md` was removed at delivery (approved by the user).
