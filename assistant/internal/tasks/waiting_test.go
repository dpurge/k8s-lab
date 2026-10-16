package tasks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"assistant/internal/tasks"
)

var (
	runSnapshot = json.RawMessage(`{"run_id":"r","state_name":"intake","state":{"n":1}}`)
	askPayload  = json.RawMessage(`{"question":"Which language?"}`)
)

// claimed returns a task that is running.
func claimed(t *testing.T, f fixture, workflow string) tasks.Task {
	t.Helper()
	f.create(t, workflow)
	got, ok, err := f.tasks.Claim(context.Background())
	if !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	return got
}

func TestSuspendParksARunningTaskWithItsRunAndQuestion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	running := claimed(t, f, "learn")

	parked, err := f.tasks.Suspend(ctx, running.ID, runSnapshot, askPayload)
	if err != nil {
		t.Fatal(err)
	}
	if parked.Status != tasks.StatusWaiting || parked.FinishedAt != nil || parked.StartedAt == nil {
		t.Errorf("parked = %+v", parked)
	}
	got, _ := f.tasks.Get(ctx, running.ID)
	if string(got.Run) != string(runSnapshot) && !jsonEqual(got.Run, runSnapshot) {
		t.Errorf("run = %s", got.Run)
	}
	if !jsonEqual(got.Ask, askPayload) {
		t.Errorf("ask = %s", got.Ask)
	}
}

func TestAWaitingTaskDoesNotBlockTheQueue(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := claimed(t, f, "learn")
	if _, err := f.tasks.Suspend(ctx, first.ID, runSnapshot, askPayload); err != nil {
		t.Fatal(err)
	}
	f.create(t, "next")
	next, ok, err := f.tasks.Claim(ctx)
	if !ok || err != nil || next.WorkflowID != "next" {
		t.Fatalf("the queue must go on while a task waits: %v %v %+v", ok, err, next)
	}
}

func TestSuspendRefusesATaskThatIsNotRunning(t *testing.T) {
	f := newFixture(t)
	pending := f.create(t, "x")
	if _, err := f.tasks.Suspend(context.Background(), pending.ID, runSnapshot, askPayload); !errors.Is(err, tasks.ErrNotRunning) {
		t.Errorf("err = %v", err)
	}
}

