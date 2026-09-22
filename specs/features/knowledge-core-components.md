---
title: Build kb-status-bar, kb-dialog, kb-card, and kb-field; rewire the interface around them
kind: feature
status: done
version: 2
updated: 2026-09-22
branch: main
---

## Problem / Motivation

The prior two features (`knowledge-header-nav-components` and `knowledge-list-detail-workspace`) under-delivered relative to the user's actual ask: a real component system covering the app's whole interface, with visual polish. As the user stated directly: "Only 2 components extracted? ... still one giant unreadable spaghetti ... I think you are VERY FAR from what I have asked for." Two concrete issues emerged:

1. The footer/status area lost its fixed height in the layout fix from `knowledge-header-nav-components`, so it visibly jumps in size between empty and populated states.
2. Native `confirm()` dialogs (six call sites: `importFile`, `removeItem`, `deleteChat`, `approveDraft`, `discardDraft`, `deleteJob`) look out of place — they must feel like part of the app or not ask for confirmation at all.

This feature builds four more `kb-*` components per the established convention in `specs/tech-stack.md` (light-DOM custom elements, `static/components/<tag>/` file layout, `component.test.js` under Node+jsdom), and — critically, unlike the prior two features — actually rewires the existing interface to be built from them, not just adds them alongside. The result is a real component-based architecture covering the core interactions, not scattered token improvements.

After this feature's first pass shipped, the user pushed back hard: "Only 2 components extracted? ... buttons that are not ported to kb-button... index.html is still a long messy file... same for login, same for signup... I am asking you for this for the 5-th or so time." The first pass fell far short of addressing the entire application — 20 remaining plain `<button>` elements across both tabs never converted to `kb-button`, `login.html`/`signup.html` never touched despite being part of "the whole application," and `index.html` remaining a single ~1000-line file (inline `<style>` + inline `<script>` + markup all together) instead of a small neat composition of components. A second, more complete pass followed immediately in the same session, addressing all of that: every remaining button, the login/signup pages themselves, and the CSS/JS extraction that turns `index.html` into readable markup.

## Acceptance Criteria

