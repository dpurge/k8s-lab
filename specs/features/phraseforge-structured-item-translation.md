---
title: One structured LLM call per vocabulary/models item, matching the prompt-eval setup
kind: feature
status: done
version: 1
updated: 2026-09-30
branch: main
---

## Problem / Motivation

The user tunes vocabulary-translation prompts with promptfoo in
`prompt-eval/vocabulary-translation`: one template (`prompts/system.txt`)
with named placeholders, per-language optional sections (grammar tags,
transcription system), `ollama:chat:gemma4:12b` with `think: false`, and a
JSON output checked against a schema. Phraseforge can't reuse any of it:

- Vocabulary/models items are translated with the generic text
  `translation` purpose (plain-text output, wrapped in a fixed
  system+user template), transcription is a separate job, and grammar and
  notes are never generated.
- There is nowhere to store per-language grammar-tag or
  transcription-system descriptions.
- Output is free text; nothing can be validated.

Goal: a prompt that passes the eval can be pasted into phraseforge (and
back) unchanged, and phraseforge sends the same request the eval sends.

Verified live on prod Ollama 0.34.1 (`gemma4:12b`, 2026-09-30) with the
eval's `system.txt` rendered for `der Koffer` (German → Polish):

- `format` set to the eval's JSON schema returns schema-shaped JSON, both
  non-streaming and with `stream: true` (46 chunks concatenating to valid
  JSON; `done_reason: stop`). The streaming client from
  `llm-streaming-progress-timeout` can carry it unchanged.
- The model returned `"grammar": "N f"` (Polish *walizka*) where the eval
  data expects `N m` (German *der Koffer*), because `system.txt` says
  "grammar tags attached to the **translated** phrase". The eval doesn't
  catch this: `assertions/validate-translation.js` hardcodes every score to
  1. Fixing the eval is out of scope (user decision); this spec's default
  prompt uses the eval's text with that one word changed (see
  Assumptions).

User decisions (2026-09-30):

- Placeholders are camelCase, exactly as in the eval: `{{sourceLanguage}}`,
  `{{targetLanguage}}`, `{{phrase}}`, `{{grammarPrompt}}`,
  `{{transcriptionPrompt}}`.
- One LLM call per item **per target locale** — the eval's shape (one
  `targetLanguage` per call).
- Models items get their own purpose and schema (phrase, transcription,
  translation — they store no grammar or notes).
- A completed call writes only fields that are still blank.
- Phraseforge only; the prompt-eval repo is not changed.

Assumptions (confirmed at approval, 2026-09-30, unless noted):

- Default `vocabularyItem` prompt = the eval's `system.txt` with
  "translated phrase" → "original phrase" in the grammar line (user:
  change the wording to get the correct result).
- The echoed `phrase` must equal the stored phrase after trimming and
  Unicode NFC normalization; a mismatch fails the job (the model answered
  about something else). The existing store-level phrase guard still
  protects against the item having been edited since the job was queued.
- Structured output is Ollama-only; a purpose pointed at OpenRouter gets no
  `format` and its output is still validated, so non-JSON output fails the
  job loudly rather than being stored.

## Acceptance Criteria

**Configuration**

- Two new purposes in `config.yaml` / `config.go` defaults and
  `k8s/configmap.yaml`: `vocabularyItem` and `modelsItem`, each with
  `provider`, `model`, `think`, `numCtx`, `timeoutSeconds` (1800), and
  `prompt` — same fields as every other purpose.
- Admin > LLM overrides accept kinds `vocabulary_item` and `models_item`
  (keyed by source language + target locale like `translation`);
  `llm_prompts_kind_check` extended.
- New table `language_llm_sections(language text PRIMARY KEY REFERENCES
  language(code), grammar_prompt text NOT NULL DEFAULT '',
  transcription_prompt text NOT NULL DEFAULT '')`, edited in a new
  "Language sections" block on Admin > LLM, and included as
  `language_sections` in the admin config export/import (import replaces
  it together with IME configs and LLM prompts; an export without the key
  still imports).

