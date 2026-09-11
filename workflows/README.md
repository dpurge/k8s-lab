# workflows

[WorkflowTemplates](https://argo-workflows.readthedocs.io/en/latest/workflow-templates/) for
[Argo Workflows](https://argo-workflows.readthedocs.io/), deployed by `task deploy-workflows` into
the `workflows` namespace. Argo Workflows and Argo Events themselves (the controllers, CRDs, and
the webhook wiring) are shared infra — see the root [`README.md`](../README.md#argo-workflows--argo-events)
— this directory is only the workflow definitions themselves.

Every `*.yaml` file here is applied with `kubectl apply -f workflows/`. Add a new file per
template (or per related group of templates); each is picked up automatically on the next
`task deploy-workflows` — no other wiring needed unless you also want it reachable via the
webhook (see below).

## Deploying

```sh
task deploy-workflows   # cluster must be running first (task start-k8s)
task delete-workflows
```

## Logging into the UI

```
http://argo.localhost:8080
```

No login needed — `deploy-workflows` patches `argo-server` to `--auth-mode=server`, matching
this cluster's no-auth-for-dev-tools posture (same as Qdrant, NATS). Anonymous browser access
works out of the box.

## Running a WorkflowTemplate

From the UI: **Workflow Templates** → pick one → **Submit**.

From the CLI ([install `argo`](https://github.com/argoproj/argo-workflows/releases) once, then
point it at this cluster):

```sh
KUBECONFIG=.k3d/kubeconfig argo submit --from workflowtemplate/hello -n workflows -p name=David
KUBECONFIG=.k3d/kubeconfig argo list -n workflows
KUBECONFIG=.k3d/kubeconfig argo logs -n workflows @latest
```

## The `hello` sample

[`hello.yaml`](hello.yaml) — takes one parameter, `name` (default `World`), and prints
`Hello {name}!`.

It's also wired to a webhook, via the `Sensor` in
[`../k8s/42-argo-workflows-webhook.yaml`](../k8s/42-argo-workflows-webhook.yaml): POSTing JSON
with a `name` field runs it, with that field passed straight through as the `name` parameter.

```sh
curl -H "Content-Type: application/json" -d '{"name": "David"}' http://argo-events.localhost:8080/example
```

Then check the result (either through the UI, or):

```sh
KUBECONFIG=.k3d/kubeconfig argo list -n workflows
KUBECONFIG=.k3d/kubeconfig argo logs -n workflows @latest   # prints: Hello David!
```

## Authoring a new WorkflowTemplate

1. Add a new `*.yaml` file here, `kind: WorkflowTemplate`, `metadata.namespace: workflows`. Use
   [`hello.yaml`](hello.yaml) as a starting point, or see the
   [upstream examples](https://github.com/argoproj/argo-workflows/tree/main/examples) for
   multi-step (`steps`/`dag`), artifact-passing, or scheduled (`CronWorkflow`) patterns.
2. `task deploy-workflows` to apply it.
3. Submit it (UI, `argo submit --from workflowtemplate/<name>`, or a webhook — see below) and
   check its logs as above.

### Wiring a new template to the webhook

The webhook (`http://argo-events.localhost:8080/example`) can only trigger one thing at a
time — right now, `hello`. To point it at a different template instead, edit the `Sensor` in
[`../k8s/42-argo-workflows-webhook.yaml`](../k8s/42-argo-workflows-webhook.yaml): change
`spec.triggers[0].template.k8s.source.resource.spec.workflowTemplateRef.name`, and the
`arguments.parameters` alongside it to match your template's own parameters. The
`dataKey: body.<field>` under `parameters` picks a field out of the posted JSON body by name
(dot notation reaches nested fields, e.g. `body.user.name`) — see the
[Argo Events parameterization tutorial](https://argoproj.github.io/argo-events/tutorials/02-parameterization/)
for the full syntax (headers via `contextKey`, defaults, `operation: append`, etc). Then
`task deploy-workflows` to apply the change.

To trigger more than one template from more than one endpoint, add another `webhook.<name>`
entry (its own `port`/`endpoint`) to the `EventSource` and a matching `dependencies` +
`triggers` entry to the `Sensor` — see the
[Argo Events webhook example](https://github.com/argoproj/argo-events/blob/v1.9.11/examples/event-sources/webhook.yaml).
