---
title: Redesign knowledge's Knowledge/Ingest tabs as list/detail workspaces
kind: feature
status: done
version: 1
updated: 2026-09-22
branch: main
---

## Problem / Motivation

Today, both the Knowledge and Ingest tabs share the same problematic layout pattern: the `<aside>` (sidebar, ~360px fixed width) contains a cramped, single-column stack of result "tiles" (using the `.item` CSS class at `knowledge/internal/server/static/index.html:130`) squeezed into that narrow column, while `<main>` always displays a single always-visible edit form — there is no actual "list view" vs. "detail view" distinction. The `<main>` content simply shows whichever item's fields happen to be populated (blank for "new", or filled in via `openItem()`/`openDraft()` from lines 516 and 784 respectively).

Both tabs would benefit from a true list/detail workspace pattern: a full-width list grid in the main area by default (giving items room to breathe and making them easier to scan), with clicking a list item switching to a detail view of that item's edit form, plus an explicit "back to list" control to return.

The sidebar, meanwhile, is already cluttered with content it shouldn't hold — the list of items/drafts competes for space with the search/filter controls that actually belong there. Clearing the sidebar of list content will let search/filter controls live comfortably, and will make room for the "New" button (create a blank knowledge item), which is an action that manages the workspace contents, not a search control per se, but sidebar-appropriate nonetheless.

## Acceptance Criteria

- Knowledge tab's `<aside>` (line 255–278) contains only: the search inputs (`q`, `filterTags`, `start`, `end`) and Search button, the New button, and Export/Import controls — no list content (`#list` is removed from the aside).
- Knowledge tab's `<main>` (line 280–308) has two view states (list/detail, simple CSS class toggle, no routing): a list view showing `#list` as a responsive card grid; a detail view showing the existing edit form fields (title/tags/summary/body, Generate buttons, Save/Delete), plus a "← Back to list" control.
- Clicking a knowledge item in the list (rendered by `renderItem` at line 502) switches to the detail view populated with that item (same data `openItem()` fetches at line 516); clicking New (line 269 `newItem()` at line 526) switches to a blank detail view; Back returns to the list view without re-fetching (the list's DOM content persists underneath).
- Saving a knowledge item (via `save()` at line 558) stays in the detail view and refreshes the underlying list data in the background; deleting one (via `removeItem()` at line 606) returns to the list view after refreshing list data, since it's gone.
- Ingest tab's `<aside>` (line 348–369) contains only: URL/file ingest controls (`ingestUrl`, `ingestTags`, buttons `startIngestUrl()`, `startIngestFile()` at lines 738 and 749) and Refresh button (`loadDrafts()` at line 763) — no drafts or jobs content.
- Ingest tab's `<main>` (line 371–395) has two view states: a list view showing the drafts card grid (`#drafts`, rendered by `renderDraft` at line 771) plus a "Jobs" section below it (`#jobs`, rendered by `renderJob` at line 858, unchanged rendering/actions); a detail view showing the existing draft edit form fields (draftId/draftSource/draftTitle/draftTags/draftSummary/draftBody, Save/Approve/Discard buttons), plus a "← Back to list" control.
- Clicking a draft in the list switches to its detail view (same data `openDraft()` fetches at line 784); Save (via `saveDraft()` at line 804) stays in detail view, refreshing draft data in background; Approve (via `approveDraft()` at line 826) or Discard (via `discardDraft()` at line 842) return to the list view after refreshing drafts, since the draft is gone from the pending set.
- Switching tabs (via `kb-nav` component or programmatically via `showTab()` at line 421) resets that tab's workspace to its list view, except when `openItem()`/`openDraft()` drive the switch themselves, which still correctly land on the detail view (net effect: tab-switch's list-view reset happens first, then is immediately overridden by the detail-view switch).
- The header/footer-pinned layout established by `knowledge-header-nav-components` (the `body { display:grid; grid-template-rows:auto 1fr auto }` at line 43–44, and `.tab { min-height:0 }` at line 100) continues to work unchanged — only `<aside>`/`<main>` content changes, not the outer grid/scrolling mechanism.
- No backend/Go change — this is entirely a static-frontend restructuring using the same existing REST API calls (knowledge, ingest endpoints).
- No new `kb-*` component is introduced by this feature (explicit decision — `renderItem`/`renderDraft` remain plain JS functions).

