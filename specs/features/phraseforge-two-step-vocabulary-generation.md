---
title: "Two-step vocabulary generation: check known phrases for a new example"
kind: feature
status: approved
version: 1
updated: 2026-10-12
branch: main
---

## Problem / Motivation

Generation of a vocabulary list already carries a usage phrase per word (`| usage`, spec phraseforge-item-job-usage-examples, prompts in phraseforge-usage-prompts, both delivered), and the item job can return an optional `example` that is stored through FillExample (cap 5, deduplicated). But item jobs are planned by ai.ItemCallLocales (internal/ai/itemcalls.go): one per site locale still lacking a translation, or one for the first site locale when only grammar or transcription is blank. A phrase that is already fully translated (phrases are shared across lists) gets no job, so the usage found for it is dropped and the phrase never gains an example. This is slice 2 of the roadmap item split with the user (slice 1: usage + examples in item jobs, done; slice 3: phraseforge-item-job-revisions, later).

User decisions: the check looks only at known phrases that came with a usage; one LLM call per list returning JSON verdicts; a phrase marked as needing an example gets the normal item job for one site locale; a failing check is skipped and logged, never failing the generate job; the new LLM purpose is named checkVocabulary (prompt file check-vocabulary.txt, admin kind label Check vocabulary) with the same model settings as generateVocabulary in the dev ConfigMap (ollama, gemma4:12b, numCtx 8192, timeoutSeconds 1800, maxAttempts 3). Ollama runs on the user's laptop, so real model calls in testing stay at 1-3 total.

## Acceptance Criteria

- [x] After a vocabulary list is generated, the candidates that have a usage and whose stored phrase is known (it already has at least one translation or example from before this generation) are sent in ONE check call per list (split into several calls only when the list exceeds the per-call candidate limit fixed in step 1, proposed default 20). New phrases and candidates without a usage are not sent and keep today's behaviour.
- [x] The check call gets each candidate numbered with phrase, usage, existing examples and existing translations (per site locale), and replies JSON `{"results":[{"n":1,"needsExample":true}]}` constrained by a schema and validated; an item job (the existing generate_vocabulary_item kind, first site locale only, carrying the usage) is enqueued only for entries with needsExample true; entries missing from the reply, unknown numbers and non-boolean values mean no job.
- [x] If the check call fails or its reply is invalid after its retries, the generate job still succeeds, no check-driven job is queued, and the failure is logged with the list id (best-effort, like the existing backfill enqueue).
- [x] The check never changes what is queued for new phrases or for locales missing a translation; a phrase with a missing translation or blank grammar/transcription gets its job exactly as before.
- [x] A new purpose `checkVocabulary` exists end to end: config key with the dev ConfigMap settings above, valid prompt kind `check_vocabulary` (admin can override it), admin kind label Check vocabulary (en and pl), default prompt file check-vocabulary.txt embedded from prompt-eval via sync-prompts, with the placeholders of the other generate prompts where they apply; a prompt in the ConfigMap equals the embedded default (config_test.go).
- [x] The default prompt lives in prompt-eval (prompt/phraseforge/default/system/check-vocabulary.txt) with a suite check on the reply schema and no more than 2 real Ollama calls, results recorded in the spec; npm test and check-sync pass in prompt-eval (the Helm-differs notes stay expected; jdp-helm not touched); good enough, not perfect.
- [x] Unit tests without an LLM cover: which candidates are sent, the reply handling (valid, missing entries, bad values), the failure path, and that jobs are queued only for needsExample entries; phraseforge go build, vet and tests pass.
- [x] phraseforge/README.md and phraseforge/CHANGELOG.md describe the check, the new purpose and that a deployed (Helm) prompt for it must be added.

## Approach

