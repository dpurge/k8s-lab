# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added

- Generate Vocabulary/Generate Models buttons on the Texts view page — submits a background job that extracts vocabulary/grammar items from the text and creates or updates one dedicated linked list.
- Jobs admin page for viewing and managing the background operation queue: list all jobs (interactive and background alike), view detailed payload/result/step information, retry failed jobs, and delete done/failed ones with state-aware actions.
- Admin LLM prompt editor now supports provider selection (Ollama or OpenRouter) and per-prompt thinking toggle (off by default), allowing fine-grained LLM behavior control alongside existing model and prompt-text fields.
- Ingest for Texts and Dialogs — create content by pasting text, uploading a file, or providing a URL; only Body, Language, and Script are required, with title, cleaned content, transcription, and translation generated automatically in the background.
- Admin LLM prompt editor now supports three additional prompt purposes (`title`, `process_text`, `process_dialog`) used during ingest, each independently configurable per language.
- Export/Import (YAML) for Texts, Dialogs, Vocabulary, and Models — bulk create/update/delete via a YAML file, with missing title/transcription/translation fields backfilled automatically in the background.

### Changed

- Default LLM models for transcription and translation updated to `gemma4:12b` (from `rinex20/translategemma3:12b`), reducing memory footprint on resource-constrained nodes.
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
