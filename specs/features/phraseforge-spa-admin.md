---
title: Port the admin panel to phraseforge's SPA shell via a JSON API
kind: feature
status: done
version: 1
updated: 2026-09-24
branch: main
---

## Problem / Motivation

Fifth SPA item, and structurally different from the four resource ports so far: Admin isn't a
list-plus-items resource with New/View/Edit navigation states — it's a single-page dashboard with
four independent subsections (role grants, IME configs, LLM prompts, config export/import), each
its own mini add-form-plus-table, all rendered by one `handleAdmin` call and mutated by nine
separate POST handlers that each just redirect back to `/admin`. There is no single-URL-model
question to answer here (it was always one URL) — the conversion is purely about replacing each
subsection's full-page POST-redirect-GET round trip with a fetch-based mutation and an in-page
re-render, matching how every other page in the app now behaves.

Two of Admin's mutation targets have **composite keys, not a single numeric id** — IME configs are
keyed by `(language, script)` and LLM prompts by `(kind, sourceLanguage, targetLanguage)` — so
their delete routes need multi-segment path params instead of the `{id}`/`{position}` shape used
everywhere else. Config export/import also differs from every prior conversion: export is a real
file download (`Content-Disposition: attachment`), which stays a plain `<a href>` real link rather
than a fetch call, since fetch can't hand the browser a save-file prompt; import uploads a file via
`multipart/form-data`, which becomes a `fetch` with a `FormData` body instead of a JSON body.

## Acceptance Criteria

- `GET /api/v1/admin` — one aggregate response with `users`, `grants`, `imeConfigs`, `llmPrompts`
  (mirrors `handleAdmin`'s own four DB reads exactly). Every mutation endpoint below causes the
  client to re-fetch this whole aggregate and re-render the current tab, rather than patching one
  table in place — this app's already-established "things can shift under you, always re-fetch"
  habit, generalized to independent tabs instead of item positions.
- **The page is redesigned into four tabs — Grants, IME, LLM Prompts, Config —** replacing
  `admin.html`'s current single scroll of five stacked cards, via a new `pf-tabs` component (see
  below). Only the active tab's content is visible at a time; switching tabs is pure client-side
  state (no URL change, no re-fetch — the aggregate is already loaded).