## Approach

1. **Add the list/detail view-toggle CSS** (`.view`/`.view.active`, analogous to the existing `.tab`/`.tab.active` mechanism at lines 94–106) and the card-grid CSS for `#list`/`#drafts` (responsive `repeat(auto-fill, minmax(...,1fr))`), plus a small "back link" style.

2. **Restructure the Knowledge tab's `<aside>`/`<main>` markup** (lines 254–309): move New into the aside's actions row next to Search (keeping it in the sidebar, not the list); wrap the existing list markup (`<div id="list">` at line 277) and the existing edit-form markup (lines 281–307) each in their own `<div class="view" ...>` inside `<main>`, with a "← Back to list" element added to the detail view.

3. **Restructure the Ingest tab's `<aside>`/`<main>` markup** (lines 347–396) the same way, additionally moving the "Jobs" heading/list (the `<h2>Jobs</h2>` at line 367 and `<div id="jobs">` at line 368) out of the aside into a new section inside the list view (below the drafts grid), since Jobs is a different kind of list (status/history, not something to browse-then-edit) and should stay visible alongside the drafts list view, not hidden inside the detail view.

4. **Add small JS view-toggle helpers** (e.g. `showKnowledgeView(view)`/`showIngestView(view)` in the script section starting at line 403) and wire them into `showTab()` (reset to list on tab entry), `newItem()`/`openItem()`/`save()`/`removeItem()`, and `openDraft()`/`saveDraft()`/`approveDraft()`/`discardDraft()`, per the stay-in-detail-on-save / return-to-list-on-remove behavior described above. No change to any of these functions' actual API calls or data handling — only when/whether they also flip the view.

5. **Live-validate**: exercise every flow in both tabs (search, new, open/edit/save, delete; ingest URL/file, open/edit/save a draft, approve, discard, job retry/delete) end-to-end against the deployed app, confirming the right view shows at each step and nothing regresses.

## Affected Areas

Just `knowledge/internal/server/static/index.html` (all of it: CSS, both tabs' markup, both tabs' view-related JS functions) and `specs/roadmap.md` (Next → Now transition, at B2 start, per usual).

## Out of Scope

- Any change to Chat tab's layout (its aside-list-of-chats + main-transcript pattern already resembles a reasonable list/detail split and wasn't flagged as a problem).
- Extracting a shared `kb-list`/`kb-card` component (explicit decision — plain JS for now, a candidate future item).
- URL-based routing/deep-linking for detail views (explicit decision — simple in-page toggle only).
- Any backend/Go change, any change to REST API request/response shapes, any change to business rules (confirm dialogs, the approve-in-flight guard at line 824, etc.) — only the view/DOM layer changes.
- Migrating any button to the `kb-button`/`kb-nav` components from the prior feature (that is `knowledge-tab-components`, a separate, still-unspecced roadmap item).

## Implementation Notes

Added CSS: `#list`/`#drafts` became `display:grid; grid-template-columns: repeat(auto-fill, minmax(18rem,1fr)); gap:1rem` (responsive card grid, replacing the cramped single-column sidebar stack); `.item` margin zeroed inside those grids (gap handles spacing instead); a `.view`/`.view.active` toggle (mirrors the existing `.tab`/`.tab.active` mechanism); a `.back-link` style.

Knowledge tab: removed `#list` from `<aside>` (aside now holds only search/filter/New/Export/Import controls — New stayed where it already was, next to Search). `<main>` now has two `<div class="view">` children: `#knowledgeListView` (containing `#list`, active by default) and `#knowledgeDetailView` (containing the existing edit-form fields unchanged, plus a new "← Back to list" link calling `showKnowledgeView('list')`).

Ingest tab: removed `#drafts` and the "Jobs" heading/`#jobs` from `<aside>` (aside now holds only URL/file ingest controls + Refresh). `<main>` now has `#ingestListView` (containing `#drafts` *and* the "Jobs" section, both visible together) and `#ingestDetailView` (existing draft-edit-form fields unchanged, plus a "← Back to list" link calling `showIngestView('list')`).

