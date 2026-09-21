---
title: Improve knowledge chat answer quality and multi-turn memory
kind: bugfix
status: done
version: 1
updated: 2026-09-21
branch: main
---

## Problem / Motivation

User-reported, with a real transcript, after the tool-calling fix
(`specs/features/fix-knowledge-chat-tools.md`) shipped: "The model responses
are very terse and skip relevant data." Investigation (live testing against
the deployed knowledge app and directly against Ollama) found two distinct,
separately-confirmed causes:

1. **Terseness on vaguely-phrased questions.** `llama3-chatqa:8b` is a small
   (8B, Q4-quantized) completion-style QA model. When a question is phrased
   abstractly (e.g. "I wanted the description of their physical appearance")
   rather than echoing the document's own vocabulary, it tends to just
   return the document's opening sentence instead of searching the whole
   passage — confirmed by direct Ollama testing (same document, same
   question, repeatable). Rephrasing the question with the document's exact
   terms (e.g. "shape, size, and which body parts...") got a genuinely
   complete answer, proving the model *can* extract the right content — it
   just needs help finding it. Adding a "be thorough" system instruction
   made the terse case *worse* (27 tokens vs. 85), so this isn't fixable by
   instruction alone.
   **Confirmed fix**: prepending each retrieved document's existing
   `Summary` field before its full body in the prompt context reliably
   improved the same vague question from a one-sentence non-answer to a
   substantive, multi-fact answer (verified via direct Ollama testing,
   before/after, same question).

2. **No multi-turn memory at all.** `knowledge/internal/chat/chat.go`'s
   `answer()` never references `c.Messages` (the chat's prior turns) —
   confirmed by reading the code. Each turn is answered from a blank slate,
   and *retrieval* also embeds only the literal current message. This is
   why the transcript's "Answer my last question" got a nonsensical,
   off-topic reply: retrieval for that literal string scored both available
   documents low and nearly tied (0.342 vs 0.339 — neither a real match),
   and the model had no way to know what "last question" even referred to.
   **Confirmed fix, two parts**:
   - Retrieval: embedding the current message concatenated with the
     immediately preceding user message (instead of the current message
     alone) separated the scores cleanly (0.339 → 0.696 for the actually
     relevant document) — verified live against `/api/v1/knowledge` search.
   - Prompt: including recent prior turns as real `user`/`assistant`
     messages (not collapsed into one blob) before the current
     context-bearing turn was tested directly against Ollama and produces a
     coherent, non-broken response — `llama3-chatqa:8b` handles a short
     multi-turn message array without the refusal/empty-answer failure mode
     found earlier for enumerated multi-document metadata (a different,
     already-fixed problem).

## Acceptance Criteria

- [x] `answer()` prepends `"Summary: " + src.Summary + "\n\n"` before each
      source's (truncated) body when building the prompt context.
- [x] `Send()` builds the Qdrant search query from the current message
      concatenated with the immediately preceding user message in the same
      chat (when one exists), not the current message alone. The literal
      current message is still what's sent to the model as "Question:" —
      only the *retrieval* query changes.
- [x] `answer()` includes the chat's recent prior turns (bounded to the last
      3 exchanges / 6 messages, oldest dropped first, to bound prompt
      growth) as real `user`/`assistant` `llm.Message` entries before the
      current context-bearing user turn.
