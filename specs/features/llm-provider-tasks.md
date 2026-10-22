---
title: Task targets to enable and disable extra LLM providers on the dev cluster
kind: feature
status: done
version: 4
updated: 2026-10-22
branch: main
---

## Problem / Motivation

The user tests on several computers. The local Ollama of the dev cluster (host.docker.internal:11434, reached through the Service `ollama` in namespace `data`, `k8s/26-ollama.yaml`) is slow and heavy on a laptop. The user wants `task` targets that enable extra providers (Ollama on the local network, OpenRouter, Vertex AI), add their configuration and tokens to the apps of the dev cluster, and remove them again. Wanted priority: 1) LAN Ollama, 2) OpenRouter, 3) Vertex AI, 4) local Ollama, 5) fail if nothing is available.

Scope (decided by the user, 2026-10-22): the apps assistant and phraseforge, which use `shared/router` (providers ollama, openai, anthropic, vertex; the highest-priority available model per tag; a circuit breaker; no pings). Knowledge has no router and is not part of this feature: a separate feature `knowledge-model-router` comes first, then knowledge joins these tasks. Dictionary has no LLM configuration. Configuration comes from the git-ignored `.env` at the repository root only (decided by the user, 2026-10-22, replacing the earlier shell-only decision): the shell's own variables of the same names are ignored, so a key exported for other work never wins over the one in `.env`. No application reads `.env`; the apps get ConfigMaps and Secrets.

## Acceptance Criteria

1. Three tasks exist: `llm-status`, `llm-enable`, `llm-disable`. `llm-status` is read-only and prints, in priority order, each source as available or unavailable with the reason; it exits 1 when no source is available (also when local Ollama is unreachable), else 0.
2. After `llm-enable`, the generated models files list the models in the priority order LAN 100, OpenRouter 90, Vertex 80, local Ollama 10.
3. A source that is unavailable is omitted from the generated files.
4. When nothing is available, `llm-enable` fails with a message that names the missing variables and changes nothing in the cluster.
5. No secret appears in stdout, in the repo, in a ConfigMap or in `.k3d/llm/*`; the files hold only `env:` and `file:` references.
6. `llm-disable` restores the exact committed default: the applied ConfigMap content equals the committed file (checked with a diff), and the generated files, the OpenRouter key Secrets and the Vertex token Secrets are gone.
7. A redeploy (`deploy-assistant`, `deploy-phraseforge`) after `llm-enable` keeps the enabled configuration.
8. Moving phraseforge's models.yaml into its own ConfigMap leaves phraseforge unchanged with the defaults; its existing tests pass.
9. Tests use no real LLM: the probes are tested against a local fake HTTP server; generation is tested by validating the output with the assistant's `models validate` (accepting phraseforge's file is checked first) for at least these combinations of available sources: all, none, LAN only, Vertex only.
10. Real calls during final verification: at most one per remote provider, each under `timeout 20` with a tiny maximum output, and reported.
11. `task test-assistant` and the phraseforge tests pass.
12. Every `kubectl` call goes through the Taskfile `KUBECTL` variable (pinned `.k3d/kubeconfig`); no other context is used. Running any task twice gives the same result.
13. The settings (`LLM_*`, `OPENROUTER_API_KEY`) come from `.env` only: a shell variable of the same name is ignored, a commented-out or empty line is off, and the file is parsed, never executed. Commenting a line out and running `llm-enable` again removes that source, its Secrets and its models-file entries.
14. One rule for every credential Secret (OpenRouter keys and Vertex tokens, both apps): `llm-enable` never overwrites a key or Secret it did not create and stops with a message naming it, changing nothing; `llm-disable` and a repeated `llm-enable` remove only what `llm-enable` marked, and keep other keys in a Secret.

## Approach

Memory consulted: vertex, kubeconfig|KUBECTL, ConfigMap|configmap, OPENROUTER|llm-keys, host.docker.internal — 3, 2, 9, 1, 0 hits; relevant: vertex-token deploy gotcha (2026-10-20T20:28:35Z), assistant-models ConfigMap built by Taskfile not kept in k8s/ (2026-10-16T19:27:36Z), phraseforge migrate Job is a separate pod spec (2026-09-24T17:52:29Z), api_key_from conventions (2026-10-20T20:28:38Z).

