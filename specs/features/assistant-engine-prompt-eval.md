---
title: Assistant engine prompts in prompt-eval
kind: feature
status: done
version: 1
updated: 2026-10-16
branch: main
---

## Problem / Motivation

The workflow engine's controller prompt is a placeholder, and there is no way to tell whether a prompt change makes a real model better or worse at driving a workflow. `prompt-eval` is where prompts are developed and evaluated for phraseforge, with promptfoo suites and a script that syncs the approved prompts into the application. The engine needs the same: its prompts and snippets live in `prompt-eval`, are evaluated there, and sync back into `assistant`.

`prompt-eval` must stay application agnostic. Today `scripts/sync-prompts.js` is written for phraseforge only. This task adds a generic, data-driven layer (an app registry, a sync command, a template renderer, assertion helpers) and registers `assistant` as its first user, without changing phraseforge's script. No application name appears in the generic code, only in registry data and in `package.json` script names.

The Go renderer in the engine and the JavaScript renderer in `prompt-eval` are two implementations of one behaviour, so a shared set of test vectors proves they agree. Likewise each evaluation case carries the reply schema its answer must satisfy, and a Go test proves that schema equals the one the engine builds for that state.

This task writes and checks the suite but never runs it against a real model; the next task (`assistant-engine-ollama-smoke`) runs one to three real calls.

Decisions confirmed by the user (2026-10-16): defaults for the snippets (`arb` and `eng` language rules, `lesson` and `quiz` output rules) and for not running real models here; the logic is re-implemented in JavaScript (not generated from Go); `prompt-eval` stays application agnostic.

## Acceptance Criteria

Generic sync (in `prompt-eval`)
1. A registry directory `apps/` holds one YAML file per application, describing: the prompt directory under `prompt/<app>/`, the target directory in the application repo with an environment variable that overrides its location, the template file, the snippet axes (directory, file name, placeholder, and the variable that selects the snippet), and extra files to sync (name in `prompt-eval`, destination in the application).
2. `node scripts/sync-app.js <app>` copies the managed files into the target and `node scripts/sync-app.js <app> --check` compares instead of writing. Check exits 1 and lists every difference, missing file and stale managed file; it exits 0 when identical. Only files the registry manages are written or reported, and unmanaged files in the target are never deleted. Any path that would leave the target root is refused.
3. No application name appears in `scripts/sync-app.js` or `lib/` generic modules. The existing `scripts/sync-prompts.js` and phraseforge's suites are unchanged and their tests still pass.

Rendering parity
4. `lib/template-render.js` renders a template from an app's registry entry: each snippet axis fills its placeholder from `<dir>/<name>/<file>` (trimmed), a missing snippet renders empty, a snippet may refer only to the selector variable (such as `{{language}}`), an unknown placeholder in the template or a snippet is an error naming the file, and runs of three or more newlines collapse to one blank line.
5. `prompt/assistant/` is not the only fixture: a self-contained vector file defines a template, snippets and cases (inputs and expected rendered text), including both axes together, missing snippets, and every error case. The JavaScript renderer and the Go renderer (`workflow.Prompts`) both pass every vector. The file is the source in `prompt-eval` and is synced into the assistant's test data.

Real prompts
6. `prompt/assistant/` holds the controller prompt `step.system.txt`, language rules `lang/arb/rules.txt` and `lang/eng/rules.txt`, and output rules `output/lesson/rules.txt` and `output/quiz/rules.txt`, in the layout the engine embeds. The placeholder prompt in `assistant/internal/workflow/prompts/` is replaced by the synced copies, and the learn-topic golden transcript is regenerated and reviewed. Snippets describe how to write user-facing text (script, transcription, diacritics, exercise format), never the controller's JSON contract, which stays in the system prompt.

