---
title: Build pf-dialog to replace native confirm(), finish componentizing admin.html
kind: feature
status: done
version: 1
updated: 2026-09-24
branch: main
---

## Problem / Motivation

Last remaining item on `specs/artifacts/phraseforge/roadmap.md`'s `## Later`: build `pf-dialog` (mirroring `kb-dialog`) to replace native `confirm()` across `admin.html`, `view.html`, `vocab-view.html`, `models-view.html`, `dialogs-view.html`'s delete forms and `vocab-edit.html`/`models-edit.html`'s per-item-row deletes (10 call sites total, found via grep — the roadmap line's "~9" was an undercount); and finish componentizing `admin.html` (the one template that predates every `pf-*` feature this series, still all plain `<label class="field-label">`/`<button class="btn">` markup).

**The real design problem this feature has to solve, not present in any prior `pf-*` feature**: `kb-dialog.confirm()` is `async` (returns a `Promise<boolean>`), because knowledge's delete flows are already JS `fetch()` calls that can simply `await` it. Phraseforge's delete/revoke/remove actions are real `<form method="post">` submissions gated by a synchronous `onsubmit="return confirm('...')"` — a native dialog blocks the thread and returns `true`/`false` immediately, which a synchronous `onsubmit` handler can return directly. An `onsubmit` handler **cannot** `await` a `Promise` and then decide — by the time the promise resolves, the synchronous handler has already returned. Porting `kb-dialog`'s API as-is, without restructuring how these forms submit, would not work at all, not just look different. This feature's actual approach is described in full below.

## Acceptance Criteria