- Each of the four components exists under `knowledge/internal/server/static/components/<tag>/` per the established file-layout convention, with a `component.test.js` under Node+jsdom.
- Footer uses `<kb-status-bar>`; its rendered height does not change between empty and populated states (the bug is fixed); `message()`'s ~10+ existing call sites are unchanged.
- All 6 native `confirm()` call sites use `kb-dialog`'s `.confirm()` instead; no native `confirm()`/`alert()` remains anywhere in `index.html`.
- `renderItem`/`renderDraft`/`renderChat` each emit a `<kb-card data-id="...">` instead of a `<div class="item"|"chat" onclick="...">`; `#list`/`#drafts`/`#chats` each have exactly one delegated `kb-card-select` listener wired once; clicking a card still opens the right item/draft/chat.
- All 12 `<label>` form-field blocks are replaced with `<kb-field label="..." for="...">`; every field keeps its existing `id`, placeholder, type, and behavior — only the label markup/styling changes.
- The Jobs list (`renderJob`) is unchanged (explicitly out of scope for `kb-card`).
- No backend/Go change; no change to any REST API call, request/response shape, or business rule (the approve-in-flight guard, what each confirm's message says, etc.) — only the presentational/interaction layer.
- `go build`/`vet`/`test` unaffected; the frontend `component.test.js` suite (existing + new) all pass under `task test-knowledge-frontend`.
- Every remaining plain `<button>` across the whole app (20 in `index.html`: Search/New/Export/Import/both Generate/Save/Delete in Knowledge; Create chat/Delete chat/Send in Chat; Ingest URL/Upload file/Refresh/Save/Approve/Discard in Ingest; Retry/Delete×2 in the Jobs list) is now `<kb-button>`, with `variant="danger"` preserving what `class="danger"` used to mark.
- `login.html` and `signup.html` are also built from the same components (`kb-button` for the theme-toggle and submit buttons, `kb-field` for the Email/Password fields) — "the whole application" now includes them, not just the main app shell.
- `index.html`'s inline `<style>` (250 lines) and inline `<script>` (563 lines) are extracted into external files (`app.css`, `app.js`), leaving `index.html` itself at 180 lines of markup — a real "small composition," not a description of one. `login.html`/`signup.html` similarly shrank from ~120 lines each to ~32-33, sharing a new `theme.css` (the CSS variable palette, used by all three pages) and a new `auth.css`/`auth.js` (the login/signup-specific styling and their now-deduplicated, parameterized form-submit handler — previously two nearly-identical inline scripts).
- A real regression was found and fixed along the way: `knowledge/internal/server/server.go`'s `requireAuth`-gated catch-all route doesn't cover new static files a *public* page depends on — `theme.css`/`auth.css`/`auth.js` and the `kb-button`/`kb-field` component files needed explicit public routes added (a new `serveStaticGlob` helper for whole component directories), or an unauthenticated visitor would get a completely unstyled, non-functional login page.
- A second real regression was found and fixed: splitting the original combined stylesheet into `theme.css` (variables only) and `app.css`/`auth.css` (page-specific rules) accidentally dropped `body`'s own `background: var(--bg)`/`color: var(--fg)`/`font-family` — the variables themselves were fine (proven by `kb-button`/`kb-field`, which consume them directly and looked correctly themed), but the page's own layout never consumed them, so dark mode looked broken everywhere except inside components. Caught by the user visually inspecting dark mode; fixed by restoring those three properties to both `app.css` and `auth.css`.
- `go build`/`vet`/`test` and the full `component.test.js` suite continue to pass unaffected; live end-to-end validation (same Node+jsdom+curl-shimmed-fetch technique used throughout this session) covered the Knowledge tab's full New/Save/Open/Delete flow, the Ingest tab's tab-reset behavior, and a real signup+login flow through the actual `login.html` markup and its real `auth.js`, all against the real backend.

## Approach

1. **Build `kb-status-bar`** — replaces the footer's two floating `<span>` elements (`#jobStatus`, `#message`). Has a fixed height regardless of content (fixing the jump bug), models its visual treatment on phraseforge's own already-proven `.status-bar` pattern (found during earlier research: fixed area, success/error color state) — implemented independently here (deliberate duplication, not shared files). Exposes two properties/methods the existing `message()` function and `pollStatus()`'s job-status line call into, so the ~10+ existing call sites to `message(text, error)` need no change — only `message()`'s own internals and one line in `pollStatus()` change.

2. **Build `kb-dialog`** — an in-app confirmation modal (styled overlay, `kb-button`-styled OK/Cancel actions, Escape-to-cancel, click-outside-to-cancel), replacing all 6 native `confirm()` call sites (`importFile`, `removeItem`, `deleteChat`, `approveDraft`, `discardDraft`, `deleteJob` — all already `async function`s, so each site's `if (!confirm("...")) return;` becomes `if (!(await confirmDialog.confirm("..."))) return;` with no other refactoring needed). A single `<kb-dialog id="confirmDialog">` instance lives once in the page (not created per-call), with a `.confirm(message)` method returning a Promise<boolean>.

3. **Build `kb-card`** — extracts the click-to-select card wrapper currently duplicated as the `.item`/`.chat` CSS classes plus an inline `onclick="run(() => open...(id))"` string-templated handler, in `renderItem()`, `renderDraft()`, and `renderChat()`. `kb-card` owns the border/radius/padding/hover styling and dispatches a `kb-card-select` event (detail: `{ id }`) on click; each list container (`#list`, `#drafts`, `#chats`) gets ONE delegated event listener (added once) instead of per-card inline handlers. The three render functions keep their differing inner content (item has a score/updated-at line; draft has source-kind/ref/chunk-index; chat has a scope-tags line) — `kb-card` deduplicates the wrapper/interaction, not content that's genuinely different per use. The Jobs list (`renderJob`) is explicitly excluded — its rows have their own inline Retry/Delete buttons and no "open" behavior, so the click-to-select semantic doesn't fit; it stays a plain styled row.

4. **Build `kb-field`** — replaces the 12 `<label>...<input>/<textarea></label>` blocks across both tabs' forms. Deliberately does NOT wrap/move its input child (avoiding the Custom-Elements timing bug hit and fixed in `kb-button` this session — connectedCallback firing before light-DOM children are parsed): instead, `<kb-field label="Title" for="title"><input id="title" /></kb-field>` has the component simply *prepend* a new `<label for="...">` element as its first child in `connectedCallback` — prepending a brand-new node never depends on the existing children already being present, so no timing hazard exists. The `for` attribute is required (explicit, not auto-detected) for the same reason. Visual style: a small block-level label above the field (matching phraseforge's own `label.field-label` convention, independently implemented), replacing today's implicit "input's own `width:100%` forces a line-wrap inside an inline `<label>`" hack.

5. **Rewire each usage site, in this order:**
   - `kb-status-bar` (smallest blast radius, fixes a live bug) — replace the two floating `<span>`s in the footer with a `<kb-status-bar>` element; update `message()` and the one `pollStatus()` call site to use the component's properties/methods instead of directly mutating DOM.
   - `kb-dialog` (medium — 6 call sites, all already-async functions) — add the `<kb-dialog id="confirmDialog">` element; replace each of the 6 native `confirm()` call sites with `await confirmDialog.confirm(...)`.
   - `kb-field` (mechanical, 12 sites, no logic change) — replace each `<label>...<input/textarea></label>` block with `<kb-field label="..." for="..."><input/textarea id="..."></kb-field>`.
   - `kb-card` (touches 3 render functions + adds delegated listeners + removes old CSS) — update `renderItem()`, `renderDraft()`, and `renderChat()` to wrap their content in a `<kb-card data-id="...">` instead of a `.item`/`.chat` div with inline `onclick`; remove the old `.item`/`.chat` CSS classes and the inline handler; add a single delegated `kb-card-select` listener to each of `#list`, `#drafts`, `#chats`.

6. **Convert every remaining plain button in `index.html` to `kb-button`; convert `login.html`/`signup.html` to use `kb-button`/`kb-field`; extract `index.html`'s inline CSS/JS into `app.css`/`app.js`, and `login.html`/`signup.html`'s duplicated CSS/JS into shared `theme.css`/`auth.css`/`auth.js`; fix the two regressions found along the way (public-route allowlist gap in `server.go`, dropped `body` theme properties in the CSS split); re-validate everything live.**

7. **End with full live validation** using the same technique proven this session (extract the real deployed inline script, run it in Node+jsdom against a minimal harness with `fetch` shimmed through `curl`, exercise real flows against the real backend) plus an explicit ask to the user for a visual spot-check, since no browser-automation tool is available this session and pixel-level appearance (the actual thing most under dispute here) can't be confirmed by this session alone.

## Affected Areas

- `knowledge/internal/server/static/index.html` (all of it)
- `knowledge/internal/server/static/components/kb-status-bar/` (new)
- `knowledge/internal/server/static/components/kb-dialog/` (new)
- `knowledge/internal/server/static/components/kb-card/` (new)
- `knowledge/internal/server/static/components/kb-field/` (new)
- `knowledge/internal/server/static/login.html`
- `knowledge/internal/server/static/signup.html`
- `knowledge/internal/server/static/app.css` (new)
- `knowledge/internal/server/static/app.js` (new)
- `knowledge/internal/server/static/theme.css` (new)
- `knowledge/internal/server/static/auth.css` (new)
- `knowledge/internal/server/static/auth.js` (new)
- `knowledge/internal/server/server.go`
- `specs/roadmap.md` (removes the old `knowledge-tab-components` `## Later` item, replaced by this feature moving through Next → Now → done)

## Out of Scope

- Porting any of this to `phraseforge`/`dictionary`.
- Any backend/Go change.
- A `kb-list` component wrapping the grid container itself (only the card, not the list, is componentized this pass).
- Componentizing the Jobs list's rows.
- Any new visual redesign beyond what's needed to build these four components in (no new color palette, no new information architecture) — this is "build the components the interface should be made of," not a second design pass.

## Implementation Notes

Built `knowledge/internal/server/static/components/kb-status-bar/` (`component.js`, `component.css`, `component.test.js`): renders a `.kb-status-job`/`.kb-status-message` pair, fixed `min-height: 2.4rem` regardless of content (fixes the footer-jump bug), `setJobStatus(text)`/`setMessage(text, isError)` methods. Footer now contains just `<kb-status-bar id="statusBar">`; `message()`'s body became a one-line call into `setMessage`; `pollStatus()`'s job-status line became a call into `setJobStatus`. All ~10 other call sites to `message(text, error)` needed no change.

Built `knowledge/internal/server/static/components/kb-dialog/` (`component.js`, `component.css`, `component.test.js`): a fixed-position overlay modal built from two internal `<kb-button>` elements (Cancel=`secondary`, OK=`primary`/`danger` depending on a `danger` option), with a `.confirm(message, {danger, okLabel})` method returning `Promise<boolean>`, resolved by OK/Cancel click, Escape, or clicking the overlay outside the box. One `<kb-dialog id="confirmDialog">` instance added once near the footer. All 6 native `confirm()` call sites (`importFile`, `removeItem`, `deleteChat`, `approveDraft`, `discardDraft`, `deleteJob`) now `await confirmDialog.confirm(...)` — the destructive ones (`importFile`, `removeItem`, `deleteChat`, `discardDraft`, `deleteJob`) pass `{danger: true}`; `approveDraft` (not destructive) uses the default. No native `confirm()`/`alert()` remains anywhere in `index.html`.

Built `knowledge/internal/server/static/components/kb-card/` (`component.js`, `component.css`, `component.test.js`): a plain clickable wrapper (border/radius/padding/hover, no Shadow DOM) dispatching `kb-card-select` (detail `{id}`) on click. `renderItem()`, `renderDraft()`, `renderChat()` now emit `<kb-card data-id="...">` instead of `.item`/`.chat` divs with inline `onclick`; `#list`/`#drafts`/`#chats` each got one delegated `kb-card-select` listener added once at boot, calling `openItem`/`openDraft`/`openChat` respectively — replacing the per-card inline handlers. `renderJob()` was deliberately left alone (still a plain `.item`-styled div, no click-to-select semantic — its own Retry/Delete buttons are the only interaction) — a `.item` CSS rule (bordered box, no cursor/hover) was kept specifically for this now-sole remaining use, since the old shared `.item, .chat` rule (which also had `cursor:pointer`/hover, appropriate for the removed click-to-navigate behavior) was replaced by `kb-card`'s own CSS for the two uses that actually navigate.

Built `knowledge/internal/server/static/components/kb-field/` (`component.js`, `component.css`, `component.test.js`): prepends a `<label class="kb-field-label" for="...">` as the element's first child in `connectedCallback` — deliberately never moves or depends on the input/textarea child already being present (unlike `kb-button`'s wrap-existing-content approach), so the exact class of timing bug found and fixed in `kb-button` doesn't apply here by construction. Added a defensive `MutationObserver` that re-inserts the label if it's ever removed, after finding jsdom (not confirmed to also affect real browsers) silently reverted a synchronous `insertBefore` sometime after `connectedCallback` returned for an element written in static HTML — see the new `specs/memory.md` `[gotcha]` entry. All 12 `<label>...<input>/<textarea></label>` blocks across both tabs' forms (and the Chat tab's "New chat title"/"Scope tags") became `<kb-field label="..." for="...">`, each keeping its existing `id`/placeholder/type unchanged.