- [x] A repeat of the reported transcript (ask a vague question, then "I
      wanted X" follow-up, then a reference like "answer my last question")
      produces on-topic, substantive answers — validated live against the
      deployed app, not just unit-level.
- [x] No regression to the already-fixed acceptance criteria in
      `fix-knowledge-chat-tools.md` (no `tools` field ever sent; still works
      unchanged with `CHAT_MODEL=llama3-chatqa:8b`).

## Approach

1. **`knowledge/internal/chat/chat.go` `Send()`** — before calling
   `s.kb.Search`, build the query text as: if `c.Messages` has a prior
   `user`-role entry, `<that message's Content> + " " + text`; otherwise
   just `text`. Pass this as `qdrant.Search.Query` instead of `text`
   directly (the `Question:` text sent to the model in `answer()` remains
   the unmodified `text`).
2. **`knowledge/internal/chat/chat.go` `answer()`**:
   - Prepend each source's `Summary` before its (possibly truncated) `Body`
     when building the per-document text, so both are visible to the model.
   - Build a bounded history slice from `c.Messages` (last 6, i.e. 3
     user/assistant exchanges) and prepend them as `llm.Message{Role:
     m.Role, Content: m.Content}` entries between the system message and
     the current context-bearing user message.
3. No changes to `shared/llm`, `knowledge/internal/config`, or
   `knowledge/k8s/deployment.yaml` — everything here is internal to
   `chat.go`'s prompt/query construction.

## Affected Areas

- `knowledge/internal/chat/chat.go`

## Out of Scope

- Summarizing or compacting history beyond the last 3 exchanges — if a chat
  outgrows that window, older context is simply dropped (no rolling
  summary); revisit only if this proves insufficient in practice.
- Any change to the relevance-cutoff problem already tracked in
  `specs/roadmap.md`'s `## Later` (`knowledge-rag-relevance-cutoff`) — that
  remains a separate, not-yet-scheduled piece of work.
- Retrieval query formation beyond "current + immediately preceding user
  message" (e.g. full conversation summarization for retrieval) — not
  evidenced as necessary by the reported transcript.

## Implementation Notes

Implemented exactly per Approach: added `lastUserMessage()` and
`recentHistory()` helpers to `knowledge/internal/chat/chat.go`. `Send()`
now builds the Qdrant retrieval query from the immediately preceding user
message (if any) concatenated with the current message. `answer()` now
prepends `"Summary: " + src.Summary + "\n\n"` before each (possibly
truncated) body, and prepends up to the last 6 `c.Messages` entries as real
`llm.Message` history before the current context-bearing user turn.

## Validation

`go build`/`vet`/`gofmt`/`test` clean. Deployed via `task deploy-knowledge`
and replayed the exact three-turn transcript from the original bug report,
live:

1. "How does a dragon look like?" — correct, complete, well-formatted
   (all nine resemblances, size range, flight/pearl details).
2. "Answer my last question" — retrieval score gap widened from 0.339 vs
   0.342 (tied, weak) to 0.696 vs 0.268 (clearly separated); answer
   correctly identified and addressed the referenced prior question, fully
   grounded in the document (previously: nonsensical, off-topic).
3. "I wanted the description of their physical appearance" — correctly
   narrowed to just appearance (unlike turn 2's broader answer), complete
   and accurate.

All three now succeed where the original transcript failed or gave
nonsense. Per-turn latency with `gemma4:12b` warm ranged ~14–54s depending
on accumulated history/context size — well under the 120s HTTP client
timeout, but real: this is chat-loop latency, not the ~1-3s seen in
isolated single-turn tests.

**Operational finding, not a code defect:** firing several overlapping
chat requests in quick succession during testing (retrying while a prior
slow request was still in flight) pushed the host to 15/16GB RAM used and
briefly starved the k3d node's kubelet enough that liveness probes failed
context-deadline-exceeded, restarting the `knowledge` pod and marking the
node briefly `NotReady`. It self-recovered within ~2 minutes once the
overlapping requests stopped. Relevant to
`knowledge-item-auto-generate.md`'s planned future bulk/backend
processing — that should issue requests sequentially, not concurrently,
given this hardware's headroom.

## Documentation Review

Checked `knowledge/README.md`: found drift — the "Chats are stored in
Postgres..." paragraph didn't mention retrieval-query concatenation,
summary-priming, or multi-turn history at all. Fixed. No constitution-file
drift (mission/tech-stack still accurate at their level of detail); no
other docs reference chat internals.

## Documentation Updates

`knowledge/README.md`: rewrote the "Chats are stored in Postgres..."
paragraph to describe retrieval-query concatenation, summary-priming, and
bounded chat-history replay. `CHANGELOG.md`: added a `## [Unreleased] /
### Changed` entry. `specs/roadmap.md`: removed this feature's `## Now`
line. `specs/memory.md`: appended an `[env]` entry about the node
resource-exhaustion finding from Validation.