- **New `pf-tabs` component** (`phraseforge/internal/server/static/components/pf-tabs/`), following
  the established `pf-*` component convention (light-DOM, self-injecting stylesheet, one dir with
  `component.js`/`component.css`/`component.test.js`). Ported from knowledge's `kb-nav`, which
  already solves exactly this problem (a data-driven, click-switched tab bar dispatching a select
  event, with no host-function coupling) — but as its own component, not a reuse of phraseforge's
  existing `pf-nav`, because `pf-nav` is documented (see its own header comment) as deliberately
  *not* this: `pf-nav` wraps real server-rendered `<a>` links for the app's real multi-page
  navigation and must keep working with JS disabled, which does not apply here — the Admin tab
  switcher is pure client-side state with no server-known "active" concept, exactly the case
  `pf-nav`'s comment says calls for a `kb-nav`-style component instead. `pf-tabs` takes an `items`
  array (`{id, label}`), an `active` attribute, and dispatches a bubbling `pf-tabs-select` event on
  click without calling any host function itself — same contract as `kb-nav`, phraseforge-named.
  Built with `document.createElement`/synchronous rendering (matching every existing `pf-*`
  component's style), not `kb-nav`'s fetched-`component.html`-template approach (phraseforge's
  `pf-*` components don't use that pattern).
- `POST /api/v1/admin/grants` (create a grant), `DELETE /api/v1/admin/grants/{id}` (revoke) —
  `handleAdminGrant`'s/`handleAdminRevoke`'s exact logic (admin grants force `language=""`;
  teacher/student require one).
- `POST /api/v1/admin/ime` (upsert), `DELETE /api/v1/admin/ime/{language}/{script}` (delete) —
  `handleAdminSetIME`'s/`handleAdminDeleteIME`'s exact logic and validation.
- `POST /api/v1/admin/llm-prompts` (upsert), `DELETE /api/v1/admin/llm-prompts/{kind}/{sourceLanguage}/{targetLanguage}`
  (delete) — `handleAdminLLMPrompt`'s/`handleAdminDeleteLLMPrompt`'s exact logic and validation.
- `POST /api/v1/admin/config/import` — same validation and same-transaction replace-all logic as
  `handleAdminImportConfig`, accepting a `multipart/form-data` file upload (client sends
  `FormData`, not JSON) and returning JSON instead of redirecting.
- `GET /admin/config/export` is **unchanged** — stays a real link/file download, not converted to
  fetch, since a fetch response can't trigger a browser save-file prompt the way a real navigation
  to a `Content-Disposition: attachment` response does.
- `admin-app.js` renders all four tabs (Grants: grant form + grants table; IME: IME form + IME
  configs table; LLM Prompts: prompt form + prompts table; Config: export link + import form) from
  one aggregate fetch, with the LLM form's existing kind-dependent target-language show/hide
  behavior preserved (currently inline JS in `admin.html`) and the grant form's role-dependent
  language-field disabling preserved (currently an inline `onchange` handler).
- Every destructive action (revoke grant, delete IME config, delete LLM prompt, import config —
  which replaces all existing IME/LLM config) is `pf-dialog`-confirmed before the fetch call,
  matching every other destructive action already converted in this app.
- `defer` on `admin-app.html`'s script tag from the start (per `specs/memory.md`'s `[gotcha]`).
- Static reference data (`languages`, `scripts`, `imePresets`, `siteLanguages`) is baked into the
  page's initial bootstrap JSON at shell load — exactly like every other shell already bakes
  `languages`/`scripts` — not re-fetched on every render, since granting/configuring can only use
  languages/scripts/presets that already existed when the page loaded.
- `GET /admin` serves the shell; the nine old `/admin/*` mutation routes (export excluded — it's
  kept) and their handlers, plus `admin.html`, are removed once validated.
- No change to Texts, Dialogs, Vocabulary, Models, Profile, or any other `pf-*` component.
- Live validation must specifically exercise: switching between all four tabs, granting a role,
  revoking it, setting an IME config, deleting it, setting an LLM prompt, deleting it, and
  confirming each tab's own table refreshes correctly after its own mutation without disturbing
  the other three tabs' state (and without losing which tab was active).

## Approach

Same shape as the prior four SPA features (API → shell → client render → cutover → validate), with
three adaptations for Admin's different structure: (1) one aggregate `GET /api/v1/admin` instead of
per-resource list/get-one endpoints, re-fetched wholesale after any tab's mutation — simpler than
tracking which specific tab changed, and this page's data is small enough that re-fetching
everything is cheap; (2) composite-key delete routes (`/ime/{language}/{script}`,
`/llm-prompts/{kind}/{sourceLanguage}/{targetLanguage}`) instead of the `{id}`/`{position}` shape,
since neither IME configs nor LLM prompts have a surrogate key; (3) **the page is redesigned into
four tabs (Grants/IME/LLM Prompts/Config) via a new `pf-tabs` component**, ported from knowledge's
`kb-nav` — the user asked for this directly, and it resolves the real problem that `admin.html`
today stacks five independent concepts (config, grants, grant list, IME, LLM prompts) into one long
scroll. `pf-tabs` is a new component rather than a `pf-nav` reuse because `pf-nav`'s own existing
doc comment already explains why it can't do this (it wraps real, server-known `<a>` links for
multi-page navigation; a client-only, no-server-state tab switcher is exactly the case it says
calls for a `kb-nav`-style component instead) — this is the "port what the other app does better"
instruction from the original migration request, applied for the first time in this session.
Config export stays a real link (unconverted); config import becomes a `FormData`-bodied `fetch`.
The new `/api/v1/admin/*` route group nests under the existing `requireAdmin` group (not the
general `requireAuth`-only `/api/v1` group), matching how `/admin/*` is already gated separately
from the rest of the app.

## Affected Areas

- `phraseforge/internal/server/server.go` (new `handleAdminApp`; new `/api/v1/admin/*` route group
  nested under the existing `requireAdmin` group; removal of the nine old `/admin/*` mutation
  routes — `GET /admin/config/export` stays; `admin.html` removed from the `init()` pages list,
  `admin-app.html` added)
- new `phraseforge/internal/server/templates/admin-app.html`
- new `phraseforge/internal/server/static/js/admin-app.js`
- new `phraseforge/internal/server/static/components/pf-tabs/` (`component.js`/`component.css`/`component.test.js`)
- removed: `phraseforge/internal/server/templates/admin.html`
- `specs/artifacts/phraseforge/roadmap.md` (`## Later` → `## Now` at B2 start)
- `phraseforge/CHANGELOG.md` (entry at B5)

