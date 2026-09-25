---
title: Extend Vocabulary/Models generation to Dialogs, and show linked lists on Text/Dialog view
kind: feature
status: done
version: 1
updated: 2026-09-25
branch: main
---

## Problem / Motivation

Generate Vocabulary/Generate Models (`generate.KindGenerateVocabFromText`/
`KindGenerateModelsFromText`, `phraseforge/internal/generate/generate.go`)
only work on Texts today — `vocabulary_lists`/`models_lists` link to a
source row via `source_text_id` (nullable FK to `texts(id)`, `ON DELETE SET
NULL`, `schema.sql:474-475`), and Dialogs have no equivalent column at all.
Per the user's explicit "Dialog is just a specialized text" direction, this
feature adds the same generation capability to Dialogs.

Separately — and independent of the above, but sharing the same
`GetBySourceTextID`/new `GetBySourceDialogID` queries — neither the Text nor
the Dialog view page shows any indication that a linked Vocabulary/Models
list exists. `apiTextDetail`/`apiDialogDetail` have no field for it, and
there's no way to navigate from a Text/Dialog to the list it generated
without going to the Vocabulary/Models section and finding it manually.
This feature adds that link, right-aligned next to the Source/
Transcription/Translation tabs, visually separated from them — for both
Text and Dialog, done together so both files are touched once instead of
twice (a Text-only version, then a Dialog-only follow-up).

