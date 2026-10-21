---
version: 22
status: approved
updated: 2026-10-21
---

## Languages & runtimes

- **Go 1.26.5** — all application modules and shared code (dictionary, phraseforge, knowledge, assistant, shared)

## Frameworks & libraries

- **Chi HTTP router** (`go-chi/chi v5.3.2`) — request routing and middleware across all three applications
- **PostgreSQL driver** (`jackc/pgx v5.10.0`) — direct pgx usage, no ORM, in all applications and shared utilities
- **Crypto** (`golang.org/x/crypto v0.57.0`) — password hashing and bearer token generation
- **Shared LLM client** (`shared/llm`) — abstraction layer for Ollama and OpenAI-compatible endpoints, consumed by phraseforge and knowledge, and by assistant through the router's Ollama provider; it has an additive `Headers` field for custom request headers
- **Shared Postgres helpers** (`shared/postgres`) — database initialization and DSN utilities
- **Prometheus client** (`prometheus/client_golang v1.25.0`) — `/metrics` endpoint, used by assistant and phraseforge
- **YAML and JSON Schema** (`go.yaml.in/yaml/v3`, `santhosh-tekuri/jsonschema/v6`) — assistant's workflow files and the schemas that validate them; `go.yaml.in/yaml/v3` is the maintained fork of the archived `gopkg.in/yaml.v3`

### Frontend libraries (knowledge app only)

- **markdown-it** (`15.0.2`) — Markdown-to-HTML rendering for knowledge chat messages; vendored as a static file; it is not part of the `shared/ui` build. Safe by default: raw HTML is escaped and dangerous link protocols (e.g., `javascript:`) are rejected by built-in link validation.
- **DOMPurify** (`3.4.15`) — HTML sanitizer applied to markdown-it's rendered output before DOM insertion, providing defense-in-depth beyond markdown-it's own default safety. Also vendored, same as above.

Both libraries are used in `knowledge/internal/server/static/index.html` only; dictionary and phraseforge are unaffected.

## Infrastructure & tooling

**Orchestration & cluster:**
- k3d (tested v5.8.3) — lightweight Kubernetes in Docker for local development
- k3s with Traefik ingress bundled — exposed on `localhost:8080` for application access
- Kubernetes (manifests in `k8s/`)

**Data systems:**
- **PostgreSQL** (`pgvector/pgvector:0.8.7-pg18-trixie`) — Postgres 18 with the pgvector 0.8.7 extension (Debian), relational database with two schemas: `dictionary` and `phraseforge`; assistant uses its own database named `assistant`
- **Qdrant** (`qdrant/qdrant:v1.19.0`) — vector database for semantic search in the knowledge application
- **Garage** (`dxflrs/garage:v2.3.0`) — S3-compatible object storage with `replication_factor = 1`
- **NATS** (`nats:2.14.6-alpine`) — message queue with JetStream enabled
- **Ollama** (optional, `host.docker.internal:11434`) — local LLM for embeddings and chat
- **OpenRouter/OpenAI-compatible** — both providers configured in phraseforge's mounted `config.yaml` with per-purpose defaults; API keys from env vars (`OPENROUTER_API_KEY`, `OLLAMA_API_KEY`)

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
| `assistant` | `assistant` | `assistant/CHANGELOG.md` | none (release workflow added in a later feature) | `specs/artifacts/assistant/roadmap.md` |
| `environment` | `.` | `CHANGELOG.md` | none | |

Built from: `shared/llm` (the Ollama and OpenAI-compatible client, with an additive `Headers` field for custom request headers) is consumed by phraseforge, knowledge and assistant; `shared/modelcfg` (the models.yaml schema, loader, lint, and credential and header resolution), `shared/yamlschema`, `shared/router` (the model router with its `ollama`, `openai`, `anthropic` and `vertex` providers and the `routertest` helpers) and `shared/retention` (batched, age-based purging) by phraseforge and assistant; `shared/postgres` by phraseforge and knowledge; `shared/ui` (the Lit component library and its handler) by phraseforge, assistant and knowledge. A change under `shared/` needs entries in every artifact that consumes the changed package.

## Key conventions

