---
title: Assistant workflow engine with mocked LLM
kind: feature
status: done
version: 1
updated: 2026-10-16
branch: main
---

## Problem / Motivation

The assistant's core is a workflow engine: a task is mapped to a state machine, and an LLM walks it, calling tools and moving between states until an end state. If this engine does not work, nothing built on it makes sense, so it is built and proven first, before the task queue, the GUI or any channel.

The approach follows SKILL.state (arXiv 2608.26263): at each step the model sees only the workflow spec, the current run state and the latest observation, never a chat history, and returns a state patch plus an action; the runtime validates both before anything is committed. This task adapts that to explicit state machines, adds suspend and resume (a learner answers minutes or days later), and adds the authoring and testing aids that make workflows safe to write.

This task proves the engine's mechanics with a scripted fake LLM. It does not prove that real models can drive it; that is the next two tasks (prompt-eval tests, then a minimal Ollama check), recorded in the roadmap.

Decisions confirmed by the user (2026-10-16): engine first, then slice 1; suspend and resume included; as much of the engine as is useful, covered by tests; no test ever calls a real LLM.

## Acceptance Criteria

Workflow definition
1. A workflow is a YAML file with keys `id`, `version`, `description`, `state_schema` (a JSON Schema for the run state), `start`, `limits` (`max_steps`, `max_tokens`, `max_retries`, `max_visits`), and `states`. Each state has `instructions`, `tools` (allowlist), `next` (allowed target states), optional `end` (boolean), optional `on_error` (a state to go to when a step fails for good) and optional `output` (the kind of output the state produces, for example `lesson` or `quiz`, which selects the output snippet of criterion 18).
2. `assistant/schemas/workflow.schema.json` describes that format. The loader rejects a file that violates it, naming the key and line.
3. A lint check, `assistant workflow validate <file>`, reports with exit code 1: unknown `start` or `next` or `on_error` target, a state that cannot reach an end state, an end state with `next`, an unreachable state, a tool name not in the tool registry, a `state_schema` that is not valid JSON Schema, and missing limits. It exits 0 and prints a one-line summary for a valid file.

Run state and patches
4. A run has `run_id`, `workflow_id`, `status` (`running`, `waiting`, `succeeded`, `failed`), `state_name`, `state` (JSON object), a visit count per state and a step counter.
5. A state patch is a JSON object merged into the run state by recursive object merge; a `null` value deletes the key; arrays are replaced whole. The merged result is validated against the workflow's `state_schema`; a patch whose result is invalid is rejected and the run state is left unchanged.

Step contract
6. Each step builds one request for the LLM containing: the system prompt, the workflow description, the current state's name, instructions, allowed tools (with argument schemas) and allowed next states, the current run state, the latest observation, and a JSON Schema for the reply. It never contains earlier steps or any message history.
7. The model's reply is JSON `{"state_patch": {...}, "action": {...}}`. Action `type` is one of `tool` (with `tool` and `args`), `goto` (with `state`) or `ask_user` (with `payload`, an object). A reply that is not JSON, has the wrong shape, uses an unknown action type, calls a tool not on the current state's allowlist, passes arguments that violate the tool's schema, or targets a state not in `next` is invalid.
8. An invalid reply is retried with the validation error shown to the model, up to `max_retries` consecutive attempts per step. Retries are recorded and their tokens are counted.

Run loop
9. `tool` runs the registered tool and its result becomes the next step's observation; the run stays in the same state. A tool error becomes the observation, so the model can react; a tool exceeding its timeout is reported the same way. Observations longer than a limit are truncated with a visible marker.
10. `goto` moves to the target state, increments its visit count, and clears the observation. Entering a state with `end: true` finishes the run as `succeeded`.
11. `ask_user` ends the current call with the run `waiting` and returns the `payload`. `Resume(run, userMessage)` continues the same run at the same state with the user's message as the observation.
12. A run is `failed` when: retries are exhausted, `max_steps` or `max_tokens` is exceeded, a state's visit count exceeds `max_visits`, or the context is cancelled. If the current state has `on_error`, the run goes to that state instead, with the failure as its observation, once per run; a second failure is final.
13. A run snapshot (the run record) serializes to JSON and restores to an identical run, so a waiting run can be stored and resumed by another process.

