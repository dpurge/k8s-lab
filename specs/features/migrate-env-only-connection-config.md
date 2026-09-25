---
title: Read Postgres/Qdrant connection config from env vars, not the mounted file
kind: bugfix
status: done
version: 1
updated: 2026-09-25
branch: main
---

## Problem / Motivation

`phraseforge`'s and `knowledge`'s `migrate` subcommand needs a mounted
config file to reach the real Postgres (and, for `knowledge`, Qdrant)
service, because `internal/config/config.go`'s `Load()` reads
`PGHost`/`PGPort`/`PGDatabase` (and, for `knowledge`, `QdrantURL`) only from
that file — never from env vars, unlike `PGUser`/`PGPassword`, which are
already env-only (see `Load()` in both apps).

In the deploying Helm chart (`jdp-helm`), the `migrate` Job runs as a
`pre-install,pre-upgrade` hook, which executes before the chart's normal
(non-hook) resources — including the ConfigMap that carries this file —
exist. Making that ConfigMap itself a hook works but pulls the app's whole
persistent config into hook churn for the sake of a few connection fields.
The chart-side fix chosen instead: stop needing the file at all for these
fields, by moving them into env vars sourced from the same
`ExternalSecret`-backed Secret that already supplies `PGUSER`/`PGPASSWORD`
— a mechanism that already works correctly for the `migrate` hook today.

This spec covers the app-source half of that fix only. The Helm chart
changes (Secret/ConfigMap/Job/Deployment wiring) are tracked separately in
`jdp-helm`, not here.

Confirmed by reading `db.Migrate`/`main.go` in both apps
(`phraseforge/internal/db/migrate.go`, `knowledge/internal/db/db.go`,
`knowledge/main.go`): `phraseforge migrate` only ever touches
Postgres fields. `knowledge migrate` additionally constructs a Qdrant
client and calls `EnsureCollection` (`kb.EnsureCollection(ctx)` in
`main.go`) — this needs `QdrantURL` and `QdrantCollection` to be correct,
and needs `EmbeddingsProvider`/`EmbeddingsDimension` to be non-empty/valid
(used to build the collection's vector size and to construct the embedder
via `embeddings.New`), but never calls `Embed()`, so
`EmbeddingsBaseURL`/`EmbeddingsModel`/`EmbeddingsAPIKey` are functionally
irrelevant to `migrate`'s success. `defaultFileConfig()`'s built-in Go
defaults for `Qdrant.Collection` (`"knowledge"`), `Embeddings.Provider`
(`"ollama"`) and `Embeddings.Dimension` (`1024`) already match every
deployment's actual values today, so those three do **not** need an env
override — only `QdrantURL` does, because its default
(`"http://localhost:6333"`) does not resolve inside a pod.

## Acceptance Criteria

- [ ] `phraseforge/internal/config/config.go`: `PGHost`, `PGPort`,
  `PGDatabase` are read via `env(...)` with the same fallback values
  `defaultFileConfig()` used to provide (`"localhost"`, `"5432"`,
  `"phraseforge"`), exactly mirroring how `PGUser`/`PGPassword` are already
  read. The `postgres:` block is removed from `fileConfig` (no file field
  reads it anymore).
- [ ] `knowledge/internal/config/config.go`: same treatment for
  `PGHost`/`PGPort`/`PGDatabase` (fallbacks `"localhost"`, `"5432"`,
  `"knowledge"`), **plus** `QdrantURL` read via `env("QDRANT_URL",
  "http://localhost:6333")`. The `postgres:` block and the `Qdrant.URL`
  field are removed from `fileConfig`; `Qdrant.Collection` and
  `Qdrant.SearchMinScore` stay file-configured (unchanged — `migrate`
  tolerates their defaults, as established above).
- [ ] Env var names are exactly `PGHOST`, `PGPORT`, `PGDATABASE` (both
  apps) and `QDRANT_URL` (knowledge only) — matching the existing
  `PGUSER`/`PGPASSWORD` naming convention, since the Helm chart's Secret
  wiring (tracked separately) will set env vars by these exact names.
- [ ] `serve` (the normal HTTP server path) picks up the same env-sourced
  values automatically, since it shares `config.Load()` with `migrate` —
  no separate code path.
