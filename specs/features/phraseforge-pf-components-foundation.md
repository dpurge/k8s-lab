---
title: Establish pf-* Web Components convention in phraseforge (header/toggles/nav)
kind: feature
status: done
version: 1
updated: 2026-09-24
branch: main
---

## Problem / Motivation

`phraseforge` is a server-rendered Go `html/template` app: its entire UI lives in one 411-line `layout.html` (inline `<style>` + inline `<script>`) plus ~15 page templates, with zero component reuse — no design system. `knowledge`, by contrast, already has a mature `kb-*` Web Components convention (light-DOM custom elements, one directory per component under `static/components/<tag>/`, jsdom-tested) covering its whole app. `specs/tech-stack.md`'s Frontend components entry already anticipates this gap: *"phraseforge's existing `pf`-prefixed JS function convention extends naturally to `pf-*` if/when phraseforge adopts this pattern; not done yet."*

This is the foundation item on `specs/artifacts/phraseforge/roadmap.md`'s `## Next`: establish the `pf-*` convention with two components — `pf-button` and `pf-nav` — and use them for exactly the header's sidebar-toggle/theme-toggle buttons and the sidebar's nav links, mirroring knowledge's own first slice (`knowledge-header-nav-components`) rather than attempting the whole app at once. The remaining templates (forms, list/card pages, admin/dialogs) are separate, already-roadmapped follow-on items (`phraseforge-pf-form-components`, `phraseforge-pf-list-card-components`, `phraseforge-pf-dialog-admin-components`).

Per the user's direction (`specs/memory.md`, 2026-09-24 `[decision]` entry), this must be designed extensibly for a future shared cross-app nav/layout system (phraseforge + knowledge + any later app), not as a phraseforge-only one-off. Concretely: `pf-nav`'s real-anchor-link rendering model and `kb-nav`'s event-driven SPA-tab model stay two distinct, documented patterns for now — reconciling them into one shared abstraction is deliberately deferred, not forced here just to look uniform prematurely (see Out of Scope).

## Acceptance Criteria

- `phraseforge/internal/server/static/components/pf-button/` (`component.js`, `component.css`, `component.test.js`) exists per `tech-stack.md`'s file-layout convention, served at `/static/components/pf-button/component.js` via the existing public `/static/*` route — verified this route is registered before (outside) the `requireAuth` group, so **no `server.go` change is needed** (unlike knowledge's original gotcha, which needed a retrofitted public-route allowlist).
- `pf-button` wraps a real inner `<button>` (native keyboard/focus/ARIA semantics), forwards `disabled`/`type`, supports `variant` (`primary` default / `secondary` / `danger` / `icon`), light DOM only, self-injecting CSS — same mechanism as `kb-button`, deliberately duplicated per app per `tech-stack.md`'s sharing model (not shared files).
- `phraseforge/internal/server/static/components/pf-nav/` exists. Unlike `kb-nav` (JS `items`/`active` properties + `kb-nav-select` event, because knowledge is a client-side tab-switcher with no server-known active state), `pf-nav` takes real `<a>` children exactly as the Go template already renders them (href, label, and the existing server-set `class="active"`) — it does **not** intercept clicks or dispatch a custom event, so real page navigation keeps working with JS disabled. Its job is purely visual chrome (padding/hover/active-indicator) around whatever links the template puts inside it.
- `layout.html`'s sidebar-toggle and theme-toggle buttons become `<pf-button variant="icon">`, keeping their existing `onclick`, `aria-label`, and `title` attributes unchanged.
- `layout.html`'s sidebar `<nav>` (Texts/Dialogs/Vocabulary/Models/Admin links) becomes `<pf-nav>` wrapping the same server-rendered `<a>` elements, including the existing active-link logic — no Go handler change.
- No visual regression: toggle buttons and nav links look and behave exactly as before (same colors, same active-state highlight, same collapse/theme behavior) — this is a structural extraction, not a redesign. No change to phraseforge's CSS variable palette/colors.
- `pf-button`'s sidebar-toggle instance has static light-DOM content (`☰`) present at parse time — this hits the exact real-Chrome-vs-jsdom parse-order race already found and fixed in `kb-button` (`specs/memory.md`, 2026-09-22T17:39:25Z `[gotcha]`): `connectedCallback` fires before the parser appends content between the tags. `pf-button` must use the same `MutationObserver`-based fix from the start, not the naive synchronous-wrap approach that failed for `kb-button` in real Chrome.
- A new `phraseforge/internal/server/static/components/package.json` (jsdom devDependency, `type: module`, `test` script `node --test`) plus a new `test-phraseforge-frontend` Task in root `Taskfile.yml` (`dir: phraseforge/internal/server/static/components`, `npm install && npm test`), mirroring `test-knowledge-frontend` exactly.
- `pf-button` and `pf-nav` each have a `component.test.js` (Node + jsdom) covering: `pf-button` wraps a real `<button>`, forwards `disabled`/`variant`, and reproduces the static-content parse-order case; `pf-nav` renders its light-DOM `<a>` children unchanged (doesn't strip or alter them) and applies its own wrapper styling.
- `go build`/`vet`/`test`/`gofmt -l .` unaffected (no Go code change — only `layout.html` is touched; confirmed no `server.go` change is needed).
- Live end-to-end validation: deploy, confirm the header's toggle buttons and sidebar nav render and behave identically to before, on both an authenticated page and the login page (theme-toggle renders there too; sidebar-toggle/`pf-nav` do not, since they're inside the existing `{{if .User}}` block) — visual spot-check by the user, consistent with every prior frontend feature in this project (no browser automation tool available this session).

