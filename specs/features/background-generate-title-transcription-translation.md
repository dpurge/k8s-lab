---
title: Convert Title/Transcription/Translation generation to background jobs everywhere
kind: feature
status: done
version: 1
updated: 2026-09-25
branch: main
---

## Problem / Motivation

Ollama is slow enough that a blocking generation call is a real UX problem
(confirmed directly this session: a real `generate_vocab_from_text` job
ran ~30s+ before completing or being cancelled, and separately caused
enough resource contention to fail the pod's own liveness probe). Generate
Vocabulary/Generate Models already avoid this — they're background jobs,
returning immediately with a status-bar "started" message
(`generate.KindGenerateVocabFromText`/`KindGenerateModelsFromText`, see
`dialog-vocabulary-models-generation`). Title/Transcription/Translation
generation does not: `phraseforgeGenerate()`
(`phraseforge/internal/server/templates/layout.html:369-401`) blocks the
browser on `POST /llm/generate` until the LLM responds, then writes the
result directly into an open form field. This feature converts these to
the same background-job pattern, decided in full during B1 chat:

- Every "Generate" action (Vocabulary, Models, Title, Transcription,
  Translation — including the per-item Transcribe/Translate on Vocabulary/
  Models item forms) becomes a background job. Clicking it submits the job
  and shows a status-bar confirmation only — no live wait, no result
  inserted into an open form during the same interaction.
- A background job needs a real saved row to write its result into, so
  (matching how Generate Vocabulary/Models already only appear once a Text
  is saved) Title/Transcription/Translation generation moves off the New
  form entirely and onto the View page's action row instead — the Edit
  form doesn't gain a duplicate copy either, since View and Edit already
  operate on the same saved resource.
- A completed job writes its result **only if the target field is still
  blank** at completion time (never overwrites existing content, even on a
  `Retry` of a stale job) — this is a **universal** rule, not new-path-only:
  it also changes the existing backfill (post-import) and ingest paths,
  which were previously unconditional overwrites. This is intentional and
  a real, if narrow, behavior change worth calling out explicitly rather
  than treating as a silent side effect.
- Since a click that would be a no-op (field already has content) is
  visible ahead of time, the corresponding Generate button is hidden
  rather than shown-and-ignored.
- Vocabulary/Models item-level Transcribe/Translate (on the item add/edit
  form) get the same treatment, letting the old synchronous path be
  deleted entirely instead of surviving indefinitely for just these two
  forms.

## Acceptance Criteria

- [ ] `texts.Store`/`dialogs.Store` gain `SetTitleIfBlank`/
  `SetTranscriptionIfBlank` (guarded `UPDATE ... WHERE
  coalesce(trim(field),'')=''`, reporting `applied bool`, not a Go-side
  read-then-write — closes a TOCTOU race against a user editing the row
  while a minutes-long LLM call runs); `translations.Store` gains
  `SetIfAbsent` (`INSERT ... ON CONFLICT DO NOTHING`). Vocabulary/Models
  item stores gain the same guard added to their existing item-transcription/
  item-translation setters.
- [ ] `ai.Service.writeback` uses the guarded setters universally — this
  applies to the existing backfill (post-import) and ingest paths too, not
  only new live-generate clicks. A skipped write (field no longer blank)
  is recorded as job success, not failure — an empty LLM result is still a
  hard failure (unchanged, protects against accidentally clearing content).
- [ ] New endpoints, mirroring `apiGenerateFromText`'s existing shape
  exactly (auth check before enqueue, 404/403/202+job_id):
  `POST /api/v1/{texts,dialogs}/{id}/generate-title`,
  `.../generate-transcription`, `.../generate-translation` (body:
  `{"locale": "en"}`, validated via `i18n.IsValid`, 400 otherwise — an
  unvalidated locale was flagged during design as a low-severity issue: it
  would create untraceable rows and waste worker time, not a security
  hole, but cheap to close at the boundary); and
  `POST /api/v1/{vocabulary,models}/{id}/items/{position}/generate-transcription`,
  `.../generate-translation` for item-level generation.
- [ ] Every new endpoint performs the same `CanEdit(language)` check
  `apiGenerateFromText` already has, before any job is enqueued — flagged
  during design as the one gap that must not ship even temporarily; a
  request that fails this check must produce no job row at all.
- [ ] The Text/Dialog **View** page gains Generate Title/Transcribe/
  Translate buttons (gated on `canEdit`), each hidden when its target field
  already has content — these replace the old Transcribe/Translate buttons
  removed from the New/Edit forms, not duplicate them. **Resolved during
  B2** (the API only reports whether a translation exists for the viewer's
  own site locale, not per-locale across every site locale): Generate
  Translate has no locale select — it targets only the viewer's own site
  locale (`BOOT.locale`, the same locale the Translation tab already
  shows), hidden via the existing `hasTranslation` field. No new per-locale
  API surface needed.
- [ ] The Vocabulary/Models item form's Transcribe/Translate buttons are
  hidden in "add new item" mode (no saved position to write back to yet)
  and shown in edit mode, subject to the same blank-check visibility rule.
- [ ] A job payload created before this change
  (`{"text_id":N,"user_id":M}`, no `resource_type`) still decodes and
  retries correctly.
- [ ] The old synchronous path is deleted: `POST /llm/generate` and its
  handler, `phraseforgeGenerate`/`phraseforgeSetFieldValue`
  (`layout.html`), and `jobs.Service.EnqueueAndAwait` (confirmed to have
  exactly one caller, this handler). `jobs.PriorityInteractive` and the
  `jobs.priority` CHECK constraint are **not** removed — historical job
  rows carry that value and the Jobs page still renders it.
- [ ] `go test ./...` passes; new tests cover: each guarded setter's
  blank-check (applied vs. skipped) against a live DB; the payload
  back-compat decode; a 403 from every new endpoint for a non-editable
  language, asserted before any enqueue; an invalid locale returning 400.

## Approach

1. **Guarded store writes** (no behavior change yet — pure additions):
   `SetTitleIfBlank`/`SetTranscriptionIfBlank` on `texts.Store`/
   `dialogs.Store`; `SetIfAbsent` on `translations.Store`; extend the
   existing guarded item-setters on `vocabulary.Store`/`models.Store` with
   the same blank condition.
2. **Switch `ai.Service.writeback`** to the guarded setters; fold
   `applied`/skip-vs-fail into the job's result handling. This step is
   where the universal behavior change actually lands (affects backfill/
   ingest immediately) — validated first against the live DB before
   anything client-facing changes.
3. **New endpoints**: the six (text/dialog × title/transcription/
   translation) plus four (vocabulary/models item × transcription/
   translation) handlers, each reusing the existing
   `buildBackfillPayload`/`generatePayload` shape directly (no new payload
   type), each performing the `CanEdit` check before `s.jobs.Enqueue`.
   Routes registered alongside the existing generate-vocabulary/
   generate-models ones.
4. **UI cutover**: remove Transcribe/Translate from `texts-app.js`/
   `dialogs-app.js`'s New *and* Edit form bodies, and from
   `vocabulary-app.js`/`models-app.js`'s item form when adding a new item;
   add the new Generate buttons to the Text/Dialog View action row and to
   the Vocabulary/Models item form's edit mode, each gated on both
   `canEdit` and the relevant field being blank. The View page needs a
   `needsTranscription` flag (computed the same way
   `ime.NeedsTranscriptionForLanguage` already does for the IME config
   endpoint) to gate the Transcribe button's visibility, since the View
   page has no live language/script selects to derive it from client-side.
   New i18n keys (en/pl) for every new button/status message.
5. **Dead-code removal**, done last so every earlier step stays
   independently revertible: delete `llm.go`, its route, `layout.html`'s
   `phraseforgeGenerate`/`phraseforgeSetFieldValue`, and
   `jobs.Service.EnqueueAndAwait`.

**Testing strategy** (real-LLM-call budget: 1, held in reserve): every
acceptance criterion above except "does a real Ollama call actually reach
the guarded write" is provable without touching Ollama — the blank-check
guards are plain SQL predicates, testable directly against the live DB
with a fake/pre-set row; authorization is testable with `httptest` and a
non-editing user, asserting no job row is created; payload back-compat is
a pure JSON-decode test. The one real call, if spent: click Transcribe on
a saved text with a blank transcription, watch the job reach `done` on the
Jobs page, confirm the DB row updated — this single run validates the
guarded-write wiring, the endpoint, and the UI cutover together.

## Affected Areas

- `phraseforge/internal/texts/texts.go`, `dialogs/dialogs.go`,
  `translations/translations.go`, `vocabulary/vocabulary.go`,
  `models/models.go`
- `phraseforge/internal/ai/ai.go` (and its test file)
- `phraseforge/internal/server/generate.go` (and its new test file),
  `server.go`, `llm.go` (deleted), `dialogs.go`, `vocabulary.go`, `models.go`
- `phraseforge/internal/server/templates/layout.html`
- `phraseforge/internal/server/static/js/texts-app.js`, `dialogs-app.js`,
  `vocabulary-app.js`, `models-app.js`
- `phraseforge/internal/i18n/i18n.go`
- `phraseforge/internal/jobs/jobs.go` (`EnqueueAndAwait` removed)

## Out of Scope

- Any change to `jobs.PriorityInteractive` or the `jobs.priority` schema
  constraint — kept for historical rows.
- A "refresh the page when the job completes" convenience — the user must
  reload/reopen to see a newly-generated title/transcription/translation,
  matching how Generate Vocabulary/Models already behave.
- Anything covered by the other two specs from this same feature request
  (`dialog-vocabulary-models-generation`,
  `text-view-enlarged-script-fix`) — this spec assumes those may land in
  either order relative to this one; no hard dependency either way.

## Implementation Notes

1. **Guarded store writes**: added `SetTitleIfBlank`/`SetTranscriptionIfBlank`
   (`(applied bool, err error)`, a single guarded `UPDATE ... WHERE
   coalesce(trim(field),'')=''`) to `texts.Store`/`dialogs.Store`, replacing
   the old unconditional `SetTitle`/`SetTranscription` (their only caller
   was `ai.Service.writeback`, so this was a safe rename, not an addition
   alongside dead code). Added `SetIfAbsent` (`INSERT ... ON CONFLICT DO
   NOTHING`) to `translations.Store`, alongside the existing `Set` (still
   used by every interactive edit-form save path, untouched).
   `vocabulary.Store`/`models.Store`'s `SetItemTranscription`/
   `SetItemTranslationGuarded` were renamed to `SetItemTranscriptionIfBlank`/
   `SetItemTranslationIfAbsent`, extended with the same blank-check inside
   their existing transactional phrase-guard (one more `FOR UPDATE` read,
   same transaction).
2. **Switched `ai.Service.writeback`**: `TextWriteback`/`TranslationWriteback`/
   `ItemTranscriptionWriteback`/`ItemTranslationWriteback` all changed to
   return `(applied bool, err error)`; `writeback` itself now returns
   `(bool, error)`, and `HandleGenerate` records `applied` in a new
   `generateResult.Applied *bool` field (omitted for the interactive,
   no-writeback path) so the Jobs page's result view can distinguish "wrote
   it" from "skipped, already had a value". `main.go`'s
   `itemTranslationWriteback` adapter updated to match.
3. **New endpoints**: `apiGenerateTitleFromText`/`apiGenerateTranscriptionFromText`/
   `apiGenerateTranslationFromText` (and their Dialog mirrors), plus
   `apiGenerateVocabItemTranscription`/`apiGenerateVocabItemTranslation`
   (and their Models mirrors) — all in `generate.go`, reusing
   `buildBackfillPayload`/`generatePayload` (package `server`'s own copy,
   already used by export/import backfill) directly. Each performs the same
   `CanEdit`/404/403 check as `apiGenerateFromText` before `s.jobs.Enqueue`.
   Translation endpoints decode `{"locale": "..."}` and reject an invalid
   one with 400 *before* any store access (see `generate_test.go`). Item
   endpoints fetch the list, check `CanEdit`, then scan its items for the
   given position (`findVocabItemByPosition`, a small shared helper over a
   local `vocabItem` shape both `vocabulary.Item`/`models.Item` satisfy
   structurally) to get the item's current phrase as generation content.
4. **UI cutover**: removed the Transcribe/Translate buttons + translation-
   target `<select>` entirely from `texts-app.js`/`dialogs-app.js`'s shared
   New/Edit `showForm` (both modes render the same markup, so one removal
   point covers both) and from `vocabulary-app.js`/`models-app.js`'s item
   form in add mode. Added Generate Title/Transcription/Translation buttons
   to the Text/Dialog View page's action row (gated on `canEdit` +
   blank-check + `needsTranscription` for the transcription one — a new
   `apiTextDetail`/`apiDialogDetail` field, `NeedsTranscription`, populated
   via `ime.NeedsTranscriptionForLanguage` since the View page has no live
   language/script select to derive it client-side). **Resolved during B2**
   (see Acceptance Criteria): Generate Translation has no locale select —
   both the View page and the Vocabulary/Models item form's edit-mode
   Translate button target only the viewer's own site locale (`BOOT.locale`),
   matching the existing single-locale Translation tab/field. New
   `texts.generate_title`/`generate_transcription`/`generate_translation`
   (+`_started`) i18n keys, reused by `dialogsAppI18nKeys`/
   `vocabularyAppI18nKeys`/`modelsAppI18nKeys` as needed — each section's
   own curated key whitelist had to be updated too (a real bug found live
   mid-session on the *previous* feature's Dialog buttons: the label showed
   the raw key, e.g. `texts.generate_vocabulary`, because
   `dialogsAppI18nKeys` hadn't been updated — fixed then, and re-checked
   carefully for every new key added in this feature).
