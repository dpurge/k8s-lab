---
title: Item job may rewrite a phrase
kind: feature
status: approved
version: 1
updated: 2026-10-12
branch: main
---

## Problem / Motivation

Slice 3a of the roadmap item `phraseforge-item-job-revisions`, which was split with the user in this session into (A) phrase rewrite, this spec, and (B) appending a sense to an existing translation (a later spec; the roadmap line of `phraseforge-item-job-revisions` stays as it is and is revised when slice B is taken up). Generating a vocabulary list (prompts: spec phraseforge-usage-prompts, delivered) extracts phrases from a text, and the step-1 model sees the word in context but often emits it in a form that is not the dictionary form: a different case than customary, or a joined form such as a preposition fused with a personal suffix. The item job today must echo the stored phrase exactly (parseVocabularyItemResponse sends a reply whose phrase differs back for correction) and only writes blank fields (FillTranslation, FillGrammar, FillTranscription), so a badly extracted phrase can never be fixed. The user's direction: let the item job rewrite the phrase; originally the phrase was preserved exactly because the extracting model sees the text, so the item job now gets the usage (delivered in the previous slices) and may rewrite. If the phrase is rewritten, its transcription and grammar tags go stale with it and may need rewriting too, so a rewrite carries all three and is applied as one change through retargetPhrase, the same path as a manual edit (internal/vocabulary/vocabulary.go: it updates the shared phrase row, or merges it into an existing phrase with exactly the new values; the survivor's non-blank translations win, examples are merged, list links re-pointed). User decisions (answers given in this session): optional `rewrite` object in the reply; the existing translations, notes and examples stay with the phrase as in a manual edit (stale translations of other locales are accepted for now, a later review job can fix them); only item jobs that carry a usage may rewrite (jobs from the manual buttons, imports and models items never do). Ollama runs on the user's laptop: real model calls in testing at most 3 in total.

## Acceptance Criteria

- [x] The vocabulary item reply schema (VocabularyItemSchema) gains an optional `rewrite`: an object `{"phrase": string, "grammar": string|null, "transcription": string|null}` or null; the top-level `phrase` stays the exact stored phrase as today (echo check unchanged); the models item schema is unchanged.
- [x] The reply is decoded leniently like `example`: a `rewrite` that is not an object, has no non-blank string `phrase`, or has wrongly typed grammar/transcription counts as no rewrite and costs no correction round; a null or absent grammar/transcription keeps the stored value.
- [x] A rewrite is applied only when the job carries a usage and its `rewrite.phrase`, after trim and NFC, differs from the stored phrase; a rewrite without a usage on the job, for a models item, or with an unchanged phrase is ignored.
- [x] A new store call applies it in one transaction: it follows the item by id, requires the item to still hold the phrase the job started with (otherwise ErrItemChanged/ErrItemNotFound and the rewrite is skipped), and calls retargetPhrase with the new phrase, grammar and transcription; the item id is kept, other lists that link the phrase see the new values, translations, notes and examples stay with the phrase, and a collision with an existing phrase merges as retargetPhrase already does.
- [x] The rewrite is applied after the job's other writes, so those still target the phrase as the job started with; a skipped or failed rewrite is logged and does not fail the job, and the job result records whether it was applied.
- [x] The default vocabulary item prompt in prompt-eval asks for the optional `rewrite` only when the usage shows the phrase is not in dictionary form (case, joined forms), tells the model to give grammar and transcription for the rewritten phrase, and carries the field in its worked examples; prompt-eval's schema, reply validation (style checks) and a few cases cover it; no more than 3 real Ollama calls in total, results recorded in the spec; `npm test` and `npm run check-sync` pass (the Helm-differs notes stay expected; jdp-helm not touched); the embedded default and the dev ConfigMap prompt are synced and equal.
- [x] Tests without an LLM cover: lenient decoding, the three ignore rules, the store call (item id kept, other list sees the change, merge on collision, ErrItemChanged skip, translations and examples kept), the job applying a rewrite after its fills and surviving a failed one; phraseforge go build, vet and tests pass.
- [x] phraseforge/README.md and phraseforge/CHANGELOG.md describe the rewrite and that a deployed (Helm) item prompt must be updated to get it.

## Approach

1. **Step 1 (no model)**: store call in internal/vocabulary (itemjob.go next to FillTranslation): a Fill-style method guarded by lockItemIfStill that calls retargetPhrase; DB tests.
2. **Step 2 (no model)**: internal/ai: optional `rewrite` in VocabularyItemSchema and VocabularyItemResponse with lenient decoding; ItemStore interface (consumer-side, adapter in main.go) gets the rewrite call; decideItemWrites/applyItemWrites apply it last, only for a job with a usage; result `applied.rewrite`; tests with the fake store and fake Ollama.
3. **Step 3**: prompt-eval: item prompt text, suite schema, lib/validate-reply.js checks, cases (a joined form that must be rewritten, a dictionary form that must not), at most 3 model calls, sync-prompts, ConfigMap prompt, check-sync, go tests.
4. **Step 4**: docs and wrap-up.

Key decisions: reply field `rewrite` as an object so the exact-echo check of the top-level phrase stays; rewrite goes through retargetPhrase unchanged (a single manual-edit path); applied last and best-effort so a rewrite problem never loses the job's translation; the usage on the job is the only trigger, so typed phrases are never rewritten by buttons or imports; jobs for the other site locales read the (possibly rewritten) stored phrase when they start, because a job reads its item's current phrase at its start and the guard only compares the phrase at the job's own start and end.

