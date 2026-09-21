---
title: Async job tracking and a persistent status line
kind: feature
status: done
version: 1
updated: 2026-09-21
branch: main
---

## Problem / Motivation

Third of six specs discussed for knowledge ingestion (see `specs/roadmap.md`).
Two related but distinct problems, both about the UI not showing what the
app is doing:

1. The "Generate" title/summary buttons block on a single HTTP request with
   zero feedback — the user reported this "felt broken." That's a
   client-side-only fix: show a short message while the request is in
   flight. No server change needed for this half.
2. The upcoming ingest pipeline (`knowledge-ingest-pipeline`, still ahead
   in `## Next`) will run a genuinely long, multi-step, server-side
   operation (fetch → extract → chunk → translate → generate → store).
   There's currently no mechanism for the server to report progress on
   something like that, or for the UI to show it. This spec builds that
   mechanism — a generic job tracker and a persistent status line, visible
   regardless of active tab — with no real producer yet; ingestion is the
   first real caller, same relationship `knowledge-translate-capability`
   has to the ingest pipeline.

## Acceptance Criteria

- [x] A new `jobs` Postgres table (`id`, `kind`, `status` — `running`/
      `done`/`failed` — `step`, `error`, `created_at`, `updated_at`) and a
      `knowledge/internal/jobs` package (`Service`, mirroring `auth`'s
      shape): `Start(ctx, kind) (Job, error)`, `SetStep(ctx, id, step)
      error`, `Finish(ctx, id, err error) error` (nil → `done`, non-nil →
      `failed` with the error text recorded), `Current(ctx) (*Job, error)`
      (the most recently updated `running` job, or `nil`).
- [x] `GET /api/v1/jobs/current` (authenticated, like every other route)
      returns the current job as JSON (200) or no content (204) — the
      existing `api()` JS helper already treats 204 as `null` cleanly.
- [x] `index.html`'s `<header>` (already outside any tab, so already
      visible everywhere) gains a status-line element, polled every few
      seconds from page load, showing `"<kind>: <step>"` when a job is
      running and nothing otherwise. Polling failures are swallowed
      silently — this is a nice-to-have indicator, not something that
      should ever interrupt the user with an error.
- [x] `generateTitle()`/`generateSummary()` show a brief "Generating…"
      message (the existing per-action `#msg` line, not the new status
      line — kept deliberately separate, see Approach) while their
      request is in flight.
- [x] No wiring of the job tracker into any real operation yet — this spec
      produces the mechanism only, validated with a synthetic job (created
      directly against Postgres, not through the app, since nothing in
      this codebase creates real jobs yet).
- [x] `go build`/`vet`/`gofmt`/`test` clean; the full read+write path
      (`Start`→`SetStep`→`Finish`→`Current`) exercised live against the
      real deployed Postgres, and `GET /jobs/current` checked against the
      live app both while a job is genuinely running and after it finishes.

## Approach

1. **`knowledge/internal/db/db.go`**: add the `jobs` table to the schema
   string (idempotent `CREATE TABLE IF NOT EXISTS`, matching every other
   table there).
2. **`knowledge/internal/jobs/jobs.go`** (new package): `Job` struct
   (`ID`, `Kind`, `Status`, `Step`, `Error`, `CreatedAt`, `UpdatedAt`);
   `Service{db *pgxpool.Pool}`; `New(db) *Service`; the four methods from
   Acceptance Criteria, using the same `uuid()`-generation idiom already
   duplicated per-package in this codebase (`auth`, `chat`) rather than
   introducing a shared helper — matches existing convention, not a new
   abstraction.
3. **`knowledge/internal/server/server.go`**: `jobs *jobs.Service` field/
   constructor param; `GET /jobs/current` under the existing authenticated
   `/api/v1` group (not nested under `/knowledge` — jobs aren't
   knowledge-item specific). Handler calls `s.jobs.Current`, writes
   200+JSON or 204.
4. **`knowledge/main.go`**: construct `jobsSvc := jobs.New(pool)`, pass
   into `server.New`.
5. **`knowledge/internal/server/static/index.html`**:
   - Add `<span class="muted" id="statusLine"></span>` to `<header>`.
   - New `pollStatus()`: `GET /jobs/current`; on a job, set
     `statusLine.textContent` to `"${kind}: ${step || "running"}"`; on no
     job (or any error), clear it. Wrapped so a failed poll never shows an
     error to the user. Started via `setInterval(pollStatus, 3000)` plus
     one immediate call, alongside the existing bootstrap calls at the
     bottom of the script.
   - `generateTitle()`/`generateSummary()`: set `message("msg",
     "Generating title…"/"Generating summary…")` right after the
     empty-body guard, before the `await api(...)` call.