- [ ] Existing tests in both `internal/config/config_test.go` files are
  updated: any case asserting `PGHost`/`PGPort`/`PGDatabase`/`QdrantURL`
  come from the file must instead assert they come from env (mirroring the
  existing `PGUser`/`PGPassword` env-override test cases), and any case
  asserting the *default* (file absent, env absent) must assert the new
  hardcoded fallback rather than `fileConfig`'s old default.
- [ ] `go test ./...` passes in both `phraseforge/` and `knowledge/`.

## Approach

1. In each app's `config.go`, delete the `Postgres` field from
   `fileConfig` (and, for `knowledge`, `Qdrant.URL` — keep
   `Qdrant.Collection`/`Qdrant.SearchMinScore`).
2. In each app's `Load()`, replace `fc.Postgres.Host`/`.Port`/`.Database`
   (and, for knowledge, `fc.Qdrant.URL`) with `env("PGHOST", "localhost")`
   / `env("PGPORT", "5432")` / `env("PGDATABASE", "<app default>")` (and
   `env("QDRANT_URL", "http://localhost:6333")`), placed alongside the
   existing `PGUser`/`PGPassword` env reads for consistency.
3. Update `defaultFileConfig()` to drop the now-removed fields; update
   `config_test.go` in both apps per the Acceptance Criteria above.
4. Run `go test ./...` in both `phraseforge/` and `knowledge/`.
5. No Dockerfile, `main.go` control-flow, or k8s manifest changes in this
   repo — `phraseforge/k8s/*`, `knowledge/k8s/*` are dev-only manifests;
   update them too for consistency (drop `postgres:`/`qdrant.url` from
   `configmap.yaml`, add `PGHOST`/`PGPORT`/`PGDATABASE`/`QDRANT_URL` env
   vars to `deployment.yaml`/`jobs/migrate-job.yaml`) so local `k3d` dev
   stays representative, but this is not required for the fix itself.

## Affected Areas

- `phraseforge/internal/config/config.go`, `config_test.go`
- `knowledge/internal/config/config.go`, `config_test.go`
- `phraseforge/k8s/configmap.yaml`, `deployment.yaml`,
  `jobs/migrate-job.yaml` (dev-manifest parity, optional per step 5)
- `knowledge/k8s/configmap.yaml`, `deployment.yaml`,
  `jobs/migrate-job.yaml` (dev-manifest parity, optional per step 5)

## Out of Scope

- Any change to `main.go`'s control flow in either app (e.g. not
  restructuring `knowledge`'s eager Qdrant/embeddings client construction
  before checking the subcommand).
- Any Helm chart change in `jdp-helm` — tracked separately there.
- Bumping either app's version/changelog for this fix specifically — do
  that as part of whatever release includes it.
- Making `Qdrant.Collection`/`Qdrant.SearchMinScore`/embeddings fields
  env-configurable — their current file/default behavior already satisfies
  every real deployment.

## Implementation Notes

1. `phraseforge/internal/config/config.go`: removed the `Postgres` block
   from `fileConfig` and `defaultFileConfig()`; `Load()` now reads
   `PGHost`/`PGPort`/`PGDatabase` via `env("PGHOST"/"PGPORT"/"PGDATABASE",
   <same defaults>)`, alongside the existing `PGUser`/`PGPassword` reads.
2. `knowledge/internal/config/config.go`: same treatment, plus removed
   `Qdrant.URL` from `fileConfig`/`defaultFileConfig()`; `Load()` reads
   `QdrantURL` via `env("QDRANT_URL", "http://localhost:6333")`.
   `Qdrant.Collection`/`SearchMinScore` stay file-configured, unchanged.
3. Updated `config_test.go` in both apps: default-fallback assertions for
   the newly env-only fields, a dedicated env-override test for each
   (`TestLoadEnvOverridesConnectionFields` in phraseforge), and rewrote the
   file-vs-env precedence tests (`TestLoadEnvOverridesOnTopOfFile` in
   phraseforge, `TestLoadNeverReadsCredentialsOrConnectionFieldsFromFile` in
   knowledge) to use a still-file-sourced field for the "file wins" side,
   since Postgres/Qdrant-URL fields no longer exist in `fileConfig` at all.