Tools and LLM seams
14. The engine depends only on consumer-side interfaces: `LLM` (takes the request and the reply JSON Schema, returns text and input and output token counts), `Tool` (name, argument schema, `Call`) with a registry, and `Recorder` (receives each step). No real LLM client, database or network code is part of this task.
15. The reply JSON Schema is passed to the `LLM` on every call, so a later Ollama or OpenAI client can use constrained decoding to avoid JSON formatting slips.

Trace and replay
16. Every step is recorded: step number, state name, request digest, raw reply, parsed patch and action, outcome (`ok`, `retry`, `rejected`, `failed`), tool name and result summary, token counts, duration. A text rendering of a run's trace is available for humans.
17. A test helper replays a recorded transcript as a fake LLM from a golden file and fails if the engine's requests diverge from the recording, so engine changes that alter prompts or ordering are caught.

Prompt snippets per language and per output
18. The system prompt is a template with named placeholders (for example `{{language}}`, `{{languageRules}}` and `{{outputRules}}`), as in phraseforge's prompts, not positional formatting. Snippets come from two independent axes:
    - Language: a run carries a language code (from the run input or the run state). The request builder fills `{{languageRules}}` from the embedded snippet `prompts/lang/<code>/rules.txt`.
    - Output: the current state's `output` kind fills `{{outputRules}}` from `prompts/output/<kind>/rules.txt`, so each kind of output (a lesson, a quiz, a plain answer) carries its own format instructions.
    A language or output kind with no snippet gets an empty text and the run continues. Both axes can apply at once and are filled independently. A placeholder left unfilled, or a snippet that references an unknown placeholder, is an error caught at load time and by a test, never sent to a model.

Example and tests
19. `assistant/workflows/learn-topic.yaml` (states `intake`, `assess`, `teach`, `done`, with a state schema holding goal, level, gaps and lessons done) runs end to end against a scripted fake LLM and fake tools, including a suspend and resume at `assess`.
20. Tests are isolated, deterministic and extensive, with no LLM, network or database: every criterion above has at least one test, plus the cases listed in the Approach. Time and sleeping use an injected clock. `go test ./...` and `go vet ./...` pass, and `CGO_ENABLED=0 go build ./...` succeeds.

## Approach

New Go module `assistant/` (the minimal part of slice 1's module; slice 1 builds on it), package `internal/workflow`, with test support in `internal/workflow/workflowtest` (scripted fake LLM, fake tools, fake clock, recorder, golden replay). The engine is a pure library: no HTTP, no database, no goroutines of its own; the caller (the later worker) drives `Start` and `Resume` and passes a context.

Public surface, kept small:
- `Load(path|bytes) (*Workflow, error)`, `Lint(*Workflow, ToolRegistry) []Finding`.
- `Engine{LLM, Tools, Recorder, Clock}` with `Start(ctx, wf, input) (*Run, Result)` and `Resume(ctx, wf, run, userMessage) (*Run, Result)`, where `Result` is one of finished, waiting (with payload) or failed (with reason).
- `Run` is a plain struct that marshals to JSON.

Key decisions:
- **Actions limited to `tool`, `goto`, `ask_user`.** A separate `finish` action is not needed: reaching an `end: true` state finishes the run, which keeps termination in the workflow definition, where the lint can check it.
- **State machine plus state schema.** The graph limits where the model may go; the schema limits what it may record. The paper's setup alone has no graph, and a graph alone has no memory of progress.
- **Stateless prompts.** The request is built from scratch each step (criterion 6), which is what keeps token use flat; a test asserts no earlier step text appears in a request.
- **Rollback by construction.** A patch is applied to a copy, validated, then committed, so rejection needs no undo.
- **Constrained decoding hook now, not later.** Passing the reply schema to the `LLM` costs nothing in this task and addresses the paper's reported formatting slips on small open-weight models.
- **Libraries.** A pure-Go YAML parser and a maintained pure-Go JSON Schema validator, chosen at implementation after checking their current APIs and licenses; no CGO, to keep the image under 50 MB.
- **System prompt is an embedded file** (`prompts/step.system.txt`) with a placeholder text here. Its real content comes from the prompt-eval task and syncs back.
- **Snippets on two axes, following phraseforge's layout.** Language snippets live in `prompts/lang/<code>/` (ISO 639-3 codes, as in phraseforge's `lang/` directories and its language skills) and output snippets in `prompts/output/<kind>/`; both fill named `{{placeholders}}` in the template. The `output` kind is declared per state in the workflow YAML, so the workflow author, not the model, chooses it. This task builds the mechanism and tests it with throwaway test languages and output kinds; the real snippets (for example Arabic, Hebrew, Japanese, and `lesson`, `quiz`) arrive with the prompt-eval task and sync back. Output kinds are meant to line up later with the response document types and templates of `assistant-response-review-loop`.

