package tasks

import (
	"context"
	"errors"
	"fmt"
)

// CancelOutcome says how a cancel took effect.
type CancelOutcome int

const (
	// CancelledNow: the task was pending or waiting and has ended as cancelled.
	CancelledNow CancelOutcome = iota + 1
	// Stopping: the task is running and was told to stop; it ends as cancelled
	// within moments.
	Stopping
)

// CancelStore is what CancelOrStop needs from the queue; *Store implements it.
type CancelStore interface {
	Cancel(ctx context.Context, id string) (Task, error)
	Get(ctx context.Context, id string) (Task, error)
}

// Stopper stops the task a worker is running, if it is the one named; *Worker
// implements it.
type Stopper interface {
	Cancel(id string) bool
}

// CancelOrStop ends a task: a pending or waiting one at once through the
// store, a running one through the worker. It returns ErrNotFound for no such
// task and ErrNotCancellable for one that is finished or not running here. The
// worker may be nil (then a running task is ErrNotCancellable). For Stopping
// the returned task is the current one, still running.
func CancelOrStop(ctx context.Context, store CancelStore, worker Stopper, id string) (Task, CancelOutcome, error) {
	t, err := store.Cancel(ctx, id)
	if err == nil {
		return t, CancelledNow, nil
	}
	if !errors.Is(err, ErrNotCancellable) {
		return Task{}, 0, err
	}
	if worker == nil || !worker.Cancel(id) {
		return Task{}, 0, ErrNotCancellable
	}
	current, err := store.Get(ctx, id)
	if err != nil {
		// Not wrapped with %w: the worker did stop the task, so a task that is gone
		// now is not a "not found" answer for the caller.
		return Task{}, 0, fmt.Errorf("get cancelled task: %v", err)
	}
	return current, Stopping, nil
}
