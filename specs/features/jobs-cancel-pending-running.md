---
title: Cancel a pending or running job, interrupting an in-flight LLM call
kind: feature
status: done
version: 1
updated: 2026-09-25
branch: main
---

## Problem / Motivation

The Jobs page offers no way to stop a `pending` or `running` job — only
`Retry` (for `failed` jobs) and `Delete` (for `done`/`failed` jobs) exist
(`phraseforge/internal/jobs/jobs.go`, `phraseforge/internal/server/jobs.go`,
`jobs-app.js`). A job stuck mid-LLM-call has no admin-facing way to abort
it short of restarting the whole pod. This matters more now that
`llm-purpose-timeout-and-prompt-config` (approved, not yet started) flags
this queue's single-worker head-of-line-blocking as an accepted risk — a
Cancel action materially reduces that risk's impact, since a stuck job no
longer has to run its full timeout to unblock the queue.

This is feasible today because `shared/llm/llm.go:137` builds every
outbound Ollama request via `http.NewRequestWithContext(ctx, ...)` —
cancelling a context genuinely aborts an in-flight call, not just a
client-side no-op. The queue is also single-worker with a DB-level
guarantee that at most one row is ever `status='running'`
(`jobs_one_running_idx`), so "cancel the currently running job" is never
ambiguous about which job is meant.

`Status` gains a new value, `cancelled`, distinct from `failed` (confirmed
decision) — a job an admin deliberately stopped should read differently in
the Status column than one that genuinely errored.

## Acceptance Criteria

- [ ] `jobs.Status` gains `StatusCancelled = "cancelled"`; the DB's
  `jobs_status_check` CHECK constraint is updated to allow it via an
  idempotent migration (`DROP CONSTRAINT IF EXISTS` / `ADD CONSTRAINT`,
  mirroring the existing `llm_prompts_kind_check` pattern) — confirmed via
  a live DB inspection that the constraint's actual name is
  `jobs_status_check`.
