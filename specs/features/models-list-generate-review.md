---
title: Generate and Review buttons on the Models list
kind: feature
status: done
version: 3
updated: 2026-10-14
branch: main
---

## Problem / Motivation

The Vocabulary list page has a Generate button (queues one item job per item
still missing a translation in the user's locale, or a transcription its
language keeps: `apiGenerateMissingVocabTranslations`) and a Review button
(queues a model review of every item, with a status column per item and a
dialog of proposals to apply or dismiss: `server/review.go`,
`vocabulary/review*.go`, `ai/review.go`, `ai/reviewjob.go`). The Models list
has neither, although its item jobs already exist (`models_item`, used by the
per-item Generate buttons). Evidence: `models-app.js` has no list-level
generate or review; `server.go:246-247` has only the per-item generate routes.

Decisions taken with the user (2026-10-14): Generate first, then Review, each
shown for review before the next; the review checks the phrase, the
transcription (only when the language has a transcription snippet) and each
locale's translation; the review is a new purpose `reviewModels` with its own
prompt and settings, a blank provider or model falling back to
`reviewVocabulary`'s (as the text reviews do).

## Acceptance Criteria

- [x] Generate (editors only): a button on the Models list view queues one
  `models_item` job per item still missing a translation in the user's locale
  or a transcription its language keeps, and says how many were queued or that
  none were needed. Same wording pattern as Vocabulary's.
- [x] Review (editors only): a button on the Models list view queues one review
  job per item that has none waiting, and says how many; each item has a status
  in an Actions column (pending, reviewed, N open, outdated) like Vocabulary's.
- [x] A review job makes one structured call per item with the phrase, its
  transcription (when the language has a snippet), the translations and the
  language's rules, and stores proposals (replace phrase with its
  transcription, replace transcription, replace a translation, or a remark)
  per item in a new table; nothing is written by the job.
- [x] A proposals dialog per item applies or dismisses each proposal; a
  proposal is refused (outdated) when the item's phrase changed since the
  review and is a conflict, left open, when the field was edited since.
- [x] New purpose `reviewModels` appears in Admin > LLM settings with its own
  prompt; blank provider/model uses `reviewVocabulary`'s.
- [x] Both buttons need edit right on the list's language, like Vocabulary's.
- [x] Tests use a fake LLM only. No Ollama call is made; at most 1-3 real calls
  on dev, only if the user agrees.
- [x] Vocabulary behaviour is unchanged (except its review column polling through
  the shared helper, see step 5).
- [x] The Models and Vocabulary review columns, the text and dialog review row and
  the Jobs page refresh themselves while work is pending, through one helper
  with backoff, a pause while hidden and an upper bound; none polls when
  nothing is pending.

## Approach

Ordered, each step shown to the user before the next.

1. **Generate** (one step): `POST /api/v1/models/{id}/generate-missing-translations`
   mirroring the vocabulary handler with `ItemState{ResourceType:"models_item"}`
   (`apiGenerateMissingModelsTranslations`, `server/models.go`); a Generate
   button and status messages in `models-app.js`; i18n keys in
   `i18n.go` (en, pl). Tests: handler with a fake store: queues for missing,
   none when complete, 403 without edit right.
2. **Review backend**: purpose `reviewModels` (`ai.go` kinds and
   placeholders, `config` default prompt file, `schema.sql` kind CHECK via the
   existing drop/recreate pattern, `admin` labels, `configmap.yaml`); table
   `models_reviews` (item_id PK referencing `models_items`, phrase, findings,
   response, created_at); `models.Store` ReviewSource/SaveReview/GetReview;
   `ai.Service.ReviewModelsItem` with reply schema and parser; job kind and
   handler. Tests with a fake LLM and the dev DB.
3. **Review endpoints and apply/dismiss**: list and item review routes under
   `/api/v1/models/{id}/...` mirroring `server/review.go`; apply writes
   phrase/transcription/translation guarded as Vocabulary's does.
4. **UI**: Review button, Actions column status with polling, then the
   proposals dialog; i18n. Screenshots at 1280 and 1024 px, RTL list.
5. **Shared polling helper** (added 2026-10-14 at the user's request, after
   step 4: polling put load on the server and the Jobs page did not refresh at
   all): `static/js/poll.js`, `pfPoll({run, anchor})`, used by the Models and
   Vocabulary review columns, `text-review.js` and the Jobs page. It polls only
   while `run()` reports pending work; backs off 8 s, 15 s, 30 s; pauses while
   the tab is hidden and refreshes once when visible again; stops after 15
   minutes of polling; `kick()` restarts it at 8 s after any user action; stops
   when its anchor has left the page or its section is hidden (the shell hides
   sections with `display: none`, so the old `root.contains` check kept polling
   in the background). No note on the Jobs page. Tested with Playwright's fake
   clock; no server change.
