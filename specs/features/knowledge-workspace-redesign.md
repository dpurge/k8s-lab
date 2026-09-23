---
title: Redesign knowledge's Ingest tab jobs/drafts split and chat transcript components
kind: feature
status: done
version: 1
updated: 2026-09-23
branch: main
---

## Problem / Motivation

The `knowledge` app's frontend (`knowledge/internal/server/static/`) has been progressively adopting a light-DOM Web Components convention (`kb-button`, `kb-nav`, `kb-card`, `kb-field`, `kb-dialog`, `kb-status-bar`). Prior features shipped: header nav components, footer/dialog/card/field components plus migrating remaining buttons and login/signup forms, and a list/detail view toggle for the Knowledge and Ingest tabs. The outstanding roadmap item `knowledge-workspace-redesign` (`specs/artifacts/knowledge/roadmap.md`, `## Next`) called out three remaining gaps, but a fresh read of the current code shows the actual remaining gaps are narrower than the roadmap text implies (most of what it lists — login/signup, knowledge tab, knowledge detail view, ingest detail — already shipped):

1. The Ingest tab's Jobs list (`renderJob()`, `app.js:545-572`, 27 lines) still builds plain `<div class="item">` markup with inline `onclick` handlers, unlike the Drafts list next to it which already renders `kb-card` elements — inconsistent, and the plain markup has two real bugs: `app.js:343`'s `onclick` attribute interpolates a value with no escaping at all, and `app.js:553`/`app.js:558`'s `esc()`-escaped ids are not actually safe in that context either, because `esc()`'s `'` → `&#39;` encoding is HTML-decoded by the parser before the attribute string is compiled as JavaScript, defeating the escaping. Not currently exploitable (ids are server-generated), but it's a broken control, not a working one.

2. Jobs and Drafts share a single Ingest list view (`#ingestListView`, `index.html:146-151`) as one flat "grid, then `<h2>Jobs</h2>`, then list" stack, rather than being visually or navigationally distinct.

3. The Chat tab's transcript (`renderMsg()`/`renderSource()`, `app.js:319-347`) still builds plain HTML strings instead of a `kb-*` component, and its markdown-rendering helper `md()` (`app.js:315-317`) returns an HTML string that gets inserted, rather than a pre-sanitized node — the reusable-component version of this should make it structurally impossible to `innerHTML` untrusted content.

## Acceptance Criteria

- A new `kb-job-card` component (own directory under `knowledge/internal/server/static/components/kb-job-card/` with `component.js`/`component.css`/`component.test.js`) replaces `renderJob()`'s string-built markup; it takes a `job` property (the full `/api/v1/jobs` item), renders the status badge (pending/active/done/failed via the existing `jobStatusLabel` mapping — pending = running with no step yet, active = running with a step, per `app.js:535-543`), step text, and error text (rendered as literal text, never HTML — must not execute if `job.error` contains markup), and emits `kb-job-retry`/`kb-job-delete` custom events (`detail: {id}`, `bubbles: true`) instead of inline `onclick` handlers, with the same conditional buttons as today (failed → Retry + Delete; other non-running statuses → Delete only; running → no actions). `app.js` retains ownership of the confirm-dialog policy and the actual retry/delete API calls, now triggered from delegated listeners on `#jobs`.

- A new `kb-message` component (own directory `.../components/kb-message/`) replaces `renderMsg()`/`renderSource()`; it accepts a `message` property `{role, contentNode, sources}` where `contentNode` is a DOM node/fragment (not an HTML string) produced by sanitization, uses `data-role="user"|"assistant"` (not the `role` attribute, which is a real ARIA attribute and must not be misused) for styling, appends `contentNode` via `appendChild` (never `innerHTML` on caller-supplied content), and renders a sources list where each source is a real `<a>` whose click emits `kb-source-select` (`detail: {id}`, bubbles) instead of an inline `onclick` — this also fixes an existing unescaped-interpolation bug at `app.js:343`.

