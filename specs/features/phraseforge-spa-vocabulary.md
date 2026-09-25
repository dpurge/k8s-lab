---
title: Port Vocabulary to phraseforge's SPA shell via a JSON API
kind: feature
status: done
version: 1
updated: 2026-09-24
branch: main
---

## Problem / Motivation

Third SPA item. Vocabulary is genuinely more complex than Texts/Dialogs: it's a **list-plus-items** model (a vocabulary list has a title/language/script, and separately a sequence of items — phrase/grammar/transcription common fields, plus translation/notes per the editing user's own site locale), not a single document. The old `vocab-edit.html` combines three things on one page: the list-meta edit form, an item add/edit form (add by default, or edit one item named by `?edit=<position>`), and a read-only table of every existing item with per-row Edit/Delete. This feature's client "manage" view reproduces that combination without a URL change — item-editing-by-position becomes in-page JS state instead of a query parameter, since the single-URL model (decided in `phraseforge-spa-shell-texts`) applies here too.

**The real risk in this feature**: item positions shift on delete (`vocabulary.DeleteItem` renumbers everything after the deleted position, per its own doc comment) — the client's item table must always re-fetch after any item mutation (add/update/delete) rather than patching positions locally, or it will show stale/wrong position numbers for subsequent edits.

## Acceptance Criteria

