---
title: Stream Ollama chat responses and time out on lack of progress
kind: feature
status: done
version: 1
updated: 2026-09-30
branch: main
---

## Problem / Motivation

Prod phraseforge (`jdpct101`) fails every translation job. Measured on
2026-09-30 against prod Ollama (`gemma4:12b`, CPU-only, `limits.cpu: 6`):

| Test | Result |
|---|---|
| Cold load, no prompt | 27 s (`done_reason: "load"`) |
| Warm translation of a 942-char Chinese text | 300 s total: 29 s prompt eval (699 tokens) + 271 s generation (1076 tokens, 3.97 tok/s) |
| Client disconnect 15 s into a cold load | Ollama logs `client connection closed before llama-server finished loading, aborting load` — the load is thrown away |

`shared/llm` sends `"stream": false` under one overall `http.Client` timeout
(`llm.go:62`, `llm.go:100`). Healthy-but-slow generation (300 s) is
indistinguishable from a hang, so the 120 s translation timeout kills
every job, and each killed job also aborts any in-progress model load.
Raising the single timeout would fix it but delay every genuine failure
by the same amount — the user rejected that trade-off.

Streaming (`"stream": true`) returns one NDJSON line per token (≈0.22 s
apart on a warm prod model, verified live), so progress is observable:
the call can allow a long wait for the first token (covers load and
prompt eval) and a short maximum gap between tokens (catches a stall
within seconds, regardless of output length).

Verified live on prod Ollama 0.34.1:
- Chunk: `{"message":{"role":"assistant","content":"Hi"},"done":false}`.
- Thinking models stream `message.thinking` with empty `content` — this
  must count as progress.
- Final chunk: `"done":true`, `done_reason` (`stop`/`length`/…), timing
  stats.
- Unknown model: HTTP 404 `{"error":"model 'x' not found"}` before any
  stream starts.

User decisions (2026-09-30): no-progress timeouts are configured per
provider; the existing `timeoutSeconds` stays as an overall backstop with
raised defaults; both phraseforge and knowledge switch to streaming. Knowledge
gets a phraseforge-style provider registry later (`knowledge-llm-providers-config`,
knowledge roadmap Next); until then it takes `shared/llm`'s defaults with no
new config keys, so there is no short-lived per-section config to migrate.

Assumption: a mid-stream `{"error": ...}` line (not observed in testing)
is treated as a failure carrying that message.

## Acceptance Criteria

- The Ollama path of `shared/llm` always streams. The OpenAI/OpenRouter
  path is unchanged.
- Three limits apply to an Ollama call:
  - **first token**: no `content`, `thinking`, or `tool_calls` chunk within
    `FirstTokenTimeout` → error `no output from model <m> within <n>s (model
    load + prompt processing)`;
  - **idle**: after the first token, no chunk within `IdleTimeout` → error
    `model <m> stalled: no output for <n>s`;
  - **overall**: `Timeout` still caps the whole call → error `model <m>
    exceeded overall timeout of <n>s`.
- Defaults in `shared/llm` when zero: first token 300 s, idle 60 s,
  overall 1800 s (was 2 min). Streamed `content` is concatenated;
  `tool_calls` from any chunk are accumulated; the returned `Response` is
  identical in shape to today's.