## Out of Scope

- Profile, the unified-shell item, the pagination item — separate roadmap entries.
- Any change to `handleAdminExportConfig` (kept as a real link/download) or to the
  `roles`/`ime`/`ai` packages' own logic, validation, or DB schema.
- Any change to `pf-*` components.

## Implementation Notes

**New `pf-tabs` component.** `phraseforge/internal/server/static/components/pf-tabs/` — ported
from knowledge's `kb-nav` (items/active/click-dispatches-event contract) but built synchronously
with `document.createElement`, matching every existing `pf-*` component's style rather than
`kb-nav`'s fetched-`component.html` approach. Registered globally in `layout.html` alongside the
other `pf-*` components (present but unused on every page except Admin, same pattern already used
for `pf-status-bar`/`pf-dialog`). 4 new tests (29/29 total with the rest of the suite).

**Step 1 — JSON API.** Moved every admin handler out of `server.go` into a new `admin.go`
(`handleAdminApp`, `apiGetAdminBootstrap`, `apiCreateAdminGrant`/`apiRevokeAdminGrant`,
`apiSetAdminIME`/`apiDeleteAdminIME`, `apiSetAdminLLMPrompt`/`apiDeleteAdminLLMPrompt`,
`apiImportAdminConfig`, plus the unchanged `handleAdminExportConfig`/`adminConfigExport`) —
mirroring the `dialogs.go`/`vocabulary.go`/`models.go` one-file-per-resource pattern. `roles.Grant`/
`auth.User` (no JSON tags of their own) are wrapped in `apiAdminGrant`/`apiAdminUser` with
camelCase tags, matching this app's JSON API convention; `ime.Config`/`ai.Prompt` (which already
have their own snake_case tags, used by the export/import file format) are returned as-is, not
re-wrapped — deliberately mixed casing in one response, justified by not touching the on-disk
config file format. `apiImportAdminConfig` drops the original handler's `config_json` raw-text
form-field fallback (never reachable from the UI, which only ever offered a file input) — only the
multipart file-upload path is ported.

**Step 2 — shell.** `admin-app.html` with `defer` from the start. `handleAdminApp` bakes
`languages`/`scripts` from the **full catalog** (`catalog.ListLanguages`/`ListScripts`, not
`s.formOptions`) since granting/configuring must cover every language, not just ones the current
admin could personally edit — admins can always edit everything, but the distinction matters for
future admin-like roles. `imePresets`/`siteLanguages` also baked at load. `adminAppI18nKeys`
gathered from `admin.html` plus `role.admin`/`role.teacher`/`role.student`/`profile.role_admin`
(48 keys); 4 new keys (`admin.tab_grants`/`tab_ime`/`tab_llm`/`tab_config`) added to `i18n.go` for
both locales (en/pl).

**Steps 3-6 — `admin-app.js` and the tab redesign.** One `loadAndRender(tab)` re-fetches
`GET /api/v1/admin` and re-renders whichever tab is requested; each of the four
`render{Grants,IME,LLM,Config}Tab()` functions owns its own form + table, wiring submit/click
handlers after every render (matching the rest of the app's re-render-from-scratch style). Ported
behaviors: the grant form's role-dependent language-select disabling; the LLM form's
kind-dependent target-language show/hide (found a real gotcha here — see below); the
transcription-kind sentinel value (`targetLanguage: "transcription"`) LLM prompts store when no
real target language applies. Config import moved from a real multipart form POST to a `fetch`
with a `FormData` body (still a real `<input type="file">`, still `pf-dialog`-confirmed before
sending, since it replaces all existing IME/LLM config). Config export is untouched — still a
plain `<a href="/admin/config/export">`.

**Gotcha found and fixed before shipping**: the LLM form's target-language field is a `pf-field`
custom element. The original inline script hid it via `targetField.style.display = 'none'`
directly; my first draft considered using the standard `hidden` attribute instead (simpler,
matches the artifact-authoring convention this session is generally aware of) but `pf-field`'s own
CSS sets `pf-field { display: block; }` as an **author**-origin rule, which beats the browser's
UA-origin `[hidden] { display: none; }` rule regardless of selector specificity — so `hidden` would
have silently done nothing on a `pf-field`. Caught during implementation (not live), before any
deploy; used `.style.display` directly instead, matching the original script's own approach. Not
worth a `memory.md` entry — `pf-field` is the only `pf-*` component with this shape, and its own
existing CSS is the reason, not a generalizable app-wide gotcha.

**Step 7 — cutover.** Removed all nine old `/admin/*` mutation routes (kept `GET /admin/config/export`
unchanged) and `admin.html`; added `admin-app.html` to the `init()` pages list. `io`/`strings`
imports became unused in `server.go` after the admin handlers moved out — removed (caught by
`go build`).

**Final file list**: modified `server.go`, `i18n.go`, `layout.html`; new `admin.go`, `admin-app.html`,
`admin-app.js`, `pf-tabs/{component.js,component.css,component.test.js}`; removed `admin.html`.

**Self-review**: re-verified every mutation's validation and the `requireAdmin` gate (the new
`/api/v1/admin/*` group nests under the same `requireAdmin` middleware the old `/admin/*` routes
used, not the general `requireAuth`-only `/api/v1` group) against each removed handler. No new
attack surface.

