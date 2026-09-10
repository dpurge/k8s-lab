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
task start-k8s               # cluster must be running first
task deploy                  # shared infra: Postgres, Qdrant, Adminer, Garage (see root README)
task deploy-phraseforge       # build the phraseforge image, apply phraseforge/k8s/, wait for Ready
task migrate-phraseforge-db  # run phraseforge's own database migration Job (safe to run twice)
```

`task deploy-phraseforge` applies `phraseforge/k8s/deployment.yaml` (Deployment/Service/Ingress).
`task migrate-phraseforge-db` creates the `phraseforge_app` database (if missing) and applies
`internal/db/schema.sql`, which is idempotent — safe to rerun after every schema change, not
just the first deploy.

## Ingress host

| Host | Service |
|---|---|
| `phraseforge.localhost:8080` | The app itself — log in as `admin`/`phraseforge` on first deploy (bootstrapped automatically; change the password from the profile page). |

```sh
curl -H "Host: phraseforge.localhost" http://localhost:8080/health
```