Test strategy (all with scripted fakes):
- Loader and schema: valid file, each kind of invalid file, error messages with key and line.
- Lint: one test per finding kind, plus a valid file.
- Merge: nested objects, null deletion, array replacement, schema rejection leaves state unchanged, patch to a deleted key.
- Request builder: contains each required part, contains no history, allowlists and next-state lists match the state, schema is attached.
- Snippets: a language snippet and an output snippet each fill their own placeholder, both together, missing language or missing output kind gives empty text, a language switch between runs, a state moving from one output kind to another changes `{{outputRules}}` in the next request, unfilled placeholder and unknown placeholder both fail at load, snippet text never leaks into another language's or output's request.
- Reply validation: each invalid-reply kind from criterion 7, with the retry message the model would see.
- Loop: tool then goto then end; tool error as observation; tool timeout; observation truncation; `max_retries`, `max_steps`, `max_tokens`, `max_visits` each fail with a distinct reason; `on_error` once then final; context cancellation mid-run.
- Suspend and resume: waiting returns the payload; resume continues at the same state; snapshot round trip through JSON; resuming a run that is not waiting is an error; a run resumed twice is rejected.
- Trace: every outcome kind recorded; token totals equal the sum of steps; golden replay passes on unchanged engine and fails when a request changes.
- Example: `learn-topic` full run with suspend and resume.

Ordered plan (each step ends with build, vet and tests green, and a short report):
1. Module bootstrap, `schemas/workflow.schema.json`, loader, tests.
2. Lint and the `assistant workflow validate` command, tests.
3. Run state, patch merge and schema validation, tests.
4. Request builder and reply validation, with the `LLM` interface and fake LLM, tests.
5. Run loop: tools, `goto`, end states, retries and guards, `on_error`, tests.
6. Suspend and resume, snapshot round trip, tests.
7. Recorder, text trace, golden replay, tests.
8. The `learn-topic` workflow and its end-to-end tests.

## Affected Areas

- New: `assistant/go.mod`, `assistant/main.go` (only the `workflow validate` command for now), `assistant/internal/workflow/`, `assistant/internal/workflow/workflowtest/`, `assistant/schemas/workflow.schema.json`, `assistant/workflows/learn-topic.yaml`, `assistant/README.md`.
- `Taskfile.yml`: a `test-assistant` target (Go tests only in this task).
- Constitution (separate gated update): `specs/artifacts/assistant/roadmap.md` (new Next items and order); `specs/tech-stack.md` only if the chosen YAML and JSON Schema libraries need listing.

## Out of Scope

- Any real LLM client, the model router, token pricing and cost attribution.
- Prompt wording and quality, and the content of the real per-language and per-output snippets; prompt-eval tests and syncing prompts back (next task, `assistant-engine-prompt-eval`).
- Calls to a real model, even a local one (task after that, `assistant-engine-ollama-smoke`).
- Database storage of runs, the worker and the task queue (slice 1).
- Real tools: memory, RAG, web search, Anki, mail.
- Sub-workflows, parallel tool calls, and branching on tool results without the model.
- HTTP API, admin GUI, Telegram, NATS, scheduler, authentication.

## Implementation Notes

All eight planned steps were done in order, each ending with build, vet and tests green, on `main`, nothing committed.