## Validation

**Automated:** `go build/vet ./...`, `gofmt -l .` clean. `task test-phraseforge-frontend` —
29/29 (25 previously-existing + 4 new `pf-tabs` tests).

**Live deployment validation** (`task deploy-phraseforge`, unauthenticated route-level checks
only — see the gap noted below, same constraint as `phraseforge-spa-models`):
- `GET /admin`, `GET /admin/config/export`, `GET /api/v1/admin` → `302` (redirect to `/login`,
  expected — same `requireAuth`-then-`requireAdmin` gate as before).
- `GET /admin/grants` → `404` — confirms the old `/admin/*` mutation routes are actually gone.
- `GET /static/js/admin-app.js`, `GET /static/components/pf-tabs/component.{js,css}` → `200`, with
  bodies containing the expected markers (`__PF_ADMIN_BOOTSTRAP__`, `pf-tabs-select`,
  `customElements.define("pf-tabs")`) — confirms the new client script and component are deployed
  correctly.

**Known gap — functional/authenticated validation not performed by me**, for the same reason as
`phraseforge-spa-models`: authenticating via `curl`/`fetch` (even the app's own hardcoded bootstrap
login) requires a command carrying a `password` field, which this session's sandbox safety
classifier blocks as "credential exploration," and the user chose to skip overriding that. The user
manually walked through the golden path in a browser instead and confirmed it works, including a
follow-up cosmetic rename (tab labels "Grants" → "Permissions" and "LLM Prompts" → "LLM", in both
`en`/`pl` locales) applied and re-deployed after the first check. **User confirmed: "It works fine"
/ "Looks fine to me."**

**Overall verdict**: build/format/route-level checks clean by me; the functional golden path
(tab switching, grant/revoke, IME set/delete, LLM prompt set/delete, config export/import) and the
tab redesign itself were confirmed working by the user's own manual test, across two passes (the
second after the tab-label rename).

## Documentation Review

**Files audited:** `phraseforge/README.md` (no architecture-tree listing — no drift),
`specs/tech-stack.md` (JSON API convention and `pf-*` component convention already documented
generically — still accurate; no update needed for `pf-tabs` specifically, since it's just another
`pf-*` component following the existing convention), `phraseforge/CHANGELOG.md` (checked for entry
mapping).

**No constitution drift found.**

**Proposed changelog entry (for B5):**

**Category: Changed**
```
- The admin panel is now a single-page client-rendered app, split into four tabs (Permissions,
  IME, LLM, Config) via a new `pf-tabs` component, instead of one long scroll of stacked cards,
  via a new `/api/v1/admin` JSON API; the old `/admin/*` mutation routes are gone (config
  export/import stay as real endpoints, not fetch-based).
```

## Documentation Updates

Added to `phraseforge/CHANGELOG.md` under `## [Unreleased]` → `### Changed`: the entry above
(tab labels reflect the user's requested rename — "Grants" → "Permissions", "LLM Prompts" →
"LLM" — applied to `admin.tab_grants`/`admin.tab_llm` in `i18n.go` for both `en`/`pl` locales).
Removed the `## Now` line for this feature from `specs/artifacts/phraseforge/roadmap.md` (feature
complete).

No `memory.md` entries — the `pf-field`/`hidden`-attribute pitfall found during implementation was
caught and fixed before any deploy, and is specific to `pf-field`'s own CSS (documented inline in
`admin-app.js` instead), not a generalizable cross-app fact worth a durable memory entry.
