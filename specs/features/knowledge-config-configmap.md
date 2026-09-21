---
title: Move knowledge app config to a mounted ConfigMap YAML file
kind: feature
status: done
version: 1
updated: 2026-09-21
branch: main
---

## Problem / Motivation

First step of a larger, explicitly-discussed plan (URL/file ingestion,
translation, async status, YAML export/import — each its own follow-up
spec). All of them need new, sometimes multi-line configuration (target
language, translation model/prompt, chunk sizing). Multi-line prompt text
is awkward as an environment variable value but natural in YAML. Per user
decision, this migration also covers *today's* existing settings
(`CHAT_MODEL`, `GENERATE_MODEL`, `EMBEDDINGS_*`, `SEARCH_MIN_SCORE`, etc.),
not just the new ones — replacing plain `env()`/`envInt()`/`envFloat()`
reads with a single mounted ConfigMap YAML file, read once at startup.

**Security constraint, not optional:** credentials never belong in a
ConfigMap (unencrypted, readable by anyone with namespace read access).
`PGUser`/`PGPassword` are already Kubernetes-Secret-sourced env vars today
(`postgres-credentials`) — they stay exactly as they are. The `*APIKey`
fields (`EMBEDDINGS_API_KEY`, `CHAT_API_KEY`, `GENERATE_API_KEY` — all
empty today, relevant only if a future deployment uses OpenAI/OpenRouter)
also stay as plain env vars for the same reason. Everything else migrates.

## Acceptance Criteria

- [x] `knowledge/internal/config.Config` keeps its current flat field names
      and shape unchanged — every other package (`chat`, `generate`,
      `qdrant`, `embeddings`, `server`) continues to reference
      `cfg.ChatModel` etc. exactly as today, with zero changes to those
      packages.
- [x] `config.Load()` reads a YAML file (path from `CONFIG_FILE` env var,
      default `/etc/knowledge/config.yaml`) into a nested structure
      matching the file's shape, layered over built-in defaults (today's
      existing fallback values) so any field the file omits keeps its
      default — the file need not be complete.
- [x] A missing config file is not an error (falls back to defaults —
      keeps `go run .` working locally without a cluster). A *malformed*
      file (YAML parse error) is an error — `Load()`'s signature changes to
      `(Config, error)`, and `main.go` fails fast on it rather than masking
      a real misconfiguration with silent defaults.
- [x] `PGUser`/`PGPassword`/`EmbeddingsAPIKey`/`ChatAPIKey`/`GenerateAPIKey`
      remain plain env vars (Secret-sourced where applicable), never read
      from the YAML file.
- [x] Every other field (`BindAddr`, `QdrantURL`, `QdrantCollection`,
      `SearchMinScore`, `PGHost`, `PGPort`, `PGDatabase`, `Embeddings{
      Provider,BaseURL,Model,Dimension}`, `Chat{Provider,BaseURL,Model,
      NumCtx}`, `Generate{Provider,BaseURL,Model,NumCtx}`) comes from the
      YAML file.
