---
title: Build pf-card and convert phraseforge's list pages' card grids
kind: feature
status: done
version: 1
updated: 2026-09-24
branch: main
---

## Problem / Motivation

Next `## Later` item on `specs/artifacts/phraseforge/roadmap.md`: build `pf-card` and use it for the four list pages' (`list.html`, `dialogs-list.html`, `vocab-list.html`, `models-list.html`) repeated `<div class="card">` item markup inside their `.card-grid` containers.

Research surfaced an important difference from knowledge's `kb-card` that shapes the whole design here, the same story as `pf-nav` in the foundation feature: knowledge's `kb-card` wraps its **entire** card as a click target and dispatches a `kb-card-select` event, because knowledge's items have no real per-item URL (a client-side SPA — clicking opens the detail view via JS). Phraseforge's cards already have a real `<h2><a href="/texts/{{.ID}}">{{.Title}}</a></h2>` inside them — only the title text is the click target, and it's a real link (works with JS disabled, real right-click/open-in-new-tab, no JS required at all). Porting `kb-card`'s "whole card is clickable via JS" model here would be a **regression**, not a feature — it would layer a JS click-interception behavior on top of, or in place of, a link that already works correctly without any JS. `pf-card` is therefore designed like `pf-nav`, not like `kb-card`: pure visual chrome around unmodified real content, no click interception, no custom event.

## Acceptance Criteria