**Step 1 (no model):** a Checker seam in internal/generate: after syncVocabItems and before the backfill, build the known-candidate list from stored items (need a vocabulary store read returning each item's examples and per-locale translations), call the checker, and for needsExample entries enqueue the item job; ItemState gains a NeedsExample flag so ItemCallLocales returns the first site locale when set; tests with a fake checker.

**Step 2 (no model):** the purpose plumbing: config key, ValidKinds, ai.Service prompt/config lookup, admin label (en/pl), embedded default prompt file placeholder text, a structured checker implementation in internal/ai using the existing structured-call and schema machinery, with tests on a stub provider.

**Step 3:** prompt-eval: check-vocabulary prompt, suite/schema, ≤2 model calls, sync-prompts, ConfigMap block, check-sync, go tests.

**Step 4:** docs and wrap-up.

**Key decisions:** the verdict decides only whether the usage shows a use the known examples do not cover (new senses of translations are out of scope here); JSON verdict instead of line format for validation; reuse the item job instead of a new kind; best-effort failure; one call per list to keep model calls low.

## Affected Areas

phraseforge/internal/generate (generate.go, new check code and tests), phraseforge/internal/ai (itemcalls.go ItemState/ItemCallLocales, the new checker, ai.go ValidKinds and config lookup), phraseforge/internal/vocabulary (a read of examples and translations of a list's items), phraseforge/internal/config (config.go, prompts.go, embedded prompts/default, config_test.go), phraseforge/internal/i18n/i18n.go and internal/server/admin.go (kind label), phraseforge/k8s/configmap.yaml, phraseforge/README.md, phraseforge/CHANGELOG.md, and in the sibling prompt-eval repository prompt/phraseforge/default/system/ plus its suite and scripts/sync-prompts.js.

## Out of Scope

Appending senses to translations and rewriting phrases (phraseforge-item-job-revisions); checking new phrases or candidates without a usage; an example-only job kind; the jdp-helm production values; models lists; prompt tuning beyond good enough; committing anything.

## Implementation Notes

* Step 1 (no model): internal/ai/itemcalls.go ItemState.NeedsExample, ItemCallLocales returns the first site locale for it (missing locales still win); internal/vocabulary/vocabulary.go Store.TranslationsByItem(listID) (non-blank translations by item id and locale); internal/generate/check.go: KnownCandidate, Checker interface (NeedsExample(ctx, language, known) map[int]bool), Service.SetChecker, knownCandidates (usage present and the phrase has a translation or an example), checkInBatches (maxCheckBatch = 20, failed batch logged with list id and skipped, indexes outside the batch ignored), Service.itemsNeedingExample; generate.go syncVocabItems runs the check before enqueueItemBackfill, which takes a needsExample flag (models pass false); no checker set = behaviour as before.
* Step 2 (no model): purpose `checkVocabulary`: config.CheckVocabulary (same settings as generateVocabulary: ollama, gemma4:12b, numCtx 8192, 1800 s, 3 attempts) with DefaultCheckVocabularyPrompt embedded from system/check-vocabulary.txt; kind `check_vocabulary` in ai.ValidKinds, purposeDefault and PromptPlaceholders (sourceLanguage, candidates); llm_prompts kind CHECK in db/schema.sql; admin label (en "Check vocabulary", pl "Sprawdź słownictwo") in i18n.go and the admin.go key list; internal/ai/check.go: CheckVocabularySchema, CheckItem, formatCheckItems (numbered from 1, usage/examples/translations lines, " | " joins, "none"), renderCheckPrompt, parseCheckVerdicts (outer shape strict so a bad reply is corrected; bad entries ignored), Service.CheckVocabulary (one user message, structured call, runWithCorrection within the purpose's attempts); languageName helper extracted from itemPromptVars; generate.AICheck adapter; main.go calls generateSvc.SetChecker(generate.AICheck{AI: aiSvc}). Deviation from the Approach: the first draft of the default prompt was written in step 2 (the code needed an embedded default), tested and left unchanged in step 3.
* Step 3: prompt-eval (sibling repo, uncommitted, main): prompt/phraseforge/default/system/check-vocabulary.txt; suite check-vocabulary/ (promptfooconfig.yaml, generate-tests.js, data/cases.yaml with 2 cases: German 4 candidates, Arabic 3); lib/check-vocabulary.js (formatCandidates, renderCheckPrompt, parseVerdicts, scoreCheck mirroring the Go side), lib/promptfoo-check-assert.js, test/check-vocabulary.test.js; package.json scripts check-vocabulary and check-vocabulary:smoke; scripts/sync-prompts.js check-sync now also verifies the suite's reply schema against CheckVocabularySchema in check.go, its num_ctx against the Go default and the prompt file; phraseforge/k8s/configmap.yaml gets a checkVocabulary block (same settings, default prompt text) and config_test.go compares it with the default.

## Validation

* Step 1 mutation check: with the check call disabled the DB test TestSyncVocabItemsQueuesAnExampleJobOnlyForKnownPhrasesTheCheckMarks fails, restored it passes (a first mutation did not compile and was redone).
* phraseforge: go build, go vet, go test ./... pass with the dev DB (PHRASEFORGE_TEST_DSN/DB via port-forward, nothing skipped) after steps 1 and 2; after step 3 without the DB env vars (step 3 touched no DB code). DB tests added: generate (marked phrase gets one job for the first locale, unmarked known phrase none, new phrases as before and never sent, failing check still succeeds, no checker unchanged) and vocabulary TranslationsByItem; ai tests for formatting, rendering, parsing, correction, budget exhaustion and no-items use a fake Ollama.
* prompt-eval: npm test 67 pass, 0 fail; npm run check-sync passes (two expected notes that the Helm prompts for vocabularyItem and generateVocabulary differ); a deliberate schema and num_ctx break in the suite config was caught by check-sync, then restored.
* Model calls (2 of 2 used, gemma4:12b on the dev Ollama, --no-cache): German deu-001, 27 s, 432+52 tokens, 4/4 verdicts as expected (yes, no, yes, no); Arabic arb-001, 7 s, 426+40 tokens, 3/3 as expected. One sample each, confidence Medium; no Chinese case. Prompt tuning is left to the later task.
* Not covered: the check has not run in the deployed dev app against a real generated list; a production Helm prompt for checkVocabulary does not exist yet (the embedded default applies).

## Documentation Review

Checked phraseforge/README.md (configuration list had no entry for the check; generate/item prompt paragraphs unchanged), phraseforge/CHANGELOG.md (Added had no entry), prompt-eval README is empty. No constitution file needed a change.

## Documentation Updates

phraseforge/README.md: new `checkVocabulary` configuration entry (what is sent, reply shape, 20 per call, which phrases get a job, failure behaviour, placeholders, admin kind). phraseforge/CHANGELOG.md: new Added entry, including that production has no Helm prompt for it yet. Roadmap Now line removed. No constitution change.