**Request (must match what promptfoo sends for the eval)**

- The prompt template is rendered by named-placeholder replacement:
  `sourceLanguage` = the item's language name from the `language` table
  (e.g. `deu` → German); `targetLanguage` = the locale's English name from a new
  `i18n.Locale.PromptName` (`en` → English, `pl` → Polish) — the `language`
  table has no English row (34 rows checked in the lab, 2026-09-30), so it
  can't resolve `en`;
  `phrase`; `grammarPrompt` and `transcriptionPrompt` = the source
  language's sections, or `""` when absent. `modelsItem` ignores
  `grammarPrompt` if its template doesn't use it.
- Sent as a single `user` message (no system message), with the purpose's
  model/think/numCtx, `stream: true`, and `format` = the item kind's JSON
  schema.

**Response**

- Vocabulary schema: `phrase` (string, required), `translation` (string,
  required), `grammar`, `transcription`, `notes` (string or null) — the
  eval's schema exactly. Models schema: `phrase`, `translation` (required),
  `transcription` (string or null).
- Validation in Go before any write: valid JSON, required fields present
  and non-empty, types as above, echoed phrase matches (see Assumptions).
  Any failure fails the job with a message naming the problem (e.g.
  `vocabulary item response: missing "translation"`) and writes nothing.

**Writes (blank fields only)**

- Vocabulary: `translation` + `notes` for the call's locale (via the
  existing `SetItemTranslationIfAbsent`), `transcription` if blank (via
  `SetItemTranscriptionIfBlank`), `grammar` if blank (new
  `SetItemGrammarIfBlank`, same phrase guard). Empty strings/null are never
  written.
- Models: `translation` for the locale and `transcription` if blank, via the
  existing setters.

**Jobs**

- New job kinds `generate_vocabulary_item` and `generate_models_item`
  (payload: list id, position, locale, phrase), one per item per locale.
- Which calls an item gets: one per site locale whose translation is
  missing; if none is missing but grammar (vocabulary) or transcription
  (when the language has a transcription section) is blank, one call for the
  first site locale; otherwise none.
- All four existing entry points enqueue the new kinds instead of separate
  item `generate_transcription` / `generate_translation` jobs:
  per-item Generate buttons (`server/generate.go`), Generate missing
  translations (`server/vocabulary.go`), import backfill
  (`server/export_import.go`), and the follow-ups after generating
  vocabulary/models from a text (`generate/generate.go`).
- Already-queued legacy item jobs of the old kinds still run.
- Texts and dialogs are unchanged.

## Approach

1. **`shared/llm`**: `Config.Format json.RawMessage`, sent as `format` on
   the Ollama path only; zero value omits it (knowledge unaffected). Test:
   `httptest` asserts `format` is in the request when set and absent when
   not.
2. **Language sections**: schema migration, a small store in
   `internal/ai` (get by language, list, replace-all in a tx), Admin > LLM
   editor (language select + two textareas, en/pl i18n), admin config
   export/import field. Tests: store round-trip is DB-bound, so unit-test the
   export/import struct (missing key tolerated).
3. **Purposes and prompt rendering**: `vocabularyItem`/`modelsItem` in
   `config.go` (defaults + tests) and `k8s/configmap.yaml`; extend
   `llm_prompts_kind_check` and the admin kind list; a pure
   `renderItemPrompt(template, vars)` with camelCase names. Tests:
   rendering with and without sections, unknown placeholder left as-is.
4. **Schemas and validation**: two schema constants plus pure
   `parseVocabularyItemResponse` / `parseModelsItemResponse`. Tests: good
   response; invalid JSON; missing/empty `translation`; wrong types; null
   optional fields; phrase mismatch (and NFC-equal phrase accepted).
