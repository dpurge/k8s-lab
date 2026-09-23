# knowledge — semantic Markdown knowledge base

> Part of the [k8s-lab](../README.md) cluster. See the root README for cluster setup,
> `task` usage, ingress conventions, and shared infrastructure.

`knowledge` is a Go/Chi application for maintaining pre-chunked Markdown knowledge articles for
RAG. It provides:

- a browser GUI at `http://knowledge.localhost:8080/`, gated behind sign-up/login, with a dark/light
  theme toggle that remembers your last choice
- a JSON REST API under `/api/v1`
- semantic/vector search backed by Qdrant
- chat with your knowledge, with chat history stored in Postgres
- LLM-generated title/summary suggestions for a knowledge item, from its body
- embedding providers for Ollama and OpenAI-compatible APIs such as OpenRouter

## Application architecture

```text
knowledge/
├── main.go                           # serve or migrate entrypoint
├── internal/config/config.go          # runtime config: mounted YAML file + credential env vars
├── internal/auth/auth.go              # signup/login/session logic (bcrypt + Postgres sessions)
├── internal/embeddings/embeddings.go  # Ollama/OpenAI/fake embedding clients
├── internal/qdrant/qdrant.go          # Qdrant collection + CRUD/search logic
├── internal/db/db.go                  # Postgres schema/migrations (users, sessions, chats, jobs, operations)
├── internal/queue/queue.go            # single-worker, priority-ordered LLM operation queue used by chat/generate/ingest
├── internal/chat/chat.go              # chat persistence + RAG orchestration; replies run async via internal/queue
├── internal/generate/generate.go      # LLM-generated title/summary from a knowledge item's body, queued via internal/queue
├── internal/translate/translate.go    # translates text into the configured knowledge-base language
├── internal/ingest/ingest.go          # URL/file → draft chunks pipeline, chained through internal/queue (chunk.go, html.go, drafts.go, source.go)
├── internal/jobs/jobs.go              # async job tracking (start/step/finish/current), polled by the UI
├── internal/server/server.go          # Chi router, REST handlers, session middleware, embedded GUI
├── internal/server/static/index.html  # vanilla HTML/CSS/JS GUI (theme toggle, logout)
├── internal/server/static/login.html  # login page
├── internal/server/static/signup.html # sign-up page
├── internal/server/static/theme.js    # shared dark/light theme toggle + localStorage persistence
├── internal/server/static/vendor/     # vendored (no CDN) markdown-it + DOMPurify for chat rendering
├── internal/server/static/components/ # kb-* Web Components (kb-button, kb-nav, kb-card, kb-field, kb-dialog, kb-status-bar, kb-message, kb-job-card); component.test.js per one, Node+jsdom
└── k8s/
    ├── configmap.yaml                 # mounted config (models, prompts, thresholds)
    ├── deployment.yaml                # Deployment, Service, Ingress
    └── jobs/migrate-job.yaml          # creates/validates Qdrant collection
```

## Data and indexing model

The application does **not** chunk articles. The caller/UI is responsible for loading articles at
the granularity desired by the RAG system.

One knowledge item = one Qdrant point:

```json
{
  "id": "...",
  "title": "Short title",
  "summary": "One-paragraph summary.",
  "body": "# Full Markdown article\n...",
  "tags": ["k8s", "headlamp"],
  "embedding_model": "nomic-embed-text",
  "created_at": "2026-09-14T12:00:00Z",
  "updated_at": "2026-09-14T12:05:00Z"
}
```

**Draft items:** chunks produced by the ingest pipeline (`POST /api/v1/ingest/url` or
`POST /api/v1/ingest/text`) are stored in a separate `knowledge_drafts` Postgres table and never
written to Qdrant. Each draft has an `id`, `job_id` (reference to the ingest job), `source_kind`
(url/text), `source_ref` (the URL or filename), `chunk_index`, `title`, `summary`, `body`, `tags`,
and timestamps. Drafts are purely for human review and staging — they never appear in search or
chat results until explicitly promoted into Qdrant (via the Ingest tab).

The embedded text is:

```text
Title: <title>

Summary: <summary>

Body:
<body>
```

Search embeds the query string and performs Qdrant vector search. Tag filtering is conjunctive:
when searching with `tag=k8s&tag=headlamp`, an item must contain **both** tags to be returned.
Semantic search defaults to the 7 most relevant items and caps requested limits at 20. Results
scoring below `qdrant.searchMinScore` are dropped entirely — Qdrant otherwise always returns its
nearest neighbors even when none are a good match, which let weak/irrelevant results reach the
chat model and get hallucinated over.

