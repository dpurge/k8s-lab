---
title: Accept longer pages for ingestion and chunking
kind: feature
status: done
version: 1
updated: 2026-10-02
branch: main
---

## Problem / Motivation

Production jdpct101 ingest is capped at 24 KB (`phraseforge/internal/server/ingest.go:53`
`ingestMaxContentBytes = 24 * 1024`). Users want to submit longer pages.
However, raising only the cap is insufficient: process_text and process_dialog
send the full cleaned text in one LLM call (`phraseforge/internal/ingest/ingest.go:~160-210`),
and follow-up translation/transcription jobs send the full text to separate
`translation` and `transcription` LLM jobs (`phraseforge/internal/ingest/ingest.go:~300-353`).
Ollama silently truncates prompts that exceed its context window
(comment `phraseforge/internal/server/ingest.go:36-52`). On prod CPU-only (~4 tok/s;
`specs/memory.md` 2026-09-30T16:04:38Z: 942 chars took ~300s), a 24 KB translation
job at linear scaling would need ~2 hours versus the 1800s overall timeout.
Prod logs (43h) show `process_text` first-token timeouts at 300s.
Therefore long pages must be chunked for cleanup AND for translation/transcription.

## Acceptance Criteria

- [x] Config: `phraseforge/internal/config/config.go` gains `Config.IngestMaxContentBytes` field (YAML `ingest.maxContentBytes`), default 24576, with validation >0. Request-body headroom = max(16 KiB, maxContentBytes/4) applied in server construction and ingest request validation.
- [x] Pure functions in `phraseforge/internal/ai/chunk.go`: `chunkRunes(numCtx int) int` returns (numCtx/2 - 512) * 3 where numCtx <= 0 is treated as 4096; `splitChunks(text string, maxRunes int) []string` normalizes CRLF, packs paragraphs greedily, splits oversize paragraphs at newlines, hard-splits at runes.
- [x] Method `ai.Service.GenerateChunked(ctx, kind, sourceLanguage, targetLanguage, contentType, content)` splits content with chunkRunes(purpose NumCtx), calls Generate sequentially per chunk, joins with "\n\n" for text or "\n" for dialog; title uses only first chunk.
- [x] Ingest wiring: `handleProcess` uses GenerateChunked for cleanup and title; `ai.HandleGenerate` uses GenerateChunked for all kinds (translation, transcription, title, others); single-chunk input behaves exactly like Generate.
- [x] `phraseforge/k8s/configmap.yaml` sets `ingest.maxContentBytes: 204800` (200 KiB); server cap stays 24 KiB by default until this deployment.
- [x] Tests: config defaults/overrides, splitChunks table tests (short/paragraph packing/oversize paragraph/hard split/CRLF/empty/round-trip), chunked generate with fake Ollama (long input -> N sequential requests within chunk size, joined output; title -> 1 request; short -> 1 request; dialog separator). **Note:** HandleGenerate-level test without DB — not met; both call sites need DB and job queue, covered by compile + tests of the pieces they call.

## Approach

1. **Configurable cap & headroom** (independently shippable): config.go gains `Config.IngestMaxContentBytes` (default 24576, validated >0) and field `Config.IngestRequestBodyHeadroom` computed at construction as max(16 KiB, maxContentBytes/4). server.New() receives cap+headroom from main.go (unexported field, implementer decides mechanism). Ingest request validation uses the cap and headroom. Tests verify config default/override/invalid and headroom helper. Behavior unchanged at default cap.
2. **Splitter & GenerateChunked** (independently shippable): chunk.go exports pure `chunkRunes(numCtx int) int` and `splitChunks(text string, maxRunes int) []string`; chunk_test.go covers table tests (short/paragraphs/newline splits/rune-safe hard split/CRLF/empty/content round-trip with joined chunks). ai.go adds method `GenerateChunked` which calls splitChunks, calls Generate per chunk sequentially (single worker, each chunk keeps timeout, shares maxAttempts), joins with "\n\n" (text) or "\n" (dialog). Title uses only first chunk. Tests include fake-Ollama end-to-end (long input multipart chunked requests with exactly N requests within limit, joined output matches input modulo separators).
3. **Wiring & deployment** (independently shippable): ingest.handleProcess calls GenerateChunked for cleanup and title. ai.HandleGenerate calls GenerateChunked for every kind. configmap.yaml sets `ingest: maxContentBytes: 204800` (200 KiB). README/CHANGELOG updated per B4. Tests run via `cd phraseforge && go test ./...`. Idempotency: all chunks processed before Create so `created:` step marker unchanged; failure at chunk k loses 1..k-1 on manual Retry (per-chunk checkpoints out of scope).

