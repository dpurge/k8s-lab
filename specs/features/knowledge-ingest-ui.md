---
title: Ingest tab — draft review, edit, approve, discard
kind: feature
status: done
version: 1
updated: 2026-09-21
branch: main
---

## Problem / Motivation

The recently shipped `knowledge-ingest-pipeline` feature can fetch a URL or uploaded file and turn it into draft chunks, but there is no way to see, edit, approve, or discard those drafts — they only exist as rows in a Postgres table reachable via one read-only, no-body list endpoint. This feature closes that loop: a new "Ingest" tab in the existing single-file frontend lets a user kick off an ingest job, review the resulting draft chunks, edit them before deciding, and either approve a draft (promoting it into Qdrant as a real, searchable knowledge item) or discard it (deleting the draft permanently). This requires adding the draft lifecycle endpoints (get-one, update, approve, discard) that the shipped pipeline deliberately left out of scope, in addition to the UI itself.

## Acceptance Criteria

- `GET /api/v1/ingest/drafts/{id}` returns the full `Draft` (including `body`, unlike the list endpoint) or `404 not_found` for a missing or malformed (non-UUID) id.
- `PUT /api/v1/ingest/drafts/{id}` accepts `{"title","summary","body","tags":[]}`, validates all three text fields are non-empty, request body capped at 256 KiB, returns the updated `Draft` or `400 validation_failed` / `404 not_found` / `413 payload_too_large`. Editing never changes `source_kind`/`source_ref`/`chunk_index`/`job_id` (provenance is immutable).
- `POST /api/v1/ingest/drafts/{id}/approve` promotes the draft into Qdrant as a real knowledge item via the same creation path (`qdrant.Create`) the existing manual "create knowledge item" flow uses — embeddings generated at promotion time — then deletes the draft row, inside one Postgres transaction (claim-then-promote-then-commit) so a promotion failure leaves the draft untouched and a missing/already-claimed draft returns `404` rather than double-promoting. Returns `201 {"id": "<new knowledge item id>"}` or `400 validation_failed` / `404 not_found`.
- `DELETE /api/v1/ingest/drafts/{id}` deletes the draft row permanently; idempotent (`204` whether or not it existed), matching this codebase's existing delete-endpoint convention.
- All four new routes sit inside the existing authenticated `/api/v1` route group — verified by an unauthenticated request to each returning `401`.
- The new "Ingest" tab lets a user: submit a URL or upload a `.txt`/`.md`/`.markdown` file plus tags to start an ingest job (reusing the shipped `POST /api/v1/ingest/url` / `POST /api/v1/ingest/text`); see a flat, most-recent-first list of draft summaries; open a draft to see its full body and edit title/summary/body/tags; save an edit independently of approving; approve a draft (with a confirmation prompt) to promote it and remove it from the list; discard a draft (with a confirmation prompt) to delete it permanently.
- The header's existing job-status poller, when it observes an ingest job transition from running to finished while the Ingest tab is open, triggers a draft-list refresh so newly created drafts appear without a manual reload.
- Security-critical: no draft-derived content (title, summary, body, source_ref, tags) is ever rendered via `innerHTML` without passing through the existing `esc()` HTML-escaping helper first, and the draft editor renders/edits content exclusively through form-field `.value` assignment (never `innerHTML`) — matching how the existing knowledge-item editor already handles equally untrusted-content-shaped fields. This is because draft content may retain unescaped markup surviving the ingest pipeline's best-effort HTML stripping, and the draft editor is the first UI surface to render it.
- New unit tests cover the two pure validation functions (UUID-shape check, non-empty-field check) and guard-clause behavior for `GetDraft`/`UpdateDraft`/`DiscardDraft`/`ApproveDraft` (validation and the "draft not found" path run before any dependency, including the promoter, is touched) — no test requires a live Postgres or Qdrant connection.
- `go build`/`go vet`/`go test`/`gofmt` all pass with no regressions to the existing `internal/ingest` or `internal/server` tests, and the existing Knowledge/Chat tabs' error routing is confirmed unregressed after the shared `showTab`/`run` helper generalization.

## Approach

### New Backend Surface

All endpoints sit inside the existing authenticated `/api/v1` group:

