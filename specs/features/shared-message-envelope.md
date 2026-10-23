---
title: One structured message format for apps calling each other over NATS
kind: feature
status: done
version: 4
updated: 2026-10-24
branch: main
---

## Problem / Motivation

The user wants every app (assistant, knowledge, phraseforge, dictionary) to receive requests over the NATS message queue, process them and answer the app that made the request, as shared functionality, with ONE structured message format consistent between applications, easy to display, read and reason about.

Concrete first uses:

- the assistant asks knowledge for documents (a search);
- the assistant asks knowledge to ingest a URL;
- the assistant asks phraseforge to ingest a URL.

Facts read in the repo on 2026-10-23:

- No Go module uses a NATS client. The only trace is the task source value `nats` (assistant/internal/tasks/tasks.go:23; schema.sql:23).
- Dev NATS is `nats:2.14.6-alpine` with JetStream (`-js -sd /data -m 8222`), 1 replica, no authentication (k8s/35-nats.yaml; specs/mission.md:23). Production NATS (read on 2026-10-23) is the service `data/jdp-data-nats` (headless, port 4222, no pods in the cluster) pointing at an external single server whose own INFO handshake, read without credentials, reports nats-server 2.14.6, go1.26.7, JetStream enabled, max payload 1048576, headers supported, and no authentication, TLS or cluster announced; dev answers identically on every compared field.
- Existing operation to expose: knowledge `POST /api/v1/knowledge/search` takes `{query, tags, start, end, limit, offset}` (knowledge/internal/server/server.go:77-84, route :201) and returns knowledge items with the source shape `{id, title, summary, tags, score, url}` (knowledge/internal/chat/chat.go:64-71).
- Existing operation to expose: knowledge `POST /api/v1/ingest/url` takes `{url, tags}` (server.go:129-132, route :214) and starts an ingest that produces drafts (`/ingest/drafts`).
- Phraseforge ingests a text from a URL through its texts ingest (phraseforge/internal/server/dialogs.go:62-66 labels `texts.ingest_source_url`). The exact handler and its input are UNVERIFIED, to find in slice 4.
- The assistant today has only a stub `rag_search` tool (assistant/internal/tools/stub.go:50).
- The user added on 2026-10-23 that the assistant may also ask for the status of tasks in other applications; knowledge `jobs.Job` is `{id, kind, status, step, error, source_kind, source_ref, source_tags, created_at, updated_at}` with `SourceText` tagged `json:"-"` so the source text is never sent to a client (knowledge/internal/jobs/jobs.go:44-56), phraseforge `jobs.Job` is `{id, kind, priority, status, payload, result, error, step, progress, created_at, updated_at}` plus an internal `UserID` hidden from JSON (phraseforge/internal/jobs/jobs.go:59-73), knowledge lists jobs at `GET /api/v1/jobs` and phraseforge has `GET /jobs/{id}` in its admin API (knowledge/internal/server/server.go:195-198; phraseforge/internal/server/server.go:307-311), and the assistant has its own tasks (assistant/internal/tasks).

## Acceptance Criteria