6. Dev deploy and check; docs and changelog.

Shared code between vocabulary and models review is extracted only where a
copy would be a straight duplicate of an unchanged function (YAGNI otherwise).

Risk: adding a kind to `llm_prompts_kind_check` must migrate an existing
database, not only a new one: tested against a dev DB created with the old
constraint before it reaches prod. The Helm pre-upgrade hook applies
`schema.sql`, so the migration must be idempotent.

## Affected Areas

- `phraseforge/internal/server/models.go`, `server.go`, new review handlers
- `phraseforge/internal/server/static/js/models-app.js`
- `phraseforge/internal/ai/` (new purpose, review for models items, job)
- `phraseforge/internal/models/` (review store, apply)
- `phraseforge/internal/config/` (default prompt, purpose config)
- `phraseforge/internal/db/schema.sql`, `internal/i18n/i18n.go`,
  `phraseforge/k8s/configmap.yaml`, admin purpose list
- `phraseforge/README.md`, `phraseforge/CHANGELOG.md`

## Out of Scope

- Changing the Vocabulary Generate or Review behaviour.
- Reviewing models list metadata (title, tags).
- Running the review on prod or deploying it (the user deploys).
- Commits: the user commits manually.

## Implementation Notes

Step 1 (Generate), done 2026-10-14:

- `server/models.go`: `apiGenerateMissingModelsTranslations`, route
  `POST /api/v1/models/{id}/generate-missing-translations` in `server.go`. It
  mirrors the vocabulary handler with `ItemState{ResourceType: "models_item"}`;
  the loop over items is a small copy, not shared, because the vocabulary
  version was left untouched.
- `models-app.js`: Generate button (editors only) and the started/none
  messages; keys `models.generate_missing_translations*` in en and pl, added
  to `modelsAppI18nKeys`.
- Test `models_generate_missing_test.go`: an empty list queues nothing; 2 of 3
  items missing a translation queue 2 `generate_models_item` jobs; a student
  gets 403 and queues nothing; an unknown list is 404.

Step 2 (Review backend), done 2026-10-14:

- Purpose `review_models`: `ai.KindReviewModels` in `ValidKinds`, placeholders
  (`sourceLanguage`, `phrase`, `transcription`, `translations`,
  `transcriptionPrompt`), `reviewModelsDefault` (blank provider/model falls back
  to `reviewVocabulary`'s), `config.ReviewModels` + yaml `reviewModels`, default
  prompt `prompts/default/system/review-models.txt`, a `reviewModels` block in
  `k8s/configmap.yaml` generated from that file (the config test keeps them
  byte-identical), admin label `admin.llm_kind_review_models` (en, pl).
- `schema.sql`: the kind list moved out of `CREATE TABLE` into an idempotent
  `DROP CONSTRAINT IF EXISTS` / `ADD CONSTRAINT llm_prompts_kind_check`, so an
  existing database is widened too; new table `models_reviews` (same shape as
  `vocabulary_reviews`).
- `models/review.go`: `ReviewSource`, `SaveReview` (guarded on the phrase, like
  Vocabulary's), `GetReview`, `ErrReviewNotFound`.
- `ai/review_models.go`: reply schema, `parseReviewModelsReply` (a phrase
  rewrite needs a transcription suggestion where the item has one and the
  language keeps one), `ReviewModelsItem` (one structured call, corrected within
  the attempt budget), job kind `review_models_item`, `NewReviewModelsJob`.
  `reviewjob.go`: the body of `HandleReviewItem` became `performReviewJob`,
  shared with `HandleReviewModelsItem`; `ReviewStore` is reused for both.
- `main.go`: `modelsReviewStore` adapter, `SetModelsReviewStore`, the job
  registered.

Tests: `ai/review_models_test.go`, `models/review_test.go`, the config test
extended. Migration checked by hand against a throwaway dev schema built from
the old `schema.sql` (git HEAD): the old constraint rejects `review_models`,
the new one accepts it, an existing row survives, a second apply is a no-op.

Step 3 (Review endpoints and apply/dismiss), done 2026-10-14:

- `models/review_apply.go`: `ListReviews`, `IsReviewStale`, `FindingList`,
  `DismissReview`, `ApplyReview`, mirroring `vocabulary/review_apply.go` for the
  three fields a models item has (phrase with its transcription, transcription,
  translation). Translation and transcription findings are applied before the
  phrase rewrite; a value edited since the review is a conflict and stays open;
  the review follows the item's new phrase (also after a merge into an existing
  phrase).
- `server/review_models.go`: `POST /models/{id}/review`, `GET /models/{id}/reviews`,
  `POST|GET /models/{id}/items/{position}/review`, `.../review/apply`,
  `.../review/dismiss`, all needing CanEdit on the list's language; routes in
  `server.go`. One waiting review job per item (`ActiveItemIDs`).
- Tests: `models/review_apply_test.go`, `server/review_models_test.go`.

Step 4 (Review UI), done 2026-10-14:

- `models-app.js`: a Review button (editors only), an Actions column on the
  list view (`renderItemsTable(list, false, list.canEdit)`), per-item status
  (Review, pending, N open, Reviewed, Outdated with a Review again) with polling
  while a job waits, and the proposals dialog with Apply, Dismiss, Apply all,
  Dismiss all and Review again. Ported from `vocabulary-app.js` (frontend code is
  per-app by convention), for phrase, transcription and translation only; the
  dialog is built with the DOM API because proposals are model output.
- The generic review strings are the existing `vocabulary.review_*` keys,
  listed in `modelsAppI18nKeys`; no new strings. `layout.html`: the table's
  button styles also apply to `.models-items-table`.
- Dev check (deployed; Playwright at 1280 and 1024 px; temporary Arabic list with
  seeded reviews, no model call, Review itself not clicked): buttons Copy,
  Generate, Review, Edit, Delete; statuses `3 open`, `Reviewed`, `Review`,
  `Outdated` + `Review`; the dialog shows current and suggested values and
  Apply wrote the translation (the column then said `2 open`). The real dev DB
  took the widened `llm_prompts_kind_check` and `models_reviews` from the
  deploy. Temporary data removed.

Step 5a (shared polling helper and the Models column), done 2026-10-14:

- `static/js/poll.js` (`window.pfPoll({run, anchor})`, `kick()`), loaded in
  `app.html` before `text-review.js`. Rules as in the Approach; a timer that
  fires while the tab is hidden makes no request and does not count towards the
  15 minute bound.
- `models-app.js`: the review column's own timer is replaced by the helper.
- Tests: `static/components/poll.test.js` (9 tests, real `poll.js` against node
  mock timers; `npm test` in `static/components` runs 57). Real page on dev with
  Playwright's fake clock and a stubbed `/reviews` reply of "pending": 8 s, 15 s,
  30 s, 30 s between requests; no request while the tab is hidden and one when
  it is visible again; one more poll after the work finished, then none; none
  after navigating to another section.

Steps 5b-5d (the other three callers), done 2026-10-14:

- `vocabulary-app.js` and `models-app.js`: an empty list has no table, so there
  is no poll (a `null` anchor would have thrown); `text-review.js`: anchored on
  its review row, one poll for both parts.
- `jobs-app.js`: `fetchAndRender(page)` renders without clearing the status bar
  (a poll must not wipe a message being read) and reports whether a job on the
  page is `pending` or `running`; `loadAndRender` clears the message and awaits
  `poll.kick()`, so the actions still set their success message after the
  refresh; `show()` stops the previous poll (the section root outlives its
  renders) and creates a new one. The Refresh button stays; no note on the page.
- `poll.js`: `kick()` returns a promise that settles when the run has finished;
  `stop()` is exposed. Two more unit tests (59 in `npm test`).
- Dev check, same fake-clock method on Vocabulary list 12, text 20 and the Jobs
  page (stubbed pending replies, no jobs queued): requests at 8, 15, 30, 30 s;
  none while hidden, one when visible again; none once nothing is pending; none
  after leaving the section. The text row makes two requests per poll (two
  parts). Empty Models and Vocabulary lists: no table, Review button present.
- Found, not caused and not fixed: `texts-app.js:235` (`showList`) throws
  `Cannot read properties of null (reading 'appendChild')` when the route
  changes while the Texts list is still loading (file unmodified since
  `581ff11`).

Step 6 (docs, race fix), done 2026-10-14: see Documentation Updates. The user
declined the optional real-model check, so no real model call was made; the
review job is covered by fake-model tests and a seeded-review browser check.

Race fix (asked for after step 5 found it): the list views of texts, dialogs,
vocabulary and models rendered a skeleton with `#listContainer`, awaited the
fetch and looked the container up again. A response that arrived after a detail
view replaced the root threw `Cannot read properties of null (reading
'appendChild')`, and one that arrived after a newer list render appended a second,
stale list. Each `showList` now numbers its render (`listRequest`) and applies a
response only if its render is still the current one and its container is on the
page. Reproduced first on the deployed old code with Playwright (held list
response, then a view, then the stale response: the exception in all four
sections; two list renders with the stale one answering last: one grid became
two), then verified after the fix: no exception, the view intact, still one grid;
normal lists render (25, 1, 3 and 2 entries).

## Validation

- `go vet`, `gofmt`, `go test ./...` pass against the dev DB; `npm test` in
  `static/components` runs 59 (48 before this feature plus 11 for `poll.js`).
- Generate, Review and the polling were each checked on the deployed dev app with
  Playwright (see Implementation Notes). The migration was applied to the real dev
  DB by the deploys, and checked beforehand against a throwaway schema built from
  the old `schema.sql`.
- Admin: the deployed admin API lists `review_models` with its default prompt and
  placeholders, and its label exists in English and Polish.
- No real model call was made (declined). A real end-to-end Models review has
  therefore not been seen; the job, parser, store and endpoints are covered with a
  fake model and the dialog with seeded reviews.

## Documentation Review

- `phraseforge/CHANGELOG.md`: user-facing, so entries are needed (Added: Models
  Generate, Models Review with the new purpose; Changed: the shared polling
  behaviour of four pages, which includes the Jobs list; Fixed: the list race).
  Nothing under `shared/` changed, so no other changelog needs an entry.
- `phraseforge/README.md`: the review purposes were listed as three, the Models
  review and the polling were undocumented, and the test note named only the
  vocabulary API tests.
- Constitution files unaffected.

## Documentation Updates

- `phraseforge/CHANGELOG.md` `## [Unreleased]`: two `Added`, one `Changed`, one
  `Fixed` entry.
- `phraseforge/README.md`: a `reviewModels` bullet (purpose, placeholders, storage,
  dialog, Generate, API), "all four review purposes", the `review_models` kind, a
  bullet on the shared polling helper, and the models API tests in the test note.
- Other repositories (user request after delivery, 2026-10-14):
  `prompt-eval`: `prompt/phraseforge/default/system/review-models.txt` (the source
  of truth; byte-identical to the embedded copy), a `review-models` suite
  (`review-models/promptfooconfig.yaml`, two cases, schema equal to
  `ReviewModelsSchema`, `num_ctx` 8192), entries in `scripts/sync-prompts.js` and
  a `review-models` npm script (no `:smoke`: only suites with a test generator
  honour `SMOKE`); `npm run check-sync` and `npm test` (78) pass. `jdp-helm`
  `jdp-frontend`: explicit `checkVocabulary`, `reviewVocabulary`, `reviewModels`,
  `reviewTranscription`, `reviewTranslation` in `values.yaml` and
  `templates/phraseforge.yaml`, plus a changelog entry; rendered with `helm
  template`, every block equal to the image defaults and loaded by phraseforge's
  own loader. Nothing there is deployed or committed.
- Reversed after delivery (user, 2026-10-14): the `reviewModels` purpose, like the
  other review purposes, no longer falls back to `reviewVocabulary`'s provider and
  model (the decision recorded in Problem / Motivation). No purpose falls back to
  another any more: `reviewVocabulary` no longer takes `vocabularyItem`'s either;
  every default states provider, model and `numCtx`; `config.Load` refuses a
  blank provider or model, naming the purpose; `reviewVocabularyDefault`,
  `reviewModelsDefault` and `reviewTextDefault` are gone and the dev ConfigMap
  states every field of every purpose (a config test enforces it).
  Same change in the other repositories: `jdp-helm` states all 14 purposes in full
  (`think` everywhere, `numCtx: 4096` for the four that left it unset; checked by
  rendering the chart and loading it with phraseforge's own `config.Load`), and
  `prompt-eval` sets `num_ctx: 4096` in the four matching suites and its sync
  check now compares the item suites' `num_ctx` too (a deliberately wrong value
  was caught).
- Prompt consistency pass (user, 2026-10-14; no model run, the user stopped the
  evals): the inline dictionary-form rules of `generate-vocabulary` moved to
  `{{dictionaryPrompt}}` (kept: "never the inflected form found in the text",
  because one Latin eval row without a snippet came back inflected without it,
  lemma check failing with the snippet-only wording and passing with the clause),
  `vocabularyItem` takes `{{dictionaryPrompt}}` for its `rewrite` rule, the
  `{grammar}` bullet points at the grammar snippet like the other prompts do, and
  the two reviews state the translation rules their generators enforce. Four real
  calls were made in total (Latin before and after, Latin after the fix, Arabic).
  Pinned by `internal/ai/default_prompts_test.go` (snippet sets per purpose, valid
  placeholders, no inline snippet content, generator and reviewer rules); the
  prompts in prompt-eval, phraseforge, the dev ConfigMap and the helm values are
  identical (`check-sync`, and 14 of 14 rendered helm prompts).