- `md()` (`app.js:315-317`) is changed to return a sanitized `DocumentFragment` (via the vendored DOMPurify's `RETURN_DOM_FRAGMENT: true` option) instead of an HTML string; its one caller is updated accordingly.

- `sendChat`'s reply-polling sentinel (`app.js:392`, `393`, `397`, currently `.msg`-based) is updated to match `kb-message` elements — this is a required fix, not optional polish: if missed, every chat send waits the full ~2-minute timeout before giving up, silently, because the poll never detects the reply arrived.

- The Ingest tab's list view gains a Drafts/Jobs sub-workspace toggle: an empty `<kb-nav id="ingestSubNav">` populated via its `items` property (`[{id:"drafts",label:"Drafts"},{id:"jobs",label:"Jobs"}]`), with `#drafts` and `#jobs` each wrapped in a `.subview` element and a `showIngestSection(id)` helper (mirroring the existing `.view`/`showIngestView` pattern) toggling `.subview.active`; the `<h2>Jobs</h2>` heading is removed (the pill now labels it). Clicking a sub-nav pill must not affect the main tab nav (`kb-nav-select` from the nested nav must not reach the main-nav listener — verified by test, since both use the same event name).

- Entering the Ingest tab (via `showTab()`) resets the sub-workspace to `drafts`, in addition to the existing reset to list view.

- Pill labels show live counts (e.g. `Drafts (3)` / `Jobs (5)`), updated whenever `loadDrafts()`/`loadJobs()` run — including a background refresh of a currently-hidden pill (from `pollStatus()`'s auto-refresh), so switching to that pill shows current data without a stale/blank flash.

- The Ingest tab's "Refresh" button refreshes whichever sub-workspace is currently active (or both), not just Drafts as today.

- The Jobs list (`#jobs`) gets the same responsive card-grid CSS as `#list`/`#drafts` for visual consistency now that it occupies the full main width on its own pill.

- No Go/backend change; no new HTTP route (confirmed: `index.html` is already behind `requireAuth`, and neither `login.html` nor `signup.html` references any new component, so no public-route exception is needed per the known `server.go` gotcha).

- All new/changed jsdom component tests pass (`kb-job-card`, `kb-message`, and coverage of the sub-nav toggle's isolation from the main nav), plus `go build/vet/test ./...` and `gofmt -l .` remain green (unaffected, since no Go file changes).

- Live end-to-end validation against the real deployed app confirms: a real ingest job's resulting draft appears under Drafts and the job itself under Jobs; switching pills shows the right content; retry/delete work on a real job; draft open/save/discard still correctly returns to the list view (Drafts pill active); a real chat send renders as a `kb-message` with markdown intact (e.g. a code block renders as an element, not escaped text) and sources listed, and the reply-polling loop terminates on the first successful poll rather than running the full timeout.

## Approach

**Key decisions:**

1. *Sub-workspace toggle*: reuse `kb-nav` (already exactly a pill group driven by an `items` property, firing `kb-nav-select`) rather than building a new toggle component or two manually-managed `kb-button`s. New `.subview`/`.subview.active` CSS pair, kept distinct from the existing `.view`/`.view.active` class (used for the outer list/detail toggle) rather than reusing that class for a second nesting level, to keep `.view` unambiguous for any future `querySelectorAll`. The nested `kb-nav`'s `kb-nav-select` listener is bound to the `#ingestSubNav` element itself (not `document`), so it cannot be intercepted by or collide with the main nav's element-bound listener — this should be covered by an explicit regression test since it's a "looks identical, works differently" risk if anyone later moves either listener to `document`-level delegation.

2. *Jobs componentization*: a new dedicated `kb-job-card`, not a reuse of `kb-card`. `kb-card` binds its click listener to itself and fires `kb-card-select` on any click including on a nested action button, which would misroute a Retry/Delete click into whatever handles `kb-card-select` (built for opening drafts/items) — wrong element for this job. `kb-card` also visually implies clickability (pointer cursor, hover state) that jobs don't have (no detail view). `kb-job-card` takes a `job` property (setter-triggered render, matching `kb-nav.items`'s pattern — chosen over attributes because a job's `error` field can be a multi-line string, awkward and escape-prone as an HTML attribute) and emits `kb-job-retry`/`kb-job-delete` events for `app.js` to handle (confirm dialogs and the actual API calls stay in `app.js`, keeping the component pure/presentational). `jobStatusLabel()`'s mapping logic and the `.job-status*` CSS move into the new component, since it has exactly one caller today.

3. *Chat message componentization*: `kb-message` takes a `contentNode` (a DOM node, not an HTML string) so it is structurally impossible for the component to `innerHTML` untrusted content — the component only ever `appendChild`s or sets `textContent`. `md()` changes to return a sanitized `DocumentFragment` via DOMPurify's `RETURN_DOM_FRAGMENT: true` (confirmed present in the vendored build) instead of an HTML string. Use `data-role`, not the `role` attribute, for user/assistant styling — `role` is a real ARIA attribute and `role="user"` is not a valid ARIA role, which would be an accessibility regression. Sources render as real `<a>` elements with delegated click handling instead of inline `onclick` strings — this also happens to fix an existing unescaped-interpolation bug (`app.js:343` had no escaping at all on an interpolated id inside an executable attribute string).

4. No constitution change needed — `specs/tech-stack.md` already documents the kb-* convention and both new components follow its existing one-directory/`component.js`+`component.css`+`component.test.js` shape exactly.

**Ordered implementation plan** (each step independently deployable via `task deploy-knowledge`, leaving the app working before moving to the next):

1. **`kb-message` + wire the Chat transcript.** New `components/kb-message/{component.js,component.css,component.test.js}`; add its `<script>`/`<link>` to `index.html`; change `md()` in `app.js` to return a sanitized fragment; delete `renderMsg`/`renderSource`; update `openChat` and `sendChat`'s optimistic-insert to build `kb-message` elements; add a delegated `kb-source-select` listener; remove the old `.msg`/`.role`/`.sources` CSS block from `app.css` (moves into the component's own CSS). **Must also fix `sendChat`'s `.msg`-based polling sentinel in this same step** (see Acceptance Criteria) — deploying Step 1 without this fix would ship a real regression (chat replies would appear to take ~2 minutes every time).

2. **`kb-job-card` + wire the Jobs list.** New `components/kb-job-card/{component.js,component.css,component.test.js}`; add its `<script>`/`<link>` to `index.html`; `loadJobs()` in `app.js` builds `kb-job-card` elements instead of calling `renderJob`; delete `renderJob`/`jobStatusLabel` from `app.js` (logic moves into the component); add delegated `kb-job-retry`/`kb-job-delete` listeners on `#jobs`; move `.job-status*` CSS out of `app.css` into the component; remove `.item` CSS from `app.css` only after confirming `renderJob` was its last user.

3. **Drafts/Jobs sub-workspace toggle.** In `index.html`, inside `#ingestListView`: add `<kb-nav id="ingestSubNav">`, wrap `#drafts` and `#jobs` each in a `<div class="subview">`, remove the `<h2>Jobs</h2>` heading. In `app.js`: set `ingestSubNav.items`/initial `active="drafts"`, add `showIngestSection(id)`, wire its `kb-nav-select` listener, reset to `drafts` inside `showTab()` alongside the existing `showIngestView("list")` reset. In `app.css`: `.subview { display: none }` / `.subview.active { display: block }` plus spacing.

4. **Ingest polish** (small, cheap, reversible): pill labels gain live counts from `loadDrafts`/`loadJobs`; `#jobs` gets the same responsive card-grid CSS as `#list`/`#drafts`; the Refresh button refreshes the currently-active sub-workspace (or both) instead of only Drafts.

**Risks & testing strategy:**

- Highest risk: the chat reply-polling sentinel (Step 1) — must be updated in the same step that removes `.msg` elements, or chat sends silently degrade to a ~2-minute wait every time. Covered by an explicit e2e check that the poll terminates on the first successful poll after a real reply lands.
- `kb-job-card` must not let a nested button's click bubble into a card-select-style event (this is exactly why it's not built on `kb-card`) — covered by a jsdom test asserting no stray select event fires when an action button is clicked.
- The nested `kb-nav`'s `kb-nav-select` must not be interceptable by the main nav's listener — covered by a jsdom test asserting a sub-nav pill click doesn't change the active top-level tab.
- `pollStatus()`'s existing auto-refresh of both `loadDrafts()`/`loadJobs()` on job completion is intentionally left as-is (it already only fires on a running→finished transition while the Ingest tab is active, i.e. two GETs per completed job, not a hot loop) — Step 4's live pill counts are what makes a hidden-pill refresh visible/useful instead of invisible.
- `kb-job-card` needs an explicit XSS-regression test: a `job.error` value containing markup (e.g. `<img onerror=...>`) must render as literal text, not create an element — this is the actual security fix, not just a refactor.
- Testing approach (no browser automation available — Chrome DevTools Protocol is blocked by policy on this machine, same constraint noted in every prior frontend feature in this project): jsdom component tests per new component (`kb-message`, `kb-job-card`, sub-nav isolation), Go build/vet/test/gofmt (unaffected, run to confirm no regression), and a live end-to-end script — extracting the real deployed `app.js` and executing it in Node+jsdom against a minimal harness DOM with `window.fetch` shimmed to shell out to `curl` (the same technique used validating `knowledge-list-detail-workspace`) — exercising a real ingest job → draft/job appearing under the correct pill, retry/delete on a real job, and a real chat send/reply with markdown and sources intact.
- Known gap, consistent with every prior frontend feature here: no pixel/visual layout confirmation is possible (CDP blocked); ask the user to spot-check the Ingest pill layout and chat bubble spacing visually after deploy.

## Affected Areas

- `knowledge/internal/server/static/index.html` (script/link tags for two new components; Ingest tab's list-view markup for the sub-nav/subview wrapping; no other structural change)
- `knowledge/internal/server/static/app.js` (`md()`, `openChat`, `sendChat`, `renderMsg`/`renderSource` removal, `loadJobs`, `renderJob`/`jobStatusLabel` removal, `showTab`, new `showIngestSection`, new delegated listeners, Refresh button handler)
- `knowledge/internal/server/static/app.css` (remove `.msg`/`.role`/`.sources` and `.job-status*` blocks — they move into their components' own CSS; add `.subview`/`.subview.active`; extend the responsive grid to `#jobs`)
- new `knowledge/internal/server/static/components/kb-message/` (`component.js`, `component.css`, `component.test.js`)
- new `knowledge/internal/server/static/components/kb-job-card/` (`component.js`, `component.css`, `component.test.js`)
- `specs/artifacts/knowledge/roadmap.md` (`## Next` → `## Now` transition at B2 start, per usual)
- `knowledge/CHANGELOG.md` (this artifact's own changelog, per `specs/tech-stack.md`'s Artifacts table — entry added at B5, not now)

## Out of Scope

- Any Go/backend change, any REST API request/response shape change, any new HTTP route.
- Generalizing the `.view` and `.subview` toggle mechanisms into one shared helper function (explicit decision — two small, working call sites don't justify the abstraction yet).
- Any drag-and-drop, wizard-style, or multi-step flow for the ingest form itself — the "redesign" here is limited to the jobs/drafts split, jobs componentization, and the small polish items listed in the Approach; the URL/file ingest controls in the Ingest tab's `<aside>` are unchanged.
- URL-based routing/deep-linking for the sub-workspace toggle (consistent with the existing list/detail toggle's own out-of-scope decision) — simple in-page state only.
- Pixel-level visual/layout confirmation (no browser automation available on this machine; the user is asked to spot-check visually after deploy, consistent with every prior frontend feature).
- Migrating any component not named above, and any other roadmap item not part of this spec (e.g. any future consolidation of `kb-card` and `kb-job-card`).

## Implementation Notes

All 4 approach steps plus a review-fix pass completed.

**Step 1** — `kb-message` component and Chat transcript wiring: Created new `knowledge/internal/server/static/components/kb-message/` with `component.js`, `component.css`, and `component.test.js`. Added `<script>` and `<link>` tags for the component to `index.html`. Changed `md()` in `app.js` to return a sanitized `DocumentFragment` via DOMPurify's `RETURN_DOM_FRAGMENT: true` option instead of an HTML string. Removed `renderMsg()` and `renderSource()` functions from `app.js`. Updated `openChat()` and `sendChat()` to build `kb-message` elements with the sanitized `contentNode`. Added a delegated `kb-source-select` listener to the chat view. Removed the old `.msg`, `.role`, and `.sources` CSS block from `app.css` (moved into `kb-message`'s own `component.css`). **Critical fix during this step**: Updated `sendChat`'s reply-polling sentinel to key off `kb-message[data-role="assistant"]` elements instead of `.msg` elements, capturing the baseline count before the send/re-render rather than after (a race condition found during self-review and fixed).

**Step 2** — `kb-job-card` component and Jobs list wiring: Created new `knowledge/internal/server/static/components/kb-job-card/` with `component.js`, `component.css`, and `component.test.js`. Added `<script>` and `<link>` tags for the component to `index.html`. Modified `loadJobs()` in `app.js` to build `kb-job-card` elements instead of calling `renderJob()`. Removed `renderJob()` and `jobStatusLabel()` functions from `app.js`, with logic moved into the component. Added delegated `kb-job-retry` and `kb-job-delete` event listeners on `#jobs`. Moved `.job-status*` CSS from `app.css` into `kb-job-card`'s `component.css`. Removed `.item` CSS from `app.css` after confirming no other callers remained.

**Step 3** — Drafts/Jobs sub-workspace toggle: In `index.html`, added `<kb-nav id="ingestSubNav">` inside `#ingestListView` and wrapped both `#drafts` and `#jobs` each in a `<div class="subview">`, removing the `<h2>Jobs</h2>` heading. In `app.js`, set `ingestSubNav.items` to `[{id:"drafts",label:"Drafts"},{id:"jobs",label:"Jobs"}]` with initial `active="drafts"`, added `showIngestSection(id)` helper function, and wired its `kb-nav-select` listener. Updated `showTab()` to reset the sub-workspace to `drafts` alongside the existing `showIngestView("list")` reset. In `app.css`, added `.subview { display: none }` and `.subview.active { display: block }` rules plus appropriate spacing. Verified by test that the nested `kb-nav`'s `kb-nav-select` listener bound to `#ingestSubNav` cannot be intercepted by or collide with the main tab nav's listener.

**Step 4** — Ingest polish: Added live pill count updates to `Drafts (N)` and `Jobs (N)` labels by re-setting `ingestSubNav.items` whenever `loadDrafts()` or `loadJobs()` run (showing `(0)` once a list has loaded and is empty, omitted before first load). Applied the same responsive card-grid CSS to `#jobs` as already existed for `#list` and `#drafts`. Changed the Ingest tab's Refresh button to refresh whichever sub-workspace is currently active (chosen as the smaller diff over tracking active state).

**Self-review pass** (code-review + security-review): Confirmed no `contentNode`/HTML-string re-parsing anywhere in the `kb-message` rendering path; `job.error` is always rendered as literal text via `textContent`. Found and fixed 5 Low-severity issues: (1) the `sendChat` poll-baseline race described in Step 1, (2) added missing `[data-role="user"|"assistant"]` CSS styling (attributes were set but unstyled), (3) unescaped `data-id` interpolation in `renderItem()` and `renderChat()` now wrapped in `esc()` to match the existing pattern in `renderDraft()`, (4) `kb-message`'s setter now `cloneNode(true)`s its incoming `contentNode` before appending to ensure it is safe to call the setter multiple times with the same object, (5) strengthened `kb-job-card`'s "no generic select event" test with a source-text assertion that `component.js` never calls `this.addEventListener(` on itself, matching the component's header-comment invariant. Remaining Low-severity review findings (jobs-list test coverage gaps, a coupling between `app-ingest-subnav.test.js` and `app.js`'s exact source formatting, minor code nits) were deliberately deferred as non-blocking.

**Final file list**: Modified `knowledge/internal/server/static/{index.html,app.js,app.css}`; new `knowledge/internal/server/static/components/kb-message/{component.js,component.css,component.test.js}` and `knowledge/internal/server/static/components/kb-job-card/{component.js,component.css,component.test.js}` and `knowledge/internal/server/static/components/app-ingest-subnav.test.js`. No Go/backend files changed; no new HTTP routes.

## Validation

**Automated test suite:**
- `go build ./...`, `go vet ./...`, `go test ./...` all pass from `knowledge/`. Test coverage: `config`, `ingest`, `qdrant`, and `translate` packages contain real tests (all passing); other packages report `[no test files]`, a pre-existing condition unrelated to this frontend-only feature. `gofmt -l .` flags only `internal/server/server.go`, a pre-existing baseline formatting issue unrelated to this feature.
- JS/component test suite: `npm test` from `knowledge/internal/server/static/components/` (also runnable as `task test-knowledge-frontend`) — all 37 tests passing. New test coverage includes all `kb-job-card` and `kb-message` component tests plus the sub-nav isolation regression test (confirming nested `kb-nav`'s `kb-nav-select` listener does not interfere with main tab nav). Zero regressions.

**Live end-to-end validation:**
- Deployed to the real local k3d cluster via `task deploy-knowledge`, running the real deployed `app.js`, `index.html`, and component files in Node + jsdom with `window.fetch` shimmed to `curl` against the real Traefik-routed backend (same validation technique as the prior `knowledge-list-detail-workspace` feature).
- All six Acceptance Criteria e2e checks passed:
  1. A real ingest job polled to completion produced a real draft and job, each correctly rendered (`kb-card` and `kb-job-card` respectively) under their respective Ingest sub-workspace pill.
  2. Switching between Drafts and Jobs pills shows the matching `.subview` content; verified the sub-nav toggle does not affect the main tab nav's active state.
  3. Retry on a real failed job created a genuinely new job (confirmed via follow-up GET); Delete on a real job removed it (confirmed gone via follow-up GET).
  4. Draft open/save/discard correctly returned to the Drafts list view (`ingestSubNav.active === "drafts"`), and the discard actually deleted it (confirmed 404 on follow-up GET).
  5. A real chat send with a fenced code block and markdown list rendered as real `<pre><code>` and `<ul><li>` DOM elements (not escaped text) in a `kb-message`; a real reply rendered with a real `.kb-message-sources` block; the reply was detected in ~13.6 seconds — well under the ~120s polling timeout, confirming the polling-sentinel fix works.
  6. All test data created during validation (2 test jobs, 1 test chat with message history) was deleted afterward and confirmed gone via follow-up GETs.

**Known limitation (consistent with every prior frontend feature):**
- Chrome DevTools Protocol is blocked by policy on this machine, so no pixel/visual layout confirmation was possible. The user was asked and chose to spot-check the Ingest pill layout, jobs grid, and chat bubble styling visually on their own later, rather than blocking here.

**Residual note on test cleanup:**
- Validating required a real authenticated session, so a real signup was created against the local dev database. The `knowledge` app has no delete-user API, so the account wasn't programmatically cleaned up. The user was asked and explicitly chose to leave it (their own local dev cluster, destroyed on `task stop` anyway).

**Overall verdict:** Clean, no regressions, all six Acceptance Criteria validated end-to-end.

## Documentation Review

**Files audited:**
1. `knowledge/README.md` — architecture and component listing
2. Root `README.md` — project overview (references individual app READMEs)
3. `specs/mission.md` — project problem/users/value proposition
4. `specs/tech-stack.md` — languages, frameworks, infrastructure, conventions, and artifacts table
5. `specs/roadmap.md` — project roadmap (checked for completeness)
6. `knowledge/CHANGELOG.md` — checked for changelog entry mapping

**Drift found — HIGH priority:**

Location: `knowledge/README.md:39`
```
├── internal/server/static/components/ # kb-* Web Components (kb-button, kb-nav); component.test.js per one, Node+jsdom
```

Problem: Lists specific component names (`kb-button`, `kb-nav`) but omits two newly added components now in the codebase:
- `kb-message` (new in this feature)
- `kb-job-card` (new in this feature)

Evidence: `specs/features/knowledge-workspace-redesign.md` Implementation Notes (lines 101, 103) document creation of:
- `knowledge/internal/server/static/components/kb-message/{component.js,component.css,component.test.js}`
- `knowledge/internal/server/static/components/kb-job-card/{component.js,component.css,component.test.js}`

Fix: Update line 39's comment to list all four components: `kb-*` Web Components (`kb-button`, `kb-nav`, `kb-message`, `kb-job-card`).

**No drift found:**
- `specs/tech-stack.md` "Key conventions" → "Frontend components" section correctly describes the convention generically (`<app-prefix>-<name>`, file layout pattern, loading pattern) without enumerating specific components by name — this convention description is timeless and won't drift as new components are added. ✓
- `specs/tech-stack.md` Artifacts table correctly maps `knowledge` artifact to `knowledge/CHANGELOG.md` changelog. ✓
- `specs/mission.md` and `specs/roadmap.md` make no component-specific claims. ✓
- Root `README.md` references app READMEs generically, makes no specific component claims. ✓

**Changelog entry mapping:**

Per `specs/tech-stack.md` Artifacts table (lines 56-67), the `knowledge` artifact is mapped to `knowledge/CHANGELOG.md` for `independent` CalVer versioning. This feature is a user-facing UI change requiring a new entry under `## [Unreleased]`.

**Proposed changelog entry text (do not yet apply — this is for B5):**

**Category: Changed**
```
- the Ingest tab now separates Jobs and Drafts into sub-workspaces with a pill-based
  toggle, each showing a responsive card grid with live item counts. Jobs and chat
  messages now render via new `kb-job-card` and `kb-message` Web Components, replacing
  string-built HTML.
```

**Category: Fixed**
```
- job error text now renders as literal text (not HTML) and chat message source links
  render as real `<a>` elements (not unescaped onclick attributes), eliminating
  HTML-escaping vulnerabilities in those fields.
```

Rationale: The Changed entry documents the user-visible redesign (new components, sub-nav toggle, live counts); the Fixed entry documents the two escaping security improvements (job error XSS, source link attribute injection) that are side effects of the componentization.

**No constitution changes needed:** `specs/tech-stack.md`'s "Frontend components" convention already covers this feature's pattern exactly (light DOM, Web Components, file layout, testing). No changes to `specs/mission.md` or `specs/roadmap.md` required beyond routine Now-line bookkeeping (already done in `specs/artifacts/knowledge/roadmap.md`).

## Documentation Updates

Updated `knowledge/README.md` line 39 to list all 8 `kb-*` Web Components now in the codebase: `kb-button`, `kb-nav`, `kb-card`, `kb-field`, `kb-dialog`, `kb-status-bar`, `kb-message`, `kb-job-card` (previously listed only `kb-button` and `kb-nav`).

Added to `knowledge/CHANGELOG.md` under `## [Unreleased]`:
- **### Changed**: new entry documenting the Ingest tab's Jobs/Drafts sub-workspace toggle with live counts and new `kb-job-card`/`kb-message` Web Components replacing string-built HTML.
- **### Fixed**: new entry documenting the HTML-escaping fixes for job error text (now literal text, not HTML) and chat message source links (now real `<a>` elements, not unescaped onclick attributes).

Removed the `## Now` line from `specs/artifacts/knowledge/roadmap.md` (feature is now complete, no longer in-flight).

No constitution files required updates — `specs/tech-stack.md` already documents the `kb-*` Web Components convention, and this feature follows it exactly.
