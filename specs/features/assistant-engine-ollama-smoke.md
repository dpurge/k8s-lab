---
title: Assistant engine smoke check against local Ollama
kind: feature
status: done
version: 1
updated: 2026-10-16
branch: main
---

## Problem / Motivation

Everything built so far for the workflow engine is proven with scripted replies. That proves the runtime mechanics, not that a real local model can answer one controller step in the format the engine requires. If it cannot, the prompt, the reply format or the choice of model has to change before the model router and the channels are built on top.

This is a feasibility spike for exactly one unknown: can the model the project uses locally (`gemma4:12b` through Ollama) produce, for a realistic step, a JSON reply that satisfies the step's schema and the case's expectations? It tests nothing else: not prompt quality across models, not the engine loop with a real client, not cost or speed beyond noting them. The machine is a laptop with 17 GB of RAM and little free memory, and the model is about 7.6 GB, so the spike is deliberately tiny.

Decisions confirmed by the user (2026-10-16): defaults, and the promptfoo suite only (no engine run with an Ollama client; that belongs to the model-router slice).

## Acceptance Criteria

1. Before any model call, a read-only precheck runs and is reported: `ollama ps` shows no model loaded, the one-minute load average is below 6, and the Ollama version answers. If a model is already loaded or the load is higher, the spike stops and reports why, with no call made.
2. At most three real model calls are made, strictly one at a time (`-j 1`), each through `assistant-step` case selection (`CASES=<id>`), never the whole suite. The first call is case `intake-empty`. The second (`assess-last-answer`) and the third (`teach-lesson-fetched`) are made only if the previous call finished within its bound.
3. Every call runs under a hard timeout enforced by the calling tool (`timeout 90`, plus `REQUEST_TIMEOUT_MS=80000` for the provider), and the whole spike is stopped at 90 seconds of wall time for the first call. A call that hits its bound is a finding: it is reported once and not retried.
4. The run uses `num_ctx: 8192` through a temporary copy of the suite config kept outside the repository (the committed config stays at 16384), `--no-cache`, `think: false`, and the telemetry, update-check and sharing switches off, so nothing leaves the machine except the call to the local Ollama.
5. For each call the report states: the case, the model's raw reply, the score and the reason from the suite's assertion (schema and expectations), the wall time, and the free memory and load before and after.
6. After the calls, `ollama ps` is checked and the model is unloaded if the spike loaded it, so the machine is left as it was found.
7. The outcome is recorded in this spec's Validation section and, if it is durable and reusable (for example how long a step takes on this machine, or that the model needs a code fence), as an entry in `specs/memory.md`. No repository file other than specs and memory changes.

## Approach

Use the promptfoo suite that already exists. For each call, build a temporary config in the scratchpad from `assistant-step/promptfooconfig.yaml` with absolute `file://` references and `num_ctx: 8192`, and run:

`timeout 90 env REQUEST_TIMEOUT_MS=80000 PROMPTFOO_DISABLE_TELEMETRY=1 PROMPTFOO_DISABLE_UPDATE=1 PROMPTFOO_DISABLE_SHARING=1 CASES=intake-empty npx promptfoo eval -c <temp config> -j 1 --no-cache -o <scratchpad>/result.json`

The run is foreground, so its duration is bounded by `timeout` and visible. Memory and load are read with `vm_stat`, `sysctl` and `uptime` before and after; the loaded model with `curl localhost:11434/api/ps`. If the first call fails the schema or times out, stop: that is the finding. Do not tune the prompt in this task.

A pass means "a real model can follow the step contract on this case", not "the prompt is good". A failure is expected to be informative: it shows whether the issue is JSON formatting (the case for constrained decoding through Ollama's `format`, which the engine already supports by passing the reply schema), content, or time.

## Affected Areas

- `specs/features/assistant-engine-ollama-smoke.md` (this file) and, if warranted, `specs/memory.md`.
- Scratchpad files only for the temporary config and results.
- No source file in `assistant/` or `prompt-eval/` changes.

## Out of Scope

- Tuning prompts, comparing models, running the full suite or more than three calls.
- A Go Ollama client, the engine driven by a real model, the model router, OpenRouter or any non-local provider.
- Changing the committed suite config.

## Implementation Notes

Run on 2026-10-16 with the user's confirmation that nothing else used Ollama. No repository file was changed, no code was written. A temporary promptfoo config (absolute `file://` references, `num_ctx: 8192`) lived in the scratchpad; each call used `timeout 90`, `REQUEST_TIMEOUT_MS=80000`, `-j 1`, `--no-cache`, `--no-write`, and the telemetry, update and sharing switches off. The model, `gemma4:12b`, was unloaded afterwards (`keep_alive: 0`), and free memory returned from 1.1 GB to 8.7 GB.

## Validation

Precheck (criterion 1): Ollama 0.40.4 answered, no model loaded, load 4.33 (limit 6). Free plus inactive memory was 3.1 GB, which the spec did not set a limit on; it fell to 1.0-1.2 GB while the 8.1 GB model was loaded, and the machine did not become unresponsive.

| Call | Case | Wall | Tokens in/out | Score | Result |
| --- | --- | --- | --- | --- | --- |
| 1 | `intake-empty` | 27 s (includes model load) | 839 / 83 | 1.0 | Pass: valid JSON, `ask_user` with a non-empty payload |
| 2 | `assess-last-answer` | 19 s | 1078 / 151 | 0.2 | Schema valid; 4 of 5 expectations missed (see below) |
| 3 | `teach-lesson-fetched` | 36 s | 1175 / 226 | 0 | Not valid JSON: a misplaced brace and a stray `1}` |

Findings (three calls; this is a sample, not a rate):
- The model can follow the step contract: call 1 passed, and call 2 produced schema-valid JSON.
- Call 2: it counted the answer correctly (`correct` 4) and asked a sixth question instead of concluding the level, which the workflow allows, but it did not record the new question in `pending`, which the instructions require; the case's expectation that it moves on to `teach` is stricter than the workflow.
- Call 3: beyond the broken JSON, it wrote `pending.expected_answer` (the schema key is `expected` and extra keys are refused) and put `lessons_done` in as an array. The engine would have refused this reply and retried, with or without the JSON error.
- Speed: 19-36 s per step with the model loaded. A 13-step run such as the `learn-topic` scenario would take roughly 4-8 minutes on this machine, acceptable for background work.
- Not tested: constrained decoding (`format`), retries with a real model, other models, and any prompt change.

Implications for the next slices (for the user to decide, nothing changed here): the real Ollama client should send the reply schema as `format`, since the engine already passes it in every request; the engine's retry loop is not optional; the controller prompt and the `assess-last-answer` case need a second look in `prompt-eval` (an alternative expectation for "asks another question and records it in `pending`").

## Documentation Review

No documentation needed changing: no behaviour or interface of the project changed. Three durable findings were appended to `specs/memory.md` (speed on this class of machine, malformed JSON without constrained decoding, the renamed `pending` key).

## Documentation Updates

Done: three `specs/memory.md` entries. The `## Now` line of `specs/artifacts/assistant/roadmap.md` was removed at delivery (approved by the user).
