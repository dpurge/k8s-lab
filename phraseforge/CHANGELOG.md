# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added

- Admin > Languages tab: each language's grammar-tag snippet and transcription snippet, one row per snippet. A snippet is defined once and used by every prompt that contains `{{grammarPrompt}}` / `{{transcriptionPrompt}}`; the transcription, generate vocabulary and generate models prompts now contain them, besides the item prompts.
- Admin > LLM: a new prompt starts from the default prompt of its kind instead of an empty box, with a list of the placeholders that kind accepts.
- English (`eng`), Finnish (`fin`), Japanese (`jpn`), Korean (`kor`), Swahili (`swa`), Swedish (`swe`) and Tagalog (`tgl`) are in the language list, so texts in them can be created (Polish and the other languages were already there). Existing databases get the rows when the schema is applied again (the migrate job).
- The size limit for ingested content is configurable (`ingest.maxContentBytes`; the default stays 24 KiB, and
  the prod ConfigMap sets 200 KiB). Long texts and dialogs are cleaned in chunks that fit each purpose's
  context window, one LLM call per chunk, and long-text translation and transcription are chunked the same
  way; a title is derived from the first chunk. A long ingest occupies the single job worker until it
  finishes.
- one structured LLM call per vocabulary/models item per site locale (job kinds
  `generate_vocabulary_item`/`generate_models_item`): new `vocabularyItem`/`modelsItem` purposes whose
  prompt is a template with `{{sourceLanguage}}`, `{{targetLanguage}}`, `{{phrase}}`,
  `{{grammarPrompt}}`, `{{transcriptionPrompt}}` (the prompt-eval setup's placeholders); the reply is
  JSON constrained by a schema and validated before anything is stored, and fills translation, notes,
  grammar, and transcription — each only where still blank.
- Admin > LLM > Language sections: per-language grammar-tag and transcription-system text for the item
  prompts, included in the admin configuration export/import. A language that needs transcription but has
  no section uses a built-in default ("Transcribe using the standard romanization for this language.").
- Cancel action on the Jobs page for pending/running jobs — interrupts an in-flight LLM call rather than only being able to wait it out or restart the pod; Retry and Delete now also work on a cancelled job.
- Generate Vocabulary/Generate Models buttons on the Texts view page — submits a background job that extracts vocabulary/grammar items from the text and creates or updates one dedicated linked list.
- Generate Vocabulary/Generate Models buttons on the Dialogs view page, matching Texts; the Text/Dialog view page now shows a right-aligned row of links to any linked Vocabulary/Models list next to the Source/Transcription/Translation tabs.
- Generate Title/Transcription/Translation buttons on the Text/Dialog view page, and Transcribe/Translate on the Vocabulary/Models item edit form — background jobs, matching Generate Vocabulary/Models, replacing the old blocking Transcribe/Translate buttons on the New/Edit forms (removed).
- Jobs admin page for viewing and managing the background operation queue: list all jobs (interactive and background alike), view detailed payload/result/step information, retry failed jobs, and delete done/failed ones with state-aware actions.
- Admin LLM prompt editor now supports provider selection (Ollama or OpenRouter) and per-prompt thinking toggle (off by default), allowing fine-grained LLM behavior control alongside existing model and prompt-text fields.
- Ingest for Texts and Dialogs — create content by pasting text, uploading a file, or providing a URL; only Body, Language, and Script are required, with title, cleaned content, transcription, and translation generated automatically in the background.
- Admin LLM prompt editor now supports three additional prompt purposes (`title`, `process_text`, `process_dialog`) used during ingest, each independently configurable per language.
- Export/Import (YAML) for Texts, Dialogs, Vocabulary, and Models — bulk create/update/delete via a YAML file, with missing title/transcription/translation fields backfilled automatically in the background.
- Generate Vocabulary/Generate Models now automatically submits transcription (where needed) and translation (for every site language) background jobs for each newly created/replaced item — no separate manual step needed after generating from a text or dialog.
- Generate Missing Translations button on the Vocabulary view page — submits one background translation job per item that doesn't yet have a translation in your own site language.
- Clear button on the Jobs page, next to Refresh — bulk-removes every successfully completed job; pending, cancelled, and failed jobs are always kept.
- Previous/Next pagination on the Texts/Dialogs/Vocabulary/Models list pages (25 items per page, most recent first) — scales to a language holding thousands of items instead of fetching and rendering every row at once.

### Changed

- Language sections moved from the Admin > LLM tab to the new Languages tab, shown one snippet per row instead of two columns in one table; the stored data and the configuration export/import format are unchanged.
- Admin > LLM prompt kinds are named after the default prompt files: "Generate translation", "Generate transcription", "Generate title", "Process text", "Process dialog", "Generate vocabulary", "Generate models", "Generate vocabulary item", "Generate models item".
- Default prompts are the files of prompt-eval's `prompt/default/system/`, embedded in the binary (`npm run sync-prompts` / `check-sync` in prompt-eval keep the copy equal); the default title and translation prompts are now the production-tuned text prompt-eval tests, which differs from the old one only in line breaks. A deployed prompt (`config.yaml` or an admin row) must contain a snippet placeholder to use the snippet.
- The default prompts for transcription, text and dialog cleaning, vocabulary and models extraction, and the per-item vocabulary and models translations are rewritten, each with two worked examples: transcription follows the scholarly transliteration of the language, with every diacritic, sentence capitals and Latin punctuation; cleaning keeps only the text itself (no navigation, no introduction before it, no questions or links after it); vocabulary uses dictionary forms and the canonical part-of-speech tags (`Adj`, `Adv`, ...); translations read like a dictionary (lowercase, senses joined by `; `, never `/`, no parentheses); notes are rare and written in the target language; models are few, key sentence patterns ordered from simple to complex. A deployment that sets its own prompts in its config file keeps them (the Helm values are updated alongside).
- vocabulary/models item generation (per-item Generate buttons, Generate missing translations, import
  backfill, and the follow-ups after generating from a text) now queues the single structured item call
  instead of separate transcription and translation jobs; Generate missing translations also fills blank
  grammar/transcription where the language has a section.
- LLM calls to Ollama now stream and fail on lack of progress instead of on one fixed timeout:
  `providers.ollama.firstTokenTimeoutSeconds` (default 300 — model load + prompt processing) and
  `providers.ollama.idleTimeoutSeconds` (default 60 — longest allowed gap in output). Each
  purpose's `timeoutSeconds` is now an overall backstop, default 1800 for every purpose (was
  120/300/600), so slow-but-healthy CPU-only translations (~300s) no longer fail at 120s. The
  `llm call` log line reports which limit fired (`limit=first_token|idle|overall`).
