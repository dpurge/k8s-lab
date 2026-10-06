---
title: Centralize prompt files in prompt-eval/prompt/ directory
kind: feature
status: done
version: 1
updated: 2026-10-06
branch: main
---

## Problem / Motivation

Prompt text is scattered across multiple directories in the prompt-eval sibling repository: `generate/prompts/*.txt` (7 files: translation, transcription, title, processText, processDialog, generateVocabulary, generateModels), `vocabulary-translation/prompts/system.txt`, `models-item/prompts/system.txt`, and 31 per-language (25 grammar, 6 transcription) grammar and transcription snippet files at `vocabulary-translation/prompts/<lang>-grammar.txt` and `<lang>-transcription.txt`. The language table in `lib/tests.js` tracks which files exist for each language. Currently, `scripts/sync-prompts.js` writes these prompts from phraseforge Go defaults and Helm values.

This spec defines step 1 of a two-step refactor: consolidate all prompts under a single `prompt-eval/prompt/` directory as the source of truth, with all test suites reading exclusively from this layout. Step 2 (separate spec, following this one) will refactor phraseforge to load prompts from this directory and update the Admin UI accordingly.

The user's rule: do not improve any existing prompt text — copy all content as-is.

## Acceptance Criteria

1. `prompt/default/system/` holds 9 files, byte-identical to the current prompts: generate-translation, generate-transcription, generate-title, process-text, process-dialog, generate-vocabulary, generate-models copied from `generate/prompts/{translation,transcription,title,processText,processDialog,generateVocabulary,generateModels}.txt`; generate-vocabulary-item from `vocabulary-translation/prompts/system.txt`; generate-models-item from `models-item/prompts/system.txt`. Verified with `cmp` against the originals before they are removed.

2. `prompt/lang/<lang>/snippet/{grammar,transcription}.txt` hold the 31 existing (25 grammar, 6 transcription) per-language files, byte-identical (moved, same content), for all languages that have them today.

