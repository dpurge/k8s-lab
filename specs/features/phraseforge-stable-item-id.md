---
title: Stable item ids; item jobs target the id
kind: bugfix
status: done
version: 1
updated: 2026-10-12
branch: main
---

## Problem / Motivation

On prod (jdpct101, namespace frontend) 118 `generate_vocabulary_item` jobs failed with "vocabulary item not found". All belong to list 32, were queued within 1.6 s at 2026-10-11 03:51:44 UTC right after the list was created, and ran about 27 h later (failed 06:21-07:22 on 10-12; one job runs at a time, 2254 were pending). Queried read-only: none of the 118 still sits at its queued position; 62 point at phrases no longer in the list, 56 moved 1-5 positions (e.g. phrase at position 29 now at 30). The user regenerated the list in between; regeneration rewrote items with different model output.

Mechanism: item jobs address an item by (list_id, position) plus the exact phrase (`ItemJobPayload`, `lockItemPhraseIfStill` in internal/vocabulary/vocabulary.go), and `relinkItems` deletes and reinserts every item on sync, so positions and phrases are not stable identities. The stale-target guard is correct but the addressing is fragile, and a stale job ends as `failed`.

NFC normalization was checked and ruled out (stored bytes are NFC). Same design in models items. The user wants a robust code fix and does not need the prod data repaired. This is also the prerequisite for later work (letting the item job revise a phrase needs an identity that survives the revision).

## Acceptance Criteria

- vocabulary_items and models_items each get a column `id bigint GENERATED ALWAYS AS IDENTITY` with a unique index; existing rows get ids from the migration; (list_id, position) stays the primary key; the DDL in internal/db/schema.sql is idempotent like the existing statements.
- The stores' item listing returns each item's id (field name `ID`).
- Item job payload is `{resource_type, item_id, locale}` (JSON field `item_id`); it no longer carries list_id, position, phrase or language. The handler reads the item's current phrase and its list's language from the database when the job starts.
- A job whose item no longer exists ends with status `cancelled` and a reason ("item no longer exists"), not `failed`. A job whose item's phrase text changed while the job was running (checked in the write-back transaction against the phrase read at job start) also ends `cancelled` ("item changed while the job was running") and writes nothing.
- jobs.Service supports a handler-returned sentinel error (e.g. `jobs.ErrSkipped`, wrapped with a reason) that completes the job as `cancelled` with that reason as its error text.
- Write-back (translation+notes, transcription, grammar; blank-only rules unchanged) is done by item id in one transaction with the item row locked FOR UPDATE.
- Sync (list regeneration, import) keeps the id of an item whose phrase is still linked in the list; removed phrases lose their item; new phrases get new ids; if a phrase appears more than once in a list, old and new occurrences pair in position order. Positions of items are updated in place; DeleteItem/AddItem leave other items' ids unchanged.
- All enqueue sites build jobs from item ids (generate backfill, import, "Generate missing translations", server generate endpoint), for vocabulary and models.
- Jobs queued before the change (old payload) fail with a clear "item_id required" error; this is accepted by the user and no legacy code path is kept.
- Tests: id stability across SyncItemsTx/relink (kept, removed, new, duplicate phrase), id unchanged after DeleteItem shift, handler on deleted item -> cancelled, phrase changed mid-job -> cancelled, ErrSkipped -> cancelled in jobs. Verified in the dev k3d cluster (KUBECONFIG=/Users/jprusaczyk/Documents/Sources/DPurge/k8s-lab/.k3d/kubeconfig, absolute path): migration applied on existing data; regenerate a list while its item jobs are pending and none fail.

## Approach

Ordered plan, one verifiable step each:

(1) schema: `ALTER TABLE ... ADD COLUMN IF NOT EXISTS id bigint GENERATED ALWAYS AS IDENTITY` for both tables plus `CREATE UNIQUE INDEX IF NOT EXISTS`; verify backfill on the dev DB (unverified assumption: Postgres fills existing rows from the identity sequence).

(2) stores: listing returns id; `relinkItems` (vocabulary and models) becomes a set-based in-place update: move all rows out of the way as today, pair old and new rows per phrase_id by occurrence order, update the paired rows' position, insert unpaired new, delete unpaired old, so kept items keep their id.

