---
version: 1
status: approved
updated: 2026-09-21
---

## Languages & runtimes

- **Go 1.26.5** — all application modules and shared code (dictionary, phraseforge, knowledge, shared)

## Frameworks & libraries

- **Chi HTTP router** (`go-chi/chi v5.3.2`) — request routing and middleware across all three applications
- **PostgreSQL driver** (`jackc/pgx v5.10.0`) — direct pgx usage, no ORM, in all applications and shared utilities
- **Crypto** (`golang.org/x/crypto v0.57.0`) — password hashing and bearer token generation
- **Shared LLM client** (`shared/llm`) — abstraction layer for Ollama and OpenAI-compatible endpoints, consumed by phraseforge and knowledge
- **Shared Postgres helpers** (`shared/postgres`) — database initialization and DSN utilities

## Infrastructure & tooling

**Orchestration & cluster:**
- k3d (tested v5.8.3) — lightweight Kubernetes in Docker for local development
- k3s with Traefik ingress bundled — exposed on `localhost:8080` for application access
- Kubernetes (manifests in `k8s/`)

**Data systems:**
- **PostgreSQL** (`postgres:18-alpine`) — relational database with two schemas: `dictionary` and `phraseforge_app`
- **Qdrant** (`qdrant/qdrant:v1.19.0`) — vector database for semantic search in the knowledge application
- **Garage** (`dxflrs/garage:v2.3.0`) — S3-compatible object storage with `replication_factor = 1`
- **NATS** (`nats:2.14.6-alpine`) — message queue with JetStream enabled
- **Ollama** (optional, `host.docker.internal:11434`) — local LLM for embeddings and chat
- **OpenRouter/OpenAI-compatible** — configurable via environment variables

**Kubernetes tooling:**
- **Argo Workflows** (v4.1.3) — workflow orchestration
- **Argo Events** (v1.9.11) — event-driven automation
- **Headlamp** (v0.45.0) — web-based Kubernetes dashboard

**Build & CI/CD:**
- **Task** (tested v3.53.1) — command orchestrator for k3d cluster management, image builds, and Kubernetes deployments via `Taskfile.yml`
- **Docker** — multi-stage builds (`golang:1.26-alpine` → `scratch`), with corporate CA trust via bind-mount; image size threshold of 50 MB enforced
- **GitHub Actions** — per-application release workflows (dictionary, phraseforge, knowledge)
  - CalVer versioning: `YYYY.MM.micro`
  - Cross-platform binaries (Linux/Windows amd64) and Docker image push to GHCR
  - Path-triggered runs: only build when that app's directory changes
  - Test step present in the knowledge app only; dictionary and phraseforge do not run tests in CI
  - Draft releases with manual publish required

## Key conventions

**Image tagging & deployment:**
- `:dev` images (mutable) for local development; requires rollout restart to pick up changes
- Versioned + `:latest` tags for releases

**Kubeconfig isolation:**
- Project-specific kubeconfig at `.k3d/kubeconfig`, never modifying `~/.kube/config`

**Credentials & configuration:**
- Generated, gitignored files only: `.k3d/postgres.env`, `.k3d/garage.toml`, `.k3d/garage.env`
- No credentials in code or committed files

**Registry addressing duality:**
- `localhost:5000` for `docker push` from host
- `k8s-lab-registry:5000` in pod specs (in-cluster)

**Kubernetes namespaces:**
- `data` — Postgres, Qdrant, Garage, NATS
- `app` — dictionary, phraseforge, knowledge
- `workflows` — Argo Workflows and Argo Events
- `kube-system` — k3s, Headlamp

**Database migrations:**
- One-time Job per app: idempotent schema.sql or migration task, applied before deployment

**LLM connectivity:**
- Ollama via `host.docker.internal` in k3s context for host-side Ollama access

**Task runtime:**
- Global error handling: `set: [errexit, nounset, pipefail]`

**Go binary optimization:**
- Stripped symbols and DWARF: `-ldflags="-s -w"`
- Static linking: `CGO_ENABLED=0`