- `/api/v1/vocabulary` — `GET` (list, with `itemCount` per list, matching `handleVocabList`'s `ItemCounts` exactly), `POST` (create), `GET one` (list detail **including its full item array**, each item carrying its locale-joined `translation`/`notes` — reproduces `handleVocabEditForm`'s `vocabViewRow` pairing, since this one response serves both the client's View and Manage states), `PUT` (update list meta), `DELETE`.
- `/api/v1/vocabulary/{id}/items` — `POST` (add item, returns `{position}`); `/api/v1/vocabulary/{id}/items/{position}` — `PUT` (update), `DELETE`. Each preserves `vocabulary.go`'s exact logic (the same `CanEdit` check via the list's own language, the same `AddItem`/`UpdateItem`/`SetTranslations`/`DeleteItem` calls in the same order, the same `ErrItemNotFound` → 404 mapping).
- `vocabulary-app.js`'s `showManage(id)` renders the list-meta form, an item add/edit form (add by default; clicking a row's "Edit" sets **in-page state**, no URL change, and re-renders the item form prefilled — replacing the old `?edit=<position>` query param), and the items table with per-row Edit/Delete (`pf-dialog`-confirmed, fetch-based, matching Texts/Dialogs' delete pattern). **After every item add/update/delete, the whole manage view re-fetches `GET /api/v1/vocabulary/{id}` and re-renders the table from scratch** — positions shift on delete, so nothing about the table is patched incrementally.
- `showView(id)` (read-only: list meta, Copy/Edit/Delete, items table without edit/delete controls) and `showNew()` (create form; on success, goes to `showManage(id)` — **not** `showView(id)`, matching `handleVocabCreate`'s original redirect to the edit page since a new list starts empty).
- `dialogs-app.js`'s already-fixed `showList()` language-filter pattern (read `window.location.search` fresh, per `specs/memory.md`'s `[gotcha]`) is applied to `vocabulary-app.js` **from the start** — not discovered as a bug a third time.
- `defer` on `vocabulary-app.html`'s script tag from the start (per the earlier `[gotcha]`/`[convention]` pair).
- `GET /vocabulary` serves the shell; the six old `/vocabulary/*` routes and their handlers, plus `vocab-list.html`/`vocab-new.html`/`vocab-edit.html`/`vocab-view.html`, are removed once validated.
- No change to Texts, Dialogs, Models, Admin, Profile, or any `pf-*` component.
- Live validation must specifically exercise: creating a list, adding two items, editing the first one (confirm prefill), deleting it, confirming the second item's position correctly shifted to `0` afterward (the actual risk this feature is about), then deleting the whole list.

## Approach

Same ordered plan as the prior two SPA features (API → shell+list → new → view → manage → delete → cutover → validate), with the two already-recorded gotchas (`defer`, language-filter query param) applied from the start rather than found afterward. The one new design piece is `showManage`'s in-page "which item, if any, is being edited" state, replacing the server's `?edit=` query param — since items are re-fetched wholesale after every mutation anyway (per the position-shift risk above), this state is simply "the position last clicked, or none," reset to none after every successful add/update/delete.

## Affected Areas

- `phraseforge/internal/server/vocabulary.go` (new `apiListVocabLists`/`apiCreateVocabList`/`apiGetVocabList`/`apiUpdateVocabList`/`apiDeleteVocabList`/`apiAddVocabItem`/`apiUpdateVocabItem`/`apiDeleteVocabItem`; removal of the seven old HTML handlers)
- `phraseforge/internal/server/server.go` (new `/api/v1/vocabulary` route group; `GET /vocabulary` → new `handleVocabularyApp`; removal of the six old `/vocabulary/*` routes)
- new `phraseforge/internal/server/templates/vocabulary-app.html`
- new `phraseforge/internal/server/static/js/vocabulary-app.js`
- removed: `phraseforge/internal/server/templates/{vocab-list,vocab-new,vocab-edit,vocab-view}.html`
- `specs/artifacts/phraseforge/roadmap.md` (`## Later` → `## Now` at B2 start)
- `phraseforge/CHANGELOG.md` (entry at B5)

## Out of Scope

- Models, Admin, Profile, the unified-shell item, the pagination item — separate roadmap entries.
- Any change to the vocabulary data model itself (no new fields, no validation changes).
- Any change to `pf-*` components.

## Implementation Notes

**Step 1 — JSON API.** Added `apiListVocabLists`/`apiCreateVocabList`/`apiGetVocabList`/`apiUpdateVocabList`/`apiDeleteVocabList`/`apiAddVocabItem`/`apiUpdateVocabItem`/`apiDeleteVocabItem` to `vocabulary.go`. `apiGetVocabList` deliberately serves both the client's View and Manage states from one response shape (`apiVocabDetail`, including the full item array with locale-joined `translation`/`notes`) — matching how `handleVocabView`/`handleVocabEditForm` already shared the same `vocabViewRow` pairing logic. Validated live, directly at the API level, the exact position-shift risk called out in the spec: created a list with two items, deleted item 0, confirmed item 1 correctly renumbered to position 0.

**Step 2 — shell.** `vocabulary-app.html` with `defer` from the start. `handleVocabularyApp` mirrors `handleTextsApp`/`handleDialogsApp`; `vocabularyAppI18nKeys` gathered from the four templates before removing them (32 keys).

**Steps 3–6 — `vocabulary-app.js`.** `showList()` includes the language-filter-from-query-string fix **from the start** (per `specs/memory.md`'s `[gotcha]`) — not discovered a third time. `showManage(id, editingPosition)` is the new structural piece: combines the list-meta form, an item add/edit form, and the items table in one client view, with `editingPosition` as pure in-page state (`null` = add mode, or the clicked row's position) replacing the old `?edit=<position>` query param. **Every item mutation (add/update/delete) re-fetches `GET /api/v1/vocabulary/{id}` and calls `showManage(id, null)`** — never patches the table in place — specifically because positions shift on delete; add/update don't actually need this, but using the identical re-fetch-and-reset path for all three keeps the code uniform rather than special-casing delete alone.

**Step 7 — cutover.** Removed all seven old HTML handlers (`handleVocabList` through `handleVocabDelete`) and the seven old `/vocabulary/*` routes (keeping `GET /vocabulary` → `handleVocabularyApp`); deleted the four old templates. One unused import (`strings`, only used by the removed `handleVocabEditForm`'s `strings.Join`) caught by the build and removed.

**Final file list**: modified `vocabulary.go`, `server.go`; new `vocabulary-app.html`, `vocabulary-app.js`; removed `vocab-{list,new,edit,view}.html`.

**Self-review** (code-review + security-review): re-verified every authorization check against its removed HTML predecessor. No new attack surface — same reasoning as the prior two SPA features.

## Validation

**Automated:** `go build/vet ./...`, `gofmt -l .` clean (one stray blank line from a `sed`-based deletion, caught by `gofmt -l` and fixed with `gofmt -w`). `task test-phraseforge-frontend` — 25/25 (unaffected; no new component needed).

**Live end-to-end validation** (deployed at every step; no template-parse panic at any point):
- JSON API exercised directly via `curl`: full list-plus-items lifecycle (create list, add two items, delete item 0, **confirmed item 1 renumbered to position 0** — the actual risk this feature was about), update item, update list meta, delete list.
- Full client lifecycle exercised via the real-page jsdom technique: New → lands on Manage (not View, matching the original redirect) → add two items → edit item 0 (confirmed prefill) → save → delete item 0 with the real `pf-dialog` confirm → **confirmed the re-rendered table shows the remaining item renumbered to position 0** → update list meta (confirmed title reflected) → back to View (confirmed its table has **no** edit/delete controls, matching the original read-only view) → delete the list → back to an empty list.
- Post-cutover: old `/vocabulary/new`, `/vocabulary/{id}` routes `404`; `/`, `/dialogs`, `/vocabulary` all `200`; `/models`, `/admin`, `/profile` unaffected; one final create/delete cycle via the API confirmed everything still works after dead-code removal.
- **Cleanup**: every test list/item created during validation was deleted; confirmed gone via follow-up queries. No residual test data.

**Known gap (consistent with every prior frontend feature)**: no real browser used — pixel-level appearance needs the user's own spot-check.

**Overall verdict**: clean, no regressions, every Acceptance Criterion validated end-to-end, including the item-position-shift risk this feature was actually most at risk on. Both previously-recorded gotchas (`defer`, language-filter query string) were correctly applied from the start — no third occurrence.

## Documentation Review

**Files audited:** `phraseforge/README.md` (no architecture-tree listing — no drift), `specs/tech-stack.md` (JSON API convention already documented generically — still accurate), `specs/roadmap.md` (unaffected), `phraseforge/CHANGELOG.md` (checked for entry mapping).

**No constitution drift found.**

**Proposed changelog entry (for B5):**

**Category: Changed**
```
- Vocabulary is now a single-page client-rendered app (list/new/view/manage, all via a new
  `/api/v1/vocabulary` JSON API, including per-item add/edit/delete) instead of four separate
  server-rendered pages, matching Texts/Dialogs; the old `/vocabulary/*` template routes are gone.
```

## Documentation Updates

Added to `phraseforge/CHANGELOG.md` under `## [Unreleased]` → `### Changed`: the entry proposed above.

Removed the `## Now` line for this feature from `specs/artifacts/phraseforge/roadmap.md` (feature complete).

No `memory.md` entries — both previously-recorded gotchas (`defer`, language-filter query string) were applied correctly from the start; nothing new surfaced.
