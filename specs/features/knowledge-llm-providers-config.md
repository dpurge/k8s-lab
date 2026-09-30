---
title: Phraseforge-style LLM provider registry and per-purpose config for knowledge
kind: feature
status: done
version: 1
updated: 2026-09-30
branch: main
---

## Problem / Motivation

Knowledge configures each LLM use separately: `embeddings`, `chat`,
`generate`, and `translate` each carry their own `provider` + `baseURL`
(four copies of the same Ollama URL in `knowledge/k8s/configmap.yaml` and in
`jdp-helm/jdp-frontend`), API keys are per purpose (`CHAT_API_KEY`,
`GENERATE_API_KEY`, `TRANSLATE_API_KEY`, `EMBEDDINGS_API_KEY`), prompts live
in a separate `prompts:` block, and there is no way to set think or a
timeout. Since `llm-streaming-progress-timeout` knowledge streams Ollama
calls, but only with `shared/llm`'s built-in limits (300 s / 60 s /
1800 s) because it has nowhere to configure them. Phraseforge already has
the shape knowledge should have: a `providers:` registry (connection,
per-provider API key from env, streaming limits) and per-purpose
`provider`/`model`/`numCtx`/`think`/`timeoutSeconds`/`prompt`.

User decisions (2026-09-30):

- An old-shape config (per-section `baseURL`, `generate:`, `prompts:`) is a
  startup error with a message saying where the value moved — never a
  silent fallback to the localhost default.
- API keys are per provider (`OLLAMA_API_KEY`, `OPENROUTER_API_KEY`,
  `OPENAI_API_KEY`), like phraseforge; the four per-purpose variables go.
- Title and summary generation become two purposes, `generateTitle` and
  `generateSummary`, each with its own prompt (one prompt per purpose).
- Embeddings use the registry too.

Assumptions (confirmed at approval, 2026-09-30):

- Registry keys: `ollama`, `openrouter`, `openai` (knowledge already
  supports the OpenAI-compatible API for chat and embeddings). Defaults:
  `http://localhost:11434`, `https://openrouter.ai/api/v1`,
  `https://api.openai.com/v1`; Ollama streaming limits 300/60.
- A per-purpose API-key env var that is still set (e.g. `CHAT_API_KEY`) is
  also a startup error naming its replacement, so a key isn't silently
  dropped.