## Approach

**Key decisions:**

1. *Where components live/are served*: `phraseforge/internal/server/static/components/<tag>/`, served through the existing public `/static/*` catch-all (`server.go:111`, registered before the `requireAuth` group at `server.go:119-120`). This is a genuine structural advantage phraseforge already has over knowledge's original setup — knowledge's static tree wasn't behind one public catch-all, so it needed a retrofitted allowlist (`specs/memory.md`, 2026-09-22T19:15:09Z `[gotcha]`); phraseforge needs no equivalent change.

2. *`pf-nav` is not a `kb-nav` port*: `kb-nav` exists because knowledge is a single-page tab-switcher with no server-known "active" state — it needs a JS property for items and a custom event so the host page's JS can react. Phraseforge is a real multi-page app; the Go template already knows and renders the active link server-side, and navigation must keep working with JS disabled. Forcing phraseforge's nav onto `kb-nav`'s event-driven model would be a regression (breaks no-JS navigation) in service of a superficial similarity. `pf-nav` instead wraps real, already-rendered `<a>` children and only contributes visual chrome. Reconciling these two patterns into one shared, cross-app abstraction is real future work (tracked only in `specs/memory.md`'s decision entry, not yet its own roadmap item) — not attempted here.

3. *No constitution change needed to start*: `tech-stack.md`'s Frontend components convention already documents the `pf-*` naming/file-layout/testing pattern generically ("if/when phraseforge adopts this pattern"). Once this feature lands, that sentence becomes stale (phraseforge *has* adopted it) — this is flagged for B4's Documentation Review, not a pre-implementation replanning gate, since no convention is changing, only being adopted by a second app.

**Ordered implementation plan** (each step independently deployable via `task deploy-phraseforge`):

