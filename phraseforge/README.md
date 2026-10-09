# phraseforge — language-learning app

> Part of the [k8s-lab](../README.md) cluster. See the root README for cluster setup, `task`
> usage, image builds, and shared infrastructure (Postgres, Qdrant, Adminer).

A Readeck-inspired Go app for language-learning content: login, per-language teacher/student roles, and four resource types —

- **Texts** — markdown articles, with an optional transcription and a translation (in the reader's own site language). Create via form, paste text, upload a file, or ingest from a URL; title and content are cleaned automatically in the background.
- **Dialogs** — the same, using phraseforge's own turn-based markdown syntax (`--:`/`@Name:` turns). Same creation methods as Texts, with background processing for title and content cleanup.
- **Vocabulary** and **Models** — structured lists (not markdown): edited one item at a time, with proper IME support on non-Latin scripts. A vocabulary item is phrase/grammar/transcription (common) plus translation/notes (per site locale, en/pl); a model item is the same minus grammar/notes — cli-tools' own `{start-models}` block, described as "vocabulary without a grammar tag or notes". Each distinct phrase (language, script, phrase, grammar, transcription) is stored once and linked from every list that has it, so its translations are shared, an already-translated phrase is not translated again, and editing it changes it in every list that uses it. Phrase text is trimmed and Unicode-normalised (NFC) before it is stored or compared. In an export every item has an `id`; importing a file you edited outside the app updates the items it names by `id` (phrase, grammar, transcription and translations), a line without an `id` is a new item, and items missing from the file are removed from the list.
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
- `providers.ollama.firstTokenTimeoutSeconds` (default 300) and `providers.ollama.idleTimeoutSeconds` (default 60) — Ollama calls stream, and fail if no output arrives within the first limit (model load + prompt processing) or if output then stops for longer than the idle limit. They belong to the provider because they reflect how fast that host runs a model.
- `<purpose>.timeoutSeconds` (default 1800) — overall cap on one call, a backstop against runaway output; the streaming limits above are what catch a hung call. An admin LLM rule's own timeout overrides it.
- `<purpose>.maxAttempts` (default 3) — total attempts for one LLM call, shared between replies that fail validation (the rejected reply and the exact error are sent back to the model) and transient failures (HTTP 5xx, connection errors, a stream that ends early, idle stalls), which wait 5s and then 15s before retrying. First-token and overall timeouts are not retried. Zero or less means 3.
- `ingest.maxContentBytes` (default 24576) — largest raw content, in bytes, that one ingest request may carry. Content is processed in chunks sized from each purpose's `numCtx`, one LLM call per chunk, so a larger limit means a longer-running job; the queue runs one job at a time.
- `vocabularyItem.{provider, model, think, timeoutSeconds, prompt}` and `modelsItem.{...}` — one structured (JSON) call per vocabulary/models item per site locale. `prompt` is a template with the placeholders `{{sourceLanguage}}`, `{{targetLanguage}}`, `{{phrase}}`, `{{grammarPrompt}}`, `{{transcriptionPrompt}}` (the same names as the prompt-eval setup, so a tuned template can be pasted in unchanged; the last two are the language's snippets, see below); the reply is constrained by a JSON schema and validated before any field is stored, and only blank fields are written.
- `transcription.{provider, model, think}` and `translation.{provider, model, think}` — per-purpose defaults (provider is `"ollama"` or `"openrouter"`; model examples: `"gemma4:12b"` for Ollama, `"meta-llama/llama-2-70b"` for OpenRouter; think is boolean, default false).

**Credentials:** API keys come from environment variables only, never from the config file:
- `OLLAMA_API_KEY` — Ollama API key (empty for local Ollama, which needs no auth).
- `OPENROUTER_API_KEY` — OpenRouter API key.

**Admin overrides:** in the admin panel's LLM tab, admins can set provider, model, and think (enable/disable thinking) per prompt, overriding the purpose defaults for that specific (kind, source_language, target_language) combination. A newly saved row inherits the purpose default.

**Default prompts:** every purpose's default prompt is a file of prompt-eval's `prompt/default/system/` (`generate-title.txt`, `generate-vocabulary.txt`, ...), embedded in the binary from `internal/config/prompts/default/`. prompt-eval is the source of truth: `npm run sync-prompts` there copies the files into this repository and `npm run check-sync` fails when the copy differs. A prompt in `config.yaml` replaces the embedded default for its purpose, and an admin row replaces both. In Admin > LLM a new prompt starts from the default for its kind, with its placeholders visible, and the form lists the placeholders available for that kind. The admin kind names match the file names ("Generate title" is `generate-title.txt`).

**Language snippets:** Admin > Languages holds, per language, a grammar-tag snippet and a transcription snippet, one table row each. A snippet is defined once and inserted wherever a prompt contains its placeholder: `{{grammarPrompt}}` (grammar-tag conventions) and `{{transcriptionPrompt}}` (transcription system) in the vocabulary/models item prompts and in the generate transcription, generate vocabulary and generate models prompts. A language without a snippet gets nothing, except that a language whose IME config needs transcription gets "Transcribe using the standard romanization for this language.". Grammar and transcription are only generated for an item whose language has the matching snippet. A prompt stored in `config.yaml` or as an admin row only uses a snippet if it contains the placeholder. Snippets are part of the admin configuration export/import.

**Example config.yaml:**
```yaml
providers:
  ollama:
    baseURL: http://host.docker.internal:11434
    firstTokenTimeoutSeconds: 300
    idleTimeoutSeconds: 60
  openrouter:
    baseURL: https://openrouter.ai/api/v1
transcription:
  provider: ollama
  model: gemma4:12b
  think: false
  timeoutSeconds: 1800
translation:
  provider: ollama
  model: gemma4:12b
  think: false
  timeoutSeconds: 1800
```

## Ingress host

| Host | Service |
|---|---|
| `phraseforge.localhost:8080` | The app itself — log in as `admin`/`phraseforge` on first deploy (bootstrapped automatically; change the password from the profile page). |

```sh
curl -H "Host: phraseforge.localhost" http://localhost:8080/health
```

## Tests

`go test ./...` runs everything except the database tests of `internal/vocabulary` and `internal/models`, which are skipped unless `PHRASEFORGE_TEST_DSN` is set to a Postgres URL. Each of those tests applies `internal/db/schema.sql` into its own throwaway schema and drops it afterwards, so a development database is safe to point at.
