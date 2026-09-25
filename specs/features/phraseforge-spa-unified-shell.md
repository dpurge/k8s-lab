---
title: Consolidate phraseforge's six SPA shells into one unified shell
kind: feature
status: done
version: 1
updated: 2026-09-24
branch: main
---

## Problem / Motivation

Every resource type shipped so far (Texts, Dialogs, Vocabulary, Models, Admin, Profile) got its
own full-page shell (`/`, `/dialogs`, `/vocabulary`, `/models`, `/admin`, `/profile`) — navigating
*within* one resource never changes the URL (an established single-URL-model decision), but
navigating *between* resources is still a real page load, because each shell is served by its own
handler/template/bootstrap/script. This was always meant to be temporary: the roadmap has carried
`phraseforge-spa-unified-shell` since the first SPA port, and the sidebar's `pf-nav` component's
own doc comment has, since it was written, explicitly named "a client-only tab switcher with no
server-known active state" as the case that calls for a `kb-nav`-style data-driven component
instead of real links — which no longer conflicts with any constraint, since the standing
`[decision]` memory entry from the Admin work says phraseforge's navigation no longer needs to
work with JavaScript disabled.

This feature is the largest in the whole SPA-migration series by a wide margin: it touches all six
already-shipped `*-app.js` files (2,153 lines combined), all six shell templates, `server.go`'s
routing, `layout.html`'s chrome, and both `pf-nav` (retired) and `pf-tabs` (extended). The
mechanical shape of the change is the same, repeated six times, so the risk is more "six chances to
get a detail wrong" than "one hard problem" — B2 works through the six sections one at a time, each
validated before moving to the next, per the usual small-steps discipline.

Two follow-on problems get solved as part of this same consolidation, since fixing them separately
afterward would mean touching all six sections' code twice:

1. **The sidebar's language filter** currently works by reloading the page with `?language=` in the
   query string — three SPA features ago this required documenting a `[gotcha]` (each section's
   own `showList()` must remember to read `window.location.search`) precisely *because* the filter
   and the section were on two different pages conceptually joined only by a URL. Once there's only
   one page, there's no page reload to carry that query string across, so the filter becomes a
   plain in-page value the active section reads directly — the whole gotcha class stops applying.
2. **Locale changes leave the sidebar/header stale.** `phraseforge-spa-profile`'s spec documented
   a deliberate `window.location.reload()` after a locale change specifically because
   `layout.html`'s chrome is server-rendered per request with no bootstrap of its own. Since this
   feature already needs every section's static bootstrap (including its i18n map) to be
   refreshable in place — the unified shell's chrome now lives in that same bootstrap — a locale
   change can re-fetch the whole bootstrap and re-render the chrome *and* the currently-active
   section, with no reload at all.

## Acceptance Criteria

- `GET /` serves one shell (`app.html`) containing all six sections' root containers
  (`#texts-app`, `#dialogs-app`, `#vocabulary-app`, `#models-app`, `#admin-app` — only rendered at
  all when `NavFlags.IsAdmin` — and `#profile-app`), one combined bootstrap
  (`window.__PF_APP_BOOTSTRAP__`, keyed `chrome`/`texts`/`dialogs`/`vocabulary`/`models`/`admin?`/
  `profile`), and all six (or five, for a non-admin) `<script defer src="...">` tags.
- `GET /dialogs`, `GET /vocabulary`, `GET /models`, `GET /admin`, `GET /profile` all `302` to `/`
  — collapsing to a single real URL, per the user's explicit choice (not deep-linkable entry
  points; a plain redirect covers anyone with an old bookmark).
