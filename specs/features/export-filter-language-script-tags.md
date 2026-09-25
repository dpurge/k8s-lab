---
title: Require language+script on export, with optional tag filtering
kind: feature
status: done
version: 1
updated: 2026-09-25
branch: main
---

## Problem / Motivation

Today, exporting Texts, Dialogs, Vocabulary, or Models
(`GET /api/v1/{texts,dialogs,vocabulary,models}/export`) only supports an
optional `?language=` filter (e.g.
`phraseforge/internal/server/export_import_texts.go:61-73`); there is no
way to narrow by `script` or by tag/label, and omitting `language`
altogether exports everything the caller can edit. Language and Script are
already mandatory, non-null columns on all four resource types, so
filtering by both is a natural, always-available narrowing — per the
approved decision, export now always requires both, replacing the
"language optional, everything else implicit" behavior. Tags remain
optional: Tags are free-text, many-to-many via a polymorphic `tagging`
table (`phraseforge/internal/tags/tags.go:19-36`); when given, an item must
carry every one of the requested tags to be included (ALL-match).

## Acceptance Criteria

- [ ] All four export endpoints require both `language` and `script` query
  params; either missing returns `400 Bad Request` with a clear message —
  no export is produced.
- [ ] The existing authorization boundary (scoped to languages the caller
  may edit, via `EditableLanguages`) is unchanged and still applied
  alongside the new required filters, not replaced by them.
- [ ] An optional `tags` query param (comma-separated, matching this app's
  existing tag-input convention) filters results to items having **every
  one** of the given tags (ALL-match); omitting it exports every item in
  that language+script combination regardless of tags.
- [ ] The same behavior is applied identically across Texts, Dialogs,
  Vocabulary, and Models — one shared helper/pattern, not four
  independently-diverging implementations.
- [ ] The Export control on each of the four list pages gains inputs for
  language, script, and optional tags before triggering the download,
  replacing today's implicit "whatever the current view shows" behavior
  with explicit required inputs.
- [ ] The export/import YAML item shape is unchanged (Language/Script/Tags
  are already present per item, e.g. `apiTextImportItem`) — only which
  items get included changes, not the file format.
- [ ] `go test ./...` passes; new/updated tests cover: missing `language`
  → 400, missing `script` → 400, `tags` narrows correctly (ALL-match,
  including that an item missing even one requested tag is excluded), and
  the existing authorization boundary still applies.

## Approach

1. Add a shared helper (used by all four export handlers) that validates
   `language`+`script` are both present (400 otherwise) and parses the
   optional `tags` query param.
2. Extend each resource's list/query path to filter by exact `script`
   match and, when `tags` is given, by "has every one of the requested
   tags" against the `tagging` table (`tags.go`'s existing many-to-many
   pattern) — post-filtering in the handler if the underlying `List`
   method isn't already shaped to take these params, whichever is the
   smaller change per resource.
3. Apply consistently to `export_import_texts.go`, `_dialogs.go`,
   `_vocabulary.go`, `_models.go`.
4. Frontend: extend `buildExportURL` (and its four call sites in
   `texts-app.js`/`dialogs-app.js`/`vocabulary-app.js`/`models-app.js`) to
   require `script` and optionally append `tags`; add minimal UI (a script
   select, a tag input) near each Export control, reusing whatever
   script/tag-selection component each section already uses elsewhere
   (IME/script config, tag autocomplete) rather than building a new one.
5. Tests: table-driven handler tests per resource type (or one shared test
   helper exercised four times) covering the acceptance criteria; no LLM
   involvement, no real-call budget needed.

## Affected Areas

- `phraseforge/internal/server/export_import_texts.go`, `_dialogs.go`,
  `_vocabulary.go`, `_models.go` (and their test files)
- `phraseforge/internal/server/static/js/texts-app.js`, `dialogs-app.js`,
  `vocabulary-app.js`, `models-app.js` (Export control + `buildExportURL`)

## Out of Scope

- A bulk "export every language/script in one file" convenience —
  explicitly dropped by requiring language+script; a caller wanting
  everything loops client-side per combination.
- Tag ANY-match ("has at least one of these tags") mode — only ALL-match
  is built.
- Any change to the import side — it already accepts Language/Script/Tags
  per item and needs no change.

## Implementation Notes

1. `phraseforge/internal/server/export_import.go`: added `exportFilter`
   (Language/Script/Tags) and `requireExportFilter(w, r)`, a shared helper
   used by all four export handlers — validates `language`+`script` are
   both present (400 otherwise) and parses `tags` via `tags.Parse` (same
   normalization as every other tag input in this app).
2. `phraseforge/internal/tags/tags.go`: added
   `ResourceIDsWithAllTags(ctx, resourceType, names) ([]int64, error)` —
   `GROUP BY resource_id HAVING COUNT(DISTINCT name) = len(names)`, the
   standard ALL-match tag query.
