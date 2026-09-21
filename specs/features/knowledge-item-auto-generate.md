---
title: Auto-generate title and summary for knowledge items
kind: feature
status: done
version: 1
updated: 2026-09-21
branch: main
---

## Problem / Motivation

Creating a knowledge item currently requires typing title, summary, tags,
and body by hand. The user wants to only write the body and set tags
manually, then generate a title and a summary from the body with a button
each, adjusting the generated text before saving if needed.

Investigated which local model to use for this — it's a different task from
chat Q&A, so the existing `CHAT_MODEL` couldn't be assumed suitable without
checking: direct testing against Ollama showed `llama3-chatqa:8b` (the
chat model, a completion-style QA model, `capabilities: ["completion"]`
only) fails this task badly — given a "write only a title" instruction, it
ignored it and hallucinated an unrelated, partly contradictory continuation
of the document instead of extracting a title. `gemma4:12b` (already
pulled locally, a real instruction-following model), given the identical
prompt and document, produced a correct, concise, instruction-compliant
title and, separately, a correct one-paragraph summary. This confirms
generation needs its own model configuration, independent of `CHAT_MODEL`
(even though, per `specs/features/knowledge-switch-default-model.md`, both
now default to the same underlying model, `gemma4:12b` with Ollama's
`thinking` disabled — that spec is what makes it fast enough for the bulk
processing planned later; without `think: false` this model is unusably
slow, as also documented there).

## Acceptance Criteria

- [x] The knowledge item editor still has separate Title/Tags/Summary/Body
      fields, all still directly editable — this feature adds generation,
      it doesn't remove manual editing.
- [x] A "Generate" button next to Title fills the Title field from the
      current Body's content via the LLM; a separate "Generate" button next
      to Summary does the same for Summary. Both are one word, per style.
- [x] Clicking Generate with an empty Body shows a message and makes no
      request (fail fast, no wasted LLM call).
- [x] Generated text populates the field but isn't saved until the user
      clicks the existing Save button — the user can edit the generated
      text first.
- [x] Generation uses a distinct, independently-configured model
      (`GENERATE_MODEL` etc., default `gemma4:12b`), not hardcoded to
      `CHAT_MODEL`'s value — proven necessary by the `llama3-chatqa:8b`
      failure above, even though both happen to default to the same model
      today.
- [x] `knowledge/k8s/deployment.yaml` sets `GENERATE_BASE_URL` (to
      `http://host.docker.internal:11434`, mirroring `CHAT_BASE_URL`) and
      `GENERATE_MODEL` (to `gemma4:12b`, the tag actually pulled and tested
      on this machine) so generation works in the deployed cluster, not just
      locally.

## Approach

1. **`shared/llm`** — no change; reuse the existing generic `Config`/
   `Client`/`Complete`.
2. **`knowledge/internal/config/config.go`** — add `GenerateProvider`,
   `GenerateBaseURL`, `GenerateAPIKey`, `GenerateModel`, `GenerateNumCtx`,
   loaded the same way as the existing `Chat*`/`Embeddings*` quads
   (`GENERATE_PROVIDER` default `ollama`, `GENERATE_BASE_URL` default
   `http://localhost:11434`, `GENERATE_MODEL` default `gemma4:12b`,
   `GENERATE_NUM_CTX` default `0`).
3. **`knowledge/internal/generate/generate.go`** (new package, mirrors the
   `chat`/`auth` package shape) — `Service` wrapping an `llm.Client` built
   from the `Generate*` config. Two methods, `Title(ctx, body)` and
   `Summary(ctx, body) (string, error)`, each a single `llm.Complete` call
   with a fixed system prompt instructing a bare title / one-paragraph
   summary respectively (no preamble, no quotes) and the body as the user
   message. Both trim whitespace from the result and reject an empty body
   before calling the LLM.
4. **`knowledge/internal/server/server.go`** — add a `generate
   *generate.Service` field and constructor parameter; two new routes under
   the existing authenticated `/api/v1` group:
   `POST /knowledge/generate/title` and `POST /knowledge/generate/summary`,
   both taking `{"body": "..."}` and returning `{"title": "..."}` /
   `{"summary": "..."}` respectively (400 on empty body, mirroring existing
   handler conventions).
5. **`knowledge/main.go`** — construct the `generate.Service` and pass it
   into `server.New`.
