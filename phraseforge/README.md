# phraseforge — language-learning app

> Part of the [k8s-lab](../README.md) cluster. See the root README for cluster setup, `task`
> usage, image builds, and shared infrastructure (Postgres, Qdrant, Adminer).

A Readeck-inspired Go app for language-learning content: login, per-language teacher/student roles, and four resource types —

- **Texts** — markdown articles, with an optional transcription and a translation (in the reader's own site language). Create via form, paste text, upload a file, or ingest from a URL; title and content are cleaned automatically in the background.
- **Dialogs** — the same, using phraseforge's own turn-based markdown syntax (`--:`/`@Name:` turns). Same creation methods as Texts, with background processing for title and content cleanup.
- **Vocabulary** and **Models** — structured lists (not markdown): edited one item at a time, with proper IME support on non-Latin scripts. A vocabulary item is phrase/grammar/transcription (common) plus up to 5 short usage examples (source-language text, shared like the translations and shown below the notes) and translation/notes (per site locale, en/pl); a model item is the same minus grammar/notes — cli-tools' own `{start-models}` block, described as "vocabulary without a grammar tag or notes". Each distinct phrase (language, script, phrase, grammar, transcription) is stored once and linked from every list that has it, so its translations are shared, an already-translated phrase is not translated again, and editing it changes it in every list that uses it. Phrase text is trimmed and Unicode-normalised (NFC) before it is stored or compared. In an export every item has an `id`; importing a file you edited outside the app updates the items it names by `id` (phrase, grammar, transcription, translations and examples: an `examples` key replaces the phrase's examples, an empty list clears them, a missing key leaves them), a line without an `id` is a new item, and items missing from the file are removed from the list.
- **LLM assist** — edit pages can generate transcription and translation through a model router that calls Ollama, OpenAI-compatible APIs such as OpenRouter, Anthropic or Vertex AI. Admins can set system prompts per request kind, source language, and target language.

Shares the `data` namespace's Postgres server with `dictionary`, but its own database (`phraseforge`) and its own migration path — the two apps' schemas never mix. Shared Postgres/LLM helper code lives in `../shared`.

## Navigation and admin pages

The main menu is the top bar: Documents and, for admins, Tasks, Usage and Config. Documents groups the content sections; on its pages the left sidebar lists Texts, Dialogs, Vocabulary and Models under the language filter. On Usage and Config the sidebar lists the section's own pages (no language filter); Tasks has no sidebar. Every screen has a `#` address: `#/tasks`, `#/usage[/models|/performance|/attempts[?page=N]]`, `#/config[/ime|/llm|/languages|/transfer]`; the old `#/jobs` and `#/admin` addresses are not kept (they land on Texts).

- **Tasks** — the background jobs (called jobs in the API and the `jobs` table). The Clear button removes completed and cancelled tasks, not pending, running or failed ones (`POST /api/v1/admin/jobs/clear`).
- **Config** (admins) — the former Admin tabs as sidebar pages: Permissions, IME, LLM (prompt settings), Languages and Export/import.
- **Usage** (admins) — Usage (calls, input tokens, output tokens and cost per user, provider and model; successful calls only; period filter, grouped by user, provider or model; `GET /api/v1/admin/llm-usage?since=`), Models (provider, model, tags, priority, availability), Performance and Attempts (every call, newest first, 25 per page, filters for model, outcome and period, and the purge of old records). `GET /api/v1/admin/llm-calls` is paged: `limit` (default 25, at most 200) and `offset`; it returns `{items, total, limit, offset}` and each item has `user_id` and `username`. The tables are shared `dp-*` components from `shared/ui`.
- **User on jobs and model calls** — `jobs` and `llm_calls` have a nullable `user_id` (`ON DELETE SET NULL`, added whenever `schema.sql` is applied). A job queued by a signed-in request records that user, and so do the model calls made for it or directly in the request; a retry keeps the original job's user. Old rows and system work have no user (shown as a dash).

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

Model calls go through a router. Two mounted files configure it, and per-kind admin overrides pick model tags:

- **`config.yaml`** (`CONFIG_FILE`, default `/etc/phraseforge/config.yaml`): the purposes (transcription, translation, title, ...) with their prompt, numCtx, timeouts and `model_tags`, plus ingest limits and retention.
- **`models.yaml`** (`PHRASEFORGE_MODELS_FILE`, default `/etc/phraseforge/models.yaml`): the providers and models the router can call. Unlike `config.yaml` it is required: the server does not start without it (`migrate` does not read it). A commented example is `models.example.yaml`; the dev cluster's is `models.dev.yaml`, which the Taskfile builds into the ConfigMap `phraseforge-models` (mounted with `config.yaml` at `/etc/phraseforge`).