Chats are stored in Postgres. A chat may define scope tags; empty tags mean search all knowledge.
Retrieval embeds the current message concatenated with the immediately preceding user message (if
any), not the current message alone — a short follow-up like "answer my last question" otherwise
embeds too weakly to reliably find the right item. The app then retrieves the 5 most relevant
Qdrant knowledge items, fetches each one's full body in code and primes it with its existing
Summary (no model tool-calling — the chat model only ever sees plain text, never a `tools`
request, so it also works with completion-only models like `llama3-chatqa`), replays up to the
last 3 exchanges of chat history to the model for continuity, and returns references with
clickable links back to the Knowledge UI.

## Configuration

Most configuration lives in a YAML file — `knowledge/k8s/configmap.yaml`'s `ConfigMap`, mounted
into both the Deployment and the migrate Job at `/etc/knowledge/config.yaml` (override the path
with `CONFIG_FILE`). A missing file falls back to built-in defaults (so `go run .` works locally
without a cluster); a malformed one is a startup error, not a silently-ignored one. Any field the
file omits keeps its default — the file doesn't need to be complete.

```yaml
bindAddr: 0.0.0.0:8300
qdrant:
  url: http://localhost:6333
  collection: knowledge
  searchMinScore: 0.4          # min Qdrant cosine-similarity score to keep a semantic search
                                # result; 0 (or negative) disables filtering. Applies to general
                                # search and chat retrieval alike.
postgres:
  host: localhost
  port: "5432"
  database: knowledge
embeddings:
  provider: ollama              # ollama, openai, or fake
  baseURL: http://localhost:11434
  model: nomic-embed-text
  dimension: 768                # must match the model and the existing Qdrant collection
chat:
  provider: ollama
  baseURL: http://localhost:11434
  model: gemma4:12b              # the client always sends think: false — a thinking-capable
                                  # model otherwise incurs a large hidden-reasoning latency cost
                                  # (confirmed: ~150s vs ~14s on the same request)
  numCtx: 0                      # Ollama options.num_ctx; 0 omits it (Ollama's own default
                                  # applies). Set to the model's real trained context when full
                                  # documents in the prompt need more room than that default.
generate:
  provider: ollama                # used by the knowledge-item "Generate" title/summary buttons;
  baseURL: http://localhost:11434 # independently configurable from chat because not every model
  model: gemma4:12b                # can do this — llama3-chatqa:8b (completion-only QA) hallucinated
  numCtx: 0                        # an unrelated continuation instead of a title when tried
knowledgeLanguage: English         # the knowledge base's target language; fixed per deployment
translate:
  provider: ollama
  baseURL: http://localhost:11434
  model: gemma4:12b  # same model used for chat/generate; chosen after live comparison showed comparable translation quality
  numCtx: 0
prompts:
  chat: |
    You answer using only the retrieved knowledge documents below.

    If unsupported by the retrieved knowledge documents, say you do not know.
  generateTitle: >-
    You write a short, specific title for the given Markdown document.
    Respond with only the title text on a single line — no quotes, no
    punctuation at the end, no preamble like "Title:".
  generateSummary: >-
    You write a one-paragraph summary of the given Markdown document,
    for use as a search-result preview. Respond with only the summary
    text — no preamble like "Summary:", no quotes.
  translate: >-
    If the following text is already in {{language}}, return it
    unchanged. Otherwise, translate it into {{language}}. Respond with
    only the resulting text — no preamble, no explanation.
```

**Credentials are never in this file** — they stay as plain (or Kubernetes-Secret-sourced) env
vars, exactly as before: `PGUSER`/`PGPASSWORD` (from the `postgres-credentials` Secret in k8s),
and `EMBEDDINGS_API_KEY`/`CHAT_API_KEY`/`GENERATE_API_KEY` (empty by default; needed for
OpenAI/OpenRouter).

The migration validates collection vector size. If the collection already exists with a different
size, migration fails; use a matching model/dimension or recreate the collection.

### Ollama notes

For local testing on the host:

```sh
ollama pull nomic-embed-text
```

The k8s manifest points at Docker Desktop/Rancher Desktop's host alias:

```text
http://host.docker.internal:11434
```