(3) jobs: add the ErrSkipped sentinel; `complete` maps it to StatusCancelled with the reason as error text (about 6 lines).

(4) item job: new payload; handler loads phrase/language by item id; write-back stores take an item id plus the phrase read at start and lock by id; mismatch or missing -> ErrSkipped.

(5) enqueue sites switch to item id (ItemJobsFor/NewItemJob take the id).

(6) tests, then dev-cluster verification.

Key decisions: surrogate `id` column rather than keying on (list_id, phrase_id) (user-confirmed naming `id`/`item_id`); no legacy payload path (user: simpler code, failing in-flight jobs is acceptable); positions stay for ordering and for the HTTP APIs that already use them.

Alternatives rejected: keep position+phrase and add retry-by-phrase lookup (still breaks when a phrase is revised, which later slices need).

Risks: the migration runs on prod's 4190 vocabulary items; confirm how schema.sql is applied in prod before release. The ~2254 pending prod jobs with the old payload will fail on deploy (accepted).

## Affected Areas

phraseforge/internal/db/schema.sql; phraseforge/internal/vocabulary/vocabulary.go; phraseforge/internal/models/models.go; phraseforge/internal/jobs/jobs.go; phraseforge/internal/ai/itemjob.go, itemcalls.go, ai.go (write-back interfaces); phraseforge/main.go (write-back adapters); phraseforge/internal/generate/generate.go; phraseforge/internal/server/vocabulary.go, generate.go, export_import.go, export_import_vocabulary.go, export_import_models.go; tests next to each; phraseforge/CHANGELOG.md.

## Out of Scope

examples store, two-step generation, review jobs and letting the item job revise the phrase (roadmap slices phraseforge-item-examples, phraseforge-two-step-vocabulary-generation, phraseforge-vocabulary-review-job, phraseforge-text-dialog-review-job); changing HTTP APIs that address items by position; repairing prod data (the 118 failed jobs, list 32); prompt changes; any legacy handling of old-payload jobs.

## Implementation Notes
1. Schema: `id bigint GENERATED ALWAYS AS IDENTITY` plus a unique index on vocabulary_items and models_items in phraseforge/internal/db/schema.sql. Verified in a throwaway schema: old schema with 3 rows, new schema applied twice, 3 non-null distinct ids (the backfill assumption from the Approach held).
2. Stores: `Item.ID` and the listing return the id; `relinkItems` (vocabulary and models) now moves old links aside, lets each new position take over the moved link to the same phrase (nth occurrence pairs with nth old link), inserts the rest and deletes the leftovers, so a kept item keeps its id. New files internal/vocabulary/itemjob.go and internal/models/itemjob.go add `ItemSource`, `FillTranslation`, `FillTranscription` and (vocabulary only) `FillGrammar`, which lock the item row by id in one transaction and return `ErrItemNotFound` or the new `ErrItemChanged`. The old position-based methods and lock helpers (`SetItemTranscriptionIfBlank`, `SetItemGrammarIfBlank`, `SetItemTranslationIfAbsent`, `lockItemPhrase`, `lockItemPhraseIfStill`) were deleted.
3. jobs: `jobs.ErrSkipped`; `complete` records a job whose error wraps it as `cancelled` with the error text. Added at the user's request after the dev check: `runOne` logs `outcome=skipped` for such jobs instead of `outcome=error`.
4. Item job: payload is `{resource_type, item_id, locale}`; `HandleItemTranslation` reads the phrase and language through `ai.ItemStore.ItemSource`, writes back through `FillTranslation/FillTranscription/FillGrammar`; the main.go adapter `itemStore` maps the store errors to `ai.ErrItemGone` and `ai.ErrItemChanged`, which wrap `jobs.ErrSkipped`. `ItemJobPayload` is no longer carrying list_id, position, phrase or language; a small `loadedItem` (payload + ItemSource) keeps the helper signatures.
5. Call sites (done inside step 4 because the signature change forced them): generate backfill, import backfill (vocabulary and models), "Generate missing translations", per-item Generate buttons now pass item ids; `ItemJobsFor` and `NewItemJob` lost their listID/position/phrase arguments.
6. Deviation from the Approach, approved by the user before it was done: the dead legacy item branches of `HandleGenerate` (`ItemPosition` in the payloads of ai and server, the `vocabItems`/`modelsItems`/`itemTranslations`/`itemReplies` fields, the interfaces `ItemTranscriptionWriteback`, `ItemTranslationWriteback`, `ItemReplyWriteback`) were removed; `ai.New` takes one `ItemStore`. A pre-existing item-level generate_translation/generate_transcription row now fails with an error (an explicit guard prevents `translations.SetIfAbsent` from storing it under an item resource type).
7. `AddItem` still returns only the position; nothing needed its id. HTTP APIs still address items by position.
Tests added: id stability across SyncItemsTx (kept, removed, new, repeated phrase) and after DeleteItem, in both stores; ItemSource/Fill* tests incl. the shared-phrase cases ported from the old tests and a sync-reorder case; jobs skipped-vs-failed test; ai tests for skip mapping, old-style payload ("item_id is required") and refusal of item-typed legacy rows.

