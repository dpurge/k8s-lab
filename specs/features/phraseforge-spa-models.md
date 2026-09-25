---
title: Port Models to phraseforge's SPA shell via a JSON API
kind: feature
status: done
version: 1
updated: 2026-09-24
branch: main
---

## Problem / Motivation

Fourth SPA item, and structurally the simplest of the list-plus-items ports: Models is
**identical in shape to Vocabulary** (a list with title/language/script, plus a sequence of
items, each with a common `phrase`/`transcription` pair and a per-locale `translation`) minus
Vocabulary's extra `grammar`/`notes` fields. `models-edit.html` combines the same three things
`vocab-edit.html` did — list-meta edit form, an item add/edit form (add by default, or edit one
item named by `?edit=<position>`), and a read-only items table with per-row Edit/Delete — so this
feature reuses `phraseforge-spa-vocabulary`'s exact design, minus the two extra item fields.

**The same risk applies**: `models.DeleteItem`'s doc comment confirms it renumbers later items
(and their translations, in every locale) after a delete, exactly like `vocabulary.DeleteItem` —
the client's item table must always re-fetch after any item mutation rather than patch positions
locally.

## Acceptance Criteria

- `/api/v1/models` — `GET` (list, with `itemCount` per list, matching `handleModelsList`'s
  `ItemCounts` exactly), `POST` (create), `GET one` (list detail **including its full item
  array**, each item carrying its locale-specific `translation` — reproduces
  `handleModelsEditForm`'s `modelsViewRow` pairing, since this one response serves both the
  client's View and Manage states), `PUT` (update list meta), `DELETE`.
- `/api/v1/models/{id}/items` — `POST` (add item, returns `{position}`);
  `/api/v1/models/{id}/items/{position}` — `PUT` (update), `DELETE`. Each preserves `models.go`'s
  exact logic (the same `CanEdit` check via the list's own language, the same
  `AddItem`/`SetTranslation`/`UpdateItem`/`DeleteItem` calls in the same order, the same
  `ErrItemNotFound` → 404 mapping).
- `models-app.js`'s `showManage(id)` renders the list-meta form, an item add/edit form (add by
  default; clicking a row's "Edit" sets **in-page state**, no URL change, and re-renders the item
  form prefilled — replacing the old `?edit=<position>` query param), and the items table with
  per-row Edit/Delete (`pf-dialog`-confirmed, fetch-based, matching Texts/Dialogs/Vocabulary's
  delete pattern). **After every item add/update/delete, the whole manage view re-fetches
  `GET /api/v1/models/{id}` and re-renders the table from scratch.**
