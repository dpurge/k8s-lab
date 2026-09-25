---
title: Port Dialogs to phraseforge's SPA shell via a JSON API
kind: feature
status: done
version: 1
updated: 2026-09-24
branch: main
---

## Problem / Motivation

Second SPA item, mirroring `phraseforge-spa-shell-texts`'s now-proven pattern exactly: a new `/api/v1/dialogs` JSON API (near-literal transcription of `dialogs.go`'s five HTML handlers), a `dialogs-app.html` shell (extends `layout.html`, empty container + bootstrap JSON + deferred script — `defer` applied from the start this time, per `specs/memory.md`'s `[gotcha]`/`[convention]` entries recorded after the Texts feature shipped with the bug and needed a follow-up fix), and `dialogs-app.js` client rendering (list/new/view/edit/delete, reusing `pf-card`/`pf-field`/`pf-button`/`pf-dialog`/`pf-status-bar`).

**The one real difference from Texts, and the actual risk in this feature**: dialogs' body isn't rendered as-is — `wrapDialogBody` (`dialogs.go`) wraps it with `{start-dialog lang=... script=...}`/`{end-dialog}` markers (or leaves an already-explicit `{start-dialog...}` body untouched) before `texts.RenderHTML` sees it, and normalizes CRLF and a specific blank-line-before-header edge case that would otherwise produce a spurious empty turn. **This must be called from the JSON API's create/update/get handlers in exactly the same way**, or dialog rendering silently regresses (turns render as plain paragraphs instead of a real dialog, or a spurious empty turn appears). Separately, dialog rendering can fail *without* being a server error — malformed turn syntax produces a real `error` from `texts.RenderHTML`, shown today as an inline notice (`BodyError`/`TranscriptionError`/`TranslationError`), not a 500. The JSON API must return these as data (e.g. `bodyError`/`transcriptionError`/`translationError` string fields alongside a normal `200`), not as an HTTP error — a render failure on one field is not a failure of the whole request.

## Acceptance Criteria

- `/api/v1/dialogs` (`GET` list, `POST` create, `GET one`, `PUT` update, `DELETE`) — same shape, same auth/error conventions as `/api/v1/texts`. `apiDialogRequest`'s `body`/`transcription` fields pass through `wrapDialogBody` exactly where `handleDialogCreate`/`handleDialogUpdate`/`handleDialogView` did — the create/update handlers wrap before calling `s.dialogs.Create`/`Update` **only where the original did** (checked: `handleDialogCreate`/`handleDialogUpdate` do *not* call `wrapDialogBody` before storing — only `handleDialogView` wraps *at render time*, storing the raw author-typed body as-is). The new `apiGetDialog` reproduces this exactly: store the raw body, wrap only when rendering.
- `apiGetDialog`'s response includes `renderedBody`/`bodyError` (mutually meaningful — a real render error still returns `200` with `bodyError` set and `renderedBody` empty, exactly matching `dialogs-view.html`'s `{{if .BodyError}}...{{else}}...{{end}}` branch), and the same pair for transcription/translation, each independently.
- `dialogs-app.html`'s script tag has `defer` from the start (per the recorded gotcha — no repeat of the Texts regression).
- `dialogs-app.js` mirrors `texts-app.js`'s structure (list/showForm/showView, `pf-dialog`-based delete) with dialog-specific i18n keys and field hints (`dialogs.field_body_hint` instead of `texts.field_body`, etc.) and renders `bodyError`/`transcriptionError`/`translationError` as the same inline `<p class="empty-state">` notice `dialogs-view.html` used, instead of the rendered `<article>`, per field independently.
- `GET /dialogs` (root of this resource) serves the shell; `GET /dialogs/new`, `GET /dialogs/{id}`, `GET /dialogs/{id}/edit`, `POST /dialogs`, `POST /dialogs/{id}/edit`, `POST /dialogs/{id}/delete` and their handlers (`handleDialogList`, `handleDialogNewForm`, `handleDialogCreate`, `handleDialogView`, `handleDialogEditForm`, `handleDialogUpdate`, `handleDialogDelete`) are removed once the SPA replacement is validated; `dialogs-list.html`/`dialogs-new.html`/`dialogs-edit.html`/`dialogs-view.html` deleted. `wrapDialogBody`/`stripBlankBeforeDialogHeader`/`isDialogHeaderLine`/`renderErrString` are kept (moved to serve the new JSON handlers).
- No change to `Texts`, `Vocabulary`, `Models`, `Admin`, `Profile`, or any `pf-*` component's own files.
- Live end-to-end validation must specifically exercise: a real multi-turn dialog body (`--:`/`@Name:` turns) rendering correctly through the new API; a deliberately malformed body producing a real `bodyError` instead of a 500; the full New → View → Edit → Delete cycle, same rigor as the Texts feature (including the corrected "real page, real script order" jsdom technique, not the flawed synthetic-harness one from earlier in that feature).