Evaluation suite
7. `assistant-step/promptfooconfig.yaml` and `assistant-step/cases/*.yaml` define at least eight cases of one controller step each, built from the learn-topic workflow: empty-state intake, intake with a remembered learner, a correct assessment answer, a wrong one, the last assessment answer ending the quiz, teaching after a lesson was fetched, a step after a tool error, and a retry after a refused reply. Each case declares the language and output kind, the step input JSON, the reply schema, and expectations (allowed action types, expected tool or target state, state-patch paths that must be present or absent).
8. A generic assertion module checks a model reply against a case: valid JSON, matches the case's reply schema, then the expectations, with failure messages that say which expectation failed. It is covered by node tests that use canned replies for every pass and failure path; no node test calls a model.
9. `npm run assistant-step` runs the suite and `npm run assistant-step:smoke` (`SMOKE=1`) runs one case, like the other suites. The provider is the local model used by the other suites with `think: false` and the context size noted in the config. Whether promptfoo accepts the config is checked by a node test that parses it, without calling a model.

Back in the assistant
10. After syncing, a Go test in `assistant` loads the synced vectors and checks `workflow.Prompts` against each. Another loads the synced cases and checks, for each, that the declared reply schema equals `workflow.ReplySchema` for the case's state of learn-topic with the test tools, and that the step input JSON has exactly the keys `BuildRequest` produces. Both run in `task test-assistant` and need no network.
11. `node scripts/sync-app.js assistant --check` exits 0 once synced, and `npm test` in `prompt-eval` passes.
12. `prompt-eval/README.md` documents the registry format, the sync command and how to add an application.

## Approach

Everything flows from `prompt-eval` to the application, as phraseforge's prompts already do: the prompt tree, the render vectors and the case files are authored in `prompt-eval`; the sync command copies them into the assistant's embedded prompt directory and into `internal/workflow/testdata/prompt-eval/`.

Generic pieces, in `prompt-eval`: `apps/<app>.yaml` (registry), `scripts/sync-app.js`, `lib/template-render.js`, `lib/app-registry.js`, `lib/assert-reply.js` and the case loader. Registry keys are proposals to be confirmed before coding (see the question at the approval gate).

Parity is proven by data, not by sharing code: one vector file, two implementations. For the schema, Go stays the arbiter: cases carry the schema, and the Go test checks it against the engine's own `ReplySchema`. If the contract changes, the Go test fails and the cases are regenerated by hand.

The provider is used without Ollama's `format` constraint because promptfoo fixes `format` per provider, not per case; the suite therefore measures how well a model follows the prompt unaided. A second provider entry with a constrained format can be added later if the smoke task shows it matters.

Test strategy: node tests (`node --test`) for the registry parser, the sync command (against temporary directories: copy, check, stale file, unmanaged file kept, path escape refused), the renderer on the vectors, and the assertion module with canned replies; Go tests for the vectors, the case schemas and the case input shape. The real model never runs here.

