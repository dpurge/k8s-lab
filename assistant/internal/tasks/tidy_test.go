package tasks_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"assistant/internal/llmcalls"
	"assistant/internal/tasks"
	"assistant/internal/tasksteps"
)

func TestCancelEndsAPendingTaskAndAWaitingOneDroppingItsQuestion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pending := f.create(t, "p")
	got, err := f.tasks.Cancel(ctx, pending.ID)
	if err != nil || got.Status != tasks.StatusCancelled || got.FinishedAt == nil {
		t.Fatalf("pending: %+v %v", got, err)
	}
	running := claimed(t, f, "w")
	f.tasks.Suspend(ctx, running.ID, runSnapshot, askPayload)
	got, err = f.tasks.Cancel(ctx, running.ID)
	if err != nil || got.Status != tasks.StatusCancelled || got.Ask != nil {
		t.Fatalf("waiting: %+v %v", got, err)
	}
	if got.Run == nil {
		t.Errorf("the run is kept so the cancelled task can still be inspected")
	}
}

func TestCancelRefusesRunningAndFinishedTasksAndUnknownIds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	running := claimed(t, f, "r")
	if _, err := f.tasks.Cancel(ctx, running.ID); !errors.Is(err, tasks.ErrNotCancellable) {
		t.Errorf("running: %v", err)
	}
	f.tasks.Finish(ctx, running.ID, nil, "", nil)
	if _, err := f.tasks.Cancel(ctx, running.ID); !errors.Is(err, tasks.ErrNotCancellable) {
		t.Errorf("finished: %v", err)
	}
	for _, id := range []string{"11111111-1111-1111-1111-111111111111", "not-a-uuid"} {
		if _, err := f.tasks.Cancel(ctx, id); !errors.Is(err, tasks.ErrNotFound) {
			t.Errorf("%s: %v", id, err)
		}
	}
}

func TestMarkCancelledEndsARunningTaskOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	running := claimed(t, f, "r")
	done, err := f.tasks.MarkCancelled(ctx, running.ID, runSnapshot)
	if err != nil || done.Status != tasks.StatusCancelled || !jsonEqual(done.Run, runSnapshot) || done.FinishedAt == nil {
		t.Fatalf("%+v %v", done, err)
	}
	if _, err := f.tasks.MarkCancelled(ctx, running.ID, nil); !errors.Is(err, tasks.ErrNotRunning) {
		t.Errorf("a task that is not running: %v", err)
	}
}

