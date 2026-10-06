---
title: Define transcription and grammar snippets once per language in phraseforge; clearer Admin/LLM configuration
kind: feature
status: done
version: 1
updated: 2026-10-06
branch: main
---

## Problem / Motivation

Step 2 of the refactor whose step 1 is specs/features/prompt-eval-prompt-layout.md (done: prompt-eval/prompt/ holds default/system/*.txt (9), default/snippet/{grammar,transcription}.txt, lang/<lang>/snippet/*.txt (31); lib/prompts.js resolver; prompt-eval/prompt is the source of truth). In the phraseforge Admin > LLM tab (internal/server/static/js/admin-app.js renderLLMTab / renderLanguageSectionsCards): (a) transcription is configured in two places: the per-source-language "Transcription" prompt kind rows (llm_prompts) and the Language sections table (language_llm_sections.transcription_prompt feeding {{transcriptionPrompt}}); only the vocabulary_item and models_item prompts have the placeholders (internal/ai/itemprompt.go), while the generate_transcription, generate_vocabulary and generate_models prompts carry generic inline transcription/grammar wording; (b) the "LLM prompts" form starts with an empty textarea (defaults never reach the browser) so it is unclear how a snippet is embedded in a final prompt; (c) the tab is crowded: Language sections cards sit under the prompt cards; (d) grammar and transcription snippets are two columns of one table, unreadable; (e) kind dropdown labels (admin.llm_kind_* in internal/i18n/i18n.go, en and pl) such as "Title" do not match the prompt file names (e.g. generate-title). Go defaults today are consts/inline strings in internal/config/config.go; ai.Service.prompt (internal/ai/ai.go) resolves llm_prompts override else purpose default; Generate() sends the prompt as the system message without placeholder rendering; EffectiveSections (internal/ai/itemcalls.go) returns the stored sections, with DefaultTranscriptionSection filled in only when the language needs transcription (IME config) and has none.

## Acceptance Criteria

1. phraseforge embeds a copy of prompt-eval/prompt/default (system + snippet) with go:embed (the Docker build cannot reach prompt-eval); a sync/check script (extending prompt-eval scripts/sync-prompts.js) fails when the embedded copy differs byte for byte from prompt-eval/prompt/default. Go defaults in internal/config/config.go are replaced by the embedded files (config tests updated). Note: for generate purposes the embedded text is the production-tuned (Helm-derived) text that prompt-eval tests, which differs from the old Go default for title and translation only in line breaks per the existing check-sync notes.

2. The default prompts for generate-transcription, generate-vocabulary and generate-models contain the additive placeholders in both prompt-eval/prompt/default/system and the embedded copy; prompt-eval's generate suites render prompts through lib/prompts.js renderPrompt with the row's language so evals see the same prompt; golden/unit tests updated; existing wording unchanged (diff shows only additions).

3. ai.Service.Generate (and chunked/lines paths that call it, and correctlines if it renders) renders {{grammarPrompt}} and {{transcriptionPrompt}} in the system prompt of every kind from the source language's sections via the same one-pass rendering and EffectiveSections semantics as item prompts; a prompt without placeholders is sent unchanged (Go unit tests).

4. The admin bootstrap response exposes the default prompt text per kind and the default snippets; in the LLM tab the prompt textarea is pre-filled with the default (placeholders visible) when adding a row and a note lists the placeholders available for that kind; editing an existing row shows its stored prompt.

5. admin.llm_kind_* labels equal the file-name-derived names in en and pl; every place showing a kind (select, table rows) uses them.

6. A new "Languages" tab exists in the Admin tab strip; the Language sections cards are removed from the LLM tab; the tab shows one table row per (language, snippet) and an add/edit form; saving/deleting one snippet does not alter the other snippet of that language; export/import of admin configuration is unchanged and still passes its tests.

7. Docs updated: README (Language sections paragraph), CHANGELOG [Unreleased] (user-facing: new Languages tab, renamed labels, pre-filled prompt, placeholders in generate prompts), k8s/configmap.yaml comments; docs state that a deployed prompt override (configmap/Helm) must contain the placeholders to use the snippets.

8. go build ./... and go test ./... pass for phraseforge; prompt-eval npm test and npm run check-sync pass; the admin UI is checked in a real browser run (run skill) for the LLM and Languages tabs.

## Approach

Ordered small steps; the user reviews after each; work stays uncommitted on main, user commits manually:

1. Embed defaults + sync check + config.go uses them (no behavior change besides the Helm-derived text noted).
2. Additive placeholders in prompt-eval defaults and embedded copy, prompt-eval suites render via resolver, update goldens.
3. Generate renders snippets (Go tests).
4. Bootstrap exposes defaults, LLM form pre-fills.
5. i18n label rename en/pl.
6. Languages tab and removal of the cards from the LLM tab.
7. Docs/CHANGELOG/configmap, then full validation and browser check.

Risks to record: deployed Helm/configmap prompts override the Go defaults, so the new placeholders have no effect in an environment until its deployed prompts (k8s/configmap.yaml here; ../jdp-helm values, a different repo — changing it needs the user's separate approval) get them too; existing per-language llm_prompts rows keep their stored text and will not have placeholders (the pre-filled form helps an admin add them); the user's machine cannot run model evals now, so no behavior evals are part of this spec; line endings must be checked with `file` before scripted edits (several prompt-eval files are CRLF).

## Affected Areas

phraseforge/internal/config/config.go and a new embedded prompts directory, internal/ai/ai.go (+chunk.go/correctlines.go if needed), internal/ai/itemcalls.go, internal/server/admin.go (bootstrap), internal/server/static/js/admin-app.js, internal/i18n/i18n.go, k8s/configmap.yaml, README.md, CHANGELOG.md, tests; prompt-eval/prompt/default/system/{generate-transcription,generate-vocabulary,generate-models}.txt, prompt-eval/scripts/sync-prompts.js, prompt-eval lib/tests.js and suites.

## Out of Scope

DB schema change; ../jdp-helm changes; replacing the generic inline wording or any other prompt tuning; model eval runs and smoke tests; removing the per-language llm_prompts rows; committing, pushing or opening PRs.

## Implementation Notes

Embedded defaults: phraseforge/internal/config/prompts/default/ (11 files) is a byte copy of prompt-eval/prompt/default, loaded with go:embed in internal/config/prompts.go; config.go's ~200 lines of prompt consts replaced by vars (DefaultTranslationPrompt ... DefaultModelsItemPrompt, DefaultGrammarSnippet, DefaultTranscriptionSnippet). prompt-eval scripts/sync-prompts.js rewritten: sync copies prompt/default into phraseforge, --check compares the copy byte for byte plus reply schemas, num_ctx, correction template; a deployed Helm prompt that differs is a note, not drift. Verified drift detection with a deliberate one-byte change.

Additive placeholders in generate-transcription ({{transcriptionPrompt}}), generate-vocabulary ({{grammarPrompt}}, {{transcriptionPrompt}}), generate-models ({{transcriptionPrompt}}) in prompt-eval defaults and the embedded copy; prompt-eval generate suites render via lib/prompts.js renderPrompt with the row's language; verified: each template minus the placeholders equals the old file, and every rendered system message equals template + snippets in all 9+28+28 changed tests; other 16 of 22 test sets byte-identical to the golden capture.

ai.Service.Generate renders the snippets through renderSystemPrompt (usesSnippets / renderSnippets in itemprompt.go, EffectiveSections semantics unchanged); a prompt without a placeholder is sent unchanged and costs no lookup; unit tests added. The Generate wiring itself needs a DB pool and has no direct unit test.

Admin bootstrap exposes llmDefaults (ai.Service.DefaultPrompts), llmPlaceholders (ai.PromptPlaceholders) and snippetDefaults; the LLM form pre-fills the default for the kind (swapped on kind change only while untouched), taller textarea, placeholder hint line (en/pl).

LLM kind labels renamed in en and pl to match file names (Generate translation, Generate transcription, Generate title, Process text, Process dialog, Generate vocabulary, Generate models, Generate vocabulary item, Generate models item; pl: Generuj tłumaczenie, Generuj transkrypcję, Generuj tytuł, Przetwórz tekst, Przetwórz dialog, Generuj słownictwo, Generuj modele, Generuj pozycję słownictwa, Generuj pozycję modeli).

Languages tab (admin-app.js): flat table, one row per (language, snippet), form with Snippet dropdown prefilled with the language's snippet or the default; saving/deleting one snippet re-sends the other unchanged and deletes the row when both are blank; section cards removed from the LLM tab; no backend/API/export-format change.

Fixes after the user tested on the dev cluster (k3d-k8s-lab, deployed twice with `task deploy-phraseforge`): (a) admin.tab_languages and three more keys were missing from adminAppI18nKeys in internal/server/admin.go (raw key shown) — fixed, and a new test TestEveryAdminAppKeyIsServedAndTranslated fails the build for any T("key") in admin-app.js not served/translated (verified it fails without the fix); (b) the dev ConfigMap k8s/configmap.yaml overrides all nine prompts, so the three generate prompts showed no placeholder (the earlier belief that only the two item prompts were overridden was wrong) — placeholders added to the three configmap prompts; TestConfigMapItemPromptsMatchDefaults now checks seven prompts equal the compiled-in defaults (translation and title differ only in line wrapping).

Added after request: ../jdp-helm/jdp-frontend/values.yaml (phraseforge section; CRLF file) got the same three placeholder additions plus a CHANGELOG entry; all nine Helm prompts are now byte-identical to prompt-eval/prompt/default/system (checked), helm lint passes, helm template renders; image tag NOT changed (the user sets it when releasing the image). This was out of the original scope and done at the user's explicit request; the user first named jdp-helm/jdp-workflow, which holds only Argo workflows, and confirmed jdp-frontend.

## Validation

go build, go vet, go test ./... pass for phraseforge; prompt-eval npm test 54/54 and check-sync pass (no Helm note after the Helm change); phraseforge pf-* component tests 31/31; validate-specs passes. Export/import: handlers unchanged; TestAdminConfigExportJSONRoundTrip and TestImportAdminConfigRejectsBadFilesBeforeTouchingTheDatabase (7 bad files, each 400) pass; TestAdminConfigExportImportRoundTripDB is written and skipped unless PHRASEFORGE_TEST_DB and PHRASEFORGE_TEST_DB_REPLACE_ALL=1 (import wipes everything) — it was NOT run; the user ran export and import on the dev cluster by hand and saw no errors. UI: the user checked the LLM and Languages tabs on the dev cluster (found the two issues above, fixed and redeployed); no automated browser run was done. Not done: model eval/smoke runs (the user's machine cannot take the load); the real promptfoo run with the new paths in prompt-eval. Behavior note: a stored admin llm_prompts row keeps its text and needs the placeholder added by hand. Acceptance criterion 8's browser-run part was satisfied by the user's manual check, not by the run skill.

## Documentation Review

User-facing changes: yes (new Languages tab, renamed labels, pre-filled prompts, placeholders) → phraseforge/CHANGELOG.md [Unreleased] entries (2 Added, 3 Changed); README paragraphs updated; k8s/configmap.yaml comments updated; the Helm chart's own CHANGELOG (jdp-helm/jdp-frontend/CHANGELOG.md) got one Changed entry. Constitution files (mission, tech-stack, roadmap): no drift found.

## Documentation Updates

phraseforge/README.md ("Default prompts" and "Language snippets" paragraphs, placeholder note in the vocabularyItem bullet), phraseforge/CHANGELOG.md, phraseforge/k8s/configmap.yaml (comments, three prompts), ../jdp-helm/jdp-frontend/{values.yaml,CHANGELOG.md}.
