---
title: Hash URLs for every screen and numbered page navigation in PhraseForge
kind: feature
status: done
version: 1
updated: 2026-10-08
branch: main
---

## Problem / Motivation

PhraseForge is one SPA shell (`phraseforge/internal/server/templates/app.html`,
`static/js/shell.js`) whose URL never changes. Two consequences, both
confirmed in the code:

- A screen can't be bookmarked or reloaded into: `shell.js` only remembers
  the active *section* in `sessionStorage` (`restoreActiveSection`), never
  the list page or the open item.
- Opening a text from page N of the Texts list and clicking its back link
  returns to page 1: the back link calls `showList()` with no arguments
  (`texts-app.js` view/edit/new back links), and a bare `showList()` resets
  `pageCursors = [null]; pageIndex = 0`. The same pattern is copy-pasted in
  `dialogs-app.js`, `vocabulary-app.js`, `models-app.js`.
- Pagination is Previous/Next only, backed by keyset cursors
  (`internal/pagination`, `ListPage`/`ListAllPage`, page size 25) with no
  total count, so the UI cannot show page numbers or jump.

`texts-app.js`'s header records "no deep-linking" as an earlier explicit
decision (phraseforge-spa-shell-texts). This feature deliberately reverses
it at the user's request.

Decisions confirmed with the user (2026-10-08): offset-based `?page=N` plus
total for numbered pages; URLs for all sections, shipped in slices; list and
view are routable, forms are not; the sidebar language filter stays in
`localStorage`, not in the URL.

## Acceptance Criteria

- [x] Every routable screen has a `#` URL; reloading it, or opening it from
  a bookmark in a new tab, shows the same screen:
  `#/<section>` (list, page 1), `#/<section>?page=N&tag=T` (list),
  `#/<section>/<id>?page=N&tag=T` (view; `page`/`tag` carry the list
  context the item was opened from). Sections: texts, dialogs, vocabulary,
  models, admin, jobs, profile.