- A new `new shell.js` owns section switching: reads `chrome` from the bootstrap to build the
  sidebar as a **vertical `pf-tabs`** (replacing the sidebar's `pf-nav`) plus the header's
  username quick-link, wires the `#language-filter` select to update one shared in-page value and
  notify whichever section is currently active, and persists the active section to
  `sessionStorage` (restored on load; falls back to `texts` if the stored value is `admin` and the
  current user isn't admin, or if nothing is stored).
- **`pf-tabs` gains an `orientation="vertical"` attribute/CSS variant** (default stays horizontal,
  Admin's own existing usage untouched) so the sidebar can reuse it instead of introducing a new
  component. This is the one exception to "no change to other `pf-*` components" — `pf-tabs` is
  being extended, not left alone, because it's the direct replacement for what `pf-nav` did in the
  sidebar.
- **`pf-nav` is removed entirely** (component directory + its one call site in `layout.html`) —
  once the sidebar switches to `pf-tabs`, nothing references it.
- Each of the six `*-app.js` files gains exactly two structural changes, nothing else:
  1. `const BOOT = window.__PF_X_BOOTSTRAP__` becomes a mutable module-level `let BOOT`, set from
     `window.__PF_APP_BOOTSTRAP__.x` at load and reassignable via a `setBootstrap(boot)` entry
     point (needed for the no-reload locale-change refresh below).
  2. The bottom-of-file unconditional auto-invocation (`showList();`, `loadAndRender("grants");`,
     `render();`, ...) and the file's own `document.querySelectorAll('pf-nav a[href="/x"]')...`
     click-interception block are both removed, replaced by registering `{ setBootstrap, show }`
     on a shared `window.pfSections.x` object that `shell.js` calls into. No other logic in any of
     the six files changes.
- Every section's `showList()`-equivalent reads the shared in-page language-filter value (set
  by `shell.js`) directly, instead of `window.location.search` — retiring the
  `[gotcha]`/`[convention]` memory pair recorded for `phraseforge-spa-shell-texts`, which no
  longer applies once there's no page reload to read a query string from.
- `POST /api/v1/profile/locale`'s client call in `profile-app.js` no longer calls
  `window.location.reload()`. Instead: on success, it fetches a new
  `GET /api/v1/app-bootstrap` (same shape as the inline bootstrap, server logic shared with
  `handleApp`), replaces `window.__PF_APP_BOOTSTRAP__`, calls `setBootstrap` on every section
  (including `chrome`, re-rendering the sidebar/header), and re-invokes `show()` on whichever
  section is currently active (itself, since you can only change locale from the Profile tab) —
  so the sidebar, header, and the open section all reflect the new locale immediately, with zero
  reload.
- `defer` on `app.html`'s six script tags, as always.
- Live validation must specifically exercise: switching between all sections with zero URL change
  each time, reloading the page and confirming it returns to the last active section (and to
  `texts` specifically if that section was `admin` and the reload happens as a non-admin — not a
  realistic scenario in practice, but the fallback logic itself is checked), the language filter
  correctly scoping whichever section is currently open, and a locale change updating the sidebar/
  header/open-section text with no reload.

## Approach

Ordered, one validated step at a time (per the small-steps ground rule — six sections is six
chances to get a small detail wrong, not one big design risk):

1. **Server**: new `app.go` with `handleApp` (combines all six sections' existing
   bootstrap-building logic, keyed by section, admin's slice conditional on `NavFlags.IsAdmin`)
   and a new `apiGetAppBootstrap` (`GET /api/v1/app-bootstrap`, same JSON shape, for the
   no-reload locale-change refresh). `server.go`: `GET /` → `handleApp`; the other five old
   shell routes become `302` redirects to `/`; add `GET /api/v1/app-bootstrap` to the general
   `/api/v1` group.
2. **`pf-tabs` vertical variant + `app.html` + `shell.js`** (skeleton first, with all six
   sections' containers present but no section-specific JS wired yet — validate the shell loads
   and the sidebar renders before touching any section's own script).
3. **Retrofit each of the six `*-app.js` files, one at a time**, in the order they were built
   (texts, dialogs, vocabulary, models, admin, profile) — mutable `BOOT`, `setBootstrap`, register
   on `window.pfSections`, remove the file's own `pf-nav`-interception block, switch its
   language-filter read from `window.location.search` to the shared value. Validate each one
   switches correctly and its language filter still works before moving to the next.
4. **Locale-change no-reload wiring** in `profile-app.js` (the one file that needs a third change
   beyond the standard two) plus `shell.js`'s locale-refresh orchestration.
5. **Cutover**: remove `pf-nav`'s component directory, the six old shell templates
   (`texts-app.html`/`dialogs-app.html`/`vocabulary-app.html`/`models-app.html`/`admin-app.html`/
   `profile-app.html`), the six old `handleXApp` functions, and their `init()` pages-list entries.
6. **Memory update**: mark the `window.location.search` language-filter `[gotcha]`/`[convention]`
   pair as superseded (criterion 3, "resolved transient" — the page-reload mechanism it was about
   no longer exists) rather than deleting the history outright; add any new gotcha found during
   implementation.

## Affected Areas

- new `phraseforge/internal/server/app.go` (`handleApp`, `apiGetAppBootstrap`)
- `phraseforge/internal/server/server.go` (routing: one `GET /`, five redirects, new
  `/api/v1/app-bootstrap`; removal of the six old `handleXApp`-serving routes)
- new `phraseforge/internal/server/templates/app.html`
- new `phraseforge/internal/server/static/js/shell.js`
- `phraseforge/internal/server/static/js/{texts,dialogs,vocabulary,models,admin,profile}-app.js`
  (mutable `BOOT`/`setBootstrap`/`window.pfSections` registration/language-filter read; no other
  logic changes)
- `phraseforge/internal/server/static/components/pf-tabs/` (new `orientation="vertical"` variant)
- removed: `phraseforge/internal/server/static/components/pf-nav/` (entire component)
- removed: the six old `*-app.html` shell templates
- removed: `handleTextsApp`/`handleDialogsApp`/`handleVocabularyApp`/`handleModelsApp`/
  `handleAdminApp`/`handleProfileApp` and their `*AppI18nKeys`/`*AppI18n` helpers (superseded by
  `handleApp`'s combined bootstrap — the underlying data-gathering calls they each made are kept,
  just consolidated into one function)
- `phraseforge/internal/server/templates/layout.html` (sidebar chrome removed — now built by
  `shell.js` from the bootstrap; `phraseforgeSetLanguageFilter`'s page-reload JS removed)
- `specs/memory.md` (mark the language-filter `window.location.search` gotcha/convention pair
  superseded)
- `specs/artifacts/phraseforge/roadmap.md` (`## Later` → `## Now` at B2 start)
- `phraseforge/CHANGELOG.md` (entry at B5)

## Out of Scope

- The pagination item (`phraseforge-spa-pagination`) — separate roadmap entry, unaffected by this
  change (each section's list-fetching logic is untouched beyond the language-filter source).
- Any change to any `/api/v1/{texts,dialogs,vocabulary,models,admin}/*` resource JSON API —
  this feature only touches the shell/page-serving layer.
- Login/signup — confirmed staying separate multi-page-with-real-navigation, unchanged.
- Fully live-retranslating a section's content the instant a *different* section's data changes
  underneath it — sections still only re-render when switched to (or after a locale change, which
  re-renders the currently-open one specifically) — no cross-section live-sync is being added.
- Any change to `pf-button`/`pf-field`/`pf-card`/`pf-dialog`/`pf-status-bar` — only `pf-tabs` is
  extended, and `pf-nav` is removed; every other component is untouched.

## Implementation Notes

**Step 1 — server.** New `app.go`: `buildAppBootstrap` gathers every section's own bootstrap data
into one keyed object (`chrome`/`texts`/`dialogs`/`vocabulary`/`models`/`admin?`/`profile`), shared
by `handleApp` (embeds it in `app.html`) and `apiGetAppBootstrap` (`GET /api/v1/app-bootstrap`,
plain JSON, used for the no-reload locale refresh). A genuine efficiency side-effect of
unification: `s.formOptions`/`s.tags.AllNames` are resource-agnostic calls that Texts/Dialogs/
Vocabulary/Models each used to call separately (four round trips); `buildAppBootstrap` now calls
each once and reuses the result across all four. `server.go`: `GET /` → `handleApp`; `/dialogs`,
`/vocabulary`, `/models`, `/profile`, and (inside the existing `requireAdmin` group) `/admin` all
`302` to `/` via one shared `redirectToApp` handler. The six old `handleXApp` functions and their
`*AppI18nKeys`/`*AppI18n` helpers were split apart — the helpers are kept (reused by
`buildAppBootstrap`), only the HTTP-handler wrappers were removed.

**Step 2 — `pf-tabs` vertical variant, `app.html`, `shell.js`.** `pf-tabs` gained an
`orientation="vertical"` CSS variant (no JS change — plain attribute selector), matching `pf-nav`'s
old sidebar look exactly. `app.html` holds all six section containers (`admin-app` only rendered
when `NavFlags.IsAdmin`) plus the combined bootstrap and six-or-five `<script defer>` tags, section
scripts first, `shell.js` last (see the ordering rationale in its own header comment).
`layout.html`'s sidebar became an empty `<aside id="sidebar">`, built entirely by `shell.js`; the
header's username/logout links became empty placeholders shell.js fills in — except the theme-
toggle button's `aria-label`/`title`, deliberately kept server-rendered since `login.html`/
`signup.html` also use it and never load `shell.js`. The old language-filter-redirect script in
`layout.html`'s `<head>` and `phraseforgeSetLanguageFilter` were removed entirely.

**Real risk found during implementation, not anticipated in the spec**: with all six sections'
containers now permanently present in the DOM (each just hidden via `style.display` when
inactive), several sections' own forms reuse the same element ids (`#language`, `#script`,
`#field-source`, `#translateBtn`, `#translation-target`) that `editor.js` and `layout.html`'s
`phraseforgeGenerate()` look up via plain `document.getElementById()`. Visiting two sections' edit
forms in one session would leave two elements sharing an id in the document, and
`getElementById()` returns whichever is first in document order — not necessarily the visible one.
Caught before any deploy; fixed by having `shell.js`'s `showSection()` clear the *previous*
section's container's `innerHTML` on every switch (safe, since every section already fully
re-renders itself via `root.innerHTML` on its own `show()` anyway) — recorded as a `[gotcha]` in
`specs/memory.md`.

**Steps 3–4 — retrofitting all six `*-app.js` files and the locale-refresh wiring.** Each file got
exactly the two changes the spec called for (mutable `BOOT` read from
`window.__PF_APP_BOOTSTRAP__.<section>`, registration on `window.pfSections.<section>` replacing
both the old `pf-nav`-interception block and the old bottom-of-file auto-invocation) plus, for the
four resource sections, reading the language filter via `window.pfGetLanguageFilter()` instead of
`window.location.search` (retiring that gotcha, now marked superseded in `specs/memory.md` rather
than edited in place, per the memory format's append-only discipline). `profile-app.js` got the
one extra change: its locale-change handler now calls `GET /api/v1/app-bootstrap` and
`window.pfRefreshAppBootstrap(...)` instead of `window.location.reload()`.

**Step 5 — cutover.** Removed the six old shell templates and the entire `pf-nav` component
directory (untracked files — plain `rm`, not `git rm`, since they'd never been committed).
`html/template` became an unused import in `admin.go`/`models.go`/`profile.go`/`vocabulary.go`
after their `handleXApp` functions were removed (each was the only `template.JS`/template-package
user in its file) — caught by `go build`, removed. `catalog` also became unused in `admin.go` for
the same reason (its only use was `handleAdminApp`'s full-catalog calls, now in `app.go`).

**Final file list**: modified `server.go`, `layout.html`, all six `*-app.js` files, `pf-tabs/
component.css` + `component.test.js`; new `app.go`, `app.html`, `shell.js`; removed the six old
`*-app.html` templates and the entire `pf-nav/` component directory.

**Self-review**: re-verified every route's auth gate against its pre-unification shape — `/admin`'s
redirect still sits inside the `requireAdmin` group (a non-admin still gets `404`, not a redirect,
matching the "don't reveal the route exists" reasoning every Admin feature has kept); the other
four redirects sit in the general `requireAuth` group, matching where their handlers always were.
No new attack surface — this feature only touches the shell/page-serving layer, not any resource's
own JSON API authorization.

## Validation

**Automated:** `go build/vet ./...`, `gofmt -l .` clean. `node --check` on all six `*-app.js` files
plus `shell.js` (these aren't covered by `task test-phraseforge-frontend`, which only exercises
`static/components/*`, since they're page scripts, not Web Components — syntax-checked directly
instead). `task test-phraseforge-frontend` — 27/27 (25 pre-existing minus `pf-nav`'s 2 removed
tests, plus 1 new `pf-tabs` `orientation="vertical"` test).

**Live deployment validation** (`task deploy-phraseforge`, unauthenticated route-level checks only
— same sandbox constraint as every feature since `phraseforge-spa-models`, more consequential here
given this feature's size):
- `GET /`, `/dialogs`, `/vocabulary`, `/models`, `/admin`, `/profile`, `GET /api/v1/app-bootstrap`
  → all `302` to `/login` (confirmed via the `Location` header) — `requireAuth` intercepts before
  any of my new redirect-to-`/` logic even runs, exactly matching every one of these routes'
  pre-existing behavior for an unauthenticated request.
- `GET /static/js/shell.js`, `/static/components/pf-tabs/component.js` → `200`, bodies containing
  the expected new markers (`pfShowSection`, `pfRefreshAppBootstrap`, `orientation`,
  `pfSections.texts`, `pfGetLanguageFilter` in `texts-app.js`, `pfRefreshAppBootstrap`/
  `app-bootstrap` in `profile-app.js`).
- `GET /static/components/pf-nav/component.js` → `404` — confirms the component is actually gone,
  not just unreferenced.
- The server process itself starting and serving traffic at all is a meaningful automated check
  here: `app.html` is parsed via `template.Must` in `init()` at process startup — a template syntax
  error would have crashed the pod immediately, and the rollout succeeded.

**Known gap — functional/authenticated validation not performed by me**, for the same reason as
every feature since Models, but covering far more surface area this time. The user manually walked
through the golden path in a browser instead and confirmed it works. **User confirmed: "Looks
good."**

**Overall verdict**: build/format/route-level checks clean by me; the functional golden path —
including the id-collision fix, the single largest new risk this feature introduced — was
confirmed working by the user's own manual test.

## Documentation Review

**Files audited:** `phraseforge/README.md` (no architecture-tree listing — no drift),
`specs/tech-stack.md` (JSON API convention and `pf-*` component convention already documented
generically — still accurate; no update needed since `pf-tabs`'s vertical variant is just another
use of the existing convention, and `pf-nav`'s removal doesn't change the convention itself, only
which components currently exist), `phraseforge/CHANGELOG.md` (checked for entry mapping).

**No constitution drift found.**

**Proposed changelog entry (for B5):**

**Category: Changed**
```
- Texts, Dialogs, Vocabulary, Models, Admin, and Profile are now one unified single-page app
  (one URL, in-page section switching via a new sidebar built from `pf-tabs`) instead of six
  separate shells each with their own URL; the sidebar's language filter and a Profile locale
  change are both now pure in-page state with no page reload, and the old `pf-nav` component is
  removed (replaced by `pf-tabs`).
```

## Documentation Updates

Added to `phraseforge/CHANGELOG.md` under `## [Unreleased]` → `### Changed`: the entry proposed
above. Removed the `## Now` line for this feature from `specs/artifacts/phraseforge/roadmap.md`
(feature complete).

No new `memory.md` entries beyond the two already added during implementation (the id-collision
`[gotcha]` and the language-filter-gotcha supersession note) — nothing further surfaced.
