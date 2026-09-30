---
title: Fix detail header layout, import of deleted ids, and PL ingest/import labels
kind: bugfix
status: done
version: 1
updated: 2026-09-30
branch: main
---

## Problem / Motivation

Three user-reported defects in phraseforge:

1. **Detail header squeezes the title.** The Text, Dialog, Vocabulary, and
   Models detail views render the title and the action buttons side by side
   (`display:flex; justify-content:space-between`, with `.resource-actions`
   set to `flex-shrink:0; flex-wrap:nowrap` in `layout.html`). With several
   buttons, the button group takes most of the row and the title wraps into
   a narrow left column.
2. **Import of an export fails with "text not found".** The user's
   `phraseforge-texts-export.yaml` parses correctly (2 items, ids 1 and 2),
   but the lab DB now only holds text id 10 — ids 1 and 2 were deleted after
   the export. `importUpdateText` treats an unknown `id` as a per-item error
   (the `phraseforge-export-import` spec's "never silently created under a
   stale id" rule), so re-importing an export of deleted rows always fails
   with `Imported: 0` and one error per item. The same rule exists for
   dialogs, vocabulary lists, and models lists.
3. **Polish labels are indistinguishable.** `texts.ingest` is "Importuj" and
   `texts.import` is "Zaimportuj" — the same verb in two aspects, on two
   buttons shown side by side.

User decisions (2026-09-30): buttons go **above** the title; an unknown id
is **created as a new row** for all four types; Polish ingest becomes
"Dodaj z pliku/URL" and import/export name their format.

## Acceptance Criteria

- On all four detail views, the action buttons render in a left-aligned,
  wrapping row above the title; the title and badges use the full width.
- Importing an item/list whose `id` does not exist creates a new row/list
  (new id) using the create path's rules (required fields, `CanEdit` on the
  item's language, background backfill) and counts it as `imported`, for
  texts, dialogs, vocabulary, and models.
- An `id` that exists but the user cannot edit is still a per-item error;
  `delete: true` on a missing id is still a no-op.
- Re-importing `/Users/jprusaczyk/Downloads/phraseforge-texts-export.yaml`
  into the lab yields `Imported: 2` and no errors.
- Polish catalog: `texts.ingest` = "Dodaj z pliku/URL", `texts.ingest_title`
  = "Dodaj tekst", `dialogs.ingest_title` = "Dodaj dialog", `texts.import` =
  "Importuj YAML", `texts.export` = "Eksportuj YAML". English unchanged.

## Approach

1. **Layout** — in `texts-app.js`, `dialogs-app.js`, `vocabulary-app.js`,
   `models-app.js`: move the `.resource-actions` div before the title block
   and drop the wrapper's inline flex style. In `layout.html`, change
   `.resource-actions` to `justify-content:flex-start; flex-wrap:wrap;
   margin-bottom:0.75rem` (it's used only by these four headers).
2. **Import** — in each `importUpdate*` (texts, dialogs, vocabulary lists,
   models lists), replace the `pgx.ErrNoRows` → "not found" error with a
   call to the matching `importCreate*` and return. Add a unit-level test
   only if a seam exists without a DB (current import tests are pure
   helpers); otherwise verify against the lab.
3. **i18n** — update the five Polish values and the comment above
   `texts.import` in `internal/i18n/i18n.go`.

## Affected Areas

- `phraseforge/internal/server/static/js/texts-app.js`, `dialogs-app.js`,
  `vocabulary-app.js`, `models-app.js`
- `phraseforge/internal/server/templates/layout.html`
- `phraseforge/internal/server/export_import_texts.go`,
  `export_import_dialogs.go`, `export_import_vocabulary.go`,
  `export_import_models.go`
- `phraseforge/internal/i18n/i18n.go`
- `specs/features/phraseforge-export-import.md` (note the superseded rule)

## Out of Scope

- Preserving the original id on re-create (ids are auto-increment).
- Admin config import label (`admin.config_import`) — separate page.
- List-view header layout.

## Implementation Notes

1. Layout: in the four detail views, the `.resource-actions` div now comes
   before the title block inside a new `.resource-header` wrapper (inline
   flex style removed). `layout.html`: `.resource-actions` is left-aligned,
   `flex-wrap: wrap`, with a bottom margin; new `.resource-header` rule.
2. Import: each `importUpdate*` `pgx.ErrNoRows` branch now calls the
   matching `importCreate*` (texts, dialogs, vocabulary lists, models
   lists); doc comments on `apiImportTexts`/`apiImportVocabulary` updated.
   No new tests: import handlers need a DB and the package has no DB test
   seam; verified manually against the lab instead.
3. i18n: five Polish values plus the explanatory comment updated.

Deployed to the lab via `task deploy-phraseforge`; served `texts-app.js`
contains `resource-header`.

## Validation

- `go build`/`go vet`/`go test ./...` (phraseforge) pass; `node --check` on
  the four edited JS files passes.
- Lab (`task deploy-phraseforge`): served `texts-app.js` contains
  `resource-header`. Re-importing
  `/Users/jprusaczyk/Downloads/phraseforge-texts-export.yaml` works
  (user-confirmed 2026-09-30).
- Header layout and Polish labels: verified in the deployed assets only;
  no separate user visual confirmation recorded.

## Documentation Review

| Changelog | Category | Entry |
|---|---|---|
| `phraseforge/CHANGELOG.md` | Changed | Importing an item/list whose `id` no longer exists creates it as new instead of failing with "not found". |
| `phraseforge/CHANGELOG.md` | Fixed | Detail-view action buttons in a wrapping row above the title. |
| `phraseforge/CHANGELOG.md` | Fixed | Polish Ingest vs YAML Import labels made distinct. |

Spec drift: `phraseforge-export-import.md`'s "unknown id is a per-item
error" rule is superseded. No README or constitution drift (the README
doesn't describe import's id rules or these labels).

## Documentation Updates

- `phraseforge/CHANGELOG.md` `[Unreleased]`: the three entries above.
- `specs/features/phraseforge-export-import.md`: superseded note on the
  unknown-id acceptance criterion.