5. **Job handler and writes**: `ai.HandleItemTranslation` for both kinds,
   registered in `main.go`; resolves purpose + override (existing
   `prompt()` path, new kinds), renders, calls `llm` with `Format`,
   validates, writes blank-only; new `vocabulary.Store.SetItemGrammarIfBlank`.
   Tests: pure `decideItemWrites(stored, response)`.
6. **Entry points**: replace item transcription/translation enqueueing at
   the four call sites with a shared `decideItemCalls` (pure, tested) that
   produces the per-locale list above.
7. **Verify**: lab deploy; for a `deu` and a `cmn` vocabulary item and one
   models item, run Generate missing translations and check the stored
   fields and the Jobs page; one bounded call against prod Ollama with the
   phraseforge-rendered prompt, diffed against the eval-rendered prompt to
   confirm they're byte-identical.

## Affected Areas

- `shared/llm/llm.go`, `shared/llm/llm_test.go`
- `phraseforge/internal/db/schema.sql`
- `phraseforge/internal/config/config.go`, `config_test.go`,
  `phraseforge/k8s/configmap.yaml`
- `phraseforge/internal/ai/` (new item handler, schemas, rendering,
  language sections store; `ai.go` kinds)
- `phraseforge/internal/vocabulary/vocabulary.go` (`SetItemGrammarIfBlank`)
- `phraseforge/internal/server/admin.go`, `generate.go`, `vocabulary.go`,
  `export_import.go`, `export_import_vocabulary.go`,
  `export_import_models.go`
- `phraseforge/internal/generate/generate.go`
- `phraseforge/internal/server/static/js/admin-app.js`,
  `phraseforge/internal/i18n/i18n.go`
- `phraseforge/main.go`
- `jdp-helm/jdp-frontend` values/template for the two new purposes (prod
  config, like the previous feature)

## Out of Scope

- Changes to the `prompt-eval` repo (its stubbed assertion, the
  "translated phrase" wording, reading phraseforge exports).
- Texts/dialogs translation and transcription (unchanged).
- Generating vocabulary/models lists from a text (`generate_vocabulary` /
  `generate_models` calls themselves) — only their per-item follow-ups
  change.
- Structured output on the OpenRouter path (`response_format`).
- One call returning all locales at once.

## Implementation Notes

1. `shared/llm`: `Config.Format json.RawMessage`, added to the Ollama
   request as `format` only when non-empty. Test
   `TestStreamSendsFormatOnlyWhenSet`.
2. Language sections: `language_llm_sections` table (`schema.sql`,
   `ON DELETE CASCADE` from `language`); `internal/ai/sections.go` store
   (list/get-or-blank/upsert/delete); admin bootstrap `languageSections`;
   `POST /api/v1/admin/language-sections`, `DELETE
   /api/v1/admin/language-sections/{language}`; admin config export gains
   `language_sections`, and import replaces sections only when the key is
   present (absent → untouched, `[]` → cleared) — test
   `TestAdminConfigLanguageSectionsAbsentVsEmpty`; Admin > LLM
   "Language sections" form and table (`admin-app.js`), 8 en/pl i18n keys.
   Lab migration verified (`\d language_llm_sections`).
   Also fixed (user-reported): `admin.llm_kind_generate_vocabulary`/
   `_generate_models` had no label in any locale nor in
   `adminAppI18nKeys`; added, plus `i18n.Has` and
   `TestEveryLLMKindHasAdminLabel` (every `ai.ValidKinds` entry labelled in
   every locale).
