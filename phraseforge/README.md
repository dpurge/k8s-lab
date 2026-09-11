# phraseforge — language-learning app

> Part of the [k8s-lab](../README.md) cluster. See the root README for cluster setup, `task`
> usage, image builds, and shared infrastructure (Postgres, Qdrant, Adminer).

A Readeck-inspired Go app for language-learning content: login, per-language teacher/student roles, and four resource types —

- **Texts** — markdown articles, with an optional transcription and a translation (in the reader's own site language).
- **Dialogs** — the same, using phraseforge's own turn-based markdown syntax (`--:`/`@Name:` turns).
- **Vocabulary** and **Models** — structured lists (not markdown): edited one item at a time, with proper IME support on non-Latin scripts. A vocabulary item is phrase/grammar/transcription (common) plus translation/notes (per site locale, en/pl); a model item is the same minus grammar/notes — cli-tools' own `{start-models}` block, described as "vocabulary without a grammar tag or notes".

Shares the `data` namespace's Postgres server with `dictionary`, but its own database (`phraseforge_app`) and its own migration path — the two apps' schemas never mix.

## Deploy and migrate

```sh
task start-k8s              # cluster must be running first
task deploy-postgres          # Postgres + Adminer (see root README)
task deploy-phraseforge        # build the image, check its size, migrate, apply phraseforge/k8s/, wait for Ready
```

`task deploy-phraseforge` builds and size-checks the image, runs the database migration Job
(creates the `phraseforge_app` database if missing and applies `internal/db/schema.sql`, which
is idempotent), then applies `phraseforge/k8s/deployment.yaml` (Deployment/Service/Ingress). Run
`task migrate-phraseforge-db` on its own later to re-apply the schema after a change without a
full redeploy. `task delete-phraseforge` removes the Deployment/Service/Ingress only — it leaves
the migration Job and the database alone.

## Ingress host

| Host | Service |
|---|---|
| `phraseforge.localhost:8080` | The app itself — log in as `admin`/`phraseforge` on first deploy (bootstrapped automatically; change the password from the profile page). |

```sh
curl -H "Host: phraseforge.localhost" http://localhost:8080/health
```
