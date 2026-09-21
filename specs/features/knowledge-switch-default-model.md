---
title: Switch chat and generation to gemma4:12b with thinking disabled
kind: bugfix
status: done
version: 1
updated: 2026-09-21
branch: main
---

## Problem / Motivation

User direction, arrived at through live testing in this session: use one
model, `gemma4:12b`, for both chat and the new generation feature
(`specs/features/knowledge-item-auto-generate.md`), instead of
`llama3-chatqa:8b` for chat and a separate model for generation.

`gemma4:12b` was initially rejected for this: a first test (default
settings) took **148.5 seconds** for one chat turn and still answered
incorrectly ("I do not know"), and `top` showed the machine at 15 of 16GB
RAM used, 80MB free, heavy compressor activity — a real, observed
system-wide slowdown, not just a slow response. Root cause, isolated by
testing: `gemma4:12b` has Ollama's `thinking` capability enabled by default,
generating a large hidden reasoning trace before every answer. Sending
`"think": false` in the Ollama `/api/chat` request body fixes this
directly — confirmed by repeating the *same* multi-turn, multi-document test
that took 148.5s and got the wrong answer: with `think: false` (model warm)
it took **14.3 seconds** and gave a correct, complete answer. A cold-load
single-turn request also confirmed the split: 13.2s one-time model load,
~1.45s actual inference. Title generation with `think: false` took 2.9s and
produced a correct, clean, instruction-compliant title.

Two other models considered and rejected in this session, for the record:
`llama3-chatqa:8b` (`GENERATE`-role only — chat itself is being replaced
here) hallucinated an unrelated, partly contradictory continuation instead
of a title, since it's a completion-only QA model with no general
instruction-following. `gemma4:e4b`/`gemma4:e2b` (Ollama "elastic" variants)
looked small by name but are actually **9.6GB and 7.2GB** downloads
respectively — larger than `gemma4:12b`, not a lighter alternative. Several
other candidate small instruct models (`llama3.2:3b`, `mistral:7b-instruct`,
`qwen2.5:7b-instruct`, `phi4-mini`, `gemma3:4b`) were proposed but the user
had already evaluated and rejected these in earlier work; `qwen2.5:7b-instruct`
also failed to download twice with a registry/network error unrelated to
model choice. `gemma4:12b` with `think: false` is the validated, working
answer.

This supersedes part of the already-shipped
`specs/features/fix-knowledge-chat-tools.md`, which deliberately kept
`llama3-chatqa:8b` and built code-side RAG specifically to work around its
lack of tool-calling. That code-side RAG design (no `tools` sent, full
document bodies fetched and injected in code) is *not* undone here — it's
model-agnostic and still the right architecture — only the deployed model
changes.

## Acceptance Criteria

- [x] `shared/llm/llm.go`'s `ollama()` always sends `"think": false` in the
      request body (unconditional — this app has no use case that wants a
      hidden reasoning trace, and it's a no-op for models without a
      `thinking` capability).
- [x] `knowledge/internal/config/config.go`'s `CHAT_MODEL` defaults to
      `gemma4:12b`. `GENERATE_MODEL` doesn't exist yet — it's added, also
      defaulting to `gemma4:12b`, in `knowledge-item-auto-generate.md`.
- [x] `knowledge/k8s/deployment.yaml`'s `CHAT_MODEL` changes from
      `llama3-chatqa:8b` to `gemma4:12b`.
- [x] The `llama3-chatqa`-specific "Please give a full and complete answer
      for the question." suffix in `chat.go`'s `answer()` is removed — it
      was a workaround for that specific model's terseness, confirmed
      unnecessary for `gemma4:12b` (already produces complete answers
      without it, verified live).
- [x] A repeat of the originally-reported terse/wrong-answer transcript's
      first turn produces a correct, complete answer once the model is
      warm — validated live against the deployed app. (The follow-up turn
      is still unfixed by design — that's `knowledge-chat-context-quality.md`'s
      job, confirmed as an expected, not-yet-fixed gap.)
- [x] `fix-knowledge-chat-tools.md` got a short note in its Implementation
      Notes pointing here, documenting that its model choice was later
      revisited (its code-side RAG design is unaffected and still in use).