Where it went beyond or differs from the Approach:
- `on_error` is taken only for step-level failures: retries exhausted, a failed model call, and a state entered too often. Spent step or token budgets and cancellation are never redirected. A new failure reason `incomplete_state` covers a run that reaches an end state without the state its schema requires, including through `on_error`.
- A state schema's `required` keys are enforced only when the run reaches an end state; types and shape are checked after every patch. Without this a schema with required keys would make the first patch impossible.
- A reply wrapped in a markdown code fence is accepted, since models often add one. Prose around the JSON is still refused.
- Tools run in their own goroutine, so a tool that ignores its context still cannot hold a run past its timeout (the goroutine is left to finish).
- `assistant workflow validate` takes `--tools name,name` instead of a registry, because no real tools exist yet; without the flag tool names are not checked.
- Numbers in run state and observations are `json.Number`, so integers survive snapshots; use `RestoreRun`, not `json.Unmarshal`.
- The reply JSON Schema (per state, with each tool's argument schema) is built by `ReplySchema` and passed in every `Request`; a test checks that `ParseReply` and that schema agree on every sample reply.
- Libraries: `go.yaml.in/yaml/v3` v3.0.5 (gopkg.in/yaml.v3 is archived), `santhosh-tekuri/jsonschema/v6` v6.0.3, `golang.org/x/text` v0.14.0 (message printer for schema errors). Module name `assistant`, Go 1.26.5.

Files (all under `assistant/`): `main.go`; `schemas/workflow.schema.json`, `schemas/schemas.go`; `internal/workflow/` (`workflow.go`, `load.go`, `lint.go`, `tools.go`, `state.go`, `prompts.go`, `request.go`, `reply.go`, `engine.go`, `record.go`, `trace.go`, `prompts/step.system.txt`); `internal/workflow/workflowtest/` (`llm.go`, `tool.go`, `recorder.go`, `golden.go`); `workflows/learn-topic.yaml`; `internal/workflow/testdata/`; `README.md`, `CHANGELOG.md`. Also `Taskfile.yml` (`test-assistant`).

Tests added: 167 top-level tests (232 counting subtests, none skipped) across the loader, lint, CLI, state and patches, prompts and snippets, request building, reply validation, the engine (retries, every guard, `on_error`, tools, cancellation, suspend and resume, snapshots, determinism, language and output snippets, concurrency), trace, golden replay and the fakes themselves, plus the `learn-topic` end-to-end run and its golden file.

## Validation

- `task test-assistant` (`go vet ./...`, `go test -count=1 -race ./...`): all packages pass.
- Coverage: `assistant` 97.6%, `internal/workflow` 93.7% (`workflowtest` shows 43.6% because most of it is exercised from the `workflow` tests).
- `CGO_ENABLED=0 go build -ldflags="-s -w"`: succeeds; binary 4.1 MB.
- Tests pass with a minimal environment (no `OLLAMA_*` variables, no network use): no test calls a real LLM.
- Mutation spot check of seven engine conditions (visit, retry, step and token limits, on_error once, observation clearing, completeness at an end state): five were caught at once; two survived (exact token-budget boundary, clearing the observation on a transition), tests were added for both and re-running the mutations showed them caught.
- No regression baseline needed: the module is new and no existing application was touched.
- Not covered: real model behaviour (the next two tasks), and two processes resuming the same stored run at once (needs a lock or version check in the storage layer of slice 1).

## Documentation Review

- `assistant/README.md`: new, written for this task.
- `assistant/CHANGELOG.md`: new; the engine, the `workflow validate` command and the `learn-topic` workflow are user-facing, so entries are needed under `## [Unreleased]`.
- Constitution drift: `specs/tech-stack.md` does not list the YAML and JSON Schema libraries the engine depends on (proposed addition to Frameworks & libraries, awaiting the user's approval). `specs/mission.md` and the roadmap need no change; the `## Now` line for this feature is removed at delivery.
- Durable knowledge appended to `specs/memory.md` (5 entries).

## Documentation Updates

Done: `assistant/README.md`, `assistant/CHANGELOG.md` (3 Added entries), five `specs/memory.md` entries. Constitution: `specs/tech-stack.md` version 14 lists the YAML and JSON Schema libraries (approved by the user). The `## Now` line was removed from `specs/artifacts/assistant/roadmap.md` at delivery.
