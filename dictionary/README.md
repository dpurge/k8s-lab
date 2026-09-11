# dictionary — multilingual dictionary API

> Part of the [k8s-lab](../README.md) cluster. See the root README for cluster setup, `task`
> usage, image builds, and shared infrastructure (Postgres, Qdrant, Adminer).

A Go/Chi REST API for multilingual dictionary entries — phrases, grammar tags, translations,
notes, and inflection tables — backed by its own Postgres database. API-only, no GUI.
Replaces the earlier `phraseforge-api` service. Implemented: health/readiness, users, per-language
role grants, short-lived bearer tokens, the language catalog + each language's own grammar
schema, dictionary entries + translations + notes, and inflection templates + forms — the
full feature set from the original design.

Three roles exist: **admin** is global and can do anything. **writer** and **reader** always
apply to one language — a user can hold different roles on different languages. A **write**
token (15 min TTL) requires a writer (or admin) grant for the target language; a **read**
token (24h TTL) accepts a writer or reader (or admin) grant — writer implies read access.
Requesting a token with no `language` only succeeds for an admin account.

On first start with an empty database, the server bootstraps `admin`/`dictionary-admin` with
the global admin role — change or replace this before deploying anywhere but this local lab.

## Deploy and migrate

```sh
task start-k8s            # cluster must be running first
task deploy-postgres       # Postgres + Adminer (see root README)
task deploy-dictionary      # build the image, check its size, migrate, apply dictionary/k8s/, wait for Ready
```

`task deploy-dictionary` builds and size-checks the image, runs the database migration Job
(`dictionary/k8s/jobs/migrate-job.yaml` — safe to rerun, a no-op after the first run), then
applies `dictionary/k8s/deployment.yaml` (Deployment/Service/Ingress). Run
`task migrate-dictionary-db` on its own later if you need to re-apply the schema without a full
redeploy (e.g. after adding a new migration). `task delete-dictionary` removes the
Deployment/Service/Ingress only — it leaves the migration Job and the database alone.

## Ingress host

| Host | Service |
|---|---|
| `dictionary.localhost:8080` | REST API — see the endpoints documented below |

```sh
curl -H "Host: dictionary.localhost" http://localhost:8080/health
curl -H "Host: dictionary.localhost" http://localhost:8080/ready
```

## API: auth, users, roles

All request/response bodies are UTF-8 JSON. Errors are `{"error":{"code":"...","message":"..."}}`.

**Mint a token** — `POST /api/v1/auth/token`. `scope` is `"write"` or `"read"`; `language` (a
3-letter ISO code) is required unless the account holds the global admin role.

```sh
curl -sS -X POST -H "Host: dictionary.localhost" -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"dictionary-admin","scope":"write"}' \
  http://localhost:8080/api/v1/auth/token
```

The remaining endpoints all require `Authorization: Bearer <admin token>` — only admin manages
users and role grants.

**Create a user** — `POST /api/v1/users`

```sh
curl -sS -X POST -H "Host: dictionary.localhost" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"username":"alice","password":"a-strong-password"}' \
  http://localhost:8080/api/v1/users
```

**List users** — `GET /api/v1/users`

```sh
curl -sS -H "Host: dictionary.localhost" -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:8080/api/v1/users
```

**Grant a role** — `POST /api/v1/users/{id}/roles`. `role` is `"admin"` (global — omit
`language`), `"writer"`, or `"reader"` (both require `language`).

```sh
curl -sS -X POST -H "Host: dictionary.localhost" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"role":"writer","language":"pol"}' \
  http://localhost:8080/api/v1/users/2/roles
```

**List a user's role grants** — `GET /api/v1/users/{id}/roles`

```sh
curl -sS -H "Host: dictionary.localhost" -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:8080/api/v1/users/2/roles
```

**Revoke a role grant** — `DELETE /api/v1/users/{id}/roles/{grantID}`

```sh
curl -sS -X DELETE -H "Host: dictionary.localhost" -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://localhost:8080/api/v1/users/2/roles/3
```

## API: languages and grammar schema

A language is set public with `is_public: true` — its content (and, once entries exist, its
dictionary) is then readable by anyone with no token. A private language requires a reader,
writer, or admin token scoped to it; admin tokens always pass. The language catalog itself
(`GET /languages`) is always public — only content is gated.

**Create a language** (admin) — `POST /api/v1/languages`

```sh
curl -sS -X POST -H "Host: dictionary.localhost" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"code":"pol","name":"Polish","is_public":false,"has_transcription":false}' \
  http://localhost:8080/api/v1/languages
```

**Update a language** (admin) — `PUT /api/v1/languages/{code}`

```sh
curl -sS -X PUT -H "Host: dictionary.localhost" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"name":"Polish","is_public":true,"has_transcription":false}' \
  http://localhost:8080/api/v1/languages/pol
```