Your Ollama process must listen on an address reachable from the k3d node. If Ollama only listens
on `127.0.0.1`, pods may get `connection refused`. Run Ollama with a reachable bind address, for
example:

```sh
OLLAMA_HOST=0.0.0.0:11434 ollama serve
```

Or edit `embeddings.baseURL`/`chat.baseURL`/`generate.baseURL` in
`knowledge/k8s/configmap.yaml` (shared by the Deployment and the migrate Job) to point at an
Ollama server on your LAN.

### OpenAI / OpenRouter-compatible embeddings

Set the provider to `openai` in the config file; the app calls `<embeddings.baseURL>/embeddings`
with a bearer token from the `EMBEDDINGS_API_KEY` env var (a credential — never in the config
file). For OpenAI:

```yaml
embeddings:
  provider: openai
  baseURL: https://api.openai.com/v1
  model: text-embedding-3-small
  dimension: 1536
```
```env
EMBEDDINGS_API_KEY=sk-...
```

For OpenRouter, use its OpenAI-compatible base URL and the embedding model ID/dimension you choose:

```yaml
embeddings:
  provider: openai
  baseURL: https://openrouter.ai/api/v1
  model: <openrouter-embedding-model>
  dimension: <model-dimension>
```
```env
EMBEDDINGS_API_KEY=...
```

## Deploy

```sh
task start-k8s
task deploy-postgres
task deploy-knowledge
```

`task deploy-knowledge` builds the image, ensures Qdrant is deployed, runs the Postgres/Qdrant migration,
and deploys the GUI/API. Postgres is required for chat persistence.

Open:

```text
http://knowledge.localhost:8080/
```

The GUI also includes a link to the Qdrant console:

```text
http://qdrant.localhost:8080/dashboard
```

Delete only the app:

```sh
task delete-knowledge
```

Re-run only the Qdrant collection migration:

```sh
task migrate-knowledge-db
```

## Authentication

The whole app — the GUI and every `/api/v1` route except the auth endpoints themselves — requires a
logged-in session. There is no per-user data isolation: everyone with an account shares the same
knowledge base and chats; signup/login is purely an access gate.

Visiting the app while logged out redirects to `/login.html`; calling the API while logged out
returns `401 unauthorized`. Sign up is open — anyone who can reach the app can create an account.

```sh
BASE=http://knowledge.localhost:8080/api/v1

# Sign up (also logs you in) and log in both set an httpOnly session cookie.
curl -sS -c cookies.txt -X POST "$BASE/auth/signup" \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"at-least-8-chars"}' | jq .

curl -sS -c cookies.txt -X POST "$BASE/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"at-least-8-chars"}' | jq .

# Use the cookie for authenticated requests.
curl -sS -b cookies.txt "$BASE/auth/me" | jq .
curl -sS -b cookies.txt "$BASE/knowledge" | jq .

curl -i -b cookies.txt -X POST "$BASE/auth/logout"
```

Sessions live in Postgres (`sessions` table, 30-day expiry) and are checked on every request; there
is no server-side session cache. Passwords are hashed with bcrypt (`users` table); the API never
returns a password or hash.

## API

Base URL used below:

```sh
BASE=http://knowledge.localhost:8080/api/v1
```

### Health/readiness

```sh
curl -i http://knowledge.localhost:8080/health
curl -i http://knowledge.localhost:8080/ready
```

`/health` checks the HTTP server. `/ready` checks Qdrant connectivity.

### Create a knowledge item

```sh
curl -sS -X POST "$BASE/knowledge" \
  -H 'Content-Type: application/json' \
  -d '{
    "title": "Headlamp in k8s-lab",
    "summary": "How to deploy and log into Headlamp in the local development cluster.",
    "body": "# Headlamp\n\nRun `task deploy-headlamp`, open the browser UI, and paste the ID token.",
    "tags": ["k8s", "headlamp", "gui"]
  }' | jq .
```

Create embeds `title + summary + body` and stores the vector in Qdrant.

### List items without semantic query

```sh
curl -sS "$BASE/knowledge" | jq .
```

List/search responses omit `body` by default:

```json
{
  "items": [
    {
      "id": "...",
      "title": "Headlamp in k8s-lab",
      "summary": "How to deploy and log into Headlamp in the local development cluster.",
      "tags": ["gui", "headlamp", "k8s"],
      "created_at": "2026-09-14T12:14:04Z",
      "updated_at": "2026-09-14T12:14:04Z"
    }
  ]
}
```