- [x] A new `knowledge/k8s/configmap.yaml` defines the ConfigMap, mounted
      into both the `knowledge` Deployment and the `knowledge-migrate` Job
      at `/etc/knowledge/config.yaml`, carrying today's actual deployed
      values (the ones currently inline in `deployment.yaml`/
      `migrate-job.yaml`'s plain-`value` env entries).
- [x] `deployment.yaml` and `migrate-job.yaml` lose the now-migrated plain
      `value` env entries; `PGUSER`/`PGPASSWORD` `secretKeyRef` entries are
      untouched.
- [x] `Taskfile.yml`'s `deploy-knowledge` applies `configmap.yaml` before
      `_migrate-db` runs (today's sequence runs the migrate Job *before*
      `deployment.yaml` is applied, so the ConfigMap must exist
      independently of that, not bundled inside `deployment.yaml`).
- [x] The app builds, deploys, and behaves identically to today (same
      model, same thresholds) — validated live, not just by build success.

## Approach

1. **`knowledge/go.mod`**: add `gopkg.in/yaml.v3` as a direct dependency
   (already transitively present in the workspace via `phraseforge`;
   well-established, Go-ecosystem-standard YAML library — low risk).
2. **`knowledge/internal/config/config.go`**:
   - Add an unexported `fileConfig` struct mirroring the YAML shape
     (nested: `qdrant`, `postgres`, `embeddings`, `chat`, `generate`), and
     a `defaultFileConfig()` returning it pre-filled with today's
     fallback values.
   - `Load()` becomes `func Load() (Config, error)`: build
     `defaultFileConfig()`, `os.ReadFile` the path from
     `env("CONFIG_FILE", "/etc/knowledge/config.yaml")`; on
     `os.IsNotExist`, keep defaults; on any other read error or a
     `yaml.Unmarshal` error, return it. `yaml.Unmarshal` decodes *into*
     the already-defaulted struct, so only fields present in the file
     override their default (standard `yaml.v3`/`encoding/json` behavior
     — never resets unset fields to zero).
   - Flatten the resulting `fileConfig` into today's flat `Config`
     struct; `PGUser`/`PGPassword`/`*APIKey` still come from `env()`
     directly, as today.
   - Delete `envInt`/`envFloat` — after this change they have no
     remaining callers.
3. **`knowledge/main.go`**: `cfg := config.Load()` → `cfg, err :=
   config.Load(); if err != nil { log.Fatal(err) }`. No other change —
   every downstream constructor (`chat.New(cfg)`, `generate.New(cfg)`,
   `qdrant.New(cfg.QdrantURL, ...)`) is untouched.
4. **`knowledge/k8s/configmap.yaml`** (new): a `ConfigMap` named
   `knowledge-config` in namespace `app`, `data["config.yaml"]` containing
   today's actual cluster values (mirroring current `deployment.yaml`/
   `migrate-job.yaml` values: `bge-m3`/1024-dim embeddings via
   `host.docker.internal`, `gemma4:12b` chat/generate with `numCtx: 8192`
   for chat, `searchMinScore: 0.4`, etc.), carrying over the existing
   explanatory comments (e.g. the bge-m3-dimension verification note).
5. **`knowledge/k8s/deployment.yaml`**: remove the now-migrated plain-value
   env entries; add a `configMap` volume (source: `knowledge-config`) and
   a `volumeMount` at `/etc/knowledge`. `PGUSER`/`PGPASSWORD` unchanged.