**List languages** (public, no token)

```sh
curl -sS http://localhost:8080/api/v1/languages -H "Host: dictionary.localhost"
```

**Get one language** — `GET /api/v1/languages/{code}` — public if `is_public`, else needs a
reader/writer/admin token for that language

```sh
curl -sS -H "Host: dictionary.localhost" http://localhost:8080/api/v1/languages/pol
```

**Replace a language's grammar schema** (admin) — `PUT /api/v1/languages/{code}/schema`. Full
replace, not a merge. `categories` are the grammatical tag sets a language defines (a
`word_class` category is required if any `templates` are given); each `templates` entry names,
per word class, the *other* categories an entry of that class may carry — a ceiling, not a
requirement, so an entry may use any subset. This is what stops a verb from taking a `gender`
tag if the verb template never lists `gender`. Tags may mix uppercase and lowercase (e.g. `V`,
`N`, `VP` for word classes — case is significant, `"V"` and `"v"` are different tags, never
folded together).

```sh
curl -sS -X PUT -H "Host: dictionary.localhost" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{
    "categories": [
      {"key":"word_class","tag_values":["V","N"]},
      {"key":"tense","tag_values":["praes","past"]},
      {"key":"number","tag_values":["sg","pl"]},
      {"key":"person","tag_values":["1","2","3"]},
      {"key":"gender","tag_values":["m","f","n"]}
    ],
    "templates": [
      {"word_class":"V","allowed_categories":["tense","person","number"]},
      {"word_class":"N","allowed_categories":["gender","number"]}
    ]
  }' \
  http://localhost:8080/api/v1/languages/eng/schema
```

**Get a language's grammar schema** — `GET /api/v1/languages/{code}/schema` — same public/token
rule as getting the language itself

```sh
curl -sS -H "Host: dictionary.localhost" http://localhost:8080/api/v1/languages/eng/schema
```

## API: dictionary entries, translations, notes

An entry's identity is `(language, phrase, tags)` — the same phrase with different grammar
tags is a different entry. `tags` must include exactly one tag from the language's `word_class`
category, plus any subset (including none) of that word class's allowed categories per its
template; tags are accepted in any order and stored canonically (word class first), so
resubmitting the same tag set in a different order is recognized as the same entry, not a new
one. One entry may have several translations, including more than one into the same target
language; `notes` (markdown) belongs to one specific translation, not the entry as a whole.
Writing always needs a writer (or admin) token for the entry's language, public or not; reading
follows the same public/token rule as the language itself.

**Create an entry** (writer/admin) — `POST /api/v1/languages/{code}/entries`

```sh
curl -sS -X POST -H "Host: dictionary.localhost" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $WRITE_TOKEN" \
  -d '{"phrase":"be","tags":["V","praes"]}' \
  http://localhost:8080/api/v1/languages/eng/entries
```

**List entries** — `GET /api/v1/languages/{code}/entries` — optional `?phrase=` filters to one
phrase's entries (there may be several, one per distinct tag set)

```sh
curl -sS -H "Host: dictionary.localhost" http://localhost:8080/api/v1/languages/eng/entries?phrase=be
```

**Get one entry** (with its nested translations) — `GET /api/v1/languages/{code}/entries/{id}`

```sh
curl -sS -H "Host: dictionary.localhost" http://localhost:8080/api/v1/languages/eng/entries/2
```

**Update an entry** (writer/admin) — `PUT /api/v1/languages/{code}/entries/{id}` — replaces
phrase and tags, re-validated the same way as create

```sh
curl -sS -X PUT -H "Host: dictionary.localhost" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $WRITE_TOKEN" \
  -d '{"phrase":"be","tags":["V","past"]}' \
  http://localhost:8080/api/v1/languages/eng/entries/2
```

**Delete an entry** (writer/admin) — `DELETE /api/v1/languages/{code}/entries/{id}` — also
deletes its translations

```sh
curl -sS -X DELETE -H "Host: dictionary.localhost" -H "Authorization: Bearer $WRITE_TOKEN" \
  http://localhost:8080/api/v1/languages/eng/entries/2
```

**Add a translation** (writer/admin) — `POST /api/v1/languages/{code}/entries/{id}/translations`
— `notes` is optional markdown, specific to this translation

```sh
curl -sS -X POST -H "Host: dictionary.localhost" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $WRITE_TOKEN" \
  -d '{"language_code":"pol","text":"być","notes":"# Irregular\n\nHighly irregular verb."}' \
  http://localhost:8080/api/v1/languages/eng/entries/2/translations
```

**Update a translation** (writer/admin) — `PUT /api/v1/languages/{code}/entries/{id}/translations/{translationID}`