3. `phraseforge/internal/server/server.go`: added a `filterByScript[T
   any]` generic helper alongside the existing `filterByID[T any]` (which
   already existed for the list-view's own single-tag `?tag=` filter) —
   reused both here instead of writing four independently-diverging
   per-resource filter loops, directly satisfying the Acceptance
   Criteria's "one shared helper/pattern" requirement.
4. Applied identically to `apiExportTexts`/`apiExportDialogs`/
   `apiExportVocabulary`/`apiExportModels`: call `requireExportFilter`
   first; pass `filter.Language` into the existing `exportScopeLanguages`
   (unchanged — an unauthorized language still falls back to the caller's
   full editable set, exactly as it already did); filter the fetched list
   by `filter.Script` via `filterByScript`; when `filter.Tags` is
   non-empty, look up matching ids via `ResourceIDsWithAllTags` and narrow
   via `filterByID`.
5. Frontend (`texts-app.js`/`dialogs-app.js`/`vocabulary-app.js`/
   `models-app.js`): replaced the static Export `<a href=...>` link (built
   once from the sidebar's language filter) with a small toolbar —
   language `<select>` (defaulting to the current sidebar filter, reusing
   each file's existing `languageOptions()`), script `<select>` (reusing
   `scriptOptions()`), a plain tags `<input>` (matching the create/edit
   form's own tag-input convention), and an Export button that reads all
   three at click time and navigates to the built URL. No new i18n keys
   needed — reused `texts.field_language`/`texts.field_script`/
   `texts.field_tags_hint`, already present in every section's own i18n
   key list (each section's create/edit form already uses them).
6. Tests added to `export_import_test.go`: `requireExportFilter` (missing
   language → not ok/400, missing script → not ok/400, both present
   parses and normalizes tags, omitted tags parses to empty) via
   `httptest`, no DB; `filterByScript` and `filterByID` (the latter
   exercising the ALL-match contract the tags= filter depends on: an item
   missing even one requested tag must be excluded) via plain structs, no
   DB.
7. Self-reviewed the diff — no findings; not invoking `reviewer` (not
   security-sensitive or architecturally significant — a straightforward
   filter addition reusing an existing generic helper, no new
   concurrency/state).
8. **Fix-forward round** (user feedback after delivery, from the actual
   rendered page): the first pass rendered bare, unstyled `<select>`/
   `<input>` elements directly in the toolbar, always visible, sitting
   next to styled `pf-button` elements with no label — exactly the "looks
   broken, no visible place to enter tags" report, and a UX regression
   from the New/Ingest/Export/Import toolbar's established plain-buttons
   pattern. Fixed by reverting Export to a plain button and moving the
   language/script/tags form into `#confirmDialog` via `showContent()`
   (the same mechanism `jobs-view-dialog-and-retry-confirm` added this
   session), wrapping each field in `<pf-field>` for proper labeling —
   matching the New/Edit forms' own field styling exactly. Confirming
   navigates to the built export URL; cancelling does nothing. Verified
   this time via a jsdom harness that executes the real `texts-app.js`
   end-to-end: toolbar has no bare inputs (only 4 plain buttons), the
   dialog opens on Export with three correctly labeled fields, Cancel
   does not navigate, and Confirm navigates to the exact expected URL
   (including the tags param). Applied identically to `dialogs-app.js`/
   `vocabulary-app.js`/`models-app.js`.

## Validation

- `go build ./...`, `go vet ./...`, `go test ./... -count=1` all pass —
  no regressions; 6 new tests pass (`TestRequireExportFilterMissingLanguage`,
  `TestRequireExportFilterMissingScript`,
  `TestRequireExportFilterBothPresentParsesTags`,
  `TestRequireExportFilterOmittedTagsIsEmpty`,
  `TestFilterByScriptKeepsOnlyExactMatch`,
  `TestFilterByIDExcludesItemMissingEvenOneTag`).
- `node --check` on all four edited JS files — no syntax errors.
- **Real-cluster verification** (deployed via `task deploy-phraseforge`;
  zero LLM calls involved — export touches no LLM code path):
  - `GET /api/v1/texts/export` with neither `language` nor `script` → 400;
    with only `language` → 400.
  - With both `language=cmn&script=hans` → 200, the one matching text
    returned with its `tags: [cmn]`.
  - `tags=cmn` (a tag the item actually has) → the item is included.
  - `tags=cmn,nonexistent-tag` (ALL-match: item has one but not both) →
    `items: []` — confirms an item missing even one requested tag is
    correctly excluded, not just any-match included.
  - `script=hant` (the item is actually `hans`) → `items: []` — confirms
    exact script matching.
  - Confirmed via `curl` that the deployed `texts-app.js` serves the new
    `exportScript`/`exportTags`/`exportTextsBtn` elements.
  - No browser available this session to visually confirm the toolbar
    layout across all four sections — functional behavior fully verified
    via the API; visual check left to the user.
- **Post-delivery correction**: the user reported the first version was
  "absolutely broken" (always-visible bare form elements, one looking like
  a disabled button, no visible way to enter tags) — confirmed by the
  actual rendered HTML they pasted. Fixed per the note above; re-verified
  via a jsdom harness executing the real JS (not just read the source):
  confirmed the toolbar renders only 4 plain buttons (no bare
  `#exportLanguage`/`#exportScript`/`#exportTags`), the dialog opens with
  three `pf-field`-labeled inputs ("Language"/"Script"/"Tags"), Cancel
  makes zero navigation calls, and Confirm navigates to
  `/api/v1/texts/export?language=cmn&script=hans&tags=travel%2C+food` —
  the exact expected URL. `go build`/`vet`/`test` re-run clean; redeployed
  and confirmed via `curl` that the live pod serves the corrected JS.

## Documentation Review

Affected Areas map to the `phraseforge` artifact only — no fan-out.

**Changelog entry needed** (user-facing, breaking change to existing
export behavior, category `Changed` — matches this file's existing
`### Changed` section style):

- `{phraseforge/CHANGELOG.md, Changed, "exporting Texts/Dialogs/Vocabulary/Models now requires both language and script (previously language alone, optional); an optional tags filter (comma-separated, matches items carrying every given tag) narrows further."}`

**Other drift:** none found. No constitution file makes a claim this
change contradicts.

## Documentation Updates

- `phraseforge/CHANGELOG.md`: added the `### Changed` entry above.
- Removed this feature's `## Now` line from
  `specs/artifacts/phraseforge/roadmap.md` — it's in the changelog now.