1. **Build `pf-button`.** New `components/pf-button/{component.js,component.css,component.test.js}`, mirroring `kb-button`'s wrap-a-real-`<button>` mechanism and `MutationObserver`-based parse-order fix (applied from the start, per the gotcha above — not discovered the hard way a second time). Wire `layout.html`'s sidebar-toggle and theme-toggle buttons to `<pf-button variant="icon">`, preserving their existing `onclick`/`aria-label`/`title`.
2. **Build `pf-nav`.** New `components/pf-nav/{component.js,component.css,component.test.js}` — a light-DOM wrapper contributing only CSS chrome around its `<a>` children, no click interception, no custom event. Wire `layout.html`'s sidebar `<nav>` to `<pf-nav>` wrapping the existing five `<a>` elements unchanged.
3. **Dev test tooling.** Add `components/package.json` (mirroring knowledge's exactly: `jsdom`, `type:module`, `node --test`) and a `test-phraseforge-frontend` Task in `Taskfile.yml`.
4. **Live-deploy and validate.** `task deploy-phraseforge`; confirm via `curl` that the new component files are served (200, unauthenticated, since `/static/*` is already public); ask the user to visually spot-check the header/sidebar on an authenticated page and the theme-toggle on the login page.

**Risks & testing strategy:**

- Highest risk: repeating `kb-button`'s parse-order bug for `pf-button`'s sidebar-toggle (static `☰` content). Mitigated by building the `MutationObserver` fix in from the start (see Acceptance Criteria) and covering it with a `component.test.js` case that writes static light-DOM content before the element is defined/upgraded, matching the regression test already proven for `kb-button`.
- `pf-nav` must not alter or strip its `<a>` children's attributes (href, class) — covered by a test asserting the rendered light DOM is unchanged after the component upgrades.
- No browser automation available this session (consistent with every prior frontend feature in this project) — live validation is `curl`-based (route/byte-serving checks) plus an explicit ask to the user for a visual spot-check.

## Affected Areas

- `phraseforge/internal/server/templates/layout.html` (header buttons → `pf-button`; sidebar `<nav>` → `pf-nav`; add `<script src>` includes)
- new `phraseforge/internal/server/static/components/pf-button/` (`component.js`, `component.css`, `component.test.js`)
- new `phraseforge/internal/server/static/components/pf-nav/` (`component.js`, `component.css`, `component.test.js`)
- new `phraseforge/internal/server/static/components/package.json` (+ generated `package-lock.json`)
- `Taskfile.yml` (new `test-phraseforge-frontend` task)
- `.gitignore` (confirm `node_modules/` already covered — likely already true from the knowledge feature; verify, don't duplicate)
- `specs/tech-stack.md` (B4 documentation review candidate — the "not done yet" phrasing becomes stale)
- `specs/artifacts/phraseforge/roadmap.md` (`## Next` → `## Now` transition at B2 start, per usual)
- `phraseforge/CHANGELOG.md` (entry added at B5)

## Out of Scope

- Any other phraseforge template (forms, list/card pages, admin, dialogs, profile, login/signup page *content*, models/vocab pages) — tracked separately as `phraseforge-pf-form-components`, `phraseforge-pf-list-card-components`, `phraseforge-pf-dialog-admin-components`.
- Any visual/color/palette change to phraseforge — it keeps its current look exactly; only markup/CSS/JS structure changes (inline → componentized).
- Any change to `knowledge` or `dictionary`.
- Reconciling `pf-nav`'s real-link model with `kb-nav`'s event-driven model into one shared cross-app abstraction — real future work, tracked only in `specs/memory.md`'s 2026-09-24 `[decision]` entry, not a roadmap item yet.
- Removing `phraseforge/internal/server/static/css/main.css` — confirmed via grep to be dead/unreferenced (leftover from the ported deno-app IME assets). Flagged as a housekeeping opportunity, not part of this feature.
- Any `server.go` change — confirmed unnecessary (`/static/*` is already served outside the `requireAuth` group).
- Any Go/backend logic change; any HTTP route change.

## Implementation Notes

**Step 1 — `pf-button`.** Built `phraseforge/internal/server/static/components/pf-button/{component.js,component.css,component.test.js}`, mirroring `kb-button`'s wrap-a-real-`<button>` mechanism and its `MutationObserver`-based parse-order fix (applied from the start, per `specs/memory.md`'s 2026-09-22T17:39:25Z `[gotcha]`, since the sidebar-toggle's static `☰` content is exactly that shape). **Extended beyond the direct `kb-button` port**: added `aria-label` forwarding (`observedAttributes` + explicit copy in `_wrap()` + `attributeChangedCallback`) — the original buttons had `aria-label` on the real `<button>`, and without forwarding it would sit uselessly on the non-interactive custom-element wrapper, a real accessibility regression caught during self-review before deploy. `title` needed no forwarding (the browser's native tooltip lookup already walks up to an ancestor's `title`). Wired `layout.html`'s sidebar-toggle and theme-toggle buttons to `<pf-button variant="icon">`, preserving `onclick`/`aria-label`/`title`.

**Step 2 — `pf-nav`.** Built `phraseforge/internal/server/static/components/pf-nav/{component.js,component.css,component.test.js}` — deliberately simpler than `pf-button`: it never reads, moves, or listens on its `<a>` children, so there is no parse-order hazard to guard against (unlike `pf-button`/`kb-button`/`kb-field`). `connectedCallback` only injects the component's stylesheet. Wired `layout.html`'s sidebar `<nav>` to `<pf-nav>` wrapping the same five server-rendered `<a>` elements, including the existing active-link logic, unchanged.

**Layout regression caught and fixed before deploy**: `layout.html`'s original `.theme-toggle`/`.sidebar-toggle` CSS classes carried both visual skin (now owned by `pf-button`'s `component.css`) *and* header-positioning rules (`.theme-toggle { margin-left: auto; ... }`, pushing it to the header's right edge). Dropping the classes entirely (as the first pass did) would have silently broken that positioning. Fixed by keeping `class="theme-toggle"` on the `<pf-button>` as a pure positioning hook, stripping the now-duplicate visual properties from `layout.html`'s inline `<style>`, and removing `.sidebar-toggle`'s and `aside.sidebar nav a`'s CSS blocks entirely (fully superseded by `pf-button`'s and `pf-nav`'s own component CSS — the `<nav>` → `<pf-nav>` tag rename alone already made the old `aside.sidebar nav a` selector dead).

**Step 3 — dev test tooling.** Added `phraseforge/internal/server/static/components/package.json` (mirroring knowledge's exactly: `jsdom ^30.1.1`, `type:module`, `node --test`) and a `test-phraseforge-frontend` Task in root `Taskfile.yml`, identical in shape to `test-knowledge-frontend`.

**Step 4 — live deploy and validation.** `task deploy-phraseforge`; see Validation below.

No `server.go` change was needed — confirmed live (see Validation) that `/static/*` is already served outside the `requireAuth` group.

**Final file list**: modified `phraseforge/internal/server/templates/layout.html`, `Taskfile.yml`; new `phraseforge/internal/server/static/components/pf-button/{component.js,component.css,component.test.js}`, `phraseforge/internal/server/static/components/pf-nav/{component.js,component.css,component.test.js}`, `phraseforge/internal/server/static/components/package.json` (+ generated `package-lock.json`, `node_modules/` — already gitignored globally).

**Self-review** (code-review): no new attack surface — `aria-label`/`type`/`disabled` values forwarded by `pf-button` all originate from the same i18n-translated template strings as before (not user-supplied), and `pf-nav` never touches its children's attributes at all. No findings requiring a fix beyond the aria-label forwarding already folded into Step 1.

## Validation

**Automated:**
- `phraseforge/internal/server/static/components`: `task test-phraseforge-frontend` — 8/8 tests pass (5 `pf-button`, including the real-Chrome parse-order regression case and the new aria-label-forwarding case; 3 `pf-nav`, including "leaves `<a>` children completely unchanged" and "injects its stylesheet exactly once").
- `go build ./...`, `go vet ./...`, `gofmt -l .` — all clean from `phraseforge/` (no Go files changed; `go test ./...` reports `[no test files]` throughout, a pre-existing condition, consistent with `tech-stack.md`: phraseforge doesn't run tests in CI).

**Live end-to-end validation** (deployed via `task deploy-phraseforge` to the real local k3d cluster):
- Confirmed via `curl` that all four new static files (`pf-button`/`pf-nav` `component.js`/`component.css`) are served with `200`, **unauthenticated** — confirming no `server.go` change was needed.
- Confirmed the unauthenticated `/login` page renders only the theme-toggle `<pf-button>` (no sidebar-toggle, no `<pf-nav>`), matching the existing `{{if .User}}` gating exactly.
- Created a real test signup/login session; confirmed the authenticated home page renders both `<pf-button>` toggles and `<pf-nav>` wrapping the five real `<a>` links with `<a href="/" class="active">` correctly server-set.
- Navigated to `/vocabulary` with the same session; confirmed the active class correctly moved to `<a href="/vocabulary" class="active">`, proving `pf-nav` doesn't interfere with or cache the server's active-link state across page loads (it can't — it never touches its children).

**Known gap (consistent with every prior frontend feature in this project)**: no browser automation tool available this session, so no pixel/visual confirmation — the user is asked to spot-check the header and sidebar visually after deploy (should be pixel-identical to before, since no CSS values changed, only which selector defines them).

**Residual note on test cleanup**: validation required a real session, so a real signup was created against the local dev database (username `pfcompfoundtest_<pid>`). Phraseforge has no delete-user API (confirmed via grep), so the account wasn't programmatically cleaned up — consistent with the same situation and user decision recorded in `knowledge-workspace-redesign`'s Validation section for knowledge's own local dev cluster.

**Overall verdict**: clean, no regressions, all Acceptance Criteria validated end-to-end.

## Documentation Review

**Files audited:**
1. `phraseforge/README.md` — no architecture-tree listing of `internal/server/static/` (unlike knowledge's own README), so nothing there references specific component names or file layout — no drift found.
2. `specs/tech-stack.md` — Frontend components convention.
3. `specs/roadmap.md` — no phraseforge-specific claims; unaffected.
4. `phraseforge/CHANGELOG.md` — checked for entry mapping.

**Drift found — approved and fixed** (constitution change, gated separately): `specs/tech-stack.md` line 72 said phraseforge's `pf-*` adoption was *"not done yet"* — no longer true. Updated to reflect that `pf-button`/`pf-nav` now exist, pointing to `specs/artifacts/phraseforge/roadmap.md` for the remaining templates. Approved by the user; written as `tech-stack.md` v6 (see Documentation Updates).

**Changelog entry mapping:** per `tech-stack.md`'s Artifacts table, `phraseforge` maps to `phraseforge/CHANGELOG.md`. This is a user-facing UI change (visual structure of the header/sidebar, even though pixel-identical) — needs an entry under `## [Unreleased]`.

**Proposed changelog entry (for B5):**

**Category: Changed**
```
- the header's sidebar-toggle/theme-toggle buttons and the sidebar navigation now render via new
  `pf-button`/`pf-nav` Web Components (matching knowledge's own `kb-*` convention), replacing
  inline markup — no visual or behavioral change.
```

## Documentation Updates

Applied the approved `specs/tech-stack.md` change (line 72, v5 → v6, `updated: 2026-09-24`) — see the diff presented and approved above; not duplicated here.

Added to `phraseforge/CHANGELOG.md` under `## [Unreleased]` → `### Changed`: the entry proposed above, documenting the `pf-button`/`pf-nav` header/sidebar componentization.

Removed the `## Now` line for this feature from `specs/artifacts/phraseforge/roadmap.md` (feature is complete, no longer in-flight; it was never re-added to `## Next`/`## Later` since it's done, not deferred).
