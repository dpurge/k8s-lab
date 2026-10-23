# shared

Go module `k8s-lab/shared`, built into assistant, knowledge and phraseforge; its
tests run in CI (`scripts/ci-test.sh`).

## Apps calling each other over NATS
Three packages give the apps one message format and one transport:

| Package | What it is |
|---|---|
| `message` | The JSON envelope (snake_case keys, `schema_version` 1), its JSON Schema, a registry of operations, a one-line `Render`, eight fixed error codes, a 512 KiB limit. |
| `ops` | The operations: `documents_search`, `url_ingest`, `task_status`. Request and response types, JSON Schemas and Parse functions, so responders and clients share one definition. |
| `bus` | The transport on NATS: a `Responder` on `dp.<app>.request` (queue group per app, at most 8 concurrent handlers) and a `Client` (default timeout 30 s). |

Nothing authenticates `from` or `to`; a payload carries no secrets. Details: each `doc.go`.

One request, from `message/doc.go` (the assistant asks knowledge for documents):

```json
{
  "schema_version": 1,
  "id": "3f0c1c52-8a43-4c1e-9d0e-6a1b2f7d9e10",
  "type": "request",
  "correlation_id": "3f0c1c52-8a43-4c1e-9d0e-6a1b2f7d9e10",
  "from": "assistant",
  "to": "knowledge",
  "created_at": "2026-10-23T09:15:00Z",
  "operation": "documents_search",
  "context": {"user_id": "u-42", "task_id": "t-1001"},
  "summary": "Find documents about kubernetes probes",
  "payload": {"query": "kubernetes liveness probe", "tags": ["ops"], "limit": 5, "offset": 0}
}
```

## Tests

`cd shared && go test ./...` runs offline; the tests that need NATS skip
without `NATS_URL`. `task test-bus` (from the repo root) runs `shared/bus`
against the dev cluster's NATS: it port-forwards `svc/jdp-data-nats` to local
port `NATS_TEST_PORT` (14222), sets `NATS_URL`, and stops the forward after.
