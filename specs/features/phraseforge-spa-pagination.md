---
title: Next/Previous pagination for Texts/Dialogs/Vocabulary/Models lists
kind: feature
status: done
version: 1
updated: 2026-09-26
branch: main
---

## Problem / Motivation

Every list endpoint (`apiListTexts`/`apiListDialogs`/`apiListVocabLists`/
`apiListModelsLists`, `phraseforge/internal/server/{server,dialogs,
vocabulary,models}.go`) currently fetches and returns *every* matching row —
`texts.Store.List`/`dialogs.Store.List`/`vocabulary.Store.ListAll`/
`models.Store.ListAll` all run an unbounded `SELECT ... ORDER BY created_at
DESC` with no `LIMIT`. This works today because every language's content is
small, but doesn't scale: a language with thousands of texts/dialogs/
vocabulary lists/model lists would mean a multi-thousand-row fetch, JSON
payload, and full card-grid render on every single list view.

Confirmed while researching this feature: the four handlers are otherwise
structurally identical (same `ViewableLanguages` → optional single-language
narrowing via `?language=` → `List`/`ListAll` → optional `?tag=` filtering
→ per-item tag lookup → JSON response), and tag filtering already has the
server-side query methods it needs
(`tags.Store.ResourceIDsWithTag`/`ResourceIDsWithAllTags`,
`phraseforge/internal/tags/tags.go`) — today's handlers already call
`ResourceIDsWithTag` and filter the *already-fetched* full list by ID in
Go (`filterByID`), rather than filtering at the SQL level before any
`LIMIT` — that in-memory-after-fetch ordering is exactly what breaks once
pagination is added (a page-sized DB fetch could yield zero
tag-matching rows even when matches exist further back), so this feature
moves the tag-ID filter into the `WHERE` clause itself.

## Acceptance Criteria

- [ ] A new `phraseforge/internal/pagination` package (shared pure logic,
  not a per-package duplicated wire type — this is common utility code all
  four resources need identically, unlike a job payload's independent-
  versioning wire contract): a `Cursor{CreatedAt time.Time; ID int64}`
  type, `Encode`/`Decode` to/from an opaque string token, and a
  `DefaultLimit = 25` constant (this feature's own chosen page size — not
  client-configurable via query param, to keep the surface area small; a
  page-size selector is a possible future follow-up, not built here).
