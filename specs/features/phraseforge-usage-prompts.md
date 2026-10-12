---
title: Prompts for usage phrases and examples (prompt-eval first, then phraseforge)
kind: feature
status: approved
version: 1
updated: 2026-10-12
branch: main
---

## Problem / Motivation

The code of the previous slice (phraseforge-item-job-usage-examples) is inert: no default prompt asks for the ` | usage` part of a generate vocabulary line, uses `{{usage}}` or `{{examples}}`, or asks for the optional `example`. prompt-eval (the sibling repository) is the source of truth for phraseforge's default prompts: they are authored and measured there, then copied into internal/config/prompts/default by `npm run sync-prompts`, and `npm run check-sync` fails on drift of prompts, reply schemas and context windows. The user wants to reuse prompt-eval for other applications, so its phraseforge material moves under a phraseforge folder. The user's instructions: work in the main branch of both repositories and do not commit; do not strive for perfect prompts, make them good enough to work acceptably (improving them is a separate task much later); run no more than 2–3 tests against Ollama and focus them on what is relevant (the dev Ollama is the user's laptop).

## Acceptance Criteria

- [x] prompt-eval layout: `prompt/default` moves to `prompt/phraseforge/default` and `prompt/lang` to `prompt/phraseforge/lang` (a plain move in the working tree, nothing committed, nothing staged); the prompt loader, the sync script, the suite configs, the tests, comments and the README follow; `npm test` and `npm run check-sync` pass; prompt-eval stays on main.
- [x] The default generate-vocabulary prompt asks for lines `phrase {grammar} [transcription] | usage` and no longer for `= translation`: the usage is a short phrase taken or shortened from the text showing the word in use (a noun phrase, a verb with its object, or subject plus verb; at most 80 characters; never a whole sentence), in the language of the text; its worked examples show it.
- [x] The default generate-vocabulary-item prompt uses `{{examples}}` and `{{usage}}` and asks for an optional `example` field (same shape and limits as the usage; different from the known examples; null when it cannot do better); its two worked examples carry an `example`.
- [x] prompt-eval's own item prompt rendering understands `usage` and `examples` like phraseforge's `renderItemPrompt` (the two placeholders; examples one per line prefixed `- `); the vocabulary item suite's reply schema equals phraseforge's `VocabularyItemSchema` (it has the optional `example`) and the suite can give a usage and examples to a case.
- [x] No more than 3 real model calls against Ollama in total, chosen for relevance: one generate-vocabulary case that must produce usage parts, and one or two vocabulary item cases with a usage and existing examples (an RTL script such as Arabic first, a CJK case if a third call is used); results recorded in the spec; good enough, not perfect.
- [x] Merged to phraseforge: `npm run sync-prompts` copies the new defaults into internal/config/prompts/default; the dev ConfigMap prompts (phraseforge/k8s/configmap.yaml) for generateVocabulary and vocabularyItem carry the new text (they override the embedded defaults on the dev cluster); `npm run check-sync` passes (a note about the production Helm values differing is expected: the jdp-helm repository is a separate change and is not touched); go tests of phraseforge pass.
- [x] phraseforge/README.md (the default prompts paragraph names the prompt-eval folder) and phraseforge/CHANGELOG.md are updated.

## Approach

(1) Move the two folders in prompt-eval and fix every reference (lib/prompts.js PROMPT_DIR, scripts/sync-prompts.js, suite configs under vocabulary-translation, models-item and generate, tests, comments, README); verify with `npm test` and `npm run check-sync`; no model involved.
(2) Edit the two default prompts, prompt-eval's item rendering and the item suite (schema, usage and examples vars for a case), unit-test the rendering.
(3) At most 3 real Ollama cases (see the criteria), record the results.
(4) `npm run sync-prompts`, update the dev ConfigMap prompts, `npm run check-sync`, go tests, phraseforge docs.
(5) Documentation and wrap-up.

Key decisions: prompt-eval keeps one root per application (`prompt/phraseforge`) so other applications can get their own; the prompt text is deliberately simple and measured only enough to see that the formats work, because tuning is a later task; no default prompt for the known-phrase check or for revising translations and phrases (roadmap items `phraseforge-two-step-vocabulary-generation` and `phraseforge-item-job-revisions` bring their own).

## Affected Areas

In the sibling prompt-eval repository: prompt/ (moved), lib/prompts.js, lib/correction.js (item rendering), lib/tests.js and the vocabulary-translation suite (config, generate-tests.js, assertions), scripts/sync-prompts.js, generate/*.yaml and models-item/promptfooconfig.yaml (paths and comments), test/*.js, README.md; in this repository: phraseforge/internal/config/prompts/default (synced copy), phraseforge/k8s/configmap.yaml, phraseforge/README.md, phraseforge/CHANGELOG.md, and tests that read the defaults if any need an update.

## Out of Scope

Tuning prompt quality (a separate task much later); the jdp-helm production values; prompts for models items, other purposes, the known-phrase check, translation append or phrase rewrite; changes to phraseforge's Go code beyond what the synced prompts need; committing anything in either repository.

## Implementation Notes

* prompt-eval (sibling repo, uncommitted, main): prompt/default -> prompt/phraseforge/default and prompt/lang -> prompt/phraseforge/lang (42 files, plain mv); updated lib/prompts.js (PROMPT_DIR), scripts/sync-prompts.js, vocabulary-translation/promptfooconfig.yaml, models-item/promptfooconfig.yaml, generate/*.yaml comments, lib/tests.js comment, test/representation.test.js, test/prompts.test.js.
* generate-vocabulary.txt: format `phrase {grammar} [transcription] | usage`, usage rule (short phrase copied from the text, original language, <=80 chars, never a whole sentence/translated/made up, left out if none), worked Arabic and German examples carry usage.
* generate-vocabulary-item.txt: JSON template and rule for optional `example`; blocks "Usage in the text this phrase came from: {{usage}}" and "Known examples of this phrase, never repeat one of them: {{examples}}"; both worked examples carry `example`. Wording of the example rule was tightened once after call 2 (see Validation).
* prompt-eval code: lib/correction.js renderItemPrompt handles usage/examples + exported formatExamples; lib/tests.js passes usage/examples vars (data rows arb-pol and cmn-pol first rows got usage and examples); reply schemas in vocabulary-translation/promptfooconfig.yaml and correction/vocabulary.yaml gain optional `example`; lib/validate-reply.js style checks exampleShort and exampleNew; lib/generate-checks.js parses `| usage` and adds usagePresent, usageShort, usageFromText; unit tests added (npm test 59/59).
* phraseforge: `npm run sync-prompts` copied the two changed prompts into phraseforge/internal/config/prompts/default; phraseforge/k8s/configmap.yaml generateVocabulary and vocabularyItem prompts replaced with the new default text (config_test.go TestConfigMapItemPromptsMatchDefaults compares them) and its comment path updated; jdp-helm untouched; phraseforge/internal/ai/itemprompt_test.go TestRenderItemPromptTemplateWithoutTheNewPlaceholdersIsUnchanged now uses a literal legacy template because the default now contains the placeholders.

## Validation

* prompt-eval: `npm test` 59 pass, 0 fail; `npm run check-sync`: embedded prompts, schemas and context windows match; two expected notes that the Helm prompts for vocabularyItem and generateVocabulary differ (production uses Helm).
* phraseforge: `go build ./...`, `go vet ./...`, `go test ./...` pass (run without the dev DB env vars, so DB-backed tests may have skipped; they do not read prompts).
* Model calls (3 of 3 used, gemma4:12b on the dev Ollama, SMOKE=1, LANGS=arb): call 1 generate-vocabulary Arabic, 41 s, 1,246+227 tokens, score 0.857: all usage checks passed; failures were existing ones (missing transcription lines, non-dictionary forms). Call 2 item prompt Arabic arb-pol-001, 22 s, 1,121+80 tokens, score 0.971: valid JSON with `example`, but the model repeated the known example (exampleNew failed). Call 3 same case after the tightened example rule and block order, 24 s, 1,136+54 tokens, score 0.978: `example` was the usage phrase, not a known one; the case still shows failed only for the existing transcription threshold (mubarmij). One sample each, confidence Medium. A repeated example is harmless in the app because FillExample drops duplicates. Chinese case not run. Prompt tuning is left to the later task.

## Documentation Review

Checked phraseforge/README.md (default prompts paragraph named the old prompt-eval folder; item prompt paragraph already described usage/examples/example), phraseforge/CHANGELOG.md (earlier entry said no default prompt asks for usage yet, and named the old folder), prompt-eval README (paths updated in the move step). No constitution file needed a change.

## Documentation Updates

phraseforge/README.md default prompts paragraph now names prompt/phraseforge/default/system/; phraseforge/CHANGELOG.md: Added entry about usage now says the default prompts ask for it and that deployed/Helm prompts must be updated, earlier entry's folder path corrected. Roadmap `## Now` line removed. No constitution change.