5. **Dead-code removal**: deleted `internal/server/llm.go` and its
   `/llm/generate` route; deleted `layout.html`'s `phraseforgeGenerate`/
   `phraseforgeSetFieldValue` (confirmed zero remaining JS callers first —
   only comments referencing the old name remained); deleted
   `jobs.Service.EnqueueAndAwait` (confirmed `handleLLMGenerate` was its
   only caller) and updated the doc comments that referenced it
   (`jobs` package doc comment, `Enqueue`'s own doc comment, `Delete`'s
   doc comment's now-obsolete "an EnqueueAndAwait caller may still be
   polling" race-window paragraph). `jobs.PriorityInteractive` and the
   `jobs_priority_check` CHECK constraint were left untouched, confirmed
   live still present after deploy (historical rows).
6. Self-reviewed the diff after each step; confirmed no orphaned CSS/JS
   (`.transcribe-action`'s stylesheet rule and `editor.js`'s null-guarded
   `#translation-target`/`#translateBtn` lookups are still needed by the
   Vocabulary/Models item form, which keeps its own Transcribe/Translate
   buttons — only Text/Dialog's New/Edit forms lost them).

## Validation

- `go build`/`vet`/`test ./... -count=1` pass after every step and again at
  the end; `node --check` on every edited JS file; the `pf-*` Web Component
  test suite (`npm test` under `internal/server/static/components`) still
  passes (31/31), unaffected.
- **New tests**:
  - `internal/generate` — n/a (this feature doesn't touch that package).
  - `internal/ai/ai_test.go` — extended every existing writeback test to
    assert `applied`, plus three new `TestWriteback*SkipsWhenAlreadyBlank`
    tests (title/item-transcription/item-translation) proving a skip is
    `(false, nil)` — job success, no write — not an error.
  - `internal/server/generate_test.go` (new) — `TestGenerateTranslationInvalidLocaleReturns400`:
    a bare `&Server{}` (no DB) hits all four locale-taking endpoints with an
    invalid locale and confirms 400, proving the validation genuinely
    happens before any store/DB access (matching the code's own structure —
    the invalid-locale branch returns before calling the shared
    `apiGenerateField*`/`apiGenerate*ItemField` body).
- **Guarded-write blank-check, live DB** (rolled-back transactions against
  the k3d cluster's Postgres): the exact guarded-`UPDATE`/`ON CONFLICT DO
  NOTHING` SQL each store method runs was exercised directly —
  `SetTitleIfBlank`'s predicate on a text with an existing title correctly
  updated 0 rows (title unchanged); `SetIfAbsent`'s insert against an
  existing `(text, 10, en)` translation correctly inserted 0 rows; the same
  insert against a not-yet-present locale (`pl`) correctly inserted 1 row.
  No vocabulary/models items exist yet in this lab DB, so the item-level
  guarded setters weren't exercised live the same way — covered instead by
  `ai_test.go`'s fake-based tests (which test the interface contract) and
  by direct code review of the transactional SQL (same `FOR UPDATE`/blank-
  check shape as the already-verified text-level setters).
- **Live deploy verification** (`task deploy-phraseforge`, twice — once
  after steps 1–3, once after steps 4–5): migration applied cleanly both
  times; `jobs_priority_check`/`PriorityInteractive` confirmed still present
  after the dead-code removal; `POST /llm/generate` now 404s; all 10 new
  endpoints return 302 (redirect to `/login`) unauthenticated, confirming
  they're registered and wrapped by `requireAuth`; the served
  `texts-app.js` contains the three new button ids and zero remaining
  `phraseforgeGenerate` references.
- **Authorization (403-before-enqueue)**: not exercised with a real
  non-editing user this session (would need provisioning a second test
  user/role) — verified instead by code inspection: every new handler
  performs the identical `fetch → CanEdit → 403-and-return-before-any-
  Enqueue` sequence already used (and unverified the same way) by the
  pre-existing `apiGenerateFromText`/`apiGenerateFromDialog`. Flagged to the
  user during B2; they chose to proceed without provisioning a live test
  user for this.
- No real Ollama call was spent — every check above is schema/auth/code-
  shape level, not a real LLM generation run. A visual click-through of the
  View page's new Generate buttons is still worth doing next time you're in
  the app.

## Documentation Review

Affected Areas map to the `phraseforge` artifact only — no fan-out.

**Changelog entries needed** (user-facing behavior change, two entries —
`Changed` for the universal blank-check rule and the New/Edit form cutover,
`Added` for the new View-page/item-form buttons):

- `{phraseforge/CHANGELOG.md, Added, "Generate Title/Transcription/Translation buttons on the Text/Dialog view page, and Transcribe/Translate on the Vocabulary/Models item edit form — background jobs, matching Generate Vocabulary/Models, replacing the old blocking Transcribe/Translate buttons on the New/Edit forms (removed)."}`
- `{phraseforge/CHANGELOG.md, Changed, "a completed Title/Transcription/Translation generation job now only writes its result if the target field is still blank — this also applies to the existing import backfill and ingest paths, which previously always overwrote; a Generate button is hidden once its target field already has content."}`

**Other drift:** none found. No constitution file makes a claim this
change contradicts.

## Documentation Updates

- `phraseforge/CHANGELOG.md`: added the two entries above.
- Removed this feature's `## Now` line from
  `specs/artifacts/phraseforge/roadmap.md` — it's in the changelog now.
