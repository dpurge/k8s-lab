---
title: Fix knowledge chat via code-side RAG, no tool-calling
kind: bugfix
status: done
version: 2
updated: 2026-09-21
branch: main
---

## Problem / Motivation

The knowledge app's chat feature is completely broken: every message returns
`chat provider returned 400 Bad Request: {"error":"registry.ollama.ai/library/llama3-chatqa:8b
does not support tools"}`.

Root cause (confirmed by reading code and reproducing the exact error against
the local Ollama instance):

- `knowledge/internal/chat/chat.go`'s `answer()` unconditionally builds a
  `tools` payload (a `read_knowledge_body` function letting the model fetch a
  retrieved document's full body) and passes it to every `s.llm.Chat(...)`
  call, regardless of model capability.
- `knowledge/k8s/deployment.yaml:43-44` sets `CHAT_MODEL=llama3-chatqa:8b`.
  That model does not support Ollama's tool-calling API — confirmed directly:
  `curl http://localhost:11434/api/chat` with a `tools` array and
  `model: llama3-chatqa:8b` reproduces the reported error verbatim.
  `/api/show` for that model lists `"capabilities": ["completion"]` only —
  no `tools` capability.

Decision (explicit user direction, overriding the tool-swap fix considered
earlier): keep `llama3-chatqa:8b` — it's tuned for QA-over-context — and stop
relying on model tool-calling entirely. Retrieval already happens in code via
Qdrant (`knowledge/internal/qdrant`); instead of asking the model to call a
tool to fetch a document's full body, fetch the top-N full bodies in code and
inject them directly into the prompt. This is real RAG, not agentic
tool-use, and it sidesteps the tool-capability problem entirely rather than
working around it.

A second, related finding: `shared/llm`'s Ollama path never sets Ollama's
`options.num_ctx`, so the model runs at Ollama's default runtime context —
confirmed empirically via `/api/ps` (`context_length: 4096`) — even though
`llama3-chatqa:8b`'s trained max is 8192 (`ollama show`'s
`llama.context_length: 8192`). Since full document bodies now go into the
prompt, using the model's real capacity matters. Confirmed empirically that
sending `options.num_ctx: 8192` in the `/api/chat` body raises the loaded
runtime context to 8192 (`/api/ps` reports `context_length: 8192`, with a
modest VRAM increase, ~5.0GB → ~5.5GB for this model).

## Acceptance Criteria

- [x] Sending a chat message in the knowledge app, deployed with
      `CHAT_MODEL=llama3-chatqa:8b` unchanged, succeeds (no 400) and returns
      an assistant reply grounded in retrieved documents.
- [x] No request from `knowledge` to the chat provider ever includes a
      `tools` field — `read_knowledge_body` and the tool-call loop are
      removed, not just made optional.
- [x] The top 5 Qdrant search results (by score) have their full Markdown
      body fetched in code and included directly in the LLM prompt, each
      truncated to ~4000 characters (word-boundary, not mid-word, with a
      trailing marker) if longer.
- [x] `shared/llm.Config` supports an optional `NumCtx` field; when set and
      the request goes through the Ollama path, the request body includes
      `"options": {"num_ctx": NumCtx}`. Unset (`0`) means no `options` field
      is sent, preserving today's behavior for every other caller.
- [x] `knowledge` wires this through a new `CHAT_NUM_CTX` env var (default
      `0`, unset), and `knowledge/k8s/deployment.yaml` sets it to `8192` for
      this model.
- [x] `knowledge/internal/chat/prompt.md` no longer instructs the model to
      call `read_knowledge_body` — it reflects that full documents are
      already provided.

## Approach

1. **`shared/llm/llm.go`** — add `NumCtx int` to `Config`. In `ollama()`,
   when `c.cfg.NumCtx > 0`, add `"options": {"num_ctx": c.cfg.NumCtx}` to the
   request body. `openAI()` is untouched (no equivalent knob; out of scope).
2. **`knowledge/internal/config/config.go`** — add `ChatNumCtx int`, loaded
   via the existing `envInt("CHAT_NUM_CTX", 0)` helper, and pass it into
   `llm.Config{NumCtx: cfg.ChatNumCtx}` in `chat.New()`.
3. **`knowledge/k8s/deployment.yaml`** — add `CHAT_NUM_CTX` with value
   `"8192"` alongside the existing `CHAT_MODEL` entry. `CHAT_MODEL` itself is
   unchanged (`llama3-chatqa:8b`).