No backend/Go change; no change to any REST API call, request/response shape, or business rule (the approve-in-flight guard, each confirmation's message text, the destructive/non-destructive distinction) — only the presentational/interaction layer.

Converted all 20 remaining plain `<button>` elements across both tabs to `<kb-button>` (Search/New/Export/Import/both Generate buttons in Knowledge; Create chat/Delete chat/Send in Chat; Ingest URL/Upload file/Refresh/Save/Approve/Discard in Ingest; Retry/Delete×2 in the Jobs list), preserving `variant="danger"` on the 5 destructive buttons that previously used `class="danger"`. Rewrote `login.html` and `signup.html` to use `<kb-button>` for the theme-toggle and form-submit buttons, and `<kb-field label="Email" for="email">` and `<kb-field label="Password" for="password">` for the two form fields — both pages now follow the same component pattern as the main app. Extracted `index.html`'s inline 250-line `<style>` and 563-line `<script>` into external `app.css` and `app.js`, reducing `index.html` to 180 lines of markup; similarly extracted `login.html`/`signup.html`'s duplicated CSS and nearly-identical form-submit handlers into shared `theme.css` (the CSS variable palette, `--bg`/`--fg`/theme defaults), `auth.css` (login/signup-specific layout and styling), and `auth.js` (the parameterized, deduplicated form-submit handler, called identically from both pages), reducing each auth page to ~32-33 lines of markup. Added a new `serveStaticGlob` helper to `knowledge/internal/server/server.go` and explicit public routes for `theme.css`, `auth.css`, `auth.js`, and the entire `components/kb-button/` and `components/kb-field/` component directories (fixing the first regression: an unauthenticated visitor's login page would otherwise receive a 302 redirect for every static file it depended on, breaking styling and functionality). Fixed the second regression by restoring `body { background: var(--bg); color: var(--fg); font-family: ... }` to both `app.css` and `auth.css` — the CSS split accidentally dropped these lines, so dark mode looked broken everywhere except inside the two components that explicitly consumed the variables (see the new `specs/memory.md` entries for details on both regressions).

## Validation

All 15 `component.test.js` tests (across all 6 `kb-*` components, including the 4 new ones) pass under Node+jsdom via `task test-knowledge-frontend`.

`go build`/`vet`/`test`/`gofmt -l .` all pass, unaffected (no Go code touched).

Live-deployed via `task deploy-knowledge`.

Full end-to-end validation against the real deployed app and real backend, using the same technique proven on the two prior features this session (real inline app-script extracted verbatim from the live page, executed in Node+jsdom against a minimal harness, `fetch` shimmed through `curl` since Node's own resolver/fetch can't reach the Traefik-routed `*.localhost` backend directly): confirmed `message()`/`pollStatus()` correctly drive `kb-status-bar`'s job/message text and error-state class; created a real knowledge item, confirmed the real rendered `<kb-card>` for it exists and that clicking it (a real DOM click, not a direct function call) fires the delegated listener and opens the correct item; exercised `kb-dialog`'s full real flow twice against the real `DELETE` endpoint — cancelling the confirmation left the item untouched (confirmed via a follow-up `GET` returning 200), then confirming it actually deleted it (confirmed via a follow-up `GET` returning 404). Separately confirmed via `curl` that the live-served markup for a representative `kb-field` (`Title`/`for="title"`) matches what was authored. All test data cleaned up (confirmed via a follow-up query returning no results).

Known gap (same as all three prior features this session): no real browser visual/pixel confirmation — the actual "does it look good" question this whole feature was about can't be confirmed by this session alone; Chrome's remote-debugging/DevTools Protocol is blocked by an admin policy on this machine. Ask the user for a visual spot-check, in particular: the footer no longer jumping in height between empty/populated, the dialog's overlay/box appearance, the card grid, and the field labels.

Full frontend test suite (`task test-knowledge-frontend`) still passes; live-deployed via `task deploy-knowledge` (twice — once for the conversion, once more for the `server.go` routing fix); confirmed via `curl` that `login.html`/`signup.html` and every file they depend on (`theme.css`, `auth.css`, `auth.js`, `theme.js`, both components' `.js`/`.css`) are reachable *without* authentication (200), while the main app's own `app.css`/`app.js` correctly remain behind the login redirect (302 unauthenticated); a real signup followed by a real login through the actual `login.html` markup (extracted verbatim and executed for real, same technique as the rest of this session) succeeded end-to-end against the real backend; re-ran the earlier Knowledge-tab and Ingest-tab flow tests against the restructured deployment and confirmed they still pass (one assertion about an unrelated, pre-existing default list-endpoint result cap flaked — confirmed via a direct query to be a data-volume artifact unrelated to this change, not a regression). No Go test/build/vet impact from the frontend changes; `server.go`'s own change (`gofmt`/`vet`/`build`/`test`) passed clean.

## Documentation Review

No drift found beyond what's already covered by the existing entries; the constitution files remain accurate (the Web Components convention in `specs/tech-stack.md` already covers this work, no new convention was introduced).

## Documentation Updates

`CHANGELOG.md` gains one more bullet (this write, below) noting the second pass: every remaining button plus login/signup now componentized, CSS/JS extracted.