Tags (decided by the user, 2026-10-22): the assistant's LAN model gets default, chat, review, cheap, orchestrator and ui, with `num_predict` of at least 2048 because of the `ui` tag (in `assistant/models.dev.yaml` that tag sits on a separate entry with `num_predict: 2048`); the OpenRouter and Vertex models get default, chat and review, without `cheap`; phraseforge's models get default, as in its models.yaml. Each model is added as follows:

| Source | Model | Provider config | Priority |
| --- | --- | --- | --- |
| LAN Ollama | `gemma4:31b` | type `ollama`, base URL from `LLM_LAN_OLLAMA_URL` | 100 |
| OpenRouter | `z-ai/glm-5.3-flash` | type `openai`, base_url `https://openrouter.ai/api/v1`, api_key_from `env:OPENROUTER_API_KEY` | 90 |
| Vertex AI | `gemini-2.5-flash-lite` | type `vertex`, api_key_from `file:/etc/<app>-vertex/token` | 80 |
| Local Ollama | existing | existing | 10 |

A higher number is tried first, so the router's runtime fallback gives the user's order. The Vertex provider calls `publishers/google/models/<model>:generateContent` (`shared/router/vertex/vertex.go`). Embedding models (tag `embed`) stay on local Ollama only; this is an assumption.

Settings (new, UPPER_SNAKE_CASE, read from `.env` by `scripts/llm-env.sh`, never from the shell): `LLM_LAN_OLLAMA_URL`, `LLM_LOCAL_OLLAMA_URL` (default `http://127.0.0.1:11434`), `OPENROUTER_API_KEY`, `LLM_VERTEX_ACCOUNT`, `LLM_VERTEX_PROJECT`, `LLM_VERTEX_LOCATION` (default `us-central1`). The Taskfile has no `dotenv:` setting; its steps run in Task's own shell, which is not bash, so they load the settings with `eval "$(bash scripts/llm-env.sh --export)"`. Secrets are read inside the command that uses them and are never printed, written to the repo or put in a ConfigMap. Generated non-secret files go under the gitignored `.k3d/llm/`.

Tasks:

- `llm-status` probes: LAN = `curl --max-time 3 $LLM_LAN_OLLAMA_URL/api/tags` succeeds and lists `gemma4:31b`; OpenRouter = the key is set (implementation checks whether a cheap authenticated check exists); Vertex = `gcloud auth print-access-token --account=$LLM_VERTEX_ACCOUNT` succeeds and the project is set; local = the host Ollama `/api/tags`.
- `llm-enable` runs the status, includes only the available sources, generates each app's models.yaml, validates it (the assistant's `models validate` command; phraseforge has none, so its file is checked with the same command, since both use `shared/modelcfg`, confirmed in the first step), creates the Secrets and ConfigMaps in namespace `app`, restarts both deployments and waits for the rollout. It fails clearly when nothing is available.
- `llm-disable` deletes the generated files, the OpenRouter key and Vertex token Secrets and the generated ConfigMap content, restores the committed local-Ollama default and restarts.

Wiring:

- The assistant models.yaml is the ConfigMap `assistant-models`, built from `assistant/models.dev.yaml` by `_ensure-assistant-models`. The assistant pod already mounts the optional Secret `assistant-vertex` at `/etc/assistant-vertex`, written by `refresh-assistant-vertex-token`; it has no OpenRouter env yet, so an optional `OPENROUTER_API_KEY` env is added.
- Phraseforge's models.yaml is a key inside the ConfigMap `phraseforge-config`, re-applied by every `deploy-phraseforge`, so a patch would be overwritten. It moves into its own ConfigMap `phraseforge-models`, built by the Taskfile and mounted separately; the deployment and the migrate Job pod spec are both checked. The deployment already has an optional `OPENROUTER_API_KEY` from the optional Secret `phraseforge-llm-keys`; an optional Vertex Secret volume is added.
- The deploy tasks use the generated `.k3d/llm/<app>-models.yaml` when it exists, else the committed dev file, so a redeploy does not undo `llm-enable`.
- A phraseforge purpose's `numCtx` must not exceed the model's `options.num_ctx`; the `gemma4:31b` entry must keep `num_ctx` at or above what the purposes ask for.
- Vertex access tokens last about an hour: one refresh task serves both apps and reads `LLM_VERTEX_ACCOUNT`; the old `ASSISTANT_VERTEX_ACCOUNT` is dropped with no fallback, and the task that read it is replaced.