- [ ] `jobs.Service` gains `Cancel(ctx, id) error`:
  - A `pending` job is cancelled by a direct, guarded UPDATE
    (`WHERE status='pending'`) — the worker never sees it, since its claim
    query only selects `pending` rows.
  - A `running` job is cancelled by invoking a per-job
    `context.CancelFunc` the worker stores (under a dedicated mutex) for
    the single currently-running job right after a successful claim, and
    clears once the handler returns. The worker's completion path writes
    `StatusCancelled` (not `StatusFailed`) specifically when `Cancel` was
    actually invoked for that job — tracked via an explicit flag set by
    `Cancel` before calling the cancel func, not by inspecting the
    handler's returned error alone (a plain `errors.Is(err,
    context.Canceled)` check would also fire on ordinary worker shutdown,
    mislabeling every in-flight job on a routine restart as
    admin-cancelled).
  - A narrow claim-window race (a claim's `UPDATE` commits `status
    ='running'` a moment before the worker stores its cancel-func slot) is
    closed by recording the requested id in a small pending-cancel set the
    worker checks (and clears) in the same critical section where it
    stores the slot — no blocking, no polling.
  - Neither path applying (job already `done`/`failed`/`cancelled`, or
    unknown id) returns the same single "cannot cancel" error shape
    `Delete` already uses for its own race window — no new error class.
- [ ] `Retry` and `Delete` both also accept a `cancelled` job, exactly as
  they already accept `failed` (a cancelled job is retryable/deletable).
- [ ] `EnqueueAndAwait` (used by the interactive `/llm/generate` path)
  treats `StatusCancelled` as terminal, returning a clear error, instead of
  polling until its own caller-side timeout.
- [ ] `FailStale` is unchanged — a job orphaned by a crash/restart is still
  `failed`, not `cancelled` (a restart is not a deliberate cancellation).
- [ ] New `POST /api/v1/admin/jobs/{id}/cancel` endpoint (mirroring
  `apiRetryAdminJob`/`apiDeleteAdminJob`'s shape and auth), inside the
  existing `requireAdmin` route group.
- [ ] Jobs page table: a **Cancel** button (with its own confirm dialog,
  matching the existing Retry-confirm pattern) replaces Retry/Delete for
  `pending`/`running` rows; Retry and Delete keep their current
  positions/behavior for `failed`/`done`/`cancelled` rows. View remains
  available for every status.
- [ ] New i18n keys (`jobs.cancel`, `jobs.cancel_confirm`) added to both
  `en`/`pl` maps and `jobsAppI18nKeys`.
- [ ] `go test ./...` passes; documented as an accepted constraint (not a
  bug to fix here): in-memory cancellation only works because this
  Deployment runs a single replica (`replicas: 1`) — a future
  multi-replica rollout would need a DB-level `cancel_requested` signal
  instead, out of scope for now.

## Approach

1. **`phraseforge/internal/db/schema.sql`**: update the inline `status`
   CHECK list (so fresh installs already include `cancelled`) and append
   an idempotent `ALTER TABLE jobs DROP CONSTRAINT IF EXISTS
   jobs_status_check; ALTER TABLE jobs ADD CONSTRAINT jobs_status_check
   CHECK (status IN ('pending','running','done','failed','cancelled'));`
   for existing installs, following the exact pattern already used for
   `llm_prompts_kind_check`.
2. **`phraseforge/internal/jobs/jobs.go`**:
   - Add `StatusCancelled Status = "cancelled"` to the existing const
     block.
   - Add `runMu sync.Mutex`, a `running *runningJob` (holding the id and
     `context.CancelFunc` of the one currently-executing job, plus a
     `cancelled bool` flag), and a small pending-cancel-id set to the
     `Service` struct.
   - In the claim/run path: derive `jobCtx, cancelJob :=
     context.WithCancel(ctx)` after a successful claim; store the slot
     under `runMu` (checking/clearing the pending-cancel set in the same
     critical section, self-cancelling immediately if the id is already
     present); pass `jobCtx` (not the bare worker `ctx`) to the handler;
     clear the slot in a `defer`.
   - Thread the `cancelled` flag into the completion path so it writes
     `StatusCancelled` instead of `StatusFailed` when set; the
     completion path's own `context.Background()`+10s-timeout write and
     `AND status='running'` guard are otherwise unchanged.
   - Add `Cancel(ctx, id) error`: try the `pending`-guarded UPDATE first;
     if 0 rows, check the running slot under `runMu` — if the id matches,
     set the `cancelled` flag and invoke the stored cancel func; otherwise
     return the `Delete`-style "cannot cancel" error, recording the id in
     the pending-cancel set only when a claim could plausibly be racing
     (i.e., the DB row was seen as `running` moments ago).
   - Widen `Retry`'s status check and `Delete`'s `WHERE status IN (...)`
     to also accept `cancelled`.
   - Add a `StatusCancelled` case to `EnqueueAndAwait`'s terminal-status
     switch.
3. **`phraseforge/internal/server/jobs.go`**: add `apiCancelAdminJob`
   (copy `apiDeleteAdminJob`'s shape, call `s.jobs.Cancel`, return 204);
   add `jobs.cancel`/`jobs.cancel_confirm` to `jobsAppI18nKeys`.
4. **`phraseforge/internal/server/server.go`**: register `POST
   /jobs/{id}/cancel` beside the existing retry/delete routes, inside the
   same `requireAdmin` group.
5. **`phraseforge/internal/i18n/i18n.go`**: add `jobs.cancel`/
   `jobs.cancel_confirm` to both `en`/`pl` maps.
6. **`jobs-app.js`**: in `render`, add a Cancel button enabled for
   `pending`/`running`; change Retry's enable condition to `failed ||
   cancelled` and Delete's to `done || failed || cancelled`. Add a Cancel
   click handler cloned from Delete's shape: confirm first
   (`T("jobs.cancel_confirm")`, danger styling), disable the row's buttons
   before the request, `POST .../cancel`, `await loadAndRender()`, then
   set the status message.
7. **`phraseforge/CHANGELOG.md`**: `### Added` entry once implemented (B4).