func seedTrace(t *testing.T, f fixture, taskID string) {
	t.Helper()
	ctx := context.Background()
	if err := tasksteps.NewStore(f.pool).Insert(ctx, tasksteps.Step{TaskID: taskID, Step: 1, Attempt: 1, State: "a", Outcome: "ok", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	err := llmcalls.NewStore(f.pool).Insert(ctx, llmcalls.Call{TaskID: &taskID, UserID: f.user.ID, ModelID: "m", ProviderID: "p", ModelName: "n", InputTokens: 10, OutputTokens: 2, CostUSD: 0.5, StartedAt: time.Now(), DurationMS: 5, Outcome: "ok"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRemoveDeletesTheTaskAndItsTraceButKeepsTheCostRecords(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.create(t, "x")
	f.tasks.Cancel(ctx, task.ID)
	seedTrace(t, f, task.ID)
	other := f.create(t, "other")
	seedTrace(t, f, other.ID)

	if err := f.tasks.Remove(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tasks.Get(ctx, task.ID); !errors.Is(err, tasks.ErrNotFound) {
		t.Errorf("the task must be gone: %v", err)
	}
	if steps, _ := tasksteps.NewStore(f.pool).ForTask(ctx, task.ID); len(steps) != 0 {
		t.Errorf("its trace must be gone: %v", steps)
	}
	usage, _ := llmcalls.NewStore(f.pool).UsageByUserAndModel(ctx, nil)
	if len(usage) != 1 || usage[0].Calls != 2 || usage[0].CostUSD != 1 {
		t.Errorf("the model calls stay, so usage and cost are unchanged: %+v", usage)
	}
	if _, err := f.tasks.Get(ctx, other.ID); err != nil {
		t.Errorf("other tasks are untouched: %v", err)
	}
	if steps, _ := tasksteps.NewStore(f.pool).ForTask(ctx, other.ID); len(steps) != 1 {
		t.Errorf("other traces are untouched")
	}
}

func TestRemoveRefusesARunningTaskAndAnUnknownOne(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	running := claimed(t, f, "r")
	if err := f.tasks.Remove(ctx, running.ID); !errors.Is(err, tasks.ErrRunning) {
		t.Errorf("running: %v", err)
	}
	if _, err := f.tasks.Get(ctx, running.ID); err != nil {
		t.Errorf("a refused removal must delete nothing: %v", err)
	}
	for _, id := range []string{"11111111-1111-1111-1111-111111111111", "not-a-uuid"} {
		if err := f.tasks.Remove(ctx, id); !errors.Is(err, tasks.ErrNotFound) {
			t.Errorf("%s: %v", id, err)
		}
	}
}

func TestRemoveFinishedDeletesOnlyTheGivenFinishedStatus(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	failedA, failedB, keep := f.create(t, "a"), f.create(t, "b"), f.create(t, "keep")
	waiting := claimed(t, f, "w") // takes the oldest pending: failedA
	_ = waiting
	// Make the states explicit instead of relying on claim order.
	f.pool.Exec(ctx, `UPDATE tasks SET status = 'failed', finished_at = now() WHERE id IN ($1::uuid, $2::uuid)`, failedA.ID, failedB.ID)
	f.pool.Exec(ctx, `UPDATE tasks SET status = 'succeeded', finished_at = now() WHERE id = $1::uuid`, keep.ID)
	seedTrace(t, f, failedB.ID)

	n, err := f.tasks.RemoveFinished(ctx, tasks.StatusFailed)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := f.tasks.Get(ctx, keep.ID); err != nil {
		t.Errorf("a task of another status stays: %v", err)
	}
	if n, _ := f.tasks.RemoveFinished(ctx, tasks.StatusFailed); n != 0 {
		t.Errorf("nothing left to remove: %d", n)
	}
}

func TestRemoveFinishedRefusesStatusesThatAreNotFinished(t *testing.T) {
	f := newFixture(t)
	for _, status := range []string{"", "pending", "running", "waiting", "bogus"} {
		if _, err := f.tasks.RemoveFinished(context.Background(), status); !errors.Is(err, tasks.ErrNotFinished) {
			t.Errorf("%q: %v", status, err)
		}
	}
}

func TestRetryQueuesACopyOfAFailedOrCancelledTaskAndLeavesTheOriginal(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task, _ := f.tasks.Create(ctx, tasks.New{UserID: f.user.ID, WorkflowID: "learn", Source: tasks.SourceAPI, Payload: json.RawMessage(`{"message":"again"}`)})
	running, _, _ := f.tasks.Claim(ctx)
	f.tasks.Finish(ctx, running.ID, nil, "boom", runSnapshot)

	copyOf, err := f.tasks.Retry(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if copyOf.ID == task.ID || copyOf.Status != tasks.StatusPending || copyOf.WorkflowID != "learn" || copyOf.UserID != f.user.ID || !jsonEqual(copyOf.Payload, json.RawMessage(`{"message":"again"}`)) {
		t.Errorf("copy = %+v", copyOf)
	}
	if copyOf.Run != nil || copyOf.Error != nil {
		t.Errorf("a retry starts clean: %+v", copyOf)
	}
	original, _ := f.tasks.Get(ctx, task.ID)
	if original.Status != tasks.StatusFailed || original.Error == nil {
		t.Errorf("the original stays as it was: %+v", original)
	}
	cancelled := f.create(t, "c")
	f.tasks.Cancel(ctx, cancelled.ID)
	if _, err := f.tasks.Retry(ctx, cancelled.ID); err != nil {
		t.Errorf("a cancelled task can be retried: %v", err)
	}
}

func TestRetryRefusesTasksThatAreNotFailedOrCancelled(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pending := f.create(t, "p")
	if _, err := f.tasks.Retry(ctx, pending.ID); !errors.Is(err, tasks.ErrNotRetryable) {
		t.Errorf("pending: %v", err)
	}
	done := claimed(t, f, "d")
	f.tasks.Finish(ctx, done.ID, nil, "", nil)
	if _, err := f.tasks.Retry(ctx, done.ID); !errors.Is(err, tasks.ErrNotRetryable) {
		t.Errorf("succeeded: %v", err)
	}
	if _, err := f.tasks.Retry(ctx, "11111111-1111-1111-1111-111111111111"); !errors.Is(err, tasks.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
}

// ── the worker stops a running task ──────────────────────────────────────────

func TestCancellingTheRunningTaskStopsItsRunAndEndsItCancelled(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.create(t, "slow")
	started := make(chan struct{})
	w, obs, _ := newWorker(f, outcomeRunner2(func(ctx context.Context, tk tasks.Task) (tasks.Outcome, error) {
		close(started)
		<-ctx.Done()
		return tasks.Outcome{Run: runSnapshot}, ctx.Err()
	}))
	done := make(chan error, 1)
	go func() { _, err := w.RunOnce(ctx); done <- err }()
	<-started
	if w.Cancel("11111111-1111-1111-1111-111111111111") {
		t.Error("cancelling some other task must do nothing")
	}
	if !w.Cancel(task.ID) {
		t.Fatal("the running task must be cancellable")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, _ := f.tasks.Get(ctx, task.ID)
	if got.Status != tasks.StatusCancelled || got.Error != nil || !jsonEqual(got.Run, runSnapshot) {
		t.Errorf("got = %+v", got)
	}
	if len(obs.got) != 1 || obs.got[0].status != tasks.StatusCancelled {
		t.Errorf("observed %+v", obs.got)
	}
	if w.Cancel(task.ID) {
		t.Error("once it ended, there is nothing to cancel")
	}
}

type outcomeRunner2 func(ctx context.Context, t tasks.Task) (tasks.Outcome, error)

func (r outcomeRunner2) Run(ctx context.Context, t tasks.Task) (tasks.Outcome, error) {
	return r(ctx, t)
}

func TestARunThatFailsBecauseOfACancelIsCancelledNotFailed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.create(t, "slow")
	started := make(chan struct{})
	w, _, _ := newWorker(f, outcomeRunner2(func(ctx context.Context, tk tasks.Task) (tasks.Outcome, error) {
		close(started)
		<-ctx.Done()
		return tasks.Outcome{}, errors.New("cancelled: the context was cancelled")
	}))
	go func() { <-started; w.Cancel(task.ID) }()
	w.RunOnce(ctx)
	if got, _ := f.tasks.Get(ctx, task.ID); got.Status != tasks.StatusCancelled {
		t.Errorf("status = %s", got.Status)
	}
}