4. Dev-manifest parity (optional step 5, done for consistency): removed
   `postgres:`/`qdrant.url` from both apps' `k8s/configmap.yaml`; added
   `PGHOST`/`PGPORT`/`PGDATABASE` (and knowledge's `QDRANT_URL`) as plain
   env vars to both apps' `k8s/deployment.yaml` and
   `k8s/jobs/migrate-job.yaml`, using the same values the ConfigMap used to
   carry.
5. Self-reviewed the diff with the `code-review` skill — no findings; the
   change is a mechanical, conventions-consistent field migration with
   matching test coverage.
6. Appended a `specs/memory.md` `[convention]` entry recording the new
   env-only fields and marking the 2026-09-24T17:52:29Z `[gotcha]` entry
   (the migrate Job connection-refused failure this change fixes) as
   superseded, for the next memory maintenance pass to purge.

## Validation

- `go build ./...` and `go test ./...` pass in both `phraseforge/` and
  `knowledge/` (baseline captured before implementation was already green;
  no regressions after).
- `go test ./internal/config/... -v` passes in both apps: 7 tests in
  phraseforge (2 new), 4 in knowledge (1 rewritten, default-fallback
  assertions added to `TestLoadDefaultsWhenFileMissing`).
- `gofmt -l` reports no formatting issues on the changed Go files.
- All six edited `k8s/*.yaml` manifests parsed successfully with
  `yaml.safe_load_all` (Python) — syntax-valid.
- Real-cluster confirmation: `task deploy-phraseforge` against the local
  k3d cluster — `phraseforge-migrate` Job completed successfully reading
  `PGHOST`/`PGPORT`/`PGDATABASE` from env (Secret/plain env vars, not the
  ConfigMap), and the Deployment rolled out and became ready.

## Documentation Review

Affected Areas map to two artifacts (`phraseforge`, `knowledge`, per
`tech-stack.md`'s Artifacts table) — no `Built from:` fan-out applies since
neither app's config package is under `shared/`.

**Changelog entries needed** (user-facing config-surface change, category
`Fixed` — matches the existing `### Fixed` section's style in both files):

- `{phraseforge/CHANGELOG.md, Fixed, "the migrate command (and serve) now read PGHost/PGPort/PGDatabase from environment variables (PGHOST/PGPORT/PGDATABASE) instead of the mounted config file, so a chart's pre-install/pre-upgrade migrate hook no longer needs the app's ConfigMap to exist first."}`
- `{knowledge/CHANGELOG.md, Fixed, "the migrate command (and serve) now read PGHost/PGPort/PGDatabase and QdrantURL from environment variables (PGHOST/PGPORT/PGDATABASE/QDRANT_URL) instead of the mounted config file, so a chart's pre-install/pre-upgrade migrate hook no longer needs the app's ConfigMap to exist first."}`

**Other drift found:**

- `knowledge/README.md`'s `## Configuration` section (lines ~103–168) is
  stale: its example `config.yaml` still shows a `qdrant.url` field and a
  whole `postgres:` block, and its "Credentials are never in this file"
  paragraph lists only `PGUSER`/`PGPASSWORD`/the `*_API_KEY` vars as
  env-only — `PGHOST`/`PGPORT`/`PGDATABASE`/`QDRANT_URL` need to be added
  to that same list, and the example YAML needs those fields removed.
- `phraseforge/README.md`: no drift — its "LLM configuration" section never
  documented a `postgres:` block or Postgres fields to begin with.
- Root `README.md`: no drift caused by this change (its pre-existing "Qdrant
  isn't yet used by either app" line is stale for an unrelated reason —
  `knowledge` does use it — but that predates this feature, out of scope
  here).
- Constitution files (`mission.md`, `tech-stack.md`, `roadmap.md`): no drift
  — none of them make a claim this change contradicts.

## Documentation Updates

- `phraseforge/CHANGELOG.md`: added a `### Fixed` entry under
  `## [Unreleased]` for the env-only Postgres connection fields.
- `knowledge/CHANGELOG.md`: added a `### Fixed` entry under
  `## [Unreleased]` for the env-only Postgres/Qdrant connection fields.
- `knowledge/README.md`'s `## Configuration` section: removed `qdrant.url`
  and the `postgres:` block from the example `config.yaml`; updated the
  "Credentials are never in this file" paragraph (retitled "Credentials and
  connection settings are never in this file") to list
  `PGHOST`/`PGPORT`/`PGDATABASE`/`QDRANT_URL` alongside the existing
  credential env vars.
- `phraseforge/README.md`: no change needed (see Documentation Review).
- Removed this feature's `## Now` line from
  `specs/artifacts/phraseforge/roadmap.md` and
  `specs/artifacts/knowledge/roadmap.md` — it's in both changelogs now, not
  in flight.
- No constitution file changes (none had drift).