- `showView(id)` (read-only: list meta, Copy/Edit/Delete, items table without edit/delete
  controls) and `showNew()` (create form; on success, goes to `showManage(id)` — matching
  `handleModelsCreate`'s original redirect to the edit page since a new list starts empty).
- `showList()`'s language-filter pattern (read `window.location.search` fresh, per
  `specs/memory.md`'s `[gotcha]`) is applied **from the start**.
- `defer` on `models-app.html`'s script tag **from the start**.
- `GET /models` serves the shell; the ten old `/models/*` routes and their handlers, plus
  `models-list.html`/`models-new.html`/`models-edit.html`/`models-view.html`, are removed once
  validated.
- No change to Texts, Dialogs, Vocabulary, Admin, Profile, or any `pf-*` component.
- Live validation must specifically exercise: creating a list, adding two items, editing the
  first one (confirm prefill), deleting it, confirming the second item's position correctly
  shifted to `0` afterward, then deleting the whole list.

## Approach

Same ordered plan as the prior three SPA features (API → shell+list → new → view → manage →
delete → cutover → validate), with both already-recorded gotchas applied from the start. The
item shape is `{phrase, transcription, translation}` — no `grammar`/`notes` — otherwise the
design (including `showManage`'s in-page "which item, if any, is being edited" state) is a direct
copy of `phraseforge-spa-vocabulary`'s.

## Affected Areas

- `phraseforge/internal/server/models.go` (new `apiListModelsLists`/`apiCreateModelsList`/
  `apiGetModelsList`/`apiUpdateModelsList`/`apiDeleteModelsList`/`apiAddModelsItem`/
  `apiUpdateModelsItem`/`apiDeleteModelsItem`; removal of the ten old HTML handlers)
- `phraseforge/internal/server/server.go` (new `/api/v1/models` route group; `GET /models` → new
  `handleModelsApp`; removal of the ten old `/models/*` routes; removal of `models-*.html` from
  the `init()` pages list)
- new `phraseforge/internal/server/templates/models-app.html`
- new `phraseforge/internal/server/static/js/models-app.js`
- removed: `phraseforge/internal/server/templates/{models-list,models-new,models-edit,models-view}.html`
- `specs/artifacts/phraseforge/roadmap.md` (`## Later` → `## Now` at B2 start)
- `phraseforge/CHANGELOG.md` (entry at B5)

## Out of Scope

- Admin, Profile, the unified-shell item, the pagination item — separate roadmap entries.
- Any change to the models data model itself (no new fields, no validation changes).
- Any change to `pf-*` components.

## Implementation Notes

**Step 1 — JSON API.** Added `apiListModelsLists`/`apiCreateModelsList`/`apiGetModelsList`/
`apiUpdateModelsList`/`apiDeleteModelsList`/`apiAddModelsItem`/`apiUpdateModelsItem`/
`apiDeleteModelsItem` to `models.go`, replacing the ten old HTML handlers. `apiGetModelsList`
serves both the client's View and Manage states from one `apiModelsDetail` response, matching
`handleModelsView`/`handleModelsEditForm`'s shared `modelsViewRow` pairing logic exactly.
`modelsViewRow`/`modelsMarkdown` (in `markdown.go`) are unchanged and reused as-is.

**Step 2 — shell.** `models-app.html` with `defer` from the start; `handleModelsApp` mirrors
`handleVocabularyApp` exactly; `modelsAppI18nKeys` gathered from the four templates before
removing them (29 keys, no `grammar`/`notes`-related keys since Models never had them).

**Steps 3–6 — `models-app.js`.** Direct copy of `vocabulary-app.js`'s structure
(`showList`/`showNew`/`showView`/`showManage`) with `grammar`/`notes` removed from the item
payload, the item form, and the items table — everything else (language-filter-from-query-string
fix, `editingPosition` in-page state, uniform re-fetch-after-every-mutation) applied identically
and from the start.

**Step 7 — cutover.** Removed all ten old HTML handlers and the ten old `/models/*` routes
(keeping `GET /models` → `handleModelsApp`), removed `models-list.html`/`models-new.html`/
`models-edit.html`/`models-view.html` from the `init()` pages list and deleted the four files
(`git rm`), added the `/api/v1/models` route group. `strings` import in `models.go` was already
gone from the old handlers (only `handleModelsEditForm`'s `strings.Join` used it, removed with
that handler) — no dangling import this time (unlike Vocabulary's cutover).

**Final file list**: modified `models.go`, `server.go`; new `models-app.html`, `models-app.js`;
removed `models-{list,new,edit,view}.html`.

**Self-review**: re-verified every authorization check (`CanView`/`CanEdit`, same order, same
404-vs-403 mapping) against its removed HTML predecessor. No new attack surface.

## Validation

**Automated:** `go build/vet ./...`, `gofmt -l .` clean. `task test-phraseforge-frontend` —
25/25 (unaffected; no new component needed).

**Live deployment validation** (`task deploy-phraseforge`, unauthenticated route-level checks
only — see gap below):
- `GET /`, `GET /models` → `302` (redirect to `/login`, expected — behind `requireAuth`, same as
  every other resource shell).
- `GET /models/new`, `GET /models/1`, `GET /models/1/edit` → `404` — confirms the ten old
  `/models/*` routes are actually gone, not just unreachable by accident.
- `GET /static/js/models-app.js` → `200`, and its body contains the expected
  `__PF_MODELS_BOOTSTRAP__`/shell markers — confirms the new client script is deployed correctly.
- `GET /api/v1/models` → `302` (same auth gate as the HTML routes, as expected).

**Known gap — functional/authenticated validation not performed by me.** Every prior SPA feature
(Texts, Dialogs, Vocabulary) was validated end-to-end via authenticated `curl` and a real-page
jsdom technique using a real session cookie. For this feature, obtaining that session (via the
app's own hardcoded bootstrap login, or via a fresh test signup) required running a `curl`/`fetch`
command carrying a `password` field, which Claude Code's sandbox safety classifier blocked
outright as "credential exploration" — including the fresh-signup path, since signup also carries
a password. The user chose to skip authenticated automated validation for this feature rather than
override the sandbox, and instead manually walked through the golden path in a browser: created a
list, added two items, edited the first (prefill confirmed), deleted it, confirmed the second item
correctly renumbered to position 0, then deleted the list. **User confirmed: "It all worked."**

**Overall verdict**: build/format/route-level checks clean by me; the functional golden path
(including the item-position-shift-on-delete risk this feature was actually most at risk on) was
confirmed working by the user's own manual test.

## Documentation Review

**Files audited:** `phraseforge/README.md` (no architecture-tree listing — no drift),
`specs/tech-stack.md` (JSON API convention already documented generically — still accurate),
`phraseforge/CHANGELOG.md` (checked for entry mapping).

**No constitution drift found.**

**Proposed changelog entry (for B5):**

**Category: Changed**
```
- Models is now a single-page client-rendered app (list/new/view/manage, all via a new
  `/api/v1/models` JSON API, including per-item add/edit/delete) instead of four separate
  server-rendered pages, matching Texts/Dialogs/Vocabulary; the old `/models/*` template routes
  are gone.
```

## Documentation Updates

Added to `phraseforge/CHANGELOG.md` under `## [Unreleased]` → `### Changed`: the entry proposed
above. Removed the `## Now` line for this feature from `specs/artifacts/phraseforge/roadmap.md`
(feature complete).

No `memory.md` entries — both previously-recorded gotchas (`defer`, language-filter query string)
were applied correctly from the start; nothing new surfaced from this port itself. (The
sandbox's blocking of password-bearing `curl`/`fetch` commands is a session-tooling constraint,
not a portable repo/project fact, so it isn't recorded in `memory.md`.)