4. **`knowledge/internal/chat/chat.go`**:
   - `Send()`: change the Qdrant search `Limit` from `7` to `5` (Qdrant
     already returns results ordered by score, so this is "pick the best 5"
     with no extra ranking logic needed).
   - `answer()`: for each of the (now 5) sources, fetch the full body via
     the existing `s.kb.Get(ctx, src.ID)` and inject it into the context
     text instead of relying on a tool call; truncate each body to ~4000
     characters at the nearest preceding whitespace, appending `[truncated]`
     when cut. If `Get` fails for an item, fall back to its summary rather
     than failing the whole request.
   - Remove the `tools` variable, the 3-step tool-call loop, and the
     `allowed` map (no longer needed once nothing calls back with a
     document ID) — a single `s.llm.Chat(ctx, messages, nil)` call replaces
     the loop.
5. **`knowledge/internal/chat/prompt.md`** — replace the line telling the
   model to call `read_knowledge_body` with wording that the full text of
   each retrieved document is already included below its summary.

Steps 1–3 (context window) and steps 4–5 (code-side RAG) are independent
enough to implement and validate separately, per the small-steps ground
rule — but both are needed together for the acceptance criteria (full
documents need the larger window to fit five of them safely).

## Affected Areas

- `shared/llm/llm.go`
- `knowledge/internal/config/config.go`
- `knowledge/internal/chat/chat.go`
- `knowledge/internal/chat/prompt.md`
- `knowledge/k8s/deployment.yaml`

## Out of Scope

- Changing `openAI()`'s tool or context-window handling.
- Making the document count (5) or per-document truncation length (~4000
  chars) configurable via env var — hardcoded per the approved numbers
  above; revisit only if it becomes a real problem (YAGNI).
- Any change to `phraseforge`'s use of `shared/llm` (it never passes `tools`
  and doesn't set `NumCtx`; both are opt-in and default to today's
  behavior).
- Auto-detecting a model's supported context length — `CHAT_NUM_CTX` is
  explicit, matching how `EMBEDDINGS_DIMENSION` already works.
- Re-validating `gemma4:12b` or other locally-pulled models — no longer
  relevant since the model is not being swapped.

## Implementation Notes