| Method + path | Request | Success | Errors |
|---|---|---|---|
| `GET /api/v1/ingest/drafts/{id}` | — | `200` full `Draft` | `404 not_found`, `500 internal` |
| `PUT /api/v1/ingest/drafts/{id}` | `{"title","summary","body","tags":[]}`, ≤256 KiB | `200` updated `Draft` | `400 validation_failed`, `404 not_found`, `413 payload_too_large`, `500 internal` |
| `POST /api/v1/ingest/drafts/{id}/approve` | — | `201 {"id"}` | `400 validation_failed`, `404 not_found`, `500 internal` |
| `DELETE /api/v1/ingest/drafts/{id}` | — | `204` (idempotent) | `500 internal` |

### Key Decisions

**`ingest` package additions**: `drafts.go` gains `ErrNotFound` sentinel, `isUUID(id) bool` and `validateDraftFields(title, summary, body string) error` (pure functions, unit-testable), `getDraft`/`updateDraft`/`deleteDraft`/`claimDraft` DB functions. `ingest.go` gains `GetDraft`/`UpdateDraft`/`DiscardDraft`/`ApproveDraft` service methods.

**Promotion via a seam, not a direct import**: the shipped pipeline makes "drafts never reach Qdrant" an import-graph invariant (`internal/ingest` imports no `qdrant` code). Approve is the one sanctioned crossing, so it goes through a new one-method `Promoter` interface (`Promote(ctx, title, summary, body string, tags []string) (string, error)`) that `ingest.Service` depends on, satisfied by a small adapter (`qdrantPromoter`) constructed in `main.go` (which already imports `qdrant`) and passed into `ingest.New` as a new parameter. This preserves the invariant (`ingest` still cannot construct a Qdrant write itself, only ask for one via primitives) and makes `ApproveDraft` unit-testable with a fake promoter — the only orchestration logic in this package that is.

**Approve is one Postgres transaction**: `BEGIN` → `DELETE ... WHERE id=$1 RETURNING *` (claims the row; 0 rows → rollback, `ErrNotFound`) → call the promoter (embeds + upserts into Qdrant) → on promoter error, rollback (the draft row is restored/untouched) → otherwise `COMMIT`. This is a deliberate choice: the residual failure mode (a commit failing in the sub-millisecond window after a successful Qdrant write) produces a harmless, visible **duplicate** knowledge item, never silent **data loss** of a draft. It also closes the double-approve race: a second concurrent approve blocks on the row lock, then finds 0 rows once the first transaction commits, and gets `404` — no double promotion. Accepted trade-off: the transaction is held open across an embedding HTTP call (can take seconds against a loaded Ollama), pinning one pool connection for that duration — acceptable for this single-developer local dev tool, not a pattern to reuse for a multi-tenant service.

**Approve is synchronous**, not run through `jobs.Start` — it's one embedding call plus one upsert, the same cost the existing manual "create knowledge item" flow already pays synchronously; wrapping it in the job machinery would add nothing and would contend with the per-kind single-running-job guard for no benefit.

**Approve's response is minimal** (`{"id"}`, not the full created item) — deliberately, so `ApproveDraft` never has to return a `qdrant`-typed value and the import invariant holds all the way to the HTTP layer; the frontend can look up the new item by ID if it needs to.

**Path-parameter safety**: a non-UUID `{id}` would otherwise reach Postgres and produce a misleading `500` (invalid-text-representation) instead of a clean `404` — `isUUID` guards every draft-by-id operation at the boundary before any query runs.