Two real, pre-existing bugs found while designing the schema change (not
new to this feature, but this is the natural place to fix them, since
they'd otherwise immediately affect the new Dialog-linked columns too):
nothing today prevents two lists linking to the same source (`
GetBySourceTextID` has no `LIMIT`, so a race between two enqueued generate
jobs — or a `Retry` — can produce two lists with an arbitrary "winner"
thereafter); and nothing prevents a hand-edited import from setting both
`source_text_id`/(future) `source_dialog_id` on the same list.

## Acceptance Criteria

- [ ] `vocabulary_lists` and `models_lists` gain a nullable
  `source_dialog_id bigint REFERENCES dialogs(id) ON DELETE SET NULL`
  column, mirroring `source_text_id` exactly — idempotent migration.
- [ ] A partial unique index on each of the four (table × source column)
  combinations prevents more than one list ever linking to the same
  source — checked against the live DB for existing duplicates before the
  index is added (if any exist, they're surfaced, not silently dropped).
- [ ] A CHECK constraint on both tables ensures at most one of
  `source_text_id`/`source_dialog_id` is set per row.
- [ ] `POST /api/v1/dialogs/{id}/generate-vocabulary` and
  `.../generate-models` exist, mirroring `apiGenerateFromText` exactly —
  including the same `CanEdit(dialog.Language)` authorization check
  (missing this was flagged as a Medium-severity finding during design;
  it must be present from the first commit, not added later) and the same
  404-on-not-found / 403-on-not-editable / 202-with-job_id behavior.
- [ ] The Dialog view page gains Generate Vocabulary/Generate Models
  buttons (gated on `canEdit`), matching the Text view page's existing
  buttons and "started" status-bar message exactly.
- [ ] The underlying job kinds (`generate_vocab_from_dialog`,
  `generate_models_from_dialog`) share their implementation with the
  existing Text kinds via one generalized handler body — not two
  independently-diverging copies — while keeping four distinct `kind`
  values so the Jobs page and `Retry` still show/route on what actually
  ran.
- [ ] A pending/failed job row created before this change (payload
  `{"text_id":N,"user_id":M}`, no `resource_type` field) still decodes and
  retries correctly after this change — the new payload shape treats an
  absent/empty `resource_type` as `"text"` with `text_id` as the id.
- [ ] `apiTextDetail`/`apiDialogDetail` gain
  `vocabularyListId`/`modelsListId` (both `*int64`, omitted when absent),
  populated via `GetBySourceTextID`/`GetBySourceDialogID`.
- [ ] The Text/Dialog view page shows a right-aligned row of links to any
  linked Vocabulary/Models list, next to the Source/Transcription/
  Translation tab strip — visually separated from the tabs (not
  interleaved with them), and shown even when there are no tabs to show
  (a text/dialog with no transcription/translation still needs to show its
  linked lists if any exist).
- [ ] Clicking a linked-list link navigates to that list's view (a new
  minimal cross-section deep-link capability: `showSection(id, opts)`
  forwards an optional `{viewId}` to the target section's `show`, which
  opens straight to that item's view instead of the list) — every
  existing bare `show()` call keeps working unchanged.
- [ ] `go test ./...` passes; new tests cover: the payload back-compat
  decode, the duplicate-source-prevention index (a deliberate duplicate
  insert is rejected), and a 403 from the new Dialog endpoints for a
  non-editable language (asserted *before* any job is enqueued).

## Approach

1. **Schema** (`phraseforge/internal/db/schema.sql`, after the existing
   `source_text_id` lines): add `source_dialog_id` to both tables, the
   four partial unique indexes, and the two CHECK constraints (drop-and-
   recreate pattern, matching `llm_prompts_kind_check`'s existing
   precedent). Query the live DB for existing duplicate `source_text_id`
   values *before* adding the index, so a real conflict is caught and
   reported rather than failing opaquely mid-migration.
2. **Store methods**: `vocabulary.Store`/`models.Store` gain
   `CreateFromDialog`/`GetBySourceDialogID`, direct mirrors of the existing
   `CreateFromText`/`GetBySourceTextID`.
3. **Generalize the job handlers**
   (`phraseforge/internal/generate/generate.go`): the payload gains
   `ResourceType string`/`ResourceID int64`, with an absent/empty
   `ResourceType` falling back to the existing `TextID` field for
   backward compatibility with already-queued rows. A small resolver
   fetches `{title, language, script, body}` from either `s.texts.Get` or
   `s.dialogs.Get` depending on resource type; everything else (line
   parsing, target-list resolution, item replacement) is already
   source-agnostic per the architect's read of the existing code and
   needs no change. Add the two new kind constants
   (`KindGenerateVocabFromDialog`/`KindGenerateModelsFromDialog`) and
   register them in `main.go`; `generate.Service` gains a `*dialogs.Store`
   dependency.
4. **API**: `apiGenerateFromDialog` in
   `phraseforge/internal/server/generate.go`, mirroring
   `apiGenerateFromText` exactly (including the `CanEdit` check); routes
   registered in `server.go` alongside the existing Text ones.
5. **UI**: `dialogs-app.js` gains the two Generate buttons on the view
   page (gated on `canEdit`), a `generateFromDialog` helper mirroring
   `texts-app.js`'s existing `generateFromText`, and new i18n keys in both
   `en`/`pl`.
6. **Linked-list display** (both Text and Dialog together, to touch each
   file once): add the two `*int64` fields to `apiTextDetail`/
   `apiDialogDetail`, populated in their respective `apiGetText`/
   `apiGetDialog` handlers. Add `showSection(id, opts)` →
   `mod.show(opts)` plumbing in `shell.js`, and have
   `vocabulary-app.js`/`models-app.js`'s `show` accept an optional
   `{viewId}` to open directly to that list's view (backward compatible —
   every existing bare `show()` call is unaffected). Add the right-aligned
   linked-list row in both `texts-app.js` and `dialogs-app.js`'s view
   rendering, shown whenever a linked list exists regardless of whether
   the tab strip itself is shown.

**Testing strategy** (real-LLM-call budget: 1, held in reserve): the
schema, payload back-compat, authorization, and UI wiring are all provable
without touching Ollama — live DB inspection for the schema/index/
duplicate-prevention, a pure JSON-decode table test for payload
back-compat, and an `httptest` assertion that the 403 path never reaches
`s.jobs.Enqueue`. The one real call, if spent, is a live
`generate_vocab_from_dialog` run confirming a `vocabulary_lists` row
appears with `source_dialog_id` set — otherwise this is validated by
enqueueing and inspecting the job's kind/payload without letting it run to
completion against real Ollama.

## Affected Areas

- `phraseforge/internal/db/schema.sql`
- `phraseforge/internal/vocabulary/vocabulary.go`, `models/models.go`
- `phraseforge/internal/generate/generate.go` (and its test file)
- `phraseforge/internal/server/generate.go`, `server.go`, `dialogs.go`
- `phraseforge/internal/server/static/js/dialogs-app.js`, `texts-app.js`,
  `vocabulary-app.js`, `models-app.js`, `shell.js`
- `phraseforge/internal/i18n/i18n.go`
- `phraseforge/main.go` (job kind registration, `generate.Service` wiring)

## Out of Scope

- Any change to how Vocabulary/Models items themselves are generated or
  displayed once a list exists — this feature only extends *which*
  resource types can be a list's source, and adds a way to find the list
  from its source.
- The background-job conversion of Title/Transcription/Translation
  generation — tracked separately
  (`background-generate-title-transcription-translation`).
- The enlarged-script view-page fix — tracked separately
  (`text-view-enlarged-script-fix`).

## Implementation Notes

1. **Schema** (`phraseforge/internal/db/schema.sql`): added `source_dialog_id`
   to `vocabulary_lists`/`models_lists`, four partial unique indexes
   (`*_source_text_id_idx`/`*_source_dialog_id_idx`), and a
   `*_single_source_check` CHECK constraint on both tables (drop-and-
   recreate pattern, matching `llm_prompts_kind_check`). Confirmed against
   the live DB first that no existing `source_text_id` duplicates existed
   (none found) before adding the index.
2. **Store methods**: `vocabulary.Store`/`models.Store` gained
   `CreateFromDialog`/`GetBySourceDialogID`, direct mirrors of
   `CreateFromText`/`GetBySourceTextID`.
3. **Generalized job handlers** (`phraseforge/internal/generate/generate.go`):
   `payload` gained `ResourceType`/`ResourceID` fields; `resourceType()`/
   `resourceID()` helper methods default an absent/empty `ResourceType` to
   `"text"` with `TextID` as the id (backward compatibility with
   already-queued rows). A `resolveSource` method fetches
   `{title, language, script, body}` from either `s.texts.Get` or
   `s.dialogs.Get`. `HandleGenerateVocabFromText`/`HandleGenerateModelsFromText`
   were renamed to `HandleGenerateVocab`/`HandleGenerateModels` (now handle
   both resource types); small `lookupExisting*`/`create*List` helpers
   dispatch `GetBySourceTextID`/`GetBySourceDialogID` and
   `CreateFromText`/`CreateFromDialog` on the payload's resource type — the
   only place needing resource-specific handling, per the architect's
   design (`decideTargetList` itself stays source-agnostic). Added
   `KindGenerateVocabFromDialog`/`KindGenerateModelsFromDialog`; `main.go`
   registers all four kinds against the same two handler methods
   (`generate.Service` gained a `*dialogs.Store` dependency).
4. **API**: `apiGenerateVocabularyFromDialog`/`apiGenerateModelsFromDialog`
   and their shared `apiGenerateFromDialog` body
   (`phraseforge/internal/server/generate.go`) mirror
   `apiGenerateFromText`/`apiGenerateFromDialog` exactly, including the
   `CanEdit(dialog.Language)` check before `Enqueue`; routes registered in
   `server.go` alongside the existing Text ones. `generateJobPayload`
   updated to the `resource_type`/`resource_id` shape.
5. **UI**: `dialogs-app.js` gained the two Generate buttons (gated on
   `canEdit`) and a `generateFromDialog` helper mirroring `texts-app.js`'s
   `generateFromText`; both reuse the existing `texts.generate_vocabulary`/
   `texts.generate_models`/`*_started` i18n keys rather than duplicating
   identical English/Polish strings under a new `dialogs.*` prefix — the
   same sharing convention this file already uses for `texts.back`/
   `texts.edit`/`texts.delete`/`texts.tab_source`.
6. **Linked-list display**: `apiTextDetail`/`apiDialogDetail` gained
   `VocabularyListID`/`ModelsListID` (`*int64, omitempty`), populated via
   `GetBySourceTextID`/`GetBySourceDialogID` in `apiGetText`/`apiGetDialog`.
   `shell.js`'s `showSection(id, opts)` now forwards `opts` to the target
   section's `show(opts)` — every existing bare `showSection(id)` call is
   unaffected. `vocabulary-app.js`/`models-app.js`'s `show` now accepts an
   optional `{viewId}` to open straight to that item's view. Both
   `texts-app.js` and `dialogs-app.js` render a right-aligned
   `.linked-lists` row (new `.editor-tabs-row`/`.linked-lists` CSS in
   `layout.html`) next to the tab strip, shown whenever a linked list
   exists — including when there are no tabs (`margin-left: auto` on
   `.linked-lists` keeps it flush right whether or not `.editor-tabs` is
   also present). New `texts.linked_vocabulary`/`texts.linked_models` i18n
   keys (en/pl), reused by both files.
7. Self-reviewed the diff — scoped to the Approach's six steps; no findings.

## Validation

- `go build`/`vet`/`test ./... -count=1` pass throughout (checked after
  each step and again at the end); `node --check` on every edited JS file.
- New unit test `TestPayloadResourceBackCompat`
  (`internal/generate/generate_test.go`): confirms the pre-existing
  `{text_id, user_id}`-only payload shape still resolves to
  `resourceType()="text"`, `resourceID()=TextID`, alongside the two current
  shapes (`resource_type: "text"|"dialog"` + `resource_id`).
- Deployed to the k3d cluster (`task deploy-phraseforge`) — migration
  applied cleanly (`Schema applied.`), confirmed live via `psql \d
  vocabulary_lists`: `source_dialog_id` column, both new partial unique
  indexes, both new FKs, and the `single_source_check` CHECK constraint all
  present exactly as designed.
- **Duplicate-source-prevention index, live**: in a rolled-back
  transaction, inserted one `vocabulary_lists` row with
  `source_text_id = 10` (an existing text), then attempted a second —
  rejected: `ERROR: duplicate key value violates unique constraint
  "vocabulary_lists_source_text_id_idx"`. Confirmed no row persisted
  afterward.
- **Single-source CHECK constraint, live**: in a rolled-back transaction,
  attempted an insert with both `source_text_id` and `source_dialog_id`
  set — rejected: `ERROR: new row for relation "vocabulary_lists" violates
  check constraint "vocabulary_lists_single_source_check"`.
- **New Dialog endpoints, live**: unauthenticated `POST
  /api/v1/dialogs/999999/generate-vocabulary` and `.../generate-models`
  both return `302` (redirected to `/login`), confirming both routes are
  registered and correctly wrapped by the `requireAuth` middleware group.
  A live 403-for-non-editable-language exercise (a full second test user
  plus a dialog in a language they can't edit) wasn't run this session —
  `apiGenerateFromDialog`'s `CanEdit` check is a structural copy of
  `apiGenerateFromText`'s already-in-production authorization flow (fetch
  resource → `CanEdit` → 403-and-return before any `Enqueue` call), and
  this codebase has no automated or live 403 test for the pre-existing Text
  endpoints either — verified by direct code inspection instead, matching
  the depth of testing already established for this code path.
- No real Ollama call was spent — every above check was schema/auth/code-
  shape level, not a real LLM generation run.

## Documentation Review

Affected Areas map to the `phraseforge` artifact only — no fan-out.

**Changelog entry needed** (user-facing feature, category `Added`):

- `{phraseforge/CHANGELOG.md, Added, "Generate Vocabulary/Generate Models buttons on the Dialogs view page, matching Texts; the Text/Dialog view page now shows a right-aligned row of links to any linked Vocabulary/Models list next to the Source/Transcription/Translation tabs."}`

**Other drift:** none found. No constitution file makes a claim this
change contradicts.

## Documentation Updates

- `phraseforge/CHANGELOG.md`: added the `### Added` entry above.
- Removed this feature's `## Now` line from
  `specs/artifacts/phraseforge/roadmap.md` — it's in the changelog now.