func TestResumeQueuesAWaitingTaskAgainWithTheAnswerAndClaimKeepsTheFirstStart(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	running := claimed(t, f, "learn")
	if _, err := f.tasks.Suspend(ctx, running.ID, runSnapshot, askPayload); err != nil {
		t.Fatal(err)
	}
	firstStart := running.StartedAt

	queued, err := f.tasks.Resume(ctx, running.ID, json.RawMessage(`"Arabic"`))
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != tasks.StatusPending || string(queued.ResumeMessage) != `"Arabic"` {
		t.Errorf("queued = %+v", queued)
	}

	time.Sleep(5 * time.Millisecond)
	again, ok, err := f.tasks.Claim(ctx)
	if !ok || err != nil || again.ID != running.ID {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if !again.StartedAt.Equal(*firstStart) {
		t.Errorf("a resumed task keeps its first start: %v then %v", firstStart, again.StartedAt)
	}
	if string(again.ResumeMessage) != `"Arabic"` || !jsonEqual(again.Run, runSnapshot) {
		t.Errorf("the worker needs the run and the answer: %+v", again)
	}
}

func TestResumeRefusesAnythingButAWaitingTask(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pending := f.create(t, "x")
	if _, err := f.tasks.Resume(ctx, pending.ID, json.RawMessage(`"a"`)); !errors.Is(err, tasks.ErrNotWaiting) {
		t.Errorf("pending: %v", err)
	}
	running := claimed(t, f, "y")
	if _, err := f.tasks.Resume(ctx, running.ID, json.RawMessage(`"a"`)); !errors.Is(err, tasks.ErrNotWaiting) {
		t.Errorf("running: %v", err)
	}
	for _, id := range []string{"11111111-1111-1111-1111-111111111111", "not-a-uuid"} {
		if _, err := f.tasks.Resume(ctx, id, json.RawMessage(`"a"`)); !errors.Is(err, tasks.ErrNotFound) {
			t.Errorf("%s: %v", id, err)
		}
	}
}

func TestAnsweringTwiceBeforeTheWorkerPicksItUpIsRefused(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	running := claimed(t, f, "learn")
	f.tasks.Suspend(ctx, running.ID, runSnapshot, askPayload)
	if _, err := f.tasks.Resume(ctx, running.ID, json.RawMessage(`"first"`)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tasks.Resume(ctx, running.ID, json.RawMessage(`"second"`)); !errors.Is(err, tasks.ErrNotWaiting) {
		t.Errorf("err = %v", err)
	}
	got, _ := f.tasks.Get(ctx, running.ID)
	if string(got.ResumeMessage) != `"first"` {
		t.Errorf("the first answer must stand: %s", got.ResumeMessage)
	}
}

func TestFinishKeepsTheFinalRunAndClearsTheQuestionAndAnswer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	running := claimed(t, f, "learn")
	f.tasks.Suspend(ctx, running.ID, runSnapshot, askPayload)
	f.tasks.Resume(ctx, running.ID, json.RawMessage(`"a"`))
	again, _, _ := f.tasks.Claim(ctx)
	final := json.RawMessage(`{"run_id":"r","state_name":"done","state":{"n":2}}`)
	done, err := f.tasks.Finish(ctx, again.ID, json.RawMessage(`{"state":{"n":2}}`), "", final)
	if err != nil {
		t.Fatal(err)
	}
	if done.Ask != nil || done.ResumeMessage != nil || !jsonEqual(done.Run, final) {
		t.Errorf("done = %+v", done)
	}
}

func TestAFailedRunKeepsItsSnapshotToo(t *testing.T) {
	f := newFixture(t)
	running := claimed(t, f, "learn")
	done, err := f.tasks.Finish(context.Background(), running.ID, nil, "retries_exhausted: bad JSON", runSnapshot)
	if err != nil || done.Status != tasks.StatusFailed || !jsonEqual(done.Run, runSnapshot) {
		t.Errorf("%+v %v", done, err)
	}
}

func TestFinishWithoutARunLeavesTheEarlierOneAlone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	running := claimed(t, f, "learn")
	f.tasks.Suspend(ctx, running.ID, runSnapshot, askPayload)
	f.tasks.Resume(ctx, running.ID, json.RawMessage(`"a"`))
	again, _, _ := f.tasks.Claim(ctx)
	done, _ := f.tasks.Finish(ctx, again.ID, nil, "boom", nil)
	if !jsonEqual(done.Run, runSnapshot) {
		t.Errorf("run = %s", done.Run)
	}
}

func TestTheListLeavesTheRunOutButShowsTheQuestion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	running := claimed(t, f, "learn")
	f.tasks.Suspend(ctx, running.ID, runSnapshot, askPayload)
	page, err := f.tasks.List(ctx, tasks.ListOptions{Status: tasks.StatusWaiting})
	if err != nil || page.Total != 1 {
		t.Fatalf("%+v %v", page, err)
	}
	if page.Tasks[0].Run != nil || !jsonEqual(page.Tasks[0].Ask, askPayload) {
		t.Errorf("task = %+v", page.Tasks[0])
	}
	got, _ := f.tasks.Get(ctx, running.ID)
	if got.Run == nil {
		t.Errorf("Get must return the run")
	}
}

func TestWaitingIsAValidStatusFilter(t *testing.T) {
	f := newFixture(t)
	if _, err := f.tasks.List(context.Background(), tasks.ListOptions{Status: "waiting"}); err != nil {
		t.Error(err)
	}
}

func TestRecoverCrashedLeavesWaitingTasksAlone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	running := claimed(t, f, "learn")
	f.tasks.Suspend(ctx, running.ID, runSnapshot, askPayload)
	n, err := f.tasks.RecoverCrashed(ctx)
	if err != nil || n != 0 {
		t.Errorf("n=%d err=%v: a waiting task survives a restart", n, err)
	}
	got, _ := f.tasks.Get(ctx, running.ID)
	if got.Status != tasks.StatusWaiting {
		t.Errorf("status = %s", got.Status)
	}
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return bytes.Equal(ax, by)
}

// ── the worker ───────────────────────────────────────────────────────────────

type outcomeRunner func(t tasks.Task) (tasks.Outcome, error)

func (r outcomeRunner) Run(_ context.Context, t tasks.Task) (tasks.Outcome, error) { return r(t) }