## Approach

Directly repeats `phraseforge-spa-shell-texts`'s ordered plan (JSON API → shell+list → new/edit → view → delete → cutover → validate), with `defer` applied from step 1 of the shell instead of discovered as a bug afterward. The `wrapDialogBody` family of functions stays exactly where it is (`dialogs.go`) and is called from the new JSON handlers at the same two points the old HTML handlers called it (store raw, wrap-at-render) — no behavior change to the wrapping/error logic itself, only who calls it.

## Affected Areas

- `phraseforge/internal/server/dialogs.go` (new `apiListDialogs`/`apiCreateDialog`/`apiGetDialog`/`apiUpdateDialog`/`apiDeleteDialog`; removal of the five old HTML handlers)
- `phraseforge/internal/server/server.go` (new `/api/v1/dialogs` route group; `GET /dialogs` → new `handleDialogsApp`; removal of the six old `/dialogs/*` routes)
- new `phraseforge/internal/server/templates/dialogs-app.html`
- new `phraseforge/internal/server/static/js/dialogs-app.js`
- removed: `phraseforge/internal/server/templates/{dialogs-list,dialogs-new,dialogs-edit,dialogs-view}.html`
- `specs/artifacts/phraseforge/roadmap.md` (`## Later` → `## Now` at B2 start)
- `phraseforge/CHANGELOG.md` (entry at B5)

## Out of Scope

- Vocabulary, Models, Admin, Profile — separate roadmap items.
- Any change to the dialog turn-syntax rules themselves (`wrapDialogBody`, `stripBlankBeforeDialogHeader`, `isDialogHeaderLine`) — ported as-is, not revisited.
- Any change to `pf-*` components.

## Implementation Notes