- [x] An empty or unknown hash opens `#/texts`. `admin`/`jobs` hashes for a
  non-admin fall back to `#/texts` (same rule as today's `isKnownSection`).
- [x] The browser Back/Forward buttons move between the screens visited.
- [x] The back link on a text/dialog/vocabulary/models view (and on
  edit/new, returning to view or list) returns to the list page and tag
  filter the item was opened from, not page 1.
- [x] List API (`GET /api/v1/texts`, then dialogs/vocabulary/models) accepts
  `?page=N` (1-based, offset) and returns `page`, `pageCount`, `total`
  alongside `items`; filters (language, tag, per-viewer visibility) apply to
  the count exactly as they apply to the items. A page past the last one
  returns empty `items` and the correct `pageCount`; the client then
  replaces the URL with the last page.
- [x] A new pager shows Previous/Next when `pageCount > 1`, and page
  numbers between them only when `pageCount > 2`, with the current page
  marked. At most 10 consecutive numbers are shown, windowed around the
  current page (4 before, 5 after, shifted at either end), with an ellipsis
  on each side that has hidden pages (e.g. `… 9 10 11 12 [13] 14 15 16 17 18 …`).
  Clicking a number navigates to `#/<section>?page=N`.
- [x] Changing the sidebar language filter or a tag filter resets to page 1
  (URL drops `page`). The language filter itself stays in `localStorage`.
- [x] Cross-section links (`pfShowSection("vocabulary", {viewId})`) become
  `#/vocabulary/<id>` navigations; existing callers keep working.
- [x] New/edit/ingest forms have no URL of their own; their screen shows
  under the parent's URL and Back returns to the previous routable screen.
- [x] The Jobs list (admin) follows the same model: `#/jobs?page=N`, numbered
  pager, `GET /api/v1/admin/jobs?page=N` returning `items/page/pageCount/
  total` (without `?page=` the original capped array is returned). Added at
  the user's request (2026-10-08) to make Dialogs, Vocabulary, Models and
  Jobs navigate the same way.
- [x] `?cursor=` keeps working on the API (no removal in this feature);
  Export/Import are unaffected.
- [x] `go test ./...` and the components' `node --test` pass; new tests per
  *Approach*. Each slice is verified running in the dev cluster before the
  next starts.

## Approach

**Route format.** `#/<section>[/<id>][?page=N&tag=T]`, parsed/built by one
small pure module, `static/js/router.js` (loaded before the section
scripts; exposes `pfRouter.parse(hash)`, `pfRouter.build(route)`,
`pfRouter.navigate(route)`, `pfRouter.replace(route)`). `navigate` assigns
`location.hash` so the browser history works for free; a single
`hashchange` listener (and the initial load) hands the parsed route to
`shell.js`.

**Shell.** `shell.js` `showSection(id, opts)` becomes the *renderer* of a
route, not the owner of state: it is called from the `hashchange` handler;
sidebar tab clicks and the profile link call `pfRouter.navigate`. The
`sessionStorage` active-section key is dropped (the hash replaces it).
`pfShowSection(id, {viewId})` is kept as a thin wrapper that navigates to
`#/<id>/<viewId>`, so the existing texts/dialogs links need no changes.

**Sections.** `show(route)` in each section renders the list for
`route.page`/`route.tag` or the view for `route.id`. Module-level
`pageCursors/pageIndex/latestNextCursor` state is deleted. The view's back
link navigates to `#/<section>?page=<route.page>&tag=<route.tag>`, which is
what fixes "back goes to page 1" structurally rather than by remembering
state. List card links navigate to `#/<section>/<id>?page=N&tag=T`.

**Backend (per section).** Add a `Count(ctx, languages, all, tagIDs)` and an
offset variant of the list query on each store (same `WHERE` and
`ORDER BY created_at DESC, id DESC` as `ListPage`, `LIMIT $n OFFSET $m`);
handlers read `?page=`, compute `pageCount = ceil(total/25)` and add
`page/pageCount/total` to the response. Existing cursor path untouched.
Trade-off accepted: offset paging can shift items between pages if rows are
added while browsing; negligible at this data size, and it is what makes
numbered, bookmarkable pages possible. Alternative rejected: keeping keyset
and numbering only visited pages (no jump, no total, bookmarks need opaque
cursors).

**Pager.** New `pf-pager` Web Component in `static/components/pf-pager/`
(same layout as `pf-tabs`/`pf-button`: `component.js/.css/.test.js`,
attributes `page`, `page-count`, event `pf-pager-select`), replacing the
hand-built Previous/Next block in the four `*-app.js` files.

**Slices** (each ends with a run in the dev cluster and your review):
1. `router.js` + shell wiring + `pf-pager` + Texts (backend count/offset,
   list/page/tag/view URLs, back-link fix). Smallest end-to-end proof.
2. Dialogs.
3. Vocabulary.
4. Models.
5. Admin / Jobs / Profile section-level URLs (admin's inner tabs: decide
   when reading `admin-app.js` in this slice).

**Testing.** Go: store `Count`/offset against the existing test DB pattern
(filters, empty, past-last page), handler JSON shape. Node (`node --test`,
jsdom, as for existing components): `router.js` parse/build round-trips and
bad input; `pf-pager` rendering (≤2 pages: Previous/Next only; >2: numbers,
ellipsis, current marked; click emits event). Manual in the dev cluster
(KUBECONFIG `.k3d/kubeconfig`, `phraseforge.localhost`): open page 3 of
Texts, open a text, Back lands on page 3; reload on a view URL; browser
Back/Forward; bookmark opened in a fresh tab.

**Risks.** (a) Unauthenticated bookmark: `/login` then redirect to `/` may
drop the fragment; to be checked in slice 1 and reported, not silently
widened. (b) Browser Back from an edit form discards unsaved edits, same
as today's back link. (c) Section DOM is cleared on section switch
(`shell.js` id-collision rule); the router must keep calling that path.

## Affected Areas

- `phraseforge/internal/server/static/js/shell.js` (route-driven)
- `phraseforge/internal/server/static/js/router.js` (new)
- `phraseforge/internal/server/static/js/{texts,dialogs,vocabulary,models,admin,jobs,profile}-app.js`
- `phraseforge/internal/server/static/components/pf-pager/` (new)
- `phraseforge/internal/server/templates/app.html` (script tags)
- `phraseforge/internal/{texts,dialogs,vocabulary,models}/*.go` (count/offset)
- `phraseforge/internal/server/{server,dialogs,vocabulary,models}.go` (list handlers)
- `phraseforge/internal/pagination/` (page-size/offset helper, if needed)
- `phraseforge/CHANGELOG.md`

## Out of Scope

- URLs for new/edit/ingest forms; language filter in the URL.
- Removing `?cursor=` / `nextCursor` from the API.
- Configurable page size; server-side routes other than `/` (the hash never
  reaches the server).
- Knowledge and Dictionary apps.
- Preserving unsaved form input across navigation.

## Implementation Notes

Slice 1 (Texts), then Dialogs, Vocabulary, Models and Jobs, all deployed to
the dev cluster (`task deploy-phraseforge`, KUBECONFIG `.k3d/kubeconfig`).

- `internal/pagination`: `ParsePage`, `Offset`, `PageCount` (+ unit tests).
- Stores `texts`, `dialogs`, `vocabulary`, `models` gain `ListOffset` and
  `Count`; `jobs.Service` gains `ListPage` and `Count` (id tiebreaker, no
  500-row cap in paged mode). List handlers read `?page=` (offset mode) and
  otherwise keep the `?cursor=` path unchanged.
- `static/js/router.js` (`parse/build/navigate/replace/setUrl/start`);
  `navigate` to the hash already shown re-renders directly.
- `shell.js` renders from the route; `ROUTED_SECTIONS` (texts, dialogs,
  vocabulary, models, jobs) receive the whole route in `show(route)`; admin
  and profile are called bare for now. `pfShowSection` is a wrapper that
  navigates.
- `texts/dialogs/vocabulary/models-app.js`: module-level cursor state
  replaced by `listContext` + `goTo`; list/view render from the route; back
  links, delete, create and tag links keep the page/tag context. Vocabulary
  and Models create sets the new item's URL with `pfRouter.setUrl` before
  showing Manage. `pf-pager` replaces the hand-built Previous/Next block.
- `jobs-app.js`: `loadAndRender(page)`, pager, clamp to last page.
- `pf-pager` shows at most 10 consecutive numbers with an ellipsis on each
  side that has hidden pages (user request, 2026-10-08).
- Not done yet: Admin and Profile section URLs (`#/admin`, `#/profile` work
  as section-level routes; admin's inner tabs are not routed).

## Validation

Baseline before changes: `go test ./...` and `npm test` (components) green,
31 JS tests. Final: `go build`, `go vet ./...`, `go test ./...` green;
`npm test` 45/45 (router: 5, pf-pager: 9 incl. the all-pages window
property check, plus the existing suites). Not unit-tested: the store SQL
(`ListOffset`/`Count`, no DB harness in the repo) — run by hand against the
dev Postgres: texts page 2 = 25 rows of 54, jobs page 3 = 11 of 61, the
language-ANY and tag-id variants parse. Section scripts verified by the
user in the dev cluster (phraseforge.localhost:8080) after each deploy: list
pages, view/Back to the same page, reload/bookmark URLs, Dialogs,
Vocabulary, Models and Jobs navigation ("looks good", 2026-10-08). Not
exercised: the 10-number window with real data (the user has too few texts;
covered by unit tests only), and an unauthenticated bookmark through the
login redirect.

## Documentation Review

- `phraseforge/CHANGELOG.md` (`## [Unreleased]`): Added — hash URLs for
  list/item screens; numbered pager incl. Jobs paging and the `?page=` API
  fields. Fixed — Back returns to the originating list page.
- README, `specs/mission.md`, `specs/tech-stack.md`: no navigation or
  pagination statements to correct; no constitution change.
- Stale in-code comments ("single-URL model", "no deep-linking") in the
  four section scripts were updated with the change.

## Documentation Updates

- Wrote the two Added entries and one Fixed entry in
  `phraseforge/CHANGELOG.md` under `## [Unreleased]`.
- Removed this feature's `## Now` line from
  `specs/artifacts/phraseforge/roadmap.md`.
- Follow-ups not built: Admin inner-tab URLs; checking whether `#` survives
  the login redirect for an unauthenticated bookmark.
