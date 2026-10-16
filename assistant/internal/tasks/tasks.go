// Package tasks is the assistant's queue: what was asked, for whom, which
// workflow runs it, and how it went.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"assistant/internal/users"
)

// Where a task came from.
const (
	SourceTelegram  = "telegram"
	SourceNATS      = "nats"
	SourceScheduler = "scheduler"
	SourceAPI       = "api"
)

// Where a task is in its life.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusWaiting   = "waiting"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Sources and Statuses list every valid value, in display order.
var (
	Sources  = []string{SourceTelegram, SourceNATS, SourceScheduler, SourceAPI}
	Statuses = []string{StatusPending, StatusRunning, StatusWaiting, StatusSucceeded, StatusFailed, StatusCancelled}
)

// Errors a caller can tell apart.
var (
	ErrNotFound       = errors.New("task not found")
	ErrUnknownUser    = errors.New("user not found")
	ErrBlankWorkflow  = errors.New("workflow_id is blank")
	ErrInvalidSource  = errors.New("source is not one of telegram, nats, scheduler, api")
	ErrInvalidStatus  = errors.New("status is not one of pending, running, waiting, succeeded, failed, cancelled")
	ErrPayloadNotJSON = errors.New("payload is not a JSON object")
)

// Task is one unit of work.
type Task struct {
	ID         string          `json:"id"`
	UserID     string          `json:"user_id"`
	WorkflowID string          `json:"workflow_id"`
	Source     string          `json:"source"`
	Status     string          `json:"status"`
	Payload    json.RawMessage `json:"payload"`
	Result     json.RawMessage `json:"result"`
	Error      *string         `json:"error"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  *time.Time      `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
	// Run is the workflow run's JSON snapshot; null until a workflow has run.
	// The list leaves it out.
	Run json.RawMessage `json:"run"`
	// Ask is what a waiting run asked the user; null otherwise.
	Ask json.RawMessage `json:"ask"`
	// ResumeMessage is the answer the worker has yet to deliver. It is internal.
	ResumeMessage json.RawMessage `json:"-"`
}

// QueueTime is how long the task waited for a worker: until it started, or
// until now if it has not started yet. Derived from the timestamps, never
// stored.
func (t Task) QueueTime(now time.Time) time.Duration {
	end := now
	if t.StartedAt != nil {
		end = *t.StartedAt
	}
	return nonNegative(end.Sub(t.CreatedAt))
}

// ExecutionTime is how long the task has been running: until it finished, or
// until now if it is still running. It reports false for a task that has not
// started.
func (t Task) ExecutionTime(now time.Time) (time.Duration, bool) {
	if t.StartedAt == nil {
		return 0, false
	}
	end := now
	if t.FinishedAt != nil {
		end = *t.FinishedAt
	}
	return nonNegative(end.Sub(*t.StartedAt)), true
}

func nonNegative(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}

// New is a task to be queued.
type New struct {
	UserID     string
	WorkflowID string
	Source     string
	// Payload must be a JSON object; empty means {}.
	Payload json.RawMessage
}

// Store reads and writes tasks.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a store on pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const columns = `id::text, user_id::text, workflow_id, source, status, payload, result, error, created_at, started_at, finished_at, run, ask, resume_message`

// listColumns leaves the run out: a page of tasks does not need every run's
// state.
const listColumns = `id::text, user_id::text, workflow_id, source, status, payload, result, error, created_at, started_at, finished_at, NULL::jsonb, ask, resume_message`

func scan(row pgx.Row) (Task, error) {
	var t Task
	var payload, result, run, ask, resume []byte
	err := row.Scan(&t.ID, &t.UserID, &t.WorkflowID, &t.Source, &t.Status, &payload, &result, &t.Error, &t.CreatedAt, &t.StartedAt, &t.FinishedAt, &run, &ask, &resume)
	if err != nil {
		return Task{}, err
	}
	t.Payload = json.RawMessage(payload)
	for _, f := range []struct {
		dst *json.RawMessage
		src []byte
	}{{&t.Result, result}, {&t.Run, run}, {&t.Ask, ask}, {&t.ResumeMessage, resume}} {
		if f.src != nil {
			*f.dst = json.RawMessage(f.src)
		}
	}
	return t, nil
}

func validSource(s string) bool {
	for _, v := range Sources {
		if v == s {
			return true
		}
	}
	return false
}

func validStatus(s string) bool {
	for _, v := range Statuses {
		if v == s {
			return true
		}
	}
	return false
}

// Create queues a task as pending.
func (s *Store) Create(ctx context.Context, n New) (Task, error) {
	workflow := strings.TrimSpace(n.WorkflowID)
	if workflow == "" {
		return Task{}, ErrBlankWorkflow
	}
	if !validSource(n.Source) {
		return Task{}, ErrInvalidSource
	}
	if !users.ValidID(n.UserID) {
		return Task{}, ErrUnknownUser
	}
	payload := n.Payload
	if len(strings.TrimSpace(string(payload))) == 0 {
		payload = json.RawMessage(`{}`)
	}
	var probe map[string]any
	if err := json.Unmarshal(payload, &probe); err != nil || probe == nil {
		return Task{}, ErrPayloadNotJSON
	}
	t, err := scan(s.pool.QueryRow(ctx,
		`INSERT INTO tasks (user_id, workflow_id, source, payload) VALUES ($1::uuid, $2, $3, $4) RETURNING `+columns,
		n.UserID, workflow, n.Source, []byte(payload)))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return Task{}, ErrUnknownUser
		}
		return Task{}, fmt.Errorf("create task: %w", err)
	}
	return t, nil
}

// Get returns the task with id, or ErrNotFound. An id that is not a UUID
// cannot exist and is also ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (Task, error) {
	if !users.ValidID(id) {
		return Task{}, ErrNotFound
	}
	t, err := scan(s.pool.QueryRow(ctx, `SELECT `+columns+` FROM tasks WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("get task: %w", err)
	}
	return t, nil
}

// ListOptions selects a page of tasks.
type ListOptions struct {
	// Status keeps only tasks in that status; empty keeps all.
	Status string
	Limit  int
	Offset int
}

// Page is one page of tasks and how many match in all.
type Page struct {
	Tasks []Task
	Total int
}

// Limits of a page.
const (
	DefaultLimit = 25
	MaxLimit     = 100
)

// List returns tasks newest first. A zero Limit means DefaultLimit.
func (s *Store) List(ctx context.Context, o ListOptions) (Page, error) {
	if o.Status != "" && !validStatus(o.Status) {
		return Page{}, ErrInvalidStatus
	}
	limit := o.Limit
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < 1 || limit > MaxLimit || o.Offset < 0 {
		return Page{}, fmt.Errorf("list tasks: limit must be 1 to %d and offset at least 0", MaxLimit)
	}

	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM tasks WHERE ($1 = '' OR status = $1)`, o.Status).Scan(&total); err != nil {
		return Page{}, fmt.Errorf("count tasks: %w", err)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+listColumns+` FROM tasks WHERE ($1 = '' OR status = $1) ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`,
		o.Status, limit, o.Offset)
	if err != nil {
		return Page{}, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	page := Page{Tasks: []Task{}, Total: total}
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return Page{}, fmt.Errorf("list tasks: %w", err)
		}
		page.Tasks = append(page.Tasks, t)
	}
	return page, rows.Err()
}