## Affected Areas

- `Taskfile.yml`: the new tasks, `_ensure-assistant-models`, `deploy-assistant`, `deploy-phraseforge`, `refresh-assistant-vertex-token`.
- `assistant/k8s/` deployment (OpenRouter env); `assistant/models.dev.yaml` (committed default, unchanged).
- `phraseforge/k8s/configmap.yaml` (models.yaml removed), the new `phraseforge-models` ConfigMap, `phraseforge/k8s/deployment.yaml`, `phraseforge/k8s/jobs/migrate-job.yaml`.
- `.k3d/llm/` (generated, gitignored); tests for the probes and the generation.
- `shared/router/vertex/vertex.go` and `vertex_test.go`: error texts only, no behaviour.
- Docs: the README and `specs/tech-stack.md` if they list the tasks or variables (checked at documentation review).

## Out of Scope

- The knowledge app (feature `knowledge-model-router` first), the dictionary.
- Production and the `jdp-helm` charts.
- New providers, embeddings on remote providers, a token refresh daemon.
- Changing the router code, except the two Vertex error texts (step 4b).
- Vertex AI in production (token minting through a service account or Workload Identity, or a refresher): not implemented, documented as a known gap.

## Implementation Notes

Baseline before any change (2026-10-22, dev cluster `k3d-k8s-lab`): `task test-assistant` 18 packages ok, `go test ./...` in `phraseforge` 16 packages ok, 0 FAIL. Database-backed tests that need a database variable are unverified when skipped.

Step 1, `llm-status` (done): `scripts/llm-status.sh` probes the four sources in priority order and exits 1 when none is available; `task llm-status` runs it. Probes: LAN = `/api/tags` within 3 s lists `gemma4:31b` (a fixed model; an earlier `LLM_LAN_OLLAMA_MODEL` setting was removed on 2026-10-22, since the LAN machine can serve other models too); OpenRouter = key set (no network call); Vertex = `gcloud auth print-access-token` succeeds under `timeout 20` and the project is set; local = `/api/tags` on `127.0.0.1:11434`. `scripts/llm-status_test.sh` (`task test-llm-scripts`) runs 10 cases against a fake Ollama and a fake gcloud and checks that no token is printed; a mutation of the LAN probe made 3 cases fail, so the tests can fail. Run on this machine with no variables set: only local Ollama available, exit 0.

Step 2, phraseforge models in their own ConfigMap (done): the `models.yaml` block moved unchanged (diffed) from `phraseforge/k8s/configmap.yaml` to `phraseforge/models.dev.yaml`; `_ensure-phraseforge-models` validates it with the assistant's `models validate` (both load through `shared/modelcfg`; accepted: "1 providers, 1 models") and creates the ConfigMap `phraseforge-models`; the Deployment and the migrate Job mount both ConfigMaps in one projected volume at `/etc/phraseforge`; `delete-phraseforge` removes the new ConfigMap; `models_test.go` now reads `models.dev.yaml` and fails if the ConfigMap still has `models.yaml`. Checked: `go vet` and `go test ./...` in `phraseforge` 16 packages ok, 0 FAIL; `task deploy-phraseforge` on `k3d-k8s-lab` exit 0; the pod is Running with 0 restarts and logs "routing over 1 model(s)"; `phraseforge-config` holds only `config.yaml`; the migrate Job succeeded; `/` answers 302.

