# knowledge — semantic Markdown knowledge base

> Part of the [k8s-lab](../README.md) cluster. See the root README for cluster setup,
> `task` usage, ingress conventions, and shared infrastructure.

`knowledge` is a Go/Chi application for maintaining pre-chunked Markdown knowledge articles for
RAG. It provides:

- a browser GUI at `http://knowledge.localhost:8080/`
- a JSON REST API under `/api/v1`
- semantic/vector search backed by Qdrant
- chat with your knowledge, with chat history stored in Postgres
- embedding providers for Ollama and OpenAI-compatible APIs such as OpenRouter

## Application architecture

```text
knowledge/
├── main.go                           # serve or migrate entrypoint
├── internal/config/config.go          # runtime config from environment
├── internal/embeddings/embeddings.go  # Ollama/OpenAI/fake embedding clients
├── internal/qdrant/qdrant.go          # Qdrant collection + CRUD/search logic
├── internal/db/db.go                  # Postgres chat schema/migrations
├── internal/chat/chat.go              # chat persistence + RAG orchestration
├── internal/server/server.go          # Chi router, REST handlers, embedded GUI
├── internal/server/static/index.html  # vanilla HTML/CSS/JS GUI
└── k8s/
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

The embedded text is:

```text
Title: <title>

Summary: <summary>

Body:
<body>
```

Search embeds the query string and performs Qdrant vector search. Tag filtering is conjunctive:
when searching with `tag=k8s&tag=headlamp`, an item must contain **both** tags to be returned.
Semantic search defaults to the 7 most relevant items and caps requested limits at 20.

Chats are stored in Postgres. A chat may define scope tags; empty tags mean search all knowledge.
When answering, the app retrieves up to 7 Qdrant knowledge items, lets the local chat model read
full bodies for retrieved documents if needed, and returns references with clickable links back to
the Knowledge UI.

## Configuration

| Environment variable | Default | Notes |
|---|---|---|
| `BIND_ADDR` | `0.0.0.0:8300` | HTTP bind address. |
| `QDRANT_URL` | `http://localhost:6333` | In k8s: `http://qdrant.data.svc.cluster.local:6333`. |
| `QDRANT_COLLECTION` | `knowledge` | Qdrant collection name. |
| `EMBEDDINGS_PROVIDER` | `ollama` | `ollama`, `openai`, or `fake`. |
| `EMBEDDINGS_BASE_URL` | `http://localhost:11434` | In k8s defaults to `http://host.k3d.internal:11434`. |
| `EMBEDDINGS_API_KEY` | empty | Required for OpenAI/OpenRouter. |
| `EMBEDDINGS_MODEL` | `nomic-embed-text` | Embedding model ID. |
| `EMBEDDINGS_DIMENSION` | `768` | Must match the embedding model and existing Qdrant collection. |
| `CHAT_PROVIDER` | `ollama` | Chat provider. Currently Ollama. |
| `CHAT_BASE_URL` | `http://localhost:11434` | In k8s: `http://host.docker.internal:11434`. |
| `CHAT_MODEL` | `gemma4:e4b` | Ollama chat model. |

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

Or edit `EMBEDDINGS_BASE_URL` in `knowledge/k8s/deployment.yaml` and
`knowledge/k8s/jobs/migrate-job.yaml` to point at an Ollama server on your LAN.

### OpenAI / OpenRouter-compatible embeddings

Set the provider to `openai`; the app calls `<EMBEDDINGS_BASE_URL>/embeddings` with a bearer token.
For OpenAI:

```env
EMBEDDINGS_PROVIDER=openai
EMBEDDINGS_BASE_URL=https://api.openai.com/v1
EMBEDDINGS_API_KEY=sk-...
EMBEDDINGS_MODEL=text-embedding-3-small
EMBEDDINGS_DIMENSION=1536
```

For OpenRouter, use its OpenAI-compatible base URL and the embedding model ID/dimension you choose:

```env
EMBEDDINGS_PROVIDER=openai
EMBEDDINGS_BASE_URL=https://openrouter.ai/api/v1
EMBEDDINGS_API_KEY=...
EMBEDDINGS_MODEL=<openrouter-embedding-model>
EMBEDDINGS_DIMENSION=<model-dimension>
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

The response includes the assistant answer plus `sources` containing `title`, `summary`, `score`,
`tags`, and a `url` link back to the Knowledge UI for each referenced item. In the Chat UI, Enter
sends the prompt and Shift+Enter inserts a new line.

Delete a chat and all its messages:

```sh
curl -i -X DELETE "$BASE/chats/$CHAT_ID"
```

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
