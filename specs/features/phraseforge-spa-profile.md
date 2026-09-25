---
title: Port profile (locale/password) to phraseforge's SPA shell via a JSON API
kind: feature
status: done
version: 1
updated: 2026-09-24
branch: main
---

## Problem / Motivation

Sixth and final planned SPA item (before the unified-shell/pagination retrofits). Profile is the
simplest page yet: one read-only role-grants list, a locale switcher, and a change-password form —
no items, no tabs, no composite keys. The one real design question is **locale switching's effect
on the surrounding chrome**: `layout.html`'s sidebar/header (nav labels, logout link, sidebar/theme
toggle titles) are all server-rendered `{{call .T ...}}` text, baked once per page load — not part
of any shell's bootstrap JSON. Today, changing locale works via a full POST-redirect-GET-shaped
render (`handleSetLocale` re-renders `profile.html` with the new locale immediately, and every
subsequent real navigation picks up the new locale for the chrome too). A pure fetch-based locale
change would update the in-page profile content but leave the chrome showing stale-language text
until the next real navigation — a genuine regression, not just a cosmetic gap.

## Acceptance Criteria

- `GET /api/v1/profile` — `{locale, grants}`, mirroring `handleProfileForm`'s own `GrantsForUser`
  read exactly (each grant wrapped with camelCase JSON tags, matching this app's convention).
- `POST /api/v1/profile/locale` — `handleSetLocale`'s exact validation (`i18n.IsValid`) and
  `SetLocale` call. **After a successful response, the client does a real `window.location.reload()`
  rather than an in-page re-render** — this is the one deliberate exception to this app's
  "fetch + re-render, never reload" SPA convention, made explicit here because `layout.html`'s
  chrome cannot otherwise pick up the new locale without a real navigation. No other phraseforge
  SPA conversion has needed this exception; it's specific to locale changing the whole page's
  language, not just one page's own content.
- `POST /api/v1/profile/password` — `handleChangePassword`'s exact validation order (mismatch →
  too short → `auth.ChangePassword` call, mapping `auth.ErrInvalidCredentials` to a specific
  "wrong current password" error) and success behavior (status-bar success message, form fields
  cleared, no reload — this one doesn't touch chrome).
- `profile-app.js` renders the three cards (roles list, locale form, password form) from one
  `GET /api/v1/profile` fetch plus the bootstrap-baked `username`/`Locales`/i18n; the password
  form always clears its three fields after either a successful change or a validation error
  (matching the original server-rendered form, which never re-populated password fields either).
- `defer` on `profile-app.html`'s script tag from the start (per `specs/memory.md`'s `[gotcha]`).
- `GET /profile` serves the shell; the old `POST /profile/password`/`POST /profile/locale` routes
  and their handlers, plus `profile.html`, are removed once validated. (`GET /profile`'s handler is
  replaced, not removed — the route itself stays.)
- No change to Texts, Dialogs, Vocabulary, Models, Admin, or any `pf-*` component.
- Live validation must specifically exercise: changing locale (confirm the page actually reloads
  and the sidebar/header text changes language, not just the profile content), changing password
  successfully, and each of the three password-validation failure paths (mismatch, too short,
  wrong current password).

## Approach