6. **`knowledge/k8s/jobs/migrate-job.yaml`**: same volume/mount addition;
   remove its now-migrated plain-value env entries (`QDRANT_URL`,
   `EMBEDDINGS_*`); `PGUSER`/`PGPASSWORD`/`PGHOST`/`PGPORT`/`PGDATABASE`
   handling stays consistent with the Deployment (host/port/database now
   from the mounted file, matching step 2/5's flattening).
7. **`Taskfile.yml`**: in `deploy-knowledge`, insert
   `'{{.KUBECTL}} apply -f knowledge/k8s/configmap.yaml'` right after
   `_ensure-db-secret` and before `_migrate-db` (so the migrate Job, which
   runs before `deployment.yaml` is applied in this same task, still has
   the ConfigMap it needs). Add the matching `delete -f
   knowledge/k8s/configmap.yaml --ignore-not-found` to `delete-knowledge`
   for symmetry.

## Affected Areas

- `knowledge/go.mod` / `go.sum`
- `knowledge/internal/config/config.go`
- `knowledge/main.go`
- `knowledge/k8s/configmap.yaml` (new)
- `knowledge/k8s/deployment.yaml`
- `knowledge/k8s/jobs/migrate-job.yaml`
- `Taskfile.yml`

## Out of Scope

- Any new ingest/translate/chunking config fields — those belong to their
  own later specs, which will each add their own section to this same
  file/struct.
- Hot-reloading the config file without a pod restart — read once at
  startup, matching today's env-var behavior (also read once at startup).
- Moving `PGUser`/`PGPassword`/`*APIKey` anywhere — they stay exactly as
  today, for the security reason stated above.
- Changing any *value* of an existing setting (model names, thresholds,
  etc.) — this is a mechanism change only; behavior stays identical.

## Implementation Notes

Implemented exactly per Approach:

- `knowledge/go.mod`: added `gopkg.in/yaml.v3` as a direct dependency
  (`go get` + `go mod tidy`; pulled in two small transitive test-utility
  deps of yaml.v3 itself, expected).
- `knowledge/internal/config/config.go`: rewritten per the design — an
  unexported `fileConfig` + `defaultFileConfig()`, `Load()` now
  `(Config, error)`, `env()` kept (still used for credentials),
  `envInt`/`envFloat` deleted (no remaining callers after the migration).
  `Config`'s exported shape is byte-for-byte unchanged.
- `knowledge/main.go`: two-line change at the `config.Load()` call site to
  handle the new error return; nothing else touched.
- `knowledge/k8s/configmap.yaml` (new): the `ConfigMap`, carrying today's
  actual cluster values, comments included.
- `knowledge/k8s/deployment.yaml` / `knowledge/k8s/jobs/migrate-job.yaml`:
  removed the migrated plain-`value` env entries, added the volume/mount;
  `PGUSER`/`PGPASSWORD` `secretKeyRef` entries untouched in both.
- `Taskfile.yml`: added the `configmap.yaml` apply step to `deploy-knowledge`
  (before `_migrate-db`) and the matching delete step to `delete-knowledge`.
  (Note: `Taskfile.yml` had unrelated pre-existing uncommitted changes from
  earlier work; this edit targeted only the `deploy-knowledge`/
  `delete-knowledge` task bodies and didn't touch anything else in the
  file.)
- Added `knowledge/internal/config/config_test.go` — this specific change
  is pure logic with zero external dependencies (no LLM/Qdrant/Postgres
  involved), unlike most of this session's other work, so a real unit test
  was the right tool here rather than live validation alone. Four tests:
  missing file → defaults, file overrides only what it sets, malformed
  file → error, credentials never read from the file even when a
  non-secret field in the same file is set.

## Validation

`go build`/`vet`/`gofmt` clean; `go test ./...` passes, including the four
new tests in `internal/config`. `kubectl apply --dry-run=client` validated
all three edited/new k8s manifests before a real deploy. `task
--list-all` confirmed the Taskfile still parses after the edit.

Full live deploy via `task deploy-knowledge` (the real bootstrap path,
which exercises the fixed ordering — ConfigMap applied before the migrate
Job runs): migrate Job's own log confirmed it read the ConfigMap correctly
(`postgres schema and collection "knowledge" ready for bge-m3 (1024
dimensions)`); the running pod's startup log showed every value read
correctly (`embeddings=ollama/bge-m3 dim=1024 chat=ollama/gemma4:12b
generate=ollama/gemma4:12b`); `/health` and `/ready` both `200`. Live
functional test after the migration: search for a real query returned the
correct item at a normal score (0.645); a live chat message retrieved the
correct source (0.699) and answered correctly — full behavioral parity
with pre-migration, not just a successful build/startup.

## Documentation Review

Checked `knowledge/README.md` thoroughly given how much of its
`## Configuration` section this change invalidates. Found and fixed:
- The whole env-var table, replaced with the YAML shape + the
  credentials-stay-as-env-vars callout.
- The architecture tree listing (`k8s/` didn't mention the new
  `configmap.yaml`; also found and fixed a pre-existing gap from an
  earlier spec — `internal/generate/generate.go` was never added there).
- The Ollama-notes section's "edit `EMBEDDINGS_BASE_URL` in
  deployment.yaml/migrate-job.yaml" pointer — now points at
  `configmap.yaml`'s `baseURL` fields.
- The OpenAI/OpenRouter-compatible-embeddings examples — rewritten as
  YAML config + a separate `EMBEDDINGS_API_KEY` env line (the credential
  half stays an env var).
- Two remaining prose references to `SEARCH_MIN_SCORE`/`GENERATE_MODEL`/
  `CHAT_MODEL` as if they were still env vars.
No constitution-file drift.

## Documentation Updates

`knowledge/README.md`: all of the above. No other docs reference this
app's configuration mechanism.
