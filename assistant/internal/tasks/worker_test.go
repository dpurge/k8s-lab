package tasks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"assistant/internal/callctx"
	"assistant/internal/metrics"
	"assistant/internal/tasks"
)

// scriptedRunner runs the task with the function registered for its workflow.
type scriptedRunner map[string]func(ctx context.Context, t tasks.Task) (json.RawMessage, error)

func (r scriptedRunner) Run(ctx context.Context, t tasks.Task) (tasks.Outcome, error) {
	fn, ok := r[t.WorkflowID]
	if !ok {
		return tasks.Outcome{}, errors.New("unknown workflow: " + t.WorkflowID)
	}
	result, err := fn(ctx, t)
	return tasks.Outcome{Result: result}, err
}

type observation struct {
	source, workflow, status string
	queued, executed         time.Duration
}

type recordingObserver struct {
	mu  sync.Mutex
	got []observation
}

func (o *recordingObserver) ObserveTask(source, workflow, status string, queued, executed time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.got = append(o.got, observation{source, workflow, status, queued, executed})
}

func newWorker(f fixture, runner tasks.Runner) (*tasks.Worker, *recordingObserver, *bytes.Buffer) {
	logs := &bytes.Buffer{}
	obs := &recordingObserver{}
	return &tasks.Worker{
		Queue: f.tasks, Runner: runner, Observer: obs,
		Log: slog.New(slog.NewJSONHandler(logs, nil)), Poll: 5 * time.Millisecond,
	}, obs, logs
}