3. `prompt/default/snippet/transcription.txt` = "Transcribe using the standard romanization for this language." (phraseforge's current DefaultTranscriptionSection from `internal/ai/itemcalls.go`). `prompt/default/snippet/grammar.txt`: a NEW generic grammar-tags text derived from the common head of existing per-language grammar files (generic part-of-speech tag list only, no language-specific rules). This is the only new prompt text; flagged for user review; adoption into phraseforge is step 2.

4. A single resolver module in `prompt-eval/lib` (e.g., `lib/prompts.js`) resolves prompts: system prompt = `lang/<lang>/system/<name>.txt` if present, else `default/system/<name>.txt` (system prompts CAN be overridden per language); snippet = `lang/<lang>/snippet/<x>.txt` if present, else `default/snippet/<x>.txt` (default snippet applies only to languages the `lib/tests.js` language table marks as needing it, preserving today's test behavior). Renders `{{grammarPrompt}}` and `{{transcriptionPrompt}}` with the same one-pass semantic as phraseforge's `renderItemPrompt` (reusing or moving the existing function from `lib/correction.js`, not duplicating).

5. All suites (vocabulary-translation, models-item, correction, generate/*) and `lib/tests.js` read prompts only through the resolver and `prompt/` directory; old prompt directories (`generate/prompts`, `vocabulary-translation/prompts`, `models-item/prompts`) are removed; promptfoo configs reference `prompt/` paths.

6. `scripts/sync-prompts.js` writes and checks `prompt/default/system/*.txt` (comparing Go defaults/Helm values and schemas); `npm run check-sync` passes.

7. Equivalence test: for every existing suite and language, the rendered prompt through the new resolver is byte-identical to the rendered prompt from the old layout (captured as golden output BEFORE the move, stored under `.agent/tmp` or test fixtures). `npm test` (node --test) passes, including new resolver unit tests (system override per language, snippet override, default fallback, missing file error with actionable message, no re-expansion of placeholders inside values).

8. Smoke: `npm run <suite>:smoke` against the model on at most ONE suite (vocabulary-translation) as a final check that the promptfoo run reads the new path; run only with explicit bound (timeout enforced by the calling tool) and only after the user's go-ahead.

## Approach

Ordered small steps, each reviewed by the user before the next:

1. Capture golden rendered prompts from the old layout.
2. Fill `prompt/default/system/` by copying + `cmp`; write default snippets.
3. Move the 31 language files to `prompt/lang/<lang>/snippet/` with new names.
4. Add `lib/prompts.js` + unit tests, then switch `lib/tests.js` and suites to it.
5. Update `scripts/sync-prompts.js` and `package.json`/`README.md`, remove old directories, run `check-sync`, `npm test`, and golden equivalence tests.
6. Bounded smoke test on vocabulary-translation suite.

Decisions made with the user: source of truth after this step = `prompt-eval/prompt/` files (phraseforge will embed/load them in step 2); language snippet file names are `grammar.txt`/`transcription.txt`; two specs, this one first.

Risk: promptfoo `file://` path resolution relative to config directory; Helm prompts (not Go defaults) are what generate/ suites test, so `default/system/` copies the CURRENT `generate/prompts/` files, not Go constants. Line endings: check `file` on each file before any scripted edit and verify `git diff --stat`/`cmp` afterwards (CRLF risk).

## Affected Areas

- `prompt-eval/prompt/**` (new directory tree)
- `prompt-eval/lib/{tests.js, correction.js, prompts.js (new)}`
- `prompt-eval/{generate, vocabulary-translation, models-item, correction}/` configs and `*-tests.js`
- `prompt-eval/scripts/sync-prompts.js`
- `prompt-eval/package.json`
- `prompt-eval/README.md`
- `prompt-eval/test/*`

No k8s-lab code changes in this spec.

## Out of Scope

- Any phraseforge (Go/JS/Helm/Admin UI) change.
- Any wording change to existing prompts.
- Per-language system overrides beyond supporting the mechanism (none are created; `lang/arb/system/` stays empty).
- Committing — the user commits manually; work stays uncommitted on main.

## Implementation Notes

Baseline before changes: npm test 43/43 pass, npm run check-sync pass, no CRLF in prompt files. Golden output captured for all test suites (1,959 tests, normal + SMOKE=1 modes, deterministic across two runs).

Copied 9 system prompts into prompt/default/system/ (generate-translation, generate-transcription, generate-title, process-text, process-dialog, generate-vocabulary, generate-models, generate-vocabulary-item, generate-models-item); all byte-identical to originals, verified with cmp. prompt/default/snippet/transcription.txt = "Transcribe using the standard romanization for this language." (phraseforge's DefaultTranscriptionSection). prompt/default/snippet/grammar.txt = the first 13 lines common to all 25 existing grammar files (part-of-speech tag list), verified identical across all 25.

Copied the 31 files (25 grammar + 6 transcription) to prompt/lang/<lang>/snippet/{grammar,transcription}.txt for 25 language directories; all byte-identical to originals, verified with cmp.

Added lib/prompts.js (createPrompts: systemPrompt, snippet, snippets, renderPrompt; per-language system prompt override of defaults, per-language snippet override, default snippet only when the caller says the language needs it, LF normalization of snippets, system prompts byte-exact, path-segment validation, reuses renderItemPrompt from lib/correction.js) and test/prompts.test.js (11 tests).

Repointed lib/tests.js (CRLF preserved; language table now holds only names; generate suites resolve system prompt via resolver with the row's language), vocabulary-translation and models-item promptfoo configs (file://../prompt/default/system/<name>.txt), comments in generate/*.yaml, test/representation.test.js (scans prompt/lang/*/snippet; Romanian check now path-based, two new assertions). Rewrote scripts/sync-prompts.js (CRLF preserved) to write/check prompt/default/system/<name>.txt. Removed old generate/prompts, models-item/prompts, vocabulary-translation/prompts (40 tracked files, all with byte-identical copies in prompt/).

Known limit: the item suites use one prompt file per promptfoo config, so per-language system-prompt overrides are applied by the generate suites and by the resolver, but not yet by vocabulary-translation/models-item promptfoo prompts (would need a promptfoo prompt function; no override files exist yet).

## Validation

After the final change: npm test 54/54 pass (43 existing + 11 new); npm run check-sync passes; npm run sync-prompts in write mode reports "unchanged" for all 9 prompts and writes nothing; test builders' output byte-identical to golden capture (1,959 tests); promptfoo validate config reports valid for vocabulary-translation, models-item and generate/title; both item configs' file:// paths resolve to existing files (1398 and 1176 bytes). No regressions vs the baseline.

Acceptance criterion 8 (real promptfoo smoke run against the model) was skipped at the user's request because their machine cannot take the load now; it remains unverified end to end — promptfoo has not been run with the new paths against a model.

## Documentation Review

No user-facing change in k8s-lab and none in prompt-eval (internal refactor of test fixtures and tooling). No changelog entry needed (prompt-eval has no CHANGELOG; k8s-lab's is unaffected). Constitution files: no drift from this step. Phraseforge-side docs (README "Language sections", configmap comments, CHANGELOG) are step 2's scope.

## Documentation Updates

None required; the spec and roadmap only.