## Validation
- Baseline before changes: go build ./... and go test ./... green; 32 DB-backed tests skipped without a test database, so the baseline was re-taken with PHRASEFORGE_TEST_DSN and PHRASEFORGE_TEST_DB pointing at the dev Postgres (port-forward): vocabulary, models, jobs, ai, generate, server all passed.
- After: gofmt -l . clean, go build ./... and go vet ./... clean, go test -count=1 ./... green with both variables set: 300 passed, 1 skipped (TestAdminConfigExportImportRoundTripDB, which wipes the database and needs PHRASEFORGE_TEST_DB_REPLACE_ALL=1; deliberately not run). No regressions against the baseline. The new tests were not run against the old implementation (they fail there by construction: the old relinkItems inserted fresh rows).
- Dev k3d cluster, `task deploy-phraseforge` twice (second time for the log fix): migrate job applied schema.sql, rollout succeeded. All 224 vocabulary and 48 models items have non-null distinct ids. A job for a nonexistent item_id ended `cancelled`, "skipped: item no longer exists" (log `outcome=skipped` after the fix); an old-style payload ended `failed`, "item job: item_id is required"; one real item job (blank Polish translation of item id 10) ended `done` in 41 s (1 model call: 37.8 s, 634 prompt tokens, 46 reply tokens), filled the translation, left the existing grammar and transcription alone, and the item kept id 10 and position 46. Test jobs were deleted afterwards.
- Not covered: regenerating a list on the cluster while its item jobs wait (regeneration calls the model, which the user's laptop cannot take); it is covered by the store tests only. The existing dev data had no blank items, so the dev blank-fill check used one translation blanked by hand (model output now replaces the old value of that item's Polish translation). Prod migration not run; how schema.sql is applied in prod was not confirmed (listed as a Risk in the Approach). validate-specs could not be run (script missing).

## Documentation Review
- phraseforge/CHANGELOG.md: needs entries (user-facing: stale item jobs no longer fail, cancelled-with-reason, upgrade note). Root CHANGELOG.md and the other artifacts' changelogs: none needed (change is under phraseforge/ only; nothing under shared/).
- phraseforge/README.md and README.md: no mention of item jobs, payloads or positions; no drift.
- specs/tech-stack.md convention on job-handler writebacks (TextWriteback, TranslationWriteback): still accurate, no drift; specs/mission.md: no drift. No constitution change.
- Older feature specs under specs/features/ that name the removed methods (phraseforge-structured-item-translation, phraseforge-export-import, phraseforge-shared-phrases, background-generate-title-transcription-translation) are historical records and were left as they are.
Changelog result: {phraseforge/CHANGELOG.md, Fixed, stale item jobs}, {phraseforge/CHANGELOG.md, Changed, cancelled-with-reason}, {phraseforge/CHANGELOG.md, Changed, upgrade note}.

## Documentation Updates
- phraseforge/CHANGELOG.md: three entries added under `## [Unreleased]` (one Fixed, two Changed).
- specs/artifacts/phraseforge/roadmap.md: the `## Now` line for this feature removed (it is in the changelog now).
- specs/memory.md: three entries appended (see the report).
- No constitution change.
