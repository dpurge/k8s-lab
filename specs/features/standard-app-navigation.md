---
title: Standard navigation and usage screens across phraseforge, knowledge and assistant
kind: feature
status: done
version: 1
updated: 2026-10-21
branch: main
---

## Problem / Motivation

Three apps have three different top menus.

- Phraseforge: texts, dialogs, vocabulary, models, Admin. The Admin tabs are grants, IME, LLM, languages and config; the LLM tab holds performance, attempts and purge. Jobs are a separate page.
- Knowledge: Documents, Chat, Ingest (sub-tabs Drafts and Jobs). It has no admin, usage or models pages.
- Assistant: Chat, Tasks, Usage. Usage is shown by user and by model, plus a model list. There is no attempts view and no provider column.

Target top menu, where relevant: Chat, Documents, Tasks, Usage, Config. Tasks, Usage and Config are admin only. Phraseforge has no chat; its top menu item Documents groups its content sections texts, dialogs, vocabulary and models (lesson models, not LLM models), which stay the pages of the left sidebar. Jobs become Tasks, and the Clear button removes completed and cancelled tasks. Most of Admin goes to Config; LLM models, performance, recent attempts and attempt records go to Usage. The left side panel lists the sub-pages of the current section (Usage: Usage, Models, Performance, Attempts; Config: grants, IME, languages, config). The attempts list is big, so it is paged.

Decisions confirmed by the user (2026-10-21):
1. Knowledge gets navigation only: Documents, Chat, Tasks (Jobs moved out of Ingest) and Config. No Usage or Models in knowledge until it records LLM calls.
2. The top menu stays; the side panel lists the section's sub-pages.
3. The Usage table has a period filter and a group-by (user, provider, model), with columns user, provider, model, calls, input tokens, output tokens, cost. The Models table has columns provider, model, tags, priority, availability.
4. Cost comes from optional input and output prices per 1M tokens in models.yaml, shown as a dash when unset (snake_case keys). The assistant already computes cost_usd from prices in its recorder, so check what modelcfg already has before adding keys in phraseforge.

## Acceptance Criteria

Step 1, assistant
1. Usage rows carry provider_id.
2. `GET /api/v1/llm-calls` returns total, limit and offset, and honours limit, offset and since, like the tasks list.
3. Group-by (user, provider, model) is done in the browser over the finest-grain rows, and the period filter changes the rows shown.
4. The pages `#/usage`, `#/models` and `#/attempts` render with the columns specified above (Usage and Models as in decision 3; Attempts as the attempts endpoint returns them), each with the side panel listing Usage, Models, Performance and Attempts.
5. The attempts page shows 25 rows per page by default and can move between pages.
6. The assistant top menu is Chat, Tasks, Usage and, only if the assistant has config content, Config; otherwise Config is omitted.

Step 2, shared UI
7. The usage, models and attempts tables live in `shared/ui` and the assistant uses them; each has a test.

Step 3, phraseforge
8. The top menu shows Documents and, for admins, Tasks, Usage and Config; Documents groups the content sections texts, dialogs, vocabulary and models, which stay the pages of the left sidebar. Tasks, Usage and Config are admin only.
9. Jobs appear as Tasks, and Clear removes only completed and cancelled tasks.
10. The LLM tab content (models, performance, recent attempts, attempt records) is under Usage, with a new usage endpoint; grants, IME, languages and config are under Config.

Step 4, knowledge
11. The top menu shows Documents, Chat, Tasks and Config; Jobs are no longer under Ingest; there is no Usage or Models page.

Step 5, cost prices
12. Optional input and output prices per 1M tokens (snake_case keys) are in models.yaml where missing, and cost is shown as a dash when unset. Each consumer of `shared/modelcfg` has a changelog entry.

## Approach

Memory consulted: grep of specs/memory.md was not run for this feature — none relevant known