Step 3a, generator (done): `scripts/llm-generate.sh OUTDIR [lan-ollama] [openrouter] [vertex]` inserts the sources into copies of the committed dev files after their top-level `providers:` and `models:` lines (no YAML tool needed) and writes `assistant-models.yaml` and `phraseforge-models.yaml`; `llm-status.sh --ids` prints the available remote sources for it. Models: `ollama-lan` `gemma4:31b` (100; assistant tags default, chat, review, cheap, orchestrator, ui with `num_predict` 2048; phraseforge `default`, `num_ctx` 16384 as the local model), `openrouter` `z-ai/glm-5.3-flash` (90), `vertex` `gemini-2.5-flash-lite` (80); the remote models have default, chat, review (assistant) or default (phraseforge). `scripts/llm-generate_test.sh` (in `task test-llm-scripts`) validates every output with the assistant's `models validate` for all, none, LAN only, Vertex only and OpenRouter only, checks the priorities, that "none" is byte-identical to the committed files, that no secret is in the files, and that bad input is refused; a mutation of the OpenRouter priority made 4 checks fail. Phraseforge's own startup checks (purposes against the models) run only in the pod, so they are checked when `llm-enable` is first applied.

Step 3b, cluster wiring (done): `task llm-enable` (`_require-cluster`, `_llm-guard`, `scripts/llm-enable.sh`, then `_llm-apply`) and `task llm-disable` (removes `.k3d/llm`, then `_llm-apply`). `scripts/llm-enable.sh` writes `.k3d/llm/{sources,assistant-models.yaml,phraseforge-models.yaml}`, fails writing nothing when no source at all is available, and with only the local Ollama removes earlier generated files. `_llm-apply` stores the OpenRouter key (`assistant-llm-keys`, merged into `phraseforge-llm-keys`) and the Vertex token (`assistant-vertex`, new `phraseforge-vertex`) through stdin, never on a command line; builds both models ConfigMaps from the generated files when they exist (`_ensure-*-models`, so a redeploy keeps the configuration); applies the manifests and restarts the deployed apps. Safeguards: a key or Secret is removed only if `llm-enable` marked it with an annotation (`llm.k8s-lab/managed-keys`, `llm.k8s-lab/managed`), and `llm-enable` refuses to overwrite an `OPENROUTER_API_KEY` set by hand in `phraseforge-llm-keys`. The assistant Deployment gained an optional `OPENROUTER_API_KEY` env and the phraseforge Deployment an optional `phraseforge-vertex` volume. Live check on `k3d-k8s-lab` with a fake LAN Ollama, a fake `gcloud` and a dummy key (no model call): enable gave priorities 100 90 80 10 and phraseforge logged "routing over 4 model(s)"; a second enable and a second disable both exit 0; disable left both ConfigMaps byte-identical to the committed files and removed the four Secrets and `.k3d/llm`; no secret value in logs, ConfigMaps or files; nothing left running. Two defects found by that run and fixed: disable failed when a Secret was already gone (a failing `kubectl get` under `pipefail`), and it deleted `assistant-vertex` unconditionally, so it is now removed only when marked. The `assistant-vertex` Secret that already existed (5 days old, from `refresh-assistant-vertex-token`) was overwritten by the first run and removed by its disable; it held an expired token.

Step 3c, `.env` only and one rule (done, 2026-10-22): the user asked that only `.env` supply the settings and that disabling be easy; `scripts/llm-env.sh` discards any shell value of the listed names, parses `.env` without executing it and, with `--export`, prints quoted `export` and `unset` lines for the Taskfile steps. `Taskfile.yml` lost its `dotenv:` line. The single credential rule (criterion 14) now covers `assistant-llm-keys`, `phraseforge-llm-keys`, `assistant-vertex` and `phraseforge-vertex` (marked by the annotations `llm.k8s-lab/managed-keys` and `llm.k8s-lab/managed`); before it, the assistant's key Secret and the Vertex Secrets were overwritten and deleted unconditionally, which the README tells users to create by hand. `llm-status_test.sh` now runs in a temporary copy of `scripts/` with its own `.env` (28 `ok` lines in `task test-llm-scripts`; a mutation that kept shell values made 4 fail) and covers shell values ignored, comments, empty values, quotes, CRLF, last assignment, unlisted names, no execution and `--export` through `eval` in `sh`. A live run on `k3d-k8s-lab` in a throwaway copy of the repo (own `.env`, dummy keys, fake `gcloud`, no model call; cluster addressed by `KUBECONFIG_PATH` on the command line, nothing copied) found one real bug the unit tests could not: Task runs `cmds` in its own shell, where `BASH_SOURCE` is unset, so sourcing the loader failed and the step ran with an empty key; fixed with `--export`. After the fix the live run passed: a key only in the shell is ignored; commenting entries out of `.env` and running `llm-enable` removes each source (priorities 90 80 10, then 80 10, then 10); a hand-made `assistant-llm-keys` with its own key and a hand-made `assistant-vertex` are refused and untouched; a hand-made `phraseforge-llm-keys` keeps its `ANTHROPIC_API_KEY` through enable and removal. Cleanup checked: no process, no generated files, no extra Secret, copy deleted.