- Non-2xx responses keep today's error text (`chat provider returned
  <status>: <body>`).
- Cancelling the caller's `ctx` still aborts the call (existing behavior,
  `memory.md` 2026-09-25T17:18:30Z).
- phraseforge config: `providers.ollama.firstTokenTimeoutSeconds` and
  `providers.ollama.idleTimeoutSeconds` (defaults 300/60), passed to every
  Ollama call; purpose `timeoutSeconds` defaults raised to 1800 for all
  seven purposes (`k8s/configmap.yaml` and `config.go` defaults). Admin >
  LLM per-rule `timeout_seconds` still overrides the overall cap only.
- knowledge: no code or config change; its three `llm.New` calls pass zero
  values and so stream with the `shared/llm` defaults (300/60/1800 s).
- phraseforge's `llm call` log line gains the limit that fired, when one
  did (`limit=first_token|idle|overall`).

## Approach

1. **`shared/llm`**: add `FirstTokenTimeout`, `IdleTimeout` to `Config`;
   replace the Ollama path's `post` with a streaming reader that decodes
   NDJSON lines, resets an idle timer per chunk, and cancels the request
   context when a limit fires, returning a typed error
   (`*TimeoutError{Limit, Model, After}`) so callers can log which limit
   fired. Overall timeout moves from `http.Client.Timeout` to a context
   deadline so all three limits report through the same path. OpenAI path
   keeps `post` with the overall timeout.
2. **Tests** (`shared/llm/llm_test.go`, `httptest` server writing NDJSON
   with controlled delays, all limits in milliseconds): content
   concatenation; thinking-only chunks count as progress; tool_calls
   accumulated; each of the three limits fires with its own error; 404
   error text unchanged; ctx cancel aborts. Update the two existing
   default-timeout tests to the new defaults.
3. **phraseforge**: config fields + defaults, thread into `llm.Config` in
   `ai.go`, log `limit=`; raise purpose defaults to 1800 in `config.go` and
   `k8s/configmap.yaml`.
4. **knowledge**: no change; build and run its tests to confirm the
   defaults apply.
5. **Verify**: lab deploy, one real translation job completes; then a
   short manual run against prod Ollama with `IdleTimeout` tiny to see the
   idle limit fire, bounded to seconds.

## Affected Areas

- `shared/llm/llm.go`, `shared/llm/llm_test.go`
- `phraseforge/internal/config/config.go`, `config_test.go`
- `phraseforge/internal/ai/ai.go`
- `phraseforge/k8s/configmap.yaml`
- knowledge (behavior only, via `shared/llm` defaults — no files changed)

## Out of Scope

- Knowledge provider registry and its streaming-timeout config keys
  (`knowledge-llm-providers-config`, later).
- Streaming tokens through to the browser (UI still waits for the final
  result).
- The OpenAI/OpenRouter streaming (SSE) path.
- Prod rollout: the prod configmap and image update for `jdpct101` are a
  separate, explicitly approved step.
- The stale `rinex20/translategemma3:12b` transcription rule in prod
  Admin > LLM (a data fix in the UI, not code).
- Ollama CPU limit / keep-alive tuning.

## Implementation Notes

1. `shared/llm/llm.go`: `Config` gains `FirstTokenTimeout`/`IdleTimeout`;
   defaults 300 s / 60 s / overall 1800 s. The Ollama path sends
   `"stream": true` and reads NDJSON in `ollamaStream`: a
   `context.WithCancelCause` context, an overall `time.AfterFunc`, and one
   progress timer that fires as `first_token` until the first chunk with
   `content`/`thinking`/`tool_calls`, then is reset to `IdleTimeout` on each
   such chunk (reported as `idle`). The firing limit is returned as
   `*TimeoutError{Limit, Model, After}` via `context.Cause`; a caller's own
   cancel is returned unchanged. `post` and the new streaming path share a
   `do` helper, so non-2xx errors keep their text. OpenAI path unchanged.
2. `shared/llm/llm_test.go`: 15 tests (content concatenation, `stream:true`
   sent, tool-call accumulation, slow-but-steady success, thinking counts as
   progress, each of the three limits, 404 text, mid-stream error, stream
   ended without `done`, caller cancel not reported as a limit). Pass under
   `-race -count=3`.
3. phraseforge: `ProviderConfig` gains `FirstTokenTimeoutSeconds`/
   `IdleTimeoutSeconds` (yaml `providers.ollama.*`, defaults 300/60); all
   seven purpose `timeoutSeconds` defaults → 1800 in `config.go` and
   `k8s/configmap.yaml`; `ai.go` threads them into `llm.Config` and logs
   `limit=`. `config_test.go` updated for the new defaults plus
   provider-limit default and override assertions.
4. knowledge: no production code change. Its translate test stub returned a
   single object without `"done": true` (which real Ollama always sends);
   added `"done": true` to the stub (`internal/translate/translate_test.go`).

## Validation

- `go build`/`go vet`/`go test ./...`: `shared`, `phraseforge`, `knowledge`
  all pass (baseline was green; the only interim failures were the
  expected old-default assertions and the knowledge stub, both fixed).
- Real client against prod Ollama (`jdpct101`, port-forward, 2026-09-30):
  normal call → `"Hello, I am here now."` in 2.7 s; `IdleTimeout` 100 ms →
  `model gemma4:12b stalled: no output for 0.1s` after 0.6 s.
- Lab: `task deploy-phraseforge` rolled out; startup log clean. In-app
  translation job completed successfully (user-confirmed 2026-09-30).
- Prod config: `jdp-helm/jdp-frontend` values + template updated (provider
  first-token/idle keys, `timeoutSeconds: 1800`); `helm lint` and
  `helm template` render verified. Image bump left to the user.

## Documentation Review

`shared/llm` fans out to phraseforge and knowledge (tech-stack `Built
from:` note); both are user-facing behavior changes.

| Changelog | Category | Entry |
|---|---|---|
| `phraseforge/CHANGELOG.md` | Changed | Ollama calls stream with first-token/idle limits; `timeoutSeconds` becomes an overall backstop (default 1800); `limit=` in the log. |
| `knowledge/CHANGELOG.md` | Changed | Ollama calls stream with 300s/60s/30min limits, replacing the fixed 2-minute timeout; not configurable yet. |

Doc drift: `phraseforge/README.md` "LLM configuration" didn't document any
timeouts; `knowledge/README.md` "Ollama notes" didn't mention limits.
Constitution: no drift (`tech-stack.md` describes `shared/llm` only as an
abstraction layer).

## Documentation Updates

- `phraseforge/CHANGELOG.md`, `knowledge/CHANGELOG.md` `[Unreleased] /
  Changed`: entries above.
- `phraseforge/README.md`: provider first-token/idle keys and purpose
  `timeoutSeconds` documented; example config updated.
- `knowledge/README.md` "Ollama notes": built-in streaming limits.
- `## Now` line removed from `specs/artifacts/phraseforge/roadmap.md` and
  `specs/artifacts/knowledge/roadmap.md`; knowledge `## Next` gains
  `knowledge-llm-providers-config`.
- Outside this repo: `jdp-helm/jdp-frontend` values, template, and
  CHANGELOG (see Validation).
