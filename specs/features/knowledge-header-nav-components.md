---
title: Style knowledge's header navigation and buttons with Web Components
kind: feature
status: done
version: 2
updated: 2026-09-22
branch: main
---

## Problem / Motivation

Knowledge's header currently mixes primary navigation (Knowledge/Chat/Ingest tab selectors) with global controls (Qdrant link, user email, theme toggle, log out), all rendered as plain `<button>` elements with no visual distinction between nav and actions. Buttons across the app have no styling beyond browser defaults (no background color, no border-radius, no hover states) — functionally correct but visually "ugly." The user confirmed that the header itself is the right place for navigation; it just needs proper visual styling via real components instead of raw buttons.

After discussion, the user chose: native Web Components (Custom Elements, light DOM) as the shared *convention* across all three apps (see `specs/tech-stack.md`'s new Frontend components entry) — deliberate per-app duplication instead of a shared-files mechanism; Node+jsdom for dev-time component tests only. This feature is deliberately scoped small as the *first* slice: establish the convention with two components (button, nav) and use them to restyle just the header — not a full redesign of every screen. The remaining ~15 buttons and per-tab panels are a separate roadmap item (`knowledge-tab-components`, not yet speced). Separately, the existing per-tab `<aside>`/`<main>` layout (which today shows search results as cramped tiles in the sidebar) is a larger, identified problem being tracked as a distinct roadmap item (`knowledge-list-detail-workspace`, not yet speced), covering both Knowledge and Ingest tabs together.

## Acceptance Criteria

- [x] `specs/tech-stack.md`'s Web Components convention (naming, file layout, loading, light-DOM+self-injecting-CSS, testing, per-app-duplication sharing model) is the one this feature follows — point to it, don't restate it.
- [x] `knowledge/internal/server/static/components/kb-button/component.js` (+ `component.css`): a custom element wrapping a real internal `<button>` (light DOM, for correct keyboard/focus/ARIA semantics — not a `<div>` pretending to be a button), forwarding `disabled` and `type` attributes to the inner `<button>`, and supporting a `variant` attribute (`primary` default, `secondary`, `danger`, and an `icon` variant for compact icon-only buttons like theme-toggle) that maps to distinct visual styles (background color, border, hover state) — fixing the "buttons look ugly" complaint. Content (a slotted `<button>` label) passes through via a real `<slot>` or equivalent light-DOM child projection.
- [x] `knowledge/internal/server/static/components/kb-nav/component.js` (+ `component.css`, `component.html`): a custom element taking a list of nav items (label + identifier) via an attribute/property, rendering them with one marked active, and styled as a real navigation control (e.g. tab-like or pill appearance with a clear active state) rather than plain buttons. Dispatches a `kb-nav-select` `CustomEvent` (detail: `{ id }`) on click — it does NOT call `showTab()` itself or know that function exists; the consuming page listens for the event and wires it to `showTab()`. This keeps the component testable/reusable independent of knowledge's specific tab-switching mechanism.
- [x] `knowledge/internal/server/static/index.html`: the header's Knowledge/Chat/Ingest navigation (currently three plain `<button>` elements) is replaced with a `<kb-nav>` component (three items: Knowledge, Chat, Ingest) styled inline within the header (no new layout column or grid change). The header's theme-toggle and Log out controls become `<kb-button>` instances (theme-toggle as `variant="icon"`, Log out as `variant="secondary"`). The header's branding/title and Qdrant link remain unchanged. A `kb-nav-select` listener is wired to the existing `showTab()` function.
- [x] All existing functionality in all three tabs (search/list/edit in Knowledge, chat creation/messaging, URL/file ingest) continues to work exactly as before — this feature only restyls the header's nav and action buttons, not any tab's internal behavior, data flow, or layout.
- [x] The header and footer status bar remain always visible (pinned) exactly as today, with only the per-tab `<aside>`/`<main>` scrolling internally — but not via the current `.tab.active { height: calc(100vh - 54px - 2.4rem) }` magic-number rule, which hardcodes the header's exact pixel height (`54px`) and silently breaks (gap, or the header/footer scrolling out of view) the moment the header's real height changes, which restyling it in this feature risks doing. Replace the outer page layout (`body`/header/tab-content/footer) with a `height:100vh` CSS Grid (`grid-template-rows: auto 1fr auto`) or equivalent Flexbox column, so header and footer take their natural height and the middle row absorbs the remainder — robust to any future header/footer height change, not just this one.
- [x] The existing per-tab `<aside>`+`<main>` markup (filters/list in the sidebar, workspace content in the main area) and the `#list`/`#drafts` rendering (currently showing cramped result tiles) are deliberately untouched by this feature — that redesign is tracked separately as the `knowledge-list-detail-workspace` roadmap item.
- [x] Both components have a `component.test.js` (Node + `jsdom`) covering their core rendering/event behavior (e.g. `kb-button` renders a real `<button>` and forwards `disabled`; `kb-nav` renders all items with exactly one active and dispatches `kb-nav-select` with the right `detail` on click).
- [x] No change to `dictionary` or `phraseforge`, and no Go/backend code change in `knowledge` — this slice is static-frontend-only in `knowledge`, plus the `tech-stack.md` constitution update.

## Approach

1. `specs/tech-stack.md`: land the Web Components convention entry (constitution change, gated separately — already drafted alongside this spec).
2. Build `kb-button`: internal real `<button>` element, `variant` attribute mapped to CSS classes for primary/secondary/danger/icon, forwards `disabled`/`type`, re-dispatches or lets the native `click` event bubble naturally (no need to synthesize a custom event — a real inner `<button>` click already bubbles through the custom element).
3. Build `kb-nav`: accepts items (e.g. a JS property set from `index.html`'s own script — `document.querySelector('kb-nav').items = [...]` — rather than a stringified HTML attribute, since items are a small structured list, not primitive text), renders them from its `component.html` template styled as a visible navigation control (not plain buttons), tracks/updates the active item via an `active` property/attribute, dispatches `kb-nav-select` on click.
4. `index.html`: add the two `<script src="/components/.../component.js">` includes; restructure the header by dropping the three tab-switch `<button>` elements and replacing them with a `<kb-nav>` element styled inline within the header (no new grid column, no layout restructuring); convert the theme-toggle and logout controls to `<kb-button>` instances; wire the `kb-nav-select` listener to call the existing `showTab(tab)`; replace the `calc(100vh - 54px - 2.4rem)` height rule with a `height:100vh` grid/flex layout (`auto`-sized header and footer rows, `1fr` middle row) so the pinned header/footer behavior survives any header height change, this feature's included.
5. Write `component.test.js` for both components.
6. Live-validate: deploy, visually confirm the restyled header and nav component, click through all three tabs confirming active-state highlighting and that every existing feature (search, chat, ingest) still works unchanged.

Testing strategy: `component.test.js` per component under Node+jsdom, per the new tech-stack.md convention; live/manual validation for the visual/integration result, consistent with this app's existing lack of broader frontend test infrastructure (a pre-existing condition, not something this feature is expected to fully solve).

Risks: this is the first real use of the new Web Components convention in this codebase — some conventions (attribute vs. property passing, exact CSS variable names to reuse/add) may need adjusting once actually built; keeping the slice small (2 components, header only) limits the blast radius of getting an early convention detail wrong, since later components can still adjust before wider rollout.

## Affected Areas

- `specs/tech-stack.md` (constitution update)
- `specs/roadmap.md` (this item moves Next → Now at B2 start, per usual)
- `knowledge/internal/server/static/components/kb-button/` (new)
- `knowledge/internal/server/static/components/kb-nav/` (new)
- `knowledge/internal/server/static/index.html`

## Out of Scope

- Migrating any of the app's other ~15 buttons (Search/New/Generate ×2/Save/Delete/Create chat/Delete chat/Send/Ingest URL/Upload file/etc.) to `kb-button` — that's `knowledge-tab-components`, a separate later roadmap item.
- Redesigning the per-tab `<aside>`/`<main>` list/detail workspace (both Knowledge and Ingest tabs have this problem — cramped result tiles in the sidebar instead of a proper list/detail layout) — tracked separately as the `knowledge-list-detail-workspace` roadmap item, not yet speced.
- Porting phraseforge's or dictionary's navigation/buttons to this convention, or building any `pf-*`/`dc-*` components.
- Any shared cross-app file-sharing or build mechanism (a Taskfile-copy approach was explicitly considered and rejected in favor of deliberate duplication).
- Any backend/Go code change.

## Implementation Notes

Added CSS variables `--accent`/`--accent-hover` to `index.html`'s `:root`/`:root[data-theme="dark"]` blocks (required by `kb-button`'s `primary` variant and `kb-nav`'s active-item highlight; both components define `var(--accent, #2f6fed)` fallbacks for graceful degradation). Built `knowledge/internal/server/static/components/kb-button/` with `component.js`, `component.css`, `component.test.js`: wraps a real inner `<button>`, forwards `disabled`/`type` via `observedAttributes`/`attributeChangedCallback`, supports `variant` (`primary`/`danger`/`icon`; base rule serves as `secondary`). CSS is self-injected as a `<link rel="stylesheet">` rather than a fetched `<style>` tag — a simpler mechanism achieving the documented convention's outcome (page author needs only the `<script>` tag); noted as an implementation refinement, not a deviation.

Built `knowledge/internal/server/static/components/kb-nav/` with `component.js`, `component.css`, `component.html`, `component.test.js`: accepts `items`/`active` JS properties, renders from a fetched-and-cached `<template>` (real separate file per spec), dispatches `kb-nav-select` with `{id}` detail. Updates its own `active` state on click; host page's `showTab()` also sets `mainNav.active` on every call so nav highlight stays in sync with programmatic tab switches (via `openItem()`/`openDraft()`).

Updated `index.html`: added `<script src="/components/.../component.js">` includes for both; replaced three tab-switch `<button>`s with `<kb-nav id="mainNav">`; converted theme-toggle and Log out to `<kb-button variant="icon">`/`<kb-button variant="secondary">`; added `mainNav.items`/`.active` setup and `kb-nav-select` listener near `showTab()`; `showTab()` now also sets `mainNav.active = tab`.

Fixed pinned layout per explicit requirement: replaced `.tab.active { height: calc(100vh - 54px - 2.4rem) }` with `body { display:grid; grid-template-rows: auto 1fr auto; height:100vh }` (header/footer auto-sized, `.tab` in `1fr` middle row). Worked around CSS gotcha: grid items' default `min-height:auto` overrides the track's size, so `.tab` needed explicit `min-height:0` to let `<aside>`/`<main>`'s existing `overflow:auto` scroll internally rather than breaking the layout (documented in `specs/memory.md`'s 2026-09-22T17:12:27Z entry; reference it, don't restate).

Dev tooling: `knowledge/internal/server/static/components/package.json` (`jsdom ^30.1.1`, `type:module`, `test` script for Node's built-in test runner), `package-lock.json`. Added `test-knowledge-frontend` task to root `Taskfile.yml` (`npm install && npm test` in that directory). Added `node_modules/` to `.gitignore`.

Pre-existing doc drift fixed (per this project's recorded gotcha): `knowledge/README.md`'s architecture tree was missing `internal/server/static/vendor/` (from earlier `knowledge-markdown-chat-render` feature) and `internal/server/static/components/` — both added.

**Post-deployment fix:** the user reported the theme-toggle and Log out buttons "do not render correctly" ("icon and text render next to the border, outside of it") after this feature shipped. Root cause: `kb-button`'s `connectedCallback` fired the instant the browser inserted `<kb-button>`'s *opening* tag during HTML parsing — before the parser had appended the emoji/text between the tags — so the synchronous `while (this.firstChild)` wrap-loop moved nothing, leaving an empty styled `<button>` with the real content stranded as a sibling text node next to it. `kb-nav` was unaffected (confirmed by the user: "Navigation buttons look fine") because its markup is always empty at parse time — it only ever receives content via the `items` JS property, set later.

First fix attempt (`queueMicrotask` deferring the wrap one microtask past `connectedCallback`) passed a jsdom-based repro built to simulate real parse-order timing, and was deployed — but the user confirmed via DevTools ("Copy outerHTML") after a hard refresh that the *exact same broken shape* persisted (`<button type="button"></button>Log out`), ruling out both browser/service-worker caching (no service worker exists in this app) and the microtask-timing theory itself: jsdom's approximation of streaming-parse timing did not hold in real Chrome. Replaced it with a `MutationObserver` on `this` (`childList`), which reacts to the actual DOM mutation whenever it happens rather than assuming any fixed number of microtask ticks — confirmed fixed by the user after this redeploy, again via a fresh DevTools outerHTML dump. Elements that already have content at `connectedCallback` time (e.g. anything created dynamically via JS, and every existing test) skip the observer and wrap synchronously; only elements written in static HTML take the observer path, briefly. Negligible performance cost either way (currently 2 instances, ≤~17 after the full `knowledge-tab-components` rollout; each observer fires once and disconnects). Added a regression test (`kb-button/component.test.js`) that executes the real component source from an inline `<head>` `<script>` against HTML containing pre-written light-DOM content, reproducing real parse-order timing more faithfully than the original tests (which built elements programmatically, setting content before insertion, and so never exercised this ordering at all).

## Validation

6 `component.test.js` tests (4 for `kb-button` including the parse-order regression test, 2 for `kb-nav`) pass under Node+jsdom via `task test-knowledge-frontend`, exercising real rendering/event behavior: inner-button wrapping, attribute forwarding, click bubbling; item rendering with exactly one active, `kb-nav-select` dispatch with correct detail, component never calls host functions directly.

**Real-browser confirmation (post-fix):** unlike the rest of this feature's validation (jsdom/Node only, no browser tool available this session), the button-wrapping fix was ultimately confirmed against an actual Chrome session — the user pasted the real rendered `outerHTML` from DevTools both before (`<button type="button"></button>Log out`, confirming the bug) and after (correct nesting, confirming the `MutationObserver` fix) the second redeploy.

`go build ./...`/`go vet ./...`/`go test ./...`/`gofmt -l .` all pass, unaffected (no Go code touched).

Live-deployed via `task deploy-knowledge`; confirmed via `curl` (authenticated session) that all 5 new files (`kb-button` and `kb-nav`'s `.js`/`.css`, plus `kb-nav`'s `.html`) are served with `200`, and live-served `index.html`'s header markup matches authored markup (`<kb-nav id="mainNav">`, both `<kb-button>` instances).

Wiring logic validated by fetching real live-served `component.js`/`component.html` bytes and replicating `index.html`'s exact boot-time code inside Node+jsdom against that real fetched code: initial active tab/nav-highlight match, nav clicks switch visible tab and nav highlight correctly, previously-active tab loses `active` class, programmatic `showTab()` (simulating `openItem()`/`openDraft()`) keeps nav highlight in sync.

Known gap: full interactive/visual confirmation in an actual browser was not possible (no browser automation tool connected this session) — above is real-code execution, not rendered-visual check. Pre-existing constraint noted in `knowledge-markdown-chat-render` feature's Validation section; not new to this feature.

## Documentation Review

`knowledge/README.md`'s architecture tree was checked against the code and had drift: missing `internal/server/static/vendor/` (from `knowledge-markdown-chat-render` feature) and `internal/server/static/components/` subdirectories — both fixed. No constitution files required further changes beyond `specs/tech-stack.md` (already updated before implementation start, cited below). Routine `specs/roadmap.md` Now-removal (this write, see Documentation Updates).

## Documentation Updates

`knowledge/README.md`'s architecture tree gained two new lines (`internal/server/static/vendor/` and `internal/server/static/components/`) — already completed. `specs/tech-stack.md`'s Frontend components entry (Web Components convention: naming, file layout, loading, light-DOM+self-injecting-CSS, testing, per-app duplication) was approved and landed before implementation (see git history or `tech-stack.md:##` Frontend components). `CHANGELOG.md` gains a new entry under `## [Unreleased] ### Changed` documenting the knowledge header navigation and button restyling via Web Components (this write, see CHANGELOG update). `specs/roadmap.md`'s `## Now` line for this feature is removed (this write, see roadmap update).
