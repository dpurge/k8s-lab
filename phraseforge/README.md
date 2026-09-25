# phraseforge — language-learning app

> Part of the [k8s-lab](../README.md) cluster. See the root README for cluster setup, `task`
> usage, image builds, and shared infrastructure (Postgres, Qdrant, Adminer).

A Readeck-inspired Go app for language-learning content: login, per-language teacher/student roles, and four resource types —

- **Texts** — markdown articles, with an optional transcription and a translation (in the reader's own site language). Create via form, paste text, upload a file, or ingest from a URL; title and content are cleaned automatically in the background.
- **Dialogs** — the same, using phraseforge's own turn-based markdown syntax (`--:`/`@Name:` turns). Same creation methods as Texts, with background processing for title and content cleanup.
- **Vocabulary** and **Models** — structured lists (not markdown): edited one item at a time, with proper IME support on non-Latin scripts. A vocabulary item is phrase/grammar/transcription (common) plus translation/notes (per site locale, en/pl); a model item is the same minus grammar/notes — cli-tools' own `{start-models}` block, described as "vocabulary without a grammar tag or notes".
- **LLM assist** — edit pages can generate transcription and translation with Ollama or OpenRouter-compatible chat APIs. Admins can set system prompts per request kind, source language, and target language.

Shares the `data` namespace's Postgres server with `dictionary`, but its own database (`phraseforge`) and its own migration path — the two apps' schemas never mix. Shared Postgres/LLM helper code lives in `../shared`.

## Deploy and migrate

```sh
task start-k8s              # cluster must be running first
task deploy-postgres          # Postgres + Adminer (see root README)
task deploy-phraseforge        # build the image, check its size, migrate, apply phraseforge/k8s/, wait for Ready
```

`task deploy-phraseforge` builds and size-checks the image, runs the database migration Job
(creates the `phraseforge` database if missing and applies `internal/db/schema.sql`, which
is idempotent), then applies `phraseforge/k8s/deployment.yaml` (Deployment/Service/Ingress). Run
`task migrate-phraseforge-db` on its own later to re-apply the schema after a change without a
full redeploy. `task delete-phraseforge` removes the Deployment/Service/Ingress only — it leaves
the migration Job and the database alone.

## LLM configuration

LLM configuration is file-based with per-purpose (transcription/translation) defaults, and per-prompt admin overrides:

**Configuration file:** mounted at `CONFIG_FILE` (default `/etc/phraseforge/config.yaml`), containing:
- `providers.ollama.baseURL` and `providers.openrouter.baseURL` — provider connection URLs.
- `transcription.{provider, model, think}` and `translation.{provider, model, think}` — per-purpose defaults (provider is `"ollama"` or `"openrouter"`; model examples: `"gemma4:12b"` for Ollama, `"meta-llama/llama-2-70b"` for OpenRouter; think is boolean, default false).

**Credentials:** API keys come from environment variables only, never from the config file:
- `OLLAMA_API_KEY` — Ollama API key (empty for local Ollama, which needs no auth).
- `OPENROUTER_API_KEY` — OpenRouter API key.

**Admin overrides:** in the admin panel's LLM tab, admins can set provider, model, and think (enable/disable thinking) per prompt, overriding the purpose defaults for that specific (kind, source_language, target_language) combination. A newly saved row inherits the purpose default.

**Example config.yaml:**
```yaml
providers:
  ollama:
    baseURL: http://host.docker.internal:11434
  openrouter:
    baseURL: https://openrouter.ai/api/v1
transcription:
  provider: ollama
  model: gemma4:12b
  think: false
translation:
  provider: ollama
  model: gemma4:12b
  think: false
```

## Ingress host

| Host | Service |
|---|---|
| `phraseforge.localhost:8080` | The app itself — log in as `admin`/`phraseforge` on first deploy (bootstrapped automatically; change the password from the profile page). |

```sh
curl -H "Host: phraseforge.localhost" http://localhost:8080/health
```