**Superseded note (added later, not re-gating this already-`done` spec):**
the deliberate choice below to keep `llama3-chatqa:8b` was revisited in
`specs/features/knowledge-switch-default-model.md` — the chat model is now
`gemma4:12b` (with Ollama's `thinking` disabled). The code-side RAG
architecture documented here (no `tools` sent, full document bodies fetched
and injected in code) is unaffected and still exactly what's deployed; only
which model receives that context changed.

Implemented in two steps, as planned:

**Step 1 — context window** (`shared/llm/llm.go`, `knowledge/internal/config/config.go`,
`knowledge/internal/chat/chat.go`'s `New()`, `knowledge/k8s/deployment.yaml`):
exactly as described in Approach items 1–3. `NumCtx`/`ChatNumCtx`/`CHAT_NUM_CTX`
all default to `0` (omit `options`), so `phraseforge` and any other caller is
unaffected — confirmed by building `phraseforge` after this change.

**Step 2 — code-side RAG** (`knowledge/internal/chat/chat.go`,
`knowledge/internal/chat/prompt.md`): `Send()`'s Qdrant `Limit` changed
7 → 5; the `tools` variable, 3-step tool-call loop, and `allowed` map were
removed; a single `s.llm.Chat(ctx, messages, nil)` call remains.

One deviation from the literal Approach wording, found and resolved during
end-to-end validation (documented here per the deviation-recording rule,
not re-gated — it doesn't change any Acceptance Criterion, only the internal
prompt format used to satisfy them): the Approach described keeping the old
`id=/score=/title=/tags=/link=`-annotated per-document format when injecting
full bodies. Live testing against Ollama showed `llama3-chatqa:8b` returns an
**empty or refusal answer** with that annotated format whenever more than one
document is present — reproduced repeatedly, isolated by varying one prompt
element at a time. Switching to **plain, unannotated concatenated document
text** (bodies joined with a blank line, no per-doc metadata) fixed this
reliably; `sources` still carries id/title/score/url for `appendReferences`,
the model itself is just never shown that metadata. `prompt.md` was written
to match (drops both the "call `read_knowledge_body`" line and the
"Include a References section" line — the latter is redundant with
`appendReferences` and, per the same testing, was part of what confused the
model).

Also added, found necessary during validation: `llama3-chatqa:8b` (NVIDIA
ChatQA) answers tersely — sometimes a single truncated word — unless the
question is suffixed with an explicit completeness instruction. `answer()`
now appends `" Please give a full and complete answer for the question."`
to the question before sending it. Confirmed via direct Ollama testing this
is a documented ChatQA prompting convention, not a workaround for our bug.

## Validation

**Automated** (`shared`, `knowledge`, `phraseforge`), run before and after
each step:
- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean in all three modules,
  no regressions.
- `go test ./...` in `knowledge` — no test files exist in this repo
  (confirmed pre-existing; not something this bugfix introduced or should
  add per Out of Scope), so this is a no-op pass, matching baseline.

**End-to-end, against the real k3d cluster** (`task deploy-knowledge`, then
live HTTP calls with a fresh signup/session):
- Created a real knowledge item (`K3d Registry Addressing`) and used the
  pre-existing unrelated `Dragons biology` item as a distractor.
- Chat question answerable only from the *full body* (not the summary) of
  the relevant item → HTTP 200, correct and complete answer, correctly
  ignored the distractor, referenced both in the links footer. Confirms
  full-body injection is genuinely in effect, not just summaries.
- Unsupported question ("current stock price of Home Depot") → HTTP 200
  (no crash), but the model **hallucinated** a fabricated answer instead of
  saying it doesn't know, since Qdrant always returns its top-5 nearest
  neighbors even when none are a good match, and nothing filters weak
  matches out before they reach the model. This is a pre-existing model/data
  limitation, not a regression from this change (the old tool-calling flow
  had the same "say you don't know" instruction and no relevance filter
  either). Per user decision, tracked as a roadmap follow-up
  (`knowledge-rag-relevance-cutoff` in `specs/roadmap.md`) rather than
  addressed in this bugfix.
- No 400 ever observed across all of the above, including with the model
  unchanged at `llama3-chatqa:8b`.

## Documentation Review

Checked `README.md` (root), `knowledge/README.md`, and the constitution
files (`specs/mission.md`, `specs/tech-stack.md`, `specs/roadmap.md`) for
drift caused by this change.

- **`knowledge/README.md:73-75`** — drift: "the app retrieves up to 7 Qdrant
  knowledge items, lets the local chat model read full bodies for retrieved
  documents if needed" describes the removed tool-calling design. Now
  retrieves 5, and the app itself fetches full bodies unconditionally
  (in code, not via a model tool call).
- **`knowledge/README.md:91`** — drift: the `CHAT_MODEL` config table row
  exists but the new `CHAT_NUM_CTX` variable is undocumented.
- **`knowledge/README.md:70`** — no drift: "Semantic search defaults to the
  7 most relevant items" describes the general `/api/v1/knowledge/search`
  REST endpoint's own default (`qdrant.Search`'s `Limit<=0` fallback,
  unchanged), not chat's hardcoded 5 — this sentence is about a different
  code path and remains accurate.
- **Root `README.md`** — no mention of chat internals, tool-calling, or
  `CHAT_MODEL`/`CHAT_NUM_CTX`; no drift found.
- **Constitution files** — no drift. `mission.md`'s "semantic search with
  LLMs" and `tech-stack.md`'s `shared/llm` description both remain accurate
  at their level of detail; neither needs the `NumCtx` implementation detail.
- No `CHANGELOG.md` exists in this repo yet.

## Documentation Updates

- `knowledge/README.md`: rewrote the "Chats are stored in Postgres..."
  paragraph to describe code-side full-body RAG instead of tool-calling, and
  the item count (5, not 7); added a `CHAT_NUM_CTX` row to the configuration
  table.
- `CHANGELOG.md`: created (didn't exist) with the standard Keep a Changelog
  header and a `## [Unreleased] / ### Fixed` entry for this bugfix.
- `specs/roadmap.md`: removed this feature's `## Now` line (now in the
  changelog, not in-flight) — the `knowledge-rag-relevance-cutoff` follow-up
  item added earlier in `## Later` is unaffected.
- No constitution-file content change was needed beyond the roadmap
  bookkeeping above.