- importing a Text/Dialog/Vocabulary list/Models list whose `id` no longer exists (e.g. deleted
  since the export) now creates it as new, under a new id, instead of failing with "not found".
- exporting Texts/Dialogs/Vocabulary/Models now requires both language and script (previously
  language alone, optional); an optional tags filter (comma-separated, matches items carrying
  every given tag) narrows further.
- the Jobs page's View action now shows a resizable, structured dialog (metadata fields plus
  separate Payload/Result blocks) instead of one raw JSON dump, and Retry now asks for
  confirmation first, matching Delete.
- Default LLM models for transcription and translation updated to `gemma4:12b` (from `rinex20/translategemma3:12b`), reducing memory footprint on resource-constrained nodes.
- a completed Title/Transcription/Translation generation job now only writes its result if the target field is still blank — this also applies to the existing import backfill and ingest paths, which previously always overwrote; a Generate button is hidden once its target field already has content.
- LLM call timeouts are now configurable per purpose (config.yaml) and per admin-configured prompt override (Admin > LLM), instead of one fixed 2-minute limit app-wide — the previous fixed limit was too short for Generate Vocabulary/Models' longer prompts.
- background LLM generation jobs (Title/Transcription/Translation) now use distinct Jobs-page kinds (`generate_title`/`generate_transcription`/`generate_translation`) instead of one generic `llm_generate` bucket, for better traceability; historical `llm_generate` rows are unaffected and still work.
- the header's sidebar-toggle/theme-toggle buttons and the sidebar navigation now render via new
  `pf-button`/`pf-nav` Web Components (matching knowledge's own `kb-*` convention), replacing
  inline markup — no visual or behavioral change.
- editor/form pages (texts, dialogs, vocabulary, models, profile, login, signup) now render their
  labeled fields and buttons via new `pf-field`/`pf-button` Web Components, replacing inline
  markup — no visual or behavioral change.
- the four list pages (Texts, Dialogs, Vocabulary, Models) now render their card-grid items via a
  new `pf-card` Web Component, replacing inline markup — no visual or behavioral change.
- delete/revoke/remove confirmations across the app (admin, texts, dialogs, vocabulary, models,
  and vocabulary/models item rows) now use an in-app `pf-dialog` instead of the browser's native
  confirm() popup; admin.html's remaining fields and buttons now use `pf-field`/`pf-button` —
  completing the pf-* componentization of phraseforge's whole UI.
- Texts is now a single-page client-rendered app (list/new/view/edit, all via a new `/api/v1/texts`
  JSON API) instead of four separate server-rendered pages, with a new `pf-status-bar` footer for
  save/delete feedback; the old `/texts/*` template routes are gone.
- Dialogs is now a single-page client-rendered app (list/new/view/edit, all via a new
  `/api/v1/dialogs` JSON API) instead of four separate server-rendered pages, matching Texts;
  the old `/dialogs/*` template routes are gone.
- Vocabulary is now a single-page client-rendered app (list/new/view/manage, all via a new
  `/api/v1/vocabulary` JSON API, including per-item add/edit/delete) instead of four separate
  server-rendered pages, matching Texts/Dialogs; the old `/vocabulary/*` template routes are gone.
- Models is now a single-page client-rendered app (list/new/view/manage, all via a new
  `/api/v1/models` JSON API, including per-item add/edit/delete) instead of four separate
  server-rendered pages, matching Texts/Dialogs/Vocabulary; the old `/models/*` template routes
  are gone.
- The admin panel is now a single-page client-rendered app, split into four tabs (Permissions,
  IME, LLM, Config) via a new `pf-tabs` component, instead of one long scroll of stacked cards,
  via a new `/api/v1/admin` JSON API; the old `/admin/*` mutation routes are gone (config
  export/import stay as real endpoints, not fetch-based).
- Profile (roles, site language, password) is now a single-page client-rendered app, all via a
  new `/api/v1/profile` JSON API, instead of a server-rendered page with full-page POST-redirect
  round trips; changing site language still triggers a real page reload (needed to refresh the
  sidebar/header text), but the password form and roles list no longer do.
- Texts, Dialogs, Vocabulary, Models, Admin, and Profile are now one unified single-page app
  (one URL, in-page section switching via a new sidebar built from `pf-tabs`) instead of six
  separate shells each with their own URL; the sidebar's language filter and a Profile locale
  change are both now pure in-page state with no page reload, and the old `pf-nav` component is
  removed (replaced by `pf-tabs`).

### Fixed

- Generating vocabulary or models no longer returns an empty list when the model writes lines in the wrong
  format (for example the grammar tag in square brackets, seen with Arabic): rejected lines are sent back to
  the model, and only those lines, for correction (up to `maxAttempts` rounds); lines still malformed are
  dropped and counted as `skipped_lines` in the job result, and a job with no valid line at all now fails with
  the first rejected lines instead of finishing as done. Long texts are generated in chunks sized to fit the
  context window, with items repeated across chunks dropped.
- An LLM call that fails with a transient error (HTTP 5xx, a connection error, a stream that ends early, or
  an idle stall) is now retried within the same `maxAttempts` budget (default 3), waiting 5s and then 15s;
  first-token and overall timeouts are not retried.
- An item job whose reply fails validation (invalid JSON, a missing field, or a phrase that differs from the
  item's) now sends the reply and the exact error back to the model for correction, up to `maxAttempts`
  attempts per call (default 3), instead of failing at once.
- Admin > LLM showed raw keys instead of labels for the Generate vocabulary / Generate models kinds, in
  both English and Polish.
- Text/Dialog/Vocabulary/Models detail views show their action buttons in a wrapping row above
  the title, so several buttons no longer squeeze the title into a narrow column.
- Polish labels for Ingest and YAML Import were the same verb ("Importuj"/"Zaimportuj"); Ingest is
  now "Dodaj z pliku/URL" (dialog titles "Dodaj tekst"/"Dodaj dialog"), and Import/Export are
  "Importuj YAML"/"Eksportuj YAML".
- the Text/Dialog view page's enlarged-script display (for Han/Arabic/Hebrew/Syriac/Japanese/Korean
  scripts) now actually takes effect on the Source tab — a CSS specificity bug silently defeated it
  even though the class was already correctly applied; Transcription/Translation are unaffected and
  stay normal size, as intended.
- Ingest's "Upload file"/"Fetch URL" fields, and the new Admin > LLM Timeout field, now render with
  the same styling as every other form field — input[type=url]/file/number were silently missing
  from the shared form-input CSS rule.
- the migrate command (and serve) now read PGHost/PGPort/PGDatabase from environment variables
  (PGHOST/PGPORT/PGDATABASE) instead of the mounted config file, so a chart's
  pre-install/pre-upgrade migrate hook no longer needs the app's ConfigMap to exist first.
- the sidebar and main content area now scroll independently (sidebar stays in place while a
  long page scrolls), instead of scrolling together as one page.
- the sidebar's language filter now actually filters the Texts and Dialogs lists again (it was
  silently ignored after their move to a client-rendered SPA).
- the admin panel's IME/LLM tabs now consistently say "Delete" (previously "Remove" on IME,
  "Delete" on LLM, "Revoke" on Permissions — now "Delete" everywhere) and support editing an
  existing IME config or LLM prompt in place, instead of only add-and-delete; retyping a long LLM
  prompt to change one field is no longer required.
- the Generate (transcribe) button now actually appears for languages/scripts configured to need
  transcription, on both the Texts/Dialogs editor and Vocabulary/Models' item form — a
  `.transcribe-action { display: none; }` stylesheet rule was silently defeating the JS that tried
  to reveal it, so it never showed for anyone, on any of the four resource types, until now.
- the Texts/Dialogs editor's Transcribe/Translate buttons and the translation target-language
  select are now scoped to their own tab (Transcribe only shows on the Transcription tab,
  Translate and the language select only on the Translation tab — Save still shows on every tab),
  instead of all three always appearing together regardless of which tab was open.
- the "Transcribe" and "Translate" buttons (Texts/Dialogs/Vocabulary/Models editors) are both now
  labeled "Generate" — which action it performs is already implied by the active tab (or, for
  Vocabulary/Models' tabless item form, by which field it fills in), so a single consistent label
  replaces the two different ones.