## Affected Areas

- phraseforge/internal/config/config.go (ingestMaxContentBytes, validation, request-body headroom)
- phraseforge/internal/config/config_test.go (config defaults, overrides, headroom)
- phraseforge/internal/server/ingest.go (request validation, cap+headroom from config)
- phraseforge/internal/server/server.go (server constructor receives cap+headroom; tests updated)
- phraseforge/main.go (wiring config cap to server)
- phraseforge/internal/ai/chunk.go (new: chunkRunes, splitChunks)
- phraseforge/internal/ai/chunk_test.go (new: table tests)
- phraseforge/internal/ai/ai.go (GenerateChunked method, HandleGenerate calls it)
- phraseforge/internal/ingest/ingest.go (handleProcess uses GenerateChunked)
- phraseforge/k8s/configmap.yaml (ingest.maxContentBytes: 204800)
- phraseforge/README.md (ingest cap documented; B4 decides)
- phraseforge/CHANGELOG.md (feature documented; B4 decides)

## Out of Scope

- URL fetch cap (5 MB) and fetch-specific handling.
- generate_vocabulary / generate_models chunking (unchanged; existing single-call interface).
- Per-chunk checkpoints or separate DB jobs per chunk.
- Parallel chunk processing.
- Per-purpose chunk-size setting (derive from existing per-purpose numCtx; tune via numCtx).
- GPU, model changes, or raising NumCtx.
- Knowledge app.

## Implementation Notes

**Step 1 — Config & server setup (phraseforge/internal/config/config.go, phraseforge/internal/server/ingest.go, phraseforge/main.go)**
- `Config.IngestMaxContentBytes` field added; YAML key `ingest.maxContentBytes`; default 24576 bytes; validation rejects <=0 with error `"config file <path>: ingest.maxContentBytes must be greater than 0, got N"` (config.go:251-252).
- `defaultIngestMaxContentBytes` const moved to server/ingest.go:57; new `Server.ingestMaxContentBytes` field; new `Server.SetIngestMaxContentBytes(n)` method (non-positive keeps default); tests use bare `&Server{}` to test the default.
- `ingestBodyLimit(cap)` = cap + max(16 KiB, cap/4) helper computes request-body headroom (server/ingest.go:412-414); unchanged at the 24 KiB default.
- Tests verify: config default 24576, file override 204800, rejection of 0 and -5; server cap default/override/fallback; ingestBodyLimit math (24, 64, 200 KiB inputs).

