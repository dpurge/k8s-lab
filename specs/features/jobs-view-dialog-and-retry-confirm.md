---
title: Structured job view dialog with a Retry confirmation step
kind: feature
status: done
version: 1
updated: 2026-09-25
branch: main
---

## Problem / Motivation

The Jobs admin page's "View" action reuses the generic `pf-dialog` confirm
modal to display a job's entire record as one flat `JSON.stringify(job,
null, 2)` blob (`phraseforge/internal/server/static/js/jobs-app.js:105-116`).
That modal is capped at `max-width: 28rem`
(`phraseforge/internal/server/static/components/pf-dialog/component.css:15-25`),
which is comfortable for a short confirmation message but cramped for a
job's `payload`/`result` fields, which can themselves be non-trivial JSON.

Separately, Retry acts immediately on click with no confirmation
(`jobs-app.js:118-147`), unlike Delete, which already confirms first
(`jobs-app.js:149-172`, via `confirmDialog.confirm(...)`). Retrying a failed
job re-enqueues real work (including, for LLM-calling kinds, a real Ollama
call), so a stray click currently has no safety net.

This is presentation-layer only — no backend/API changes.

## Acceptance Criteria

- [ ] The View dialog shows a structured layout, not one flat JSON dump:
  job metadata (id/kind/priority/status/created_at/updated_at) as labeled
  fields, an "Error" block shown only when `job.error` is non-empty, and
  distinct "Payload" and "Result" sections, each independently
  pretty-printed (`JSON.stringify(..., null, 2)`); Result shows a clear
  placeholder (e.g. "—") when `null`.
- [ ] The View dialog is visibly wider than the standard confirm dialog
  (enough to read realistic payload/result JSON without excessive
  wrapping), while every other use of the shared `pf-dialog` component
  (Delete confirms, Admin page confirms, etc.) is visually unchanged.
- [ ] Clicking Retry now opens a confirmation dialog first; the retry
  request only fires on confirmation, mirroring Delete's existing
  confirm-then-act flow (including the same immediate-disable-both-buttons
  pattern, now applied after confirmation instead of before the request).
- [ ] Delete's existing behavior is unchanged.
- [ ] `go test ./...` passes in `phraseforge`; this feature makes zero LLM
  calls.

## Approach

1. `pf-dialog/component.js`: add a way to render structured HTML content
   without changing the existing `confirm(message, options)` API used by
   every other caller (Delete, Admin page confirms, etc.) — e.g. a new
   `showContent(node, options)` method that appends a DOM node (or sets
   `innerHTML` from a trusted, locally-built fragment — never
   user-controlled HTML) instead of `textContent`, plus a `wide` option
   toggling a CSS class for a larger `max-width`.
2. `pf-dialog/component.css`: add a `.pf-dialog-box.wide` rule for the
   larger width, capped so it still fits comfortably within `max-height:
   80vh` and phone-width viewports.
3. `jobs-app.js`'s View click handler: replace the
   `JSON.stringify(job, null, 2)` call with building the structured
   fragment described in the acceptance criteria, passed to the new
   `showContent`(or equivalent) call with `wide: true`.
4. `jobs-app.js`'s Retry click handler: call `confirmDialog.confirm(...)`
   first; only enter the existing disable/POST/refresh logic
   (`jobs-app.js:118-147`) on confirmation — restructured to match
   Delete's handler shape (`jobs-app.js:149-172`).
5. Add the new retry-confirm message as an i18n key (en/pl) in
   `phraseforge/internal/i18n/i18n.go`, following the existing per-section
   `jobsAppI18nKeys`/`jobsAppI18n` pattern established by
   `phraseforge-jobs-menu.md`.

**Testing:** pure frontend/component change, zero LLM calls. Covered by the
existing `pf-dialog/component.test.js`-style unit test pattern plus a
manual check in a running dev server (per this session's UI-change
guidance) — no real-call budget needed for this feature.

## Affected Areas

- `phraseforge/internal/server/static/components/pf-dialog/component.js`,
  `component.css`, `component.test.js`
- `phraseforge/internal/server/static/js/jobs-app.js`
- `phraseforge/internal/i18n/i18n.go`

## Out of Scope

- Any backend/API change — the job record's shape and the retry/delete
  endpoints themselves are unchanged.
- A dedicated full-page job view (route-based) — this stays a modal, per
  the approved direction.
- Adding a confirmation step to any dialog outside the Jobs page.

## Implementation Notes

1. `pf-dialog/component.js`: added `showContent(node, options)` alongside
   the existing `confirm()`, plus a shared `_open(options)` helper both now
   call. `node` is appended via the DOM API only — callers build it with
   `createElement`/`textContent`, never by parsing an HTML string, so
   dynamic job data can never be interpreted as markup. Added
   `options.wide` (toggles a `.wide` box class) and `options.hideCancel`
   (hides the Cancel button for a pure "nothing to confirm" view — added
   after initial review, see the fix-forward round below). Both dimensions
   of any prior manual resize are cleared on every `_open()` so reusing the
   single page-level dialog instance never leaks state between opens.
2. `pf-dialog/component.css`: added `.pf-dialog-box.wide` (wider default,
   user-resizable via `resize: both` + `overflow: auto`, height left
   `auto` so the box hugs its actual content instead of always opening at
   a fixed size) and content-block styling (`dl`/`dt`/`dd` for metadata,
   `pre` for pretty-printed JSON, using the existing `--surface-alt`
   token — no new CSS variables invented).
3. `jobs-app.js`: added `field()`/`jsonBlock()`/`buildJobView()` helpers
   that render a job's full detail (id/kind/priority/status/created_at/
   updated_at, step/error only when present, Payload/Result each
   pretty-printed) as a `DocumentFragment`; the View click handler now
   calls `confirmDialog.showContent(buildJobView(job), {okLabel:
   T("jobs.view_close"), wide: true, hideCancel: true})` instead of
   `JSON.stringify`-dumping the whole job into `confirm()`. The Retry click
   handler now calls `confirmDialog.confirm(T("jobs.retry_confirm"))`
   first and only proceeds into the existing disable/POST/refresh logic on
   confirmation, mirroring Delete's handler shape exactly.
4. Added `jobs.view_id`, `jobs.view_updated`, `jobs.view_step`,
   `jobs.view_payload`, `jobs.view_result`, `jobs.retry_confirm` to
   `phraseforge/internal/i18n/i18n.go` (en/pl) and `jobsAppI18nKeys` in
   `phraseforge/internal/server/jobs.go`; reused the existing
   `jobs.col_kind`/`col_priority`/`col_status`/`col_created`/`col_error`
   keys for the matching metadata labels rather than declaring duplicates.
5. Self-reviewed the diff — no findings; the change is confined to the
   affected files with no unrelated edits.
6. **Fix-forward round** (user feedback after an initial browser check,
   before this spec's own validation gate — not a regression from a passing
   state): the first pass's `.wide` box used a fixed `height` (leaving dead
   space below the buttons whenever content was shorter than that height)
   and `buildJobView` wrapped its content in one `<div>`, which meant
   component.css's "`> * + *`" spacing rule — targeting `.pf-dialog-content`'s
   *direct* children — never matched the nested dl/Payload/Result blocks,
   so they rendered glued together. Fixed by dropping the forced `height`
   (kept only `min-height`/`max-height`, letting the box hug its content)
   and switching `buildJobView` to return a `DocumentFragment` instead of a
   wrapping `<div>`, so its pieces land as `.pf-dialog-content`'s own direct
   children. Also added `hideCancel` at this point: the user pointed out a
   pure View screen showing both Cancel and Close was confusing when both
   already did exactly the same thing (see this repo's own investigation
   of that, from before this feature existed).

## Validation

- `go build ./...`, `go vet ./...`, `go test ./... -count=1` all pass in
  `phraseforge` — no regressions against the pre-implementation baseline.
- `npm test` (Node's built-in test runner + jsdom) in
  `phraseforge/internal/server/static/components`: 31/31 pass (27
  baseline + 4 new: `showContent()` rendering/hiding/widening,
  `confirm()` clearing stale `showContent()` state, `hideCancel`
  toggling, and manual-resize state not leaking between opens).
- `node --check` on `jobs-app.js` — no syntax errors.
- Real-cluster confirmation: built and deployed to the local k3d cluster
  (`task deploy-phraseforge`) twice (once per fix-forward round);
  confirmed via `curl` that the live pod was serving the new JS/CSS both
  times. The user checked both the View dialog (structured layout, single
  Close button, resizable, proper spacing, no dead space) and the Retry
  confirmation in their own browser and confirmed it "looks good."
- No `reviewer` invocation — not security-sensitive or architecturally
  significant (presentation-layer only, no backend/API/data changes).

## Documentation Review

Affected Areas map to the `phraseforge` artifact only (all paths are under
`phraseforge/`, per `tech-stack.md`'s Artifacts table) — no fan-out.

**Changelog entry needed** (user-facing UI change, category `Changed` —
matches this file's existing `### Changed` section style):

- `{phraseforge/CHANGELOG.md, Changed, "the Jobs page's View action now shows a resizable, structured dialog (metadata fields plus separate Payload/Result blocks) instead of one raw JSON dump, and Retry now asks for confirmation first, matching Delete."}`

**Other drift:** none found. `tech-stack.md`'s Frontend components
convention (light-DOM Web Components, self-injecting style, per-app
duplication) already describes exactly the pattern this feature extends,
not one it changes — no update needed there. No README/mission/roadmap
drift beyond the `## Now`→changelog handoff below.

## Documentation Updates

- `phraseforge/CHANGELOG.md`: added a `### Changed` entry under
  `## [Unreleased]` per the mapping above.
- Removed this feature's `## Now` line from
  `specs/artifacts/phraseforge/roadmap.md` — it's in the changelog now,
  not in flight.