Both are read at startup: restart the pod after a change.

**`config.yaml` keys:**
- `<purpose>.model_tags` (default `[default]`) — the tags a model must all have to serve the purpose; see Model routing below.
- `<purpose>.timeoutSeconds` (default 1800) — overall cap on one call, a backstop against runaway output; the streaming limits of the model (`first_token_timeout_seconds`, `idle_timeout_seconds` in `models.yaml`) are what catch a hung call. An admin LLM rule's own timeout overrides it.
- `<purpose>.maxAttempts` (default 3) — total attempts for one LLM call, shared between replies that fail validation (the rejected reply and the exact error are sent back to the model) and transient failures (HTTP 5xx, connection errors, a stream that ends early, idle stalls), which wait 5s and then 15s before retrying. First-token and overall timeouts are not retried. Zero or less means 3.
- `llm_calls.retention_days` (default 30) — days to keep the model-call history (table `llm_calls`); older rows are purged every 6 hours. 0 keeps it forever.
- `jobs.retention_days_done` (default 7) and `jobs.retention_days_failed` (default 30) — days to keep finished jobs (table `jobs`; `failed` also covers cancelled), purged every 6 hours. Pending and running jobs are never purged; 0 keeps a kind forever.
- `ingest.maxContentBytes` (default 24576) — largest raw content, in bytes, that one ingest request may carry. Content is processed in chunks sized from each purpose's `numCtx`, one LLM call per chunk, so a larger limit means a longer-running job; the queue runs one job at a time.

**Validation and behavior:**
- Unknown keys in `config.yaml` are rejected at startup with a message naming the file and the key (misspelt keys used to be silently ignored, so `num_ctx` instead of `numCtx` is now an error). The keys this feature replaced are rejected with a message naming the replacement: a purpose's `provider` and `model` (use `model_tags` and `models.yaml`), its `think` (now the model's `options.think`) and the top-level `providers:` block (now in `models.yaml`). `numCtx`, `timeoutSeconds`, `maxAttempts`, `maxReplyTokens` and `prompt` are unchanged.
- Retention days are capped at 36500 (99.9 years); exceeding this limit is a validation error.
- Attempt records: each model call attempt is recorded synchronously with a 5 s timeout; if the database is down each attempt can wait up to 5 s for the insert to time out. Error text in the record is bounded to 1000 bytes, UTF-8 safe, to prevent unbounded growth.
- Retry behavior: when a job's only model matching its tags is held by the circuit breaker (after consecutive failures), the router waits for it to become available (up to 2 minutes per attempt, within the total attempt budget) instead of failing at once. No wait occurs when no model has the required tags or a credential is missing.

