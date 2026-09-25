// Package jobs provides phraseforge's single app-wide job queue: at most one
// job runs at a time, and an interactive-priority job (a synchronous
// /llm/generate call) always jumps ahead of any pending background-priority
// one — see specs/features/phraseforge-job-queue.md for the full design.
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
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
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

// Service owns the jobs table and the single worker goroutine that drains
// it. Handler registration follows knowledge's convention: jobs never
// imports ai (or any future job-kind package), and main.go registers each
// service's handler method here after constructing everything.
type Service struct {
	db *pgxpool.Pool

	mu       sync.RWMutex
	handlers map[string]HandlerFunc

	wake chan struct{}
}

func New(db *pgxpool.Pool) *Service {
	return &Service{db: db, handlers: map[string]HandlerFunc{}, wake: make(chan struct{}, 1)}
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
// caller never waits for the job itself here; use EnqueueAndAwait when the
// caller needs the result.
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

// EnqueueAndAwait enqueues a job and polls until it reaches a terminal state
// or ctx is done. This is what lets a caller like /llm/generate keep its
// synchronous, request-blocking contract while still running through the
// same single-worker, priority-ordered queue as everything else: the job
// itself is durable (a disconnected caller's own ctx timing out does not
// lose the work — see Job's row), only this particular wait gives up.
func (s *Service) EnqueueAndAwait(ctx context.Context, kind string, priority Priority, payload json.RawMessage) (json.RawMessage, error) {
	id, err := s.Enqueue(ctx, kind, priority, payload)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		j, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		switch j.Status {
		case StatusDone:
			return j.Result, nil
		case StatusFailed:
			return nil, errors.New(j.Error)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
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

// Retry re-reads a failed job's original kind/payload and enqueues a new job
// with them, returning the new job's id. It does not modify or resubmit the
// original row.
//
// It also carries the failed job's own Step value forward onto the new job
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
	if j.Status != StatusFailed {
		return "", fmt.Errorf("jobs: cannot retry job %s: status is %q, not %q", id, j.Status, StatusFailed)
	}
	return s.enqueue(ctx, j.Kind, j.Priority, j.Payload, j.Step)
}

// Delete removes a done/failed job's row. It refuses to delete a
// pending/running job — a pending job may still be waiting for its turn, and
// a running one may be actively processed by the single worker or polled by
// an EnqueueAndAwait caller — deleting either out from under them isn't
// safe. Not-found and not-terminal both surface as the same error: the
// caller (the Jobs page) only ever offers Delete on rows it already knows
// are terminal, so distinguishing the two cases has no UI use here.
//
// This is not airtight the instant a job turns terminal: an EnqueueAndAwait
// caller polls on pollInterval (500ms), so for up to that long after a job
// reaches 'done'/'failed' its poller may still be mid-wait and not yet have
// noticed. Deleting in that narrow window would make its next Get return
// not-found instead of the real result. Accepted as an unlikely edge case
// (it requires a human to act on the Jobs page inside that same half-second)
// rather than solved with e.g. a grace period, since fixing it for real would
// need the poller itself to distinguish "completed" from "deleted".
func (s *Service) Delete(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, "DELETE FROM jobs WHERE id=$1 AND status IN ('done','failed')", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("jobs: cannot delete job %s: not found or not in a terminal state", id)
	}
	return nil
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

	fn, registered := s.handler(j.Kind)
	if !registered {
		s.complete(context.Background(), j.ID, nil, fmt.Errorf("jobs: no handler registered for kind %q", j.Kind))
		return true
	}

	start := time.Now()
	result, runErr := fn(ctx, j.ID, j.Payload)
	outcome := "success"
	if runErr != nil {
		outcome = "error"
	}
	slog.Info("job completed", "kind", j.Kind, "priority", string(j.Priority), "duration_ms", time.Since(start).Milliseconds(), "outcome", outcome)
	s.complete(context.Background(), j.ID, result, runErr)
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
// the job's own (possibly worker-shutdown) context, so a completion is never
// lost to a context that expired at the wrong moment. A short timeout bounds
// that Background() context so a hung DB connection can't block the single
// worker goroutine forever; if it fires, this job's row is left at
// status='running' and is recovered by the next FailStale at the next
// restart, rather than wedging the whole queue.
func (s *Service) complete(ctx context.Context, id string, result json.RawMessage, jobErr error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	status := StatusDone
	var errText *string
	if jobErr != nil {
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