- `phraseforge/internal/server/static/components/pf-card/` (`component.js`, `component.css`, `component.test.js`) exists. Like `pf-nav`, it never reads, moves, or listens on its children — `connectedCallback` only injects the component's stylesheet. No parse-order hazard to guard against (unlike `pf-button`/`pf-field`).
- `pf-card`'s CSS reproduces `.card`'s exact current values (`background: var(--surface); border: 1px solid var(--border); border-radius: 0.6rem; padding: 1.5rem; box-shadow: 0 1px 2px rgba(0,0,0,0.04);`) plus its nested `h2`/`h2 a`/`h2 a:hover`/`.meta` rules, scoped as `pf-card h2`, `pf-card h2 a`, etc. `pf-card { display: block; }` (matching the replaced `<div>`'s default, needed for it to size correctly as a `.card-grid` grid item).
- Each of the four list templates' `<div class="card">...</div>` per-item markup becomes `<pf-card>...</pf-card>`, with every child (the title `<h2><a>`, the language/script badge, tag links, the meta/count/date line) completely unchanged — only the outer element tag changes.
- The surrounding `.card-grid` container **stays a plain `<div class="card-grid">`** — not componentized, matching knowledge's own explicit precedent (`kb-card`'s Out of Scope: "a `kb-list` component wrapping the grid container itself ... only the card, not the list, is componentized this pass").
- `layout.html`'s existing `.card`/`.card-grid` CSS is **left completely untouched** — `admin.html` and `profile.html` both still use `class="card"` (confirmed via grep) and are out of scope for this feature, so that CSS is still load-bearing elsewhere. `pf-card`'s component.css is a deliberate duplicate of the same values, not a replacement.
- The `<a class="btn" href="/texts/new">` ("New") links at the top of each list page are **not** touched — `pf-button` wraps a real `<button>` (form-control semantics), and converting a real navigable `<a>` to it would silently break navigation (no `href` capability on a `<button>`). This is a deliberate exclusion, not an oversight, since it might otherwise look like a missed spot.
- No change to any Go/backend code, any HTTP route, `admin.html`, `profile.html`, or any template not in the four listed.
- `pf-card` has a `component.test.js` (Node + jsdom) covering: registers the custom element, leaves arbitrary light-DOM children (an `h2`/`a`/`.meta` fixture) completely unchanged, injects its stylesheet exactly once — mirroring `pf-nav`'s existing test shape.
- `go build`/`vet`/`gofmt -l .` unaffected. `task test-phraseforge-frontend` passes (existing 12 tests plus new `pf-card` tests).
- Live end-to-end validation: deploy, confirm via a real authenticated session that each of the four list pages renders its items as `<pf-card>` with all content intact, that clicking through a card's title link still navigates correctly (real `<a href>`, unaffected), and that `admin.html`/`profile.html`'s untouched `.card` usage still renders correctly (proving the shared CSS wasn't broken).

## Approach

**Key decision**: `pf-card` is deliberately *not* a `kb-card` port, for the same reason `pf-nav` wasn't a `kb-nav` port (`phraseforge-pf-components-foundation`) — phraseforge already has real, working navigation (`<a href>`) that must keep working with JS disabled; wrapping the whole card as a JS click target would duplicate or shadow that, not improve it. `pf-card` contributes only CSS chrome, exactly like `pf-nav`.

**Ordered implementation plan** (each step independently deployable via `task deploy-phraseforge`):

1. **Build `pf-card`** (`component.js`/`component.css`/`component.test.js`), copying `.card`'s exact visual values into scoped `pf-card`/`pf-card h2`/etc. selectors. Add its `<script>` include to `layout.html`.
2. **Convert `list.html`, `dialogs-list.html`, `vocab-list.html`, `models-list.html`**: `<div class="card">` → `<pf-card>`, no other change to any of the four templates.
3. **Live-deploy and validate**: confirm all four list pages render correctly, title links still navigate, and `admin.html`/`profile.html`'s still-plain `.card` usage is unaffected.

**Risks & testing strategy:**

- Lowest-risk feature so far in this series — `pf-card` is structurally identical to the already-proven `pf-nav` (no child manipulation at all). Main risk is a copy-paste CSS mismatch versus `.card`'s exact values; covered by comparing rendered output directly (`curl`) against `admin.html`'s still-plain `.card` on the same deploy, both should look identical.
- No browser automation available this session — live validation is `curl`/session-based; visual spot-check asked of the user as with every prior frontend feature here.

## Affected Areas

- `phraseforge/internal/server/templates/{list,dialogs-list,vocab-list,models-list}.html`
- `phraseforge/internal/server/templates/layout.html` (add `<script src="/static/components/pf-card/component.js">`)
- new `phraseforge/internal/server/static/components/pf-card/` (`component.js`, `component.css`, `component.test.js`)
- `specs/artifacts/phraseforge/roadmap.md` (`## Later` → `## Now` transition at B2 start)
- `phraseforge/CHANGELOG.md` (entry added at B5)

## Out of Scope

- `admin.html`, `profile.html` — both still use plain `class="card"`; `phraseforge-pf-dialog-admin-components` covers `admin.html`. `profile.html`'s `.card` usage isn't named in any current roadmap item — flagged, not fixed here.
- The `.card-grid` container itself — stays a plain `<div>`, matching knowledge's own `kb-card` precedent.
- The `<a class="btn">` "New" links — a real anchor, not a `<button>`; `pf-button` doesn't apply to it.
- `.badge`/`.tag-link`/`.meta` — no new component invented for these; not requested by this roadmap item.
- Any change to `editor.js`, `ime.js`, any Go/backend code, any HTTP route, or any CSS color/variable value.
- Any change to `knowledge` or `dictionary`.

## Implementation Notes

**Step 1 — `pf-card`.** Built `phraseforge/internal/server/static/components/pf-card/{component.js,component.css,component.test.js}` — structurally identical to `pf-nav` (no child manipulation, `connectedCallback` only injects the stylesheet). CSS reproduces `.card`'s exact values plus its nested `h2`/`h2 a`/`.meta` rules, scoped under `pf-card`. Added its `<script>` include to `layout.html`.

**Step 2 — the four list templates.** `list.html`, `dialogs-list.html`, `vocab-list.html`, `models-list.html`: `<div class="card">` → `<pf-card>`, no other change — every child (title link, badge, tag links, meta line) byte-for-byte unchanged. `.card-grid` containers left as plain `<div>`s, per the approved plan.

No deviations from the approved Approach — this was the lowest-risk feature in the series, exactly as anticipated (no attribute-forwarding, no `this`-rebinding, no inline-style hazards like `pf-button`/`pf-field` hit).

**Final file list**: modified `list.html`, `dialogs-list.html`, `vocab-list.html`, `models-list.html`, `layout.html`; new `phraseforge/internal/server/static/components/pf-card/{component.js,component.css,component.test.js}`.

**Self-review** (code-review): confirmed `admin.html`/`profile.html`'s plain `class="card"` usage is untouched and their shared CSS in `layout.html` wasn't modified. No new attack surface — `pf-card` never reads or writes any attribute.

## Validation

**Automated:**
- `phraseforge/internal/server/static/components`: `task test-phraseforge-frontend` — 15/15 tests pass (5 `pf-button`, 4 `pf-field`, 3 `pf-nav`, 3 new `pf-card`).
- `go build ./...`, `go vet ./...`, `gofmt -l .` — all clean.

**Live end-to-end validation** (deployed via `task deploy-phraseforge`; no template-parse panic on startup):
- Confirmed `pf-card`'s static files serve `200`.
- Logged in as the seeded `admin` account. Created one real item in each of the four resource types (text, dialog, vocabulary list, models list) — all `302`.
- Confirmed each of the four list pages (`/`, `/dialogs`, `/vocabulary`, `/models`) renders exactly one `<pf-card>` with its real `<h2><a href="/texts/4">...</a></h2>` title link and badge/meta content fully intact.
- Confirmed `admin.html` (7 occurrences) and `profile.html` (2 occurrences) still render plain `class="card"` elements, unaffected — proving the shared `.card` CSS in `layout.html` was not broken by adding `pf-card`'s separate component CSS.
- **Cleanup**: deleted all four test items; confirmed all `404` afterward. No residual test data.

**Known gap (consistent with every prior frontend feature in this project)**: no browser automation tool available this session — pixel-level visual confirmation wasn't possible. The user is asked to spot-check that the four list pages' card grids look identical to before.

**Overall verdict**: clean, no regressions, all Acceptance Criteria validated end-to-end.

## Documentation Review

**Files audited:**
1. `phraseforge/README.md` — no architecture-tree listing (unchanged) — no drift.
2. `specs/tech-stack.md` — Frontend components convention: still accurate ("started with `pf-button`/`pf-nav`... see roadmap") — a historical statement, not a claim of current completeness. No update needed.
3. `specs/roadmap.md` — unaffected.
4. `phraseforge/CHANGELOG.md` — checked for entry mapping.

**No constitution drift found.**

**Proposed changelog entry (for B5):**

**Category: Changed**
```
- the four list pages (Texts, Dialogs, Vocabulary, Models) now render their card-grid items via a
  new `pf-card` Web Component, replacing inline markup — no visual or behavioral change.
```

## Documentation Updates

Added to `phraseforge/CHANGELOG.md` under `## [Unreleased]` → `### Changed`: the entry proposed above.

Removed the `## Now` line for this feature from `specs/artifacts/phraseforge/roadmap.md` (feature complete).

No `memory.md` entries this time — nothing durable/reusable surfaced beyond what's already recorded; this feature validated cleanly against the plan with no deviations.
