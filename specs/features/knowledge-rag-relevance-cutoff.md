---
title: Filter low-relevance results out of Qdrant search
kind: bugfix
status: done
version: 1
updated: 2026-09-21
branch: main
---

## Problem / Motivation

Tracked in `specs/roadmap.md`'s `## Later` since an earlier session: Qdrant
vector search always returns its top-N nearest neighbors, even when none of
them are a good match for the query. In knowledge chat this was observed to
make the chat model hallucinate a confident, fabricated answer instead of
saying it doesn't know, because it was handed two weakly-scored, genuinely
irrelevant documents as if they were real context (e.g. asked about Home
Depot's stock price against a knowledge base containing only k3d and
dragon-mythology articles — both retrieved items scored 0.23–0.28, and the
model still invented a specific dollar figure).

This session's own live testing (bge-m3 embeddings, this Qdrant collection)
gives a concrete, evidence-based split to filter on:
- Genuinely relevant matches scored 0.51–0.76 across several different
  questions against the same "Dragons biology" item.
- Genuinely irrelevant matches (same queries against an unrelated k3d
  registry article, and vice versa) scored 0.19–0.34.

There's a clear gap between these clusters around 0.4.

User decision: apply the cutoff to Qdrant search generally — both chat
retrieval and the general `/api/v1/knowledge` search API/UI — not just
chat. Since both already call the same `qdrant.Client.Search()`, the
cutoff belongs there, not duplicated in each caller.

## Acceptance Criteria

- [x] `qdrant.Client` has a configurable minimum-score threshold; semantic
      search results (`vectorSearch()`, i.e. queries with a non-empty
      `Query`) scoring below it are dropped from the returned list.
- [x] The threshold is configurable via a new `SEARCH_MIN_SCORE` env var,
      defaulting to `0.4` per the evidence above. `0` (or negative) disables
      filtering entirely, matching the `0`-means-off convention already
      used by `CHAT_NUM_CTX`/`GENERATE_NUM_CTX`.
- [x] Listing without a query (`scroll()` — tag/date browsing, no semantic
      ranking) is unaffected — there's no relevance score to filter on
      there, and no reason to.
- [x] Both the general search API (`/api/v1/knowledge/search` and the
      `q=` query-string form) and knowledge chat retrieval benefit
      automatically, with no changes to `chat.go` or `server.go`'s search
      handlers — filtering happens once, inside `qdrant.Client`.
- [x] Repeating the originally-observed hallucination case (a question
      genuinely unsupported by the knowledge base) now returns zero or
      near-zero sources instead of two weak, irrelevant ones, and the
      model's answer changes accordingly — validated live.
- [x] No regression: a genuinely relevant query against real content in the
      knowledge base still returns that content — validated live.

## Approach

1. **`knowledge/internal/config/config.go`** — add an `envFloat(key
   string, fallback float64) float64` helper (parses with
   `strconv.ParseFloat`, mirroring `envInt`). Add `SearchMinScore float64`
   to `Config`, loaded via `envFloat("SEARCH_MIN_SCORE", 0.4)`.
2. **`knowledge/internal/qdrant/qdrant.go`** — add `minScore float64` to
   `Client`; change `New(base, collection string, emb embeddings.Embedder,
   minScore float64) *Client` to accept and store it. In `vectorSearch()`,
   after building the `out` slice, filter it: keep an item only if
   `c.minScore <= 0` or `*it.Score >= c.minScore`.
3. **`knowledge/main.go`** — pass `cfg.SearchMinScore` into the single
   `qdrant.New(...)` call site.
4. **`knowledge/k8s/deployment.yaml`** — add `SEARCH_MIN_SCORE: "0.4"`
   explicitly, matching this codebase's convention of setting every config
   var even where it equals the default.
5. No change to `chat.go`, `server.go`, or the migrate job — they're
   unaffected by the `qdrant.New` signature change (main.go is the only
   caller) and inherit the filtering transparently through `Search()`.

## Affected Areas

- `knowledge/internal/config/config.go`
- `knowledge/internal/qdrant/qdrant.go`
- `knowledge/main.go`
- `knowledge/k8s/deployment.yaml`

## Out of Scope

- Per-request threshold overrides (e.g. a `min_score` query param) — a
  single configured value is enough per today's evidence; revisit only if
  a real need for per-call tuning shows up.
- Re-deriving the 0.4 default from a broader set of topics/embeddings —
  based on this session's live testing with `bge-m3` against this
  collection's actual content; revisit if a different embedding model or a
  much larger/more diverse knowledge base shows the split doesn't hold.
- Surfacing "N results were filtered as too weak" in the UI/API response —
  not requested; the effect (fewer, more relevant results) is the goal.

## Implementation Notes

Implemented exactly per Approach: `envFloat` helper and `SearchMinScore`
added to `knowledge/internal/config/config.go`; `qdrant.Client` gained a
`minScore` field, `New()`'s signature grew a `minScore float64` parameter
(single call site, `knowledge/main.go`, updated); `vectorSearch()` filters
its `out` slice on `c.minScore <= 0 || p.Score >= c.minScore`.
`knowledge/k8s/deployment.yaml` got an explicit `SEARCH_MIN_SCORE: "0.4"`.
No changes to `chat.go`, `server.go`, or the migrate job, as planned.

## Validation

`go build`/`vet`/`gofmt`/`test` clean. Deployed via `task deploy-knowledge`
and tested live, one sequential request at a time:

- General search API, irrelevant query ("current stock price of Home
  Depot") → `{"items": []}` (previously two weak matches, 0.28/0.23).
- General search API, relevant query ("What does a dragon look like?") →
  still returns `Dragons biology` at 0.703. No regression.
- Chat, the exact originally-hallucinating question → `sources: []`,
  answer changed from a fabricated stock price to "I do not have access to
  information regarding the current stock price of Home Depot."
- Chat, a genuinely relevant question (k3d registry DNS) → still retrieves
  and correctly answers using `K3d Registry Addressing` (score 0.578). No
  regression to chat itself.

## Documentation Review

Checked `knowledge/README.md`: found drift — the "Semantic search
defaults to..." sentence didn't mention the new filter, and the config
table had no `SEARCH_MIN_SCORE` row. Both fixed. No constitution-file
drift.

## Documentation Updates

`knowledge/README.md`: extended the semantic-search sentence to describe
the filter and why, and added a `SEARCH_MIN_SCORE` config row.
`CHANGELOG.md`: added a `## [Unreleased] / ### Fixed` entry.
`specs/roadmap.md`: removed this feature's `## Now` line (it originated in
`## Later`, moved directly to `## Now` at B2 start per the user's explicit
approval to implement — noted here for traceability, not re-gated as a
separate roadmap-direction change).