**`vocabularyItem.{model_tags, numCtx, timeoutSeconds, maxAttempts, maxReplyTokens, prompt}` and `modelsItem.{...}` — one structured (JSON) call per vocabulary/models item per site locale. `prompt` is a template with the placeholders `{{sourceLanguage}}`, `{{targetLanguage}}`, `{{phrase}}`, `{{grammarPrompt}}`, `{{transcriptionPrompt}}` (the same names as the prompt-eval setup, so a tuned template can be pasted in unchanged; the last two are the language's snippets, see below). A vocabulary item prompt may also use `{{usage}}` (how the word was used in the generated text, from an optional ` | usage` part at the end of a generate vocabulary line; empty for items not made by generating a list) and `{{examples}}` (the phrase's examples so far, one per line, each prefixed `- `; empty when there are none); a template without them renders exactly as before. The reply is constrained by a JSON schema and validated before any field is stored, and only blank fields are written; a vocabulary reply may also carry an optional `example` (a short phrase of at most 80 characters), which is added to the phrase's examples when there are fewer than 5 and it is new, and is ignored, without a correction call, when it is blank, not a string or too long. A vocabulary reply may also carry an optional `rewrite` (`{"phrase", "grammar", "transcription"}` or null) for a phrase that is not in dictionary form (a different case, a joined form): it is applied only when the job carries a usage (generated from a text) and the new phrase differs from the stored one after trim and NFC, as one change like a manual edit of the phrase (the item keeps its id, every list that links the phrase shows the new values, translations, notes and examples stay with it, and a phrase that already exists with exactly those values is merged into). An empty grammar or transcription keeps the stored one, and a language without that section ignores it. The rewrite is applied after the job's other writes; if it is skipped or fails it is logged and the job still succeeds. A `rewrite` that is not that object is ignored without a correction call. Translations of other locales are not redone when a phrase is rewritten. A vocabulary item prompt may also use `{{translation}}` (the phrase's existing translation in the job's locale, empty when there is none), and the reply may carry an optional `newSense` (one extra meaning in the target language, or null). A new sense is appended to the locale's existing translation as `old; sense` (the old text kept as the prefix) only by a job made for a phrase the check marked `needsSense` (the job's payload has `sense`), and only if the translation is not blank and the sense is not already one of its senses (compared ignoring case, trim and Unicode form); notes are untouched and there is no cap on the number of senses. Models often put the extra meaning into `translation` instead of `newSense`, so when `newSense` is empty the senses of the reply's `translation` that the existing one lacks are used, but only if the reply keeps every existing sense. A skipped or failed append is logged and the job still succeeds.
- `checkVocabulary.{model_tags, numCtx, timeoutSeconds, maxAttempts, prompt}` — the check of known phrases after a vocabulary list is generated. Phrases that already have a translation or an example and came with a usage (` | usage`) are sent, numbered with their usage, examples and translations, in one call per list (20 phrases per call at most); the model replies `{"results":[{"n":1,"needsExample":true,"needsSense":false}]}` for each (`needsSense` means the usage shows a meaning that none of the existing translations covers; an absent or non-boolean `needsSense` counts as false). A phrase marked `needsExample` gets one item job, for the first site locale, which may return an `example` for it; a phrase marked `needsSense` gets one item job for every site locale in which it already has a translation, each of which may append a new sense there (see the item reply above); new phrases and phrases without a usage are not sent and are queued as always. A reply that is not that JSON is sent back for correction within `maxAttempts`; if the call still fails, it is logged with the list id and skipped, and the generate job succeeds. `prompt` is a template with `{{sourceLanguage}}` and `{{candidates}}`. In Config > LLM it is the kind "Check vocabulary" (`check_vocabulary`).
- `reviewVocabulary.{model_tags, numCtx, timeoutSeconds, maxAttempts, prompt}` — the review of one vocabulary item (job kind `review_vocabulary_item`, one structured call per item). It has its own `model_tags`, like every purpose; to review with a more capable model give that model a tag of its own in `models.yaml` and set the tag here or in an Config > LLM row for the kind; no other purpose changes. The prompt's placeholders are `{{sourceLanguage}}`, `{{phrase}}`, `{{grammar}}`, `{{transcription}}`, `{{examples}}` (numbered), `{{translations}}` (one line per locale, English first as the reference, notes below), `{{dictionaryPrompt}}`, `{{grammarPrompt}}`, `{{transcriptionPrompt}}`. The reply is a schema-constrained set of `{ok, suggestion, reason}` checks for phrase, grammar and transcription, per-locale translations and notes, and per-example keep/remove/shorten. Nothing is written by the job: its proposals are stored per item (table `vocabulary_reviews`, one review per item, replaced by the next one) and shown in the Review dialog of the vocabulary page, where an editor applies or dismisses each (or all). A phrase proposal carries its grammar and transcription and is applied as one change; a proposal is only applied while the item still has the reviewed phrase and the value it was about (otherwise it is a conflict and stays open). Checks for a field whose language has no matching snippet (dictionary form, grammar, transcription) are ignored. API (edit right on the list's language): `POST /api/v1/vocabulary/{id}/review` and `POST .../items/{position}/review` queue jobs (never a second one for an item that has one waiting), `GET .../reviews`, `GET .../items/{position}/review`, `POST .../review/apply` and `.../review/dismiss` with `{"indexes": [...]}` (none = every open proposal).
- `reviewModels.{model_tags, numCtx, timeoutSeconds, maxAttempts, prompt}` — the review of one models item (job kind `review_models_item`, one structured call per item). It has its own `model_tags`. The prompt's placeholders are `{{sourceLanguage}}`, `{{phrase}}`, `{{transcription}}`, `{{translations}}` (one line per locale, English first as the reference) and `{{transcriptionPrompt}}`. The reply is a schema-constrained set of `{ok, suggestion, reason}` checks for the phrase, the transcription and each locale's translation. Nothing is written by the job: its proposals are stored per item (table `models_reviews`, one review per item, replaced by the next one) and shown in the Review dialog of the models page, where an editor applies or dismisses each (or all). A phrase proposal carries its transcription and is applied as one change; the transcription check is ignored for a language without a transcription snippet; a proposal is only applied while the item still has the reviewed phrase and the value it was about (otherwise it is a conflict and stays open). The models page also has a Generate button that queues the translations and transcriptions the list still lacks, like the vocabulary page's. API (edit right on the list's language): `POST /api/v1/models/{id}/generate-missing-translations`, `POST /api/v1/models/{id}/review` and `POST .../items/{position}/review` queue jobs (never a second one for an item that has one waiting), `GET .../reviews`, `GET .../items/{position}/review`, `POST .../review/apply` and `.../review/dismiss` with `{"indexes": [...]}` (none = every open proposal).
- `reviewTranscription.{model_tags, numCtx, timeoutSeconds, maxAttempts, prompt}` and `reviewTranslation.{...}` — the reviews of a text's or dialog's transcription and of its translations, each with its own model tags, numCtx and prompt (none falls back to another's). The transcription and the translations are reviewed separately: the text and dialog view pages have a Review transcription and a Review translation button (editors only) and a status line with a link per part that opens the proposals dialog. One job (kind `review_text`, payload `{resource_type, resource_id, part}`) and one structured call per part, which sees the source text, the part under review and the vocabulary list generated from the text (up to 100 phrases with their translations; English translation first as the reference); a text whose source and reviewed part exceed 12000 characters is split into paragraph chunks (blank-line separated) of at most 12000 characters, one call each, and the chunk results are merged into one proposal per field; this needs the source and the reviewed part (each locale, for translations) to have the same number of paragraphs, and a paragraph pair over the limit cannot be split, otherwise the job fails with a message naming the counts. The transcription is only reviewed when the text has one and its language has a transcription snippet. A proposal is the whole corrected transcription or translation of one locale; nothing is written by the job. Proposals are stored per resource and part (table `resource_reviews`, replaced by the next review) and applied or dismissed one by one or all open; a proposal is refused while the source text changed since the review (409, the review is shown as outdated) and is a conflict, left open, when the transcription or translation was edited since. API (edit right on the language): `POST /api/v1/{texts|dialogs}/{id}/review/{transcription|translation}` queues (never a second job for a part that has one waiting), `GET` the same path returns `{pending, review}`, and `POST .../apply` and `.../dismiss` take `{"indexes": [...]}` (none = every open proposal).
- **Choosing a bigger review model:** all four review purposes (`reviewVocabulary`, `reviewModels`, `reviewTranscription`, `reviewTranslation`) use the models tagged `default` unless set. To use a bigger one, add it to `models.yaml` with a tag of its own (for example `reviewer`) and set `model_tags: [reviewer]` on the purposes in `config.yaml` (in the cluster: `phraseforge.<purpose>` in the Helm chart repository's `jdp-frontend/values.yaml`), or per language in Config > LLM (kind `review_vocabulary`, `review_models`, `review_transcription` or `review_translation`) without a redeploy. With an `ollama` provider the model must be pulled on the server and fit its memory and storage. To review everything with the bigger model set the tag on all four.
- **Pages that wait for background work** (the review columns of the vocabulary and models pages, the review line of a text or dialog, and the Tasks list) refresh themselves through one helper, `static/js/poll.js`: only while something is pending or running, after 8 s, then 15 s, then 30 s between requests; not while the browser tab is hidden (one refresh when it is visible again); not for more than 15 minutes (any click starts it again); and not after leaving the page. Its timing is covered by `static/components/poll.test.js` (`npm test` there).
- `transcription.{model_tags, numCtx, timeoutSeconds, maxAttempts, prompt}` and `translation.{...}` — per-purpose defaults, like every other purpose.

### Model routing

A purpose does not name a model. It lists `model_tags` in `config.yaml` (default `[default]`), and every model in `models.yaml` has `tags` and a `priority`:

- A call goes to the models that have **all** the purpose's tags, tried from the highest `priority` down, skipping models that are not available. The first one that answers wins.
- When a model fails, the call falls back to the next candidate. A model is not available while the circuit breaker holds it out, or while its provider needs a credential and has none (see Where to configure API keys).
- The breaker (`routing.breaker` in `models.yaml`): `failures` (default 3) server errors or timeouts in a row hold a model out for `cooldown_seconds` (default 60); a rate limit (429) holds it out at once for `rate_limited_cooldown_seconds` (default 30); a connection failure (refused, no route, DNS, dial timeout) means the provider's machine is unreachable and holds out **every model of that provider** for `cooldown_seconds`. After the cooldown the model is tried again. A refused request (other 4xx) or a cancelled call changes nothing.
- A purpose whose tags match no model fails the server at startup, and so does an `ollama` model matching a purpose with `options.num_ctx` below the purpose's `numCtx` (the purpose sizes its input to that window); the message names the purpose and the model.
- Config > LLM can override the tags (and the timeout) per (kind, source language, target language), without a redeploy: tick the tags a model must have. Only tags that exist in `models.yaml` are accepted.

**`models.yaml`** has `providers` (`id`, `type` = `ollama`, `openai`, `anthropic` or `vertex`, `base_url`, `project` and `location` for `vertex`, the compulsory `api_key_from`, see below, and the optional `headers`, see Custom request headers), `models` (`id`, `provider`, `model`, `tags`, `priority`, `options` and `price` with `input_per_mtok` and `output_per_mtok`, US dollars per million tokens, 0 by default) and `routing.breaker`. The options are `num_ctx` and `think` (`ollama` only), `first_token_timeout_seconds` (model load + prompt processing) and `idle_timeout_seconds` (longest gap between outputs), which end a hung streaming call (`ollama` only, the other types do not stream), and `num_predict` and `overall_timeout_seconds` (every type); a provider ignores the options that do not apply to it. The full schema is `../shared/modelcfg/models.schema.json`, and the package documentation of `../shared/modelcfg` has the per-type table.

### Where to configure API keys

Every provider entry in `models.yaml` has the **compulsory** key `api_key_from`; its value may be empty:

| `api_key_from` | Meaning |
|---|---|
| `""` | no credential |
| `env:NAME` | the environment variable `NAME`, read on every call; the pod's environment only changes on a restart, so a new value needs one |
| `file:/absolute/path` | the content of the file, trimmed of whitespace, re-read on every call, so a rotated secret or a refreshed token needs no restart |

A provider without the key is a configuration error, so a forgotten credential is not a silent default. The key itself is never written in `config.yaml`, `models.yaml` or the database: `api_key_from` only says where to find it.

**Credentials are optional for every provider, and a missing one is never a startup error.** Which provider type needs one:

| Provider type | `api_key_from` | Credential | Sent as | Other required fields |
|---|---|---|---|---|
| `ollama` | `""`, `env:NAME` or `file:/path` (conventionally `env:OLLAMA_API_KEY`) | not needed (a local server or one behind a proxy); sent when present | `Authorization: Bearer` | `base_url` |
| `openai` (any OpenAI-compatible API: OpenRouter, OpenAI, a local server) | same (conventionally `env:OPENROUTER_API_KEY` or `env:OPENAI_API_KEY`) | not needed for a local server; sent when present | `Authorization: Bearer` | `base_url` |
| `anthropic` | same (conventionally `env:ANTHROPIC_API_KEY`) | **needed**: without one the provider is disabled | `x-api-key` header | `base_url` is optional (default `https://api.anthropic.com`) |
| `vertex` (Google) | same, usually `file:/path` to a token file | **needed**: an OAuth access token, not an API key; without one the provider is disabled | `Authorization: Bearer` | `project` and `location` (no `base_url`) |

What happens without a needed credential (an empty or unset variable, an unreadable or empty file):

- The provider is **disabled**: the router skips its models and calls fall back to the next model.
- The log has one warning `llm provider unavailable` (fields `provider_id`, `reason` = `missing_credentials`, `models`, `credential_source` = `env:NAME`, `file:/path` or `none`; never the credential) at startup and whenever the state changes, and an info line `llm provider available` when the credential appears.
- The Usage > Models page shows the model as unavailable, with the badge `missing_credentials` (or `cooldown` while the circuit breaker holds it).
- A purpose whose matching models are **all** on providers without a usable credential logs the warning `purpose has no usable model` at startup. A purpose whose tags match no model at all is still a fatal configuration error.
- With `file:` the provider returns by itself once the file has content; with `env:` set the variable and restart.

**Where the value comes from:**

- **In a cluster:** the Helm chart repository's `jdp-frontend` chart reads the keys from its secret store entry for phraseforge through an ExternalSecret, and the Deployment passes them as environment variables (each optional, so a missing one never blocks the pod). The chart's values say which keys are copied; add the key to the secret store first, then enable it in the values (a missing remote key would fail the whole ExternalSecret, the database credentials with it), and restart the pod. Its `models.yaml` names them as `api_key_from: env:NAME`.
- **In the dev cluster:** `k8s/deployment.yaml` reads `OLLAMA_API_KEY`, `OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY` and `OPENAI_API_KEY` from the Secret `phraseforge-llm-keys` in the `app` namespace, each marked `optional: true`, so by default these variables are unset (`task deploy-phraseforge` does not create that Secret). To manage `OPENROUTER_API_KEY` there, use `task llm-enable` (the key comes from the git-ignored `.env`; it also adds the LAN Ollama, OpenRouter and Vertex models): see [LLM providers in the dev cluster](../README.md#llm-providers-in-the-dev-cluster) in the root README. For the other keys, create the Secret by hand: put `NAME=value` lines in a file outside the repository and run (with the lab's kubeconfig, as the Taskfile's `KUBECTL` does):

  ```sh
  kubectl -n app create secret generic phraseforge-llm-keys --from-env-file=<file> --dry-run=client -o yaml | kubectl -n app apply -f -
  kubectl -n app rollout restart deployment/phraseforge
  ```

  Then name the variable in a provider of the dev `models.yaml` (`models.dev.yaml`, which `task deploy-phraseforge` rebuilds into the ConfigMap `phraseforge-models`), for example `api_key_from: env:ANTHROPIC_API_KEY`, and run `task deploy-phraseforge`. That file has one Ollama provider with `api_key_from: ""`. For a variable with another name, add it to the `env` list of `k8s/deployment.yaml` the same way.

  The rule: `task llm-enable` never overwrites a key or Secret it did not create. It stops and names it, so a hand-made `phraseforge-llm-keys` stays untouched, and its other keys (for example `ANTHROPIC_API_KEY`) are kept when `llm-enable` adds or removes `OPENROUTER_API_KEY`. `llm-disable` and a repeated `llm-enable` remove only what `llm-enable` created.
- **Vertex in the dev cluster:** the Deployment mounts the optional Secret `phraseforge-vertex` (key `token`) at `/etc/phraseforge-vertex`. `task llm-enable` creates it from your gcloud account (`LLM_VERTEX_ACCOUNT` and `LLM_VERTEX_PROJECT` in `.env`) and adds a `vertex` provider reading `file:/etc/phraseforge-vertex/token`; the token is valid about an hour, and `task llm-refresh-vertex-token` writes a new one. Production Vertex is not implemented: see [Known gaps](../README.md#known-gaps) in the root README.

### Custom request headers

Every provider except `demo` may have an optional `headers` mapping (header name to value), the same for all types. Some gateways want extra headers, for example attribution headers or an extra token.

```yaml
providers:
  - id: openrouter
    type: openai
    base_url: https://openrouter.ai/api/v1
    api_key_from: env:OPENROUTER_API_KEY
    headers:
      HTTP-Referer: https://example.org   # a literal value
      X-Gateway-Token: env:GATEWAY_TOKEN  # read from the environment on every call
      X-Other: file:/run/secrets/other    # the file's trimmed content, read on every call
```

- A value is `env:NAME` (an environment variable), `file:/absolute/path` (the file's content, trimmed of whitespace) or else a literal. A literal that must itself start with `env:` or `file:` cannot be written.
- `env:` values are read on every call from the pod's environment, which changes only on restart; a new value needs a pod restart. `file:` contents are re-read on every call, so a rotated secret needs no restart.
- Headers are sent after the provider's own headers and may replace one that is not reserved, for example `anthropic-version`. Names are case-insensitive and may not repeat.
- Reserved names are refused at startup with a message naming the provider and the header: `Authorization` and `x-api-key` (set the credential with `api_key_from`), and `Content-Type`, `Content-Length`, `Host`, `Connection` and `Transfer-Encoding`, which the client sets. A name must be a valid HTTP header name, a literal must not contain a line break or NUL, `env:NAME` needs a valid variable name and `file:` an absolute path. The findings never repeat a value.
- A header from `env:` or `file:` that is unset, empty or unreadable disables the provider exactly like a missing credential: reason `missing_credentials`, the models are skipped, calls fall back, and the `llm provider unavailable` warning names the source in `credential_source`, for example `none; header X-Gateway-Token from env:GATEWAY_TOKEN` (the source, never a value). If it vanishes between the check and the call, that call fails without a request.
- Header names, values and sources are never shown in the admin UI or API, in metrics or in `llm_calls`; a name and source appear only in the `credential_source` of the two provider log lines above and in the error text of the rare call that finds a header gone after the availability check (that error is logged and recorded like any failed attempt).
- Every provider type applies them: `ollama` through the shared chat client, the others directly.

### Adding another provider

Add an entry to `providers` and one or more to `models` in `models.yaml`, and restart.

- `id` is free: lowercase letters, digits and hyphens, starting with a letter, unique. Models refer to it in `provider`.
- `type` picks the API: `ollama`, `openai`, `anthropic` or `vertex`.
- Several providers of one type are fine: several Ollama hosts, or several OpenAI-compatible endpoints such as OpenRouter and OpenAI, each with its own `base_url` and `api_key_from`.
- `openai` means any endpoint that implements `POST {base_url}/chat/completions` with a Bearer key (for OpenRouter, `base_url: https://openrouter.ai/api/v1`).
- Timeouts in PhraseForge: the purpose's `timeoutSeconds` (config.yaml or Config > LLM override) is sent with every attempt as the overall timeout and overrides each model's `options.overall_timeout_seconds`; set a purpose's `timeoutSeconds: 0` to let the model's option decide. Model `options`: `first_token_timeout_seconds` and `idle_timeout_seconds` apply to `ollama` models only; `overall_timeout_seconds` and `num_predict` apply to every type, `num_ctx` and `think` to `ollama` only. The timeout applies per attempt, so a fallback chain can take several times as long. No per-provider connect timeout.

### Example: cluster CPU, LAN GPU, OpenRouter

The Helm chart's example `models.yaml` has three models, all for every purpose that asks for `default` except the last:

| Model | Provider | Tags | Priority | Role |
|---|---|---|---|---|
| `gemma4-12b-cluster` | Ollama in the cluster (CPU, slow) | `default` | 10 | always there |
| `gpu-lan-large` | Ollama on a GPU machine in the local network, usually switched off | `default`, `gpu` | 100 | used whenever the machine is up |
| `openrouter-reference` | OpenRouter | `openrouter` only | 50 | for comparing quality; never used by default |

With the GPU machine up, every call goes to it (priority 100); when it is off, the call falls back to the cluster model. The cost of an off machine: a refused or unroutable connection fails at once and holds the provider out for `cooldown_seconds`, then one call tests it again; a machine that silently drops packets costs a wait until a timeout expires on each test, so keep `first_token_timeout_seconds` of the GPU model moderate (the cluster model needs 300 s for a cold start, a GPU rarely does). A failed test call is not lost: it falls back to the cluster model.

`openrouter-reference` lacks the `default` tag, so no purpose uses it unless selected. To try it on some jobs: in Config > LLM add an override for a kind and languages with the tag `openrouter`, or set `model_tags: [openrouter]` on one purpose in `config.yaml`. This sends the text to a third party, and needs `OPENROUTER_API_KEY` in the pod's environment (an `openai` provider sends no credential when it has none, which OpenRouter does not accept).

### Observability

- **Logs:** every call writes `llm attempt`, `llm breaker open`, `llm provider down`, `llm breaker closed` and `llm call done` lines sharing a `call_id`; the messages, levels and fields, with `jq` examples, are in the package documentation of `../shared/router/doc.go`. Lines never hold prompts, replies or keys.
- **Usage section (admins):** Usage (calls, tokens and cost per user, provider and model), Models (availability of every model and why), Performance (aggregates by model) and Attempts (each attempt with outcome and error, with a purge of old attempts); the Tasks page shows the background tasks (jobs).
- **`/metrics`** (Prometheus): exposed without login on the main listener, reachable through the ingress. Shows model ids, provider ids, call counts, cost, and Go/process info. Never contains credentials or model content. Metrics: `phraseforge_llm_calls_total`, `phraseforge_llm_tokens_total`, `phraseforge_llm_cost_usd_total`, `phraseforge_llm_call_duration_seconds`, `phraseforge_llm_model_available`, `phraseforge_llm_record_errors_total`, `phraseforge_llm_calls_purged_total` and `phraseforge_jobs_purged_total`. **Security note:** in production, block `/metrics` at the ingress or scrape the pod address directly instead of going through the public endpoint. Each scrape evaluates provider availability (reads credential and header files).

### Upgrading from the old configuration

`config.yaml` of an older version is refused at startup, each message naming its replacement:

| Old key | Replacement |
|---|---|
| `providers` (`ollama`/`openrouter` `baseURL`, timeouts) | the providers and model options of `models.yaml`; the Ollama key is `api_key_from` |
| `<purpose>.provider`, `<purpose>.model` | `<purpose>.model_tags`; the models are in `models.yaml` |
| `<purpose>.think` | `options.think` of the model in `models.yaml` |

The provider and model overrides of the Config > LLM rows (table `llm_prompts`) are dropped and replaced by tags; set them again with tags in Config > LLM. `models.yaml` is new and required by the server.

**Config overrides:** in Config > LLM, admins can set model tags (a model must have every ticked tag; none ticked inherits the purpose's `model_tags`), think (enable/disable thinking) and timeout per kind, overriding the purpose defaults for that specific (kind, source_language, target_language) combination. A newly saved row inherits the purpose default. The tags to choose from are those of the models in `models.yaml`; an unknown tag, or tags no single model has together, is rejected. The prompt text is never stored per row: it is the purpose default, shown read-only in the LLM tab, and a language's rules come from Config > Languages.

**Default prompts:** every purpose's default prompt is a file of prompt-eval's `prompt/phraseforge/default/system/` (`generate-title.txt`, `generate-vocabulary.txt`, ...), embedded in the binary from `internal/config/prompts/default/`. prompt-eval is the source of truth: `npm run sync-prompts` there copies the files into this repository and `npm run check-sync` fails when the copy differs. A prompt in `config.yaml` replaces the embedded default for its purpose, and an admin row replaces both. In Config > LLM a new prompt starts from the default for its kind, with its placeholders visible, and the form lists the placeholders available for that kind. The admin kind names match the file names ("Generate title" is `generate-title.txt`).

**Language snippets:** Config > Languages holds, per language, a grammar-tag snippet, a transcription snippet and a dictionary-form snippet, one tile each. A snippet is defined once and inserted wherever a prompt contains its placeholder: `{{grammarPrompt}}` (grammar-tag conventions) `{{transcriptionPrompt}}` (transcription system) and `{{dictionaryPrompt}}` (the dictionary form of the phrases, used by the review prompt and any prompt that contains it) in the vocabulary/models item prompts and in the generate transcription, generate vocabulary and generate models prompts. A language without a snippet gets nothing, except that a language whose IME config needs transcription gets "Transcribe using the standard romanization for this language.". Grammar and transcription are only generated for an item whose language has the matching snippet. A prompt in `config.yaml` only uses a snippet if it contains the placeholder. Snippets are part of the admin configuration export/import.

**Example config.yaml** (the keys omitted keep their defaults):
```yaml
transcription:
  model_tags: [default]
  numCtx: 4096
  timeoutSeconds: 1800
translation:
  model_tags: [default]
  numCtx: 4096
  timeoutSeconds: 1800
```

## Ingress host

| Host | Service |
|---|---|
| `phraseforge.localhost:8080` | The app itself — log in as `admin`/`phraseforge` on first deploy (bootstrapped automatically; change the password from the profile page). |

```sh
curl -H "Host: phraseforge.localhost" http://localhost:8080/health
```

## Message bus (NATS)

Phraseforge answers `task_status` over NATS on `dp.phraseforge.request`, with the message format of `shared/message`: `{id}` in, the job's `id`, `kind`, `status`, `step`, `progress`, `error`, `created_at` and `updated_at` out (the job's payload, result and user are never copied; an id that is not a UUID or is unknown answers `not_found`). Any other operation, `url_ingest` included, answers `unknown_operation`.

It listens only when `nats.url` in `config.yaml` (or the env var `PHRASEFORGE_NATS_URL`) is set; empty, the default in code, means it never connects, and a failed connection is logged without stopping the app. The dev ConfigMap sets `nats://jdp-data-nats.data.svc:4222`. Nothing authenticates a sender: any client that can reach NATS can ask. On SIGTERM or SIGINT phraseforge drains the bus first, then stops HTTP (10 s limit). `phraseforge_bus_requests_total{operation,outcome}` counts the requests.

## Tests

`go test ./...` runs everything except the database tests of `internal/vocabulary`, `internal/models` and the vocabulary and models API tests of `internal/server`, which are skipped unless `PHRASEFORGE_TEST_DSN` is set to a Postgres URL (the admin config round trip and the checkpoint tests of `internal/jobs` use `PHRASEFORGE_TEST_DB` instead; the other `internal/jobs` database tests use `PHRASEFORGE_TEST_DSN` with a throwaway schema). Each of those tests applies `internal/db/schema.sql` into its own throwaway schema and drops it afterwards, so a development database is safe to point at.
