// Package queue provides the single app-wide LLM-operation queue: at most
// one operation runs at a time, and an interactive-priority operation
// (chat reply, manual generate) always jumps ahead of any pending
// background-priority one (an ingest chunk) — see specs/features/
// knowledge-background-queue.md for the full design.
package queue

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
// when nothing woke it early via Enqueue's nudge — this queue is
// single-worker and low-throughput by design, so a short fixed poll costs
// nothing real while keeping the implementation simple (no LISTEN/NOTIFY).
const pollInterval = 500 * time.Millisecond

type Operation struct {
	ID        string
	Kind      string
	Priority  Priority
	Status    Status
	Payload   json.RawMessage
	Result    json.RawMessage
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// HandlerFunc performs one operation's actual work. It must not be called
// directly by any package other than queue itself — kinds are dispatched by
// the worker loop via the registration in Register.
type HandlerFunc func(ctx context.Context, payload json.RawMessage) (result json.RawMessage, err error)

// Service owns the operations table and the single worker goroutine that
// drains it. Handler registration follows this codebase's existing
// import-graph-avoidance convention (see ingest.Promoter): queue never
// imports chat/generate/ingest, and main.go registers each service's
// handler method here after constructing everything.
type Service struct {
	db *pgxpool.Pool

	mu       sync.RWMutex
	handlers map[string]HandlerFunc

	wake chan struct{}
}

func New(db *pgxpool.Pool) *Service {
	return &Service{db: db, handlers: map[string]HandlerFunc{}, wake: make(chan struct{}, 1)}
}

// Register associates a handler with an operation kind. Must be called
// before Run starts processing operations of that kind; calling it again
// for the same kind replaces the previous handler.
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

// Enqueue records a new pending operation and wakes the worker so it need
// not wait for the next poll tick. It returns as soon as the row is
// written — the caller never waits for the operation itself here; use
// EnqueueAndAwait when the caller needs the result.
func (s *Service) Enqueue(ctx context.Context, kind string, priority Priority, payload any) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	id, err := uuid()
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(ctx, "INSERT INTO operations(id,kind,priority,status,payload) VALUES($1,$2,$3,'pending',$4)", id, kind, priority, body)
	if err != nil {
		return "", err
	}
	s.nudge()
	return id, nil
}

// EnqueueAndAwait enqueues an operation and polls until it reaches a
// terminal state or ctx is done. This is what lets generate.Title/Summary
// keep their synchronous, request-blocking contract while still running
// through the same single-worker, priority-ordered queue as everything
// else: the operation itself is durable (a disconnected caller's own ctx
// timing out does not lose the work — see Operation's row), only this
// particular wait gives up.
func (s *Service) EnqueueAndAwait(ctx context.Context, kind string, priority Priority, payload any) (json.RawMessage, error) {
	id, err := s.Enqueue(ctx, kind, priority, payload)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		op, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		switch op.Status {
		case StatusDone:
			return op.Result, nil
		case StatusFailed:
			return nil, errors.New(op.Error)
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

// Get returns one operation by id.
func (s *Service) Get(ctx context.Context, id string) (Operation, error) {
	row := s.db.QueryRow(ctx, "SELECT id,kind,priority,status,payload,coalesce(result,'null'),coalesce(error,''),created_at,updated_at FROM operations WHERE id=$1", id)
	return scanOperation(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanOperation(row scanner) (Operation, error) {
	var op Operation
	var payload, result []byte
	err := row.Scan(&op.ID, &op.Kind, &op.Priority, &op.Status, &payload, &result, &op.Error, &op.CreatedAt, &op.UpdatedAt)
	if err != nil {
		return Operation{}, err
	}
	op.Payload = json.RawMessage(payload)
	op.Result = json.RawMessage(result)
	return op, nil
}

// FailStale marks any still-"running" operation as failed. A process
// restart (crash, rolling update, node reschedule) leaves a claimed
// operation's row stuck at status='running' forever, since nothing else
// ever calls complete for it — left alone, operations_one_running_idx
// (a single global slot, unlike jobs' per-kind index) would then block
// every future claim permanently, jamming the whole queue. Call once at
// startup, before Run starts claiming new work — mirrors
// jobs.Service.FailStale.
func (s *Service) FailStale(ctx context.Context) error {
	_, err := s.db.Exec(ctx, "UPDATE operations SET status='failed', error='interrupted by restart', updated_at=now() WHERE status='running'")
	return err
}

// Run is the worker loop: it must be started exactly once, in its own
// goroutine, with a context that lives for the server's whole lifetime.
// It never returns until ctx is done.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		for s.runOne(ctx) {
			// Drain everything currently pending before waiting on the next
			// tick or nudge — re-checking priority after every completed
			// operation is what lets a newly-arrived interactive operation
			// jump ahead of remaining background ones immediately.
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

// runOne claims and runs at most one pending operation, preferring the
// oldest pending interactive-priority row over any background-priority
// row. It reports whether it did work, so Run's caller can keep draining
// without waiting for the next tick.
func (s *Service) runOne(ctx context.Context) bool {
	op, ok, err := s.claim(ctx)
	if err != nil {
		log.Printf("queue: claim operation: %v", err)
		return false
	}
	if !ok {
		return false
	}

	fn, registered := s.handler(op.Kind)
	if !registered {
		s.complete(context.Background(), op.ID, nil, fmt.Errorf("queue: no handler registered for kind %q", op.Kind))
		return true
	}

	start := time.Now()
	result, runErr := fn(ctx, op.Payload)
	outcome := "ok"
	if runErr != nil {
		outcome = "error"
	}
	slog.Info("queue operation completed", "kind", op.Kind, "priority", string(op.Priority), "duration_ms", time.Since(start).Milliseconds(), "outcome", outcome)
	s.complete(context.Background(), op.ID, result, runErr)
	return true
}

// claim atomically picks the oldest pending interactive-priority operation,
// or (only if none is pending) the oldest pending background-priority one,
// and marks it running in a single statement — the standard Postgres
// claim-a-job pattern, safe even without an explicit transaction since one
// UPDATE statement is already atomic. operations_one_running_idx (see
// db.schema) is the DB-level backstop; with exactly one worker goroutine
// calling claim, that backstop is never actually contended.
func (s *Service) claim(ctx context.Context) (Operation, bool, error) {
	row := s.db.QueryRow(ctx, `
		UPDATE operations SET status = 'running', updated_at = now()
		WHERE id = (
			SELECT id FROM operations
			WHERE status = 'pending'
			ORDER BY (priority = 'interactive') DESC, created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id,kind,priority,status,payload,coalesce(result,'null'),coalesce(error,''),created_at,updated_at`)
	op, err := scanOperation(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, false, nil
	}
	if err != nil {
		return Operation{}, false, err
	}
	return op, true, nil
}

// complete records an operation's terminal state using ctx.Background()
// rather than the operation's own (possibly worker-shutdown) context, so a
// completion is never lost to a context that expired at the wrong moment —
// the same rationale as ingest's finish helper.
func (s *Service) complete(ctx context.Context, id string, result json.RawMessage, opErr error) {
	status := StatusDone
	var errText *string
	if opErr != nil {
		status = StatusFailed
		text := opErr.Error()
		errText = &text
	}
	if result == nil {
		result = json.RawMessage("null")
	}
	if _, err := s.db.Exec(ctx, "UPDATE operations SET status=$1, result=$2, error=$3, updated_at=now() WHERE id=$4", status, result, errText, id); err != nil {
		log.Printf("queue: failed to record completion for operation %s: %v", id, err)
	}
}