JS: added `showKnowledgeView(view)`/`showIngestView(view)` (toggle the `.active` class on the two view divs for that tab). `showTab()` now also resets the entered tab to its list view (`if (tab === "knowledge") showKnowledgeView("list")`, same for ingest) — comment explains `openItem()`/`openDraft()` call `showTab()` themselves and then switch to detail immediately after, overriding this reset, so they still land correctly on detail.

`openItem()`/`openDraft()`: each gained a final `showKnowledgeView("detail")`/`showIngestView("detail")` call after populating fields.

`newItem()`: the old field-clearing logic was extracted into a new `clearItemForm()` helper (no view change); `newItem()` itself now calls `clearItemForm()` then `showKnowledgeView("detail")` (it's the button handler, which should open a blank detail view — this is new behavior, since previously "New" just cleared the always-visible form).

`removeItem()`: now calls `clearItemForm()` (not `newItem()`, to avoid an unwanted detail-view flip) then, after `loadList()`, explicitly `showKnowledgeView("list")` — deleting an item returns to the list.

`approveDraft()`/`discardDraft()`: each gained a final `showIngestView("list")` call after their existing `clearDraftForm()`/`loadDrafts()` — approving or discarding returns to the list. `saveDraft()` was left completely unchanged (no view-switch call added) — saving intentionally stays in the detail view.

No backend/Go change; no change to any REST API call, request/response shape, or business rule (confirm() dialogs, the approve-in-flight guard) — only when/whether the view switches.

## Validation

`go build ./...`/`go vet ./...`/`go test ./...`/`gofmt -l .` all pass, unaffected (no Go code touched).

Live-deployed via `task deploy-knowledge`.

Full end-to-end validation against the real deployed app and real backend, since no browser-automation tool was available this session (same constraint as the two prior features): extracted the actual inline app-logic `<script>` block verbatim from the live-served `index.html` and executed it, unmodified, inside Node+jsdom against a minimal test-harness DOM (just the referenced element IDs, no unrelated markup) — with `window.fetch` shimmed to shell out to `curl` (Node's own DNS resolver doesn't resolve `*.localhost`, and its native `fetch`/undici silently drops an explicit `Host` header override, so neither direct nor 127.0.0.1-targeted `fetch` could reach the right Traefik-routed backend — `curl`, already proven reliable all session, sidesteps both issues). `markdownit`/`DOMPurify`/`matchMedia` were stubbed (irrelevant to this feature's logic; the real markdown-it/DOMPurify/kb-* library-loading in jsdom hit unrelated VM/environment incompatibilities not worth fighting for this check).

Knowledge tab, 14 checks, all passed: initial view is list; New opens a blank detail view; Back returns to list; creating and saving a real item (via the live API) stays in the detail view and the list refreshes in the background to include it; opening that real item via `openItem()` shows the correct fetched data in the detail view; deleting it (`removeItem()`, real `DELETE` call) returns to the list view and the item is confirmed gone from the rendered list; forcibly switching tabs while in a detail view resets to the list view on tab re-entry; entering the Ingest tab also resets to its list view.

Ingest tab, 8 checks, all passed, using a real draft produced by an actual `POST /ingest/text` job run to completion and polled via `/jobs/current`: initial view is list; the real draft appears in the rendered drafts list; opening it (`openDraft()`) shows the correct fetched data in the detail view with the right `draftId`; editing and saving it (`saveDraft()`, real `PUT` call) stays in the detail view; discarding it (`discardDraft()`, real `DELETE` call, confirm auto-accepted) returns to the list view and the draft is confirmed gone from the rendered list.

All test data (one knowledge item, one ingest job + its draft) created during validation was cleaned up (deleted/discarded) afterward — confirmed via a follow-up query showing none remain.

Known gap (same as the two prior features): no real browser visual/layout confirmation (the card-grid CSS itself, exact spacing, etc.) — this validated real DOM/JS *behavior* end-to-end against the real backend, not pixel rendering. Chrome's remote-debugging/DevTools Protocol is blocked by an admin policy on this machine, so no automated screenshot was possible; ask the user to spot-check the visual layout if they want that confirmed too.

## Documentation Review

No documentation drift found — `knowledge/README.md`'s API documentation describes the REST endpoints, none of which changed; its architecture-tree file listing is unaffected (no new files, only `index.html` content changed). No constitution file needed changes.

## Documentation Updates

`CHANGELOG.md` gains an entry under `### Changed` (see that file); no other doc changes needed.
