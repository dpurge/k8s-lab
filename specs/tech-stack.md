---
version: 5
status: approved
updated: 2026-09-23
---

## Languages & runtimes

- **Go 1.26.5** — all application modules and shared code (dictionary, phraseforge, knowledge, shared)

## Frameworks & libraries

- **Chi HTTP router** (`go-chi/chi v5.3.2`) — request routing and middleware across all three applications
- **PostgreSQL driver** (`jackc/pgx v5.10.0`) — direct pgx usage, no ORM, in all applications and shared utilities
- **Crypto** (`golang.org/x/crypto v0.57.0`) — password hashing and bearer token generation
- **Shared LLM client** (`shared/llm`) — abstraction layer for Ollama and OpenAI-compatible endpoints, consumed by phraseforge and knowledge
- **Shared Postgres helpers** (`shared/postgres`) — database initialization and DSN utilities

### Frontend libraries (knowledge app only)

- **markdown-it** (`15.0.2`) — Markdown-to-HTML rendering for knowledge chat messages; vendored as a static file, no build step or npm/bundler introduced. Safe by default: raw HTML is escaped and dangerous link protocols (e.g., `javascript:`) are rejected by built-in link validation.
- **DOMPurify** (`3.4.15`) — HTML sanitizer applied to markdown-it's rendered output before DOM insertion, providing defense-in-depth beyond markdown-it's own default safety. Also vendored, same as above.

Both libraries are used in `knowledge/internal/server/static/index.html` only; dictionary and phraseforge are unaffected.

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

### Artifacts

| Artifact | Root | Changelog | Versioning | Roadmap |
| --- | --- | --- | --- | --- |
| `dictionary` | `dictionary` | `dictionary/CHANGELOG.md` | independent (CalVer, per-app GitHub Actions release workflow) | `specs/artifacts/dictionary/roadmap.md` |
| `phraseforge` | `phraseforge` | `phraseforge/CHANGELOG.md` | independent (CalVer, per-app GitHub Actions release workflow) | `specs/artifacts/phraseforge/roadmap.md` |
| `knowledge` | `knowledge` | `knowledge/CHANGELOG.md` | independent (CalVer, per-app GitHub Actions release workflow) | `specs/artifacts/knowledge/roadmap.md` |
| `environment` | `.` | `CHANGELOG.md` | none | |

Built from: `shared/llm` and `shared/postgres` are consumed by phraseforge and
knowledge; a change under `shared/` needs entries in both.

## Key conventions

**Frontend components (per-app, not shared as files):**
- Native Web Components (Custom Elements), light DOM only (no Shadow DOM) — so components keep inheriting each app's existing CSS custom-property theming instead of being isolated from it.
- Naming: `<app-prefix>-<name>` custom element tags — e.g. knowledge uses `kb-*` (phraseforge's existing `pf`-prefixed JS function convention extends naturally to `pf-*` if/when phraseforge adopts this pattern; not done yet).
- File layout: one directory per component under that app's `static/components/<tag-name>/`: `component.js` (required — calls `customElements.define`), `component.html` (optional — a `<template>` fragment, fetched and cached on first use, following the same fetch+cache pattern phraseforge's `ime.js`/`pfLoadIme` already uses for IME data), `component.css` (optional — injected into `<head>` as a `<style>` tag once on first use, scoped by using the tag name as the outermost selector).
- Loading: a consuming page includes one `<script src="/components/<tag>/component.js">` per component it uses; the component's own JS handles fetching its template and injecting its style, so the page needs nothing else.
- Sharing model: **deliberate duplication**, not a shared file mechanism — each app (dictionary, phraseforge, knowledge are independent Go modules, each with its own `go:embed` static tree, no bundler) keeps its own copies of any component it needs. What's shared across apps is the *convention* (naming, file layout, light-DOM-plus-self-injecting-style pattern, testing approach below) — not literal files. This was an explicit choice over a Taskfile-copy-based sharing mechanism, favoring simplicity over strict DRY across apps.
- Testing: each component gets a `component.test.js`, run under Node + `jsdom` — a dev-time-only dependency (never used to build or ship the actual assets, which stay zero-build-step). A small per-app `package.json` plus a Taskfile task (e.g. `task test-knowledge-frontend`) wires this up.

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

**Background operation queue (knowledge app):**
- Any new LLM-calling code path routes through `internal/queue` rather than calling the LLM
  inline — one app-wide worker, interactive priority (a user is waiting) always processed ahead
  of background priority (batch work like ingest).
- Handler registration crosses package boundaries via a small consumer-side interface plus a
  `main.go` adapter (the same pattern as `ingest.Promoter`), never a direct import between the
  calling package and the queue's consumers.

**Structured logging:**
- `log/slog`, JSON handler to stdout, configured once in `main.go`.
- Every LLM call, background job start/finish, data import, and data-record create/update/delete
  gets one log line: duration and outcome only, never the content body.
