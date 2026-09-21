# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- knowledge: "Generate" buttons in the knowledge item editor fill Title and
  Summary from the current Body via the LLM (`generate.model`), editable
  before saving.
- knowledge: a translation capability (`translate.model`, defaulting to
  `rinex20/translategemma3:12b`) and a configured knowledge-base language
  (`knowledgeLanguage`), used by the ingest pipeline to translate chunks.
- knowledge: a footer status area, visible on every tab, showing what the
  app is currently doing during a long-running background job
  (`GET /api/v1/jobs/current`) and every action's success/error feedback in
  one consistent place — used by the ingest pipeline to report
  fetch/chunk/translate/generate progress.
- knowledge: an ingest pipeline — `POST /api/v1/ingest/url` fetches a URL
  (stripping HTML) or `POST /api/v1/ingest/text` accepts an uploaded
  `.txt`/`.md`/`.markdown` file, chunks the content, translates and generates
  a title/summary per chunk, and stores each chunk as a draft
  (`GET /api/v1/ingest/drafts`) pending human review — never written directly
  to the searchable knowledge base.
- knowledge: an "Ingest" tab to review, edit, approve, or discard draft
  chunks produced by the ingest pipeline — approving promotes a draft into a
  real, searchable knowledge item (the same creation path as manually adding
  one); discarding deletes it permanently.
- knowledge: job history (`GET /api/v1/jobs`, all jobs regardless of
  status, not just the currently-running one) plus retry
  (`POST /api/v1/jobs/{id}/retry`, re-launches a failed job from its stored
  source) and delete (`DELETE /api/v1/jobs/{id}`) — a failed job's error no
  longer disappears once it stops running, visible in the Ingest tab's Jobs
  list.

### Changed

- knowledge: the "Generate" title/summary buttons now show a "Generating…"
  message while their request is in flight, instead of no feedback at all.

- knowledge: configuration (model names, endpoints, thresholds — everything
  except credentials) now comes from a mounted ConfigMap YAML file
  (`knowledge/k8s/configmap.yaml`) instead of plain deployment env vars.
  `PGUSER`/`PGPASSWORD`/`*_API_KEY` are unaffected, still env-var/Secret
  sourced. Pure mechanism change — no setting's value changed.
- knowledge: default chat model changed to `gemma4:12b`, with Ollama's
  `thinking` mode always disabled — the same request that took 148s and
  answered incorrectly now takes ~14s and answers correctly.
- knowledge: chat now replays recent conversation history and each
  retrieved document's summary to the model, and folds the previous
  question into retrieval for short follow-ups (e.g. "answer my last
  question") — previously each message was answered with no memory of
  prior turns.
- knowledge: bulk export/import now uses pretty-printed YAML instead of
  JSON. Import gains an explicit `delete: true` marker per row (hard
  deletes by id, idempotent), ignores a row's `created_at`/`updated_at` as
  purely informational, and skips re-embedding an existing item whose
  title/summary/body/tags are unchanged from what's already stored —
  re-embedding is real LLM/embedding cost, only paid when content actually
  changed. `ImportResult` gains `deleted`/`unchanged` counts alongside
  `imported`.

### Fixed

- knowledge: chat no longer fails with a 400 (`... does not support tools`) on
  models without Ollama tool-calling support, such as `llama3-chatqa:8b`.
  Retrieval-augmented answers are now built entirely in code — the app
  fetches each retrieved document's full body and includes it directly in
  the prompt — instead of relying on the chat model to call a tool.
- knowledge: search (general search and chat retrieval alike) now drops
  results below `qdrant.searchMinScore` (default `0.4`) instead of always
  returning the nearest neighbors regardless of match quality — previously
  this let genuinely irrelevant results reach the chat model, which then
  hallucinated a confident, fabricated answer instead of saying it didn't
  know.
- knowledge: URL ingestion now sends a descriptive `User-Agent` header
  instead of Go's default (`Go-http-client/1.1`) — many real-world sites
  (e.g. Wikipedia) reject the default outright with `403`, which made
  ingesting a real page fail every time.