1. A Go package (PROPOSED `shared/message`) defines the envelope, JSON with snake_case keys:
   - `schema_version` (1);
   - `id` (UUID string, PROPOSED);
   - `type` (`request`, `response`, `event`, `error`);
   - `correlation_id`;
   - `from`, `to` (app names);
   - `created_at` (RFC 3339 UTC);
   - `operation` (the name of what is asked, snake_case, required on every message; PROPOSED first names: `documents_search`, `url_ingest`, `task_status`);
   - `context` {`user_id` (opaque, informational), `task_id` (the requester's task, for tracing)} (both optional, PROPOSED);
   - `summary` (optional one-line human-readable text for logs and displays, at most 500 characters, PROPOSED);
   - `payload` (a JSON object whose shape belongs to the operation);
   - `error` {`code`, `message`, `retryable`} (on `error` only).

   Rules: a `response`, `event` and `error` carry the request's `correlation_id` and `operation`; `from` and `to` are not proof of identity (no authentication exists).
2. An operation registry in the same package: each app registers, per operation, a JSON Schema for the request payload and one for the response payload; `Validate` checks the envelope and, when the operation is registered, its payload. A JSON Schema (draft 2020-12) for the envelope ships with the package, and a test feeds the Go validation and the schema the same valid and invalid samples and requires identical verdicts.
3. Input is untrusted: a maximum encoded size (PROPOSED 512 KiB), schema validation on every receive, fixed error codes (PROPOSED: `invalid_message`, `unknown_operation`, `invalid_payload`, `not_found`, `too_large`, `timeout`, `unavailable`, `internal`), error texts that never name a dev-only `task` command (memory.md:147), and no secrets in envelopes or logs.
4. Display: a pure function renders any envelope as a plain one-line or short text for logs and admin pages (operation, type, summary or key payload fields, error code), with golden tests; it never prints a payload field the schema marks as secret (PROPOSED: none exist yet; the rule is that payloads carry no secrets).
5. Transport (PROPOSED package `shared/bus`, official `github.com/nats-io/nats.go`, a version at least two weeks old: the latest on 2026-10-23, v1.55.0, is dated that day and is not eligible; every API used is verified with `go doc` and a compile; the dependency is reviewed and recorded in tech-stack.md):
   - each app listens on `dp.<app>.request`;
   - a requester sends a `request` with a reply inbox and gets exactly one terminal `response` or `error` for a quick operation, with a timeout (PROPOSED 30 s) mapped to the `timeout` error;
   - a long operation (`url_ingest`) answers at once with a `response` whose payload carries the task id, and the requester asks for its state with the `task_status` operation (PROPOSED: polling replaces completion events; the `event` type stays defined in the envelope for future progress messages and no operation uses it in this spec);
   - the responder dispatches by `operation`, validates, recovers from handler panics into an `internal` error, and bounds concurrency (PROPOSED 8);
   - Core NATS request/reply carries every operation; JetStream is not used in this spec: long operations (url_ingest) are persisted by the receiving app in its own job table before it answers, so the answer to the requester is the saved task id, which a JetStream consumer cannot give; while an app is down the requester gets the retryable unavailable error and asks again;
   - an app listens only when its NATS URL is configured (off by default; PROPOSED config key `nats.url` and env `<APP>_NATS_URL`, snake_case/prefix conventions of tech-stack.md).
6. Transport tests: scripted fakes where possible; tests that need a real NATS run only when `NATS_URL` points at a server (the dev cluster's NATS through a port-forward started and stopped by a Taskfile task, as `task test-assistant` does for Postgres) and are skipped with a visible reason otherwise.
7. Knowledge answers `documents_search` (payload as its search endpoint: `query`, `tags`, `start`, `end`, `limit`, `offset`; response `documents` with `{id, title, summary, tags, score, url}`) and `url_ingest` (`url`, `tags`; response with the draft or job id), by calling its own services directly, not over HTTP. Phraseforge answers `task_status` only in this spec; `url_ingest` for phraseforge is NOT built because phraseforge's URL ingest needs a language and a script (required, validated against its catalogue, no default) and an owning user (texts.user_id is NOT NULL, internal/db/schema.sql:104) and a per-language role check (`CanEdit`), none of which `ops.URLIngestRequest` or an unauthenticated bus request has, and phraseforge's SSRF-protected URL fetch (`fetchIngestURL`, `rejectPrivateOrMetadataAddress`: refuses loopback, private, link-local and metadata addresses, max 3 redirects, 15 s, 5 MB) lives unexported in package server; the responder answers `unknown_operation` for it. Each has tests with fakes and, with `NATS_URL`, a real round trip. Knowledge and phraseforge also answer `task_status`: payload `{id}`, response `{id, kind, status, step, progress, error, created_at, updated_at}` (PROPOSED shape; `progress` only where the app has one); the response NEVER includes the task's `payload` or `result` (they can hold user text; knowledge already hides its source text); an unknown id gives the `invalid_payload` error or a `not_found` code; a test per app with fakes and, with `NATS_URL`, a real round trip.
8. The assistant has a small typed client (PROPOSED `internal/bus`) with `SearchDocuments` and `IngestURL`, and also `TaskStatus(app, id)`, built on the transport, tested with a fake responder and, with `NATS_URL`, against a real round trip. Wiring it into workflow tools (`rag_search`) is NOT part of this spec.
9. README of shared, each touched app's README and CHANGELOG (per the Artifacts table), tech-stack.md (NATS client, the envelope as shared contract), and the CI scripts (`scripts/ci-test.sh`, release workflow path filters) are checked and each decision recorded in Documentation Review as `{file, needs change: yes/no, reason}` (the closeout rules require it).

## Approach

Memory consulted: nats|jetstream|queue, envelope|message format|schema|a2ui, shared/ — 3 relevant entries (memory.md:46-47 queue handlers must not enqueue-and-await on their own queue; memory.md:147 shared error texts run in production and never name a dev task)

Ordered slices, each shippable and verified before the next, with a go-ahead between slices:

1. The envelope package, schema, operation registry, render, tests (criteria 1-4).
2. The transport helper with env-gated real-NATS tests and the Taskfile task, after the dependency review (criteria 5-6).
3. Knowledge responder, including `task_status` (criterion 7).
4. Phraseforge responder, including `task_status`, after finding its URL ingest entry point (criterion 7).
5. The assistant client (criterion 8).
6. Docs, changelogs, constitution, CI review (criterion 9).

Decisions from the user on 2026-10-23:

- the approved field names `schema_version`, `id`, `type`, `correlation_id`, `from`, `to`, `created_at`, `context`, `error` {`code`, `message`, `retryable`};
- one inbox subject per app and replies to the requester's reply inbox;
- core request/reply (JetStream was dropped while building, see the deviations paragraph);
- the first slice is the shared package with schema and tests;
- A2UI is internal to the assistant and not part of the envelope;
- the test variable is `NATS_URL`.

Proposed values approved by the user on 2026-10-23 ("defaults"): packages shared/message and shared/bus, UUID ids, 512 KiB limit, 30 s timeout, concurrency 8, the eight error codes, operation names documents_search, url_ingest and task_status, config key nats.url with env <APP>_NATS_URL and listening off unless configured, test variable NATS_URL.

Note: the earlier design of `parts` (text, sources, ui, question) is dropped; the source shape lives in the `documents_search` response.

Alternatives considered:

- a typed part list (dropped: apps exchange operations, not conversation turns);
- protobuf (rejected: the user wants it easy to read and display);
- per-app reply subjects (rejected: a reply inbox needs no registry).

Risks:

- No authentication on NATS or on `from`: until assistant-auth, any client that can reach NATS could search knowledge or make it fetch a URL, which is why listening is off unless configured and production NATS is not assumed.
- A message-driven URL fetch is a server-side request risk (knowledge already does it for logged-in users; the same URL checks must apply, knowledge's StartURL applies validateURL (http or https with a host) on both the HTTP and the bus path (verified)).
- Production NATS is the headless service data/jdp-data-nats in front of an external single server (verified on 2026-10-23, see Problem / Motivation); it has no authentication, so listening stays off unless configured.
- `task_status` exposes the existence and state of tasks of any user to any NATS client (no authentication yet), so it returns metadata only.
- The dev single replica is not a durability story (specs/mission.md:22).

Deviations and additions made while building (for the user's final approval):

- JetStream not used (reason in criterion 5).
- An operations catalogue package `shared/ops` was added between slices 2 and 3 (names, types, JSON Schemas, Parse functions) so that responders and clients share one definition.
- The shared module's own tests now run in CI (`scripts/ci-test.sh` runs `cd shared && go vet ./... && go test -count=1 ./...` for assistant, knowledge and phraseforge) because a change under shared/ releases every consumer yet its tests never ran.
- Knowledge and phraseforge `main.go` replaced `log.Fatal(http.ListenAndServe(...))` by an http.Server with SIGINT/SIGTERM graceful shutdown (bus drained first, then HTTP with a 10 s limit) so that the responder can drain.
- The dev NATS service also answers to the production name `jdp-data-nats` (k8s/35-nats.yaml) so the same URL works in dev and prod.
- Apps connect only when their NATS URL is set and a failed connection never stops the app.

Testing strategy: offline unit, golden and verdict-equality tests; real-NATS tests gated by `NATS_URL`; no real model calls.

## Affected Areas

- `shared/` (message and bus packages, go.mod dependency)
- `knowledge/` (responder, config, README, CHANGELOG)
- `phraseforge/` (responder, config, README, CHANGELOG)
- `assistant/` (client, README, CHANGELOG)
- `Taskfile.yml` (NATS test task)
- `scripts/ci-test.sh` and release workflows only if criterion 9's review finds a need
- `specs/tech-stack.md`

## Out of Scope

- Chat turns and A2UI over NATS.
- The assistant answering requests.
- Wiring the client into workflow tools (roadmap: assistant-rag, assistant-chat-port).
- Removing chat from knowledge.
- The `chat` task source.
- Authentication and authorisation (assistant-auth).
- Production NATS deployment.
- Dictionary adopting the bus.
- Telegram.
- Listing or cancelling or retrying tasks of other apps (only status is in scope).
- phraseforge url_ingest over the bus: needs the user's decisions on the owning user for texts created over the bus, where language and script come from (new optional fields in ops.URLIngestRequest or configured defaults), what happens to tags (phraseforge texts have none) and whether a bus request may skip the per-language role check; proposed roadmap item phraseforge-bus-ingest (not added to any roadmap yet)

## Implementation Notes

Slice 1, `shared/message`: envelope.go, codec.go, validate.go, registry.go, render.go, schema.go, envelope.schema.json, doc.go and testdata. The payload is kept as json.RawMessage so its bytes stay exact; decoding is strict; the size limit is 512 KiB; there are eight error codes; Render prints payload keys, never values. 29 tests, including a verdict test over 52 samples that checks the Go validation and the embedded JSON Schema agree. No new dependency.

Slice 2, `shared/bus`: subjects.go, conn.go, responder.go (with a pure, testable core `process`), client.go and doc.go. The client library is nats.go v1.54.0, released 2026-09-18, chosen because v1.55.0 is dated 2026-10-23. Responder: a queue group per app, at most 8 concurrent handlers, saturation answers `unavailable`, panics and unknown handler errors become the fixed `internal` text, and the handler's error text is logged server-side only. Client: default timeout 30 s; no responders and timeouts map to the retryable `unavailable` and `timeout`. Taskfile task `test-bus` (variable NATS_TEST_PORT, port 14222) runs the real-NATS tests through a port-forward of svc/jdp-data-nats and verifies the port is free afterwards. `scripts/ci-test.sh` runs the shared tests.

Slice 2b, `shared/ops`: `documents_search`, `url_ingest` and `task_status` with types, six JSON Schemas, Parse functions and Register. A 94-sample verdict test; the `task_status` response schema forbids `payload` and `result`.

Slice 3, knowledge: package internal/responder (interfaces Searcher, Ingester and Jobs; Run wiring). The handlers reuse kb.Search and ingest.Service.StartURL (the same validateURL as the HTTP handler: http or https with a host). Default limit 5; `total` is omitted. `task_status` copies only id, kind, status, step, error (cut to 1000), created_at and updated_at. An ingest already running answers `unavailable`. Config key `nats.url` and env `KNOWLEDGE_NATS_URL`; metric `knowledge_bus_requests_total{operation,outcome}`; `knowledge/k8s/configmap.yaml` sets the dev URL.

Slice 4, phraseforge: internal/bus answers `task_status` only (a non-UUID id answers `not_found` without querying; the Job payload, result and user id are never copied). Config key `nats.url` and env `PHRASEFORGE_NATS_URL`; metric `phraseforge_bus_requests_total`. `url_ingest` is deferred (see criterion 7 and Out of Scope).

Slice 5, assistant: internal/bus Client (`SearchDocuments` to knowledge, `IngestURL` and `TaskStatus` to knowledge or phraseforge; the context task and user ids are copied from callctx); metrics `assistant_bus_requests_total` and `assistant_bus_request_duration_seconds`; env `ASSISTANT_NATS_URL`; connectBus and closeBus in app.go. Not used by any workflow tool yet (roadmap: assistant-rag, assistant-chat-port).

Dev: k8s/35-nats.yaml gains the service `jdp-data-nats` (headless, 4222).

## Validation

Validation run of 2026-10-24 (the clock passed midnight; the runs started on 2026-10-23 evening), dev cluster context k3d-k8s-lab.

Offline tests:

- shared: `go test ./...` passes in every package, run 6 times in a row without a failure (message 29 tests, ops 114 passes including subtests, bus 42 top-level passes plus 13 real-NATS tests that skip without NATS_URL).
- knowledge: 137 pass, 0 fail, 8 skip (7 database tests need KNOWLEDGE_TEST_DSN, 1 real-NATS test).
- phraseforge: 441 pass, 0 fail, 217 skip (215 database tests need PHRASEFORGE_TEST_DSN, the others include the real-NATS test).
- assistant: 443 pass, 0 fail, 234 skip (database tests need ASSISTANT_TEST_DATABASE_URL; 1 real-NATS test).
- gofmt and go vet clean in all four modules.

Real NATS (the dev cluster's NATS through a port-forward, NATS_URL set):

- `task test-bus` exit 0 with 42 passes and 0 skips in shared/bus (the 13 NATS tests ran).
- assistant internal/bus 13 passes (TestRoundTripThroughRealNATS); knowledge internal/responder 16 passes (TestRoundTripOverRealNATS); phraseforge internal/bus 15 passes (TestNATSRoundTripAnswersTaskStatusAndLeavesURLIngestUnanswered).
- The port-forwards were stopped and the ports verified free each time.

CI parity: `task check-ci` ok (16 tests); `task ci-local APP=` assistant, knowledge and phraseforge ok (each ran the new shared tests step).

Real artifact: knowledge, phraseforge and assistant were deployed to the dev cluster (task deploy-knowledge, deploy-phraseforge, deploy-assistant, exit 0, rollouts ok). The assistant logged `connected to NATS`; knowledge logged `message bus responder started` on dp.knowledge.request. A throwaway client (shared/bus, NATS_URL through a port-forward) then called the real dev apps:

- knowledge documents_search returned 1 real document in 2.2 s with a real embedding call;
- a bad payload gave invalid_payload with a field message;
- an unknown task id gave not_found;
- url_ingest of a URL that fails fast inside the pod returned a task id with status running, and task_status of it returned exactly id, kind, status, step, error, created_at, updated_at (no source or payload);
- an unregistered operation gave unknown_operation;
- phraseforge task_status of a real job returned kind generate_transcription, status done, and not_found for an unknown id; url_ingest gave unknown_operation.

The one job row the test created in knowledge was deleted afterwards (0 jobs left).

Failure checks by mutation (reported by the engineers): the shared/bus queue group, drain, error-text and recover rules each failed their tests when broken.

Not validated: url_ingest over the bus with a real URL (it starts model calls); url_ingest on phraseforge (not built); the signal handling of the new graceful shutdown in knowledge and phraseforge (compile and vet only); the assistant Client against the real dev knowledge (its own tests use a fake and a real NATS responder; the real-app calls above used the shared client); production NATS behaviour (only its handshake was read); a vulnerability scan of nats.go (govulncheck not installed); the licence mapping of klauspost/compress flate; authentication (none exists).

## Documentation Review

Records `{file, needs change: yes/no, reason}`:

- README.md (root): needs change: yes: no row for `task test-bus` and no mention that the dev NATS answers to `jdp-data-nats` (done).
- shared/README.md: needs change: yes: the module had none (created, 40 lines: message, ops, bus, `task test-bus`, the example request from shared/message/doc.go).
- knowledge/README.md: needs change: yes: no section on the bus (done: "Message bus (NATS)").
- phraseforge/README.md: needs change: yes: no section on the bus (done: "Message bus (NATS)").
- assistant/README.md: needs change: no: the section "Calling other apps (internal/bus)" was checked against assistant/app.go (connectBus warns and continues when the connection fails), assistant/internal/config/config.go (`ASSISTANT_NATS_URL`), assistant/internal/metrics/metrics.go (labels app, operation, outcome; duration labels app, operation) and assistant/k8s/deployment.yaml (the env var is set to the jdp-data-nats URL).
- CHANGELOG.md (root): needs change: yes: done, one line under Added.
- assistant/CHANGELOG.md: needs change: yes: done, one line under Added.
- knowledge/CHANGELOG.md: needs change: yes: done, one line under Added and one under Changed.
- phraseforge/CHANGELOG.md: needs change: yes: done, one line under Added and one under Changed.
- dictionary/CHANGELOG.md: needs change: no: dictionary imports no shared module and is unchanged.
- scripts/ci-test.sh: needs change: yes: done (shared tests step for assistant, knowledge and phraseforge).
- .github/workflows/release-assistant.yml, release-knowledge.yml, release-phraseforge.yml: needs change: no: shared/** is already in the path filters. Their step comment "Builds shared/ui, then vets and tests; see scripts/ci-test.sh" does not mention the shared tests (Low; not edited, workflows are never auto-edited).
- .github/workflows/release-dictionary.yml: needs change: no: dictionary does not use shared and shared/** is deliberately not listed.
- Taskfile.yml: needs change: yes: `test-bus` and NATS_TEST_PORT (done).
- Dockerfiles (assistant, knowledge, phraseforge): needs change: no: the deploys built with the new dependency.
- k8s/35-nats.yaml: needs change: yes: service `jdp-data-nats` (done).
- knowledge/k8s/configmap.yaml, phraseforge/k8s/configmap.yaml: needs change: yes: `nats.url` set to the dev URL (done).
- assistant/k8s/deployment.yaml: needs change: yes: env `ASSISTANT_NATS_URL` (done).
- assistant/.env.example: needs change: unknown (read denied), decision left to the user.
- specs/mission.md: needs change: no: it names NATS as a dev message queue without authentication and without durability, which is still true; it does not describe apps calling each other, a decision left to the user.
- specs/tech-stack.md: needs change: yes: NATS client dependency, shared packages, config keys, dev/prod NATS facts, test task; drafted for the user's approval, applied only after it.

Drift found and not fixed:

- shared/ops/doc.go:12 said `url_ingest` is answered by "knowledge and phraseforge"; phraseforge answers `unknown_operation` for it (internal/bus answers `task_status` only). Found and fixed at final approval: the comment now says `knowledge (phraseforge: not yet)`.
- This spec, Risks: fixed at final approval.
- This spec, Approach, decisions of 2026-10-23: fixed at final approval.
- scripts/ci-test.sh, line 10: the comment sentence is longer than the other comment lines (Low; not edited, script).

## Documentation Updates

- shared/README.md: created.
- README.md: row for `task test-bus` in the task table; one paragraph in the NATS section on the service `jdp-data-nats`.
- knowledge/README.md and phraseforge/README.md: new section "Message bus (NATS)".
- CHANGELOG.md, assistant/CHANGELOG.md, knowledge/CHANGELOG.md, phraseforge/CHANGELOG.md: `## [Unreleased]` entries under the existing headings.
- specs/memory.md: three entries appended.
- Constitution: specs/tech-stack.md and the roadmaps: fixed at final approval.
- specs/tech-stack.md updated to version 26 (approved by the user on 2026-10-24); shared/ops/doc.go comment corrected (url_ingest is answered by knowledge only for now); roadmaps: this feature's Now lines removed; roadmap item phraseforge-bus-ingest added to the phraseforge roadmap.
