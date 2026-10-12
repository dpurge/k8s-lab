---
title: Examples for vocabulary phrases
kind: feature
status: done
version: 2
updated: 2026-10-12
branch: main
---

## Problem / Motivation

A vocabulary phrase has a translation and free-form notes per site locale but no usage examples. The user wants each phrase to carry a short list of examples (under 5; a noun phrase, a verb with its object, or subject+verb for an intransitive verb; not whole sentences) shown with the vocabulary. They are the groundwork for later roadmap slices (phraseforge-two-step-vocabulary-generation, phraseforge-vocabulary-review-job, phraseforge-text-dialog-review-job), where the examples become context for translating and reviewing, and can be edited the same way translations are. This slice only stores, edits, imports/exports and displays them; no LLM is involved.

Decisions taken with the user: examples belong to the shared phrase (every list linking the phrase shows the same ones, like translations); an example is source-language text only (no per-locale translation); at most 5 per phrase and at most 80 characters each; vocabulary items only (not models items); displayed in the notes column below the notes text, following the script conventions (dir/RTL, enlarged script) of the phrase cell.

Names confirmed by the user: table `phrase_examples`, JSON field `examples` (array of strings), Go field `Examples []string` on `vocabulary.Item`.

## Acceptance Criteria

- [x] New table `phrase_examples(phrase_id bigint NOT NULL REFERENCES phrases(id) ON DELETE CASCADE, position integer NOT NULL CHECK (position BETWEEN 0 AND 4), example text NOT NULL CHECK (btrim(example) <> '' AND char_length(example) <= 80), PRIMARY KEY (phrase_id, position))` in phraseforge/internal/db/schema.sql, idempotent like the surrounding DDL.
- [x] `vocabulary.Item.Examples []string` (in position order, empty when none) is filled by `Items`; `Store.SetItemExamples(ctx, listID, position, examples []string) error` replaces the whole list of the phrase the item links to, in one transaction. Each example is normalised with textnorm.Phrase (trim + NFC); blank ones and exact duplicates (keeping the first) are dropped; more than 5 remaining, or one longer than 80 characters (code points, after NFC), is rejected with `ErrInvalidExamples` carrying the reason, and nothing is written. Visible in every list that links the phrase.
- [x] Examples follow the phrase like translations do: when `retargetPhrase` merges a phrase into an identical one, the survivor keeps its own examples and the merged phrase's examples not already present are appended up to 5; when a list's language changes (`relinkItemsToListLanguage`) the new phrase gets the old phrase's examples if it has none of its own; deleting the last link deletes the phrase and its examples (cascade).
- [x] API: the item in `GET /api/v1/vocabulary/{id}` has `examples` (array, `[]` when none); `POST .../items` and `PUT .../items/{position}` accept `examples` (array): absent leaves the examples unchanged, `[]` clears them; an invalid list gives 400 `validation_error` with the reason; permissions are those of the item edit (CanEdit for the list's language).
- [x] Export carries `examples` per item (omitted when none); import: the key present replaces the phrase's examples (an empty array clears), absent leaves them, same validation; an invalid list is reported through the error mechanism the import already uses for an invalid item.
- [x] Display (list view and manage view): below the notes text in the notes column (alone when there are no notes) each example on its own line, in the notes text's own size and colour (not enlarged, not bold); each line keeps the script's direction (`dir` from the list's script direction) so RTL scripts such as Arabic and Hebrew read right to left, and the block is only as wide as its longest line so it starts where the notes start; notes text and the other columns are unchanged. The vocabulary table keeps a minimum width for the phrase (8.5 rem, five enlarged CJK characters), translation (11 rem) and notes (15 rem) columns so none is squeezed.
- [x] Edit form: an "Examples" textarea, one example per line, with direction and alignment by script (`dir` and the script's class, as for the phrase field) and the same size and font as the other form fields, a hint stating the limits (5 examples, 80 characters), saved together with the item; empty means none. New i18n keys exist for every site locale (en, pl).
- [x] Tests: store (round trip with diacritized Arabic bytes preserved, normalisation and duplicate/blank dropping, count and length limits, shared across two lists, merge, language relink, cascade), API (get, put with absent/empty/valid/invalid, permission), import/export round trip including an RTL sample. Verified on the dev k3d cluster without any LLM call: migration applied to existing data; examples set through the API for an item are returned by GET and exported; display checked in the browser by the user.

## Approach

1. **Schema and store**: table, `Item.Examples` filled by `Items` with one extra query per list, `SetItemExamples` and `ErrInvalidExamples`, store tests.
2. **Follow-the-phrase**: the merge in `retargetPhrase` and the copy in `relinkItemsToListLanguage`, tests.
3. **API**: `examples` on the item response and on POST/PUT (absent = unchanged, `[]` = clear), validation to 400, tests.
4. **Export and import**: `examples` on the export item and import item (a nil slice means untouched, a non-nil empty slice clears), `SyncItem` carries it, round-trip test with an RTL sample.
5. **UI**: examples below the notes in the items table (list and manage views) with dir/enlarged per script, the edit form textarea, i18n keys en/pl, a small CSS rule; no automated UI test exists for vocabulary-app.js, so this step is checked by the user in the browser on dev.
6. **Deploy to dev** (task deploy-phraseforge), verify without LLM, then documentation (CHANGELOG, spec, memory if anything durable).

Key decisions: examples per phrase, not per item or per locale (consistent with translations and with the later slices that use them as shared context); a separate table with a position column rather than an array column (consistent with the other tables, keeps per-example constraints in the database); validation in the store as the single boundary, the CHECK constraints as a backstop; the merge rule keeps the survivor's examples first.

Alternatives rejected: a text[] column on phrases (no per-example constraints, harder to extend with per-example data later); per-locale examples (an example is source-language text).

Risks: `Items` costs one more indexed query per call for callers that do not need examples (accepted for one Item shape); 80 characters are code points, so a fully diacritized Arabic phrase of about six words fits but a longer one is rejected, as intended; the migration is additive and creates a new table only.

## Affected Areas

phraseforge/internal/db/schema.sql; phraseforge/internal/vocabulary/vocabulary.go (Item, Items, SyncItem/SyncItemsTx, retargetPhrase, relinkItemsToListLanguage) and a new internal/vocabulary/examples.go; phraseforge/internal/server/vocabulary.go (apiVocabItem, apiVocabItemRequest, handlers); phraseforge/internal/server/export_import_vocabulary.go (export and import item types, sync); phraseforge/internal/server/static/js/vocabulary-app.js and the css it uses; phraseforge i18n message files for en and pl (find where the vocabulary.* keys live); tests next to each; phraseforge/CHANGELOG.md.

## Out of Scope

Models items; generating or suggesting examples with an LLM, using them as prompt context, reviewing them (roadmap slices phraseforge-two-step-vocabulary-generation, phraseforge-vocabulary-review-job, phraseforge-text-dialog-review-job); per-locale translations of examples; examples in texts or dialogs; searching or filtering by example; changing how notes work.

## Implementation Notes

1. Step 1, schema and store: table `phrase_examples` in internal/db/schema.sql (position 0-4, non-blank, at most 80 characters as constraints); `Item.Examples` filled by `Items` with one extra batched query per call; new internal/vocabulary/examples.go with `SetItemExamples` (replace-all in one transaction), `NormalizeExamples` (trim + NFC, drops blanks and exact duplicates, rejects more than 5 or over 80 characters as `ErrInvalidExamples`) and `CheckExamples`. `vocabulary.Item` holds a slice now and is no longer comparable with ==: two comparisons in internal/generate/parse_test.go use reflect.DeepEqual.
2. Step 2, following the phrase: `mergeExamples` is called from `retargetPhrase` (survivor's examples first, the merged phrase's missing ones appended up to 5); `relinkItemsToListLanguage` copies the examples to a new phrase that has none.
3. Step 3, API: `examples` on every item of GET /api/v1/vocabulary/{id} (always an array); POST and PUT accept `examples` (absent leaves them, `[]` clears); `CheckExamples` runs before any write, so a refused request creates or changes nothing; on PUT the examples are written after `UpdateItem`, so they go to the phrase the item links to afterwards.
4. Step 4, export and import: `examples` per item on export (omitted when none) and on import (present replaces, `[]` clears, absent leaves); `SyncItem.Examples` (nil leaves alone, non-nil replaces) is applied in `SyncItemsTx`, which covers both the update and the create path; `vocabItemsUnchanged` compares examples when the incoming item mentions them (otherwise an import that changed only the examples was skipped as unchanged); `validateVocabImportItems` reports an invalid list naming the item.
5. Step 5, UI: `renderExamples` and the table cell in vocabulary-app.js; an "Examples" textarea (`#field-examples`) in the item form, sent as an array split on lines; editor.js applies the script styling and source IME to it; CSS in templates/layout.html; i18n keys `vocabulary.col_examples` and `vocabulary.examples_hint` in en and pl and in the client key whitelist in internal/server/vocabulary.go.
6. Deviations from the approved Approach, made after the user reviewed the page on dev: the examples first used the phrase cell's enlarged class and, in RTL, sat flush against the right edge of the notes cell; the user found them too prominent and the notes column squeezed. Now: no enlargement, notes text styling, a `width: fit-content` block that keeps `dir`; the vocabulary table has its own class `vocab-items-table` (the models table shares `vocab-table` and is untouched) with minimum widths for the phrase (widened from 7 to 8.5 rem at the user's request), translation and notes columns; the textarea is styled by id to match the other fields (an input's generic CSS rule outranks the script class but a textarea's does not, which had made it larger than the phrase field).
7. Tests added: store (round trip with diacritized Arabic, normalisation, limits and rejection without writing, exact limits, replace/clear, shared between lists, missing item, cascade on delete, merge with and without the cap, language change with and without examples on the target phrase, sync write/replace/clear/nil and an invalid sync that changes nothing); a real-database HTTP harness in internal/server/vocabulary_examples_test.go with API tests (add/get, empty array not null, PUT absent/valid/empty, invalid gives 400 and writes nothing, examples follow a phrase edit, student can see but not change) and export/import tests (export carries examples, round trip unchanged, replace, absent, invalid, clear, create through import) plus unit tests for the comparison and the validation.
8. Dev data: 22 examples on 12 phrases of dev lists 11, 12 and 13 were inserted by SQL so the user could see them; nothing in the repository.

## Validation

- Baseline before the work: go build and the full suite green with PHRASEFORGE_TEST_DSN and PHRASEFORGE_TEST_DB set to the dev Postgres (port-forward): all packages passed.
- After: `gofmt -l .` clean; `go build ./...` and `go vet ./...` clean; `go test -count=1 -v ./...` with both variables set: 326 PASS (300 before this feature, so 26 new), 0 FAIL, 1 SKIP (TestAdminConfigExportImportRoundTripDB, which wipes the database and needs PHRASEFORGE_TEST_DB_REPLACE_ALL=1; deliberately not run). Frontend: `npm test` in internal/server/static/components, 45 passed. No regressions.
- Mutation checks: removing the boundary validation made the invalid-examples API test fail (the item was created and then a 500 came back); making the import comparison ignore examples made both the unit test and the HTTP round trip fail (an examples-only import was skipped as unchanged). Both files were restored.
- Dev k3d cluster: `task deploy-phraseforge` several times; the migration created `phrase_examples`; the job queue was empty and no LLM call was made anywhere in this feature. The UI was checked with Playwright screenshots at 1280 and 1024 px (two Arabic right-to-left lists, one Chinese list, the edit form) and by the user in the browser; the layout was corrected twice from that review (see Implementation Notes).
- Not covered: there is no automated test for vocabulary-app.js and editor.js (checked visually only); the production migration was not run (it creates one new table; how schema.sql is applied in production was not confirmed, as noted in the earlier stable-item-id spec); validate-specs could not be run (script missing).

## Documentation Review

- phraseforge/CHANGELOG.md: entries needed (Added: examples; Changed: table column widths). Root CHANGELOG.md and the other artifacts: none needed (change is under phraseforge/ only).
- phraseforge/README.md: the paragraph describing a vocabulary item and what an import updates did not mention examples; the testing note named only internal/vocabulary and internal/models as needing PHRASEFORGE_TEST_DSN, but internal/server's new vocabulary API tests use it too (and internal/jobs and the admin config round trip use PHRASEFORGE_TEST_DB).
- README.md (root), specs/tech-stack.md, specs/mission.md: no drift; no constitution change.
- Changelog result: {phraseforge/CHANGELOG.md, Added, examples}, {phraseforge/CHANGELOG.md, Changed, table column widths}.

## Documentation Updates

- phraseforge/CHANGELOG.md: one Added and one Changed entry under `## [Unreleased]`.
- phraseforge/README.md: the vocabulary item paragraph and the testing note updated.
- specs/artifacts/phraseforge/roadmap.md: the `## Now` line for this feature removed.
- specs/memory.md: three entries appended (see the report).
- No constitution change.
