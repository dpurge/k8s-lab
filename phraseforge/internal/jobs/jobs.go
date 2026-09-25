// Package jobs provides phraseforge's single app-wide job queue: at most one
// job runs at a time, and an interactive-priority job always jumps ahead of
// any pending background-priority one — see
// specs/features/phraseforge-job-queue.md for the full design. Every
// current caller enqueues at background priority (see
// background-generate-title-transcription-translation, which retired the
// last interactive-priority caller, the old synchronous /llm/generate
// path); PriorityInteractive is kept only because historical job rows carry
// that value and the Jobs page still needs to render it.
//
// This is a single unified table, ported from knowledge's internal/queue
// package (knowledge/internal/queue/queue.go) but deliberately not split
// into knowledge's separate jobs (ingest-specific tracker) + operations
// (general LLM queue) tables: a future Jobs menu is meant to show every
// submitted job, interactive and background alike, in one place.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Priority string

const (
	PriorityInteractive Priority = "interactive"
	PriorityBackground  Priority = "background"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// pollInterval bounds how long the worker waits before re-checking for work
// when nothing woke it early via Enqueue's nudge — matches knowledge's
// internal/queue: this queue is single-worker and low-throughput by design,
// so a short fixed poll costs nothing real while keeping the implementation
// simple (no LISTEN/NOTIFY).
const pollInterval = 500 * time.Millisecond

type Job struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Priority  Priority        `json:"priority"`
	Status    Status          `json:"status"`
	Payload   json.RawMessage `json:"payload"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	Step      string          `json:"step,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// HandlerFunc performs one job's actual work. It must not be called directly
// by any package other than jobs itself — kinds are dispatched by the worker
// loop via the registration in Register. id is this job's own row id —
// passed through so a multi-step handler (see phraseforge/internal/ingest)
// can read/record its own progress via Get/SetStep, making a Retry after a
// partial failure safe to re-run instead of repeating already-completed
// side effects.
type HandlerFunc func(ctx context.Context, id string, payload json.RawMessage) (result json.RawMessage, err error)

// runningJob tracks the one job the single worker is currently executing —
// jobs_one_running_idx (db/schema.sql) plus the single Run goroutine
// guarantee there is never more than one, so a single pointer (not a map
// keyed by id) is enough. cancelled is set by Cancel before it calls the
// stored CancelFunc, and is what lets complete tell "this failed because an
// admin cancelled it" apart from any other reason the handler returned an
// error (including an ordinary worker-shutdown context cancellation, which
// must NOT be mistaken for an admin cancel) — inspecting the returned error
// alone (e.g. errors.Is(err, context.Canceled)) cannot make that
// distinction reliably, since shutdown produces the exact same sentinel.
//
// finished is set (under runMu, in the same critical section that reads
// cancelled for complete's benefit) the instant the handler returns — before
// complete's own DB round-trip, which can take up to several seconds (its
// own bounded timeout). Without it, a Cancel landing in that window would
// find a slot that still matches this id, "successfully" flip cancelled and
// invoke the now-inert CancelFunc, and report success even though the job
// already finished and complete is busy writing its real done/failed
// outcome — silently lying to the caller. cancelRunning checks finished and
// reports that case as "too late" instead of a false success.
type runningJob struct {
	id        string
	cancel    context.CancelFunc
	cancelled bool
	finished  bool
}

// cancelOutcome is cancelRunning's tri-state result — plain bool can't
// distinguish "no matching slot at all" (fall through to the claim-window
// path) from "matched, but the handler already returned" (a definite
// too-late error, never a false success).
type cancelOutcome int

const (
	cancelNoSlot cancelOutcome = iota
	cancelDone
	cancelTooLate
)

// Service owns the jobs table and the single worker goroutine that drains
// it. Handler registration follows knowledge's convention: jobs never
// imports ai (or any future job-kind package), and main.go registers each
// service's handler method here after constructing everything.
type Service struct {
	db *pgxpool.Pool

	mu       sync.RWMutex
	handlers map[string]HandlerFunc

	wake chan struct{}

	// runMu guards running and cancelRequested. Every critical section
	// under it must be pure field access — never a DB call, never a call
	// into a job handler — so it can't become a bottleneck or deadlock risk
	// (calling a stored CancelFunc is fine: it never re-enters this
	// package).
	runMu sync.Mutex
	// running is the currently-executing job, or nil between claims.
	running *runningJob
	// cancelRequested closes the narrow race window between claim's UPDATE
	// committing status='running' and runOne storing running: an id lands
	// here only when Cancel observed the row as already 'running' in the
	// DB but found no matching running slot yet. runOne consumes (and
	// deletes) its own id from this set in the same critical section where
	// it stores its slot, self-cancelling immediately if present.
	//
	// The window this is meant to cover is sub-millisecond, but an id can
	// end up here with nothing left to ever consume it — e.g. Cancel racing
	// a row whose claim succeeded but whose own completion write then
	// failed/timed out (left stuck at 'running' until the next restart's
	// FailStale, same as if Cancel had never been called). The value is
	// each entry's insertion time so Run's loop can prune anything older
	// than a few poll intervals, rather than let a handful of these
	// genuinely rare cases accumulate for the process's entire lifetime.
	cancelRequested map[string]time.Time
}

func New(db *pgxpool.Pool) *Service {
	return &Service{
		db:              db,
		handlers:        map[string]HandlerFunc{},
		wake:            make(chan struct{}, 1),
		cancelRequested: map[string]time.Time{},
	}
}

// Register associates a handler with a job kind. Must be called before Run
// starts processing jobs of that kind; calling it again for the same kind
// replaces the previous handler.
func (s *Service) Register(kind string, fn HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[kind] = fn
}

func (s *Service) handler(kind string) (HandlerFunc, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn, ok := s.handlers[kind]
	return fn, ok
}

func uuid() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// Enqueue records a new pending job and wakes the worker so it need not wait
// for the next poll tick. It returns as soon as the row is written — the
// caller never waits for the job itself here; every caller in this codebase
// reads the result later (the Jobs page, or a resource's own view once
// reopened), matching the background-job UX every "Generate" action uses.
func (s *Service) Enqueue(ctx context.Context, kind string, priority Priority, payload json.RawMessage) (string, error) {
	return s.enqueue(ctx, kind, priority, payload, "")
}

// enqueue is Enqueue's real body, plus an initial step value — only Retry
// (below) ever passes a non-empty one, to carry a failed job's recorded
// progress forward onto the new job it creates.
func (s *Service) enqueue(ctx context.Context, kind string, priority Priority, payload json.RawMessage, step string) (string, error) {
	if payload == nil {
		payload = json.RawMessage("{}")
	}
	id, err := uuid()
	if err != nil {
		return "", err
	}
	var stepArg any
	if step != "" {
		stepArg = step
	}
	_, err = s.db.Exec(ctx, "INSERT INTO jobs(id,kind,priority,status,payload,step) VALUES($1,$2,$3,'pending',$4,$5)", id, kind, priority, payload, stepArg)
	if err != nil {
		return "", err
	}
	s.nudge()
	return id, nil
}

// SetStep overwrites only a job's step marker — a narrow setter matching
// texts.Store.SetTitle's out-of-order-safety pattern, used by a multi-step
// job handler (see phraseforge/internal/ingest) to record how far it got, so
// a Retry after a partial failure (see Retry's own doc comment) can skip
// already-completed side effects instead of repeating them.
func (s *Service) SetStep(ctx context.Context, id, step string) error {
	_, err := s.db.Exec(ctx, "UPDATE jobs SET step=$1, updated_at=now() WHERE id=$2", step, id)
	return err
}

func (s *Service) nudge() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// jobColumns is the column list every full-row Job query scans, factored out
// so Get/List/claim's RETURNING stay in sync by construction.
const jobColumns = "id,kind,priority,status,payload,coalesce(result,'null'),coalesce(error,''),coalesce(step,''),created_at,updated_at"

type scanner interface {
	Scan(dest ...any) error
}

func scanJob(row scanner) (Job, error) {
	var j Job
	var payload, result []byte
	err := row.Scan(&j.ID, &j.Kind, &j.Priority, &j.Status, &payload, &result, &j.Error, &j.Step, &j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		return Job{}, err
	}
	j.Payload = json.RawMessage(payload)
	j.Result = json.RawMessage(result)
	return j, nil
}

// Get returns one job by id.
func (s *Service) Get(ctx context.Context, id string) (Job, error) {
	row := s.db.QueryRow(ctx, "SELECT "+jobColumns+" FROM jobs WHERE id=$1", id)
	return scanJob(row)
}

// List returns the most recent 500 jobs of any status, most recent first —
// matches knowledge's internal/jobs.List cap.
func (s *Service) List(ctx context.Context) ([]Job, error) {
	rows, err := s.db.Query(ctx, "SELECT "+jobColumns+" FROM jobs ORDER BY created_at DESC LIMIT 500")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Retry re-reads a failed or cancelled job's original kind/payload and
// enqueues a new job with them, returning the new job's id. It does not
// modify or resubmit the original row.
//
// It also carries the original job's own Step value forward onto the new job
// (rather than starting it blank): a multi-step handler that recorded
// partial progress via SetStep before failing (e.g. phraseforge/internal/
// ingest's handleProcess, after its row is created but before its follow-up
// jobs are enqueued) relies on that marker being visible to the job that
// actually re-runs the work — which, since Retry mints a brand-new job id
// rather than resubmitting this same row, would otherwise be a fresh job
// with no memory of it, defeating the whole point of recording it.
func (s *Service) Retry(ctx context.Context, id string) (string, error) {
	j, err := s.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if j.Status != StatusFailed && j.Status != StatusCancelled {
		return "", fmt.Errorf("jobs: cannot retry job %s: status is %q, not %q or %q", id, j.Status, StatusFailed, StatusCancelled)
	}
	return s.enqueue(ctx, j.Kind, j.Priority, j.Payload, j.Step)
}

// Delete removes a done/failed/cancelled job's row. It refuses to delete a
// pending/running job — a pending job may still be waiting for its turn, and
// a running one may be actively processed by the single worker — deleting
// either out from under them isn't safe. Not-found and not-terminal both
// surface as the same error: the caller (the Jobs page) only ever offers
// Delete on rows it already knows are terminal, so distinguishing the two
// cases has no UI use here.
func (s *Service) Delete(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, "DELETE FROM jobs WHERE id=$1 AND status IN ('done','failed','cancelled')", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("jobs: cannot delete job %s: not found or not in a terminal state", id)
	}
	return nil
}

// DeleteAllDone bulk-removes every job row currently at status='done' —
// deliberately narrower than Delete's own three terminal statuses: a
// pending/running job is still doing something, and a failed/cancelled row
// is left for the operator to inspect or Retry, so only 'done' is safe to
// clear in bulk without an operator reviewing each row individually first
// (see the Jobs page's Clear button). Returns the number of rows removed.
func (s *Service) DeleteAllDone(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, "DELETE FROM jobs WHERE status = 'done'")
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Cancel stops a pending or running job. A pending job is cancelled purely
// in the DB — the worker's claim query only ever selects 'pending' rows, so
// it never sees this one. A running job is cancelled by invoking the
// CancelFunc runOne stores for the single currently-executing job, which
// (since shared/llm builds every outbound request via
// http.NewRequestWithContext) genuinely aborts an in-flight LLM call rather
// than merely marking the row.
//
// Not-found, already-terminal, and "raced with completion" all surface as
// the same error, mirroring Delete's own reasoning: the Jobs page only ever
// offers Cancel on rows it already knows are pending/running, so
// distinguishing the cases further has no UI use here — except
// cancelTooLate (see cancelRunning), which must never be reported as
// success: the job already finished and complete is (or already has)
// recorded its real outcome, so a nil return here would tell the caller
// "cancelled" about a job that is actually done/failed.
func (s *Service) Cancel(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, "UPDATE jobs SET status='cancelled', error='cancelled by admin', updated_at=now() WHERE id=$1 AND status='pending'", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	switch s.cancelRunning(id) {
	case cancelDone:
		return nil
	case cancelTooLate:
		return fmt.Errorf("jobs: cannot cancel job %s: already finished", id)
	}

	// cancelNoSlot: neither the pending path nor an already-stored running
	// slot matched. This id might be caught in the narrow window between
	// claim's UPDATE committing status='running' and runOne storing its
	// slot — re-check the DB status (a plain read, no lock held) first.
	j, getErr := s.Get(ctx, id)
	if getErr != nil {
		return fmt.Errorf("jobs: cannot cancel job %s: not found or not pending/running", id)
	}
	// Cancel is idempotent for a job that's already cancelled — a
	// double-click (or two admins acting on the same row) on a job that was
	// cancelled while still pending has already achieved the caller's goal,
	// and reporting that as an error would be misleading.
	if j.Status == StatusCancelled {
		return nil
	}
	if j.Status != StatusRunning {
		return fmt.Errorf("jobs: cannot cancel job %s: not found or not pending/running", id)
	}
	// Re-check the slot and, if it's still not there, record the request —
	// both under the SAME lock acquisition as one indivisible step. Doing
	// this as two separate locked sections (check, then a later separate
	// lock to insert) leaves a window where runOne's own claim-and-publish
	// critical section (see runOne) can run entirely in between: it would
	// find cancelRequested still empty, publish its slot without
	// self-cancelling, and this insert would then arrive too late for
	// anyone to ever consume it — a lost cancel that still reports success.
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.running != nil && s.running.id == id {
		if s.running.finished {
			return fmt.Errorf("jobs: cannot cancel job %s: already finished", id)
		}
		s.running.cancelled = true
		s.running.cancel()
		return nil
	}
	s.cancelRequested[id] = time.Now()
	return nil
}

// cancelRunning reports whether id matches the currently-running slot, and
// if so, whether it was still live (cancelDone: cancelled) or had already
// finished by the time this call ran (cancelTooLate — see runningJob's own
// doc comment on why this can't be conflated with success). Never touches
// the DB while holding runMu.
func (s *Service) cancelRunning(id string) cancelOutcome {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.running == nil || s.running.id != id {
		return cancelNoSlot
	}
	if s.running.finished {
		return cancelTooLate
	}
	s.running.cancelled = true
	s.running.cancel()
	return cancelDone
}

// cancelRequestTTL bounds how long an entry may sit in cancelRequested
// before Run's loop prunes it — generously long relative to the
// sub-millisecond window it's meant to cover (claim's UPDATE committing vs.
// runOne publishing its slot), so this only ever removes an entry from the
// genuinely rare case where nothing was ever going to consume it (see
// cancelRequested's own doc comment).
const cancelRequestTTL = 5 * time.Second

// pruneCancelRequested removes any cancelRequested entry older than
// cancelRequestTTL. Called once per Run loop iteration — pure field access
// under runMu, like every other access to this map.
func (s *Service) pruneCancelRequested() {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	for id, requestedAt := range s.cancelRequested {
		if time.Since(requestedAt) > cancelRequestTTL {
			delete(s.cancelRequested, id)
		}
	}
}

// FailStale marks any still-"running" job as failed. A process restart
// (crash, rolling update, node reschedule) leaves a claimed job's row stuck
// at status='running' forever, since nothing else ever calls complete for
// it — left alone, jobs_one_running_idx (a single global slot) would then
// block every future claim permanently, jamming the whole queue. Call once
// at startup, before Run starts claiming new work.
func (s *Service) FailStale(ctx context.Context) error {
	_, err := s.db.Exec(ctx, "UPDATE jobs SET status='failed', error='interrupted by restart', updated_at=now() WHERE status='running'")
	return err
}

// Run is the worker loop: it must be started exactly once, in its own
// goroutine, with a context that lives for the server's whole lifetime. It
// never returns until ctx is done.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		for s.runOne(ctx) {
			// Drain everything currently pending before waiting on the next
			// tick or nudge — re-checking priority after every completed
			// job is what lets a newly-arrived interactive job jump ahead
			// of remaining background ones immediately.
			if ctx.Err() != nil {
				return
			}
		}
		s.pruneCancelRequested()
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
	}
}