Ordered plan, each step a thin deliverable that can be reviewed and run:
1. Assistant: usage rows gain provider; group-by is done in the browser over the finest-grain rows; add a paged attempts endpoint `GET /api/v1/llm-calls` with limit, offset and since, like the tasks list; add pages `#/usage`, `#/models` and `#/attempts` with the side panel; top menu Chat, Tasks, Usage, Config, where Config appears only if the assistant has config content.
2. Extract the usage, models and attempts tables into `shared/ui`.
3. Phraseforge: Jobs to Tasks with Clear; LLM tab content into Usage; the rest of Admin into Config; add a usage endpoint.
4. Knowledge: navigation only.
5. Add cost prices to models.yaml where missing; check what `shared/modelcfg` already has first. Its consumers need changelog entries.

## Affected Areas

- `assistant/internal/llmcalls`
- `assistant/internal/server` (api.go, models_api.go, static)
- `shared/ui`
- `phraseforge/internal/server`
- `knowledge/internal/server/static`
- `shared/modelcfg` (step 5)

Risks: the working tree has uncommitted work from the shared-ui and a2ui features. Step 1 edits files that may overlap with it (assistant static `app.js`, `index.html`, `as-usage`, `as-model-list`), and the shared-ui feature is still implementing.

## Out of Scope

- Knowledge usage, models and LLM call recording.
- New admin features.
- A mobile redesign.
- Dictionary (it has no GUI).

## Implementation Notes

All steps are done: step 1 (assistant, sub-steps 1a and 1b), step 2, step 3 (3a, 3b, 3c-1, 3c-2, 3c-3, 3d), step 4 and step 5 (documentation).

Sub-step 1a, backend:
- `assistant/internal/llmcalls/llmcalls.go`: usage rows now carry `provider_id` (`UsageByUserAndModel` groups by user, provider, model). New `Store.Attempts(ctx, since, limit, offset)` returns a page, newest first, plus the total; failed attempts are included.
- `assistant/internal/server/models_api.go` and `api.go`: new `GET /api/v1/llm-calls?since&limit&offset` (default limit 25, maximum 200; bad values give 400 `invalid_request`). The response is `{items,total,limit,offset}`.
- `assistant/schemas/api/openapi.yaml` documents the endpoint and the `provider_id` field; the README lists the endpoint.
- Tests: `llmcalls_test.go` (`TestUsageKeepsProvidersApart`, `TestAttemptsArePagedNewestFirstWithATotal`); `server/models_api_test.go` (attempts paging, since, bad query, empty list, a 500 that does not leak the cause); in `admin_test.go` the regex that scans the SPA's API calls now allows a hyphen (it cut `/api/v1/llm-calls` at "llm").

Sub-step 1b, frontend (`assistant/internal/server/static`):
- `as-usage` gains a Provider column and a Group by control (user, provider, model). Sums are done in the browser over the finest-grain rows. The group is an attribute; address `#/usage?period=&group=`.
- New `as-attempts` component: columns Started, User, Provider, Model, Outcome, Input tokens, Output tokens, Cost, Duration; period filter; `dp-pager`; 25 rows per page; address `#/attempts?period=&page=`.
- `as-model-list` columns are now Provider, Model, Tags, Priority, Availability; new page `#/models`.
- `app.js` has a side panel (Usage, Models, Attempts) in the `dp-shell` sidebar slot. The top menu stays Chat, Tasks, Usage, with Usage active on all three pages. Config is not added in the assistant (no config content yet).
- CSS: compact tables; long model and user names are truncated with a tooltip; tags and availability wrap.

Validation run during step 1 (to be recorded in full under Validation):
- `task test-assistant` exit 0 (vet and `go test -race`, 18 packages ok) against dev cluster context `k3d-k8s-lab` with the database reachable. The new DB tests were also run with `-v` and passed, not skipped.
- `node --test` in `assistant/internal/server/static/components`: 336 pass, 0 fail, 0 skipped.
- Browser check with Playwright (Chromium) against the real static files and the real `shared/ui` dist, served by a local mock API (not the Go server, not deployed): 1280 px light and dark, 1024 px light; no console errors; no page-level horizontal overflow. Found and fixed: clipped Cost column, clipped Availability column, 24-character timestamps, wrapping badges. At 1024 px with the side panel open the Attempts table still scrolls sideways inside its card and fits when the panel is collapsed. Pager, group-by and panel collapse were driven in the browser.
- Not checked: phone width, right-to-left text, empty and error states in the browser (covered by component tests), the real Go server with real data, deployment to dev.

