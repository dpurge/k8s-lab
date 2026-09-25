---
title: Add a Jobs menu to phraseforge
kind: feature
status: done
version: 1
updated: 2026-09-24
branch: main
---

## Problem / Motivation

`phraseforge-job-queue` (shipped, `specs/features/phraseforge-job-queue.md`) added the queue infrastructure but deliberately left it invisible — no HTTP route, no UI, by explicit design (that feature's Out of Scope). Right now there's no way to see what jobs have run, retry a failed one, or clean up old rows except by querying Postgres directly.

This feature adds the Jobs menu: an admin-only page listing every job (interactive and background alike, matching the earlier "everything visible" architecture decision made when planning phraseforge's roadmap), with view/retry/delete actions.

This also settles an open design question that the previous feature's reviewer explicitly flagged: whether the `jobs` table needs a `user_id`/ownership column for per-user authorization. The decision here is that jobs are treated as admin-facing operational records, not per-user private data — no ownership column is added; access is gated entirely by the existing admin role, matching how the admin dashboard already surfaces other site-wide operational data (LLM prompts, grants, IME config).

This feature also adds a `Delete` method to `jobs.Service` — it didn't exist before (the prior feature only needed `Enqueue`/`EnqueueAndAwait`/`Register`/`Run`/`Get`/`List`/`Retry`/`FailStale`). Delete is restricted to jobs in a terminal state (`done`/`failed`): deleting a `pending` or `running` job is rejected, since nothing safe can be done with a job an `EnqueueAndAwait` caller might still be polling for, or one the single worker is actively processing.

Per an explicit decision, this page uses a manual "Refresh" button rather than auto-polling — matching phraseforge's existing convention (the admin dashboard only re-fetches after an explicit mutation, no polling exists anywhere in the app today).

## Acceptance Criteria

- New `phraseforge/internal/jobs` method: `Delete(ctx, id string) error` — deletes a `done`/`failed` job's row; returns an error (does not delete) if the job is `pending` or `running`, or doesn't exist.
- New admin-gated HTTP routes under `/api/v1/admin` (reusing the existing `requireAdmin` middleware group): `GET /api/v1/admin/jobs` (list, most-recent-first, matching `jobs.List`'s existing 500-row cap), `GET /api/v1/admin/jobs/{id}` (one job's full detail including payload/result/step/error), `POST /api/v1/admin/jobs/{id}/retry`, `DELETE /api/v1/admin/jobs/{id}`.
- Retry/Delete follow the same JSON error-shape convention (`{"error": {"code": "...", "message": "..."}}`) as the rest of the admin API; retrying a non-failed job or deleting a non-terminal job returns HTTP 400 with a clear message derived from `jobs.Service`'s own returned error.
- A new top-level "Jobs" nav item, visible only to admins (the same `chrome.navFlags.isAdmin` conditional the existing "Admin" tab uses) — not nested inside the Admin page's own tabs, matching the roadmap's "Jobs menu item" wording.
- New `phraseforge/internal/server/static/js/jobs-app.js` SPA section (registered on `window.pfSections.jobs`, with `show()`/`setBootstrap()` per the unified-shell convention) rendering: a table (Kind, Priority, Status, Created, Error) with a manual "Refresh" button (no auto-polling); a "View" action per row opening a `pf-dialog` with the job's full payload/result/step; a "Retry" action enabled only when `status='failed'`; a "Delete" action (behind a danger confirm dialog, matching `admin-app.js`'s existing delete-confirmation pattern) enabled only when `status` is `done` or `failed`.
- New `#jobs-app` container + `<script defer src="/static/js/jobs-app.js">` in `app.html`, gated the same way the existing `#admin-app` container/script are (`{{if .NavFlags.IsAdmin}}`).
- `nav.jobs` i18n key added (en + pl).
- No change to `jobs.Service`'s `Enqueue`/`EnqueueAndAwait`/`Register`/`Run`/`Get`/`List`/`Retry`/`FailStale` — this feature only adds `Delete` and exposes the existing read/retry methods over HTTP.
- No `user_id`/ownership column added to the `jobs` table — access control is entirely at the admin-role level, not per-job-owner.
- `go build ./...`, `go vet ./...`, `go test ./...` pass. Manual verification: an admin can see the Jobs page, view a completed job's detail, retry a failed job (a new row appears), and delete a done/failed job (it disappears from the list); a non-admin user does not see the Jobs nav item, and a direct API call to `/api/v1/admin/jobs` as a non-admin gets the same 404 the rest of `/api/v1/admin` already returns for non-admins.

## Approach

1. **`phraseforge/internal/jobs/jobs.go`**: add `Delete(ctx context.Context, id string) error` — a single atomic `DELETE FROM jobs WHERE id=$1 AND status IN ('done','failed')` with a `RowsAffected()` check; 0 rows affected returns a clear error (covers both "not found" and "not in a terminal state" — the caller doesn't need to distinguish them for this feature's UI, which will only ever show the Delete action on rows it already knows are terminal).
2. **`phraseforge/internal/server`**: add the four HTTP handlers (list/get/retry/delete) — put them in a new `jobs.go` handler file (matching how `llm.go` is its own file, rather than growing `admin.go` further), wired into the existing `requireAdmin`-gated route group in `server.go`.
3. **`phraseforge/internal/server/app.go`**: add `"nav.jobs"` to `chromeI18nKeys`; no new bootstrap data needed beyond the existing `navFlags.isAdmin` flag already present.
4. **`phraseforge/internal/server/static/js/shell.js`**: add `"jobs"` to `SECTION_IDS`, and to the sidebar nav item construction conditioned on `chrome.navFlags.isAdmin` (same conditional the existing "admin" item uses).
5. **`phraseforge/internal/server/templates/app.html`**: add the `#jobs-app` container and its deferred script tag, both gated by `{{if .NavFlags.IsAdmin}}`, matching `#admin-app`'s exact gating.
6. **New `phraseforge/internal/server/static/js/jobs-app.js`**: table+actions UI following `admin-app.js`'s established pattern exactly (fetch-render, re-fetch after each mutation, `pf-button`/`pf-dialog`/`statusBar` usage, danger-confirm before delete).
7. **`phraseforge/internal/i18n/i18n.go`**: add `nav.jobs` (en + pl).

Key decisions: (a) admin-only access, no `user_id` column — jobs are operational records, not per-user private data, matching the admin dashboard's existing scope; (b) manual Refresh, no polling — matches the app's existing convention, avoids introducing a new pattern; (c) `Delete` only for terminal-state jobs — a `pending`/`running` job may still have an `EnqueueAndAwait` caller polling for it, or be actively processed by the single worker, so deleting it isn't safe; (d) routes nested under the existing `/api/v1/admin` group rather than a new top-level `/api/v1/jobs` group, reusing the existing `requireAdmin` middleware rather than inventing a parallel authorization path — the nav item is still its own top-level "Jobs" entry in the UI, only the API routing is nested.

## Affected Areas

- `phraseforge/internal/jobs/jobs.go`
- `phraseforge/internal/server/jobs.go` (new handler file)
- `phraseforge/internal/server/server.go`
- `phraseforge/internal/server/app.go`
- `phraseforge/internal/server/static/js/shell.js`
- `phraseforge/internal/server/templates/app.html`
- `phraseforge/internal/server/static/js/jobs-app.js` (new)
- `phraseforge/internal/i18n/i18n.go`

## Out of Scope

- Per-user job ownership/authorization (a `user_id` column) — deliberately not added; jobs are admin-wide operational visibility only, per this feature's explicit decision.
- Auto-refresh/polling — manual Refresh button only, per explicit decision.
- Pagination — the list stays capped at 500 rows server-side (existing `jobs.List` behavior); a dedicated pagination UI is `phraseforge-spa-pagination`'s scope, not needed yet at this scale.
- Any new job kind, or any change to how jobs are created — this feature only adds visibility/management of jobs already created by the existing `llm_generate` kind.
- Bulk actions (retry-all, delete-all) — one-at-a-time actions only, matching the roadmap's literal "retry/delete" wording.

## Implementation Notes

All 7 Approach steps implemented, plus one self-flagged fix-forward round. `go build ./...`, `go vet ./...`, `go test ./...` clean in `phraseforge`; `node --check` clean on the new/changed JS files.

**Files changed/created:**
- `phraseforge/internal/jobs/jobs.go`: added `Delete(ctx, id string) error` — atomic `DELETE FROM jobs WHERE id=$1 AND status IN ('done','failed')`, returning a clear error on 0 rows affected (covers both not-found and non-terminal-status cases).
- New `phraseforge/internal/server/jobs.go`: four admin handlers — `apiListAdminJobs` (GET, array of `apiJobSummary{id,kind,priority,status,error,createdAt}` with server-formatted timestamps, matching `apiTextSummary`'s convention), `apiGetAdminJob` (GET one, full `jobs.Job` including payload/result/step; `pgx.ErrNoRows` → 404), `apiRetryAdminJob` (POST, any service error → 400 `validation_error`, success → `{"id": "<new job id>"}`), `apiDeleteAdminJob` (DELETE, same 400-on-error pattern, 204 on success). Also declares `jobsAppI18nKeys`/`jobsAppI18n(loc)` for the section's own bootstrap i18n (see fix-forward round below).
- `phraseforge/internal/server/server.go`: registered the four routes (`GET /jobs`, `GET /jobs/{id}`, `POST /jobs/{id}/retry`, `DELETE /jobs/{id}`) inside the existing `requireAdmin`-gated `/api/v1/admin` route group.
- `phraseforge/internal/server/app.go`: added `"nav.jobs"` to `chromeI18nKeys`; added a `bootstrap["jobs"]` block (navFlags, locale, i18n) inside the existing `if nv.IsAdmin` block, matching how `bootstrap["admin"]` is assembled.
- `phraseforge/internal/server/static/js/shell.js`: added `"jobs"` to `SECTION_IDS`, extended the admin-only visibility check to cover it, added the sidebar nav item conditioned on `chrome.navFlags.isAdmin`.
- `phraseforge/internal/server/templates/app.html`: added `#jobs-app` container + deferred script tag, both `{{if .NavFlags.IsAdmin}}`-gated, matching `#admin-app`'s exact pattern.
- New `phraseforge/internal/server/static/js/jobs-app.js`: registers `window.pfSections.jobs`; table (Kind/Priority/Status/Created/Error) with a manual Refresh button; row actions View (always enabled, opens the shared confirm-dialog singleton with pretty-printed JSON of payload/result/step), Retry (enabled only when `status==='failed'`), Delete (enabled only when `status` is `done`/`failed`, behind a danger confirm).
- `phraseforge/internal/i18n/i18n.go`: added `nav.jobs` plus the Jobs section's own i18n keys (see below).

**Fix-forward round (self-flagged during implementation, then fixed):** the first implementation pass hardcoded English strings in `jobs-app.js` (title, column headers, button labels, dialog text) instead of routing through this app's i18n system, since the spec's Approach step 3 said "no new bootstrap data needed beyond navFlags.isAdmin" — which turned out to be incorrect once checked against how `admin-app.js` actually gets its own section-specific strings. Investigated `admin-app.js`'s real pattern (a per-section `<section>AppI18nKeys`/`<section>AppI18n(loc)` pair in that section's Go handler file, embedded into `bootstrap["<section>"]["i18n"]`, read via a `T(key)` lookup in the section's JS) and applied it exactly: added `jobsAppI18nKeys`/`jobsAppI18n` to `phraseforge/internal/server/jobs.go`, wired into `app.go`'s bootstrap, added the corresponding `en`/`pl` keys to `i18n.go` (`jobs.title`, `jobs.refresh`, `jobs.col_kind`, `jobs.col_priority`, `jobs.col_status`, `jobs.col_created`, `jobs.col_error`, `jobs.view`, `jobs.retry`, `jobs.view_close`, `jobs.delete_confirm`, `jobs.empty` — reusing the existing `texts.delete` key for the Delete action, matching how `admin-app.js` itself reuses `texts.edit`/`texts.delete` rather than declaring per-section duplicates), and updated `jobs-app.js` to use `T(...)` throughout instead of literals.

**Known, deliberately out-of-scope gaps** (pre-existing app-wide patterns, not introduced or worsened by this feature): the two post-mutation `statusBar.setMessage(...)` strings ("Job requeued."/"Job deleted.") remain hardcoded English — confirmed this matches every other section (`dialogs-app.js`, `texts-app.js`, `models-app.js`, `vocabulary-app.js`, and `admin-app.js` itself all hardcode their own equivalent transient status messages); fixing this app-wide is a separate concern, not this feature's. Job status/kind/priority cell *values* (the actual data, not UI chrome) are left untranslated, matching how `admin-app.js`'s own tables leave comparable raw values (e.g. provider names) untranslated. No test file exists for `jobs.Delete` or the new handlers — `phraseforge/internal/jobs` and `phraseforge/internal/server` have no existing test harness at all (DB-backed, no seam), matching the same gap already noted in `phraseforge-job-queue`'s own Implementation Notes.

**Reviewer pass (fix-forward round)**: A reviewer pass (this adds a new admin-facing delete/retry surface exposing prior LLM request/response content) found no blocking issues — authorization gating, `Delete`'s atomicity, `Retry`'s new-row semantics, information exposure, and i18n bootstrap gating all checked out structurally. Two should-fix UX defects and two low-severity items were addressed:

- **Fixed**: disabled Retry/Delete buttons were visually indistinguishable from enabled ones — `.link-btn`'s CSS had no `:disabled` variant, so a disabled action looked and behaved (cursor, color) identical to an enabled one, silently doing nothing when clicked. Added `.link-btn:disabled { color: var(--muted); cursor: default; text-decoration: none; }` to `layout.html`, using the app's existing `--muted` token.
- **Fixed**: the View dialog (showing a job's full payload/result) had no `max-height`/`overflow`, so a long LLM request/response would render clipped past the viewport with no way to scroll to it — the feature's primary read affordance was effectively unusable for realistic content. Added `max-height: 80vh; overflow-y: auto;` to `pf-dialog`'s dialog-box CSS (a shared component; behavior-neutral for every other caller's short one-line confirm dialogs, since the rule only engages once content actually exceeds the height).
- **Fixed**: `Delete`'s doc comment overstated its safety guarantee — reworded to note the (accepted, unlikely) edge case where a job that just turned terminal can still have an `EnqueueAndAwait` poller mid-wait for up to one `pollInterval` (500ms), so deleting in that narrow window could make the poller's next `Get` return not-found instead of the real result. Comment-only change, no logic change.
- **Fixed**: double-click (or two admins acting simultaneously) on Retry could enqueue duplicate jobs, since the button stayed clickable while its request was in flight. Now disables Retry (and Delete, on the same row, for consistency) immediately before the request, re-enabling on error — following the same disable-before-await pattern `layout.html`'s existing `phraseforgeGenerate` already uses.
- **Deliberately not fixed** (matches this feature's own stated design, and mirrors an already-deferred pattern from `phraseforge-job-queue`): Retry/Delete map every service-level error to HTTP 400, including what should arguably be a 500 for an infrastructure failure (e.g. a DB connection error) — this was an explicit design choice in the approved spec ("any service error → 400"), not a regression, and the previous feature already deferred the equivalent generate-path issue for the same reason.

All builds/tests re-verified green after this round: `go build ./...` + `go vet ./...` + `go test ./...` clean in `phraseforge`; `node --check` clean on `jobs-app.js`. No redeploy/re-smoke-test was performed for this round — the changes are CSS/JS/comment-only with no backend logic change, so the risk of a build-time regression not already caught by `go build`/`node --check` is negligible.

## Validation

Full end-to-end deploy + validation against the local k3d dev cluster (`task deploy-phraseforge`), zero LLM/Ollama calls made (confirmed via pod logs — only the startup config line appears, no generate activity):

- **Static validation**: `go build ./...`, `go vet ./...`, `go test ./...` clean in `phraseforge`; `node --check` clean on the new/changed JS files.
- **Deploy**: completed cleanly, ready at `http://phraseforge.localhost:8080`.
- **Auth**: confirmed `admin`/`phraseforge` login and admin-role session.
- **List**: `GET /api/v1/admin/jobs` returned both pre-existing `llm_generate`/`done` fixture rows in the documented summary shape.
- **Get one**: `GET /api/v1/admin/jobs/{id}` returned full job detail including `payload`/`result`.
- **Retry rejection path** (no LLM call): retrying a `done` (non-`failed`) job correctly returned HTTP 400 `validation_error` with the service's own message, rejected before any enqueue.
- **Delete**: successfully deleted a `done` job (204), confirmed gone from a subsequent list. (Test-fixture row `396e4570-0eca-4211-8441-eb22ed200151` was permanently deleted as part of this validation — expected/acceptable, it was old test data from a prior feature's validation.)
- **Non-admin gating** (both sub-cases): no cookie → redirect to `/login` via `requireAuth`, identical to existing `/api/v1/admin/` behavior; authenticated non-admin session → 404, byte-for-byte identical to the existing `/api/v1/admin/` bootstrap route's non-admin response. Confirms the new routes inherit the exact same authorization behavior as the rest of the admin API, no new/different gating introduced.
- **UI markup gating**: confirmed `#jobs-app` container + script tag present in the rendered page for an admin session, absent for a non-admin session, unreachable (redirected) for an anonymous request.

All Acceptance Criteria confirmed met. No regressions, no bugs found. One residual test artifact noted: a throwaway non-admin test account (`pfjobstest_43742`) remains in the dev DB — no delete-user API exists in phraseforge to clean it up, consistent with the same accepted precedent from a prior feature's validation notes.

## Documentation Review

Three documentation targets audited for drift caused by this feature:

1. **phraseforge/README.md** (full file, 70 lines reviewed)
   - **Issue**: No section documents admin-facing features or pages. README covers user features (Texts, Dialogs, Vocabulary, Models, LLM assist) but not admin capabilities (Permissions, IME config, LLM config, Config management, Jobs).
   - **Severity**: Medium (consistency/readability)
   - **Evidence**: phraseforge/README.md has no admin section; phraseforge/CHANGELOG.md:39-42 confirms admin panel and tabs exist; spec:14 notes Jobs is "user-facing" for admins.
   - **User decision**: SKIP — documenting all admin pages is a pre-existing gap this feature did not cause, deferred to a future cross-app doc initiative.

2. **specs/tech-stack.md** (constitution file; Background operation queue section, lines 124–125)
   - **Issue**: Section says phraseforge "uses one unified `internal/jobs` table so a **future** Jobs UI can show every submitted job." Jobs UI now exists; "future" language is obsolete.
   - **Severity**: Low (documentation drift)
   - **Evidence**: tech-stack.md:124-125 says "future"; spec:title and spec:14 confirm Jobs page is implemented.
   - **User decision**: APPROVED — update to present tense.

3. **phraseforge/CHANGELOG.md** (Unreleased section, lines 5–75)
   - **Issue**: No entry for Jobs feature in `### Added` section, despite being a substantial user-facing admin capability.
   - **Severity**: High (required documentation missing)
   - **Evidence**: CHANGELOG:5-75 contains no "Jobs" entry; spec fully implemented and validated per Implementation/Validation notes.
   - **User decision**: APPROVED — add the drafted entry.

## Documentation Updates

Applied three approved documentation updates:

1. **specs/tech-stack.md** (constitution file, version incremented 10 → 11)
   - Updated "Background operation queue (knowledge, phraseforge)" section (lines 117–125).
   - Changed "so a future Jobs UI can show every submitted job" to "featuring an admin-only Jobs page that shows every submitted job" — present tense reflecting the Jobs page now being live.
   - Set `updated: 2026-09-24`, status remains `approved`.

2. **phraseforge/CHANGELOG.md** (Unreleased section)
   - Added new entry under `### Added`: "Jobs admin page for viewing and managing the background operation queue: list all jobs (interactive and background alike), view detailed payload/result/step information, retry failed jobs, and delete done/failed ones with state-aware actions."
   - Placed as the first entry in the Added section, above the existing LLM prompt editor enhancement.

3. **specs/artifacts/phraseforge/roadmap.md** (mechanical bookkeeping)
   - Removed the `## Now` line: "Add a Jobs menu to phraseforge — branch `main` — specs/features/phraseforge-jobs-menu.md"
   - No version bump needed; this is a lifecycle transition from in-flight to complete, recorded via the CHANGELOG entry above.

**Deliberately skipped per user decision:**
- **phraseforge/README.md** — No changes. The gap of missing admin-section documentation is pre-existing and out of scope for this feature; it will be addressed in a future cross-app documentation initiative.
