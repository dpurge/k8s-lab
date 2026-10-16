package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"assistant/internal/users"
)

// ErrNotRunning is returned by Finish and Suspend for a task that is not running.
var ErrNotRunning = errors.New("task is not running")

// ErrNotWaiting is returned by Resume for a task that is not waiting for an
// answer, including one whose answer the worker has not picked up yet.
var ErrNotWaiting = errors.New("task is not waiting for an answer")

// InterruptedMessage is the error of a task that was running when the process
// stopped.
const InterruptedMessage = "interrupted: the process stopped while this task was running"

// Claim starts the oldest pending task: it becomes running, with started_at set
// the first time (a resumed task keeps its first start).
// It reports false when there is nothing to do: no pending task, or a task is
// already running (the queue runs one task at a time, app-wide).
func (s *Store) Claim(ctx context.Context) (Task, bool, error) {
	t, err := scan(s.pool.QueryRow(ctx, `
		UPDATE tasks SET status = 'running', started_at = COALESCE(started_at, now())
		WHERE id = (
			SELECT id FROM tasks WHERE status = 'pending'
			ORDER BY created_at, id
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+columns))
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "tasks_one_running_idx" {
			return Task{}, false, nil
		}
		return Task{}, false, fmt.Errorf("claim task: %w", err)
	}
	return t, true, nil
}

// Finish ends a running task as succeeded, with result (a JSON value, may be
// nil), or as failed, with message, and sets finished_at. Exactly one of the
// two is expected: a non-empty message means failed. run, if not empty, is the
// workflow run's final snapshot; any question or answer still held is cleared.
func (s *Store) Finish(ctx context.Context, id string, result json.RawMessage, message string, run json.RawMessage) (Task, error) {
	status := StatusSucceeded
	var errText *string
	if message != "" {
		status = StatusFailed
		errText = &message
	}
	var resultArg []byte
	if status == StatusSucceeded && len(result) > 0 {
		resultArg = []byte(result)
	}
	var runArg []byte
	if len(run) > 0 {
		runArg = []byte(run)
	}
	t, err := scan(s.pool.QueryRow(ctx, `
		UPDATE tasks SET status = $2, result = $3, error = $4, finished_at = now(),
		       run = COALESCE($5, run), ask = NULL, resume_message = NULL
		WHERE id = $1::uuid AND status = 'running'
		RETURNING `+columns, id, status, resultArg, errText, runArg))
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotRunning
	}
	if err != nil {
		return Task{}, fmt.Errorf("finish task: %w", err)
	}
	return t, nil
}

// Suspend parks a running task whose run is waiting for the user: it becomes
// waiting, holding the run's snapshot and the question. It no longer counts as
// running, so the queue goes on.
func (s *Store) Suspend(ctx context.Context, id string, run, ask json.RawMessage) (Task, error) {
	t, err := scan(s.pool.QueryRow(ctx, `
		UPDATE tasks SET status = 'waiting', run = $2, ask = $3, resume_message = NULL
		WHERE id = $1::uuid AND status = 'running'
		RETURNING `+columns, id, []byte(run), []byte(ask)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotRunning
	}
	if err != nil {
		return Task{}, fmt.Errorf("suspend task: %w", err)
	}
	return t, nil
}

// Resume queues a waiting task again with the user's answer, which the worker
// delivers to the run. ErrNotFound for no such task, ErrNotWaiting for one in
// any other state: answering twice is refused.
func (s *Store) Resume(ctx context.Context, id string, message json.RawMessage) (Task, error) {
	if !users.ValidID(id) {
		return Task{}, ErrNotFound
	}
	t, err := scan(s.pool.QueryRow(ctx, `
		UPDATE tasks SET status = 'pending', resume_message = $2
		WHERE id = $1::uuid AND status = 'waiting'
		RETURNING `+columns, id, []byte(message)))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := s.Get(ctx, id); errors.Is(getErr, ErrNotFound) {
			return Task{}, ErrNotFound
		}
		return Task{}, ErrNotWaiting
	}
	if err != nil {
		return Task{}, fmt.Errorf("resume task: %w", err)
	}
	return t, nil
}

// RecoverCrashed fails every task still marked running. Call it once at
// startup, before the worker starts: with one worker, a running task can only
// be left over from a process that stopped. It returns how many it failed.
func (s *Store) RecoverCrashed(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE tasks SET status = 'failed', error = $1, finished_at = now() WHERE status = 'running'`, InterruptedMessage)
	if err != nil {
		return 0, fmt.Errorf("recover crashed tasks: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// Errors of the actions that tidy the queue.
var (
	// ErrNotCancellable: only a pending or waiting task can be cancelled here;
	// a running one is stopped through the worker, a finished one is done.
	ErrNotCancellable = errors.New("task cannot be cancelled in its state")
	// ErrRunning: a running task cannot be removed; cancel it first.
	ErrRunning = errors.New("task is running")
	// ErrNotRetryable: only a failed or cancelled task can be retried.
	ErrNotRetryable = errors.New("only a failed or cancelled task can be retried")
	// ErrNotFinished: bulk removal is for finished tasks only.
	ErrNotFinished = errors.New("status is not one of succeeded, failed, cancelled")
)

// Cancel ends a pending or waiting task as cancelled, dropping the question it
// held. ErrNotFound for no such task, ErrNotCancellable for any other state.
func (s *Store) Cancel(ctx context.Context, id string) (Task, error) {
	if !users.ValidID(id) {
		return Task{}, ErrNotFound
	}
	t, err := scan(s.pool.QueryRow(ctx, `
		UPDATE tasks SET status = 'cancelled', finished_at = now(), ask = NULL, resume_message = NULL
		WHERE id = $1::uuid AND status IN ('pending', 'waiting')
		RETURNING `+columns, id))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := s.Get(ctx, id); errors.Is(getErr, ErrNotFound) {
			return Task{}, ErrNotFound
		}
		return Task{}, ErrNotCancellable
	}
	if err != nil {
		return Task{}, fmt.Errorf("cancel task: %w", err)
	}
	return t, nil
}

// MarkCancelled ends a running task as cancelled, after the worker stopped its
// run at the user's request. run, if not empty, is the run's last snapshot.
func (s *Store) MarkCancelled(ctx context.Context, id string, run json.RawMessage) (Task, error) {
	var runArg []byte
	if len(run) > 0 {
		runArg = []byte(run)
	}
	t, err := scan(s.pool.QueryRow(ctx, `
		UPDATE tasks SET status = 'cancelled', finished_at = now(), run = COALESCE($2, run), ask = NULL, resume_message = NULL
		WHERE id = $1::uuid AND status = 'running'
		RETURNING `+columns, id, runArg))
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotRunning
	}
	if err != nil {
		return Task{}, fmt.Errorf("mark task cancelled: %w", err)
	}
	return t, nil
}

// removeSQL drops what hangs on the tasks selected by $1: the trace goes, the
// model call records stay (so cost stays attributed to the user) but lose their
// task.
const removeCalls = `UPDATE llm_calls SET task_id = NULL WHERE task_id IN (%s)`
const removeSteps = `DELETE FROM task_steps WHERE task_id IN (%s)`

// Remove deletes a task with its trace. ErrNotFound for no such task,
// ErrRunning for one that is running. The model calls it made are kept, with no
// task, so usage and cost do not change.
func (s *Store) Remove(ctx context.Context, id string) error {
	if !users.ValidID(id) {
		return ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("remove task: %w", err)
	}
	defer tx.Rollback(ctx)
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM tasks WHERE id = $1::uuid FOR UPDATE`, id).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("remove task: %w", err)
	}
	if status == StatusRunning {
		return ErrRunning
	}
	pick := `SELECT $1::uuid`
	for _, q := range []string{removeCalls, removeSteps} {
		if _, err := tx.Exec(ctx, fmt.Sprintf(q, pick), id); err != nil {
			return fmt.Errorf("remove task: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM tasks WHERE id = $1::uuid`, id); err != nil {
		return fmt.Errorf("remove task: %w", err)
	}
	return tx.Commit(ctx)
}

// RemoveFinished deletes every task in the given finished status (succeeded,
// failed or cancelled) and returns how many. Anything else is ErrNotFinished:
// there is no way to remove running or waiting tasks in bulk.
func (s *Store) RemoveFinished(ctx context.Context, status string) (int, error) {
	if status != StatusSucceeded && status != StatusFailed && status != StatusCancelled {
		return 0, ErrNotFinished
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("remove tasks: %w", err)
	}
	defer tx.Rollback(ctx)
	pick := `SELECT id FROM tasks WHERE status = $1`
	for _, q := range []string{removeCalls, removeSteps} {
		if _, err := tx.Exec(ctx, fmt.Sprintf(q, pick), status); err != nil {
			return 0, fmt.Errorf("remove tasks: %w", err)
		}
	}
	tag, err := tx.Exec(ctx, `DELETE FROM tasks WHERE status = $1`, status)
	if err != nil {
		return 0, fmt.Errorf("remove tasks: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("remove tasks: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// Retry queues a new task with the user, workflow, source and payload of a
// failed or cancelled one. The original stays as it is, for comparison.
func (s *Store) Retry(ctx context.Context, id string) (Task, error) {
	old, err := s.Get(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if old.Status != StatusFailed && old.Status != StatusCancelled {
		return Task{}, ErrNotRetryable
	}
	return s.Create(ctx, New{UserID: old.UserID, WorkflowID: old.WorkflowID, Source: old.Source, Payload: old.Payload})
}