**Testing strategy** (real-LLM-call budget: at most 1, held in reserve,
not spent unless genuinely needed): the core claim→cancel→
`context.Canceled`→`StatusCancelled` flow, the claim-window race fix,
Retry/Delete accepting `cancelled`, and the "already terminal" error path
are all provable with a test-only job kind whose fake handler blocks on
`<-ctx.Done()` — zero real LLM calls. This needs a real Postgres (the
`cancelled` CHECK value is itself part of what's under test); if standing
up DB-backed Go tests for `phraseforge/internal/jobs` (which has none
today) is more than this slice should take on, the fallback is a manual
k3d verification covering the same scenarios, folded into the real-call
budget: start a real `generate_vocab_from_text` job, click Cancel, confirm
the row reaches `cancelled` within seconds (not the full timeout) and the
pod log shows the call aborting — one real call, not one completed one.

## Affected Areas

- `phraseforge/internal/db/schema.sql`
- `phraseforge/internal/jobs/jobs.go` (and a new test file, if DB-backed
  tests are added per the Approach's testing note)
- `phraseforge/internal/server/jobs.go`, `server.go`
- `phraseforge/internal/i18n/i18n.go`
- `phraseforge/internal/server/static/js/jobs-app.js`
- `phraseforge/CHANGELOG.md`

## Out of Scope

- Multi-replica support for cancellation (a DB-level `cancel_requested`
  signal the worker polls) — documented as an accepted constraint of this
  single-replica deployment, not built now.
- Any change to `llm-purpose-timeout-and-prompt-config`'s own scope — the
  two features don't conflict (different files beyond `schema.sql`/
  `i18n.go`, different key prefixes) and this one can land first or after
  it in either order.
- Changing `FailStale`'s behavior (crash-orphaned jobs stay `failed`).

## Implementation Notes

1. `phraseforge/internal/db/schema.sql`: added `cancelled` to the inline
   `status` CHECK list, plus an idempotent `DROP CONSTRAINT IF EXISTS
   jobs_status_check` / `ADD CONSTRAINT` pair for existing installs —
   verified against the live k3d Postgres that the constraint's actual
   auto-generated name really is `jobs_status_check` before writing the
   migration.
2. `phraseforge/internal/jobs/jobs.go`: added `StatusCancelled`; a
   `runningJob` struct (id, `context.CancelFunc`, an explicit `cancelled`
   flag) tracked via `Service.running`/`runMu`/`cancelRequested`;
   `runOne` now derives a per-job `context.WithCancel(ctx)`, stores/clears
   the running slot around the handler call (self-cancelling immediately
   if the claim-window race left a pending request for this id), and
   passes the resolved `cancelled` flag (never the raw error) into
   `complete`, which writes `StatusCancelled` instead of `StatusFailed`
   only when that flag is set. Added `Cancel(ctx, id) error`: a
   `pending`-guarded UPDATE first, then the in-memory running slot, with
   the pending-cancel-id set closing the narrow window between claim's
   `UPDATE` and the slot being stored. `Retry`/`Delete`/
   `EnqueueAndAwait` all extended to also accept/treat `cancelled` like
   `failed`.
3. `phraseforge/internal/server/jobs.go`, `server.go`: `apiCancelAdminJob`
   (mirrors `apiDeleteAdminJob`'s shape) and its route, inside the
   existing `requireAdmin` group.
4. `phraseforge/internal/i18n/i18n.go`: `jobs.cancel`/`jobs.cancel_confirm`
   (en/pl), added to `jobsAppI18nKeys`.
5. `jobs-app.js`: table actions now render per-status via
   `jobActionCells` — one fixed column for Retry (only failed/cancelled),
   one shared column for Cancel-or-Delete (Cancel for pending/running,
   Delete for done/failed/cancelled) — rather than always rendering every
   button and toggling `disabled`. Cancel gets its own confirm step,
   matching Retry/Delete's existing pattern.
6. Self-reviewed the diff with the `code-review` skill: found and fixed
   one stale comment (a leftover reference to an earlier, renamed helper
   name, `retryOrCancelDeleteHtml` → `jobActionCells`); otherwise no
   findings.
7. Appended three `specs/memory.md` entries: the explicit-flag attribution
   pattern, the claim-vs-cancel race-closing pattern, and confirmation
   that `shared/llm`'s `http.NewRequestWithContext` makes cancellation
   genuinely abort an in-flight call.
8. **Fix-forward rounds** (feedback during review, before this spec's own
   validation gate):
   - UI: first pass always rendered Cancel/Retry/Delete and toggled
     `disabled` — read as clutter (a permanently-disabled Cancel sitting
     on a done/failed row). Fixed by conditionally rendering only the
     actions that apply to a row's status.
   - UI: that fix still let Delete's column position shift depending on
     whether Retry was also present on the row. Fixed again per
     "keep the same column for Delete/Cancel, they are mutually
     exclusive": restructured into three fixed columns (View, Retry,
     Cancel-or-Delete) so Cancel and Delete always land in the exact same
     column — verified directly (not just eyeballed) via a jsdom harness
     that executes the real `jobs-app.js` against a fake job list and
     inspects the rendered `<td>` structure per status.
   - **Concurrency (found by an adversarial `reviewer` pass, "Request
     changes" — see B3 below): two confirmed bugs in the first
     implementation, both "Cancel returns success while nothing is
     actually cancelled":**
     - The running slot was cleared by a `defer` that only ran *after*
       `complete`'s own DB write (which can take up to several seconds,
       bounded by its own timeout). A `Cancel` landing in that window
       would still match the slot, flip the (already-consumed) `cancelled`
       flag, and report success — while `complete` had already written (or
       was about to write) the job's real `done`/`failed` outcome. Fixed
       by adding an explicit `finished` bool to `runningJob`, set (in the
       same critical section that reads `cancelled`) the instant the
       handler returns, *before* `complete` is called — `cancelRunning`
       now returns a tri-state (`cancelDone`/`cancelTooLate`/
       `cancelNoSlot`) so a too-late `Cancel` gets an honest error instead
       of a false 204.
     - The claim-window fix itself had a gap: the second slot re-check and
       the `cancelRequested` insert were two separate lock acquisitions,
       leaving a window where `runOne`'s own publish-and-consume step
       could run entirely in between — recording a request nobody would
       ever consume, again reporting false success. Fixed by merging the
       re-check and the insert into one atomic critical section. Also
       moved slot publishing (and cancel-request consumption) to happen
       unconditionally right after a successful claim, even when no
       handler turns out to be registered for the kind — the original
       code only published a slot for the handler-registered path, so an
       unregistered-kind job (rare, but possible) could leave the same
       kind of unconsumable entry behind.
     - Minor: made `Cancel` idempotent for an already-`cancelled` job
       (a double-click on a job cancelled while still `pending` previously
       returned a 400 for an operation that had already succeeded).
     - Minor: `cancelRequested` gained a timestamp per entry and a
       per-tick prune in `Run`'s loop (5s TTL), bounding the one residual,
       genuinely rare case the fixes above don't eliminate: a claimed row
       whose *own completion write* then fails or times out (already an
       accepted, pre-existing limitation recovered only by a restart's
       `FailStale` sweep, same as before this feature existed) can still
       leave one unconsumable entry — now it expires instead of persisting
       for the process's lifetime.

## Validation

- `go build ./...`, `go vet ./...`, `go test ./... -count=1` all pass in
  `phraseforge` — no regressions.
- `npm test` in `phraseforge/internal/server/static/components`: 31/31
  pass, unchanged (this feature touches no dialog component).
- `node --check` on `jobs-app.js` — no syntax errors.
- A jsdom harness executing the real `jobs-app.js` confirmed the rendered
  table has 8 columns and the exact expected action set per status:
  `pending`/`running` → View + Cancel (Retry column empty);
  `failed`/`cancelled` → View + Retry + Delete; `done` → View + Delete
  (Retry column empty, Delete in the same column Cancel uses).
- **Real-cluster verification** (this repo has no DB-backed Go test
  convention to extend — confirmed by grep, the one existing reference to
  this idea is a comment explicitly choosing *not* to mock `pgxpool.Pool`
  — so this feature followed the spec's own documented fallback):
  deployed to the local k3d cluster; verified against the live DB that
  the migration applied (`jobs_status_check` now includes `cancelled`);
  enqueued two real `generate_vocab_from_text` jobs back-to-back on the
  one available text so the second stayed `pending` while the first was
  `running`; cancelled the `pending` one (zero LLM cost) — confirmed
  `status='cancelled'`, `error='cancelled by admin'` instantly; cancelled
  the `running` one (the feature's 1 real-call budget) after it had
  already been running ~23s — confirmed it reached `cancelled` within
  ~1s of the request (pod log: `duration_ms=29640 outcome=error`, far
  short of the 2-minute client timeout), proving the in-flight call was
  genuinely aborted, not waited out. Verified `Retry` and `Delete` both
  accept a `cancelled` job by exercising them against these same two rows.
- **`reviewer` pass** (this change is architecturally significant —
  new mutex-guarded state and race handling — so B3 invoked one beyond
  the engineer's own self-review): an adversarial concurrency review of
  `jobs.go` found two confirmed High-severity bugs (both "Cancel reports
  success while nothing is actually cancelled" — see Implementation
  Notes' fix-forward round) plus three lower-severity issues (a
  permanent-leak edge case, a wedged-row false-success edge case treated
  as an accepted follow-up matching `FailStale`'s own existing
  restart-recovery story, and a double-cancel idempotency gap). The two
  High findings and the idempotency gap were fixed; the leak was bounded
  with a TTL/prune; the wedged-row case is recorded as an accepted,
  pre-existing-class limitation, not solved here. The review also
  explicitly verified (and I re-verified by reading the final code)
  that: no field is read/written outside `runMu`; no DB call or handler
  call ever happens while `runMu` is held; a stale slot/`cancelRequested`
  entry can never cancel the *wrong* (later) job, since ids are fresh
  UUIDs and `Retry` mints a new one rather than resubmitting a row; and
  ordinary worker shutdown can never be misreported as `cancelled`.
- **Post-fix live re-verification** (the feature's 2nd and final real LLM
  call, still aborted early rather than run to completion): enqueued a
  fresh `generate_vocab_from_text` job, confirmed `running`, cancelled it
  after ~19s — reached `cancelled` within ~1s (pod log:
  `duration_ms=19403 outcome=error`) — then immediately cancelled the
  same now-`cancelled` job again and confirmed 204 (idempotent, per the
  F5 fix) instead of the previous 400. Both confirm the reworked
  concurrency logic still produces the correct end-to-end behavior after
  the fixes, not just that it compiles.
- `go test ./... -race` (in addition to the plain run above) — passes,
  though this package has no test file so `-race` doesn't itself exercise
  the new concurrency logic; correctness there rests on the manual
  critical-section audit above and the architect/reviewer passes, per
  this repo's established no-DB-mock convention (see B2's testing-strategy
  note) rather than a from-scratch test harness for this one feature.

## Documentation Review

Affected Areas map to the `phraseforge` artifact only — no fan-out.

**Changelog entry needed** (new user-facing admin capability, category
`Added` — matches the existing `### Added` section's style, including the
prior "Jobs admin page..." entry this one extends):

- `{phraseforge/CHANGELOG.md, Added, "Cancel action on the Jobs page for pending/running jobs — interrupts an in-flight LLM call rather than only being able to wait it out or restart the pod; Retry and Delete now also work on a cancelled job."}`

**Other drift:** none found. No constitution file makes a claim this
change contradicts.

## Documentation Updates

- `phraseforge/CHANGELOG.md`: added the `### Added` entry above.
- Removed this feature's `## Now` line from
  `specs/artifacts/phraseforge/roadmap.md` — it's in the changelog now.