// runOne claims and runs at most one pending job, preferring the oldest
// pending interactive-priority row over any background-priority row. It
// reports whether it did work, so Run's caller can keep draining without
// waiting for the next tick.
func (s *Service) runOne(ctx context.Context) bool {
	j, ok, err := s.claim(ctx)
	if err != nil {
		log.Printf("jobs: claim job: %v", err)
		return false
	}
	if !ok {
		return false
	}

	// jobCtx (not the bare worker ctx) is what a Cancel call actually
	// interrupts — see Cancel/cancelRunning. The slot is published (and any
	// cancel request already recorded for this id is consumed) in one
	// atomic step, unconditionally — even when no handler turns out to be
	// registered below — so Cancel can never observe a state where the
	// publish and the consume have happened separately, and a no-handler
	// job can't leave an unconsumable cancelRequested entry behind either
	// (both were real bugs in an earlier version of this code; see
	// specs/memory.md and this feature's own spec for the history).
	jobCtx, cancelJob := context.WithCancel(ctx)
	defer cancelJob()
	rj := &runningJob{id: j.ID, cancel: cancelJob}
	s.runMu.Lock()
	if _, requested := s.cancelRequested[j.ID]; requested {
		delete(s.cancelRequested, j.ID)
		rj.cancelled = true
		cancelJob()
	}
	s.running = rj
	s.runMu.Unlock()
	defer func() {
		s.runMu.Lock()
		s.running = nil
		delete(s.cancelRequested, j.ID)
		s.runMu.Unlock()
	}()

	fn, registered := s.handler(j.Kind)
	if !registered {
		s.runMu.Lock()
		rj.finished = true
		cancelled := rj.cancelled
		s.runMu.Unlock()
		s.complete(context.Background(), j.ID, nil, fmt.Errorf("jobs: no handler registered for kind %q", j.Kind), cancelled)
		return true
	}

	start := time.Now()
	result, runErr := fn(jobCtx, j.ID, j.Payload)
	outcome := "success"
	if runErr != nil {
		outcome = "error"
	}
	slog.Info("job completed", "kind", j.Kind, "priority", string(j.Priority), "duration_ms", time.Since(start).Milliseconds(), "outcome", outcome)

	// finished is set in the same critical section as reading cancelled,
	// immediately after the handler returns and before complete's own DB
	// round-trip (which can take up to several seconds) — see runningJob's
	// doc comment on why a Cancel landing during that round-trip must see
	// "too late", never a false success.
	s.runMu.Lock()
	rj.finished = true
	cancelled := rj.cancelled
	s.runMu.Unlock()
	s.complete(context.Background(), j.ID, result, runErr, cancelled)
	return true
}

