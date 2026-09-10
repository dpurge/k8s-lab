# k8s-lab

Local, k3d-based Kubernetes cluster for development. Managed entirely through `task` — no manual `kubectl`/`k3d` setup required, and your default `~/.kube/config` is never touched.

This cluster runs two applications, each documented in its own README:

- [`dictionary/README.md`](dictionary/README.md) — Go/Chi REST API for multilingual dictionary entries.
- [`phraseforge/README.md`](phraseforge/README.md) — Go app for language-learning content.

This file covers only what's shared: the Taskfile, the k3d cluster itself, and the shared
infrastructure (Postgres, Qdrant, Adminer, Garage, NATS) both apps run against.

## Prerequisites

- [k3d](https://k3d.io/) (tested with v5.8.3)
- [Task](https://taskfile.dev/) (tested with 3.53.1)
- `jq`, `sed`, `awk`, `grep`, `openssl` — all standard on Linux/macOS. Developed and tested on
  those platforms; Windows would additionally need Git for Windows or WSL for these.
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

If you're on a corporate network, `task start-k8s` automatically trusts the corporate CA bundle (`/usr/local/munki/certs.pem`, standard on managed machines) inside the cluster node so external image pulls work despite TLS inspection. If that file isn't present, cluster creation still succeeds identically — you'll just see a note that external pulls may fail on networks with TLS inspection.

## Deploying

`task deploy` sets up **shared infrastructure only** — Postgres, Qdrant, Adminer, Garage. It
never touches the apps; each app has its own deploy task, documented in its own README, and its
k8s manifests live in its own directory (`dictionary/k8s/`, `phraseforge/k8s/`), not here.

```sh
task start-k8s   # cluster must be running first
task deploy      # shared infra only
task deploy-dictionary    # see dictionary/README.md
task deploy-phraseforge   # see phraseforge/README.md
```

`task deploy` performs the following steps in order:
1. Applies `k8s/00-namespaces.yaml` (creates `data` and `app` namespaces).
2. Generates `.k3d/postgres.env` and `.k3d/garage.toml` with random credentials if absent, then creates the `postgres-credentials` Secret (in both `data` and `app`) and the `garage-config` Secret (in `data`). Re-running is idempotent — secrets are never rotated after first use.
3. Applies all `k8s/*.yaml` manifests (Postgres, Qdrant, Adminer, Garage). The `k8s/jobs/` and `k8s/garage/` subdirectories are **not** included.
4. Waits for all four Deployments to roll out. Garage's own readiness additionally needs a one-time bootstrap — see [Garage](#garage-s3-compatible-object-storage) below; expect its rollout wait to time out until you've run `task init-garage` once.

**Known gotcha:** rebuilding an image without changing any k8s manifest does **not** make the
running pod pick it up — same mutable `:dev` tag, unchanged Deployment spec, so Kubernetes sees
nothing to roll. Force it with, e.g.:

```sh
task run-k8s -- rollout restart deployment/dictionary -n app
```

### Recipes

| Command | What it does |
|---|---|
| `task build-image` | Builds and pushes both `dictionary:dev` and `phraseforge:dev` images to the local registry (each app's own deploy task also does this on its own). |
| `task deploy` | Shared infra only: namespaces → credentials → manifests → rollout wait. |

Each app's own deploy/migrate tasks and full API docs live in its own README (linked above).

## Shared infrastructure

Postgres, Qdrant, Adminer, Garage, and NATS run in the `data` namespace and are shared by both apps.

**Postgres** — one server, two independent databases: `dictionary` (used by the `dictionary`
app) and `phraseforge_app` (used by `phraseforge`), each created and migrated by its own app's
own migration task — the two schemas never mix. Credentials are generated once into
`.k3d/postgres.env` (gitignored) — by whichever of `task deploy`, `task deploy-dictionary`, or
`task deploy-phraseforge` you run first — and projected as the `postgres-credentials` Secret
into both the `data` and `app` namespaces.

**Qdrant** (vector DB) is deployed alongside Postgres but isn't yet used by either app —
provisioned ahead of need. It's ClusterIP-only, no ingress; reach it in-cluster only (e.g. via
`task run-k8s -- exec`).

**Adminer** gives a DB UI for Postgres, server field pre-filled with its in-cluster DNS name:

| Host | Service |
|---|---|
| `adminer.localhost:8080` | Adminer DB UI — supply user/password from `.k3d/postgres.env` |

```sh
curl -H "Host: adminer.localhost" http://localhost:8080/
```

### Garage (S3-compatible object storage)

[Garage](https://garagehq.deuxfleurs.fr/) (`dxflrs/garage:v2.3.0`), for developing against
S3-compatible object storage without a cloud account. Single-node, `replication_factor = 1` —
a dev tool, not a durability story.

Its config (`rpc_secret`, `admin_token`, `metrics_token`) is generated once into
`.k3d/garage.toml` (gitignored, rendered from the checked-in `k8s/garage/garage.toml.tmpl`) and
projected as the `garage-config` Secret — same pattern as Postgres, and for the same reason:
real secrets never touch git.

**One-time bootstrap** (after `task deploy`): Garage's S3 API refuses every request — even on
a single node — until its cluster layout has been assigned, which nothing does automatically.
`task init-garage` handles this, and is safe to rerun:

```sh
task init-garage
```

In order, it: assigns this node a layout role (zone `dev`, capacity `1G`) and applies it if
none is assigned yet; creates a `dev` bucket if missing; and creates a `dev-app` access key
with read/write on that bucket if missing. Garage never lets you retrieve a secret key after
creation, so the key and its credentials are written **once**, on first creation, to
`.k3d/garage.env` (gitignored):

```sh
GARAGE_KEY_ID=GK...
GARAGE_SECRET_KEY=...
GARAGE_BUCKET=dev
GARAGE_S3_REGION=garage
GARAGE_S3_ENDPOINT=http://garage.localhost:8080
```

If `dev-app` already exists in Garage but `.k3d/garage.env` is missing locally (e.g. deleted or
never committed to a machine-specific backup), the task fails loudly with recovery instructions
rather than silently leaving you without usable credentials.

**Ingress hosts:**

| Host | Service |
|---|---|
| `garage.localhost:8080` | S3 API — path-style requests only (see below); admin token not required |
| `garage-admin.localhost:8080` | Admin API — cluster status/health, bucket/key management; `GET /health` needs no auth, everything else needs `Authorization: Bearer <admin_token>` from `.k3d/garage.toml` |

```sh
curl -H "Host: garage-admin.localhost" http://localhost:8080/health
```

**Developing against it with an S3 SDK (boto3, aws-cli, etc.):** don't point your client at the
Traefik ingress. S3 clients bake the endpoint hostname directly into request signing — there's
no way to separately override the `Host` header the way `curl -H` does — so they need
`garage.localhost` to actually resolve via your language runtime's own DNS resolver. It reliably
does in a plain browser or `curl` (which special-cases `*.localhost` itself, per RFC 6761), but
verify before assuming your tooling does too: on this machine, Python's `socket.getaddrinfo`
does **not** resolve it, even though `curl` does. The dependable option regardless of that is
`kubectl port-forward` directly to the Service, bypassing hostname resolution entirely:

```sh
task run-k8s -- port-forward -n data svc/garage 3900:3900
```

Then, in another terminal, point your client at `http://localhost:3900` with path-style
addressing and the credentials from `.k3d/garage.env`. Verified end-to-end with boto3:

```python
import boto3

s3 = boto3.client(
    "s3",
    endpoint_url="http://localhost:3900",   # NOT garage.localhost — see above
    aws_access_key_id="<GARAGE_KEY_ID>",
    aws_secret_access_key="<GARAGE_SECRET_KEY>",
    region_name="garage",
)
s3.put_object(Bucket="dev", Key="hello.txt", Body=b"hello from garage dev bucket")
print(s3.get_object(Bucket="dev", Key="hello.txt")["Body"].read())
```

### NATS (message queue, JetStream)

[NATS](https://docs.nats.io/) (`nats:2.14.6-alpine`) with [JetStream](https://docs.nats.io/nats-concepts/jetstream)
enabled, for developing against a message queue/streaming backend without a hosted service.
Single node, persistent storage on its own PVC — no bootstrap step needed (unlike Garage):
JetStream accepts stream/consumer commands as soon as the pod is ready.

No auth is configured — dev-only, matching Qdrant's posture in this cluster.

**Ingress host** (monitoring API only — `/healthz`, `/varz`, `/jsz`, ...; no auth):

| Host | Service |
|---|---|
| `nats.localhost:8080` | NATS monitoring HTTP API |

```sh
curl -H "Host: nats.localhost" http://localhost:8080/healthz
```

**Developing against it:** the client port (4222) speaks the NATS wire protocol, not HTTP, so
it's never ingress-routable — there's no hostname trick that helps here, unlike Garage's S3
API. Reach it with `kubectl port-forward` directly to the Service:

```sh
task run-k8s -- port-forward -n data svc/nats 4222:4222
```

Then, in another terminal, point your client at `nats://localhost:4222`. Verified end-to-end
with `nats-py` — create a stream, publish, pull-consume, confirm persisted message count:

```python
import asyncio, nats

async def main():
    nc = await nats.connect("nats://localhost:4222")
    js = nc.jetstream()
    await js.add_stream(name="MYSTREAM", subjects=["my.>"])
    await js.publish("my.subject", b"hello")
    sub = await js.pull_subscribe("my.>", "my-consumer")
    for msg in await sub.fetch(1, timeout=5):
        print(msg.data)
        await msg.ack()
    await nc.close()

asyncio.run(main())
```