### Semantic search with tag filter

`q` is embedded and searched against Qdrant vectors. `tag` may be repeated; all requested tags are
required.

```sh
curl -sS --get "$BASE/knowledge" \
  --data-urlencode 'q=How do I open the Kubernetes dashboard?' \
  --data-urlencode 'tag=k8s' \
  --data-urlencode 'tag=headlamp' \
  --data-urlencode 'limit=5' | jq .
```

Semantic search results include `score`:

```json
{
  "items": [
    {
      "id": "...",
      "title": "Headlamp in k8s-lab",
      "summary": "How to deploy and log into Headlamp in the local development cluster.",
      "tags": ["gui", "headlamp", "k8s"],
      "created_at": "...",
      "updated_at": "...",
      "score": 0.73
    }
  ]
}
```

### Semantic search with JSON body

```sh
curl -sS -X POST "$BASE/knowledge/search" \
  -H 'Content-Type: application/json' \
  -d '{
    "query": "How do I open the Kubernetes dashboard?",
    "tags": ["k8s", "headlamp"],
    "limit": 5
  }' | jq .
```

### Filter by updated date range

`start` and `end` are optional RFC3339 timestamps applied to `updated_at`.

```sh
curl -sS --get "$BASE/knowledge" \
  --data-urlencode 'q=dashboard login token' \
  --data-urlencode 'tag=k8s' \
  --data-urlencode 'start=2026-01-01T00:00:00Z' \
  --data-urlencode 'end=2026-12-31T23:59:59Z' | jq .
```

### Bulk export

Returns every item (unlike list/search, unfiltered by default and always including `body`), as a
pretty-printed YAML download (`Content-Disposition: attachment; filename="knowledge-export.yaml"`).
Optional `tag` (repeatable) scopes the export the same way it scopes list/search — conjunctive, all
requested tags required.

```sh
curl -sS "$BASE/knowledge/export" -o knowledge-export.yaml

curl -sS --get "$BASE/knowledge/export" \
  --data-urlencode 'tag=k8s' \
  --data-urlencode 'tag=headlamp' -o headlamp-export.yaml
```

```yaml
items:
  - id: 9f1c2d34-5678-4abc-9def-0123456789ab
    title: Headlamp in k8s-lab
    summary: How to deploy and log into Headlamp in the local development cluster.
    body: |
      # Headlamp

      Run `task deploy-headlamp`.
    tags:
      - gui
      - headlamp
      - k8s
    embedding_model: bge-m3
    created_at: 2026-09-12T08:31:00Z
    updated_at: 2026-09-12T08:31:00Z
```

### Bulk import

Accepts the same `{items: [...]}` shape an export produces, so it round-trips a prior export
(backup/restore, or copying a tag subset into another instance). `created_at`/`updated_at` in the
file are informational only — always ignored on import; the server sets its own. Each row is
handled independently, so one bad row doesn't fail the batch:

- Only `body` is required — `title`, `summary`, and `tags` may all be omitted. If a **new** item's
  `title` or `summary` is missing, it's created with that field blank and a background operation
  is queued to fill it in from `body` (see
  [Background operation queue](#background-operation-queue)); the Knowledge list shows a
  "(generating title/summary…)" placeholder until it lands. Omitting `title`/`summary` on a row
  that updates an **existing** `id` instead keeps that item's current value rather than blanking
  it out.
- An item **with** an `id` that already exists, and whose `title`/`summary`/`body`/`tags` all
  exactly match what's already stored, is skipped entirely — no write, no re-embedding. Re-embedding
  is a real LLM/embedding cost, only paid when content actually changed.
- An item with an `id` that already exists but differs is updated in place and re-embedded,
  preserving the original `created_at`.
- An item with no `id`, or an `id` that doesn't exist yet, is created (new random `id` if none
  given) and embedded.
- An item with `delete: true` and an `id` is deleted permanently. A missing target is a no-op, not
  an error — safe to reimport the same file twice.

```sh
curl -sS -X POST "$BASE/knowledge/import" \
  -H 'Content-Type: application/yaml' \
  --data-binary @knowledge-export.yaml
```

```json
{ "imported": 1, "deleted": 0, "unchanged": 8, "errors": [] }
```

A row missing `title`/`summary`/`body` (and not marked `delete: true`) is skipped and reported, not
fatal to the rest of the batch:

```json
{
  "imported": 3,
  "deleted": 0,
  "unchanged": 0,
  "errors": [
    { "index": 4, "id": "...", "message": "title, summary, and body are required" }
  ]
}
```

Request body is capped at 8 MiB (`413 payload_too_large` beyond that — a whole-collection export,
not a single item, so this cap is much larger than ingest's per-source 2 MiB cap). Malformed YAML
anywhere in the file rejects the whole request (`400 invalid_yaml`) — YAML's indentation
sensitivity makes this more likely than with JSON, so a syntax mistake near a `delete: true` row
can reject rows that were otherwise fine.

In the GUI, use Export/Import in the Knowledge tab; Export has its own tags field (separate from
the search filter) — leave it empty to export everything, or list tags to scope the export. Import
reads a local `.yaml`/`.yml` file (the same shape Export produces) and reports how many rows were
imported, deleted, unchanged, and failed.

### Fetch a full item by ID

```sh
ID=<item-id>
curl -sS "$BASE/knowledge/$ID" | jq .
```

This returns `body`.

### Update an item

Updates replace `title`, `summary`, `body`, and `tags`; preserve `created_at`; set a new
`updated_at`; and re-embed the item.

```sh
curl -sS -X PUT "$BASE/knowledge/$ID" \
  -H 'Content-Type: application/json' \
  -d '{
    "title": "Headlamp in k8s-lab, updated",
    "summary": "Updated Headlamp deployment and login notes.",
    "body": "# Headlamp\n\nUpdated Markdown body.",
    "tags": ["k8s", "headlamp", "dashboard"]
  }' | jq .
```

### Delete an item

```sh
curl -i -X DELETE "$BASE/knowledge/$ID"
```

Successful deletes return `204 No Content`.

### Chat with knowledge

Create a chat. Empty `tags` searches all knowledge; non-empty tags scope retrieval conjunctively.

```sh
CHAT_ID=$(curl -sS -X POST "$BASE/chats" \
  -H 'Content-Type: application/json' \
  -d '{"title":"Kubernetes notes","tags":["k8s"]}' | jq -r .id)
```

Send a message:

```sh
curl -sS -X POST "$BASE/chats/$CHAT_ID/messages" \
  -H 'Content-Type: application/json' \
  -d '{"message":"How do I open Headlamp?"}' | jq .
```

This returns immediately with only `user_message` — it does not wait on the LLM. The reply is
produced by a background operation (see [Background operation queue](#background-operation-queue))
and appears asynchronously as a new `assistant`-role message the next time the chat is fetched:

```sh
curl -sS "$BASE/chats/$CHAT_ID" | jq .messages
```

Each assistant message carries `sources` containing `title`, `summary`, `score`, `tags`, and a
`url` link back to the Knowledge UI for each referenced item. In the Chat UI, Enter sends the
prompt and Shift+Enter inserts a new line; the UI polls for the reply and shows a status-bar
message while it's pending. Message content (both roles) is rendered as Markdown — headers,
bold/italic, inline and fenced code, lists, tables, links, and images — via a vendored
`markdown-it` + `DOMPurify` pipeline (see `knowledge/internal/server/static/vendor/`), not raw
text; this is also why the reference list above renders as a real bulleted list.

Delete a chat and all its messages:

```sh
curl -i -X DELETE "$BASE/chats/$CHAT_ID"
```

### Generate title/summary

Suggest a title or summary from a knowledge item's body, using `generate.model` (independent of
`chat.model`). The Knowledge UI's editor has a "Generate" button next to each field that does
this; either result can be edited before Save.

```sh
curl -sS -X POST "$BASE/knowledge/generate/title" \
  -H 'Content-Type: application/json' \
  -d '{"body":"Kubernetes namespaces isolate cluster resources into logical groups for organization and access control."}' | jq .
```

The same request against `/knowledge/generate/summary` returns `{"summary": "..."}` instead. An
empty `body` returns `400 validation_failed` without calling the model.

### Ingest

Ingest a URL or text file into the knowledge base as draft chunks pending human review. Both endpoints
start an async job and return `202` with the job status. The job processes the source, chunks it,
translates and generates a title/summary for each chunk, and stores the results in a staging table
(never written to Qdrant until explicitly approved via the Ingest tab).

**Ingest a URL:**

```sh
curl -sS -X POST "$BASE/ingest/url" \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com/article","tags":["docs","example"]}' | jq .
```

Request:
- `url` (required, string): the HTTP(S) URL to fetch
- `tags` (optional, array of strings): tags to apply to all chunks from this source

Response: `202` with a job object `{"id","kind","status","step","created_at","updated_at"}` once
the job starts running.

Error codes:
- `400 validation_failed` — missing/empty url, invalid scheme (must be http/https), or invalid host
- `409 job_running` — another ingest job is already running; only one job can run at a time
- `500 internal` — fetch or processing error; check the job's error field via `/api/v1/jobs/current`

**Ingest a text file:**

```sh
curl -sS -X POST "$BASE/ingest/text" \
  -H 'Content-Type: application/json' \
  -d '{"filename":"readme.md","content":"# My Document\n\nContent here...","tags":["docs"]}' | jq .
```

Request:
- `filename` (required, string): the file name (extension must be `.txt`, `.md`, or `.markdown`)
- `content` (required, string): the file contents (max 2 MiB)
- `tags` (optional, array of strings): tags to apply to all chunks from this source

Response: same as URL ingest — `202` with a job object.

Error codes:
- `400 validation_failed` — missing/empty filename, invalid extension, or invalid UTF-8
- `409 job_running` — another ingest job is already running
- `413 payload_too_large` — request body exceeds 2 MiB plus headroom for JSON structure
- `500 internal` — processing error; check `/api/v1/jobs/current`

**List draft chunks:**

```sh
curl -sS "$BASE/ingest/drafts" | jq .
```

Response: `200` with an array of draft chunk summaries (most recent first, up to 500):

```json
{
  "drafts": [
    {
      "id": "...",
      "job_id": "...",
      "source_kind": "url",
      "source_ref": "https://example.com/article",
      "chunk_index": 0,
      "title": "Generated title for this chunk",
      "summary": "Generated summary for this chunk",
      "tags": ["docs", "example"],
      "created_at": "2026-09-21T14:32:00Z",
      "updated_at": "2026-09-21T14:32:00Z"
    }
  ]
}
```

**Get a full draft by ID:**

```sh
ID=<draft-id>
curl -sS -b cookies.txt "$BASE/ingest/drafts/$ID" | jq .
```

Response: `200` with the full `Draft` (including `body`, unlike the list endpoint):

```json
{
  "id": "...",
  "job_id": "...",
  "source_kind": "url",
  "source_ref": "https://example.com/article",
  "chunk_index": 0,
  "title": "Generated title for this chunk",
  "summary": "Generated summary for this chunk",
  "body": "# Full Markdown body of this chunk",
  "tags": ["docs", "example"],
  "created_at": "2026-09-21T14:32:00Z",
  "updated_at": "2026-09-21T14:32:00Z"
}
```

Error codes:
- `404 not_found` — draft does not exist or id is malformed
- `500 internal` — database error

**Update a draft:**

```sh
curl -sS -b cookies.txt -X PUT "$BASE/ingest/drafts/$ID" \
  -H 'Content-Type: application/json' \
  -d '{
    "title": "Edited title",
    "summary": "Edited summary",
    "body": "# Edited Markdown body",
    "tags": ["docs"]
  }' | jq .
```

Request:
- `title` (required, string): new title (non-empty)
- `summary` (required, string): new summary (non-empty)
- `body` (required, string): new body (non-empty, max 256 KiB)
- `tags` (optional, array of strings): new tags

Response: `200` with the updated `Draft` (same shape as Get).

Error codes:
- `400 validation_failed` — title/summary/body are empty or missing
- `404 not_found` — draft does not exist or id is malformed
- `413 payload_too_large` — request body exceeds 256 KiB
- `500 internal` — database error

**Approve and promote a draft:**

Promote a draft into Qdrant as a real, searchable knowledge item via the same creation path the manual "add knowledge item" flow uses. The draft is deleted if the promotion succeeds; if it fails, the draft remains unchanged.

```sh
curl -sS -b cookies.txt -X POST "$BASE/ingest/drafts/$ID/approve" | jq .
```

Response: `201` with the new knowledge item's id:

```json
{
  "id": "<new knowledge item id>"
}
```

Error codes:
- `400 validation_failed` — draft has an empty title, summary, or body (rare; would indicate a bug in the ingest pipeline)
- `404 not_found` — draft does not exist, id is malformed, or the draft was already promoted by another request
- `500 internal` — Qdrant error, network error, or database error; the draft is unchanged

**Discard a draft:**

Permanently delete a draft without promoting it.

```sh
curl -sS -b cookies.txt -X DELETE "$BASE/ingest/drafts/$ID"
```

Response: `204 No Content` (idempotent — returns 204 whether or not the draft existed).

Error codes:
- `500 internal` — database error

### Job status

The most recently updated running job, if any — polled by the footer status line in the Knowledge
UI every few seconds, visible on any tab. Ingest jobs (started by `POST /api/v1/ingest/url` or
`POST /api/v1/ingest/text`) drive this endpoint during a fetch+process run, reporting
fetch/chunk/translate/generate/save progress at each stage.

```sh
curl -sS -w '\n%{http_code}\n' -b cookies.txt "$BASE/jobs/current"
```

`200` with `{"id","kind","status","step","created_at","updated_at"}` (plus `"error"`,
`"source_kind"`, `"source_ref"`, `"source_tags"` if the job has a stored source) when a job is
running; `204` with no body otherwise.

### Job history, retry, and delete

Unlike `/jobs/current`, this lists every job regardless of status (most recent first, capped at
500), so a failed job's error stays visible after it stops running:

```sh
curl -sS -b cookies.txt "$BASE/jobs"
```

```json
{ "jobs": [ { "id": "...", "kind": "ingest", "status": "failed", "step": "fetching example.com", "error": "...", "source_kind": "url", "source_ref": "https://example.com/page", "source_tags": ["docs"], "created_at": "...", "updated_at": "..." } ] }
```

Retry a failed job from its stored source — starts a brand-new job (the original stays in history
unchanged). Reuses the content already acquired on the failed attempt rather than redoing the
work: a URL source reuses the text already fetched (only re-fetches if the original attempt never
got that far), and a file/text source reuses the originally uploaded text, same as before:

```sh
curl -sS -b cookies.txt -X POST "$BASE/jobs/$ID/retry"
```

Response: `202` with the new job (same shape as starting an ingest job directly).

Error codes:
- `404 not_found` — no such job, or malformed id
- `409 job_running` — another job of that kind is already running
- `409 not_retryable` — the job already succeeded, or has no stored source (e.g. a job row from
  before this feature existed)
- `400 validation_failed` — the stored source itself fails revalidation (e.g. a URL that's no
  longer well-formed)

Delete a job's history row permanently:

```sh
curl -sS -b cookies.txt -X DELETE "$BASE/jobs/$ID"
```

Response: `204` (idempotent — returns 204 whether or not the job existed). Refuses a still-running
job with `409 job_running`, since removing that row mid-run would silently defeat the
single-concurrent-job-per-kind guard.

### Background operation queue

Every LLM-calling code path — chat replies, manual title/summary generation, and ingest's
per-chunk pipeline (translate, title, summary, save draft) — runs through one app-wide operation
queue (`internal/queue`) with a single worker: **at most one LLM call happens at a time**, never
concurrently. Two priorities exist:

- **Interactive** — chat replies and manual "Generate" clicks. Always processed ahead of any
  pending background work, so the app stays responsive even while a large ingest job is running.
- **Background** — ingest's chunk-by-chunk pipeline. Only runs when no interactive work is
  pending; completing one chunk enqueues the next rather than looping in-process, so an
  interactive request that arrives in between gets a chance to run first.

This is why `POST .../messages` returns immediately (see [Chat with knowledge](#chat-with-knowledge))
while `/knowledge/generate/title` and `/knowledge/generate/summary` still block until the model
responds — both are interactive-priority, but generate's HTTP handler waits for its own operation
to finish (usually fast, since nothing outranks it) while chat's doesn't wait at all. A fired
operation is durable — it completes even if the original HTTP request that triggered it is long
gone, so a slow reply is never silently lost. Every LLM call, ingest job start/finish, data
import, and knowledge item create/update/delete is logged (structured, `log/slog` JSON to
stdout) with duration and outcome, never content.

## Validation and errors

Create/update require non-empty `title`, `summary`, and `body`. Tags are normalized to lowercase,
trimmed, sorted, and deduplicated.

Example validation failure:

```sh
curl -sS -X POST "$BASE/knowledge" \
  -H 'Content-Type: application/json' \
  -d '{"title":"","summary":"","body":"","tags":[]}' | jq .
```

```json
{
  "error": {
    "code": "validation_failed",
    "message": "title, summary, and body are required"
  }
}
```

A missing item returns `404` with `not_found`.