- Admin-editable DB overrides (phraseforge's Admin > LLM) are not part of
  this; config stays file-based.

## Acceptance Criteria

- New `config.yaml` shape (defaults shown):

  ```yaml
  providers:
    ollama:
      baseURL: http://localhost:11434
      firstTokenTimeoutSeconds: 300
      idleTimeoutSeconds: 60
    openrouter:
      baseURL: https://openrouter.ai/api/v1
    openai:
      baseURL: https://api.openai.com/v1
  embeddings:
    provider: ollama        # a registry key, or "fake"
    model: bge-m3
    dimension: 1024
  chat:
    provider: ollama
    model: gemma4:12b
    numCtx: 8192
    think: false
    timeoutSeconds: 1800
    prompt: ...             # today's prompts.chat
  generateTitle:   { provider, model, numCtx, think, timeoutSeconds, prompt }
  generateSummary: { provider, model, numCtx, think, timeoutSeconds, prompt }
  translate:       { provider, model, numCtx, think, timeoutSeconds, prompt }
  knowledgeLanguage: English
  qdrant: { collection, searchMinScore }   # unchanged
  bindAddr: ...                            # unchanged
  ```

- Defaults preserve today's behavior: same models, `chat.numCtx` 8192,
  same prompt texts, think false; `timeoutSeconds` 1800 everywhere (the
  overall backstop, as in phraseforge).
- Each chat purpose's LLM client gets its provider's base URL, API key, and
  first-token/idle limits, plus its own model, numCtx, think, and timeout.
- `Load` fails with an actionable error when: a purpose or `embeddings`
  names a provider not in the registry (except `embeddings.provider:
  fake`); the file has a removed key (`embeddings.baseURL`,
  `chat.baseURL`, `translate.baseURL`, `generate`, `prompts`) — e.g.
  `chat.baseURL is no longer supported; set providers.ollama.baseURL`; or
  a removed env var is set (`CHAT_API_KEY` → `OLLAMA_API_KEY`/
  `OPENROUTER_API_KEY`/`OPENAI_API_KEY`).
- `generate` title and summary calls use their own purpose's client and
  prompt.
- The startup log line reports each purpose's provider/model.
- `knowledge/k8s/configmap.yaml` and prod `jdp-helm/jdp-frontend`
  (knowledge template + values) use the new shape; lab deploy starts and
  chat, title, summary, translate, and embeddings all still work.

## Approach

1. **Config** (`knowledge/internal/config`): new types `ProviderConfig`
   (BaseURL, APIKey, FirstTokenTimeoutSeconds, IdleTimeoutSeconds),
   `PurposeConfig` (Provider, Model, NumCtx, Think, TimeoutSeconds,
   Prompt), `EmbeddingsConfig` (Provider, Model, Dimension);
   `Config.Providers map[string]ProviderConfig`, `Config.Chat/
   GenerateTitle/GenerateSummary/Translate PurposeConfig`,
   `Config.Embeddings`. Removed-key detection by decoding the file once
   into `map[string]any`; removed env vars checked in `Load`. A method
   `Config.LLM(p PurposeConfig) llm.Config` builds a client config (safe
   after `Load` validated the provider). Tests: defaults, file override,
   each removed key/env var error, unknown provider, `LLM()` mapping.
2. **Callers**: `chat.go`, `generate.go` (two clients: title, summary),
   `translate.go` use `cfg.LLM(...)`; `main.go` builds embeddings from
   `cfg.Providers[cfg.Embeddings.Provider]` and logs purposes. Update
   existing tests (`translate_test.go`) to the new config fields.
3. **Lab config**: rewrite `knowledge/k8s/configmap.yaml`; deploy
   (`task deploy-knowledge`), check the startup log, run one chat, one
   title/summary generate, one translate, and a search (embeddings).
4. **Prod config**: `jdp-helm/jdp-frontend` knowledge template + values in
   the new shape (values.yaml is CRLF — preserve it); `helm lint`; render
   the ConfigMap and load it through `config.Load`.
5. **Docs**: knowledge README config section, CHANGELOGs (knowledge,
   jdp-frontend).

## Affected Areas

- `knowledge/internal/config/config.go`, `config_test.go`
- `knowledge/internal/chat/chat.go`, `knowledge/internal/generate/generate.go`,
  `knowledge/internal/translate/translate.go`, `translate_test.go`
- `knowledge/main.go`
- `knowledge/k8s/configmap.yaml`, `knowledge/README.md`,
  `knowledge/CHANGELOG.md`
- `jdp-helm/jdp-frontend/templates/knowledge.yaml`, `values.yaml`,
  `CHANGELOG.md`

## Out of Scope

- Admin UI / DB-stored prompt or model overrides for knowledge.
- Changing any model, prompt text, or default behavior.
- Phraseforge config (already in this shape).
- `shared/llm` changes (already supports everything needed).

## Implementation Notes

1. Config (`knowledge/internal/config/config.go`): `ProviderConfig`,
   `PurposeConfig`, `EmbeddingsConfig`; `Config.Providers` (fixed
   `ollama`/`openrouter`/`openai` file structs so a partial file keeps the
   other defaults), `Chat`/`GenerateTitle`/`GenerateSummary`/`Translate`,
   `Embeddings`. `Config.LLM(p)` builds the `llm.Config`;
   `Config.EmbeddingsConnection()` maps the provider to embeddings' API
   style (`openrouter`/`openai` → `openai`). `Load` rejects removed keys
   (raw-map check), removed env vars, and unknown providers, all problems
   joined into one error in a fixed order. Default prompts/models copied
   verbatim from the old defaults. 13 tests incl.
   `TestConfigMapLoadsAndMatchesDefaults` (lab ConfigMap loads and matches
   the defaults' prompts/models/numCtx/provider/timeout).
2. Callers: `chat.go`/`translate.go` use `llm.New(cfg.LLM(...))`;
   `generate.go` holds two `purposeClient`s (title, summary: client, model,
   prompt); `main.go` uses `EmbeddingsConnection` and logs every purpose.
   `translate_test.go` updated to the new fields. Pre-existing, untouched:
   `internal/server/server.go` fails `gofmt -l` at HEAD too.
3. `knowledge/k8s/configmap.yaml` rewritten in the new shape (same models,
   numCtx, prompt texts, comments).
4. Prod: `jdp-helm/jdp-frontend` knowledge template + values
   (`providers.ollama.{baseUrl,firstTokenTimeoutSeconds,idleTimeoutSeconds}`,
   per-purpose `provider`/`model`/`timeoutSeconds`, chat `numCtx`; prompts
   left to the image defaults as before); values.yaml CRLF preserved.

## Validation

- `go build`/`go vet`/`go test ./...` (knowledge) pass; baseline was green.
- Lab: `task deploy-knowledge` rolled out; startup log `embeddings=ollama/
  bge-m3 dim=1024 chat=ollama/gemma4:12b generateTitle=ollama/gemma4:12b
  generateSummary=ollama/gemma4:12b translate=ollama/gemma4:12b`.
- Prod chart: `helm lint` passes; the rendered ConfigMap loaded through
  `config.Load` gives the prod Ollama URL, limits 300/60, the same models
  and chat numCtx 8192, timeouts 1800, prompts equal to the defaults; the
  knowledge Deployment sets none of the removed API-key env vars.
- User UI check in the lab (2026-09-30): generate title and summary work;
  a chat answer arrived (slowly). Lab `operations` rows in the last 2 h:
  `chat_reply` 1, `generate_title` 2, `generate_summary` 2, all `done`.
  Not exercised: translate (only runs during ingest; `jobs` has 0 rows, so
  no ingest ran) and a standalone search. Chat retrieval embeds the
  question, so embeddings are exercised indirectly (Medium confidence).
- User noticed the Jobs page shows none of these: pre-existing design —
  it lists only ingest `jobs` rows, while chat/generate run as queue
  `operations` (`internal/queue`); not caused by this feature.

## Documentation Review

| Changelog | Category | Entry |
|---|---|---|
| `knowledge/CHANGELOG.md` | Changed | Breaking config change to the provider-registry shape, per-provider API keys, old shape/env vars rejected. |
| `knowledge/CHANGELOG.md` | Changed | Corrected today's streaming entry ("Not configurable yet" → where the limits are configured). |
| `jdp-helm/jdp-frontend/CHANGELOG.md` | Changed | Knowledge config moved to the new shape; needs the new image. |

Doc drift: `knowledge/README.md` Configuration, Ollama notes, and
OpenAI/OpenRouter embeddings sections described the old shape; the example
and `ollama pull` also still said `nomic-embed-text`/768 while defaults are
`bge-m3`/1024. Constitution: no drift.

## Documentation Updates

- `knowledge/README.md`: Configuration block and credentials paragraph
  rewritten; Ollama notes (`bge-m3`, `providers.ollama.baseURL`, streaming
  limits); OpenAI/OpenRouter embeddings examples.
- `knowledge/CHANGELOG.md`, `jdp-helm/jdp-frontend/CHANGELOG.md`: entries
  above.
- `specs/artifacts/knowledge/roadmap.md`: `## Now` line removed.