Ordered plan (each step ends with that repo's tests green and a short report):
1. Registry format, `lib/app-registry.js`, `sync-app.js` with tests, and the `assistant` registry entry (nothing synced yet).
2. Render vectors, `lib/template-render.js`, the Go parity test; both pass.
3. Real prompts and snippets, sync into the assistant, regenerate and review the learn-topic golden.
4. The assertion module and its tests.
5. The suite config, cases and npm scripts, with the config-shape test.
6. Sync the cases, add the Go case-schema and input-shape tests.
7. Documentation in both repos.

## Affected Areas

- In `prompt-eval` (a separate repository, which the user commits): `apps/`, `scripts/sync-app.js`, `lib/` (new generic modules), `prompt/assistant/`, `vectors/`, `assistant-step/`, `test/`, `package.json`, `README.md`.
- In this repository: `assistant/internal/workflow/prompts/**` (replaced by synced files), `assistant/internal/workflow/testdata/prompt-eval/**` (synced), `assistant/internal/workflow/testdata/learn_topic.golden.json` (regenerated), new Go tests, `assistant/README.md`, `assistant/CHANGELOG.md`.
- Constitution: none expected; `specs/tech-stack.md` already covers prompts and the sync convention generically.

## Out of Scope

- Running the suite against any real model, tuning the prompts for quality, comparing models (next task, `assistant-engine-ollama-smoke`, and later).
- Converting phraseforge's `sync-prompts.js` or its suites to the generic layer.
- Snippets for languages other than `arb` and `eng`, and output kinds other than `lesson` and `quiz`.
- Constrained decoding through Ollama's `format`, and any non-Ollama provider.
- Helm or deployment of prompts.

## Implementation Notes

All seven steps were done in order, on `main`, nothing committed, in two repositories.

In `prompt-eval`: `apps/assistant.yaml`; `lib/app-registry.js`, `lib/app-sync.js`, `scripts/sync-app.js`; `lib/template-render.js`; `lib/assert-reply.js`, `lib/promptfoo-assert-reply.js`, `lib/promptfoo-chat-prompt.js`; `lib/prompt-cases.js`; `vectors/template-render.json`; `prompt/assistant/` (controller prompt, `lang/arb`, `lang/eng`, `output/lesson`, `output/quiz`); `assistant-step/` (config, `generate-tests.js`, `cases.yaml` with 9 cases, `schemas/` for intake, plan, assess, teach); tests for each module; `README.md` (was empty); `package.json` scripts and `ajv` as a direct dev dependency (the lock file gained one line, added by hand because npm rewrites the CRLF file whole).

In this repository: `assistant/internal/workflow/prompts/**` (synced copies of the real prompts), `testdata/prompt-eval/**` (synced vectors, cases, schemas), `prompts.go` (see below), `prompts_vectors_test.go`, `prompt_eval_cases_test.go`, `workflows/learn-topic.yaml`, regenerated `testdata/learn_topic.golden.json`, README and CHANGELOG.

Where it went beyond or differs from the Approach:
- The registry's `target` has `root` and `prompts` (not one `dir`), so that the destination of every extra file is also checked against the application root. The vector file is named `template-render.json` because it is generic.
- Writing the render vectors by hand exposed a gap in the Go renderer: `{{output}}` was not allowed in the template, and `{{language}}` was not filled inside output snippets. Both now work, and the vectors pass in Go and JavaScript.
- Designing the cases exposed a flaw in `learn-topic`: the model never sees the question it just asked, so it could not judge the answer. The workflow gained a `pending` key (the open question and the expected answer) and its instructions say when to set and clear it; the end-to-end test script and the golden were updated.
- Cases carry a generic `select` map instead of `language` and `output` keys, and name their reply schema by the state (`schemas/<state>.json`), so the generic code has no application vocabulary.
- The Go tests check each case input against the full request the engine builds (not only the key set), which also pins the workflow and state blocks to the workflow file.
- `promptfoo validate config` accepts the suite config, so `npm run assistant-step:validate` exists and needs no model.

## Validation

- `task test-assistant` (go vet, `go test -race`): all packages pass.
- `prompt-eval`: `npm test` 176 tests pass (95 of them new); `npm run check-sync-assistant` reports 11 files in sync; phraseforge's `npm run check-sync` still passes unchanged.
- `npm run assistant-step:validate`: "Configuration is valid." No test and no command in this task called a model.
- Cross-implementation parity: the 18 render vectors and 7 error vectors pass in JavaScript and in Go.
- Drift checks, by hand: changing a workflow instruction, using a tool the state lacks in an example reply, editing a schema file, and giving a case the wrong output kind each made the matching Go test fail; restoring them made the suite pass again.
- Not covered: how any real model scores on the suite, and the quality of the prompts (next task and later).

## Documentation Review

- `prompt-eval/README.md`: written (it was empty): applications registry, sync command, rendering, suites, adding an application.
- `assistant/README.md` and `assistant/CHANGELOG.md`: updated for the prompts, the sync flow, the cases and the `pending` key.
- Constitution: `specs/tech-stack.md` says prompts and the sync convention generically and needs no change. The `## Now` roadmap line is removed at delivery.
- Durable knowledge appended to `specs/memory.md` (3 entries).

## Documentation Updates

Done: `prompt-eval/README.md`, `assistant/README.md`, `assistant/CHANGELOG.md` (two entries), three `specs/memory.md` entries. The `## Now` line of `specs/artifacts/assistant/roadmap.md` was removed at delivery (approved by the user).