## Affected Areas

phraseforge/internal/vocabulary (itemjob.go, tests), phraseforge/internal/ai (itemresponse.go, itemjob.go, tests), phraseforge/main.go (ItemStore adapter), phraseforge/internal/config/prompts/default (synced), phraseforge/k8s/configmap.yaml, phraseforge/README.md, phraseforge/CHANGELOG.md, and in the sibling prompt-eval repository: prompt/phraseforge/default/system/generate-vocabulary-item.txt, vocabulary-translation (suite config, data), correction/vocabulary.yaml, lib/validate-reply.js, tests.

## Out of Scope

appending a sense to an existing translation and extending the step-2 check with a needsSense verdict (slice B, later); changing grammar or transcription without a phrase change; clearing or regenerating translations of a rewritten phrase; models items; the jdp-helm production values; prompt tuning beyond good enough; committing anything.

## Implementation Notes

* Step 1: internal/vocabulary/itemjob.go Store.RewritePhrase(ctx, itemID, phrase, newPhrase, grammar, transcription) (applied, err): new phrase trimmed and NFC (blank = ErrBlankPhrase), lockItemIfStill guard (ErrItemChanged / ErrItemNotFound), unchanged phrase = not applied without error, empty grammar or transcription keeps the stored value, then retargetPhrase in one transaction.
* Step 2: internal/ai/itemresponse.go: VocabularyItemSchema gains optional `rewrite` (object or null, required inner `phrase`, optional grammar and transcription); PhraseRewrite type and VocabularyItemResponse.Rewrite read leniently by rewriteOf (not an object, blank or non-string phrase, wrongly typed grammar or transcription = no rewrite, no correction round); internal/ai/itemjob.go: itemWrites.Rewrite set by usableRewrite only for a vocabulary job whose usage survives cleanUsage and whose phrase differs after trim and NFC, grammar and transcription dropped (= keep) where the language has no such section; applyItemWrites calls applyRewrite last with the phrase the job started with, best-effort (skip or failure logged with item id and outcome only, job succeeds), result key applied.rewrite for vocabulary items; ai.ItemStore gains RewritePhrase, main.go itemStore adapts it with skipOnStaleItem.
* Step 3: prompt-eval (sibling repo, uncommitted, main): generate-vocabulary-item.txt has `"rewrite": null` in the template, a rule (null when the phrase is already a dictionary headword or the usage is empty; otherwise the dictionary form with grammar and transcription), and worked examples 3 (German Kinder -> das Kind) and 4 (Arabic joined form); the rule and example 4 were strengthened once after call 1; schemas of vocabulary-translation/promptfooconfig.yaml and correction/vocabulary.yaml gain rewrite; lib/validate-reply.js style checks rewriteWithUsage, rewriteExpected, rewriteUnneeded, rewriteMissing; data rows arb-pol-006 (joined form, expected rewrite) and deu-pol-901; unit test added; the synced embedded default and the dev ConfigMap vocabularyItem prompt are equal (config_test.go TestConfigMapItemPromptsMatchDefaults).
* Not done: a safeguard against a rewrite that differs from the stored phrase only slightly (for example only in vowel signs) was offered and not requested; such a rewrite is applied.

## Validation

* phraseforge: go build, go vet, go test ./... pass with the dev DB after steps 1 and 2 (nothing skipped), after step 3 without the DB env vars (no DB code touched). New tests: store (item id kept and other list sees the change, translations and examples kept, empty grammar kept and given one written, merge into an existing phrase with the survivor's translation winning and examples merged, ErrItemChanged, ErrItemNotFound, ErrBlankPhrase, unchanged = not applied); ai (12 decoding cases, ignore rules: no usage, usage over 80 characters, unchanged phrase, other Unicode form, models item; section gating; rewrite applied last with the job's starting phrase; skipped, gone and failing rewrite do not fail the job; full job through fake Ollama with and without a usage).
* Mutation checks: store call without the keep-grammar rule fails its test; the job without the usage requirement fails two tests; both restored.
* prompt-eval: npm test 68 pass, 0 fail; npm run check-sync passes (two expected Helm notes; check-sync reported the schema drift between steps 2 and 3, fixed in step 3).
* Model calls (3 of 3 used, gemma4:12b, --no-cache, Arabic only): call 1 arb-pol-006 31 s, 1,323+63 tokens: valid JSON (Ollama accepts the object-or-null schema) but `rewrite: null`; call 2 same case after the prompt change, 27 s, 1,422+91 tokens: the model rewrote but proposed فِيه instead of the expected فِي (fails rewriteExpected); call 3 arb-pol-001 (dictionary form with a usage), 11 s, 1,440+62 tokens: `rewrite: null` as wanted (the case still fails only the existing transcription threshold). One sample each, confidence Medium; no German or Chinese call. Quality on hard joined forms is weak; tuning is left to the later task.
* Not covered: no run in the deployed dev app; the production Helm item prompt does not ask for rewrite yet.

## Documentation Review

* Checked phraseforge/README.md (the vocabulary item reply paragraph described only `example`) and phraseforge/CHANGELOG.md (no entry for the rewrite); prompt-eval README is empty. No constitution file needed a change.

## Documentation Updates

* phraseforge/README.md: the item reply paragraph describes `rewrite` (when it applies, how, failure, other locales not redone). phraseforge/CHANGELOG.md: new Added entry including that a Helm item prompt must be updated. Roadmap Now line removed. No constitution change.