- [ ] `texts.Store`/`dialogs.Store`/`vocabulary.Store`/`models.Store` each
  gain a `ListPage(ctx, languages []string, all bool, tagIDs []int64,
  cursor *pagination.Cursor, limit int) (items []T, hasMore bool, err
  error)`: `tagIDs` nil means no tag filter; non-nil (including empty)
  restricts to exactly those ids via `WHERE id = ANY($n)`, computed by the
  caller from the existing `ResourceIDsWithTag`/`ResourceIDsWithAllTags`
  *before* this query runs. Keyset pagination on `(created_at, id) DESC`
  (a compound key, not `id` alone — an imported/backfilled row could
  legitimately have a `created_at` that doesn't match insertion/id order,
  same as today's plain `ORDER BY created_at DESC` already assumes).
  Fetches `limit+1` rows, trims the extra one, and sets `hasMore`
  accordingly — the standard "peek one past the page" technique, no
  separate `COUNT(*)` query. The existing unpaginated `List`/`ListAll`
  methods are **kept, unchanged** — Export still needs every row.
- [ ] Each of the four `apiList*` handlers reads an optional `?cursor=`
  query param (opaque, from `pagination.Decode`; a missing/invalid value
  is treated as "first page", not a 400 — matches this app's existing
  lenient-query-param conventions elsewhere), computes the tag-filtered id
  set (if `?tag=` is present) the same way it already does today just
  *before* calling `ListPage` instead of after, and returns `{"items":
  [...], "nextCursor": <string, omitted/null when there is no next page>}`
  — every existing field on each item summary (`apiTextSummary`/
  `apiDialogSummary`/`apiVocabSummary`/`apiModelsSummary`) is unchanged.
  Vocabulary/Models' per-item `Items()` call for `itemCount` now runs over
  one page's items instead of the full list — strictly cheaper, no
  behavior change to the field itself.
- [ ] `texts-app.js`/`dialogs-app.js`/`vocabulary-app.js`/`models-app.js`'s
  `showList` gains Previous/Next buttons below the card grid — Previous
  hidden/disabled on the first page, Next hidden/disabled when the
  response's `nextCursor` is absent. No backend "go backward" capability
  is needed: the frontend keeps a small in-memory stack of cursors it has
  already seen (module-level state, reset whenever the language or tag
  filter changes, or the section is left and re-entered) — Next pushes the
  just-returned `nextCursor`, Previous pops the stack and re-fetches with
  the popped value.
- [ ] Changing the sidebar language filter or a tag filter always resets
  to page 1 (clears the cursor stack) — matches how `showList` already
  fully re-renders on any filter change today.
- [ ] Export/Import (`apiExportTexts`/`.../Dialogs`/`.../Vocabulary`/
  `.../Models`, `phraseforge/internal/server/export_import*.go`) are
  **unaffected** — they keep calling the existing unpaginated `List`/
  `ListAll` directly, since a bulk export must include everything
  regardless of the UI's own page size.
- [ ] `go test ./...` passes; new tests cover: `pagination.Encode`/
  `Decode` round-tripping (including a malformed/garbage input decoding
  to an error the caller treats as "no cursor"); each store's `ListPage`
  keyset boundary (an item exactly at the cursor is excluded, not
  repeated) and `hasMore` computation (`limit+1`-row peek), against a live
  DB with a small seeded set of rows spanning more than one page.

## Approach

1. **`phraseforge/internal/pagination`** (new package): `Cursor`,
   `Encode`/`Decode` (base64 of a small JSON `{created_at, id}` — RFC3339Nano
   for the timestamp, to round-trip exactly), `DefaultLimit`. Pure,
   DB-free, fully unit-tested here.
2. **Store layer**: add `ListPage` to `texts.Store`/`dialogs.Store`/
   `vocabulary.Store`/`models.Store`, alongside (not replacing) the
   existing `List`/`ListAll`. Same `all`/`languages` branch structure
   already used; add the `tagIDs`/cursor/limit conditions to the `WHERE`
   clause and the `ORDER BY .../LIMIT` tail.
3. **HTTP layer**: update `apiListTexts`/`apiListDialogs`/
   `apiListVocabLists`/`apiListModelsLists` to decode `?cursor=`, resolve
   the tag filter to an id set *before* calling `ListPage` (reusing the
   exact same `ResourceIDsWithTag`/`ResourceIDsWithAllTags` calls, just
   reordered), call `ListPage` with `pagination.DefaultLimit`, encode the
   next cursor from the last returned item (or omit it when `!hasMore`).
4. **Frontend**: each `showList` gains a `cursor` parameter and a
   module-level cursor-stack; render Previous/Next `pf-button`s below the
   `.card-grid`; wire their clicks to re-invoke `showList` with the
   popped/pushed cursor. New i18n keys (en/pl) for the two button labels.

**Testing strategy**: entirely DB/pure-logic level — no LLM calls involved
in this feature at all. `pagination` package: pure unit tests. Store
`ListPage`: tested against the live k3d DB (seed a handful of rows spanning
2+ pages, confirm correct page boundaries and `hasMore`) — the same
live-DB-verification approach already used for this session's schema/index
work, not a new testing convention. HTTP layer: existing handler test
conventions in this codebase are pure-function-only (no DB-backed
`httptest` harness exists here — confirmed earlier this session); covered
instead by live curl verification after deploy, matching how every other
API-layer change this session was verified.

## Affected Areas

- `phraseforge/internal/pagination` (new package + test file)
- `phraseforge/internal/texts/texts.go`, `dialogs/dialogs.go`,
  `vocabulary/vocabulary.go`, `models/models.go` (new `ListPage`/
  `ListAllPage` methods; no new Go test files here — see Validation on why
  this is verified live instead)
- `phraseforge/internal/server/server.go`, `dialogs.go`, `vocabulary.go`,
  `models.go` (the four `apiList*` handlers)
- `phraseforge/internal/server/static/js/texts-app.js`, `dialogs-app.js`,
  `vocabulary-app.js`, `models-app.js`
- `phraseforge/internal/i18n/i18n.go` (+ each section's own `*AppI18nKeys`
  whitelist — a real bug found twice earlier this session when this step
  was missed)

## Out of Scope

- A page-size selector or `?limit=` query param — fixed at
  `pagination.DefaultLimit` (25) for this feature.
- Pagination of a single Vocabulary/Models list's *items* (item-level
  add/edit/delete stays exactly as it is) — only the four top-level
  resource lists (Texts/Dialogs/Vocabulary/Models) are paginated.
- Any change to Export/Import, which must keep seeing every row.
- Jump-to-page-N navigation — strictly Next/Previous, matching the
  roadmap item's own wording.

## Implementation Notes

1. **`phraseforge/internal/pagination`** (new package): `Cursor{CreatedAt,
   ID}`, `Encode`/`Decode` (base64 of a small JSON shape, RFC3339Nano
   timestamp), `DefaultLimit = 25`. `Decode` returns an error for any
   malformed/garbage input — every caller treats that as "no cursor," never
   a 400.
2. **Store layer**: added `ListPage` (texts/dialogs) / `ListAllPage`
   (vocabulary/models — mirroring their existing `ListAll` naming), each
   alongside its unpaginated sibling (kept, unchanged, still used by
   Export). Two query shapes per store (admin `all=true` vs.
   language-filtered), each with `($n::bigint[] IS NULL OR id = ANY($n))`
   for the optional tag-ID restriction and `($n::timestamptz IS NULL OR
   (created_at, id) < ($n, $n+1))` for the optional cursor — both guarded
   so a nil Go slice/pointer becomes a real SQL `NULL`, not an empty-array
   false-filter. Fetches `limit+1` rows, trims to `limit`, sets `hasMore`.
