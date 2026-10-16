package tasks_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"assistant/internal/tasks"
)

func TestClaimTakesTheOldestPendingTask(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	newest, oldest, middle := f.create(t, "newest"), f.create(t, "oldest"), f.create(t, "middle")
	f.setCreatedAt(t, oldest.ID, base)
	f.setCreatedAt(t, middle.ID, base.Add(time.Minute))
	f.setCreatedAt(t, newest.ID, base.Add(2*time.Minute))

	var order []string
	for i := 0; i < 3; i++ {
		got, ok, err := f.tasks.Claim(ctx)
		if err != nil || !ok {
			t.Fatalf("claim %d: %v %v", i, ok, err)
		}
		if got.Status != tasks.StatusRunning || got.StartedAt == nil || got.FinishedAt != nil {
			t.Errorf("a claimed task is running with a start time: %+v", got)
		}
		order = append(order, got.WorkflowID)
		if _, err := f.tasks.Finish(ctx, got.ID, nil, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := order[0] + "," + order[1] + "," + order[2]; got != "oldest,middle,newest" {
		t.Errorf("order = %s", got)
	}
}

func TestClaimOnAnEmptyQueue(t *testing.T) {
	f := newFixture(t)
	if _, ok, err := f.tasks.Claim(context.Background()); ok || err != nil {
		t.Errorf("ok=%v err=%v", ok, err)
	}
}

func TestClaimBreaksTiesById(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	a, b := f.create(t, "a"), f.create(t, "b")
	f.setCreatedAt(t, a.ID, at)
	f.setCreatedAt(t, b.ID, at)
	first, _, _ := f.tasks.Claim(ctx)
	want := a.ID
	if b.ID < a.ID {
		want = b.ID
	}
	if first.ID != want {
		t.Errorf("equal timestamps are claimed by id: got %s want %s", first.ID, want)
	}
}

func TestOnlyOneTaskRunsAtATime(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.create(t, "first")
	f.create(t, "second")
	running, ok, err := f.tasks.Claim(ctx)
	if !ok || err != nil {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if _, ok, err := f.tasks.Claim(ctx); ok || err != nil {
		t.Errorf("a second claim while one runs must report nothing to do: ok=%v err=%v", ok, err)
	}
	if _, err := f.tasks.Finish(ctx, running.ID, nil, "", nil); err != nil {
		t.Fatal(err)
	}
	if next, ok, err := f.tasks.Claim(ctx); !ok || err != nil || next.WorkflowID != "second" {
		t.Errorf("after the first finished the second can start: %+v %v %v", next, ok, err)
	}
}

func TestConcurrentClaimsHaveOneWinner(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 5; i++ {
		f.create(t, "w")
	}
	const workers = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	errs := []error{}
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, ok, err := f.tasks.Claim(context.Background())
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
			if ok {
				winners++
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("claims must not error under contention: %v", errs)
	}
	if winners != 1 {
		t.Errorf("%d claims won, want exactly 1", winners)
	}
	var running int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM tasks WHERE status = 'running'`).Scan(&running); err != nil || running != 1 {
		t.Errorf("running tasks = %d (%v)", running, err)
	}
}

func TestFinishRecordsASuccess(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.create(t, "w")
	claimed, _, _ := f.tasks.Claim(ctx)
	done, err := f.tasks.Finish(ctx, claimed.ID, json.RawMessage(`{"answer":42}`), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != tasks.StatusSucceeded || string(done.Result) != `{"answer": 42}` && string(done.Result) != `{"answer":42}` || done.Error != nil || done.FinishedAt == nil {
		t.Errorf("done = %+v", done)
	}
	if done.StartedAt == nil || done.FinishedAt.Before(*done.StartedAt) || done.StartedAt.Before(done.CreatedAt) {
		t.Errorf("timestamps out of order: %+v", done)
	}
}

func TestFinishRecordsAFailure(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.create(t, "w")
	claimed, _, _ := f.tasks.Claim(ctx)
	done, err := f.tasks.Finish(ctx, claimed.ID, json.RawMessage(`{"ignored":true}`), "unknown workflow: w", nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != tasks.StatusFailed || done.Error == nil || *done.Error != "unknown workflow: w" || len(done.Result) != 0 && string(done.Result) != "null" {
		t.Errorf("a failed task keeps its error and no result: %+v", done)
	}
}

func TestFinishWithoutAResultLeavesItNull(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.create(t, "w")
	claimed, _, _ := f.tasks.Claim(ctx)
	done, err := f.tasks.Finish(ctx, claimed.ID, nil, "", nil)
	if err != nil || (len(done.Result) != 0 && string(done.Result) != "null") {
		t.Errorf("done = %+v, %v", done, err)
	}
}

func TestFinishOnlyEndsARunningTask(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pending := f.create(t, "w")
	if _, err := f.tasks.Finish(ctx, pending.ID, nil, "", nil); !errors.Is(err, tasks.ErrNotRunning) {
		t.Errorf("a pending task cannot be finished: %v", err)
	}
	claimed, _, _ := f.tasks.Claim(ctx)
	if _, err := f.tasks.Finish(ctx, claimed.ID, nil, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tasks.Finish(ctx, claimed.ID, nil, "again", nil); !errors.Is(err, tasks.ErrNotRunning) {
		t.Errorf("a finished task cannot be finished twice: %v", err)
	}
	got, _ := f.tasks.Get(ctx, claimed.ID)
	if got.Status != tasks.StatusSucceeded || got.Error != nil {
		t.Errorf("the second finish must change nothing: %+v", got)
	}
	if _, err := f.tasks.Finish(ctx, "not-a-uuid", nil, "", nil); err == nil {
		t.Errorf("a malformed id must be an error")
	}
}

func TestRecoverCrashedFailsRunningTasksOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.create(t, "was-running")
	f.create(t, "still-pending")
	running, _, _ := f.tasks.Claim(ctx)

	n, err := f.tasks.RecoverCrashed(ctx)
	if err != nil || n != 1 {
		t.Fatalf("recovered %d, %v", n, err)
	}
	got, _ := f.tasks.Get(ctx, running.ID)
	if got.Status != tasks.StatusFailed || got.Error == nil || *got.Error != tasks.InterruptedMessage || got.FinishedAt == nil {
		t.Errorf("recovered task = %+v", got)
	}
	pending, _ := f.tasks.List(ctx, tasks.ListOptions{Status: tasks.StatusPending})
	if pending.Total != 1 || pending.Tasks[0].WorkflowID != "still-pending" {
		t.Errorf("pending tasks must be left alone: %+v", pending)
	}
	if again, err := f.tasks.RecoverCrashed(ctx); err != nil || again != 0 {
		t.Errorf("a second recovery finds nothing: %d, %v", again, err)
	}
	if next, ok, _ := f.tasks.Claim(ctx); !ok || next.WorkflowID != "still-pending" {
		t.Errorf("after recovery the queue moves on: %+v %v", next, ok)
	}
}
