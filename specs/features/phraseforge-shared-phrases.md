---
title: Share identical phrases across vocabularies and models lists
kind: feature
status: done
version: 3
updated: 2026-10-09
branch: main
---

## Problem / Motivation

Every vocabulary list stores its own `vocabulary_items (list_id, position, phrase, grammar, transcription)` rows, with translations in `vocabulary_item_translation (list_id, position, locale)` (`phraseforge/internal/db/schema.sql`). Models lists mirror this in `models_items` / `models_item_translation`. The same phrase in two lists is therefore stored twice and translated twice: each copy is submitted to the LLM separately, even when an identical, already-translated copy exists elsewhere.

Goal: one stored copy of each phrase (same language, script, phrase, grammar, transcription), listed in any number of vocabularies or models lists. A phrase that is already translated is just listed, with no translation job.

Assumptions (confirmed by the user, 2026-10-09):
- Identity of a shared phrase is `(language, script, phrase, grammar, transcription)`; language and script come from the list.
- Editing a phrase changes it in every list that uses it (no fork).
- Translation `notes` belong to the shared phrase's per-locale translation.
- Scope is vocabulary and models lists.
- Existing duplicates are merged by a forced, destructive migration; losing some data is acceptable.
- Editing a shared phrase needs the same permission as today: `roles.CanEdit` for the phrase's language.
- Review decisions (2026-10-09, after the independent review): when a list's language or script changes, its items are re-linked to phrases in the new language (found or created) in one transaction, copying translations, so a phrase never sits in a list of a different language; phrase text is normalised (trim, Unicode NFC) at the store boundary so near-identical strings are not separate records.
- Import is an editing tool (confirmed by the user, 2026-10-09, after step 3b): the user exports, edits phrase, grammar, transcription and translations outside the app, and re-imports. Import therefore overwrites translations and changes the shared phrase; it must not be fill-only. Items are matched by a per-item `id` added to the export (the shared phrase's id), chosen over matching by position because position matching would rewrite shared phrases wrongly after any reordering, insertion or deletion in the file.

## Acceptance Criteria

- Adding the same (language, script, phrase, grammar, transcription) to two lists creates one `phrases` row and two list links.
- Adding a phrase that already has a translation for the viewer's locale enqueues no translation job; the translation shows immediately in the new list.
- A phrase without a translation for a locale still gets a job as today; once the job completes, the translation is visible in every list that links the phrase.
- Editing a phrase in one list changes it in all lists that link it. If the edit makes it identical to another existing phrase, the two are merged: translations are unioned per locale (surviving row's value wins), all list links are re-pointed, and the other row is removed, in one transaction.
- Removing an item from a list removes only the link; a phrase left with no links in any list is deleted in the same transaction.
- Deleting an item still renumbers the later positions of that list.
- Editing a shared phrase requires `roles.CanEdit` for its language, as today.
- Export adds an `id` to every item (the shared phrase's id); everything else in the format is unchanged.
- Import by list id matches file items to the list's items by item `id`:
  - an item with an `id` that the list links changes that shared phrase's phrase, grammar and transcription (merging into an identical existing phrase if there is one), so the change shows in every list that links it;
  - the translations in the file overwrite the phrase's stored translation and notes for each locale present in the file (a locale present with a blank translation clears it; a locale absent from the file is left untouched);
  - an item with no `id` is a new line: find-or-create, so an identical existing phrase is just linked, and a phrase already translated is not submitted for translation;
  - an item the list links but whose `id` is not in the file is unlinked from the list (the phrase itself is deleted only if no list links it);
  - the file's order becomes the list's order;
  - an `id` that is not linked by that list is treated as a new line, never as an edit of another list's phrase;
  - when the import changes the list's language or script, no item is edited in place: every line is a new line in the new language.
- Regenerating a list from its source text keeps the translations of phrases that are generated again, and does not submit them for translation.
- Changing a list's language or script re-links its items to phrases in the new language in one transaction (translations copied, orphaned old phrases removed); afterwards no item of the list links a phrase of another language.
- Phrase, grammar and transcription are trimmed and NFC-normalised before they are compared or stored, on every path (add, edit, import, generate, sync); the same text typed with a stray space or in a different Unicode form is the same phrase.
- In one import, an earlier line's phrase is never rewritten or deleted by a later line.
- A file with no item ids (an old export or a hand-written file) imports as all-new lines, with translations overwriting as above.
- Import still enqueues translation, transcription or grammar jobs only for what is missing after the import.
- The migration merges existing duplicates into shared phrases, reports a dry-run count of rows to be merged first, keeps the first translation found per locale, and is safe to run repeatedly.
- The same holds for models lists, with their own tables (no grammar, no notes).
- All existing phraseforge tests pass or are updated to the new shape; new tests cover find-or-create, merge-on-edit, orphan cleanup, and skip-translation.

## Approach

Names (confirmed by the user, 2026-10-09):

| | Vocabulary | Models |
|---|---|---|
| Shared phrase | `phrases(id, language, script, phrase, grammar, transcription)` | `models_phrases(id, language, script, phrase, transcription)` |
| Translation | `phrase_translation(phrase_id, locale, translation, notes)` | `models_phrase_translation(phrase_id, locale, translation)` |
| List link | `vocabulary_items(list_id, position, phrase_id)` | `models_items(list_id, position, phrase_id)` |

Design:
- A unique key on the identity columns, with null grammar/transcription treated as `''`. One find-or-create function per store is the only way items get created (HTTP add, generate-from-text, import).
- Translations hang off `phrase_id`, so delete-and-shift no longer moves them and the deferred-FK workaround in `schema.sql` goes away.
- Job decisions (`ItemJobsFor`) read translations through the join, so an already-translated phrase reports as translated and enqueues nothing.
- AI write-backs (`SetItemTranslationIfAbsent`, `SetItemTranscriptionIfBlank`, `SetItemGrammarIfBlank`, wired in `main.go` and `internal/ai/itemjob.go`) are re-keyed on `phrase_id`, keeping the stale-target phrase guard.
- `db.Migrate` applies the whole `schema.sql` on every run, so the data merge must be idempotent, guarded on whether `vocabulary_items.phrase` still exists.

Import (revised after step 3b, see Problem / Motivation): the export gets an item `id` (the phrase id). Import by list id no longer deletes and re-adds every item. It syncs the list to the file in one transaction: for each file line, an `id` the list links is edited through the same `retargetPhrase` primitive as an UI edit and its translations are overwritten; a line without a usable `id` is added through find-or-create; links not mentioned in the file are removed; positions are rewritten to the file's order. The fill-only `FillTranslations(Tx)` added in 3b is replaced by this and removed if nothing else uses it. A new store method, `SyncItemsTx`, owns this logic so it is testable against the DB without HTTP.

Ordered plan, each step shippable and tested on its own:
1. Vocabulary schema and merge migration (dry-run count first, then forced merge).
2. Vocabulary store and read paths (`Items`, `Translations`, list and detail views).
3. Vocabulary write paths (add, update with merge, delete with orphan cleanup, generate, import, job write-backs).
3d. Import/export sync for vocabulary: item `id` in the export, `SyncItemsTx`, import handler wired to it, tests, live check (revision after 3b).
4. Models: repeat steps 1-3d for `models_*`.

Risks: the merge is destructive (accepted); a global edit changes other users' lists; step 1 changes the schema before the code reads it, so steps 1 and 2 must deploy together.

## Affected Areas

- `phraseforge/internal/db/schema.sql`, `phraseforge/internal/db/migrate.go`
- `phraseforge/internal/vocabulary/vocabulary.go`, `phraseforge/internal/models/models.go`
- `phraseforge/internal/server/vocabulary.go`, `models.go`, `generate.go`, `export_import_vocabulary.go` (export `id`, import sync), `export_import_models.go`
- `phraseforge/internal/generate/generate.go`
- `phraseforge/internal/ai/itemjob.go`, `ai.go`; `phraseforge/main.go` (write-back adapter)
- Related tests, `phraseforge/README.md`, `phraseforge/CHANGELOG.md`

## Out of Scope

- Forking a phrase per list on edit.
- Changing the export/import file format beyond adding the per-item `id`.
- Reordering or position-identity changes beyond what the new link table needs.
- Phrase sharing across languages or scripts.
- Per-user or per-list translation overrides.

## Implementation Notes

**Step 1 — vocabulary schema and merge migration (done, schema only; not yet applied to the dev DB).**
- `phraseforge/internal/db/schema.sql`: added `phrases` and `phrase_translation`; `vocabulary_items` is now `(list_id, position, phrase_id)`; removed the old `vocabulary_item_translation` table and its deferred-FK workaround.
- A guarded `DO` block upgrades legacy databases (runs only while `vocabulary_items.phrase` exists): inserts distinct phrases, sets `phrase_id`, copies translations (first non-empty per phrase and locale, lowest list_id then position), drops the old table and columns.
- Deviation from the Approach's wording, within its intent: orphan phrases are deleted by a statement-level `AFTER DELETE` trigger on `vocabulary_items` (`delete_orphan_phrases`) instead of Go code, so list-deletion cascades are covered too. `grammar`/`transcription` are stored as `NOT NULL DEFAULT ''`.
- Dry-run on the dev DB before the change: vocabulary 224 items, 2 duplicate groups (2 rows removed, 222 distinct phrases); models 48 items, 1 duplicate group (3 rows removed; models is step 4). Backup taken with pg_dump before testing.
- The migration is not applied for real yet: the running app still reads the old columns, so steps 1 and 2 deploy together.

**Step 2 — vocabulary read paths (done; not yet applied to the dev DB).**
- `phraseforge/internal/vocabulary/vocabulary.go`: `Items` joins `phrases`; `Translations` joins `phrase_translation` through `vocabulary_items`. Public signatures are unchanged (still keyed by position), so no server read handler needed edits.
- Verified on the dev DB inside a rolled-back transaction: the new `Items` output equals the old for all 224 items; translations equal except 2 rows (list 13, position 14, `en`/`pl`) where a merged duplicate in another list had a different translation and the lowest-list copy won — the accepted merge loss.
- `go build ./...` and `go test ./...` pass. The write paths still use the old columns until step 3, so the migration stays unapplied.

**Step 3a — vocabulary store write methods (done; HTTP handlers, import/generate callers and job write-backs are 3b-3c).**
- `phraseforge/internal/vocabulary/vocabulary.go`: `AddItem`/`AddItemTx` find-or-create the phrase (`INSERT … ON CONFLICT … DO UPDATE … RETURNING id`, race-free) and link it; `UpdateItem`, `SetItemTranscriptionIfBlank` and `SetItemGrammarIfBlank` all go through one primitive, `retargetPhrase`, which changes the shared phrase for every list and merges into an existing identical phrase (translations carried over, the survivor's own non-blank translation wins); `SetItemTranslationIfAbsent` and `SetTranslations(Tx)` write `phrase_translation`; `DeleteItemTx` only renumbers positions (the trigger removes orphans). Public signatures are unchanged.
- The stale-target phrase guard is kept: the phrase is resolved from `(listID, position)` inside the transaction (`lockItemPhrase`).
- New `phraseforge/internal/vocabulary/vocabulary_test.go`: 9 integration tests (shared storage, identity fields, orphan cleanup on item and list delete, renumbering, global edit, merge, the two IfBlank write-backs, translation-if-absent). They need `PHRASEFORGE_TEST_DSN` and are skipped without it; each runs in a throwaway schema it drops. Run against the dev Postgres through a temporary port-forward: 9/9 pass, no leftover schemas, dev DB unchanged.

**Step 3b — caller audit and fixes (done; vocabulary only). Note: the fill-only import behaviour described below was superseded by spec version 2 (import overwrites; see 3d).**
- Audited every caller of the store's write methods (HTTP add/update/delete handlers, generate-from-text, import, wholesale replace, job write-backs in `main.go` and `internal/ai`). Signatures were unchanged, so the job write-backs and handlers needed no edits; three real problems were found and fixed:
  - Import wrote translations with `SetTranslations(Tx)`, so an empty value would have deleted a translation shared with other lists and a different value would have overwritten it. Import (`replaceVocabItemsTx`, `addVocabItemsAndBackfill`) now uses new `FillTranslations(Tx)`: fills only a blank translation, ignores empty values.
  - Backfill after import (`server/export_import.go` `enqueueItemBackfill`) and after generate (`generate/generate.go` `enqueueItemBackfill`) decided jobs only from the incoming data, so an already-translated phrase would still have been sent to the LLM. Both now also mark the locales the phrase already has (new `TranslatedLocales`) for vocabulary items.
  - Left as designed: the add form writes a non-empty translation and the edit form writes its translation unconditionally, i.e. an explicit user edit changes the shared translation for every list (the global-edit decision).
- Two new store tests (`FillTranslations`, `TranslatedLocales`). Full `go test -count=1 ./...` with `PHRASEFORGE_TEST_DSN` set against the dev Postgres: all packages pass; no leftover test schemas.
- Known limitation, not changed: identity includes grammar and transcription, so a generated item that lacks them is a different phrase from the same word once it has them. Its translation job may run before the later grammar/transcription write-back merges the two.

**Step 3c — migration applied and deployed to the dev cluster; live end-to-end check passed (vocabulary).**
- `task deploy-phraseforge` (absolute `KUBECONFIG_PATH`): image built, migration Job ran `schema.sql`, new pod rolled out and healthy. DB afterwards: 224 items, 222 phrases, 444 phrase_translation, old columns/table gone, orphan trigger present — exactly the dry-run prediction. Pre-change backup: pg_dump in the session scratchpad (not committed).
- End-to-end through the real API (`/signup` throwaway user + teacher role for `arb`, via the ingress): created a new list and added a phrase that already existed with an `en` translation in another list, sending no translation. Result: `phrases` stayed at 222 (stored once, 2 links); `GET` of the new list showed the translation immediately; `generate-missing-translations` returned `enqueued: 0` and the jobs table was unchanged (61 before and after). The throwaway user was deleted afterwards (cascade removed its list; phrases/translations back to 222/444, lists back to 3).
- Not exercised live: import and generate-from-text paths (covered by store tests and code audit only); a different-locale or untranslated phrase still enqueues jobs as before.

**Step 3d-1 — store: `SyncItemsTx` (done; server wiring is 3d-2).**
- `phraseforge/internal/vocabulary/vocabulary.go`: new `SyncItemsTx(ctx, tx, listID, []SyncItem, allowEdit)` makes a list's items exactly the given sequence in one transaction — a line whose `PhraseID` the list links edits that shared phrase for every list (`retargetPhrase`, which now returns the surviving id after a merge) and overwrites the given locales' translations (a present locale with blank fields clears it, an absent one is untouched); any other line is find-or-create; unmentioned links are removed; positions follow the order. Old links are parked at `position + 2^30`, the new ones inserted, then the parked ones deleted, so `delete_orphan_phrases` can't remove a phrase that is about to be linked again. `findOrCreatePhrase` extracted from `AddItemTx`; `Item` gains `PhraseID` (what the export will emit as `id`).
- Decisions beyond the spec text: an id the list doesn't link, or one used by an earlier line of the same sync, makes the line new (never edits another list's phrase, never lets one of two differing lines silently win).
- 9 new DB tests (edit by id across lists, reorder keeps phrase ids and translations, new line links a translated phrase, unmentioned links removed but shared phrases kept, foreign id ignored, edits-not-allowed, blank vs absent locale, same id twice, collision merge). With `PHRASEFORGE_TEST_DSN` against the dev Postgres: 20/20 vocabulary tests pass. Not deployed yet.

**Step 3d-2 — server wiring, deployed, live round trip passed (vocabulary).**
- `phraseforge/internal/server/export_import_vocabulary.go`: export items carry `id` (the phrase id); import items accept `id`; `replaceVocabItemsTx` replaced by `syncVocabItemsTx` (builds `SyncItem`s and calls `SyncItemsTx` in one transaction; `allowEdit` is false when the import also changes the list's language or script); the create-from-file path uses the same function (and is now transactional); backfill specs use the file position. The fill-only `FillTranslations(Tx)` (3b) and its test were removed. Doc comments updated.
- Tests: 2 pure round-trip tests (export YAML -> import keeps item ids; a file without ids decodes to 0). Full `go test -count=1 ./...` with `PHRASEFORGE_TEST_DSN`: all packages pass.
- Deployed with `task deploy-phraseforge`. Live round trip through the real API as a throwaway user, using only its own two lists: created two lists from files sharing one phrase (`phrases` +2, not +3); exported (items have ids); edited phrase, grammar, transcription and both translations of the shared phrase and swapped the item order; re-imported. Result: the other list showed every fixed field with the same phrase id (edited in place), the order followed the file, no new phrases, no jobs enqueued, and re-importing the unchanged export reported `unchanged: 1`. User deleted afterwards: phrases/translations/lists back to 222/444/3.
- Models import/export is unchanged until step 4 (still wholesale replace, no item ids).

**Step 4a — models schema and merge migration (done, schema only; not yet applied to the dev DB).**
- `phraseforge/internal/db/schema.sql`: `models_phrases(id, language, script, phrase, transcription)` unique on those four, `models_phrase_translation(phrase_id, locale, translation)`, `models_items(list_id, position, phrase_id)`, a guarded `DO` block upgrading legacy databases (same method as vocabulary), and an orphan trigger (`delete_orphan_models_phrases`). The old `models_item_translation` table and its deferred-FK workaround are gone.
- Verified on the dev DB inside a rolled-back transaction, schema applied twice: 48 items, 45 phrases (the 3 predicted merges), 90 translations — equal to what the old shape implies; old and new `Items` and `Translations` results identical in both directions (this merge loses no translation); no null `phrase_id`; old table/columns gone; trigger left no orphans after deleting a list; DB unchanged afterwards. The fresh-install path is covered by the vocabulary DB tests applying the same `schema.sql` into an empty schema (pass).
- Not applied for real: the deployed app's models code still reads the old columns, so 4a deploys together with 4b.

**Step 4b — models store (done; server wiring is 4c; not yet deployed or applied).**
- `phraseforge/internal/models/models.go`: ported from vocabulary, minus grammar and notes. `Items` and `Translations` read through `models_phrases` / `models_phrase_translation`; `AddItem(Tx)` find-or-create; `UpdateItem`, `SetItemTranscriptionIfBlank` go through `retargetPhrase` (global edit, merge on collision); `SetItemTranslationIfAbsent` and `SetTranslation(Tx)` write `models_phrase_translation`; `DeleteItem(Tx)` only renumbers; new `TranslatedLocales` and `SyncItemsTx` (`SyncItem.Translations` is a locale -> translation map, since models has no notes). `Item` gains `PhraseID`. Existing method signatures are unchanged except additions, so no caller needed edits yet.
- New `phraseforge/internal/models/models_test.go`: 8 DB tests mirroring vocabulary's (shared storage, identity fields, orphan cleanup on last link and list delete with renumbering, global edit and merge, the two if-blank write-backs, sync edit by id, reorder/remove/new lines, guards, blank vs absent locale). One test first asserted a per-item edit and failed; the test was wrong (a global edit moves every list that shares the phrase), not the code, and was corrected.
- With `PHRASEFORGE_TEST_DSN` against the dev Postgres: `go test -count=1 ./...` all packages pass; no leftover test schemas.

**Step 4c — models server wiring, deployed, migration applied, live round trip passed.**
- `phraseforge/internal/server/export_import_models.go`: export items carry `id`, import items accept `id`, `replaceModelsItemsTx` replaced by `syncModelsItemsTx` (`allowEdit` false when language or script changes), the create-from-file path uses it too. `server/export_import.go` and `generate/generate.go` backfills now skip locales already translated for models phrases as well (`models.Store.TranslatedLocales`). 2 pure round-trip tests added; `go test -count=1 ./...` with `PHRASEFORGE_TEST_DSN`: all pass.
- A fresh `pg_dump` was taken before applying (session scratchpad, not committed), then `task deploy-phraseforge` applied the models migration: 48 items, 45 phrases, 90 translations (the predicted 3 merges, no translation lost), old table and columns gone, trigger present; vocabulary untouched (222 phrases).
- Live round trip through the real API (throwaway user, its own two lists): shared phrase stored once; export has item ids; re-importing an edited file fixed phrase, transcription and both translations of the shared phrase in the other list with the same phrase id (en via the API, pl checked in the DB), reordered the list, created no phrases and enqueued no jobs; an unchanged re-import reported `unchanged: 1`; a file without item ids imported as new lines. One check failed first because it looked for the pl value in an API response that only carries the viewer's locale; the check was wrong, not the app. Cleanup restored 45/90 models phrases/translations, 2 models lists, 222 vocabulary phrases.

**Independent review and fixes (done, deployed, verified live).** The `reviewer` agent was run on the finished change against two criteria from the user: easy-to-understand logic and no duplicated records. It found three defects, which were each reproduced with a throwaway DB test before any fix (all failed as claimed), plus smaller points. While fixing, I found one more the review had missed: `apiAddModelsItem` (`server/models.go`) called `SetTranslation` unconditionally, so adding an already-translated models phrase with a blank translation field would have cleared the translation for every list (vocabulary's handler already guarded this; my 3b audit had only covered vocabulary). Fixes:
1. Regenerating a list from its text deleted every item and re-added it, so the orphan trigger deleted translations and the phrases were re-translated. `generate` now syncs through `SyncItemsTx` (`syncVocabItems` / `syncModelsItems`, one transaction, also for a brand-new list); the two delete-then-add functions and about 70 lines are gone. Regression tests in both store packages.
2. `SyncItemsTx` let a later line rewrite or merge-delete a phrase an earlier line had resolved to. A phrase is now "claimed" once any line resolves to it, by either route, and only unclaimed linked phrases can be edited. Tests: later edit of an earlier line's phrase, a swap of two lines' texts, a merge that would delete an earlier line's phrase. The function is split into `linkedPhraseIDs`, `resolveSyncLine`, `writeSyncTranslations` and `relinkItems`, with the park-insert-delete ordering and why the bulk move is safe explained.
3. Changing a list's language or script left its items linked to old-language phrases (a duplicate source, and a permission bypass: `CanEdit` was checked on the new language while editing old-language phrases). `UpdateMeta` (both stores) now re-links the items to the same text in the new language in one transaction (find or create, translations copied, orphaned old phrases deleted explicitly; phrases other lists still use are untouched). Test in both packages.
4. Normalisation (user decision): new `phraseforge/internal/textnorm` (`Phrase` = trim + Unicode NFC, `golang.org/x/text` was already a dependency) applied in `findOrCreatePhrase` and `retargetPhrase` in both stores, so add, edit, import, generate and sync all use it; a phrase that is blank after trimming is rejected (`ErrBlankPhrase`, HTTP 400 on add and edit). Backfill jobs and import specs now take the phrase text as stored, because a job's stale-target guard compares against the stored text. Existing rows: a dry-run showed 42 vocabulary and 9 models phrases differing from their normal form, all vocalised Arabic whose combining marks are not in canonical order (NFC-only; no whitespace differences), with no collisions. `schema.sql` gained a SQL `normalize_phrase()` and an idempotent `UPDATE` per table that normalises them and never fails on the UNIQUE key (a row whose normal form is taken is left as it is).
5. Readability: stale comments removed or rewritten (a doc comment for a method that no longer exists, "wholesale replace" wording in `Begin` and others); the phrase guard repeated in three write-backs (two in models) is now `lockItemPhraseIfStill`; the schema comment now states that the orphan trigger fires on deleted links only, so code that re-points a link deletes the phrase it left behind.
Left as is, on the reviewer's advice and mine: the near-copy between `vocabulary.go` and `models.go` (a generic table-parametrised store would touch 600+ lines and make the SQL harder to read; the "mirrors X" comments stay); two concurrent renames to the same values give one a unique-violation error instead of a merge (no duplicate is created).

B3 (re-validation, below), B4 (documentation review) and B5 (documentation update, delivery): B3 re-run; B4 and B5 not started.

## Validation

Run from `phraseforge/` on 2026-10-09 (the repo's CI workflows only run `go test ./...`; there is no linter config):
- `go build ./...`: ok. `go vet ./...`: ok. `gofmt -l .`: nothing listed.
- `go test -count=1 ./...` without a database (as CI runs it): all 10 packages pass; the 28 DB tests in `internal/vocabulary` and `internal/models` are skipped.
- Same with `PHRASEFORGE_TEST_DSN` set to the dev Postgres through a temporary `kubectl port-forward` (stopped afterwards): all packages pass; the 28 DB tests run and pass (20 vocabulary incl. sync, 8 models); no leftover `pf_test_*` schemas.
- `go test -count=1 -race ./internal/vocabulary ./internal/models` with the DB: ok.
- Baseline before the work (B2 step 1): build and tests green, so no regressions against it; no pre-existing failures.
- Live checks on the dev cluster after `task deploy-phraseforge` (details in Implementation Notes 3c, 3d-2, 4c): migrations applied with the predicted counts; throwaway-user round trips for vocabulary and models passed (shared storage, no translation job for an already-translated phrase, export `id`, edit by id across lists, reorder, unchanged re-import is a no-op).
- Not run: the pf-* Web Component tests (`task test-phraseforge-frontend`) — no frontend file was changed. Not covered by automated tests: the HTTP handlers' use of the store (covered by the live round trips only) and concurrent edits.
- Known limits found: (1) a phrase longer than about 2.7 KB of incompressible text now fails on the unique index (a 6.4 KB high-entropy value gave `index row size 6432 exceeds btree version 4 maximum 2704`; compressible text of 2000-3000 bytes inserted fine); realistic vocabulary phrases are far shorter, and the failure is a per-add or per-list import error, not a crash. A fix, if wanted, is a unique index on an MD5 of the phrase. (2) Two concurrent edits or imports touching overlapping shared phrases in opposite order can deadlock; Postgres aborts one of them with an error and a retry succeeds. (3) The generated-item identity gap described under Implementation Notes 3b.
- `reviewer` agent: run after the first B3 pass (see Implementation Notes, "Independent review and fixes"); its findings were reproduced, fixed and re-validated below.

Re-validation after the review fixes (2026-10-09):
- `go vet ./...` and `gofmt -l .` clean; `go test -count=1 ./...` with `PHRASEFORGE_TEST_DSN`: all 11 packages pass (new `internal/textnorm` included); the DB tests run and pass (41 across `internal/vocabulary` and `internal/models`); `-race` on the two DB packages clean; no leftover `pf_test_*` schemas. Without the variable they skip, as in CI.
- A fresh `pg_dump` was taken, then `task deploy-phraseforge` applied the normalisation migration: counts unchanged (222 vocabulary phrases, 444 translations, 224 items; 45 models phrases, 90 translations, 48 items) and zero phrases left that differ from `normalize_phrase()`.
- Live, as a throwaway user through the real API, everything cleaned up afterwards (counts back to baseline each time): the vocabulary and models round trips from 3d-2 and 4c re-run, all pass; plus new checks, all pass: (a) the same text added by hand with stray spaces and in a different mark order in two lists is one phrase; (e) a blank phrase is HTTP 400 on vocabulary and on models; (b) adding an already-translated models phrase with a blank translation leaves the translation in place and visible in the second list; (c) changing a list's language through `PUT /vocabulary/{id}` leaves no item linked to an old-language phrase, the other list keeps its phrase, the translation survives the move, and adding the same text again creates no duplicate.
- Not exercised live: regenerating a list from its text (needs an LLM job on the dev Ollama; covered by the store-level regression tests that the generate code now reduces to).
- Observation, explained: the `jobs` table held 61 `failed` rows in the backups taken before the models migration and 0 in the one taken before the normalisation deploy. The user confirmed they cleared the jobs themselves (the admin "clear jobs" action); this work did not touch them.

## Documentation Review

Checked against the final change (2026-10-09): `phraseforge/README.md`, `phraseforge/CHANGELOG.md`, the root `README.md` and `CHANGELOG.md`, `specs/mission.md`, `specs/tech-stack.md`, `specs/artifacts/phraseforge/roadmap.md`, `specs/memory.md`.
- `phraseforge/README.md` (the Vocabulary/Models bullet): described the old per-list item shape only; it did not say phrases are shared, that edits propagate, or what the export `id` is for. It had no mention of how to run the DB tests.
- `phraseforge/CHANGELOG.md` `[Unreleased]`: no entry for any of this. User-facing entries needed (Keep a Changelog, one line each): Changed — shared phrases and the merge of existing duplicates; export `id` and import by id with overwrite; regenerating keeps translations; a language or script change moves the items. Fixed — phrase text normalisation and the 400 on a blank phrase; the models add form no longer clearing a shared translation. Internal-only changes (store refactors, tests) need none.
- Root `README.md`, root `CHANGELOG.md` (the `environment` artifact), `mission.md` and `tech-stack.md`: no statement affected; no constitution change is needed.
- `specs/artifacts/phraseforge/roadmap.md`: carries this feature's `## Now` line, to be removed now that it is in the changelog.
- `specs/memory.md`: one entry (2026-10-08, "phraseforge has no DB-backed Go tests") is now false; a superseding entry was appended instead, following this file's convention. Durable facts from this work worth recording: the DB test variable, the orphan trigger's DELETE-only rule, the normalisation helper, and the index-size limit on phrases.

## Documentation Updates

- `phraseforge/README.md`: the Vocabulary/Models bullet now says phrases are stored once and shared, edits and translations propagate, text is normalised, and how the export `id` and import by id work; a new "Tests" section explains `PHRASEFORGE_TEST_DSN`.
- `phraseforge/CHANGELOG.md` `[Unreleased]`: four entries under Changed and two under Fixed (as listed in Documentation Review).
- `specs/artifacts/phraseforge/roadmap.md`: this feature's `## Now` line removed (version 28).
- `specs/memory.md`: four entries appended (1 build, 2 gotcha, 1 convention), quoted in the delivery summary. The old "no DB-backed Go tests" entry is superseded by the first of them and was left in place; a later maintenance pass can purge it.
- No constitution file changed.