Known: cost shows four decimals below $1 (existing formatter).

Step 2 (shared tables):
- `shared/ui/src/format.ts` (tokens, cost, duration, compactTime, fullTime, DASH), `shared/ui/src/table.css` (shared table look: sideways scroll in a box, ellipsis for long names, badges themed by tokens), and three presentational Lit components registered in `dp-core.ts`:
  - `dp-usage-table`: columns User, Provider, Model, Calls, Input tokens, Output tokens, Cost; a null user, provider or model shows a dash.
  - `dp-model-table`: Provider, Model, Tags, Priority, Availability; states available, held_out with a return time, no_credentials.
  - `dp-attempt-table`: Started, User, Provider, Model, Outcome, Input tokens, Output tokens, Cost, Duration.
- Each table takes `rows` as a property, an `empty-text` attribute and an optional `labels` object that replaces column headings and availability words, so a localised app such as phraseforge can translate them. They only draw rows; the host loads, groups and pages.
- Assistant `as-usage`, `as-attempts` and `as-model-list` keep loading, filters, grouping, paging and refresh, and hand rows to the shared tables; their table markup and table CSS were deleted.
- Tests: new `format.test.ts` and one test file per table (18 tests). `task test-ui` in `shared/ui` exit 0: 339 pass, 0 fail, 0 skipped, `tsc --noEmit` clean, Go handler test ok. Assistant frontend `node --test`: 336 pass, 0 fail, 0 skipped (tests now assert the rows handed to the table; rendering is tested in `shared/ui`). Assistant Go embed and SPA-call checks pass (`TestOnlyTheFilesThePageNeedsAreEmbedded`, `TestEveryComponentOnDiskIsEmbedded`, `TestTheSPACallsOnlyDocumentedAPIOperations`).
- Browser check (Playwright, Chromium, 1280 and 1024 px, local mock API serving the real static files and the rebuilt `shared/ui` dist): the page assigns rows before the shared bundle upgrades the elements and the tables still fill (12, 3 and 25 body rows); group-by and select-driven regrouping work; no console errors; the Attempts table shows all nine columns at 1280 px; the Models table does not scroll at 1024 px. Mock server stopped, port free.
- Bugs found and fixed during the step: a leftover line that threw in `as-attempts` and was reported as a load failure; the error message text was no longer cleared after a successful load.
- Shared change: a change under `shared/ui` needs a changelog entry in every artifact that consumes it (assistant, phraseforge, knowledge): to be done at B4/B5.