func TestTheWorkerParksATaskWhoseRunWaitsAndFinishesItAfterTheAnswer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.create(t, "learn")
	calls := 0
	var seen []tasks.Task
	w, obs, _ := newWorker(f, outcomeRunner(func(t tasks.Task) (tasks.Outcome, error) {
		calls++
		seen = append(seen, t)
		if calls == 1 {
			return tasks.Outcome{Waiting: true, Run: runSnapshot, Ask: askPayload}, nil
		}
		return tasks.Outcome{Result: json.RawMessage(`{"state":{}}`), Run: runSnapshot}, nil
	}))

	if ran, err := w.RunOnce(ctx); !ran || err != nil {
		t.Fatalf("first: %v %v", ran, err)
	}
	got, _ := f.tasks.Get(ctx, task.ID)
	if got.Status != tasks.StatusWaiting || !jsonEqual(got.Ask, askPayload) || got.FinishedAt != nil {
		t.Fatalf("after the first run: %+v", got)
	}
	if len(obs.got) != 0 {
		t.Errorf("a waiting task is not a finished one: %+v", obs.got)
	}
	if ran, _ := w.RunOnce(ctx); ran {
		t.Fatal("nothing is pending while the task waits")
	}

	if _, err := f.tasks.Resume(ctx, task.ID, json.RawMessage(`"Arabic"`)); err != nil {
		t.Fatal(err)
	}
	if ran, err := w.RunOnce(ctx); !ran || err != nil {
		t.Fatalf("second: %v %v", ran, err)
	}
	if string(seen[1].ResumeMessage) != `"Arabic"` || seen[1].Run == nil {
		t.Errorf("the runner must get the run and the answer: %+v", seen[1])
	}
	done, _ := f.tasks.Get(ctx, task.ID)
	if done.Status != tasks.StatusSucceeded || len(obs.got) != 1 || obs.got[0].status != tasks.StatusSucceeded {
		t.Errorf("done = %+v, observed %+v", done, obs.got)
	}
}

func TestAFailedRunKeepsItsSnapshotAndTheErrorIsTheTasks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.create(t, "learn")
	w, _, _ := newWorker(f, outcomeRunner(func(tasks.Task) (tasks.Outcome, error) {
		return tasks.Outcome{Run: runSnapshot}, errors.New("retries_exhausted: the model kept sending invalid JSON")
	}))
	w.RunOnce(ctx)
	got, _ := f.tasks.Get(ctx, task.ID)
	if got.Status != tasks.StatusFailed || got.Error == nil || !strings.Contains(*got.Error, "retries_exhausted") || !jsonEqual(got.Run, runSnapshot) {
		t.Errorf("got = %+v", got)
	}
}

func TestAWaitingOutcomeWithAnErrorIsAFailure(t *testing.T) {
	f := newFixture(t)
	task := f.create(t, "learn")
	w, _, _ := newWorker(f, outcomeRunner(func(tasks.Task) (tasks.Outcome, error) {
		return tasks.Outcome{Waiting: true, Run: runSnapshot, Ask: askPayload}, errors.New("broke while asking")
	}))
	w.RunOnce(context.Background())
	got, _ := f.tasks.Get(context.Background(), task.ID)
	if got.Status != tasks.StatusFailed {
		t.Errorf("an error wins over waiting: %s", got.Status)
	}
}

func TestAFailedParkIsReportedNotSwallowed(t *testing.T) {
	logs := &bytes.Buffer{}
	w := &tasks.Worker{Queue: &parkFails{}, Runner: outcomeRunner(func(tasks.Task) (tasks.Outcome, error) {
		return tasks.Outcome{Waiting: true, Run: runSnapshot, Ask: askPayload}, nil
	}), Log: slog.New(slog.NewJSONHandler(logs, nil))}
	ran, err := w.RunOnce(context.Background())
	if !ran || err == nil || !strings.Contains(err.Error(), "waits for the user") || !strings.Contains(err.Error(), "database is gone") {
		t.Errorf("ran=%v err=%v", ran, err)
	}
}

type parkFails struct{ fakeQueue }

func (*parkFails) Claim(context.Context) (tasks.Task, bool, error) {
	return tasks.Task{ID: "t", WorkflowID: "w"}, true, nil
}
func (*parkFails) Suspend(context.Context, string, json.RawMessage, json.RawMessage) (tasks.Task, error) {
	return tasks.Task{}, errors.New("database is gone")
}