**Step 2 — Chunking (phraseforge/internal/ai/chunk.go)**
- `chunkRunes(numCtx)` = max((numCtx/2-512)*3, 512); numCtx <= 0 treated as 4096 (assumption: Ollama's real default window unknown).
- `splitChunks(text, maxRunes)` normalizes CRLF to LF; returns text unchanged if it fits or is whitespace-only (nil); packs paragraphs greedily at double-newlines, line breaks, then runes; oversize parts never glued to coarser ones; UTF-8 safe.
- `GenerateChunked` (ai/chunk.go:110-114) wraps Generate: splits content, calls sequentially, joins with "\n\n" for text or "\n" for dialog; title uses first chunk only; single chunk behaves exactly like Generate.
- `generateChunks` (ai/chunk.go:122-147) pure orchestrator, testable without DB; failing chunk → `"chunk k of n: ..."` error; blank content → `"content is required"`.
- Tests in chunk_test.go (9 cases) and chunked_generate_test.go (8 cases): chunk sizes, empty/CRLF/paragraph/line/rune splitting, multi-byte safety, sequential requests, output joining.

**Step 3 — Wiring & deployment (phraseforge/internal/ingest/ingest.go, phraseforge/internal/ai/ai.go, phraseforge/k8s/configmap.yaml)**
- `ingest.handleProcess` calls `GenerateChunked` for cleanup and title (title gets whole cleaned text, GenerateChunked sends only its first chunk).
- `ai.HandleGenerate` calls `GenerateChunked` for every kind (so translation/transcription are chunked).
- phraseforge/k8s/configmap.yaml:27-32 sets `ingest: maxContentBytes: 204800` with comment explaining chunking and queue impact.
- `TestConfigMapItemPromptsMatchDefaults` asserts ConfigMap loads `IngestMaxContentBytes == 204800`.
- Wiring call sites not unit-tested (need DB/job queue); covered by compile + tests of `splitChunks`/`GenerateChunked` pieces.

**Self-review notes:** No Critical/High findings. First-party frontend (phraseforge/internal/server/static) has no client-side size limit (grepped JS/templates). Prod ingress (phraseforge.home.arpa, not in this repo) request-body limit unknown. Stored job payload now up to 200 KiB per ingest. Each chunk keeps its own maxAttempts budget, so a chunk can take up to 3 retries.

## Validation

**Build & unit test pass:**
- `gofmt -l .` clean (phraseforge root).
- `go vet ./...` clean (phraseforge root).
- `go test ./...` passes in phraseforge (ai, config, generate, ingest, pagination, server packages); shared and knowledge tests also pass.
- `go test ./internal/config/` passes after the docs edits.
- ConfigMap YAML parses without error.

**Not verified:**
- Behavior against real prod Ollama (API response time, streaming behavior, truncation risk with very large chunks).
- Chunk processing duration on CPU-only prod hardware (earlier estimate: LOW confidence; testing would require 200 KiB ingest on actual cluster).
- Whether first_token timeout (300s, not retried per config.go:287) hits chunks on slow hardware; queue hold time with one worker on ~19 chunks (NumCtx 8192, up to 10752 runes each) or ~44 chunks (NumCtx 0 for translation/transcription, 4608 runes each).

**Post-deploy checks (recommended):**
- Monitor `llm call` log: `duration_ms`, `limit` per chunk; if queue hogging occurs, lower `ingest.maxContentBytes` in ConfigMap.

## Documentation Review

**Audit scope:** phraseforge/CHANGELOG.md, phraseforge/README.md, phraseforge/k8s/configmap.yaml, specs/tech-stack.md, constitution files.

**Corrections to proposed audit:**
- Proposed audit claimed long ingest "does not block the queue," but the final changelog entry and configmap comment correctly state it occupies the single job worker until it finishes; this is correct.
- Proposed audit said final README would refer readers to CHANGELOG; the final README bullet (line 39) is self-contained and does not.
- Proposed audit claimed knowledge needs a changelog entry; knowledge/CHANGELOG.md was not updated, which is correct since this change touches no file under `shared/` (the only files shared between artifacts).
- Grepped phraseforge/CHANGELOG.md and phraseforge/README.md; no text states the old fixed 24 KB limit — all references cite the configurable default (24 KiB) and/or the 200 KiB prod value.

**Documents checked:** phraseforge/CHANGELOG.md (✓ entry present), phraseforge/README.md (✓ entry present), phraseforge/k8s/configmap.yaml (✓ block present with correct comments), knowledge/CHANGELOG.md (✓ not updated, correct), specs/tech-stack.md (✓ no drift), specs/mission.md (✓ no drift).

## Documentation Updates

**Applied changes:**

- **phraseforge/CHANGELOG.md** — "### Added" section (lines 9–13): ingest content size limit is configurable via `ingest.maxContentBytes` (default stays 24 KiB, prod ConfigMap sets 200 KiB); long texts/dialogs are cleaned in chunks fitting each purpose's context window, one LLM call per chunk; long-text translation and transcription chunked the same way; title derived from first chunk; a long ingest occupies the single job worker until it finishes.
- **phraseforge/README.md** — "LLM configuration" section, new bullet (line 39): `ingest.maxContentBytes` (default 24576) — largest raw content that one ingest request may carry; content is processed in chunks, one LLM call per chunk, so a larger limit means a longer-running job; the queue runs one job at a time.
- **phraseforge/k8s/configmap.yaml** — ingest block and comments (lines 27–32): already present from implementation step, no further change.
- **knowledge/CHANGELOG.md** — no entry (feature does not touch shared/).
- **Constitution files** (`specs/tech-stack.md`, `specs/mission.md`) — no updates required.