func TestRunOnceRunsTheOldestTaskAndRecordsASuccess(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	older, newer := f.create(t, "echo"), f.create(t, "echo")
	f.setCreatedAt(t, older.ID, time.Now().Add(-time.Hour))
	w, _, _ := newWorker(f, scriptedRunner{"echo": func(_ context.Context, t tasks.Task) (json.RawMessage, error) {
		return json.RawMessage(`{"ran":"` + t.ID + `"}`), nil
	}})

	ran, err := w.RunOnce(ctx)
	if !ran || err != nil {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	got, _ := f.tasks.Get(ctx, older.ID)
	if got.Status != tasks.StatusSucceeded || !strings.Contains(string(got.Result), older.ID) || got.Error != nil || got.StartedAt == nil || got.FinishedAt == nil {
		t.Errorf("older = %+v", got)
	}
	if other, _ := f.tasks.Get(ctx, newer.ID); other.Status != tasks.StatusPending {
		t.Errorf("one call runs one task; the newer is %s", other.Status)
	}
}

func TestRunOnceRecordsAFailure(t *testing.T) {
	f := newFixture(t)
	task := f.create(t, "breaks")
	w, _, _ := newWorker(f, scriptedRunner{"breaks": func(context.Context, tasks.Task) (json.RawMessage, error) {
		return json.RawMessage(`{"partial":true}`), errors.New("the index is offline")
	}})
	if ran, err := w.RunOnce(context.Background()); !ran || err != nil {
		t.Fatalf("a failing task is not a worker error: ran=%v err=%v", ran, err)
	}
	got, _ := f.tasks.Get(context.Background(), task.ID)
	if got.Status != tasks.StatusFailed || got.Error == nil || *got.Error != "the index is offline" || !isNullRaw(got.Result) {
		t.Errorf("task = %+v", got)
	}
}

func isNullRaw(r json.RawMessage) bool { return len(r) == 0 || string(r) == "null" }

func TestAnErrorWithoutAMessageStillFailsTheTask(t *testing.T) {
	f := newFixture(t)
	task := f.create(t, "silent")
	w, _, _ := newWorker(f, scriptedRunner{"silent": func(context.Context, tasks.Task) (json.RawMessage, error) {
		return nil, errors.New("")
	}})
	w.RunOnce(context.Background())
	got, _ := f.tasks.Get(context.Background(), task.ID)
	if got.Status != tasks.StatusFailed || got.Error == nil || *got.Error == "" {
		t.Errorf("task = %+v", got)
	}
}

func TestUnknownWorkflowFailsTheTask(t *testing.T) {
	f := newFixture(t)
	task := f.create(t, "no-such-workflow")
	w, _, _ := newWorker(f, scriptedRunner{})
	w.RunOnce(context.Background())
	got, _ := f.tasks.Get(context.Background(), task.ID)
	if got.Status != tasks.StatusFailed || *got.Error != "unknown workflow: no-such-workflow" {
		t.Errorf("task = %+v", got)
	}
}

func TestAPanickingWorkflowFailsTheTaskAndTheWorkerSurvives(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	bad := f.create(t, "panics")
	f.setCreatedAt(t, bad.ID, time.Now().Add(-time.Hour))
	good := f.create(t, "fine")
	w, _, logs := newWorker(f, scriptedRunner{
		"panics": func(context.Context, tasks.Task) (json.RawMessage, error) { panic("kaboom") },
		"fine":   func(context.Context, tasks.Task) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
	})
	if ran, err := w.RunOnce(ctx); !ran || err != nil {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	got, _ := f.tasks.Get(ctx, bad.ID)
	if got.Status != tasks.StatusFailed || !strings.Contains(*got.Error, "workflow panicked: kaboom") {
		t.Errorf("panicked task = %+v", got)
	}
	if !strings.Contains(logs.String(), `"panic":"kaboom"`) {
		t.Errorf("the panic must be logged")
	}
	if ran, err := w.RunOnce(ctx); !ran || err != nil {
		t.Fatalf("the worker must go on: ran=%v err=%v", ran, err)
	}
	if after, _ := f.tasks.Get(ctx, good.ID); after.Status != tasks.StatusSucceeded {
		t.Errorf("next task = %+v", after)
	}
}

func TestRunOnceWithNothingToDo(t *testing.T) {
	f := newFixture(t)
	w, obs, _ := newWorker(f, scriptedRunner{})
	if ran, err := w.RunOnce(context.Background()); ran || err != nil {
		t.Errorf("ran=%v err=%v", ran, err)
	}
	if len(obs.got) != 0 {
		t.Errorf("nothing ran, nothing is observed")
	}
}

func TestRunOnceDoesNotStartATaskWhileAnotherRuns(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.create(t, "a")
	f.create(t, "b")
	stuck, _, _ := f.tasks.Claim(ctx) // something else holds the one running slot
	w, _, _ := newWorker(f, scriptedRunner{"a": nil, "b": nil})
	if ran, err := w.RunOnce(ctx); ran || err != nil {
		t.Errorf("ran=%v err=%v", ran, err)
	}
	if got, _ := f.tasks.Get(ctx, stuck.ID); got.Status != tasks.StatusRunning {
		t.Errorf("the running task must be untouched: %+v", got)
	}
}

func TestTheOutcomeIsObservedForMetrics(t *testing.T) {
	f := newFixture(t)
	ok := f.create(t, "fine")
	f.setCreatedAt(t, ok.ID, time.Now().Add(-2*time.Hour))
	f.create(t, "breaks")
	w, obs, _ := newWorker(f, scriptedRunner{
		"fine":   func(context.Context, tasks.Task) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
		"breaks": func(context.Context, tasks.Task) (json.RawMessage, error) { return nil, errors.New("no") },
	})
	w.RunOnce(context.Background())
	w.RunOnce(context.Background())
	if len(obs.got) != 2 {
		t.Fatalf("observations = %+v", obs.got)
	}
	first, second := obs.got[0], obs.got[1]
	if first.source != "api" || first.workflow != "fine" || first.status != "succeeded" {
		t.Errorf("first = %+v", first)
	}
	if first.queued < 2*time.Hour-time.Minute || first.queued > 2*time.Hour+time.Minute {
		t.Errorf("queue time = %v, want about 2h", first.queued)
	}
	if first.executed < 0 || first.executed > 10*time.Second {
		t.Errorf("execution time = %v", first.executed)
	}
	if second.workflow != "breaks" || second.status != "failed" {
		t.Errorf("second = %+v", second)
	}
}

func TestTheMetricsEndpointReflectsFinishedTasks(t *testing.T) {
	f := newFixture(t)
	f.create(t, "fine")
	f.create(t, "fine")
	f.create(t, "breaks")
	m := metrics.New()
	w, _, _ := newWorker(f, scriptedRunner{
		"fine":   func(context.Context, tasks.Task) (json.RawMessage, error) { return nil, nil },
		"breaks": func(context.Context, tasks.Task) (json.RawMessage, error) { return nil, errors.New("no") },
	})
	w.Observer = m
	for i := 0; i < 3; i++ {
		w.RunOnce(context.Background())
	}
	text := scrapeMetrics(t, m)
	for _, want := range []string{
		`assistant_tasks_total{source="api",status="succeeded",workflow="fine"} 2`,
		`assistant_tasks_total{source="api",status="failed",workflow="breaks"} 1`,
		`assistant_task_queue_seconds_count{workflow="fine"} 2`,
		`assistant_task_execution_seconds_count{workflow="breaks"} 1`,
	} {
		if !strings.Contains(text, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestLogsCarryIdWorkflowOutcomeAndDurationButNeverPayloadOrResult(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	okTask, _ := f.tasks.Create(ctx, tasks.New{UserID: f.user.ID, WorkflowID: "fine", Source: tasks.SourceAPI, Payload: json.RawMessage(`{"secret":"PAYLOAD-SECRET"}`)})
	badTask, _ := f.tasks.Create(ctx, tasks.New{UserID: f.user.ID, WorkflowID: "breaks", Source: tasks.SourceAPI, Payload: json.RawMessage(`{"secret":"PAYLOAD-SECRET"}`)})
	f.setCreatedAt(t, okTask.ID, time.Now().Add(-time.Hour))
	w, _, logs := newWorker(f, scriptedRunner{
		"fine": func(context.Context, tasks.Task) (json.RawMessage, error) {
			return json.RawMessage(`{"out":"RESULT-SECRET"}`), nil
		},
		"breaks": func(context.Context, tasks.Task) (json.RawMessage, error) {
			return nil, errors.New("failed on PAYLOAD-SECRET")
		},
	})
	w.RunOnce(ctx)
	w.RunOnce(ctx)

	for _, secret := range []string{"PAYLOAD-SECRET", "RESULT-SECRET"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("%s leaked into the log:\n%s", secret, logs)
		}
	}
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line is not JSON: %s", l)
		}
		lines = append(lines, m)
	}
	if len(lines) != 4 {
		t.Fatalf("want a start and a finish line per task, got %d:\n%s", len(lines), logs)
	}
	start, finish := lines[0], lines[1]
	if start["msg"] != "task started" || start["task_id"] != okTask.ID || start["workflow"] != "fine" {
		t.Errorf("start = %v", start)
	}
	if finish["msg"] != "task finished" || finish["task_id"] != okTask.ID || finish["workflow"] != "fine" || finish["outcome"] != "succeeded" {
		t.Errorf("finish = %v", finish)
	}
	if _, ok := finish["duration_ms"].(float64); !ok {
		t.Errorf("finish has no duration_ms: %v", finish)
	}
	if lines[3]["outcome"] != "failed" || lines[3]["task_id"] != badTask.ID {
		t.Errorf("failed finish = %v", lines[3])
	}
}

func TestShutdownStillRecordsTheRunningTask(t *testing.T) {
	f := newFixture(t)
	task := f.create(t, "slow")
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	w, _, _ := newWorker(f, scriptedRunner{"slow": func(ctx context.Context, _ tasks.Task) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	done := make(chan struct{})
	go func() {
		w.RunOnce(ctx)
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunOnce did not return after its context ended")
	}
	got, _ := f.tasks.Get(context.Background(), task.ID)
	if got.Status != tasks.StatusFailed || got.Error == nil || !strings.Contains(*got.Error, "context canceled") {
		t.Errorf("an interrupted task must be recorded, not left running: %+v", got)
	}
}

// ── the loop, without a database ─────────────────────────────────────────────

type fakeQueue struct {
	mu       sync.Mutex
	pending  []tasks.Task
	claims   int
	finished []string
	claimErr error
}

func (q *fakeQueue) Claim(context.Context) (tasks.Task, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.claims++
	if q.claimErr != nil {
		return tasks.Task{}, false, q.claimErr
	}
	if len(q.pending) == 0 {
		return tasks.Task{}, false, nil
	}
	t := q.pending[0]
	q.pending = q.pending[1:]
	return t, true, nil
}

func (q *fakeQueue) MarkCancelled(_ context.Context, id string, _ json.RawMessage) (tasks.Task, error) {
	return tasks.Task{ID: id, Status: tasks.StatusCancelled}, nil
}

func (q *fakeQueue) Suspend(_ context.Context, id string, _, _ json.RawMessage) (tasks.Task, error) {
	return tasks.Task{ID: id, Status: tasks.StatusWaiting}, nil
}

func (q *fakeQueue) Finish(_ context.Context, id string, _ json.RawMessage, message string, _ json.RawMessage) (tasks.Task, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.finished = append(q.finished, id+":"+message)
	return tasks.Task{ID: id, Status: tasks.StatusSucceeded, CreatedAt: time.Now(), StartedAt: ptr(time.Now())}, nil
}

func ptr(t time.Time) *time.Time { return &t }

func (q *fakeQueue) snapshot() (claims int, finished []string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.claims, append([]string(nil), q.finished...)
}

type okRunner struct{}

func (okRunner) Run(context.Context, tasks.Task) (tasks.Outcome, error) { return tasks.Outcome{}, nil }

func TestRunWorksThroughTheQueueBackToBackThenWaits(t *testing.T) {
	q := &fakeQueue{pending: []tasks.Task{{ID: "1"}, {ID: "2"}, {ID: "3"}}}
	w := &tasks.Worker{Queue: q, Runner: okRunner{}, Log: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), Poll: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	deadline := time.After(5 * time.Second)
	for {
		if _, fin := q.snapshot(); len(fin) == 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("three queued tasks should run without waiting for the poll interval")
		case <-time.After(5 * time.Millisecond):
		}
	}
	claims, _ := q.snapshot()
	time.Sleep(30 * time.Millisecond)
	if after, _ := q.snapshot(); after != claims {
		t.Errorf("an idle worker with an hour-long poll must not look again: %d then %d claims", claims, after)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run must return when its context ends, even while waiting")
	}
}

func TestRunPollsAnEmptyQueueAtTheInterval(t *testing.T) {
	q := &fakeQueue{}
	w := &tasks.Worker{Queue: q, Runner: okRunner{}, Log: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), Poll: 20 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Millisecond)
	defer cancel()
	w.Run(ctx)
	claims, _ := q.snapshot()
	if claims < 3 || claims > 8 {
		t.Errorf("%d looks in 110 ms at a 20 ms interval: expected a handful, not a spin", claims)
	}
}

func TestRunSurvivesQueueErrorsWithoutSpinning(t *testing.T) {
	q := &fakeQueue{claimErr: errors.New("database down")}
	logs := &bytes.Buffer{}
	w := &tasks.Worker{Queue: q, Runner: okRunner{}, Log: slog.New(slog.NewJSONHandler(logs, nil)), Poll: 20 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Millisecond)
	defer cancel()
	w.Run(ctx)
	claims, _ := q.snapshot()
	if claims < 2 || claims > 8 {
		t.Errorf("%d attempts: the loop must back off after an error", claims)
	}
	if !strings.Contains(logs.String(), "database down") {
		t.Errorf("the error must be logged")
	}
}

func TestZeroPollMeansTheDefault(t *testing.T) {
	q := &fakeQueue{}
	w := &tasks.Worker{Queue: q, Runner: okRunner{}, Log: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	w.Run(ctx)
	if claims, _ := q.snapshot(); claims != 1 {
		t.Errorf("with the default one-second poll a 60 ms run looks once, not %d times", claims)
	}
}

func TestTheRunnerKnowsWhichTaskAndUserItRunsFor(t *testing.T) {
	f := newFixture(t)
	task := f.create(t, "who")
	var got callctx.Info
	var ok bool
	w, _, _ := newWorker(f, scriptedRunner{"who": func(ctx context.Context, _ tasks.Task) (json.RawMessage, error) {
		got, ok = callctx.From(ctx)
		return json.RawMessage(`{}`), nil
	}})
	if ran, err := w.RunOnce(context.Background()); !ran || err != nil {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	if !ok || got.TaskID != task.ID || got.UserID != f.user.ID {
		t.Errorf("the workflow's context carries %+v (ok=%v), want task %s and user %s", got, ok, task.ID, f.user.ID)
	}
}