3. **HTTP layer**: added a shared `decodeCursorParam(r) *pagination.Cursor`
   helper (package `server`); all four `apiList*` handlers now compute the
   tag-filtered ID set *before* calling the paginated store method (moving
   `ResourceIDsWithTag`'s existing call site earlier, not adding a new
   query), call `ListPage`/`ListAllPage` with `pagination.DefaultLimit`,
   and add `nextCursor` to the response only when a further page exists.
   `filterByID` (the old post-fetch in-memory filter) is untouched — it's
   still used by Export/Import's own unpaginated paths.
4. **Frontend**: each of the four `showList` functions gained a
   `pageDirection` parameter and three module-level pagination-state
   variables (`pageCursors`, `pageIndex`, `latestNextCursor`) — any call
   without an explicit `pageDirection` (initial load, a language-filter
   change via the sidebar, a tag-filter link/clear click) resets to page 1;
   `"next"`/`"prev"` advance/retreat the index using only cursors already
   seen client-side, so no backend "go backward" capability was needed.
   Previous/Next `pf-button`s render below the card grid, only when
   relevant (Previous hidden on page 1, Next hidden with no further page).
   New `texts.pagination_previous`/`_next` i18n keys (en/pl), reused by
   all four sections (added to all four `*AppI18nKeys` whitelists —
   learned earlier this session that missing this step silently shows the
   raw key instead of the label).
5. Self-reviewed the diff — scoped exactly to the Approach's four steps;
   no findings.

## Validation

- `go build`/`vet`/`test ./... -count=1` pass; `node --check` on all four
  edited JS files. New pure unit tests: `pagination.Encode`/`Decode`
  round-trip (including a nanosecond-precision, non-trivial timestamp) and
  four malformed-input cases, all correctly erroring.
- **Keyset boundary, live k3d DB** (rolled back after): inserted 4
  throwaway rows in a single `INSERT` — meaning all 4 share the *exact
  same* `created_at` (a real edge case: proves the `(created_at, id)`
  compound key, not `created_at` alone, since a tie on the primary sort key
  is exactly what would silently reorder/repeat/skip rows with a
  single-column sort). Ran the literal `ListPage` SQL by hand:
  - Page 1 (`limit=2`, no cursor) → `[19, 18]`, third row (`17`) present in
    the `limit+1` peek → `hasMore=true`. Matches Go's `out[:limit]` trim.
  - Page 2 (cursor = page 1's last item, `(that created_at, 18)`) →
    `[17, 16]`, plus the pre-existing real row (`id=10`, a different,
    earlier `created_at`) as the peek → `hasMore=true`.
  - No row skipped (18 correctly excluded from page 2 via the strict `<`)
    and no row repeated across the two pages.
  - Cleaned up: `DELETE FROM texts WHERE title LIKE 'pagetest%'` — back to
    the 1 pre-existing row.
- **Live deploy verification** (`task deploy-phraseforge`): schema
  unaffected (no migration needed — this feature is query-level only);
  `GET /api/v1/texts?cursor=bogus` returns its normal 302-unauthenticated
  redirect (not a 500), confirming a malformed cursor is handled
  leniently even through the full HTTP stack, not just in the unit test;
  all four served JS bundles contain the pagination code
  (`pagination_previous`/`_next`/`latestNextCursor` all present).
- No LLM calls involved in this feature at all.
- **Related, found and fixed during this session's live testing**: the
  production Helm chart (`jdp-helm/jdp-frontend`, a separate repository)
  had the identical "prompt/timeout only in Go's compiled-in defaults, not
  the deployed config" gap this session's `llm-purpose-timeout-and-
  prompt-config` feature was supposed to close — `values.yaml`'s own
  comment explicitly said it was mirroring the lab `configmap.yaml`'s
  *previous* (incomplete) state. Fixed there too (`values.yaml` +
  `templates/phraseforge.yaml`'s ConfigMap template), verified with
  `helm template`/`helm lint` (no deploy). Out of scope for *this* spec's
  own Affected Areas (different repo, different feature) — noted here only
  because it surfaced while validating this feature's own deploy.

## Documentation Review

Affected Areas map to the `phraseforge` artifact only — no fan-out.

**Changelog entry needed** (user-facing feature, category `Added`):

- `{phraseforge/CHANGELOG.md, Added, "Previous/Next pagination on the Texts/Dialogs/Vocabulary/Models list pages (25 items per page, most recent first) — scales to a language holding thousands of items instead of fetching and rendering every row at once."}`

**Other drift:** none found. No constitution file makes a claim this
change contradicts.

## Documentation Updates

- `phraseforge/CHANGELOG.md`: added the `### Added` entry above.
- Removed this feature's `## Now` line from
  `specs/artifacts/phraseforge/roadmap.md` — it's in the changelog now.
