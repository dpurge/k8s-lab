---
title: Generate Vocabulary/Models from a Text
kind: feature
status: done
version: 1
updated: 2026-09-25
branch: main
---

## Problem / Motivation

Add "Generate Vocabulary" and "Generate Models" buttons on the Texts view page (Dialogs are out of scope — the roadmap item is explicitly scoped to "the text view page"). Each submits a background job that extracts vocabulary/grammar-model items from the text's body via LLM and upserts them into one dedicated list linked to that text via a new `source_text_id` column — created on first run, wholesale-replaced (reusing the exact transactional wholesale-replace pattern just built and fixed in `phraseforge-export-import`) on rerun.

This introduces two new LLM purposes (`generate_vocabulary`, `generate_models`) following the exact same one-kind-per-purpose, per-language-admin-configurable pattern established by every prior LLM-purpose feature this session — new config.yaml blocks, `llm_prompts` CHECK constraint expansion, built-in default prompts. Both purposes ask the LLM to respond in the EXACT same line-based markdown format this app already uses for vocabulary/models blocks (documented in `phraseforge/internal/server/markdown.go`'s existing generation code): `phrase {grammar} [transcription] = translation` for vocabulary, `phrase [transcription] = translation` for models — reusing an already-established, already-documented format instead of inventing a new one, and making the parser side straightforward (split lines, one regex per format).

Per explicit user direction: this feature prioritizes shipping something that works well enough for interactive manual testing over exhaustive automated validation — the structured-LLM-output parsing is inherently something only a live call can fully prove, and the user will test that live themselves. Validation for this feature is build/test/structural only, no live LLM call budget spent proving the parser end-to-end (unlike every prior feature this session, which included at least one minimal live pass).

## Acceptance Criteria

- Two new LLM purposes (`generate_vocabulary`, `generate_models`) added to `phraseforge/internal/config`'s provider-registry shape, each `{provider, model, numCtx, think}`, defaulting to `ollama`/`gemma4:12b`/`numCtx: 8192`/`think: false` (matching the other content-generation purposes' larger context window, since a full text body is the input).
- `llm_prompts.kind` CHECK constraint expanded to also allow `generate_vocabulary`/`generate_models`, each independently admin-overridable per (kind, source_language, target_language) via the existing admin LLM-prompt UI (reuse `ai.ValidKinds`, don't hand-maintain a second list — this was exactly the blocking bug fixed in the ingest feature, don't reintroduce it).
- `ai.Service.Generate` supports both new kinds via the existing per-kind lookup, each with a built-in default prompt instructing the LLM to extract vocabulary/grammar items from the given text and respond ONLY in the exact line format above, one item per line, no commentary/preamble/code fences.
- `vocabulary_lists` and `models_lists` each gain a nullable `source_text_id bigint REFERENCES texts(id) ON DELETE SET NULL` column (additive `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`).
- New job kinds `generate_vocab_from_text`/`generate_models_from_text` (new package `phraseforge/internal/generate`, mirroring `phraseforge/internal/ingest`'s structure): handler reads the text by id, calls `ai.Service.Generate` with the corresponding kind and the text's body as content, parses the line-based response into items (a phrase-required, grammar/transcription-optional line for vocabulary; phrase-required, transcription-optional for models — malformed/unparseable lines are skipped, not fatal, since a partial extraction is better than a failed job over one bad line), then: if a list with this text's `source_text_id` already exists, wholesale-replace its items (reusing the transactional replace method just added for `phraseforge-export-import`, not a fresh reimplementation); otherwise create a new list (title derived from the text's own title, e.g. `"Vocabulary: <text title>"` / `"Models: <text title>"`, language/script copied from the text, `source_text_id` set).
- New `POST /api/v1/texts/{id}/generate-vocabulary` and `POST /api/v1/texts/{id}/generate-models` endpoints: same `CanEdit(text's language)` authorization as every other text-scoped action; enqueues the corresponding background job (`jobs.PriorityBackground`); responds `202` with `{"job_id": "..."}`. No new per-item transcription/translation backfill is triggered by this feature — a future feature can add that to the generated vocabulary/models items if wanted, out of scope here.
- New "Generate Vocabulary" / "Generate Models" buttons on the Texts view page (not Dialogs, not the list view), same visibility gating (`CanEdit` the text's language) as other action buttons already there; clicking one POSTs to the corresponding endpoint and shows an "started" acknowledgment via the status bar — no live progress tracking (matches this app's established Jobs-page-is-admin-only convention).
- `go build ./...`, `go vet ./...`, `go test ./...` pass, including new unit tests for the line-format parser (pure function, table-driven, well-formed lines / malformed lines / mixed / empty input — no DB or LLM needed) and the "which list to target" decision logic (existing `source_text_id` found → reuse that list's id; not found → create new). No live LLM call is required to consider this feature done, per explicit user direction — the user will test the actual generation quality interactively themselves.

## Approach

1. **`phraseforge/internal/config/config.go`**: add `GenerateVocabulary`, `GenerateModels` `PurposeConfig` fields, defaults matching the other content-generation purposes.
2. **`phraseforge/internal/db/schema.sql`**: expand `llm_prompts.kind` CHECK (reuse the existing drop/recreate-constraint idiom already used twice this session); add `source_text_id` to `vocabulary_lists`/`models_lists`.
3. **`phraseforge/internal/ai/ai.go`**: add the two new kinds to the per-kind lookup and `ai.ValidKinds`; add their built-in default prompts.
4. **New `phraseforge/internal/generate` package**: `HandleGenerateVocabFromText`/`HandleGenerateModelsFromText` job handlers, a pure line-parser function per format (vocabulary: `phrase {grammar} [transcription] = translation`, grammar/transcription/translation all optional; models: `phrase [transcription] = translation`, transcription/translation optional), and the create-or-reuse-by-`source_text_id` logic, reusing `phraseforge-export-import`'s transactional wholesale-item-replace method (do not reimplement it — call the shared method).
5. **`phraseforge/internal/server`**: new handlers for the two POST endpoints, route registration in the existing `requireAuth` group.
6. **`phraseforge/main.go`**: register the two new job kinds against the new package's handlers.
7. **Frontend**: two new buttons on the Texts view page (`texts-app.js`), matching the existing action-button pattern there.
8. **i18n**: new button labels, en + pl.

## Affected Areas

- `phraseforge/internal/config/config.go`
- `phraseforge/internal/db/schema.sql`
- `phraseforge/internal/ai/ai.go`
- New `phraseforge/internal/generate/` package
- `phraseforge/internal/server/` (new handlers, route registration)
- `phraseforge/internal/vocabulary/vocabulary.go`, `phraseforge/internal/models/models.go` (if the transactional wholesale-replace method needs a small signature adjustment to be called from outside the export-import files — check when implementing)
- `phraseforge/main.go`
- `phraseforge/internal/server/static/js/texts-app.js`
- `phraseforge/internal/i18n/i18n.go`

## Out of Scope

- Dialogs — this roadmap item is Texts-only, per its own explicit wording.
- Per-item transcription/translation backfill on the generated vocabulary/models items — future enhancement, not this feature.
- Live LLM-call validation of the actual generation/parsing quality — explicitly deferred to the user's own interactive testing, per direct instruction.
- Any UI for viewing/managing the linked vocabulary/models list beyond what the existing Vocabulary/Models list/view pages already provide (the generated list is just a normal list, visible there like any other).

## Implementation Notes

All 8 Approach steps implemented in one pass. New LLM purposes `generate_vocabulary`/`generate_models` added (config, schema CHECK constraint, `ai.ValidKinds`, built-in prompts matching the existing markdown block format). `source_text_id` added to `vocabulary_lists`/`models_lists`. New `phraseforge/internal/generate` package with job handlers and a pure line-parser (vocabulary: `phrase {grammar} [transcription] = translation`; models: `phrase [transcription] = translation`; malformed lines skipped, not fatal); reuses `vocabulary.Store`/`models.Store`'s transactional `Begin`/`*Tx` primitives (added in the export-import fix-forward round) for wholesale item replacement on rerun, matching this app's established duplication-over-shared-abstraction convention rather than importing an unexported helper from another package. Two new HTTP endpoints (`POST /api/v1/texts/{id}/generate-vocabulary`/`generate-models`), two new buttons on the Texts view page. Translation text from the LLM's output line is intentionally not captured (vocabulary/models items have no translation column — that's per-locale, in a separate table, and out of scope here).

## Validation

`go build`/`go vet`/`go test` clean, including new unit tests for the line-parser and the target-list decision logic (pure functions, no DB/LLM). A structural deploy check confirmed: migration applied cleanly (both new columns present, CHECK constraint includes both new kinds), pod starts cleanly, both new routes are registered and correctly authenticated. Per explicit user direction, no live LLM call was made to validate the actual generation/parsing quality end-to-end — that's deferred to the user's own interactive testing, which this feature is built to support (buttons are live on the Texts view page now).

## Documentation Review

Not started.

## Documentation Updates

Not started.