// claim atomically picks the oldest pending interactive-priority job, or
// (only if none is pending) the oldest pending background-priority one, and
// marks it running in a single statement — the standard Postgres
// claim-a-job pattern, safe even without an explicit transaction since one
// UPDATE statement is already atomic. jobs_one_running_idx (see
// db/schema.sql) is the DB-level backstop; with exactly one worker goroutine
// calling claim, that backstop is never actually contended.
func (s *Service) claim(ctx context.Context) (Job, bool, error) {
	row := s.db.QueryRow(ctx, `
		UPDATE jobs SET status = 'running', updated_at = now()
		WHERE id = (
			SELECT id FROM jobs
			WHERE status = 'pending'
			ORDER BY (priority = 'interactive') DESC, created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+jobColumns)
	j, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	return j, true, nil
}

// complete records a job's terminal state using ctx.Background() rather than
// the job's own (possibly worker-shutdown or, now, admin-cancelled)
// context, so a completion is never lost to a context that expired at the
// wrong moment. A short timeout bounds that Background() context so a hung
// DB connection can't block the single worker goroutine forever; if it
// fires, this job's row is left at status='running' and is recovered by the
// next FailStale at the next restart, rather than wedging the whole queue.
//
// cancelled comes from runOne's own tracking (rj.cancelled, set only by
// Cancel), never from inspecting jobErr — a handler returning
// context.Canceled during ordinary worker shutdown must land on
// StatusFailed like any other interrupted run, not StatusCancelled.
func (s *Service) complete(ctx context.Context, id string, result json.RawMessage, jobErr error, cancelled bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	status := StatusDone
	var errText *string
	switch {
	case cancelled:
		status = StatusCancelled
		text := "cancelled by admin"
		errText = &text
	case jobErr != nil:
		status = StatusFailed
		text := jobErr.Error()
		errText = &text
	}
	if result == nil {
		result = json.RawMessage("null")
	}
	// AND status='running' makes this a no-op rather than a clobber if
	// something else (e.g. a concurrent FailStale from another pod during a
	// bad rollout) already moved the row out of 'running'.
	tag, err := s.db.Exec(ctx, "UPDATE jobs SET status=$1, result=$2, error=$3, updated_at=now() WHERE id=$4 AND status='running'", status, result, errText, id)
	if err != nil {
		log.Printf("jobs: failed to record completion for job %s: %v", id, err)
		return
	}
	if tag.RowsAffected() == 0 {
		slog.Info("jobs: completion skipped, job no longer running", "id", id)
	}
}