**Step 1 — JSON API.** Added `apiListDialogs`/`apiCreateDialog`/`apiGetDialog`/`apiUpdateDialog`/`apiDeleteDialog` to `dialogs.go`, near-literal transcriptions of the old HTML handlers. Confirmed live that `wrapDialogBody` is called at exactly the same point the original did (render-time only, in `apiGetDialog` — create/update store the raw body, matching `handleDialogCreate`/`handleDialogUpdate`'s original behavior exactly). Confirmed the render-error path returns `200` with `bodyError` set (not a 500) by deliberately testing a malformed body first.

**Step 2 — shell.** `dialogs-app.html` with `defer` on its script tag **from the start** (per the memory entries from the Texts feature's regression) — no repeat of that bug. `handleDialogsApp` mirrors `handleTextsApp` exactly; `dialogsAppI18nKeys` gathered from grepping the four templates before removing them (26 keys, including `dialogs.render_error_prefix` for the error-notice path).

**Steps 3–6 — `dialogs-app.js`.** Mirrors `texts-app.js` structure exactly, with one addition: a `renderedOrError(html, errorText, extraClass)` helper choosing between the rendered `<article>` and the same inline `<p class="empty-state">` notice `dialogs-view.html` used, applied independently to body/transcription/translation.

**Step 7 — cutover.** Removed `handleDialogList`/`handleDialogNewForm`/`handleDialogCreate`/`handleDialogView`/`handleDialogEditForm`/`handleDialogUpdate`/`handleDialogDelete` and the six old `/dialogs/*` routes (keeping `GET /dialogs` → `handleDialogsApp`); deleted the four old templates. `wrapDialogBody`/`stripBlankBeforeDialogHeader`/`isDialogHeaderLine`/`renderErrString` kept, now serving only the JSON handlers.

**No repeat of the Texts feature's regression** — `defer` was applied to `dialogs-app.html`'s script tag from its first version, and validated using the corrected "real page, real script order" jsdom technique (not the flawed synthetic-harness one) from the start of this feature's own validation, not discovered as a bug afterward.

**Final file list**: modified `dialogs.go`, `server.go`; new `dialogs-app.html`, `dialogs-app.js`; removed `dialogs-{list,new,edit,view}.html`.

**Self-review** (code-review + security-review): re-verified every authorization check against its removed HTML predecessor before deleting it. No new attack surface — same reasoning as the Texts feature (JSON in/out at the same trust level, `template.HTML` fields unchanged).

## Validation

**Automated:** `go build/vet ./...`, `gofmt -l .` clean. `task test-phraseforge-frontend` — 25/25 (unaffected; no new component was needed, all reused from prior features).

**Live end-to-end validation** (deployed at every step; no template-parse panic at any point):
- JSON API exercised directly via `curl`: list, create (with correct multi-turn syntax, confirmed real `dialog-item`/`dialog-header`/`dialog-content` HTML), create with deliberately malformed syntax (confirmed `200` + `bodyError`, not a 500), update, delete.
- Full client lifecycle exercised via the corrected jsdom technique against the real deployed page: New → View (real dialog markup rendered) → Edit (prefill confirmed byte-for-byte) → deliberately broke the syntax on save → View correctly showed the inline error notice instead of the article → Delete (real `pf-dialog` confirm, correctly localized into Polish, matching the admin account's actual locale — incidental but strong confirmation the i18n bootstrap dict works) → back to an empty list.
- Post-cutover: old `/dialogs/new`, `/dialogs/{id}` routes `404`; `/`, `/dialogs` both `200`; `/vocabulary`, `/models`, `/admin`, `/profile` unaffected; one final create/delete cycle via the API confirmed everything still works after dead-code removal.
- **Cleanup**: every test dialog created during validation was deleted; confirmed gone via follow-up queries. No residual test data.

**Known gap (consistent with every prior frontend feature)**: no real browser used — pixel-level appearance needs the user's own spot-check.

**Real regression found by the user after this feature shipped, shared with `phraseforge-spa-shell-texts`**: `dialogs-app.js`'s `showList()` had the identical bug as `texts-app.js` — the sidebar's `#language-filter` navigates via `?language=...`, but neither script ever read that query parameter, so the language filter silently did nothing on either page. Fixed identically in both files (read `new URLSearchParams(window.location.search).get("language")` fresh on every `showList()` call); see `phraseforge-spa-shell-texts`'s Validation section for the concrete before/after test against real data. Recorded as a `[gotcha]` in `specs/memory.md` for every remaining SPA list view — this is now a required item to get right from the start, not discover twice more.

**Overall verdict**: clean, no regressions, every Acceptance Criterion validated end-to-end, including the dialog-specific render-error path this feature was actually most at risk on. One shared regression (the language filter) reached the user across both shipped SPA features before being caught; fixed in both, and now durable for the rest.

## Documentation Review

**Files audited:** `phraseforge/README.md` (no architecture-tree listing — no drift), `specs/tech-stack.md` (JSON API convention already documented generically from the Texts feature, describes the pattern not per-resource specifics — still accurate, no update needed), `specs/roadmap.md` (unaffected), `phraseforge/CHANGELOG.md` (checked for entry mapping).

**No constitution drift found.**

**Proposed changelog entry (for B5):**

**Category: Changed**
```
- Dialogs is now a single-page client-rendered app (list/new/view/edit, all via a new
  `/api/v1/dialogs` JSON API) instead of four separate server-rendered pages, matching Texts;
  the old `/dialogs/*` template routes are gone.
```

## Documentation Updates

Added to `phraseforge/CHANGELOG.md` under `## [Unreleased]` → `### Changed`: the entry proposed above.

Removed the `## Now` line for this feature from `specs/artifacts/phraseforge/roadmap.md` (feature complete).

No `memory.md` entries — nothing new surfaced beyond what's already recorded from the Texts feature.
