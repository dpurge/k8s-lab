---
title: Translation capability wrapping rinex20/translategemma3:12b
kind: feature
status: done
version: 1
updated: 2026-09-21
branch: main
---

## Problem / Motivation

Second of six specs discussed for knowledge ingestion (see `specs/roadmap.md`'s
`## Next`). The knowledge base stores content in one configured language;
ingested content in a different language needs translating before it's
stored. This spec builds only the translation building block — a
`knowledge/internal/translate` package mirroring how `generate` already
wraps title/summary generation — with no wiring into ingestion yet (that's
`knowledge-ingest-pipeline`, still ahead of it in `## Next`).

Verified live against Ollama before designing: rather than a separate
language-detection step, a single instruction — "if this text is already
in {language}, return it unchanged; otherwise translate it" — works
correctly both ways with `rinex20/translategemma3:12b` (already pulled
locally): an English sentence came back unchanged (10.6s cold), and the
same sentence in Polish came back correctly translated to English (1.7s
warm). `ollama show` confirms this model has `capabilities: ["completion"]`
only — no `thinking`, so no repeat of the `gemma4:12b` latency trap from
earlier in this session.

## Acceptance Criteria

- [x] `knowledge/internal/translate/translate.go` (new) provides a
      `Service` with a `Translate(ctx, text) (string, error)` method,
      using the verified prompt shape above, parameterized by the
      configured target language. Empty input returns an error without
      calling the LLM (matching `generate`'s existing convention).
- [x] Translation uses its own independently-configured model/endpoint
      (`translate.*` in the config file), not `chat.*` or `generate.*` —
      `rinex20/translategemma3:12b` is a translation-specialized model,
      unrelated to either of those roles.
- [x] The knowledge base's target language is a new top-level
      `knowledgeLanguage` config field (default `English`), read once at
      startup like every other setting since `knowledge-config-configmap.md`
      — not something changed per session.
- [x] No wiring into `main.go`, `server.go`, or any HTTP route — this spec
      produces the capability only. `knowledge-ingest-pipeline` is the
      first real caller.
- [x] `go build`/`vet`/`gofmt`/`test` clean; a live check (not a committed
      test — `knowledge`'s CI has no reachable Ollama, so a live-dependent
      test file would break it) confirms the actual `translate.Service`
      Go code path — not just raw curl to Ollama — produces the same
      correct pass-through/translate behavior end to end.

## Approach

1. **`knowledge/internal/config/config.go`**: add `Translate{Provider,
   BaseURL,APIKey,Model,NumCtx}` to `fileConfig`/`Config`, following the
   exact pattern `Generate*` already established (defaults: `ollama`,
   `http://localhost:11434`, `rinex20/translategemma3:12b`, `0`;
   `TranslateAPIKey` stays env-var-sourced, like every other `*APIKey`).
   Add a new top-level `KnowledgeLanguage string` (default `English`).
2. **`knowledge/internal/translate/translate.go`** (new package, mirrors
   `generate`'s shape): `Service{llm *llm.Client, language string}`;
   `New(cfg config.Config) *Service` builds the `llm.Client` from
   `Translate*` config and stores `cfg.KnowledgeLanguage`;
   `ErrEmptyText` sentinel; `Translate(ctx, text)` rejects empty input,
   otherwise sends `{system: "If the following text is already in
   {language}, return it unchanged. Otherwise, translate it into
   {language}. Respond with only the resulting text — no preamble, no
   explanation.", user: text}` via `llm.Client.Complete`, trims the result.
3. **`knowledge/internal/translate/translate_test.go`**: one dependency-free
   unit test for the empty-input guard (no live Ollama needed, so safe to
   commit and run in CI).
4. **`knowledge/k8s/configmap.yaml`**: add a `translate:` section (real
   deployed values) and top-level `knowledgeLanguage: English`.
5. **`knowledge/README.md`**: add the new fields to the Configuration
   section's YAML example.
6. No `main.go`/`server.go` change — nothing constructs or calls
   `translate.Service` yet.

## Affected Areas

- `knowledge/internal/config/config.go`
- `knowledge/internal/translate/translate.go` (new)
- `knowledge/internal/translate/translate_test.go` (new)
- `knowledge/k8s/configmap.yaml`
- `knowledge/README.md`

## Out of Scope

- Wiring `translate.Service` into any HTTP route, the ingest pipeline, or
  `main.go` — that's `knowledge-ingest-pipeline`'s job.
- Language *detection* as a separate step — the single-instruction
  approach handles both cases, verified live.
- Making `knowledgeLanguage` changeable without a redeploy — per earlier
  discussion, it's a per-deployment setting.
- Chunk-level translation orchestration (splitting long ingested text into
  pieces before translating) — belongs to the ingest pipeline spec, which
  owns chunking.

## Implementation Notes

Implemented exactly per Approach:

- `knowledge/internal/config/config.go`: added `Translate{Provider,
  BaseURL,APIKey,Model,NumCtx}` and top-level `KnowledgeLanguage` to both
  `fileConfig` and `Config`, defaults as specified (`rinex20/
  translategemma3:12b`, `English`). `TranslateAPIKey` stays env-sourced.
- `knowledge/internal/translate/translate.go` (new): `Service`/`New`/
  `ErrEmptyText`/`Translate` exactly as designed.
- `knowledge/internal/translate/translate_test.go` (new): one
  dependency-free unit test for the empty-input guard.
- `knowledge/k8s/configmap.yaml`: added `translate:` + `knowledgeLanguage:
  English`.
- `knowledge/README.md`: added both to the Configuration YAML example.
- No `main.go`/`server.go` change, as planned — `translate.Service` is
  built but not yet constructed or called anywhere in this codebase.

## Validation

`go build`/`vet`/`gofmt`/`test` clean across all modules, including the
new `translate_test.go`. A throwaway, uncommitted test (written, run,
then deleted — a live-Ollama-dependent test would break CI, which has no
reachable Ollama) called the real `translate.New(cfg).Translate()` Go
path twice: an English sentence came back unchanged, and a Polish sentence
("To jest tekst w jezyku polskim o kotach") came back correctly translated
("This is a text in Polish about cats") — confirming the Go wiring itself
(config → `llm.Client` → prompt → response) is correct, on top of the
already-verified raw-Ollama behavior from B1. Also redeployed the live app
via `task deploy-knowledge` to confirm the larger `configmap.yaml` doesn't
break startup — startup log and `/health` both fine, no regression, exactly
as expected since nothing new is wired into any request path yet.

## Documentation Review

Checked `knowledge/README.md`'s Configuration section (already
comprehensive after `knowledge-config-configmap.md`) — needed the two new
fields added, done. No other docs reference config; no constitution-file
drift.

## Documentation Updates

`knowledge/README.md`: added `translate`/`knowledgeLanguage` to the
Configuration YAML example, noting `translate` isn't wired into any
feature yet.