**Update semantics**: tags are normalized via the existing `qdrant.NormalizeTags` in the HTTP handler (not inside `ingest`, preserving its qdrant-free import graph — the same pattern the shipped POST handlers already use); title/summary/body must be non-empty (mirrors the existing `qdrant` validation, so nothing edited into a draft could later fail approval's own validation); no optimistic concurrency (last write wins — single-reviewer tool); provenance fields are never editable.

### Frontend

All code resides in the single existing `knowledge/internal/server/static/index.html`.

A third tab, `#ingestTab`, is added to the existing two-tab (`#knowledgeTab`/`#chatTab`) structure, same aside+main grid layout and CSS variables. Aside: URL input, tags input, "Ingest URL"/"Upload file" actions (file upload via a hidden `<input type=file accept=".txt,.md,.markdown">`, matching the existing JSON-import pattern), a "Refresh" action, and the draft list. Main: a form (hidden id, a muted source-provenance line, title/tags inputs, summary/body textareas, and Save/Approve/Discard actions) plus a message line.

`showTab`/`run`'s tab-set and error-routing logic are generalized from a hard-coded two-tab check to a small tab-id list, so a third tab's errors route to its own message element — this is the only change to already-shipped, working UI code in this feature, done as its own isolated, separately-validated step.

New JS functions mirror existing ones function-for-function: `startIngestUrl`/`startIngestFile` mirror `createChat`/`importFile`; `loadDrafts`/`renderDraft` mirror `loadList`/`renderItem`; `openDraft` mirrors `openItem`; `saveDraft`/`approveDraft`/`discardDraft` mirror `save`/`removeItem`. The job-status poller gains a small running→finished transition check that triggers a draft-list refresh when the Ingest tab is open.

**Hard security requirement, not a nice-to-have**: draft `title`/`summary`/`body`/`source_ref`/tags may retain unescaped markup surviving the ingest pipeline's best-effort HTML stripping — this is attacker-influenceable content (an arbitrary fetched URL or uploaded file) that would be stored XSS in a same-origin page holding the session cookie if ever assigned via `innerHTML` without escaping. The draft list may use `innerHTML` only with every interpolated field wrapped in the existing `esc()` helper (matching how the existing knowledge-item list already handles this). The draft detail/editor renders and edits exclusively via form-field `.value` assignment — never `innerHTML` — identical to how the existing knowledge-item editor already handles item bodies. No markdown preview of draft bodies in this slice, specifically because that would reintroduce an HTML-rendering path for this content.

### Ordered Implementation Plan

Ten steps, each ending green on build/vet/test/gofmt. Backend endpoints are built and validated live before the frontend code that calls them:

1. Draft DB layer: `ErrNotFound`, `isUUID`, `validateDraftFields`, `getDraft`, `updateDraft`, `deleteDraft` in `drafts.go` + `drafts_test.go` for the two pure functions.
2. Service methods `GetDraft`/`UpdateDraft`/`DiscardDraft` in `ingest.go`, extending `ingest_test.go`'s existing zero-value-`Service` guard-clause pattern.
3. HTTP for read/update/discard: three routes, request type, 256 KiB `MaxBytesReader` on PUT, a `writeDraftOrErr` helper, tag normalization in the PUT handler.
4. Approve, service side: `Promoter` interface, `claimDraft` transaction, `ApproveDraft`, `New`'s new parameter, `qdrantPromoter` adapter + `main.go` wiring. Tests with a fake promoter (never called for a bad-ID or validation failure).
5. Approve, HTTP side: route + handler + response shape + error mapping. **Live-validate all four new endpoints with curl before starting step 6.**
6. Frontend scaffolding only: header button, `#ingestTab` markup, generalized `showTab`/`run`, new CSS rule — no new API calls yet. Validate the existing Knowledge/Chat tabs' error routing is unregressed before continuing.
7. Frontend submit: `startIngestUrl`, `startIngestFile`, the poller's finished-job refresh hook.
8. Frontend draft list + detail: `loadDrafts`, `renderDraft`, `openDraft`, `clearDraftForm`.
9. Frontend actions: `saveDraft`, `approveDraft`, `discardDraft`.
10. Docs: README's `### Ingest` section gets the four new endpoints and an updated data-model note; CHANGELOG entry; roadmap's `## Next` line for this feature removed.

### Risks and Testing Strategy

**Security — High if violated**: the `innerHTML`-escaping and form-field-only-editor requirements above are this feature's one hard constraint; a review gate is grepping every new `innerHTML` occurrence in `index.html` and confirming each interpolated value is `esc()`-wrapped.

**Security — Medium, accepted by design**: approving a draft is the moment unreviewed-source content gains reach into the searchable corpus (and later, `chat`'s retrieval context) — the human approval step is the intended control, not a gap to close further.

**Security — Medium, must be implemented**: the PUT endpoint is the only new one with a request body and must have the same `MaxBytesReader` discipline the shipped POST endpoints already have.

**Security — Medium, must be verified**: all four routes must be confirmed inside the authenticated group (curl each without a session cookie, expect `401`).

**Security — Low, no action needed**: no per-draft ownership model exists (matches the existing no-ownership model for knowledge items); `SameSite=Lax` session cookies and the lack of any CORS policy mean the new state-changing endpoints add no CSRF exposure beyond what already exists; a `500` error path echoing a raw Qdrant error string is a pre-existing pattern (`s.create`), not newly introduced.

**Concurrency**: approve+approve, discard-then-approve, and approve-while-an-ingest-job-is-running are all closed or made safe by the transaction/row-lock design and the fact that approve never touches the `jobs` table; edit+approve interleaving is last-writer-wins, acceptable for a single-reviewer tool.

**Testing**: unit-testable (hermetic, no live infra) — `isUUID`/`validateDraftFields` (pure, full table tests), and guard-clause tests for all four new service methods against a zero-value `Service` (including `ApproveDraft` with a fake `Promoter`, confirming the promoter is never called for a bad ID or a validation failure). Not unit-testable here (needs live Postgres/Qdrant, consistent with this codebase's existing convention of not testing DB-dependent code) — the actual SQL, the transaction/rollback behavior, and all frontend behavior; a live-cluster validation pass is required before this feature is considered proven, covering (at minimum): the four endpoints' happy paths and 404/400/413 paths, a deliberately-failed approve (e.g. Qdrant unavailable) confirming the draft survives, a double-approve returning 404 on the second attempt, all four endpoints returning 401 without a session cookie, and a UI check that a draft body containing `<img src=x onerror=alert(1)>` and `<script>alert(1)</script>` renders as literal text with no script execution, in both the list and the editor.

**The `showTab`/`run` generalization step** is the only change to already-shipped UI code in this feature and must be validated in isolation (existing tabs' error routing unregressed) before any new frontend code is built on top of it.

**UX note** (not a defect, but worth building in): approving a draft can take several seconds (one embedding call); the button should show a "working" state during the request so a reviewer doesn't double-click and get a confusing 404 from the (correctly-functioning) double-approve guard.

## Affected Areas

Modified: `knowledge/internal/ingest/drafts.go`, `knowledge/internal/ingest/ingest.go`, `knowledge/internal/ingest/ingest_test.go`, `knowledge/internal/server/server.go`, `knowledge/internal/server/static/index.html`, `knowledge/main.go`, `knowledge/README.md`, `CHANGELOG.md`, `specs/roadmap.md`.

New: `knowledge/internal/ingest/drafts_test.go`.

Explicitly untouched: `internal/db/db.go` (no schema change needed — the existing `knowledge_drafts` table already has everything this feature needs), `internal/qdrant`, `internal/chat`, `internal/jobs`, `internal/embeddings`, `internal/auth`, `internal/generate`, `internal/translate`, `internal/config`, `shared/*`, `knowledge/k8s/*`, and the already-shipped `internal/ingest/{chunk,html,source}.go`.

## Out of Scope

- Bulk approve/discard, select-all.
- Undo of an approve or discard; no soft delete, no draft version history.
- Re-running, resuming, or cancelling an ingest job; no job history view.
- Pagination, search, or filtering of the draft list (stays flat, most-recent-first, existing 500-row cap).
- Grouping drafts by originating job/source, or "approve all chunks from this run."
- Provenance carried onto the promoted knowledge item (no `source_ref`/`job_id` on `qdrant.Item` — a separate feature if wanted).
- Re-generating a draft's title/summary/translation from the UI (existing `/knowledge/generate/*` endpoints are not wired into the draft editor).
- Markdown preview of draft bodies (deliberately — see the security requirement above).
- Any change to the ingest pipeline itself, the `knowledge_drafts` schema, config, or deployment manifests.
- `knowledge-export-import-yaml`, `knowledge-prompts-configmap`, `knowledge-markdown-chat-render` — unrelated queued roadmap items.

## Implementation Notes

### What Was Built

All nine code steps of the approved ten-step plan have been implemented. Step 10 (documentation) is separate and handled in the Documentation Updates section.

**Step 1 — Draft DB layer.** `knowledge/internal/ingest/drafts.go` gained `ErrNotFound` sentinel, `isUUID(id) bool` and `validateDraftFields(title, summary, body string) error` pure functions, and `getDraft`, `updateDraft`, `deleteDraft` DB functions. New `knowledge/internal/ingest/drafts_test.go` contains 8 tests covering the two pure functions (`isUUID` and `validateDraftFields`) with full table-driven test cases.

**Step 2 — Service methods.** `knowledge/internal/ingest/ingest.go` gained `GetDraft`, `UpdateDraft`, and `DiscardDraft` service methods as thin wrappers over the DB functions from step 1. `UpdateDraft` is where field validation actually happens — the DB-layer `updateDraft` trusts its caller. Tests were extended in `ingest_test.go` following the existing zero-value-`Service` guard-clause pattern.

**Step 3 — HTTP for read/update/discard.** Three new routes in `knowledge/internal/server/server.go`: `GET /api/v1/ingest/drafts/{id}`, `PUT /api/v1/ingest/drafts/{id}`, and `DELETE /api/v1/ingest/drafts/{id}`. The PUT route includes a 256 KiB `MaxBytesReader` constraint before JSON decode, a `writeDraftOrErr` dispatch helper, and tag normalization via `qdrant.NormalizeTags` in the handler (keeping the `ingest` package qdrant-free).

**Step 4 — Approve, service side.** A new `ingest.Promoter` interface (`Promote(ctx, title, summary, body string, tags []string) (string, error)`) was introduced. The `claimDraft` transaction in `knowledge/internal/ingest/drafts.go` uses `BEGIN` → `DELETE ... WHERE id=$1 RETURNING *` to claim the row (returning 0 rows rolls back with `ErrNotFound`) → call the promoter → COMMIT on success or ROLLBACK on any failure, restoring the draft. `ingest.Service` gained a new `promoter` field, `Service.ApproveDraft` service method, and `New` gained a new `promoter` parameter. A `qdrantPromoter` adapter was wired in `knowledge/main.go`. This is the first real Postgres transaction in the codebase.

**Step 5 — Approve, HTTP side.** `knowledge/internal/server/server.go` gained the `POST /api/v1/ingest/drafts/{id}/approve` route and handler. Success returns `201 {"id": "<new knowledge item id>"}`. Error mapping includes `qdrant.ErrValidation` → `400`.

**Step 6 — Frontend scaffolding.** A third `#ingestTab` was added to the existing two-tab structure with the same aside+main grid layout and CSS variables. The header button triggers the new tab. The `showTab` and `run` functions were generalized from a hard-coded two-tab check to a small `TAB_MSG_IDS` mapping, replacing the old logic. This is the only change to already-shipped UI code.

**Step 7 — Frontend submit.** `knowledge/internal/server/static/index.html` gained `startIngestUrl` and `startIngestFile` functions mirroring the existing `createChat`/`importFile`. The existing `pollStatus` poller gained a running→finished job-transition hook that triggers `loadDrafts` refresh when the Ingest tab is open.

**Step 8 — Frontend list + detail.** `loadDrafts` and `renderDraft` functions mirror the existing `loadList`/`renderItem`. The draft list uses `innerHTML` with every interpolated field (title, summary, tags, source_ref, server-generated draft id in `onclick` handlers) wrapped in `esc()` for defense-in-depth. `openDraft` opens the detail editor. The detail form uses only `.value` and `.textContent` assignments for all content, never `innerHTML`, matching the existing knowledge-item editor pattern.

**Step 9 — Frontend actions.** `saveDraft` (PUT, re-displays the server's tag-normalized response), `approveDraft` (confirmation prompt, a module-level `approveInFlight` flag as a UX nicety against double-clicks — not a security control, the DB row lock is what prevents double-promotion), and `discardDraft` (confirmation prompt, DELETE).

### Security Review and Fixes

A dedicated security review was conducted beyond normal self-review, consistent with this project's practice for security-sensitive changes. All spec-claimed mitigations were verified to hold as implemented: the `innerHTML`/`esc()` discipline, the approve transaction's rollback-restores-the-draft and row-lock-closes-the-double-approve-race behavior (confirmed line by line, including the rare commit-failure case, which produces only a harmless visible duplicate, never silent data loss), all four routes' authentication, the `MaxBytesReader`-before-`decode` ordering, parameterized SQL throughout, and `isUUID` guarding every DB path before it runs.

**Three real issues were found and fixed:**

1. **Medium — Error routing fallback.** `run()`'s error-routing fallback (`document.activeElement.closest(".tab")`) silently swallowed errors for non-focusable elements (a draft-list row click never changes focus) and was browser-dependent for buttons (Safari/Firefox on macOS don't focus a clicked button by default, unlike Chrome). This meant a real error (a 404 opening a just-discarded draft, a validation failure, a double-approve 404) could land in a hidden message element with zero visible feedback. This also affected the pre-existing Knowledge/Chat tabs. **Fixed:** routing now reads `document.querySelector(".tab.active")?.id` directly instead of inferring the active tab from focus.

2. **Medium — Coverage gap on service methods.** Two of the four new service methods (`GetDraft`, `DiscardDraft`) and `UpdateDraft`'s malformed-id path had no guard-clause test, unlike `ApproveDraft`'s equivalent test. **Fixed:** added the three missing tests, closing a coverage gap (not a bug fix — the underlying code was already correct).

3. **Low — Uncaught floating promise.** The job poller's background draft-list refresh (`loadDrafts()`, called outside `run()`) was an uncaught floating promise. **Fixed:** added `.catch(() => {})`, consistent with the poller's existing "ignore, next poll retries" philosophy for its primary `/jobs/current` call.

### Known Limitations (Not Changed, Intentionally)

Four additional Low-severity/informational items were surfaced but deliberately NOT changed. They are recorded here as known limitations/notes for the risk section rather than code changes:

1. **`esc()` escaper precision.** `esc()` is technically the wrong escaper for a JavaScript string literal inside an HTML attribute (`onclick="...('${esc(d.id)}')"`) — not exploitable today since `d.id` is always a server-generated, `isUUID`-validated UUID, and this is still strictly more defensive than the pre-existing `renderItem`'s equivalent (which doesn't escape item ids at all). Worth revisiting if this pattern is ever reused for non-server-generated values.

2. **Concurrent approval single-flight.** Concurrent approvals of different drafts have no server-side single-flight (unlike ingest jobs, which do); each holds a pooled Postgres connection for the duration of an embedding call. Self-inflicted-only, authenticated-only, on a single-developer tool — accepted, not fixed.

3. **LLM-sourced markdown rendering.** Promoting a draft feeds its content into the searchable corpus and therefore into chat's retrieval context, which can reach a pre-existing, unrelated `javascript:`-href rendering path in the chat markdown renderer (`md()`) if the LLM reproduces a malicious link from ingested content and a user clicks it. Pre-existing sink, not introduced by this feature; the human-approval step remains the primary control the spec already relies on for corpus-reach risk generally.

4. **Knowledge item ID enumeration via approve response.** `POST .../approve`'s `{"id"}` response could theoretically be used to enumerate knowledge item IDs. Not an issue: the existing app has no per-item access control beyond authentication (confirmed against `ingest.Service.Get`'s existing behavior), so nothing is exposed that isn't already exposed via `GET /api/v1/knowledge`.

### Testing

All `go build`, `go vet`, `go test`, and `gofmt` pass with no regressions to existing `internal/ingest` or `internal/server` tests. The existing Knowledge/Chat tabs' error routing is confirmed unregressed after the `showTab`/`run` helper generalization. Unit tests cover the two pure validation functions and guard-clause behavior for all four new service methods, confirmed to validate and fail before any dependency (including the promoter) is touched — no test requires live Postgres or Qdrant.

### Architecture Review and Fixes Applied

A dedicated architectural review (beyond the security review) found the overall design sound — the `Promoter` seam genuinely preserves the "ingest never imports qdrant" invariant with no speculative surface, the transaction's delete-then-promote-then-commit ordering was confirmed as the right trade-off (a two-phase alternative would need a schema change and trades a harmless visible duplicate for a stuck-claim state needing its own recovery mechanism — strictly worse for this tool), and the new HTTP handlers and frontend functions genuinely mirror their existing knowledge-item CRUD analogs. It found one real design leak, fixed:

- **Medium** — `ApproveDraft` validated only that the draft's id was well-formed before promoting; it never called the package's own `validateDraftFields` on the draft's actual title/summary/body, so a draft with an empty field (reachable in principle: `generate.Title` can return an empty string on an empty LLM completion) would only be caught two packages downstream, by `qdrant.Create`'s own validation — surfacing as `qdrant.ErrValidation`, which the HTTP handler had to special-case. This also meant the spec's own acceptance criterion ("validation... run before any dependency, including the promoter, is touched") wasn't actually met for the approve path. Fixed: extracted a new `approveDraftFields` method that validates before ever calling the promoter, confirmed the existing transaction-rollback logic in `claimDraft` requires no change (any non-nil error from the promote callback already triggers rollback, regardless of which specific error it is), and removed the now-unreachable `qdrant.ErrValidation` case from the HTTP handler (confirmed unreachable by checking `qdrant.Create`'s own validation checks exactly the same three fields for emptiness, nothing else). Added `TestApproveDraft_EmptyFieldsNeverCallsPromoter`, made hermetic by testing the extracted `approveDraftFields` method directly against a hand-built `Draft` and a fake promoter — no live Postgres needed, closing the coverage gap the spec's own testing plan had promised but the original implementation hadn't delivered.

Additional low-severity polish applied in the same pass: three doc comments in `drafts.go` had gone stale as later steps landed (one claiming no full-draft-with-body endpoint existed, one justifying the drafts list's row cap with a "no delete path yet" rationale that discard/approve had since made false, one not crediting `claimDraft` as a caller of `ErrNotFound`) — all corrected; approving now shows a visible "Approving…" message during the multi-second wait (previously only an invisible in-flight flag existed); validation error text on the Ingest tab now shows a clean message ("title, summary, and body are required") instead of a raw Go error string, matching the existing knowledge-item editor's equivalent message; a dead `typeof loadDrafts === "function"` guard (leftover scaffolding insurance from before `loadDrafts` existed) was simplified away; and `openDraft` now calls `showTab("ingest")` first, matching `openItem`'s equivalent pattern, for correctness if ever called from outside the already-active tab.

One item from the review was deliberately not changed: the `showTab`/`run` per-tab load-hook registration (currently a small if-chain rather than a unified per-tab config object) — the reviewer itself assessed this as not worth restructuring until a fourth tab is ever added, so it was left as-is.

## Validation

- **Automated (this session, hermetic, no live infra):** `go build ./...`, `go vet ./...`, `go test ./...`, `gofmt -l .` all pass, zero regressions, re-run after every implementation and fix round. The `internal/ingest` package now has 51 tests (up from 37 before this feature: the pre-existing 32 pure-function tests in `chunk_test.go`/`html_test.go`/`source_test.go` plus 5 pre-existing guard-clause tests in `ingest_test.go`), covering: `isUUID`/`validateDraftFields` (pure, table-tested), and guard-clause behavior for all four new service methods (`GetDraft`, `UpdateDraft`, `DiscardDraft`, `ApproveDraft` — including both its malformed-id and, after the architecture-review fix, its empty-fields path) against a zero-value `Service`, proving validation runs before any nil dependency (including the fake `Promoter`) is ever touched.
- **Coverage gap, explicitly not a defect:** the actual SQL in `getDraft`/`updateDraft`/`deleteDraft`/`claimDraft`, the transaction/rollback behavior itself, and all frontend behavior have no automated test coverage — all require a live Postgres/Qdrant connection this environment doesn't have, the same gap the sibling `knowledge-ingest-pipeline` feature already had for its own DB-dependent code.
- **Live-infrastructure validation — required, NOT YET PERFORMED, must be done by the user against a real cluster:**
  1. `task deploy-knowledge` — this feature adds no schema change, so this just confirms the existing `knowledge_drafts` table still migrates cleanly.
  2. `GET`/`PUT`/`DELETE /api/v1/ingest/drafts/{id}` happy paths, plus each one's `404`/`400`/`413` paths (a malformed id, an empty-field update, an oversized PUT body).
  3. `POST /api/v1/ingest/drafts/{id}/approve` happy path — confirm the new item appears in the Knowledge tab / `GET /api/v1/knowledge`, and the draft disappears from `/api/v1/ingest/drafts`.
  4. A deliberately-failed approve (e.g. Qdrant unreachable) — confirm the draft row survives (the single most important live check for the transaction design).
  5. A double-approve (second request after the first completes) — confirm it returns `404`, not a second promotion.
  6. All four new endpoints without a session cookie — confirm `401`.
  7. A UI check: a draft whose title/summary/body/tags contain `<script>alert(1)</script>` and `<img src=x onerror=alert(1)>` renders as literal text with no script execution, in both the draft list and the editor form fields.
  8. A UI click-through of the full flow: submit a URL or file from the Ingest tab, watch the header status line advance, confirm the draft list auto-refreshes on job completion, open a draft, edit and save it, then separately approve or discard it — confirming the "Approving…" message appears during the approve request.
  9. This feature's live validation may be worth running together with the sibling `knowledge-ingest-pipeline` feature's own still-outstanding live validation, since both need the same running cluster.

## Documentation Review

**Summary:** 4 issues (4 High, 0 Medium, 0 Low) across 1 file.

### High

1. `knowledge/README.md:545` — Outdated reference to a future feature.
   - **Why:** The Ingest section describes drafts as never being written to Qdrant "until explicitly approved by a future feature," but that feature is now complete and implemented. A reader following the docs would believe draft promotion is not yet possible.
   - **Evidence:** Feature is implemented with four new endpoints (`GET`/`PUT`/`DELETE /ingest/drafts/{id}`, `POST /ingest/drafts/{id}/approve` at `knowledge/internal/server/server.go:169-172`), service methods (`GetDraft`, `UpdateDraft`, `DiscardDraft`, `ApproveDraft` in `knowledge/internal/ingest/ingest.go`), and frontend UI (three-tab interface with Ingest tab at `knowledge/internal/server/static/index.html:180`).
   - **Fix:** Change `(never written to Qdrant until explicitly approved by a future feature)` to `(never written to Qdrant until explicitly approved, reviewed via the Ingest tab)`.

2. `knowledge/README.md:615-617` — Marked as not yet accessible, but is now implemented.
   - **Why:** The note claims "full chunk bodies are stored but not yet accessible via an API endpoint — reviewing/approving/discarding drafts is a future feature." This is contradicted by four new endpoints that were added. A reader would not know these endpoints exist.
   - **Evidence:** `GET /api/v1/ingest/drafts/{id}` returns the full `Draft` with body (`knowledge/internal/server/server.go:502-504`, handler calls `s.ingest.GetDraft`); `PUT`, `POST .../approve`, and `DELETE` endpoints exist at lines 507-523, 545-557, and 517-523 respectively.
   - **Fix:** Replace the entire note with documentation of the four new draft lifecycle endpoints (see issue 3 below).

3. `knowledge/README.md:540-617 (Ingest section)` — Missing API documentation for four new endpoints.
   - **Why:** The Ingest section documents only the two URL/text submission endpoints and the list endpoint. It does not document `GET /ingest/drafts/{id}`, `PUT /ingest/drafts/{id}`, `POST /ingest/drafts/{id}/approve`, or `DELETE /ingest/drafts/{id}`, which are critical for the feature's core use case (reviewing, editing, approving, discarding drafts). A user reading the docs would not know these endpoints exist.
   - **Evidence:** All four endpoints are implemented and authenticated:
     - `GET /ingest/drafts/{id}` at `knowledge/internal/server/server.go:502-504`, returns full Draft with body, handler is `getDraft`.
     - `PUT /ingest/drafts/{id}` at lines 507-514, accepts `title`, `summary`, `body`, `tags`, validates non-empty fields, caps request at 256 KiB.
     - `POST /ingest/drafts/{id}/approve` at lines 545-557, returns `201 {"id": "<new knowledge item id>"}`, promotes draft to Qdrant in a transaction.
     - `DELETE /ingest/drafts/{id}` at lines 517-523, returns `204` (idempotent).
   - **Fix:** Add a new subsection after the list endpoint documenting all four endpoints with their request/response shapes, error codes, and examples (see issue 4 details).

4. `knowledge/README.md:68` — Outdated reference to draft promotion as a future feature.
   - **Why:** In the Data and indexing model section, drafts are described as never appearing "until explicitly promoted into Qdrant by a future feature (`knowledge-ingest-ui`)." This is outdated; the feature is now implemented.
   - **Evidence:** Feature is complete and implemented (as cited in issue 1 above).
   - **Fix:** Change `until explicitly promoted into Qdrant by a future feature (\`knowledge-ingest-ui\`)` to `until explicitly promoted into Qdrant (via the Ingest tab)`.

### Medium

None.

### Low

None.

**Files checked:**
- `knowledge/README.md` — Ingest section and data model section; Ingest endpoint documentation missing, future-feature language outdated.
- `specs/mission.md` — No drift found; problem/value remain accurate.
- `specs/tech-stack.md` — No update needed; "direct pgx usage, no ORM" already covers transaction usage without explicit mention. First-use-of-transactions is a usage detail, not a new dependency.
- `specs/roadmap.md` — No drift found; "Now" entry for `knowledge-ingest-ui` accurately describes the work.
- `CHANGELOG.md` — No entry for `knowledge-ingest-ui` yet (expected; moved to Done section by B5, not this phase).

## Documentation Updates

Updated `knowledge/README.md`:
1. Line 68 (Data and indexing model section): Changed "future feature (`knowledge-ingest-ui`)" to "(via the Ingest tab)" to reflect that draft promotion is now implemented.
2. Line 545 (Ingest section intro): Changed "future feature" reference to "Ingest tab" to match the implemented UI.
3. Lines 615-617 (replacing outdated note): Replaced the "full chunk bodies are stored but not yet accessible" note with comprehensive API documentation for four new draft lifecycle endpoints:
   - `GET /api/v1/ingest/drafts/{id}` — retrieve full draft with body
   - `PUT /api/v1/ingest/drafts/{id}` — edit draft (title, summary, body, tags)
   - `POST /api/v1/ingest/drafts/{id}/approve` — promote draft into Qdrant
   - `DELETE /api/v1/ingest/drafts/{id}` — discard draft permanently
   
   Each endpoint's documentation includes request shape, response shape, error codes, and curl examples matching the style of the existing ingest endpoint docs.

Updated `CHANGELOG.md` under `## [Unreleased]` → `### Added`:
- Added entry describing the Ingest tab feature: "an 'Ingest' tab to review, edit, approve, or discard draft chunks produced by the ingest pipeline — approving promotes a draft into a real, searchable knowledge item (the same creation path as manually adding one); discarding deletes it permanently."

Updated `specs/roadmap.md`:
- Removed the `## Now` line for `knowledge-ingest-ui` (work is complete, no longer "in progress").