Step 4, token refresh (done): `task llm-refresh-vertex-token` replaces `refresh-assistant-vertex-token` (removed; `ASSISTANT_VERTEX_ACCOUNT` is gone with no fallback). It needs Vertex enabled (`.k3d/llm/sources`) and `LLM_VERTEX_ACCOUNT` in `.env`, refuses a Secret `llm-enable` did not create (the Vertex guard is now the internal task `_llm-guard-vertex`, used by `llm-enable` and the refresh), and reuses `_llm-vertex-token`. Live run in a throwaway repo copy (fake `gcloud`, no model call): refresh without Vertex enabled exits non-zero and creates nothing; after `llm-enable` a refresh replaces the token in both Secrets (`fake-token-1` to `fake-token-2`) without restarting a pod and without printing it; with the Secret unmarked it refuses and leaves it untouched; with the account commented out of `.env` it says so. The assistant README (two passages) and the Deployment comment now name the new tasks. Cleanup checked: copy deleted, no extra Secret, no process. The same search found the router's own error texts still naming the removed task; see step 4b.

Step 4b, neutral Vertex error texts (done, approved by the user 2026-10-22): the router also runs in production, where no `task` command exists, so no message in shared code may name one. In `shared/router/vertex/vertex.go` the missing-credential error is now the base error alone (it already names `file:` or `env:`), and the 401/403 error ends "(the access token may have expired or lack permission; renew the credential that api_key_from points to)". The two assertions in `vertex_test.go` now check `api_key_from` and `expired` and that the text contains no `task `. Checked: `go vet` and `go test` of `shared/router/...` all ok; `assistant` 18 packages ok and `phraseforge` 16 packages ok, 0 FAIL. Dev hints stay in dev-only places (the README and the task descriptions).

Restart only on change (added 2026-10-22 at the user's request, with knowledge's step 2): `_llm-restart` no longer runs `rollout restart`. Each deployed app's Deployment pod template gets the annotation `llm.k8s-lab/config` holding the resource versions of its models ConfigMap and its key Secret (never their content), set with `kubectl patch`; Kubernetes rolls pods out only when it changes. Live check in a throwaway repo copy (fake `gcloud`, dummy keys): the first enable rolled the pods; the same enable again, a Vertex token refresh and a repeated disable left every pod untouched; adding an OpenRouter key and a disable rolled them. The first harness run reported false mismatches because it counted pods still terminating and a stale snapshot; the harness was fixed, not the task. Later changes to these tasks belong to `specs/features/knowledge-model-router.md`.

## Validation

Final run (2026-10-22, dev cluster `k3d-k8s-lab`): `task test-assistant` 18 packages ok, `go test ./...` in `phraseforge` 16 packages ok and in `shared` 10 packages ok, 0 FAIL, same package counts as the baseline for assistant and phraseforge; `task test-llm-scripts` 29 checks ok (mutations of the LAN probe, the OpenRouter priority, the shell-ignoring loader and the model probe each made tests fail); `go vet` clean; the validator passes. Database-backed tests that need `PHRASEFORGE_TEST_DSN` or `PHRASEFORGE_TEST_DB` were not run, as at the baseline: unverified, and this feature touches no database code.