## Approach

1. **`shared/llm/llm.go`** — in `ollama()`, add `"think": false` to the
   request body unconditionally (alongside the existing `stream`/`options`
   fields).
2. **`knowledge/internal/config/config.go`** — change the `CHAT_MODEL`
   fallback from `"gemma4:e4b"` to `"gemma4:12b"` (the `e4b` tag is real but
   is a 9.6GB elastic variant, not the lightweight default the name
   suggests, and was never actually deployed). `GENERATE_MODEL`'s default
   (added in `knowledge-item-auto-generate.md`) is also `"gemma4:12b"`.
3. **`knowledge/k8s/deployment.yaml`** — change `CHAT_MODEL` to
   `gemma4:12b`. `CHAT_BASE_URL` stays as-is (already
   `http://host.docker.internal:11434`); `CHAT_NUM_CTX` stays `8192` (no
   evidence it needs to change — `gemma4:12b`'s trained max is far higher,
   but nothing here needs more than the already-tested 8192).
4. **`knowledge/internal/chat/chat.go`** — remove the
   `" Please give a full and complete answer for the question."` suffix
   from the question text in `answer()`.
5. No change to the code-side RAG design itself (retrieval, full-body
   injection, no `tools` sent) — that stays exactly as implemented in
   `fix-knowledge-chat-tools.md`.
6. **`fix-knowledge-chat-tools.md`** — append a short note to
   Implementation Notes pointing to this spec.

## Affected Areas

- `shared/llm/llm.go`
- `knowledge/internal/config/config.go`
- `knowledge/internal/chat/chat.go`
- `knowledge/k8s/deployment.yaml`
- `specs/features/fix-knowledge-chat-tools.md` (pointer note only)

## Out of Scope

- Re-testing whether the metadata-annotated (`id=/score=/title=`) context
  format works on `gemma4:12b` — plain concatenated text already works
  well for it (validated live); no reason to change back.
- Removing the summary-priming improvement from
  `knowledge-chat-context-quality.md` — it's harmless and still a
  reasonable defensive practice even though `gemma4:12b` didn't strictly
  need it in testing.
- Pulling or evaluating any further models.
- Changing `EMBEDDINGS_MODEL` (`bge-m3`) — unrelated to this decision.

## Implementation Notes

Implemented exactly per Approach: `think: false` added unconditionally in
`shared/llm/llm.go`'s `ollama()`; `CHAT_MODEL` default changed to
`gemma4:12b` in `knowledge/internal/config/config.go`; `deployment.yaml`'s
`CHAT_MODEL` changed to `gemma4:12b` (comment updated to reflect the new
model); the `llama3-chatqa`-specific completeness suffix removed from
`chat.go`'s `answer()`. No changes needed to `CHAT_NUM_CTX` (still 8192, no
evidence it needs to differ for this model) or to the code-side RAG design.

## Validation

`go build`/`vet`/`gofmt`/`test` clean across `shared`, `knowledge`,
`phraseforge`. Deployed via `task deploy-knowledge` and tested live:
repeating the originally-reported transcript's first turn ("How does a
dragon look like?") now returns a complete, accurate, well-formatted answer
(all nine resemblances, size range, flight/pearl details) instead of the
original one-sentence/wrong-topic response. A second turn ("Answer my last
question") correctly returns "I do not know" rather than hallucinating —
better than before, though still not using conversation history, since that
fix belongs to `knowledge-chat-context-quality.md`, not this spec; expected
and confirms this spec's boundary.

## Documentation Review

Checked `knowledge/README.md`: found drift at the `CHAT_MODEL` config table
row (still said default `gemma4:e4b`) — fixed. `CHANGELOG.md`'s existing
entry and the "Chats are stored in Postgres..." paragraph's mention of
`llama3-chatqa` are architectural/historical statements, not claims about
current deployment, so left as-is. No constitution-file drift.

## Documentation Updates

`knowledge/README.md`: `CHAT_MODEL` row updated to `gemma4:12b` default,
with a note on the unconditional `think: false` and why. `CHANGELOG.md`:
added a `## [Unreleased] / ### Changed` entry. `specs/roadmap.md`: removed
this feature's `## Now` line.