```sh
curl -sS -X PUT -H "Host: dictionary.localhost" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $WRITE_TOKEN" \
  -d '{"text":"być","notes":"updated note"}' \
  http://localhost:8080/api/v1/languages/eng/entries/2/translations/1
```

**Delete a translation** (writer/admin) — `DELETE /api/v1/languages/{code}/entries/{id}/translations/{translationID}`

```sh
curl -sS -X DELETE -H "Host: dictionary.localhost" -H "Authorization: Bearer $WRITE_TOKEN" \
  http://localhost:8080/api/v1/languages/eng/entries/2/translations/1
```

## API: inflection templates and forms

Optional per language. A **template** is identified by `index_tags` (e.g. `{V,praes}` = present
tense) and has a markdown `body` containing `{tag}` placeholders — literal tag values, not
positional indices. A **form** is one concrete wordform (e.g. `text: "am"`, `tags:
["V","praes","sg","1"]`) — not tied to any dictionary entry, just tagged data.

Rendering: a template applies when the *request's* tags are a subset of its `index_tags` (a
broad request like `{V}` matches every tense's template; a narrower `{V,praes}` matches only
that one). A template with no matching form data at all is silently skipped — only templates
that have *something* to show are returned. Each `{tag}` placeholder is filled from the one
form (among those covering this template) whose tags, minus the template's own `index_tags`,
contain that tag value.

**Create a template** (writer/admin) — `POST /api/v1/languages/{code}/inflection-templates`

```sh
curl -sS -X POST -H "Host: dictionary.localhost" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $WRITE_TOKEN" \
  -d '{"index_tags":["V","praes"],"body":"{1} {2} {3}"}' \
  http://localhost:8080/api/v1/languages/eng/inflection-templates
```

**Save forms in bulk** (writer/admin) — `POST /api/v1/languages/{code}/inflection-forms` —
re-saving the same tags updates the text rather than erroring, so bulk loading is safe to rerun

```sh
curl -sS -X POST -H "Host: dictionary.localhost" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $WRITE_TOKEN" \
  -d '{"forms":[
        {"text":"am","tags":["V","praes","sg","1"]},
        {"text":"are","tags":["V","praes","sg","2"]},
        {"text":"is","tags":["V","praes","sg","3"]}
      ]}' \
  http://localhost:8080/api/v1/languages/eng/inflection-forms
```

**List templates** / **list forms** — `GET /api/v1/languages/{code}/inflection-templates` /
`GET /api/v1/languages/{code}/inflection-forms`

```sh
curl -sS -H "Host: dictionary.localhost" http://localhost:8080/api/v1/languages/eng/inflection-templates
curl -sS -H "Host: dictionary.localhost" http://localhost:8080/api/v1/languages/eng/inflection-forms
```

**Update a template** (writer/admin) — `PUT /api/v1/languages/{code}/inflection-templates/{id}`

```sh
curl -sS -X PUT -H "Host: dictionary.localhost" -H "Content-Type: application/json" \
  -H "Authorization: Bearer $WRITE_TOKEN" \
  -d '{"index_tags":["V","praes"],"body":"{1} / {2} / {3}"}' \
  http://localhost:8080/api/v1/languages/eng/inflection-templates/1
```

**Delete a template** / **delete a form** (writer/admin)

```sh
curl -sS -X DELETE -H "Host: dictionary.localhost" -H "Authorization: Bearer $WRITE_TOKEN" \
  http://localhost:8080/api/v1/languages/eng/inflection-templates/1
curl -sS -X DELETE -H "Host: dictionary.localhost" -H "Authorization: Bearer $WRITE_TOKEN" \
  http://localhost:8080/api/v1/languages/eng/inflection-forms/1
```

**Render inflection tables for an entry** — `GET /api/v1/languages/{code}/entries/{id}/inflection`
— defaults to the entry's own tags; an optional `?tags=` overrides them (matching the original
design's own example: requesting `be {V}` or `be {V praes}` both return `"am are is"`)

```sh
curl -sS -H "Host: dictionary.localhost" http://localhost:8080/api/v1/languages/eng/entries/1/inflection
curl -sS -H "Host: dictionary.localhost" "http://localhost:8080/api/v1/languages/eng/entries/1/inflection?tags=V"
curl -sS -H "Host: dictionary.localhost" "http://localhost:8080/api/v1/languages/eng/entries/1/inflection?tags=V,praes"
```

## Additional recipes

| Command | What it does |
|---|---|
| `task migrate-dictionary-db` | Runs the dictionary database migration Job on its own (without a full redeploy); prints logs; safe to run repeatedly. |

`task deploy-dictionary` already checks the compressed image size against the 50 MB ceiling
(NFR-IMG-001) as part of its build step. The shared infrastructure tasks are documented in the
[root README](../README.md#deploying).