Real run with the user's `.env` (Vertex only; LAN Ollama and OpenRouter were not configured, so they were not tested against real services): `task llm-status` showed Vertex available (token minted for the user's account, project `np-te-core-ai`, region default) and local Ollama available; `task llm-enable` exit 0 with priorities 80 and 10 in both ConfigMaps, both apps rolled out, phraseforge logged "routing over 2 model(s)", the Secrets `assistant-vertex` and `phraseforge-vertex` were created; one real `ask` task (user Janusz, tag `chat`, prompt "Reply with the single word: ready") ran through the deployed assistant: provider `vertex`, model `gemini-2.5-flash-lite`, outcome `ok`, 7 input and 1 output tokens, 1272 ms, result text "ready"; `task llm-refresh-vertex-token` exit 0 without a pod restart (gcloud returned the same valid token, so the Secrets were unchanged); the test task was deleted (204); `task llm-disable` exit 0 restored both ConfigMaps byte for byte and removed the Secrets and `.k3d/llm/`. Not exercised against a real service: phraseforge's own Vertex call (the shared router and its token mount were verified, the call itself was not), LAN Ollama, OpenRouter. One file predating this work was left in `.k3d/`: the empty `assistant-vertex-token` from the removed task.

## Documentation Review

Changelog entries (each under `## [Unreleased]`), as `{path, category, entry}`:

- `CHANGELOG.md`, Added: the `llm-status`, `llm-enable`, `llm-disable` and `llm-refresh-vertex-token` tasks, `.env`/`.env.example` and the `scripts/` shell scripts (tested by `task test-llm-scripts`).
- `CHANGELOG.md`, Removed: `task refresh-assistant-vertex-token` and `ASSISTANT_VERTEX_ACCOUNT`, replaced by `task llm-refresh-vertex-token` and `LLM_VERTEX_ACCOUNT` in `.env`.
- `assistant/CHANGELOG.md`, Added: optional `OPENROUTER_API_KEY` environment variable from the Secret `assistant-llm-keys`.
- `assistant/CHANGELOG.md`, Changed: the Vertex error texts no longer name a dev `task` command.
- `phraseforge/CHANGELOG.md`, Changed: `models.yaml` in its own ConfigMap `phraseforge-models`, built from `phraseforge/models.dev.yaml`.
- `phraseforge/CHANGELOG.md`, Added: optional `phraseforge-vertex` Secret volume.
- `phraseforge/CHANGELOG.md`, Changed: the same Vertex error texts.

Other docs checked and changed: `README.md` (Commands table; new section "LLM providers in the dev cluster" with "Known gaps"), `phraseforge/README.md` (dev-cluster key and Vertex bullets, `models.yaml` location), `assistant/README.md` (dev provider list, `env:` keys bullet, Vertex production gap), the header comments of `assistant/models.dev.yaml` and `phraseforge/models.dev.yaml`, `specs/memory.md` (six entries appended, one stale entry removed). `specs/tech-stack.md` is a constitution file: changes are proposed to the human, not written. Knowledge and dictionary docs are not affected.

Constitution: `specs/tech-stack.md` proposed in five changes (credentials and configuration, LLM connectivity, infrastructure lines including the stale `config.yaml` OpenRouter line, tooling, key conventions with the unprefixed `LLM_*` naming exception and the Task shell note) and approved by the user on 2026-10-22; applied as version 23 and validated. The root README sentence naming an "Admin > LLM page" was corrected to phraseforge's Usage page after a check against its README.

## Documentation Updates

Applied in step B4 and kept: root `README.md` (new section "LLM providers in the dev cluster" with "Managing LLM keys during development" and "Known gaps", five Commands rows), `phraseforge/README.md`, `assistant/README.md`, the header comments of both `models.dev.yaml` files, `CHANGELOG.md`, `assistant/CHANGELOG.md` and `phraseforge/CHANGELOG.md` under `## [Unreleased]`, and six entries in `specs/memory.md` with the stale `ASSISTANT_VERTEX_ACCOUNT` entry removed. Constitution: `specs/tech-stack.md` version 23, approved by the user on 2026-10-22. The roadmap line for this feature was removed from `specs/roadmap.md`. Known gap documented as not implemented: Vertex AI in production.