6. **Deliberately two separate mechanisms, not one**, even though the
   roadmap item bundles them: the status line is server-polled (for
   genuinely long, multi-step server operations); Generate's feedback is
   client-only and immediate (a single blocking fetch has no "steps" to
   report). Routing Generate's feedback through the polled status line
   too would risk the 3-second poll overwriting/clearing the "Generating…"
   text mid-flight — a real, avoidable race, not a hypothetical one, since
   Generate's own request can complete in a similar timeframe to one poll
   interval.

## Affected Areas

- `knowledge/internal/db/db.go`
- `knowledge/internal/jobs/jobs.go` (new)
- `knowledge/internal/server/server.go`
- `knowledge/main.go`
- `knowledge/internal/server/static/index.html`

## Out of Scope

- Wiring the job tracker into any real operation — `knowledge-ingest-
  pipeline` is the first real caller.
- Multiple concurrent jobs / a job history or list — `Current()` returns
  only the single most-recently-updated running job, matching the
  singular "what is the app doing right now" framing of the request; no
  evidence today of a need for concurrent job tracking.
- Any change to how errors are displayed elsewhere in the UI.
- WebSockets/SSE for push-based updates — plain polling is simple and
  sufficient at this scale.

## Implementation Notes

Implemented exactly per Approach:

- `knowledge/internal/db/db.go`: added the `jobs` table.
- `knowledge/internal/jobs/jobs.go` (new): `Job`/`Service`/`New`/`Start`/
  `SetStep`/`Finish`/`Current`, using the same per-package `uuid()` idiom
  already duplicated in `auth`/`chat`, and the same `errors.Is(err,
  pgx.ErrNoRows)` convention `auth` already uses for "not found."
- `knowledge/internal/server/server.go`: `jobs` field/constructor param;
  `GET /jobs/current` → `s.currentJob` (200+JSON or 204).
- `knowledge/main.go`: constructs `jobs.New(pool)`, passes it through.
- `knowledge/internal/server/static/index.html`: `#statusLine` added to
  `<header>`; `pollStatus()` polls it every 3s from bootstrap, swallowing
  errors; `generateTitle()`/`generateSummary()` now show/clear a
  "Generating…" message on the existing `#msg` line.

## Validation

`go build`/`vet`/`gofmt`/`test` clean across all modules. Deployed via
`task deploy-knowledge`; migration completed without SQL errors,
confirming the new table's syntax.

Live validation, in order:
1. `GET /jobs/current` with no jobs in the table → `204`, correct.
2. Inserted a synthetic `running` job directly via `psql` (`kubectl exec`
   into the postgres pod) → `GET /jobs/current` returned it correctly as
   `200` + the exact JSON shape designed.
3. Updated that row's `step` via `psql` → next poll reflected the new
   step, confirming step updates surface correctly.
4. Marked it `done` via `psql` → `GET /jobs/current` returned `204` again.
5. Separately, port-forwarded the live Postgres and ran a throwaway,
   uncommitted test exercising the *real* Go methods end to end
   (`Start`→`SetStep`→`Current`→`Finish`(with a simulated error)→
   `Current`) — all passed, confirming the write path (not just the read
   path validated via psql above) is correct. Deleted afterward — a
   live-Postgres-dependent test would break CI, which has neither a
   reachable Postgres nor Ollama.
6. Re-verified the exact `GET /jobs/current` command as it appears in the
   README (literal `-b cookies.txt`, not a shell variable) before
   publishing it, per the verify-before-publish rule.

## Documentation Review

Checked `knowledge/README.md`. Found and fixed: the architecture tree was
missing `internal/jobs/jobs.go` — and, a pre-existing gap from
`knowledge-translate-capability` that spec's own review missed,
`internal/translate/translate.go` too. Added a "### Job status" API
reference subsection matching the existing per-endpoint pattern. No
constitution-file drift.

## Documentation Updates

`knowledge/README.md`: architecture tree (both missing entries), new
"### Job status" section with a verified-live example. `CHANGELOG.md`:
`## [Unreleased]` entries for both the new job-tracking/status-line
mechanism and the Generate-buttons feedback fix. `specs/roadmap.md`:
removed this feature's `## Now` line.
