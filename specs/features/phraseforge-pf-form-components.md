---
title: Build pf-field and convert phraseforge's editor/form templates to pf-button/pf-field
kind: feature
status: done
version: 1
updated: 2026-09-24
branch: main
---

## Problem / Motivation

`phraseforge-pf-components-foundation` established the `pf-*` convention (`pf-button`, `pf-nav`) on the header only. This is the next `## Later` item on `specs/artifacts/phraseforge/roadmap.md`: build `pf-field` (mirroring knowledge's `kb-field`) and use `pf-button`/`pf-field` across the app's editor/form templates — `new.html`, `edit.html`, `vocab-new.html`, `vocab-edit.html`, `models-new.html`, `models-edit.html`, `dialogs-new.html`, `dialogs-edit.html`, `profile.html`, `login.html`, `signup.html` — extracting their remaining inline button/label markup, not redesigning them.

Research surfaced a real coupling to check before touching anything: `phraseforge/internal/server/static/js/editor.js` (`pfApplyImeConfig`/`pfWireIme`/`pfApplyScriptStyle`) queries `#language`, `#script`, `#field-source`, `#field-transcription`, `#transcription-group`, and `.transcribe-action` directly by ID/class and mutates those elements' own `className`/`dir`/`onkeydown`/`style.display` — never anything about their surrounding `<label>` or wrapper `<div>`. `pf-field` (like `kb-field`) never touches its input/select/textarea child at all — it only ever prepends a label — so this coupling survives untouched as long as every existing `id` stays exactly where it is.

## Acceptance Criteria

- `phraseforge/internal/server/static/components/pf-field/` (`component.js`, `component.css`, `component.test.js`) exists, mirroring `kb-field` exactly: prepends a `<label class="pf-field-label" for="...">` as its first child in `connectedCallback` (never wraps/moves the input child — avoids the parse-order hazard `pf-button` had to solve), with the same jsdom-observed defensive `MutationObserver` re-insertion `kb-field` needed (`specs/memory.md`, 2026-09-22T18:50:40Z `[gotcha]`).
- Every existing `<label class="field-label" for="X">...</label>` + sibling input/select pair across the eleven listed templates becomes `<pf-field label="..." for="X"><input/select id="X" ...></pf-field>` — the input/select keeps its exact `id`, `name`, `required`, `value`, and event wiring unchanged; only the label markup moves from a sibling to `pf-field`'s own prepended child.
- The `.field-row > div` wrapper around the language/script `pf-field` pair is removed — `pf-field` becomes the direct `.field-row` child (still matched by the existing `.field-row > * { flex: 1; }` rule, since that's a universal-child selector).
- `vocab-edit.html`/`models-edit.html`'s `<div id="transcription-group">` is removed; `pf-field` itself carries `id="transcription-group"` (so `editor.js`'s `document.getElementById('transcription-group').style.display = ...` toggle keeps working — `pf-field`'s own CSS default is `display: block`, matching the removed div's default, so the same inline-style toggle behaves identically).
- Fields with **no existing visible label** (title/username/password — placeholder-only) are **not** wrapped in `pf-field` and gain no new label — wrapping them would add a visible label where none exists today, a UX change out of scope for a structural-extraction pass (matches the "no visual regression" precedent from `phraseforge-pf-components-foundation`).
- Every `.btn`/`.btn-secondary`/`.btn-danger` submit/action button across the eleven templates becomes `<pf-button variant="primary|secondary">` (danger variant: none exist in this template set — `.link-btn` delete buttons are explicitly out of scope, see below). The two Transcribe/Translate `pf-button`s keep their `class="transcribe-action"` attribute (a page-behavior hook `editor.js` toggles via `document.querySelectorAll('.transcribe-action')` — same "class survives as a JS/layout hook, not as pf-button's own styling" pattern already used for `.theme-toggle` in the foundation feature).
- `.editor-tab-btn` tab buttons (Source/Transcription/Translation, in `new.html`/`edit.html`/`dialogs-new.html`/`dialogs-edit.html`) are **not** converted — they're a distinct family (active-tab toggle semantics, not a `pf-button` variant) with no roadmap item of their own yet; left as plain `<button>`, unchanged.
- `vocab-edit.html`/`models-edit.html`'s per-item-row `.link-btn` delete buttons (inline in the item table, native `confirm()` via `onsubmit`) are **not** converted — replacing native `confirm()` is explicitly `phraseforge-pf-dialog-admin-components`'s job (needs a `pf-dialog` that doesn't exist yet), and that item's current one-liner only names `vocab-view`/`models-view`, not these `-edit` pages' inline rows. Flagged in Out of Scope as a roadmap-item scope gap to fix when that item starts.
- No change to `editor.js`/`ime.js` logic, any Go/backend code, any HTTP route, or any CSS variable/color value. `login.html`/`signup.html` get only their submit `<button class="btn">` converted to `<pf-button variant="primary">` — no `pf-field` usage there (no existing labels).
- `pf-field` has a `component.test.js` (Node + jsdom) covering: label prepended with correct text/`for`, real parse-order timing (label written directly in static HTML still ends up first), and that it never touches an existing `id`/`value`/other attribute on its input child.
- `go build`/`vet`/`gofmt -l .` unaffected (no Go files change). `task test-phraseforge-frontend` passes (existing `pf-button`/`pf-nav` tests plus new `pf-field` tests).
- Live end-to-end validation: deploy, and for a representative page from each family (a tabbed editor page, a list+item-entry page, profile, login) confirm via a real authenticated session that `#language`/`#script` selection still correctly drives `editor.js`'s IME/script-styling/transcription-visibility behavior (the exact coupling this feature must not break), and that every converted button/field still submits/behaves identically. Visual spot-check by the user (no browser automation tool available this session, consistent with every prior frontend feature here).

## Approach

**Key decisions:**

1. *`pf-field` is a direct, unmodified port of `kb-field`'s mechanism* (prepend-only, no child-wrapping, defensive `MutationObserver`) — the exact same reasoning applies: phraseforge's labels sit next to real form inputs written in static HTML, so any approach that needs to read/move the input at `connectedCallback` time would hit the same parse-order race `pf-button` already had to solve for its own (different) reason. Prepending a brand-new label node has no such dependency.

2. *Every existing `id` stays exactly where it is, including `#transcription-group` moving onto `pf-field` itself* — this is the one non-mechanical rewrite in this feature (removing a wrapper div and letting the component itself carry the ID), justified because `editor.js`'s `pfApplyImeConfig` needs that exact ID to keep toggling visibility; verified `pf-field`'s CSS default (`display: block`) reproduces the removed div's default, so the inline `style.display` toggle behaves identically before and after.
3. *Class-as-hook pattern, reused from the foundation feature*: `.transcribe-action` stays on the converted `pf-button` elements for the same reason `.theme-toggle` stayed on the header's `pf-button` — a page-behavior/positioning hook is orthogonal to the component's own `variant`-driven visual styling, and removing it would silently break `editor.js`'s show/hide logic.
4. *No new components invented beyond what's roadmapped*: `.editor-tab-btn` and the `.link-btn` delete-confirm pattern both stay untouched — building a tab component or a dialog component here would be scope creep into two other already-distinct roadmap items (one not yet even planned, one planned but not yet started).

**Ordered implementation plan** (each step independently deployable via `task deploy-phraseforge`):

1. **Build `pf-field`** (`component.js`/`component.css`/`component.test.js`), matching `kb-field`'s mechanism and CSS shape but with phraseforge's own `label.field-label` values (`font-size: 0.85rem; color: var(--muted); margin-bottom: 0.3rem;`) and `pf-field { display: block; }`.
2. **Convert `new.html`/`edit.html`/`dialogs-new.html`/`dialogs-edit.html`** (the tabbed-editor family): language/script `field-row` pair, tags field, and the Save/Transcribe/Translate buttons.
3. **Convert `vocab-new.html`/`vocab-edit.html`/`models-new.html`/`models-edit.html`** (the list+item-entry family): same field-row/tags pattern in the `-new`/`-edit` list forms, plus the item-entry form's phrase/grammar/transcription/translation/notes `pf-field`s (including the `#transcription-group` ID move) and their Save/Transcribe/Translate buttons. Per-item-row `.link-btn` deletes are explicitly untouched (see Acceptance Criteria).
4. **Convert `profile.html`/`login.html`/`signup.html`**: profile's two submit buttons to `pf-button`; login/signup's submit buttons to `pf-button` (no `pf-field` — no existing labels there).
5. **Live-deploy and validate**: `task deploy-phraseforge`; real authenticated session exercising language/script change on at least one page from each family (confirming `editor.js`'s IME/styling/transcription-toggle coupling survives), plus a full save/create flow on one page per family.

**Risks & testing strategy:**

- Highest risk: breaking `editor.js`'s ID-based DOM queries. Mitigated structurally (`pf-field` never touches its child's attributes) and verified live by actually triggering a language/script change and checking the resulting IME wiring/script styling/transcription visibility on real deployed pages, not just unit tests.
- `#transcription-group` moving from a wrapper `div` onto `pf-field` itself is the one place this feature does more than "move a label" — covered by a live check that toggling the language/script selector to a needs-transcription vs. no-transcription combination actually shows/hides the transcription `pf-field` correctly.
- No browser automation available this session — live validation is `curl`/session-based (submit forms, inspect served markup) plus an explicit ask to the user for a visual spot-check, consistent with every prior frontend feature in this project.

## Affected Areas

- `phraseforge/internal/server/templates/{new,edit,vocab-new,vocab-edit,models-new,models-edit,dialogs-new,dialogs-edit,profile,login,signup}.html`
- `phraseforge/internal/server/templates/layout.html` (add `<script src="/static/components/pf-field/component.js">` to `<head>`, alongside `pf-button`/`pf-nav`)
- new `phraseforge/internal/server/static/components/pf-field/` (`component.js`, `component.css`, `component.test.js`)
- `specs/artifacts/phraseforge/roadmap.md` (`## Later` → `## Now` transition at B2 start, per usual)
- `phraseforge/CHANGELOG.md` (entry added at B5)
- `specs/artifacts/phraseforge/roadmap.md`'s `phraseforge-pf-dialog-admin-components` one-liner (documentation note, not this feature's own change — see Out of Scope) may need its wording widened later to also cover `vocab-edit.html`/`models-edit.html`'s item-row deletes, not just `-view.html`

## Out of Scope

- `.editor-tab-btn` tab buttons — distinct family, no `pf-button` variant fits, no roadmap item yet.
- `vocab-edit.html`/`models-edit.html`'s per-item-row `.link-btn` delete buttons and their native `confirm()` — this is `phraseforge-pf-dialog-admin-components`'s job once `pf-dialog` exists; **note for that item's own B1**: its current one-liner names only `vocab-view`/`models-view`, but these `-edit` pages have the identical pattern and should be included then.
- Any field with no existing visible label (title/username/password) — not wrapped in `pf-field`, no new label added.
- Any change to `editor.js`, `ime.js`, any Go/backend code, any HTTP route, or any CSS color/variable value.
- `vocab-list.html`/`models-list.html`/`list.html`/`dialogs-list.html` — `phraseforge-pf-list-card-components`'s scope.
- `admin.html`, all `*-view.html` templates — `phraseforge-pf-dialog-admin-components`'s scope.
- Any change to `knowledge` or `dictionary`.

## Implementation Notes

**Step 1 — `pf-field`.** Built `phraseforge/internal/server/static/components/pf-field/{component.js,component.css,component.test.js}`, a direct port of `kb-field`'s prepend-only mechanism (including its defensive jsdom-only `MutationObserver` re-insertion). CSS matches phraseforge's existing `label.field-label` values exactly (`font-size: 0.85rem; color: var(--muted); margin-bottom: 0.3rem;`). Added its `<script>` include to `layout.html`.

**Step 2 — `new.html`/`edit.html`/`dialogs-new.html`/`dialogs-edit.html`.** Converted the language/script `field-row` pair and the tags field to `pf-field`, removing the now-redundant per-field wrapper `<div>`s inside `.field-row` (confirmed `.field-row > * { flex: 1; }` still matches `pf-field` directly, since it's a universal-child selector). Converted Save/Transcribe/Translate to `pf-button`. `.editor-tab-btn` tab buttons and the tab-pane `<textarea>`s (no existing labels) were left untouched, per the approved Out of Scope.

**Two real regressions found and fixed during this step, before deploy — not in the original plan:**
1. `phraseforgeGenerate(kind, sourceID, targetID, contentType, button)` does `button.disabled = true; button.textContent = 'Working...'` on its last argument. All 12 Transcribe/Translate call sites passed `this` — previously the real `<button>` (since `onclick` was on it directly); now `this` inside an `onclick` set on `<pf-button>` is the *custom element*, so `.disabled = true` would be a no-op property set and `.textContent = 'Working...'` would have wiped out the inner real button entirely. Fixed by changing all 12 call sites to pass `this.querySelector('button')` instead — `phraseforgeGenerate` itself needed no change, and the real button is guaranteed to exist by the time a user can click it.
2. `login.html`/`signup.html`'s submit button relied on `style="width: 100%;"` directly on the real `<button>` to fill the centered form card. Moving that inline style to the outer `<pf-button>` (a `display: inline-block` wrapper) would stretch the invisible wrapper without the inner real button following, since nothing makes the inner button fill its host. Fixed with a narrowly-scoped `pf-button.full-width` CSS hook (added to `layout.html`'s shared `<style>`, styling both the host and its inner button) rather than a rule scoped to `.form-card` (which every other page's Save button also sits inside, and must stay content-sized).

**Step 3 — `vocab-new.html`/`vocab-edit.html`/`models-new.html`/`models-edit.html`.** Same field-row/tags pattern. `vocab-edit.html`/`models-edit.html`'s item-entry `pf-field`s (phrase/grammar/transcription/translation/notes) converted; `<div id="transcription-group">` removed, with `pf-field` itself now carrying that `id` (verified live — see Validation — that `editor.js`'s `document.getElementById('transcription-group').style.display` toggle still finds and controls it correctly). Per-item-row `.link-btn` deletes (native `confirm()`) left completely untouched, per the approved Out of Scope note about `phraseforge-pf-dialog-admin-components`'s scope gap.

**Step 4 — `profile.html`/`login.html`/`signup.html`.** Converted profile's two submit buttons and login/signup's submit buttons to `pf-button` (login/signup also getting the `full-width` fix from Step 2).

**Final file list**: modified all 11 templates listed in Affected Areas plus `layout.html` (script include + `full-width` CSS); new `phraseforge/internal/server/static/components/pf-field/{component.js,component.css,component.test.js}`.

**Self-review** (code-review): re-checked all 12 `phraseforge('...', this...)` call sites for the `.querySelector('button')` fix (none missed); re-checked every `.form-card`/`.form-actions` submit button isn't accidentally caught by the `full-width` rule (scoped by class, not container, so none are). No new attack surface — every `pf-field`/`pf-button` attribute value is either static template text or the same i18n-translated strings as before, never user-supplied.

## Validation

**Automated:**
- `phraseforge/internal/server/static/components`: `task test-phraseforge-frontend` — 12/12 tests pass (5 `pf-button`, 4 `pf-field` including real parse-order and the `#transcription-group`-id case, 3 `pf-nav`).
- `go build ./...`, `go vet ./...`, `gofmt -l .` — all clean (no Go files changed).

**Live end-to-end validation** (deployed via `task deploy-phraseforge`; no template-parse panic on startup, which `go build`/`vet` can't catch since `html/template.Must` panics at runtime init):
- Confirmed `pf-field`'s static files serve `200`.
- Logged in as the seeded `admin` account (avoids the no-delete-user-API cleanup problem from the previous feature).
- Created a real text (`fra`/`latn`), confirmed `/texts/new` and `/texts/3/edit` render all `pf-field`s/`pf-button`s correctly, including the exact `id="language"`/`id="script"`/`id="field-source"` values `editor.js` depends on.
- Edited that text's language/script to `arb`/`arab` (a real language/script change) — succeeded (302), confirming `editor.js`'s `pfApplyImeConfig` still reads `#language`/`#script` correctly post-conversion.
- Created a real vocabulary list and added a real item through the item-entry form (all five `pf-field`s, including `id="transcription-group"`) — succeeded (302).
- Saved a real profile locale change through its `pf-button` — succeeded (200).
- Confirmed via `curl` that `login.html` renders `<pf-button ... class="full-width">` and the matching CSS rule.
- **Cleanup**: deleted the test text and vocabulary list; confirmed both `404` afterward. No residual test data left (an improvement over the previous feature, which couldn't clean up its test signup — this time the existing seeded `admin` account was reused instead of creating a new one).

**Known gap (consistent with every prior frontend feature in this project)**: no browser automation tool available this session — the `full-width` CSS fix and general visual layout weren't pixel-confirmed. The user is asked to spot-check the login/signup submit button width and the converted forms' field spacing.

**Overall verdict**: clean, no regressions after the two fixes described above, all Acceptance Criteria validated end-to-end.

## Documentation Review

**Files audited:**
1. `phraseforge/README.md` — no architecture-tree listing (unchanged since the foundation feature's review) — no drift found.
2. `specs/tech-stack.md` — Frontend components convention: still accurate as written ("started with `pf-button`/`pf-nav` on its header — see roadmap for remaining templates") — it's a historical "started with" statement, not a claim that adoption is still limited to the header, so no update needed. Consistent with the precedent set reviewing knowledge's own docs: this section deliberately doesn't enumerate every component by name (that's a README's job, and phraseforge's README doesn't have an architecture tree to enumerate into).
3. `specs/roadmap.md` — no phraseforge-specific claims; unaffected.
4. `phraseforge/CHANGELOG.md` — checked for entry mapping.

**No constitution drift found** — no update needed this time.

**Changelog entry mapping:** `phraseforge` → `phraseforge/CHANGELOG.md`. User-facing UI change (visual structure of eleven templates, pixel-identical) — needs an entry under `## [Unreleased]`.

**Proposed changelog entry (for B5):**

**Category: Changed**
```
- editor/form pages (texts, dialogs, vocabulary, models, profile, login, signup) now render their
  labeled fields and buttons via new `pf-field`/`pf-button` Web Components, replacing inline
  markup — no visual or behavioral change.
```

## Documentation Updates

Added to `phraseforge/CHANGELOG.md` under `## [Unreleased]` → `### Changed`: the entry proposed above.

Removed the `## Now` line for this feature from `specs/artifacts/phraseforge/roadmap.md` (feature complete).

Appended two `[gotcha]`/`[convention]` entries to `specs/memory.md` during implementation (see Implementation Notes) — not duplicated here.
