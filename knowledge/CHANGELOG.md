# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added

- chat messages (both user and assistant) now render as sanitized
  Markdown HTML — headers, bold/italic, code (inline and fenced), lists
  (unordered and ordered, including the chat retrieval reference list),
  tables, links, and images — via a vendored markdown-it + DOMPurify
  pipeline, instead of escaped text with only basic link support.
- "Generate" buttons in the knowledge item editor fill Title and
  Summary from the current Body via the LLM (`generate.model`), editable
  before saving.
- a translation capability (`translate.model`, defaulting to
  `gemma4:12b`) and a configured knowledge-base language
  (`knowledgeLanguage`), used by the ingest pipeline to translate chunks.
- chat/generate/translate prompt templates are now configurable
  via the mounted config file (`knowledge/k8s/configmap.yaml`'s
  `prompts:` block — `chat`, `generateTitle`, `generateSummary`,
  `translate`) instead of being hardcoded in Go, so they are editable
  without a rebuild.
- a footer status area, visible on every tab, showing what the
  app is currently doing during a long-running background job
  (`GET /api/v1/jobs/current`) and every action's success/error feedback in
  one consistent place — used by the ingest pipeline to report
  fetch/chunk/translate/generate progress.
- an ingest pipeline — `POST /api/v1/ingest/url` fetches a URL
  (stripping HTML) or `POST /api/v1/ingest/text` accepts an uploaded
  `.txt`/`.md`/`.markdown` file, chunks the content, translates and generates
  a title/summary per chunk, and stores each chunk as a draft
  (`GET /api/v1/ingest/drafts`) pending human review — never written directly
  to the searchable knowledge base.
- an "Ingest" tab to review, edit, approve, or discard draft
  chunks produced by the ingest pipeline — approving promotes a draft into a
  real, searchable knowledge item (the same creation path as manually adding
  one); discarding deletes it permanently.
- job history (`GET /api/v1/jobs`, all jobs regardless of
  status, not just the currently-running one) plus retry
  (`POST /api/v1/jobs/{id}/retry`, re-launches a failed job from its stored
  source) and delete (`DELETE /api/v1/jobs/{id}`) — a failed job's error no
  longer disappears once it stops running, visible in the Ingest tab's Jobs
  list.
- a single app-wide background operation queue — chat replies,
  manual title/summary generation, and ingest's per-chunk pipeline all run
  through one worker, one operation at a time, with interactive work
  (chat, manual generate) always jumping ahead of background work (ingest).
  Sending a chat message now returns immediately; the reply appears
  asynchronously once ready. Importing data now only requires `body`
  (title/summary/tags are all optional); a missing title/summary is
  filled in later by a queued background generate operation.
- `GET /api/v1/knowledge` and the search endpoint now accept
  `offset`/`limit` and return `has_more`, with Prev/Next controls in the
  Knowledge tab, so the list scales past a single page.
- the Jobs list shows a colored status badge
  (pending/active/done/failed) and the job's current step text, instead of
  only a bare `status` string.
- structured (`log/slog`, JSON to stdout) logging for every LLM
  call, ingest job start/finish, data import, and knowledge item
  create/update/delete — durations and outcomes only, never content.

### Changed

- the Knowledge tab's aside now visually separates the search
  block from the export/import block with a divider, instead of both
  reading as one undifferentiated stack of inputs.
- the footer status/message area, delete/discard/approve confirmations,
  item/draft/chat list cards, and form field labels are now built from four new
  `kb-*` Web Components (`kb-status-bar`, `kb-dialog`, `kb-card`, `kb-field`) rather
  than native browser dialogs and ad-hoc markup — fixing a footer-height jump bug in
  the process and replacing native `confirm()` popups with an in-app modal.
- every remaining button in the knowledge app, plus the login and signup
  pages, now use the same `kb-button`/`kb-field` components as the rest of the app;
  `index.html`'s and login/signup's inline CSS/JS were extracted into shared files
  (`theme.css`, `app.css`, `app.js`, `auth.css`, `auth.js`).
- the Knowledge and Ingest tabs now use a list/detail workspace layout. The
  sidebar holds only search/filter controls (plus "New" for Knowledge); the main
  workspace shows the item/draft list as a responsive card grid by default, and
  clicking an item switches to a detail view (with a "← Back to list" control) instead
  of always showing a single cramped sidebar-plus-form layout.
- header navigation (Knowledge/Chat/Ingest) and its theme-toggle/log-out
  controls are now built from reusable `kb-button` and `kb-nav` Web Components with
  real visual styling (color variants, hover states, active-item highlights),
  replacing unstyled plain `<button>` elements — establishes the first use of the
  new cross-app Web Components convention (see `specs/tech-stack.md`'s Frontend
  components entry).
- the "Generate" title/summary buttons now show a "Generating…"
  message while their request is in flight, instead of no feedback at all.
- the Ingest tab now separates Jobs and Drafts into sub-workspaces
  with a pill-based toggle, each showing a responsive card grid with live item
  counts. Jobs and chat messages now render via new `kb-job-card` and `kb-message`
  Web Components, replacing string-built HTML.
- configuration (model names, endpoints, thresholds — everything
  except credentials) now comes from a mounted ConfigMap YAML file
  (`knowledge/k8s/configmap.yaml`) instead of plain deployment env vars.
  `PGUSER`/`PGPASSWORD`/`*_API_KEY` are unaffected, still env-var/Secret
  sourced. Pure mechanism change — no setting's value changed.
- default chat model changed to `gemma4:12b`, with Ollama's
  `thinking` mode always disabled — the same request that took 148s and
  answered incorrectly now takes ~14s and answers correctly.
- chat now replays recent conversation history and each
  retrieved document's summary to the model, and folds the previous
  question into retrieval for short follow-ups (e.g. "answer my last
  question") — previously each message was answered with no memory of
  prior turns.
- bulk export/import now uses pretty-printed YAML instead of
  JSON. Import gains an explicit `delete: true` marker per row (hard
  deletes by id, idempotent), ignores a row's `created_at`/`updated_at` as
  purely informational, and skips re-embedding an existing item whose
  title/summary/body/tags are unchanged from what's already stored —
  re-embedding is real LLM/embedding cost, only paid when content actually
  changed. `ImportResult` gains `deleted`/`unchanged` counts alongside
  `imported`.

### Fixed

- retrying a URL-sourced ingest job now reuses the content
  already fetched on the failed attempt instead of re-fetching the URL,
  matching how text/file-upload retries already behaved.
- chat no longer fails with a 400 (`... does not support tools`) on
  models without Ollama tool-calling support, such as `llama3-chatqa:8b`.
  Retrieval-augmented answers are now built entirely in code — the app
  fetches each retrieved document's full body and includes it directly in
  the prompt — instead of relying on the chat model to call a tool.
- search (general search and chat retrieval alike) now drops
  results below `qdrant.searchMinScore` (default `0.4`) instead of always
  returning the nearest neighbors regardless of match quality — previously
  this let genuinely irrelevant results reach the chat model, which then
  hallucinated a confident, fabricated answer instead of saying it didn't
  know.
- URL ingestion now sends a descriptive `User-Agent` header
  instead of Go's default (`Go-http-client/1.1`) — many real-world sites
  (e.g. Wikipedia) reject the default outright with `403`, which made
  ingesting a real page fail every time.
- job error text now renders as literal text (not HTML) and chat
  message source links render as real `<a>` elements (not unescaped onclick
  attributes), eliminating HTML-escaping vulnerabilities in those fields.