Same shape as the prior SPA features (API → shell → client render → cutover → validate), scaled
down for a single-page-no-items resource — one aggregate `GET /api/v1/profile` (analogous to
Admin's aggregate, but far smaller: just `locale` and `grants`), re-fetched after a password
change (grants never change here, but re-fetching uniformly costs nothing and stays consistent
with the rest of the app's habit) — not re-fetched after a locale change, since that path reloads
the whole page anyway. `username` is immutable per session and baked into the initial bootstrap
alongside `Locales`/i18n, never re-fetched. New `profile.go` file, mirroring the
`admin.go`/`vocabulary.go`/`models.go` one-file-per-resource convention.

## Affected Areas

- `phraseforge/internal/server/server.go` (new `handleProfileApp`; `/api/v1/profile` routes added
  to the existing general `/api/v1` route group — profile has never been admin-gated; removal of
  the old `handleChangePassword`/`handleSetLocale` routes; `profile.html` removed from the
  `init()` pages list, `profile-app.html` added)
- new `phraseforge/internal/server/profile.go`
- new `phraseforge/internal/server/templates/profile-app.html`
- new `phraseforge/internal/server/static/js/profile-app.js`
- removed: `phraseforge/internal/server/templates/profile.html`
- `specs/artifacts/phraseforge/roadmap.md` (`## Later` → `## Now` at B2 start)
- `phraseforge/CHANGELOG.md` (entry at B5)

## Out of Scope

- The unified-shell item, the pagination item — separate roadmap entries.
- Any change to `layout.html`'s chrome rendering itself (the locale-reload approach works around
  this rather than fixing it — a real fix belongs to the unified-shell design, per the standing
  `[decision]` memory entry that phraseforge's navigation no longer must work with JS disabled).
- Any change to `pf-*` components, or to the `auth`/`roles`/`i18n` packages' own logic.

## Implementation Notes

**Step 1 — JSON API.** New `profile.go`, mirroring the `admin.go`/`vocabulary.go`/`models.go`
one-file-per-resource convention: `apiGetProfile` (`{locale, grants}`, each grant wrapped as
`apiProfileGrant` with camelCase tags), `apiSetProfileLocale`, `apiChangeProfilePassword`. Password
change keeps `handleChangePassword`'s exact validation order (mismatch → too short → store's own
current-password check), but maps the two failure kinds to different HTTP statuses where the
original didn't need to (it just re-rendered a page either way): `auth.ErrInvalidCredentials` →
`400 invalid_current_password`; any other store error → `500 internal_error` — a deliberate
improvement over the original's undifferentiated re-render, not a behavior change a user would
notice.

**Step 2 — shell.** `profile-app.html` with `defer` from the start. `handleProfileApp` bakes
`username` and `locales` (the site-locale options) into the bootstrap once — both are immutable
for the page's lifetime (a locale change reloads the whole page, so there's never a case where the
bootstrap's `locales` list needs to reflect a change made without a reload). `profileAppI18nKeys`
gathered from `profile.html` plus `role.admin`/`role.teacher`/`role.student` (19 keys);
`profile.language_saved` deliberately excluded — it named the old flash-message shown after a full
page re-render, which no longer applies now that a locale change ends in
`window.location.reload()` instead (the visible language change across the whole UI is its own
confirmation).

**Steps 3-4 — `profile-app.js`.** One `render()` fetches `GET /api/v1/profile` and draws all three
cards. The locale form's submit handler is the one place in this whole SPA-conversion project that
calls `window.location.reload()` after a successful mutation instead of re-rendering in place —
documented inline and in the spec's Approach, since it's a deliberate, scoped exception to this
app's established pattern, not an oversight. The password form always calls `.reset()` immediately
after reading its three field values (before the fetch resolves either way), matching the original
server-rendered form, which also never re-populated password fields on either success or failure.

**Step 5 — cutover.** Removed `handleProfileForm`/`handleChangePassword`/`handleSetLocale` from
`server.go`; the two old `POST /profile/*` routes are gone, `GET /profile` now points at
`handleProfileApp`. `profile.html` removed from the `init()` pages list (replaced with
`profile-app.html`) and deleted from disk.

**Final file list**: modified `server.go`; new `profile.go`, `profile-app.html`, `profile-app.js`;
removed `profile.html`.

**Self-review**: re-verified the password-change validation order and the locale-validity check
(`i18n.IsValid`) against the removed handlers — unchanged. No new attack surface; `/api/v1/profile`
sits in the same general `requireAuth`-only route group `/profile` always used (never
admin-gated).

## Validation

**Automated:** `go build/vet ./...`, `gofmt -l .` clean. `task test-phraseforge-frontend` —
29/29 (unaffected; no new component needed).

**Live deployment validation** (`task deploy-phraseforge`, unauthenticated route-level checks
only — same sandbox constraint as `phraseforge-spa-models`/`phraseforge-spa-admin`):
- `GET /profile`, `GET /api/v1/profile` → `302` (redirect to `/login`, expected — same
  `requireAuth` gate as before).
- `GET /static/js/profile-app.js` → `200`, with the body containing the expected
  `__PF_PROFILE_BOOTSTRAP__`/`location.reload` markers — confirms the new client script (including
  the deliberate reload-after-locale-change behavior) is deployed correctly.

**Known gap — functional/authenticated validation not performed by me**, for the same reason as
the three prior features: authenticating via `curl`/`fetch` requires a command carrying a
`password` field, blocked by this session's sandbox safety classifier, and the user has chosen to
skip overriding that each time. The user manually walked through the golden path in a browser
instead and confirmed it works. **User confirmed: "It works."**

**Overall verdict**: build/format/route-level checks clean by me; the functional golden path —
including the locale-reload behavior, the one real design risk in this feature — was confirmed
working by the user's own manual test.

## Documentation Review

**Files audited:** `phraseforge/README.md` (no architecture-tree listing — no drift),
`specs/tech-stack.md` (JSON API convention already documented generically — still accurate),
`phraseforge/CHANGELOG.md` (checked for entry mapping).

**No constitution drift found.**

**Proposed changelog entry (for B5):**

**Category: Changed**
```
- Profile (roles, site language, password) is now a single-page client-rendered app, all via a
  new `/api/v1/profile` JSON API, instead of a server-rendered page with full-page POST-redirect
  round trips; changing site language still triggers a real page reload (needed to refresh the
  sidebar/header text), but the password form and roles list no longer do.
```

## Documentation Updates

Added to `phraseforge/CHANGELOG.md` under `## [Unreleased]` → `### Changed`: the entry proposed
above. Removed the `## Now` line for this feature from `specs/artifacts/phraseforge/roadmap.md`
(feature complete).

No `memory.md` entries — no new gotcha surfaced; the locale-reload design decision is documented
in this spec and inline in `profile-app.js`, scoped to this one feature rather than a durable
cross-feature fact.