3. Purposes: `VocabularyItem`/`ModelsItem` in `config.Config`
   (yaml `vocabularyItem`/`modelsItem`, 1800 s, NumCtx 0 to match the eval);
   default prompts are `config.DefaultVocabularyItemPrompt` (eval
   `system.txt` with "original phrase") and `DefaultModelsItemPrompt` (no
   grammar/notes), mirrored into `k8s/configmap.yaml` and guarded by
   `TestConfigMapItemPromptsMatchDefaults`. `ai.ValidKinds` +
   `llm_prompts_kind_check` gain `vocabulary_item`/`models_item` (lab
   constraint verified); `purposeDefault` maps them. Admin > LLM shows the
   target-locale field for `translation`, `vocabulary_item`, `models_item`
   (`TARGETED_LLM_KINDS`). `i18n.Locale.PromptName` (English/Polish).
   `ai.renderItemPrompt` (single-pass camelCase replacement) with 3 tests.
   Note: the eval's prompt files are CRLF; phraseforge stores LF, so step 7
   compares rendered prompts with line endings normalized.
4. Schemas and validation (`internal/ai/itemresponse.go`):
   `VocabularyItemSchema` (the eval's is-json schema verbatim) and
   `ModelsItemSchema`; `parseVocabularyItemResponse`/
   `parseModelsItemResponse` decode into `*string` fields (null → blank,
   non-string → `"<field>" must be a string or null`), require non-blank
   `phrase`/`translation`, and compare the echoed phrase to the stored one
   after trim + NFC (`golang.org/x/text/unicode/norm`, already in the module
   graph via pgx — `go mod tidy` moved it and `gopkg.in/yaml.v3` from
   indirect to direct, same versions). Errors are prefixed
   `vocabulary item response:` / `models item response:`. A code-fenced
   reply is rejected (strict, per spec). 6 tests incl. the prod
   `der Koffer` reply and an NFC composed/decomposed case.
5. Job handler (`internal/ai/itemjob.go`): kinds
   `generate_vocabulary_item`/`generate_models_item`, both registered to
   `HandleItemTranslation` in `main.go`; payload `ItemJobPayload`
   (`resource_type`, `list_id`, `position`, `language`, `locale`,
   `phrase`). Flow: validate payload → language sections → prompt vars
   (source name from `catalog.GetLanguageIfExists`, target from
   `PromptName`) → `prompt()` (purpose default or Admin override for the
   new kind) → one user message with `Format` = schema via the new shared
   `callLLM` (extracted from `Generate`, which now uses it too) →
   `decideItemWrites` → `applyItemWrites` (translation+notes first, then
   transcription, then grammar; each blank-only; any store error fails the
   job). Refinement beyond the spec: grammar/transcription are written only
   when the language has that section (without one, the prompt asks for
   nothing, so a volunteered value is a guess). New
   `vocabulary.Store.SetItemGrammarIfBlank`; new `ai.ItemReplyWriteback`
   implemented by main.go's existing `itemTranslationWriteback` adapter
   (`SetItemTranslationWithNotes`, `SetItemGrammarIfBlank`); `ai.New` gains
   one parameter. Job result: `{response, applied{translation,
   transcription, grammar}}`. 9 tests (`itemjob_test.go`).
   Lab check (2026-09-30): job for list 11 position 2 `拥有` → pl wrote
   grammar `V`, skipped already-set translation/transcription, 27 s.
6. Entry points (`internal/ai/itemcalls.go` shared by `server` and
   `generate`): `EffectiveSections` (stored sections, plus
   `DefaultTranscriptionSection` "Transcribe using the standard
   romanization for this language." when the IME config needs
   transcription and no section exists — user decision 2026-09-30, so e.g.
   `arb` keeps item transcriptions); pure `ItemCallLocales(ItemState,
   sections, locales)`; `NewItemJob`; `ItemJobsFor`. `HandleItemTranslation`
   now uses `EffectiveSections` too. Rewired: per-item Transcribe/Translate
   buttons → one `enqueueItemButtonJob` (Transcribe = caller's locale; the
   old `kind` parameter dropped); Generate missing translations → caller's
   locale only, and also items missing grammar/transcription that have a
   section (so existing items get grammar); import `enqueueItemBackfill`
   (gains `grammar`; a locale present in the file still counts as
   provided); generate-from-text follow-ups (gains `grammar` from the
   extraction). Removed dead code: `decideItemBackfill` + its 4 tests,
   `generate`'s `llmGeneratePayload`/`enqueueLLMGenerate`/
   `transcriptionTargetLanguage`. Legacy item jobs of the old kinds still
   run through `HandleGenerate`. Tests: `TestItemCallLocales` (7 cases),
   `TestNewItemJobKindAndPayload`. Admin > LLM spacing: the prompt-table
   and sections-form cards gained `margin-bottom:1.5rem` (user-reported
   touching cards).

## Validation

- `go build`/`go vet`/`go test ./...` pass in `shared` (incl. `-race`),
  `phraseforge`, `knowledge` after every step.
- Lab (2026-09-30): direct job for `拥有` wrote grammar `V` only; user
  pressed Generate missing translations on list 11 (pl) → one
  `generate_vocabulary_item` job, `applied.transcription: true` for
  `幼崽` (user-confirmed; note the model returned `yòusǎ`, correct pinyin
  is `yòuzǎi` — model accuracy, not code).
- Request parity with promptfoo 0.123.1 (read from its published source,
  `dist/src/providers-vvyLQL6h.js:15841`): a text prompt is sent as one
  user message (`parseChatPrompt(prompt, [{role:"user", ...}])`), `think`
  from config, nunjucks with `autoescape: false`. Difference: the eval's
  `promptfooconfig.yaml` sets no `format`, so it runs without structured
  output (phraseforge always sends the schema).
- Rendered-prompt parity for `cmn-pol-001` (nunjucks 3.2.4 from the eval's
  lockfile vs `renderItemPrompt` with the lab's saved `cmn` sections):
  identical after CRLF → LF normalization except the approved
  "translated" → "original" word. Lab `cmn` sections equal the eval files.
- Prod Ollama with the phraseforge-rendered prompt + schema: `{"phrase":
  "名字", "translation": "imię", "grammar": "N", "transcription":
  "míngzi", "notes": ""}` in 27.1 s — the eval's expected row exactly.
- Prod config: `jdp-helm/jdp-frontend` values/template gain
  `vocabularyItem`/`modelsItem` (values.yaml CRLF preserved; `git diff`
  equal with and without `-w`); `helm lint` passes; the rendered
  ConfigMap loaded through `config.Load` yields prompts identical to the
  Go defaults, 1800 s timeouts, provider limits 300/60.

## Documentation Review

| Changelog | Category | Entry |
|---|---|---|
| `phraseforge/CHANGELOG.md` | Added | One structured call per item per locale (`vocabularyItem`/`modelsItem`, eval placeholders, schema-validated, blank-only writes). |
| `phraseforge/CHANGELOG.md` | Added | Admin > LLM > Language sections, in config export/import, with the default transcription section. |
| `phraseforge/CHANGELOG.md` | Changed | The four item entry points queue the structured call; Generate missing translations also fills grammar/transcription. |
| `phraseforge/CHANGELOG.md` | Fixed | Missing Generate vocabulary/models kind labels. |
| `knowledge/CHANGELOG.md` | — | None: `shared/llm`'s `Format` is unused by knowledge (internal). |

Doc drift: `phraseforge/README.md` "LLM configuration" lacked the item
purposes and language sections. Constitution: no drift.

## Documentation Updates

- `phraseforge/CHANGELOG.md` `[Unreleased]`: the four entries above.
- `phraseforge/README.md`: `vocabularyItem`/`modelsItem` config bullet and a
  "Language sections" paragraph.
- `specs/artifacts/phraseforge/roadmap.md`: `## Now` line removed.
- `specs/memory.md`: promptfoo request shape; no English row in `language`.
- Outside this repo: `jdp-helm/jdp-frontend` values, template, CHANGELOG.