- `phraseforge/internal/server/static/components/pf-dialog/` (`component.js`, `component.css`, `component.test.js`) exists, mirroring `kb-dialog`'s mechanism exactly: built from two `pf-button` instances (Cancel=`secondary`, OK=`primary`/`danger` per a `danger` option), `.confirm(message, {danger})` returns `Promise<boolean>`, resolved by OK/Cancel click, Escape, or clicking the overlay outside the box. CSS matches phraseforge's own palette (`var(--surface)`/`var(--border)`/`var(--text)`, not knowledge's `--bg`/`--fg`).
- **The actual replacement mechanism**: every `<form method="post" ... onsubmit="return confirm('...')">` becomes `<form method="post" ... data-confirm="..." {{if <destructive>}}data-confirm-danger="true"{{end}}>` (no `onsubmit` attribute). A single shared `phraseforgeWireConfirmForms()` function (added to `layout.html`'s existing shared `<script>` block, called once at the bottom like `phraseforgeSetIcon()` already is) attaches one `submit` listener per `form[data-confirm]`: on submit, `event.preventDefault()`, `await` `confirmDialog.confirm(form.dataset.confirm, {danger: form.dataset.confirmDanger === "true"})`, and if the result is `true`, call the form's native `.submit()` (not `.requestSubmit()` — the native method does not re-dispatch a `submit` event, so no re-entrancy guard is needed). A single `<pf-dialog id="confirmDialog">` is added once to `layout.html` (present on every page, like the header — harmless on pages that don't use it, avoids tracking which of the ~9 templates need it added individually).
- All 10 call sites converted, with the correct danger flag preserving each one's current visual severity: **danger** (previously `.btn-danger` or `.link-btn`'s red-colored delete/revoke/remove action) — `view.html`, `vocab-view.html`, `models-view.html`, `dialogs-view.html`'s Delete forms; `admin.html`'s revoke-grant, delete-LLM-prompt, remove-IME-config forms; `vocab-edit.html`/`models-edit.html`'s per-item-row delete forms. **Not danger** (matches its current plain `.btn` styling) — `admin.html`'s config-import form.
- **Scope extension, called out explicitly for approval** (adjacent to the confirm-form work, same files, low risk, using only the already-built `pf-button`): `view.html`/`vocab-view.html`/`models-view.html`/`dialogs-view.html`'s Copy button (`variant="secondary"`) and Delete submit button (`variant="danger"`) also convert to `pf-button` — these four templates were explicitly out of scope for `phraseforge-pf-form-components` (named there as this feature's territory), and leaving their buttons as plain `<button class="btn ...">` while restructuring the very form those buttons submit would be an odd half-measure. Their `<a class="btn" href=".../edit">Edit</a>` links stay untouched (real anchors, not buttons — the same rule applied throughout this series).
- `admin.html` "finished": all 13 `<label class="field-label" for="X">` + sibling input/select pairs become `<pf-field>` (mirroring the mechanical conversion already proven in `phraseforge-pf-form-components`); all 4 `.btn` submit buttons (config-import, grant, ime-save, llm-save) become `<pf-button variant="primary">`. The `<a class="btn" href="/admin/config/export">` export link stays a plain anchor (same real-link rule). The `needs_transcription` checkbox's wrapping `<label style="display:flex;...">` (a different shape — label wrapping a checkbox inline, not `field-label` + sibling) and the inline `<script>` block driving the LLM kind/target-language toggle are **not** touched — neither matches an established conversion pattern, and extracting inline JS wasn't asked for by this roadmap item (unlike `phraseforge-pf-form-components`, which explicitly said "extracting their inline CSS/JS").
- `.link-btn` trigger buttons themselves (revoke/llm-delete/ime-remove/vocab-edit's-and-models-edit's item-delete) stay plain `<button class="link-btn">` — no existing `pf-button` variant matches `.link-btn`'s minimal text-link styling, and no new variant was requested by this roadmap item; only their form's submit mechanism changes.
- No change to any Go/backend code, any HTTP route, `editor.js`, `ime.js`, or any CSS color/variable value.
- `pf-dialog` has a `component.test.js` mirroring `kb-dialog`'s three tests exactly (OK resolves true, Cancel resolves false, Escape/overlay-click resolves false), built against the real `pf-button` it composes.
- **Testing gap this feature must close, unlike every prior `pf-*` feature**: the client-side confirm/cancel *decision* (does the form actually submit) cannot be exercised by `curl` — `curl` posting directly to a delete route always "confirms," since there is no client-side gate to bypass over raw HTTP. A dedicated jsdom test extracts the real deployed `layout.html`'s `phraseforgeWireConfirmForms` logic (same "extract the real deployed script, execute it in Node+jsdom" technique used throughout this project) against a fixture `data-confirm` form with a mocked `pf-dialog.confirm()` and a mocked `form.submit()`, asserting `submit()` is called only when `confirm()` resolves `true`, and never called (plus `preventDefault()` observed) when it resolves `false`.
- `go build`/`vet`/`gofmt -l .` unaffected. `task test-phraseforge-frontend` passes (existing 15 tests plus new `pf-dialog` and wiring-logic tests).
- Live end-to-end validation: deploy; confirm via a real authenticated admin session that every converted page renders correctly and that the *backend* delete/revoke/remove/import routes still work when posted to directly (proving the server side of the restructured forms is intact); confirm via the jsdom wiring test (above) that the *client-side* gate itself behaves correctly, since no browser automation tool is available this session to click a real dialog.

## Approach

**Key decision — why `pf-dialog` isn't a drop-in `kb-dialog` port**: covered above (Problem/Motivation) and reflected in the `data-confirm` attribute + `phraseforgeWireConfirmForms()` mechanism, which is the actual novel piece of this feature — everything else (the component itself, the `pf-field`/`pf-button` conversions) is mechanical repetition of already-proven patterns from the prior three features in this series.

**Ordered implementation plan** (each step independently deployable via `task deploy-phraseforge`):

1. **Build `pf-dialog`** (`component.js`/`component.css`/`component.test.js`), composed from two `pf-button` instances, CSS matching phraseforge's palette. Add `<pf-dialog id="confirmDialog">` once to `layout.html`, plus `phraseforgeWireConfirmForms()` in its shared `<script>` block, called once at the bottom.
2. **Convert `view.html`, `vocab-view.html`, `models-view.html`, `dialogs-view.html`**: delete form → `data-confirm`/`data-confirm-danger`; Copy/Delete buttons → `pf-button`.
3. **Convert `vocab-edit.html`, `models-edit.html`**: per-item-row delete form → `data-confirm-danger` (trigger button itself stays `.link-btn`, unconverted).
4. **Convert `admin.html`**: all 4 confirm forms → `data-confirm`(`-danger`); all 13 field-label pairs → `pf-field`; all 4 submit buttons → `pf-button`. Checkbox label, export link, and inline LLM-toggle script left untouched.
5. **Add the jsdom wiring-logic test** (extracting the real deployed `layout.html`'s script), covering the confirm/cancel decision path this feature is otherwise untestable without a real browser.
6. **Live-deploy and validate**: real admin session exercising the backend routes directly (proving the server side survives), plus the jsdom wiring test (proving the client-side gate is correct).

**Risks & testing strategy:**

- Highest risk: getting the `data-confirm` → `phraseforgeWireConfirmForms` → `form.submit()` mechanism wrong in a way that either never submits (dialog "does nothing") or always submits (dialog is decorative, no real gate). Covered by the dedicated wiring-logic jsdom test described above, executed against the real deployed script, not a hand-copied approximation of it.
- Using `form.submit()` (not `.requestSubmit()`) specifically to avoid re-entrant `submit` events — documented as a `[gotcha]`-worthy fact for `specs/memory.md` if it proves non-obvious during implementation.
- No browser automation available this session (consistent with every prior frontend feature here) — the actual "does clicking OK in a real browser delete the row" question can't be fully closed this session; the user is asked to spot-check one destructive action (e.g., revoke a test grant) after deploy.

## Affected Areas

- `phraseforge/internal/server/templates/{admin,view,vocab-view,models-view,dialogs-view,vocab-edit,models-edit}.html`
- `phraseforge/internal/server/templates/layout.html` (`<pf-dialog id="confirmDialog">`, `phraseforgeWireConfirmForms()`, new `<script>` include)
- new `phraseforge/internal/server/static/components/pf-dialog/` (`component.js`, `component.css`, `component.test.js`)
- new wiring-logic test file under `phraseforge/internal/server/static/components/` (exact name decided during implementation, mirroring knowledge's `app-ingest-subnav.test.js` precedent of a test file that isn't itself one component's own directory)
- `specs/artifacts/phraseforge/roadmap.md` (`## Later` → `## Now` transition at B2 start — this is the last item, so `## Later` becomes empty)
- `phraseforge/CHANGELOG.md` (entry added at B5)

## Out of Scope

- Any new `pf-button` variant for `.link-btn`'s styling — trigger buttons stay plain, only their form's submit mechanism changes.
- The `needs_transcription` checkbox's label, and admin.html's inline LLM-kind/target-language toggle `<script>` — different shape / not requested by this roadmap item.
- `<a class="btn">` links anywhere (config export, Edit links on the four `-view.html` pages) — real anchors, not buttons, same rule applied throughout this series.
- Any change to `editor.js`, `ime.js`, any Go/backend code, any HTTP route, or any CSS color/variable value.
- Porting any of this to `knowledge` or `dictionary`.
- Any redesign of the confirmation copy/wording itself — every `data-confirm` message is the exact existing i18n string, unchanged.

## Implementation Notes

**Step 1 — `pf-dialog`.** Built `phraseforge/internal/server/static/components/pf-dialog/{component.js,component.css,component.test.js}`, composed from two `pf-button` instances, mirroring `kb-dialog`'s mechanism exactly (`.confirm()` returns a `Promise<boolean>`; resolved by OK/Cancel/Escape/overlay-click). CSS matches phraseforge's own palette. Added `<pf-dialog id="confirmDialog">` once to `layout.html` (present on every page, like the header) plus `phraseforgeWireConfirmForms()` in its shared `<script>` block: intercepts `submit` on every `form[data-confirm]`, `preventDefault()`s, `await`s `confirmDialog.confirm(...)`, and calls the form's native `.submit()` (not `.requestSubmit()`, which would re-dispatch a `submit` event and re-trigger this same listener) if confirmed.

**Step 2 — `view.html`/`vocab-view.html`/`models-view.html`/`dialogs-view.html`.** Delete forms → `data-confirm`/`data-confirm-danger`; Copy/Delete buttons → `pf-button`. **Reused the `phraseforgeGenerate` fix from `phraseforge-pf-form-components`**: `phraseforgeCopyMarkdown`/`phraseforgeCopyActiveMarkdown` also read/write `button.textContent` on their `this` argument — same class of bug — so all four Copy buttons pass `this.querySelector('button')`, not `this`.

**Step 3 — `vocab-edit.html`/`models-edit.html`.** Item-delete forms → `data-confirm-danger`; the `.link-btn` trigger button itself stays unconverted, per the approved plan (no fitting `pf-button` variant).

**Step 4 — `admin.html`.** All 4 confirm forms → `data-confirm`(`-danger`) (config-import: not danger, matching its original plain `.btn`; revoke/llm-delete/ime-remove: danger). 12 of 13 field-label pairs → `pf-field`; the 13th (`admin-config-file`) deliberately excluded and documented inline — it sits in a horizontal flex row with the submit button (`align-items:center`), unlike every other field-label pair on the page (block-stacked inside a `.field-row` column), and `pf-field`'s default `display:block` would have stacked the label above the input as one flex item, breaking the inline layout. `#llm-target-language-field`'s wrapper `<div>` removed, `pf-field` carrying that `id` (same `#transcription-group` technique proven in the prior feature — verified live, see Validation, that the LLM kind/target-language toggle still works). All 4 `.btn` submit buttons → `pf-button`. The `needs_transcription` checkbox's wrapping `<label>` (opposite shape — label wraps its input, not a sibling) and the inline LLM-toggle `<script>` left untouched, as planned.

**Step 5 — wiring-logic test.** Added `phraseforge/internal/server/static/components/layout-confirm-forms.test.js`, which reads the real `layout.html` template from disk (not a hand-copied approximation), extracts the shared `<script>` block via the fact that it's the only other attribute-less `<script>` tag besides the pre-paint theme IIFE, and executes it in jsdom against a fixture `data-confirm` form with a mocked `pf-dialog.confirm()` and mocked `form.submit()`. **One test fix needed**: the extracted script runs inside jsdom's own vm realm (`runScripts: "dangerously"`), so its object literals aren't prototype-identical to ones built in the test's Node realm even with identical shape — `assert.deepEqual` on the whole `{message, options}` object failed on a reference-equality check despite matching structure; fixed by comparing fields individually (`confirmArgs.message`, `confirmArgs.options.danger`).

**Final file list**: modified `admin.html`, `view.html`, `vocab-view.html`, `models-view.html`, `dialogs-view.html`, `vocab-edit.html`, `models-edit.html`, `layout.html`; new `phraseforge/internal/server/static/components/pf-dialog/{component.js,component.css,component.test.js}` and `phraseforge/internal/server/static/components/layout-confirm-forms.test.js`.

**Self-review** (code-review + security-review): every `data-confirm` value is the exact existing i18n string (server-rendered, same escaping as before — Go's `html/template` attribute-context escaping, unchanged mechanism, just a different attribute name than `onsubmit`). No new attack surface — `form.submit()` submits the same fields the form already had; nothing about field values changed. Re-checked all 4 Copy-button call sites for the `this.querySelector('button')` fix (none missed, matching the exact pattern from the prior feature).

## Validation

**Automated:**
- `phraseforge/internal/server/static/components`: `task test-phraseforge-frontend` — 22/22 tests pass (5 `pf-button`, 4 `pf-field`, 3 `pf-nav`, 3 `pf-card`, 4 `pf-dialog`, 3 new wiring-logic tests).
- `go build ./...`, `go vet ./...`, `gofmt -l .` — all clean (no Go files changed).

**Live end-to-end validation** (deployed via `task deploy-phraseforge`; no template-parse panic across all 7 modified templates):
- Logged in as the seeded `admin` account.
- Confirmed `admin.html` renders 12 `pf-field`s, 6 `pf-button`s (4 own + 2 from the header), and — after creating one test LLM prompt and one test IME config to populate their otherwise-empty tables — all 4 `data-confirm` forms present with the correct danger flag; deleted both via their real backend routes (`302`), confirming the server side survives the restructuring; confirmed the tables returned to 2 `data-confirm` forms afterward (grants/config-import only).
- Created a real text, confirmed `/texts/5` renders `pf-button` Copy/Delete and the Delete form's `data-confirm`/`data-confirm-danger`; deleted it for real (`302`, then `404` on follow-up).
- Created a real vocabulary list and item, confirmed `/vocabulary/3/edit` renders the item-delete form's `data-confirm-danger` with its `.link-btn` trigger correctly left unconverted; deleted the item then the list for real (`302`, then `404` on follow-up).
- **The actual client-side gate** (does cancelling really prevent deletion) — not exercisable via `curl` (posting straight to a delete route always "succeeds" over raw HTTP) — is exactly what the new `layout-confirm-forms.test.js` proves instead: `form.submit()` is called only when `confirm()` resolves `true`, never when it resolves `false`, and a plain form without `data-confirm` is never intercepted at all.
- **Cleanup**: all test data (1 LLM prompt, 1 IME config, 1 text, 1 vocabulary list+item) deleted; confirmed gone. No residual test data.

**Known gap (consistent with every prior frontend feature in this project)**: no browser automation tool available this session — a real click on `pf-dialog`'s OK/Cancel button in an actual browser wasn't performed. The jsdom wiring test plus `pf-dialog`'s own component tests (both proven against real code, not hand-simulated) are the strongest verification possible without one; the user is asked to spot-check one destructive action (e.g., revoke a test grant) visually after deploy.

**Overall verdict**: clean, no regressions, all Acceptance Criteria validated end-to-end — including the one genuinely novel mechanism in this feature (the sync-`onsubmit`-to-async-`confirm()` restructuring), which is the part most worth having tested thoroughly.

## Documentation Review

**Files audited:**
1. `phraseforge/README.md` — no architecture-tree listing (unchanged) — no drift.
2. `specs/tech-stack.md` — Frontend components convention: still accurate — no update needed.
3. `specs/roadmap.md` — unaffected.
4. `phraseforge/CHANGELOG.md` — checked for entry mapping.

**No constitution drift found.**

**Proposed changelog entry (for B5):**

**Category: Changed**
```
- delete/revoke/remove confirmations across the app (admin, texts, dialogs, vocabulary, models,
  and vocabulary/models item rows) now use an in-app `pf-dialog` instead of the browser's native
  confirm() popup; admin.html's remaining fields and buttons now use `pf-field`/`pf-button` —
  completing the pf-* componentization of phraseforge's whole UI.
```

## Documentation Updates

Added to `phraseforge/CHANGELOG.md` under `## [Unreleased]` → `### Changed`: the entry proposed above.

Removed the `## Now` line for this feature from `specs/artifacts/phraseforge/roadmap.md` — this was the last item on phraseforge's roadmap, so `## Later` is now empty.

Appended two `[convention]`/`[gotcha]` entries to `specs/memory.md` during implementation (see Implementation Notes) — not duplicated here.
