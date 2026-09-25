---
title: Add Export/Import (YAML) for phraseforge content
kind: feature
status: done
version: 1
updated: 2026-09-25
branch: main
---

## Problem / Motivation

Add Export/Import (YAML) for all four phraseforge content types: Texts, Dialogs, Vocabulary, Models — mirroring knowledge's own export/import pattern (per-item upsert with error collection, not phraseforge's existing admin-config delete-and-replace-all pattern, which is unsuitable here since losing all content on one bad row would be destructive). Only Body/Language/Script (Texts/Dialogs) or Phrase (Vocabulary/Models items) is required on import; everything else is optional and missing generated fields (Title, Transcription, Translation) are backfilled via background jobs, extending the exact machinery `phraseforge-ingest-texts-dialogs` already built (the `llm_generate` job kind's writeback mechanism) rather than duplicating it.

Three design decisions made with the user during B1:
1. **Import does NOT re-run ingest's cleanup pipeline** (`process_text`/`process_dialog`). Import's use case is round-tripping already-exported or hand-authored YAML — the body is presumably already reasonable content, not raw scraped HTML. Only genuinely-missing Title/Transcription/Translation fields trigger background generation; the body is stored as given.
2. **Vocabulary/Models DO get per-item background backfill** for missing Transcription/Translation — this requires extending the writeback mechanism to item-level granularity (a list id plus an item position, not just a single resource id), since vocabulary/models items are addressed by `(listID, position)`, not their own independent id. This is real new machinery, not a reuse of the existing text/dialog-level writeback as-is.
3. **Vocabulary/Models list items are wholesale-replaced on re-import**, not diffed/merged: since an item's only identity is its position within the list (no independent stable id), re-importing a list deletes its existing items and re-adds every YAML item fresh, including re-setting translations. This avoids ambiguous position-based diffing when items are reordered/inserted/removed between export and re-import.

Texts and Dialogs are structurally similar (flat documents with a body); Vocabulary and Models are structurally similar to each other but different from Texts/Dialogs (a List plus an ordered Items array, each item optionally translated per site locale). Per this app's established "deliberate duplication, not a shared abstraction" convention (already used for frontend components across apps), this feature implements four parallel, independent export/import flows rather than one generic engine — texts and dialogs share the *shape* of their approach, as do vocabulary and models, but the actual Go code is written per-type, matching how CRUD handlers for these four types are already separate files today.

Export always respects the existing `?language=` list-filtering convention (exporting whatever the current filtered view would show, or everything if unfiltered) — it is not a separate all-languages dump. Authorization is the same `CanEdit(language)` check every other create/edit/ingest path already uses — not admin-gated, since content itself is per-language teacher-editable, unlike the admin-only config export/import already in `admin.go` (IME configs, LLM prompts — genuinely admin-only concepts, left untouched by this feature).

Given real potential for a large volume of background jobs from one import (e.g., a vocabulary list of 20 items each missing everything could enqueue up to 60 background jobs — one transcription plus two translations per item), this feature does not attempt to bound or throttle that at the API layer beyond what the existing single-worker queue already provides; it is the user's own choice how large an import to submit.

## Acceptance Criteria

**Shared conventions (all four types):**
- Export is YAML (not JSON, matching knowledge's convention and the roadmap's explicit wording), served as a file download (`Content-Disposition: attachment`).
- Import accepts a YAML (or JSON, since YAML is a superset) request body, capped at a reasonable size (e.g. 8 MiB, matching knowledge's own cap).
- Import uses per-item upsert with error collection (not fail-fast, not delete-and-replace-all): each item in the request is processed independently; one bad item does not abort the rest. Response shape: `{imported, deleted, unchanged, errors: [{index, id, message}]}` (matching knowledge's `ImportResult` shape).
- An item with `id` present: if it's found AND the requesting user `CanEdit` that row's language, update it; if found but the user cannot edit its language, that item is a per-item error (never silently skipped or silently applied to a row the user shouldn't touch); if not found at all, that item is a per-item error (never silently created under a stale id).
- An item with no `id`: always creates a new row (after `CanEdit(language)` on the item's own stated language).
- A row/list with `delete: true` requires `id`; deleting an id that doesn't exist is a no-op (idempotent), matching knowledge's convention exactly.
- "Unchanged" detection: if every field the import would write already matches the existing row/list exactly (including item arrays and translations, for Vocabulary/Models), skip the write and any background-job enqueueing entirely, counted as `unchanged` rather than `imported` — this matters a lot here since a repeated/idempotent import (e.g. from a script) should not burn LLM calls on rows that haven't actually changed.
- All new/changed HTTP routes live in the same `requireAuth` group as existing create/edit endpoints — not admin-gated.

**Texts/Dialogs export/import:**
- `GET /api/v1/texts/export` / `GET /api/v1/dialogs/export` (respects `?language=` like the existing list endpoints): YAML `{items: [...]}`, each item: `id`, `title`, `body`, `transcription`, `language`, `script`, `ingest_source`, `tags`, `translations` (a map of site locale → translation text, both `en` and `pl` — a full round-trippable export, not just the current viewer's own locale), `created_at`, `updated_at`.
- `POST /api/v1/texts/import` / `POST /api/v1/dialogs/import`: only `body`/`language`/`script` required per item. Missing `title` → enqueue a background `llm_generate`/`title` job with writeback (this requires wiring up `TextWriteback.SetTitle` in `HandleGenerate`'s dispatch, which exists on the interface today but is currently never called — dead code the reviewer flagged during the ingest feature; this feature is what finally uses it). Missing `transcription` (only if the language needs it, per the existing ime-config check) → enqueue a background transcription job (reuses the exact mechanism ingest already built, unchanged). Missing translation for either site locale → enqueue one background translation job per missing locale (same reuse). The body itself is stored exactly as given — no cleanup/process step runs.

**Vocabulary/Models export/import:**
- `GET /api/v1/vocabulary/export` / `GET /api/v1/models/export` (respects `?language=`): YAML `{lists: [...]}`, each list: `id`, `title`, `language`, `script`, `tags`, `items` (ordered array; each item: `phrase`, `grammar` [vocabulary only], `transcription`, `translations` — a map of site locale → `{translation, notes}` for vocabulary, or just `{translation}` for models, since models items have no notes field).
- `POST /api/v1/vocabulary/import` / `POST /api/v1/models/import`: only `phrase` required per item; `title`/`language`/`script` required for a NEW list (an update-by-id can omit them to leave the list's own metadata unchanged — only its items are being replaced). On import, a list's items are wholesale-replaced: existing items for that list id are deleted, then every YAML item is added fresh in order (this is what makes "unchanged" detection meaningful — comparing the whole incoming items array, including translations, against what's already stored, before doing any delete/re-add work). Missing `transcription` on an item (if the language needs it) → enqueue a background transcription job targeting that specific `(listID, position)`. Missing `translation` on an item for either site locale → enqueue one background translation job per missing locale, targeting that `(listID, position, locale)`. `notes` (vocabulary only) is never auto-generated — it's a free-form personal annotation field, left blank if not provided.

**New/changed backend pieces:**
- `phraseforge/internal/ai/ai.go`'s `HandleGenerate` payload gains an `ItemPosition int` field (only meaningful when the resource type indicates a vocabulary/models item, not a text/dialog); its writeback dispatch is extended to cover: `kind=="title"` for text/dialog resource types (newly wired to the existing but previously-unused `TextWriteback.SetTitle`); and `kind` ∈ {`transcription`,`translation`} for `vocabulary_item`/`models_item` resource types, dispatching to new item-level writeback interfaces.
- New narrow setters: `vocabulary.Store.SetItemTranscription(ctx, listID, position, transcription) error`, `models.Store.SetItemTranscription(ctx, listID, position, transcription) error` (single-column update, matching the existing `SetTitle`/`SetTranscription` narrow-setter pattern from the text/dialog case — never touches other item fields).
- New small consumer-side interfaces (matching the established "define a small interface taking only primitives" convention): `ItemTranscriptionWriteback` (satisfied by `vocabulary.Store`/`models.Store`) and `ItemTranslationWriteback` (dispatches internally to `vocabulary.Store.SetTranslations`/`models.Store.SetTranslation` by resource type — vocabulary's existing `SetTranslations` is a batch setter, called here with a single-element slice for one position; models' existing `SetTranslation` is already single-item).

## Approach

1. **`phraseforge/internal/ai/ai.go`**: extend `HandleGenerate`'s payload (`ItemPosition int`), extend the writeback dispatch switch to cover `title`→`TextWriteback.SetTitle` and the two new item-level writeback interfaces for `vocabulary_item`/`models_item` resource types.
2. **`phraseforge/internal/vocabulary/vocabulary.go`, `phraseforge/internal/models/models.go`**: add `SetItemTranscription(ctx, listID, position, transcription) error` narrow setters.
3. **`phraseforge/internal/server`**: new `apiExportTexts`/`apiImportTexts` and the Dialogs equivalent (two files or extending the existing `texts.go`/`dialogs.go` handler files — match whichever existing file-per-resource convention this codebase already uses); new `apiExportVocabulary`/`apiImportVocabulary` and the Models equivalent, in their own handler areas. Each import handler: per-item/per-list upsert-with-error-collection, unchanged detection, `id`-found-but-not-editable and `id`-not-found as per-item errors (never silent), enqueue background jobs for missing generated fields per the Acceptance Criteria above.
4. **Route registration**: 8 new routes (`GET`/`POST` `export`/`import` × 4 types) inside the existing `requireAuth` group.
5. **Frontend**: Export/Import buttons on all four list views (`texts-app.js`, `dialogs-app.js`, `vocabulary-app.js`, `models-app.js`), matching the existing admin-config export/import UI pattern already in `admin-app.js` (file download for export, file-picker upload for import, a result summary shown via `statusBar` after import — imported/deleted/unchanged counts, and any per-item errors).
6. **i18n**: new labels for the Export/Import buttons and the import-result summary, following the established per-section bootstrap i18n convention, en + pl.

## Affected Areas

- `phraseforge/internal/ai/ai.go`
- `phraseforge/internal/vocabulary/vocabulary.go`
- `phraseforge/internal/models/models.go`
- `phraseforge/internal/server/` (texts/dialogs/vocabulary/models handler files, route registration)
- `phraseforge/internal/server/static/js/texts-app.js`, `dialogs-app.js`, `vocabulary-app.js`, `models-app.js`
- `phraseforge/internal/i18n/i18n.go`

## Out of Scope

- Any change to `admin.go`'s existing config export/import (IME configs, LLM prompts) — untouched, genuinely separate and admin-only.
- Cross-instance import portability (importing a YAML file exported from a different phraseforge deployment) — ids are plain auto-increment integers, not portable UUIDs; this feature's primary use case is round-tripping within the same instance (export, hand-edit, re-import) or bulk-authoring fresh content (no `id` field at all). An `id` present in an import that doesn't match any row in the target database, or matches a row the importing user cannot edit, is a per-item error, not silently treated as a fresh create.
- Bounding/throttling the number of background jobs one import can enqueue — the existing single-worker queue already serializes everything; this feature doesn't add a separate limit.
- Auto-generating Vocabulary's `notes` field — free-form personal annotation, never auto-generated.
- A generic/shared export-import engine across the four types — deliberately four parallel implementations, matching this app's established duplication-over-abstraction convention.

## Implementation Notes

All Approach steps implemented across 4 sequential passes, each verified to build/vet/test clean before the next started. Full repo (`shared`, `phraseforge`, `knowledge`) builds clean after all 4.

**Pass 1 (writeback extension core):**
- `phraseforge/internal/ai/ai.go`: `generatePayload` gained `ItemPosition int`; `HandleGenerate`'s writeback dispatch extended to finally wire up `kind=="title"` for text/dialog resource types (this path existed on the `TextWriteback` interface since the ingest feature but was never called — dead code until now), plus two new interfaces (`ItemTranscriptionWriteback`, `ItemTranslationWriteback`) for `vocabulary_item`/`models_item` resource types, addressed by `(listID, position)` since items have no independent id.
- `vocabulary.go`/`models.go`: new `SetItemTranscription` narrow setters.
- `main.go`: a new adapter implementing `ItemTranslationWriteback` by holding both stores and dispatching by resource type — needed because vocabulary's existing `SetTranslations` is a batch setter (called here with a single-element slice) while models' `SetTranslation` is already single-item.
- Caught during implementation: a naive single-item call into vocabulary's batch `SetTranslations` would have silently nulled out an item's existing hand-written `notes` (that setter writes translation+notes together, and a translation-only background writeback would otherwise hand it an empty notes value) — the adapter now reads the existing notes first and carries them through unchanged, so only translation ever changes via this path.

**Pass 2 (Texts/Dialogs export/import):**
- New `phraseforge/internal/server/export_import.go` (shared helpers: `importResult`/`importError` envelope, pure `importUnchanged`/`decideBackfill` comparison/decision functions, `writeYAML`/`readImportBody`), `export_import_texts.go`, `export_import_dialogs.go`.
- `GET/POST /api/v1/texts/export|import`, same for dialogs, in the existing `requireAuth` group. YAML field naming: snake_case, matching knowledge's own export/import convention and phraseforge's existing job-payload wire-format convention (not the camelCase used in phraseforge's own config.yaml, which is a different context).
- Same `CanEdit`-based scoping as the existing list endpoints; an update-by-id that changes a row's language re-checks `CanEdit` on the NEW language too (closing a permission-boundary gap the literal per-item algorithm didn't explicitly call out, but required by the spec's own stated authorization rule).
- "Unchanged" detection compares every field the import would touch (including per-locale translations) before writing anything or enqueueing any backfill job — this matters because a repeated/idempotent import shouldn't burn LLM calls on rows that haven't actually changed.

**Pass 3 (Vocabulary/Models export/import):**
- New `export_import_vocabulary.go`, `export_import_models.go`, reusing Pass 2's shared envelope/helpers plus new list-level and item-level analogues (`listMetaUnchanged`, `decideItemBackfill`, `enqueueItemBackfill`).
- Wholesale item replacement on update-by-id (per the approved design decision): existing items deleted highest-position-first (avoids triggering `DeleteItem`'s own position-shifting logic needlessly), then every YAML item re-added fresh.
- A blank `phrase` anywhere in an imported list's items aborts that whole list as one list-level error, applied uniformly to both the create path and the update-by-id wholesale-replace path — chosen specifically to avoid leaving a list with its old items deleted and no valid new ones written if a bad item were discovered mid-replacement.
- List-level metadata (title/language/script/tags) is individually optional on an update-by-id (a blank/omitted value means "leave unchanged," never "clear the field") — a deliberate difference from Texts/Dialogs' import, where an omitted value does clear the corresponding field, because vocabulary/models lists have no legitimate reason to hold a genuinely blank title/language/script.
- `notes` (vocabulary only) is never auto-generated, per the approved scope decision.

**Pass 4 (frontend + i18n, all 4 types):**
- Export: a plain anchor link to the export endpoint (matching the existing admin-config-export pattern exactly, since the backend's `Content-Disposition: attachment` makes a plain click download without navigating away) — respects the active language filter.
- Import: file picker (`.yaml`/`.yml`) read client-side via `FileReader` (matching the ingest feature's established convention), POSTed as raw text; result summary shown via the status bar, with per-item errors (if any) shown in the same reusable `pf-dialog` component the Jobs page's "View" action already uses.
- Caught during implementation: Polish already translates `texts.ingest` as "Importuj" — since Import and Ingest buttons now sit side by side on the same Texts/Dialogs list view, using the same word for `texts.import` would have made them indistinguishable in Polish. Used "Zaimportuj" (perfective aspect) for Import instead, disambiguating the two actions; English is unaffected since "Import"/"Ingest" were already distinct words.
- The response envelope has one combined `imported` counter (create and update both increment it — there's no separate "updated" count), so the summary reports "Imported/Deleted/Unchanged" counts, not a 4-way imported/updated/deleted/unchanged breakdown.

**Known, deliberately out-of-scope items** (per the spec's own Out of Scope section, unchanged): cross-instance import portability, bounding background-job volume from one import, auto-generating vocabulary notes, a shared generic export-import engine across the four types (deliberately four parallel implementations).

**Fix-forward round (blocking issues found during review):** A reviewer pass identified 5 critical data-safety issues, all fixed before final validation.
1. **Omitted `items:` silently deleted all items** (vocabulary/models wholesale replace): `Items` field changed from slice to pointer-to-slice (`*[]Item`) so an omitted field decodes to `nil` (distinguishable from an explicitly empty slice), allowing the import handler to reject the import as incomplete rather than silently replacing a list with zero items.
2. **Wholesale replacement not transactional** (data-loss risk on mid-replacement failure or client disconnect): new `Begin`/`*Tx`-suffixed methods added to `vocabulary.Store` and `models.Store`, allowing the whole delete-then-re-add sequence to run within a single database transaction (using a timeout-bounded context that survives the triggering request's cancellation, ensuring cleanup completes even if the HTTP request closes early).
3. **Item-level writeback unguarded against stale positions** (silently writing a generated transcription/translation to the WRONG item if its position shifted before the job ran): every item-level writeback now includes the item's phrase as a guard condition in its UPDATE statement — if the phrase no longer matches, the UPDATE fails (and the job fails), rather than silently succeeding on whatever item now occupies that position.
4. **Update path allowed blanking text/dialog body** (should-fix, not a blocker but risky): validation added to reject any import item with an empty body.
5. **Blank language on update bypassed admin re-check** (should-fix, permission boundary): blank language on an update-by-id now re-checks `CanEdit` on the existing language before accepting the "leave unchanged" semantics, not after.

## Validation

Full end-to-end deploy + validation against the local k3d dev cluster (`task deploy-phraseforge`), live LLM calls capped at exactly 6 (per explicit user direction to minimize LLM-call testing this session):

- **Deploy**: completed cleanly; confirmed no schema migration needed for this feature (the idempotent migrate job ran with no changes).
- **Zero-LLM-call checks, all passing, confirmed to genuinely cost zero calls** (jobs count never moved): malformed YAML rejected with a clean 400; all four export endpoints (texts/dialogs/vocabulary/models) return valid, correctly-typed YAML; idempotent delete of a nonexistent id counts toward `deleted` with no error, matching the spec's stated convention; an `id` that doesn't exist (not a delete) is a per-item error, not a silent create or a 500; re-importing an unchanged row is correctly detected as `unchanged` and enqueues no backfill job — notably confirmed even for a pre-existing row whose language needs transcription but whose own transcription field is empty, proving unchanged detection short-circuits job enqueueing entirely rather than only skipping the row write.
- **Live Text import pass** (3 calls: title, translation×2 — French, a language that doesn't need transcription): imported a text with only body/language/script; confirmed the previously-dead `TextWriteback.SetTitle` path was actually exercised for the first time via a real HTTP request (non-empty LLM-generated title), both `en`/`pl` translations written back correctly.
- **Live Vocabulary import pass** (3 more calls, 6 total: transcription, translation×2 — Mandarin/Simplified Han, a language that does need transcription): imported a one-item list missing transcription and both translations. Confirmed via direct SQL query (not just the API response) that the new item-level writeback wrote the transcription and both translations to the correct `(list_id, position)` row — this is the highest-risk new code in the feature (item-level writeback addressed by list id + position rather than a single resource id), and this is its first proof against a real database rather than the unit tests' fake writeback implementations.
- **Cleanup**: all test data (the text, the vocabulary list, and all job rows created during validation) removed; final DB state confirmed identical to the pre-test baseline.

All Acceptance Criteria confirmed met. No regressions, no bugs found.

**Coverage note for future work** (not a defect): this validation exercised only item position 0 (a single-item list, to stay within the LLM-call budget) — the wholesale-replace-on-reimport path and correct addressing for a non-zero item position were not exercised live, though `decideItemBackfill`'s pure-function unit tests do cover position handling without any LLM cost. A deliberate follow-up test with a multi-item, no-backfill-needed list (zero LLM calls) would strengthen confidence in higher-position addressing if desired.

**Fix-forward round verification:** all five fixes verified via `go build`, `go vet`, and `go test -count=1` (all clean). New targeted unit tests added for each fix: omitted-vs-empty items decoding (`Items` pointer-to-slice distinguishes nil from empty slice), phrase-mismatch causing item-level writeback job failure (the guard condition in UPDATE preventing silent wrong-item overwrites), wholesale-replace transaction boundaries, and blank-body/blank-language rejection. No additional live redeploy/LLM-call validation performed for this round per explicit user direction to prioritize speed — these are structural/logic fixes already covered by the new unit tests and the original feature's live validation (6 LLM calls) already proved the underlying end-to-end pipeline works; the fixes narrow failure-mode edge cases, they do not change the happy-path behavior already proven live.

## Documentation Review

Not started.

## Documentation Updates

Not started.