6. **`knowledge/internal/server/static/index.html`** — add a one-word
   "Generate" button next to the Title field and another next to the
   Summary field. Each POSTs the current Body field's value to the matching
   endpoint and overwrites that field's value with the result; each is a
   no-op with a message if Body is empty. No other form fields change.
7. **`knowledge/k8s/deployment.yaml`** — add `GENERATE_BASE_URL` and
   `GENERATE_MODEL` env vars (values above).

## Affected Areas

- `knowledge/internal/config/config.go`
- `knowledge/internal/generate/generate.go` (new)
- `knowledge/internal/server/server.go`
- `knowledge/main.go`
- `knowledge/internal/server/static/index.html`
- `knowledge/k8s/deployment.yaml`

## Out of Scope

- Removing or hiding the manual Title/Summary inputs — they stay editable.
- Batch/bulk generation for existing items, or regenerating on every save.
- Changing the existing `qdrant.validate()` rule that title/summary/body
  must all be non-empty at save time — unrelated to this feature.
- Streaming the generated text token-by-token — a single blocking request
  per click is enough for this use case.
- Changing `CHAT_MODEL`/the chat path — handled separately in
  `knowledge-switch-default-model.md`. This feature only adds
  `GENERATE_MODEL` as its own independent config knob.

## Implementation Notes

Implemented exactly per Approach:

- `knowledge/internal/config/config.go`: `GenerateProvider`/`GenerateBaseURL`/
  `GenerateAPIKey`/`GenerateModel`/`GenerateNumCtx`, defaults matching the
  existing `Chat*` quad's shape (`ollama` / `http://localhost:11434` / `""` /
  `gemma4:12b` / `0`).
- `knowledge/internal/generate/generate.go` (new): `Service` wrapping its
  own `llm.Client`; `Title`/`Summary` methods, each rejecting an empty body
  with `ErrEmptyBody` before calling the LLM.
- `knowledge/internal/server/server.go`: `generate *generate.Service`
  field/constructor param; `POST /knowledge/generate/title` and
  `POST /knowledge/generate/summary` routes (both under the existing
  authenticated group), 400 on `ErrEmptyBody`, 500 on any other error.
- `knowledge/main.go`: constructs `generate.New(cfg)`, passes it into
  `server.New`.
- `knowledge/internal/server/static/index.html`: one-word "Generate"
  buttons next to Title and Summary, each guarding on an empty Body client
  side before calling its endpoint and overwriting the field.
- `knowledge/k8s/deployment.yaml`: added `GENERATE_PROVIDER`,
  `GENERATE_BASE_URL`, `GENERATE_MODEL` (matching the `CHAT_*` pattern for
  explicitness even where a value equals its default).

## Validation

`go build`/`vet`/`gofmt`/`test` clean. Deployed via `task deploy-knowledge`
and tested live, one sequential request at a time (per the concurrency
lesson from `knowledge-chat-context-quality.md`'s Validation):

- `POST /knowledge/generate/title` with a real document body → `200`,
  `"Mythology and Symbolism of the Dragon"` in 10.6s.
- `POST /knowledge/generate/summary`, same body → `200`, a correct,
  on-task one-paragraph summary in 3.4s.
- `POST /knowledge/generate/title` with `{"body":""}` → `400
  validation_failed`, no LLM call made.

**Not verified: real browser interaction.** No browser-automation tool was
available in this session, so the "Generate" buttons were not literally
clicked in a rendered page. What was verified: the backend endpoints
directly (above), and that the added JS uses the exact same `run()`/
`api()`/`message()` helpers every other button on this page already uses,
with no new client-side mechanism introduced. Recommend a quick manual
check at `http://knowledge.localhost:8080` before relying on this feature.

## Documentation Review

Checked `knowledge/README.md`: found drift — the feature-bullet list at the
top didn't mention generation, the config table was missing all `GENERATE_*`
rows, and the API reference section (which documents every other endpoint,
e.g. "Chat with knowledge") had no entry for the two new routes. All fixed.
No constitution-file drift.

## Documentation Updates

`knowledge/README.md`: added a feature bullet, five `GENERATE_*` config
rows, and a new "### Generate title/summary" API reference section — its
example command was actually run against the live deployment before being
committed to docs, per the verify-before-publish rule, and its output
matches what's shown. `CHANGELOG.md`: added a `## [Unreleased] / ### Added`
entry. `specs/roadmap.md`: removed this feature's `## Now` line.