**Frontend components (shared library `shared/ui`):**
- Components are Lit 3.3.3 custom elements with Shadow DOM, written in TypeScript under `shared/ui/src/` using `static properties` (no decorators), so the source is also valid for Node's built-in type stripping. Lit, TypeScript and esbuild are pinned npm dependencies of `shared/ui` with a committed lockfile.
- Build: `task build-ui` bundles the source with esbuild (ESM, minified, code splitting) into `shared/ui/dist/`, which is gitignored and never committed. A small core entry holds Lit and the common components (`dp-shell`, `dp-button`, `dp-card`, `dp-field`, `dp-dialog`, `dp-status-bar`, `dp-pager`, `dp-tabs`); catalog components are separate chunks that a page loads on demand. Type checking is `tsc --noEmit`.
- A2UI renderer (assistant chat): `@a2ui/web_core` 0.14.0 (Apache-2.0, pinned exactly, with its dependencies `@preact/signals-core` and `@lit/context`) and `zod` 3.25.76 (the same version web_core uses, declared because `defineCatalog` builds argument schemas with it) are dependencies of `shared/ui`. It provides the A2UI v1.0 message processor, data model, binding, function evaluation and the Lit base element; the `dp-a2ui-*` components that draw the catalog are our own, under `shared/ui/src/a2ui/`, light-DOM and styled only by the design tokens. It is built as separate on-demand entries (`dist/a2ui.js`, about 118 KB gzipped, and the development gallery `dist/a2ui-gallery.js`) that only pages drawing A2UI surfaces import; `dp-core.js` does not include it. Components never write a `style` attribute (the assistant's Content-Security-Policy forbids inline styles); they use classes or CSSOM. The reference examples and conformance cases the tests use are copied unchanged under `shared/ui/src/a2ui/fixtures/` with the upstream commit recorded. Icons are 59 Material Design paths in `src/a2ui/icons.ts`, generated once from `@material-design-icons/svg` 0.14.15 (Apache-2.0; not a dependency), because the page's policy allows no icon font. The assistant's `ASSISTANT_A2UI_MEDIA_HOSTS` (bare https hosts, validated because they are written into the Content-Security-Policy) is the only configuration that changes the policy. The JSON schemas the assistant's server validates against will be vendored under `assistant/schemas/a2ui/v1_0/` when the server side is built.
- Theming: components read only CSS custom properties (the phraseforge palette in `shared/ui/tokens.css`, light and dark selected by `data-theme` on `<html>`), so apps share one palette without sharing page CSS.
- Naming: `dp-<name>` custom element tags (`dp-button`, `dp-card`, `dp-field`, `dp-dialog`, `dp-status-bar`, `dp-pager`, `dp-tabs`, `dp-shell`). The per-app `as-*`, `kb-*` and `pf-*` components are deleted as each app converts; new components are added to `shared/ui`, not to an app.
- Layout: `dp-shell` provides the phraseforge layout (top bar, collapsible sidebar with remembered state, theme toggle, footer status bar) for every app with a GUI.
- File layout: one directory per component under `shared/ui/src/components/<tag-name>/` with `component.ts` (calls `customElements.define`) and `component.test.ts`.
- Sharing model: `shared/ui` is a Go package of the `k8s-lab/shared` module that embeds `dist/` with `go:embed` and exposes one handler; each app mounts it at `/ui/` and loads the core with one `<script type="module" src="/ui/dp-core.js">`. Apps already depend on `shared` through a `replace` directive. Because `dist/` is built, not committed, each app's Dockerfile gets one Node build stage for `shared/ui` (trusting the corporate CA for `npm ci`, the same way its Go stage does) before `go build`; locally, `task build-ui` runs before `go run` or `go test` of an app.
- Testing: each component gets a `component.test.ts`, run directly on the TypeScript source with `node --test` (Node's built-in type stripping) under `jsdom`, with no compile step; `task test-ui` runs the tests and `tsc --noEmit`. Each app keeps its own frontend tests for its own pages.

**JSON API (phraseforge, started with `phraseforge-spa-shell-texts`):**
- `/api/v1/<resource>` — REST-shaped JSON endpoints (`GET`/`POST`/`GET one`/`PUT`/`DELETE`), same session-cookie auth (`requireAuth`) as the server-rendered pages.
- Errors are always `{"error": {"code": "...", "message": "..."}}`, matching knowledge's own `writeErr` convention exactly.
- Introduced resource-by-resource as each is migrated to phraseforge's SPA shell (see `specs/artifacts/phraseforge/roadmap.md`); a resource's old server-rendered routes/templates are removed in the same feature that adds its JSON API, never left coexisting.

**Image tagging & deployment:**
- `:dev` images (mutable) for local development; requires rollout restart to pick up changes
- Versioned + `:latest` tags for releases

**Kubeconfig isolation:**
- Project-specific kubeconfig at `.k3d/kubeconfig`, never modifying `~/.kube/config`.
- Every `kubectl` or `helm` command against the dev cluster passes `--kubeconfig=<absolute path to this project>/.k3d/kubeconfig` explicitly. The `KUBECONFIG` environment variable and the current context are never relied on, because the default configuration can include other clusters, production among them. A bare `kubectl` or `helm` is forbidden, including a read-only "which context" check.
- Prefer `task` targets: `KUBECTL` in `Taskfile.yml` already pins the flag.
- If the dev cluster is unreachable, report it and never try another context. Any other cluster, production included, is changed only when the user explicitly asks.

**Credentials & configuration:**
- Generated, gitignored files only: `.k3d/postgres.env`, `.k3d/garage.toml`, `.k3d/garage.env`
- No credentials in code or committed files

**Registry addressing duality:**
- `localhost:5000` for `docker push` from host
- `k8s-lab-registry:5000` in pod specs (in-cluster)

**Kubernetes namespaces:**
- `data` — Postgres, Qdrant, Garage, NATS
- `app` — dictionary, phraseforge, knowledge, assistant
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

**Naming conventions:**
- **Configuration keys** (YAML, JSON), **API request and response fields**, **query parameters**, **database tables and columns**: `snake_case` (lowercase words separated by underscores).
- **Environment variables**: `UPPER_SNAKE_CASE` with the application's prefix (e.g., `PHRASEFORGE_MODELS_FILE`, `ASSISTANT_LLM_CALLS_RETENTION_DAYS`).
- **Names that users choose and that appear as values** (provider IDs, model IDs, tags, workflow IDs): lowercase letters, digits and hyphens, starting with a letter (e.g., `ollama-cpu`, `openrouter`).
- **Metric names**: `snake_case` with the application prefix (e.g., `phraseforge_llm_calls_total`, `assistant_llm_calls_purged_total`).
- **Go and JavaScript identifiers**: follow their respective language conventions and are not covered by these rules.
- **i18n message keys**: dotted lowercase words with underscores (e.g., `admin.llm_model_tags`, `admin.llm_reason_cooldown`).
- **LEGACY — existing camelCase keys**: Some existing keys are camelCase: the PhraseForge `config.yaml` purpose keys (such as `numCtx`, `maxAttempts`), parts of PhraseForge's older admin JSON (such as `llmPrompts`, `sourceLanguage`), and the Helm chart's values for them. The human intends to migrate all of them to `snake_case`. Until then, legacy names stay as they are, but **every NEW key — even one added next to legacy camelCase neighbours — must be `snake_case`**. When a key is renamed, the old name must be rejected with a message naming the replacement rather than silently ignored.
- **Check before naming**: before proposing any new key, field, query parameter, column or option name, check this section; a name that does not match is a defect, not a style choice, even when it sits next to legacy camelCase neighbours or copies an existing legacy spelling.

**Background operation queue (knowledge, phraseforge):**
- Any new LLM-calling code path routes through a background operation queue rather than calling
  the LLM inline — one app-wide worker per app, interactive priority (a user is waiting) always
  processed ahead of background priority (batch work like ingest).
- knowledge splits this into two tables: `internal/jobs` (an ingest-specific lifecycle tracker)
  plus `internal/queue`'s `operations` table (the general LLM queue, invisible to any UI).
  phraseforge instead uses one unified `internal/jobs` table, featuring an admin-only Jobs page that shows every
  submitted job — interactive and background alike — in one place, rather than leaving a queue
  invisible.
- Handler registration crosses package boundaries via a small consumer-side interface plus a
  `main.go` adapter (the same pattern as `ingest.Promoter`), never a direct import between the
  calling package and the queue's consumers.
- Job handlers can write results back to the database via consumer-side interfaces (`TextWriteback`, `TranslationWriteback`) when optional resource-type and resource-id fields are present in the payload, enabling automatic content generation during background operations like ingest.

**Schemas as contracts (assistant):**
- Every contract lives under `assistant/schemas/`: `db/` (idempotent DDL), `api/` (OpenAPI), and as each slice needs them `messages/`, `documents/`, the workflow schema and the model-config schema. A test checks each code surface against its schema, so a schema and its code cannot drift apart.

**Model routing (assistant):**
- Models are configured in `models.yaml`, validated by `assistant/schemas/models.schema.json`: providers (an Ollama endpoint may be on localhost or on another machine; several can be listed), and models with tags and a priority. A call goes to the highest-priority available model that has the requested tags and falls back to lower ones; a circuit breaker, not pings, decides what is available. Every attempt is recorded in `llm_calls` and attributed to the user.

**Tests never call a real LLM (assistant):**
- LLM access sits behind an interface and tests use a scripted fake that returns canned responses. The whole suite must pass with no LLM reachable.

**Structured logging:**
- `log/slog`, JSON handler to stdout, configured once in `main.go`.
- Every LLM call, background job start/finish, data import, and data-record create/update/delete
  gets one log line: duration and outcome only, never the content body.
