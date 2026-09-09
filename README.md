# k8s-lab

Local, k3d-based Kubernetes cluster for development. Managed entirely through `task` — no manual `kubectl`/`k3d` setup required, and your default `~/.kube/config` is never touched.

## Prerequisites

- [k3d](https://k3d.io/) (tested with v5.8.3)
- [Task](https://taskfile.dev/) (tested with 3.53.1) — cross-platform on Linux/macOS/Windows; runs recipes through its own embedded shell interpreter, so no separate bash/WSL/Git-for-Windows install is required
- `jq`
- Docker (this project was built/tested against Rancher Desktop's Docker engine on macOS)

## Quick start

```sh
task start-k8s               # create the cluster (first run) or resume it (if stopped)
task run-k8s -- get nodes    # kubectl against this cluster only
task stop-k8s                 # pause — state, workloads, and registry contents are preserved
task delete-k8s               # full teardown — cluster, registry, and local kubeconfig all removed
```

## Commands

| Command | What it does |
|---|---|
| `task start-k8s` | Creates a single-node k3d cluster with a local registry and Traefik ingress on first run; resumes it if it exists but is stopped; no-ops if already running. |
| `task stop-k8s` | Pauses the cluster (`k3d cluster stop`) — workloads, pods, and registry contents survive. |
| `task delete-k8s` | Fully tears down the cluster, its registry, and the project's local kubeconfig file. |
| `task run-k8s -- <args>` | Runs `kubectl <args>` against this project's cluster only, e.g. `task run-k8s -- get pods`, `task run-k8s -- apply -f deploy.yaml`. Fails with a clear message if the cluster isn't running. |

## Kubeconfig isolation

Cluster credentials live in `.k3d/kubeconfig` (git-ignored), not in `~/.kube/config`. The cluster is created with `--kubeconfig-update-default=false --kubeconfig-switch-context=false`, so your existing kubectl contexts are never modified. Always use `task run-k8s -- ...` rather than raw `kubectl` to talk to this cluster.

## Working with images

**Pulling public images** works normally — reference them in a pod spec as usual (e.g. `nginx:alpine`).

**Pushing and using a locally-built image** — note the registry has two different addresses depending on where you're referencing it from:

```sh
docker build -t localhost:5000/my-app:1 .
docker push localhost:5000/my-app:1
```

Then, in your pod/deployment spec, reference the image via the registry's **in-cluster** name (not `localhost`):

```yaml
image: k8s-lab-registry:5000/my-app:1
```

| Context | Address |
|---|---|
| `docker push` from your host | `localhost:5000/<image>` |
| pod spec `image:` field | `k8s-lab-registry:5000/<image>` |

This asymmetry is a property of k3d's registry integration, not a bug — both point at the same registry.

## Ingress

Traefik (bundled with k3s) is exposed on **host port 8080** (not 80). This is a deliberate choice for this machine: Rancher Desktop can't reliably forward privileged ports (<1024) without admin access enabled, so 8080 is used instead. If your `task start-k8s` finds port 8080 already in use, it fails immediately and tells you what's holding it.

Example — expose a Service via Ingress and hit it:

```sh
task run-k8s -- expose deployment my-app --port=80 --name=my-app-svc
cat <<'EOF' | task run-k8s -- apply -f -
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: my-app-ingress
spec:
  rules:
  - host: my-app.localhost
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: my-app-svc
            port:
              number: 80
EOF
curl -H "Host: my-app.localhost" http://localhost:8080/
```

TLS/HTTPS is out of scope for now — HTTP only.

## Corporate network note

If you're on the Home Depot corporate network, `task start-k8s` automatically trusts the corporate CA bundle (`/usr/local/munki/thd_certs.pem`, standard on managed machines) inside the cluster node so external image pulls work despite Netskope TLS inspection. If that file isn't present, cluster creation still succeeds identically — you'll just see a note that external pulls may fail on networks with TLS inspection.

## phraseforge-api (dictionary service)

### Deploy and migrate

```sh
task start-k8s          # cluster must be running first
task deploy             # build image, push, apply all k8s manifests, wait for Ready
task migrate-db         # run the database migration Job (safe to run twice — second run is a no-op)
```

`task deploy` performs the following steps in order:
1. Builds and pushes `phraseforge-api:dev` to the local registry.
2. Applies `k8s/00-namespaces.yaml` (creates `data` and `app` namespaces).
3. Generates `.k3d/postgres.env` with random credentials if absent, then creates the `postgres-credentials` Secret in both `data` and `app` namespaces. Re-running is idempotent — the password is never rotated after first use.
4. Applies all `k8s/*.yaml` manifests (Postgres, Qdrant, Adminer, phraseforge-api, phraseforge). The `k8s/jobs/` subdirectory is **not** included — migrations stay manual.
5. Waits for all five Deployments to roll out.

The migration Job (`k8s/jobs/migrate-job.yaml`) is applied only by `task migrate-db`. Run it once after a fresh deploy; subsequent runs are no-ops.

### Ingress hosts

Both hosts use Traefik on host port 8080 (HTTP only):

| Host | Service |
|---|---|
| `phraseforge-api.localhost:8080` | REST API — `GET /healthz`, `GET /readyz`, `POST /api/v1/dictionary/entries`, … |
| `adminer.localhost:8080` | Adminer DB UI — server field pre-filled with Postgres DNS; supply user/password from `.k3d/postgres.env` |

```sh
curl -H "Host: phraseforge-api.localhost" http://localhost:8080/healthz
curl -H "Host: phraseforge-api.localhost" http://localhost:8080/readyz
```

### Additional recipes

| Command | What it does |
|---|---|
| `task build-image` | Builds the `phraseforge-api:dev` Docker image and pushes it to the local registry. |
| `task check-image-size` | Queries the registry manifest API and prints the compressed image size; fails if > 50 MB (NFR-IMG-001). |
| `task deploy` | Full deploy: build → namespaces → credentials → manifests → rollout wait. |
| `task migrate-db` | Runs the database migration Job; prints logs; safe to run repeatedly. |

## phraseforge (language-learning app)

A Readeck-inspired Go app for language-learning content: login, per-language teacher/student roles, and four resource types —

- **Texts** — markdown articles, with an optional transcription and a translation (in the reader's own site language).
- **Dialogs** — the same, using phraseforge's own turn-based markdown syntax (`--:`/`@Name:` turns).
- **Vocabulary** and **Models** — structured lists (not markdown): edited one item at a time, with proper IME support on non-Latin scripts. A vocabulary item is phrase/grammar/transcription (common) plus translation/notes (per site locale, en/pl); a model item is the same minus grammar/notes — cli-tools' own `{start-models}` block, described as "vocabulary without a grammar tag or notes".

Shares the `data` namespace's Postgres server with phraseforge-api, but its own database (`phraseforge_app`) and its own migration path — the two apps' schemas never mix.

### Deploy and migrate

```sh
task start-k8s               # cluster must be running first
task deploy                  # builds and deploys phraseforge alongside phraseforge-api (same task)
task migrate-phraseforge-db  # run phraseforge's own database migration Job (safe to run twice)
```

`task deploy` builds and pushes both `phraseforge-api:dev` and `phraseforge:dev` in one `task build-image` step, then applies `k8s/60-phraseforge.yaml` along with every other manifest — there's no separate deploy task to run. `task migrate-phraseforge-db` is the phraseforge equivalent of `task migrate-db`: it creates the `phraseforge_app` database (if missing) and applies `internal/db/schema.sql`, which is idempotent — safe to rerun after every schema change, not just the first deploy.

### Ingress host

| Host | Service |
|---|---|
| `phraseforge.localhost:8080` | The app itself — log in as `admin`/`phraseforge` on first deploy (bootstrapped automatically; change the password from the profile page). |

```sh
curl -H "Host: phraseforge.localhost" http://localhost:8080/healthz
```

## Requirements

Full traceable requirements for this project live in [`docs/REQUIREMENTS/`](docs/REQUIREMENTS/INDEX.md), including the environment-specific decisions above and why they were made.