Step 3a (phraseforge: Jobs become Tasks, Clear):
- `phraseforge/internal/jobs/jobs.go`: `DeleteAllDone` (removed only status 'done') is renamed `DeleteCompletedAndCancelled` and removes 'done' and 'cancelled'; pending, running and failed are kept (failed stays for inspection or Retry). The handler `apiClearAdminJobs` in `server/jobs.go` calls it; the API path POST /api/v1/admin/jobs/clear is unchanged.
- User-facing text: English "Jobs" became "Tasks" (nav.jobs, jobs.title, jobs.empty, the cancel, retry and delete confirmations, and the status messages "Task cancelled.", "Task requeued.", "Task deleted." in `static/js/jobs-app.js`). The Clear confirmation now reads "Remove every completed and cancelled task? Pending, running, and failed tasks are kept." with a Polish equivalent (Polish already said "Zadania").
- Test: `phraseforge/internal/jobs/clear_db_test.go` `TestDeleteCompletedAndCancelledKeepsTheRest` (done and cancelled removed; failed, pending, running kept; a second Clear removes nothing).
- Validation: `go test -count=1 -v ./...` in phraseforge against the dev cluster (context k3d-k8s-lab) database assistant_test with PHRASEFORGE_TEST_DSN set (throwaway schemas, port-forward stopped afterwards): 619 passed, 0 failed, 8 skipped; the new test ran and passed. The 8 skips need PHRASEFORGE_TEST_DB: seven job checkpoint tests (Retry, Complete, progress; code not changed here) and TestAdminConfigExportImportRoundTripDB (wipes the database; deliberately not run). `task test-phraseforge-frontend`: 70 pass, 0 fail, 0 skipped. Not done: the page was not opened in a browser and phraseforge was not deployed.
- Still to do in step 3: the internal section id and address stay `jobs` (#/jobs) for now; renaming the address and moving phraseforge's main menu to the top bar happens with the menu slice. `phraseforge/README.md` line about "The Jobs list of the admin panel" needs updating at B4, and phraseforge's CHANGELOG needs an entry at B5.
- Finding (superseded by the correction in Step 3b): phraseforge's main menu (Texts, Dialogs, Vocabulary, Models, Admin, Jobs) was a vertical dp-tabs in the left sidebar (`shell.js`). The target top bar is Documents, Tasks, Usage and Config; the four content sections stay in the sidebar.

Step 3b (phraseforge: main menu in the top bar):
- Correction (user, 2026-10-21: "Top menu = Documents, side menu = texts, dialogs, vocabulary, models; as before"; "It was a simple move of admin and jobs"): the first version of this step wrongly put Texts, Dialogs, Vocabulary and Models in the top bar. That misreading was the coordinator's and was corrected the same day. The statements below are the corrected truth.
- The top bar (`layout.html` has `<dp-tabs slot="header" id="mainNav" variant="underline">`; `shell.js` builds its items in `buildMainNav`, also after a locale change) is Documents and, for an admin, Tasks, Usage and Config (nav ids `documents`, `tasks`, `usage`, `config`). A non-admin sees only Documents. Documents groups the four content sections, which stay the pages of the left sidebar as before (a vertical dp-tabs under the language filter). The Documents item opens the content section the reader was last on (default texts). Usage and Config list their own pages in the sidebar; Tasks has no sidebar.
- The two admin sections' internal ids were renamed so the menu item, the address, the container element and the bootstrap key all use one name: `jobs` became `tasks` (address #/tasks, container `tasks-app`, bootstrap key `tasks`) and `admin` became `config` (#/config, `config-app`, key `config`); `app.go` bootstrap keys, `app.html` containers, `shell.js`, `jobs-app.js` and `admin-app.js` follow. Per the user's direction (2026-10-21: "Do not keep redirects from old paths. Make the code simple") there is no redirect or alias for the old addresses: an old #/jobs or #/admin falls through to the existing unknown-section rule and lands on #/texts.
- Strings: nav.jobs and nav.admin became nav.tasks ("Tasks"/"Zadania") and nav.config ("Config"/"Konfiguracja"); new nav.main ("Main"/"Główne") is the menu's accessible name; new nav.documents ("Documents"/"Dokumenty"); `app.go`'s `chromeI18nKeys` lists them.
- The language filter and the four content pages (texts, dialogs, vocabulary, models) show only on the content sections; on Tasks the sidebar element is taken out of dp-shell (dp-shell draws a sidebar for as long as it has a child in the slot) so no empty panel shows, and it is put back on the other sections.
- Browser check of the correction (Playwright, local harness with the real `shell.js`): admin top bar "Documents|Tasks|Usage|Config"; sidebar "filter ; Texts|Dialogs|Vocabulary|Models"; clicking Models in the sidebar gives #/models with Documents still active; Tasks then Documents returns to #/models; Usage and Config show their own pages; a non-admin sees "Documents" only; no console errors. Phraseforge frontend tests: 52 pass. The corrected build was redeployed to dev (`task deploy-phraseforge`, context k3d-k8s-lab): rollout succeeded, pod Running, the served shell.js contains the new menu code.
- Validation of the first version (before the correction; its menu result is superseded): phraseforge `go test -count=1 ./...` all ok (database tests skip without PHRASEFORGE_TEST_DSN; they were run with it in step 3a, and this slice changed only `app.go` and `i18n.go` on the Go side); `task test-phraseforge-frontend`: 70 pass, 0 fail, 0 skipped. `shell.js` has no unit tests. Browser check (Playwright, Chromium) against a local harness that serves the real `shell.js`, section scripts, layout CSS and the real `shared/ui` dist with stubbed API data (not the Go server, not deployed): for an admin the menu is Texts, Dialogs, Vocabulary, Models, Tasks, Config and for a non-admin only the first four; the active item follows the address; clicking Config, Texts and Tasks changes the address and the active item; the language filter shows only on content sections; no empty sidebar on Tasks and Config; no page-level horizontal overflow at 1280, 1024 and 700 px; at 700 px the menu scrolls sideways inside the header (dp-shell's existing behaviour) and the user name shrinks. Harness stopped, port free. One page error seen on #/config came from the harness stub lacking the IME preset data the Config page reads; not from this change.
- Not checked: the real server with a real login, deployment to dev, a phone width below 700 px, right-to-left text.
- Remaining for step 3: Usage and Config screens (the admin LLM tab content moves to Usage with the shared tables and a usage endpoint; the other admin tabs to Config with sub-pages in the sidebar); `phraseforge/README.md` still mentions "Admin > LLM tab" and "The Jobs list of the admin panel" (B4); phraseforge CHANGELOG entry (B5).

Step 3c-1 and 3c-2 (phraseforge: record the user, usage endpoint, paged attempts):
- Decision (user, 2026-10-21): phraseforge records the user of a model call. Finding that led to it: phraseforge's llm_calls had no user and jobs had no requester.
- Schema (`phraseforge/internal/db/schema.sql`, applied by `phraseforge migrate`, which the Helm chart runs as a pre-upgrade Job and `task deploy-phraseforge` as a Job, idempotent): `ALTER TABLE jobs ADD COLUMN IF NOT EXISTS user_id bigint REFERENCES users(id) ON DELETE SET NULL`; the same on llm_calls plus index llm_calls_user_idx (user_id, started_at). Additive and nullable; old rows and system work show no user. A test applies the schema a second time and checks the rows and columns survive. Deployment note: the index build and the foreign key take brief locks on llm_calls when the new version starts on dev and prod.
- Mechanism: `jobs.WithUserID`/`UserID` context helpers (`jobs/jobctx.go`); `server.requireAuth` puts the signed-in user in every request context; `Service.enqueue` stores it as jobs.user_id (a Retry keeps the user of the job it retries, falling back to the retrying user for a job with none); `runOne` restores it in the handler context so follow-up jobs and model calls inherit it; `llmcalls.Recorder` writes it to llm_calls.user_id (`Call.UserID`). `Job.UserID` is not part of the admin API's JSON.
- Endpoints: new GET /api/v1/admin/llm-usage?since= (admin only): successful attempts summed per user, provider and model, highest cost first, items {user_id, username, provider_id, model_id, calls, input_tokens, output_tokens, cost_usd}; user_id and username are null for calls with no user. GET /api/v1/admin/llm-calls is now paged: offset parameter (integer, 0 or more), response {items, total, limit, offset}, default limit 25 (was 50), maximum 200, and each item has user_id and username. `Store.List(ctx, filter, limit, offset)` returns (items, total, err); `Store.UsageByUserAndModel` is new.
- Tests (all ran, none skipped): jobs: TestEnqueueRecordsTheUserInTheContextAndNoneWithoutOne, TestRetryKeepsTheUserOfTheJobItRetries, TestAHandlerSeesTheUserTheJobWasQueuedFor, TestTheSchemaCanBeAppliedAgainAndKeepsTheUserColumns, TestUserIDRoundTripsThroughTheContextAndIsAbsentWithoutOne; llmcalls: TestACallIsRecordedForTheUserInTheContextAndNoneWithoutOne; server: TestLLMCallsArePagedWithATotalAndNameTheUser, TestLLMUsageSumsOnlySuccessfulCallsPerUserProviderAndModel, TestLLMUsageOfNoCallsIsAnEmptyArray, plus the existing llm-calls tests updated (default limit 25, new bad-parameter cases, documented-fields check, non-admin refusal and login redirect for the new route).
- Validation: `go test -count=1 -v ./...` in phraseforge against the dev cluster (context k3d-k8s-lab) database assistant_test with PHRASEFORGE_TEST_DSN: the last full run was 625 passed, 0 failed, 8 skipped; the targeted run of internal/server, internal/llmcalls and internal/jobs: 152 passed, 0 failed, 8 skipped. The 8 skips need PHRASEFORGE_TEST_DB (seven job checkpoint tests including TestAHandlerSeesItsJobIDInItsContext, and the database-wiping round trip) and cover code not changed here. Port-forward stopped, port free.
- Not done: a direct model call made by a handler is attributed through the request context, which was not checked call by call; the pf-llm-attempts component still reads only `items` (the Usage screen, step 3c-3, replaces it); no deployment.

Step 3c-3 (phraseforge: the Usage section):
- Shared: `dp-usage-table` gained a `groupBy` property ("user", "provider", "model"; attribute group-by) and an exported `grouped()` function, so the summing over the finest-grain rows (by user id, else user name; highest cost first; omitted fields null) is written once. The assistant's as-usage lost its own grouping code and hands the table all rows plus the grouping (its tests now check that). New tests in shared/ui for the grouping.
- phraseforge Usage section (admin only, top menu item "Usage" between Tasks and Config): `static/js/usage-app.js`, container `usage-app` in app.html, bootstrap key `usage` in app.go, section registered on window.pfSections.usage. The address names its page: #/usage, #/usage/models, #/usage/performance, #/usage/attempts[?page=N]; router.js parse/build gained a `sub` field (a lower-case word after the section; an item id stays numeric), with tests. shell.js lets a section list its own pages in the sidebar (a `sidebarNav(route)` hook); Usage lists Usage, Models, Performance and Attempts there as a vertical dp-tabs.
- Pages: Usage = period filter, group-by (user, provider, model; default one row per user, provider and model), summary line, dp-usage-table with columns User, Provider, Model, Calls, Input tokens, Output tokens, Cost from GET /api/v1/admin/llm-usage (a call with no user shows a dash). Models = dp-model-table (Provider, Model, Tags, Priority, Availability; states available, cooling down, credentials missing) with a Refresh button from GET /api/v1/admin/models, highest priority first. Performance = the existing pf-llm-performance component unchanged. Attempts = period, model and outcome filters, dp-attempt-table, dp-pager at 25 per page via the address, the existing pf-llm-purge control, from GET /api/v1/admin/llm-calls with limit, offset and total.
- Removed from the Admin (Config) LLM tab: the models, performance, attempts and purge cards and their code in admin-app.js (the prompt configuration, which stays in Config, is untouched). Components pf-llm-models and pf-llm-attempts and their tests were deleted (replaced by the shared tables); pf-llm-performance and pf-llm-purge remain. Information no longer shown in the attempts list: the purpose (kind) column, the HTTP status and the request id (the API still returns them); the error text is the outcome badge's tooltip.
- Strings: the models, performance, attempts and purge strings moved from adminAppI18nKeys to a new usageAppI18nKeys (server/usage_app.go) with the LLM kind names; new keys nav.usage and usage.* in English and Polish (including usage.col_duration, replacing "Duration (s)" whose values already carry the unit). Test usage_app_i18n_test.go checks that every key usage-app.js uses is served and translated in every locale.
- Validation: phraseforge `go test -count=1 -v ./...` against the dev cluster (context k3d-k8s-lab) database assistant_test with PHRASEFORGE_TEST_DSN: 629 passed, 0 failed, 8 skipped (the same 8 that need PHRASEFORGE_TEST_DB: seven job checkpoint tests and the database-wiping round trip); port-forward stopped. `task test-phraseforge-frontend`: 52 pass, 0 fail, 0 skipped (fewer than the 70 before because the tests of the two deleted components went with them; two router tests were added). `task test-ui`: 344 pass, 0 fail, 0 skipped, Go handler ok. Assistant frontend: 335 pass, 0 fail, 0 skipped. Browser check (Playwright, Chromium, 1280 px) against a local harness that serves the real shell.js, usage-app.js, layout CSS, the real English strings from i18n.go and the rebuilt shared/ui dist, with stubbed admin endpoints (not the Go server, not deployed): all four pages render with the sidebar marking the current page; grouping by user gives 3 rows; the pager goes to page 2 (#/usage/attempts?page=2); changing the outcome filter returns to the first page; the Tasks page has no sidebar; no console errors, no page overflow. Found and fixed in the browser: the cost summary showed "$18.6" (now currency formatted), the "Group by" label wrapped, a wide model select pushed the filters to a second row, and a raw key showed as the duration header (the key had not been added).
- Not checked: the real server with a real login, deployment to dev, narrow widths (phone), right-to-left text, dark theme on these pages, a model call made directly by a handler being attributed to its user, Polish wording in the browser. The sidebar list is a vertical dp-tabs, which looks different from the assistant's plain link list (to unify later).
- Remaining for step 3: the Config screen (the existing admin tabs under Config with sub-pages in the sidebar); `phraseforge/README.md` still describes "Admin > LLM tab" with the monitoring cards and "The Jobs list of the admin panel" (B4); CHANGELOG entries (B5).

Step 3d (phraseforge: the Config pages):
- The Config section's former in-page tab bar (dp-tabs "adminTabs" in admin-app.js) is gone. Its pages are listed in the sidebar (admin-app.js now has `sidebarNav(route)`, which shell.js calls) and named by the address: #/config (Permissions, the default), #/config/ime, #/config/llm, #/config/languages and #/config/transfer (the export/import page, whose internal tab id changed from `config` to `transfer`, with container tabTransfer and renderTransferTab). An unknown page after #/config shows Permissions. shell.js lists `config` among the routed sections so show(route) gets the route. The page title (h1) is the current page's name; the old "Admin" title is gone.
- Strings: admin.tab_config became admin.tab_transfer ("Export / import", Polish "Eksport / import"); nav.config is now served to the Config page; admin.title was removed (unused).
- Behaviour kept: every mutation still re-fetches GET /api/v1/admin and re-renders the current page (loadAndRender); only the switching moved.
- Validation: phraseforge `go test -count=1 -v ./...` against the dev cluster (context k3d-k8s-lab) database assistant_test with PHRASEFORGE_TEST_DSN: 629 passed, 0 failed, 8 skipped (the same 8 that need PHRASEFORGE_TEST_DB), including TestEveryAdminAppKeyIsServedAndTranslated and TestEveryUsageAppKeyIsServedAndTranslated; go vet and gofmt clean; port-forward stopped. `task test-phraseforge-frontend`: 52 pass, 0 fail, 0 skipped. Browser check (Playwright, Chromium, 1280 px) against the local harness with the real shell.js, admin-app.js, the real admin strings and stubbed data: all five pages show the right content and title with the right sidebar item active, the Config menu item active on each, clicking sidebar items changes the address, no in-page tab bar, no console errors. Harness stopped, port free. The admin script has no unit tests; its forms were not exercised (saving a grant, an IME config, a prompt, a snippet, an import).
- Not checked: the real server with a real login, saving or importing through the forms, deployment to dev, narrow widths, dark theme, Polish wording. Known leftover copy: the LLM page's hint text still says "from the Languages tab".
- Step 3 (phraseforge) is complete: Jobs became Tasks with the new Clear rule, the main menu is in the top bar, Usage and Config have sidebar pages. Remaining: step 4 (knowledge navigation) and step 5 (documentation: phraseforge/README.md still describes "Admin > LLM tab" with the monitoring cards and "The Jobs list of the admin panel"; changelog entries for assistant, phraseforge, knowledge and every consumer of shared/ui).

Step 4 (knowledge: navigation):
- Decisions (user, 2026-10-21): in knowledge the ingest Drafts go under Documents (not a top item called Ingest); knowledge has no admin or config concept and no config content, so it gets no Config item (the plan's "Config" for knowledge is dropped for that reason); "Tasks" is the old Jobs list.
- Knowledge menu (knowledge/internal/server/static/index.html, app.js): top bar Documents, Chat, Tasks (with its count, "Tasks (N)"); a new sidebar list `documentsNav` (a vertical dp-tabs) with Documents and Drafts (with its count, "Drafts (N)"), shown above the current page's own panel on the Documents and Drafts pages only; the Drafts page keeps the ingest form (URL, file upload) in its panel; a new Tasks tab (tasksTab, tasksAside with a Refresh button) holds the jobs list that used to be the Jobs sub-tab of Ingest. The old Ingest tab's sub-tab bar (ingestSubNav), its sub-views, showIngestSection and refreshIngestLists were removed; the counts are kept by updateNavCount; the drafts and tasks are loaded at start and after an ingest job finishes, so the counts stay current. User-facing words "job" became "task" in the list's empty state, the retry message, the delete confirmation and the deleted message. Knowledge's address does not name the page (it has no router; unchanged).
- Tests: the old components/app-ingest-subnav.test.js was replaced by components/app-navigation.test.js, which extracts the real wiring, showTab and count source from app.js and checks the menu and list items, showTab's workspace/panel/list/menu state, the menu and Documents-list clicks, loading on open, the list view on re-entry, and that each count updates its own label without disturbing the active item. `task test-knowledge-frontend`: 24 passed, 0 failed, 0 skipped. Browser check (Playwright, Chromium, 1280 px) against a local harness serving the real static files and the shared/ui dist with stubbed API data: menu "Documents|Chat|Tasks (3)", list "Documents|Drafts (2)"; the right workspace and panel on each page; the Documents list only on Documents and Drafts; the menu stays on Documents while on Drafts; no console errors, no page overflow; harness stopped.
- Not checked: knowledge against its real server and login, the document detail and chat pages after the change, narrow widths, dark theme. Knowledge has no Clear button for tasks (per-task delete only); not added.
- Remaining: step 5 (documentation: READMEs, e.g. phraseforge/README.md still describes the Admin > LLM tab and "the Jobs list of the admin panel"; changelogs for assistant, phraseforge, knowledge and every consumer of shared/ui).

Deployment (user instruction 2026-10-21: redeploy using k8s-lab/.k3d/kubeconfig; the user said they mostly care about phraseforge):
- phraseforge only was deployed, with `task deploy-phraseforge` from the repo root against context k3d-k8s-lab (the dev cluster; kubeconfig /Users/jprusaczyk/Documents/Sources/DPurge/k8s-lab/.k3d/kubeconfig, pinned by the Taskfile). The migration job completed ("Schema applied"), the rollout succeeded, the pod is Running 1/1 with no restarts, the log says phraseforge is listening.
- Checked afterwards read-only: jobs.user_id and llm_calls.user_id exist in the dev phraseforge database (llm_calls has 0 rows there, so Usage is empty until models are called); /static/js/usage-app.js, /static/js/shell.js and /ui/dp-core.js answer 200 at http://phraseforge.localhost:8080 and the served shell.js contains the usage section.
- Not done: the page was not opened with a login on dev; assistant and knowledge were not redeployed (their changes are in the working tree: the assistant's Usage, Models and Attempts pages and shared tables, and knowledge's menu).

## Validation

Per-step commands and results are in Implementation Notes; this is the summary.

Suites last run:
- phraseforge Go, against the dev test DB assistant_test: 629 passed, 0 failed, 8 skipped (the 8 need PHRASEFORGE_TEST_DB).
- phraseforge frontend: 52 pass.
- shared/ui: 344 pass.
- assistant frontend: 335 pass.
- knowledge frontend: 24 pass.
- assistant Go full run: exit 0, earlier in step 1.

Browser checks were made against local harnesses with stubbed data, not against real servers with logins.

Never checked: real login sessions on dev, the knowledge and assistant deployed builds, narrow widths, dark theme, Polish wording, saving through the Config forms.

## Documentation Review

- READMEs checked against this change: assistant, phraseforge, knowledge, shared/ui.
- Changelogs: assistant, phraseforge and knowledge (the three consumers of shared/ui); shared/ui itself has no changelog.
- The dictionary artifact has no GUI and consumes none of this.
- Not done: constitution files (specs/tech-stack.md etc.) were not reviewed for drift.
- Known drift outside this change: knowledge/README.md line ~39 still lists kb-button, kb-nav, kb-card, kb-field, kb-dialog, kb-status-bar.

## Documentation Updates

- READMEs updated for assistant, phraseforge, knowledge and shared/ui.
- Changelog entries under [Unreleased] added for assistant, phraseforge and knowledge.
- Not done: constitution files not updated; the stale knowledge/README.md line (~39) left as is; assistant and knowledge were not redeployed (only phraseforge was, on the user's instruction that they mostly care about phraseforge).
