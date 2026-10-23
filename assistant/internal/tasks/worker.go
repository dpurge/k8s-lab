package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"assistant/internal/callctx"
	"assistant/internal/workflow"
)

// Queue is what the worker needs from the task store.
type Queue interface {
	Claim(ctx context.Context) (Task, bool, error)
	Finish(ctx context.Context, id string, result json.RawMessage, message string, run json.RawMessage) (Task, error)
	Suspend(ctx context.Context, id string, run, ask json.RawMessage) (Task, error)
	MarkCancelled(ctx context.Context, id string, run json.RawMessage) (Task, error)
}

// Outcome is how a run of a task ended, short of failing.
type Outcome struct {
	// Result is the task's result, a JSON value, when it succeeded.
	Result json.RawMessage
	// Run is the workflow run's JSON snapshot, kept on the task. A runner that
	// fails a task may still return it, so the failed run can be inspected.
	Run json.RawMessage
	// Waiting means the run asked the user something (Ask) and is parked until
	// an answer arrives; the task is not finished.
	Waiting bool
	Ask     json.RawMessage
}

// Runner runs the workflow a task names. An error fails the task; its text is
// shown to the user in the admin interface.
type Runner interface {
	Run(ctx context.Context, t Task) (Outcome, error)
}

// Notifier is told when a task waits for the user or ends, with the task as
// stored. The conversation layer uses it to speak for the assistant.
type Notifier interface {
	TaskUpdated(ctx context.Context, t Task)
}

// Observer receives a finished task's outcome for metrics.
type Observer interface {
	ObserveTask(source, workflow, status string, queued, executed time.Duration)
}

// DefaultPoll is how long an idle worker waits before looking for work again.
const DefaultPoll = time.Second

// finishTimeout bounds recording a result after the run's context ended, so a
// shutdown still records how the task that was running ended.
const finishTimeout = 10 * time.Second

// Worker takes tasks from the queue one at a time and runs them.
type Worker struct {
	Queue    Queue
	Runner   Runner
	Observer Observer // optional
	Notifier Notifier // optional
	Log      *slog.Logger
	// cancelMu guards the task being run, so Cancel can stop it.
	cancelMu  sync.Mutex
	runningID string
	stop      context.CancelFunc
	cancelled bool
	// Poll is the wait between looks at an empty queue; zero means DefaultPoll.
	Poll time.Duration
}

// RunOnce claims and runs at most one task. It reports whether it ran one. A
// problem with the queue itself is an error; a task that fails is not.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	task, ok, err := w.Queue.Claim(ctx)
	if err != nil || !ok {
		return false, err
	}
	w.Log.Info("task started", "task_id", task.ID, "workflow", task.WorkflowID, "source", task.Source)

	runCtx, stop := context.WithCancel(callctx.With(ctx, callctx.Info{TaskID: task.ID, UserID: task.UserID}))
	w.cancelMu.Lock()
	w.runningID, w.stop, w.cancelled = task.ID, stop, false
	w.cancelMu.Unlock()
	outcome, runErr := w.run(runCtx, task)
	w.cancelMu.Lock()
	wasCancelled := w.cancelled
	w.runningID, w.stop, w.cancelled = "", nil, false
	w.cancelMu.Unlock()
	stop()

	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if wasCancelled {
		done, err := w.Queue.MarkCancelled(finishCtx, task.ID, outcome.Run)
		if err != nil {
			return true, fmt.Errorf("record the cancellation of task %s: %w", task.ID, err)
		}
		queued := done.QueueTime(time.Now())
		executed, _ := done.ExecutionTime(time.Now())
		w.Log.Info("task cancelled", "task_id", done.ID, "workflow", done.WorkflowID, "duration_ms", executed.Milliseconds())
		w.notify(finishCtx, done)
		if w.Observer != nil {
			w.Observer.ObserveTask(done.Source, done.WorkflowID, done.Status, queued, executed)
		}
		return true, nil
	}
	if runErr == nil && outcome.Waiting {
		parked, err := w.Queue.Suspend(finishCtx, task.ID, outcome.Run, outcome.Ask)
		if err != nil {
			return true, fmt.Errorf("park task %s while it waits for the user: %w", task.ID, err)
		}
		w.Log.Info("task waiting", "task_id", parked.ID, "workflow", parked.WorkflowID)
		w.notify(finishCtx, parked)
		return true, nil
	}
	message := ""
	if runErr != nil {
		message = runErr.Error()
		if message == "" {
			message = "workflow failed without a message"
		}
	}
	done, err := w.Queue.Finish(finishCtx, task.ID, outcome.Result, message, outcome.Run)
	if err != nil {
		return true, fmt.Errorf("record the outcome of task %s: %w", task.ID, err)
	}

	executed, _ := done.ExecutionTime(time.Now())
	queued := done.QueueTime(time.Now())
	// What is logged, and what is kept out of it, is finishedLogAttrs's concern.
	w.Log.Info("task finished", finishedLogAttrs(done, executed)...)
	if w.Observer != nil {
		w.Observer.ObserveTask(done.Source, done.WorkflowID, done.Status, queued, executed)
	}
	w.notify(finishCtx, done)
	return true, nil
}

// maxFailureLogBytes bounds the failure message in the log.
const maxFailureLogBytes = 600

// finishedLogAttrs are the attributes of the "task finished" line. Ids, names,
// the outcome and timings are always logged: a payload or result can hold
// anything the user sent or a workflow produced, so those never are. A task
// that did not succeed also says why, so the log explains a failure without a
// look at the task: failure_reason is the workflow's reason code and
// failure_message its text, cut short. Only a failure that carries a known
// reason code says why: its text comes from the workflow engine. Any other
// error can quote whatever a payload or a result held, so it adds nothing. The
// engine's text can quote the model's reply (see workflow.RedactExcerpt), so
// that quote is removed first.
func finishedLogAttrs(done Task, executed time.Duration) []any {
	attrs := []any{"task_id", done.ID, "workflow", done.WorkflowID, "outcome", done.Status, "duration_ms", executed.Milliseconds()}
	if done.Status == StatusSucceeded || done.Error == nil {
		return attrs
	}
	reason, message := splitFailure(*done.Error)
	if reason == "" {
		return attrs
	}
	return append(attrs, "failure_reason", string(reason), "failure_message", truncateBytes(workflow.RedactExcerpt(message), maxFailureLogBytes))
}

// splitFailure separates the workflow's reason code from the message. The
// workflow runner reaches the worker as an error whose text is "reason: message"
// (internal/workflows), so the reason is read from the text, and only a known
// reason code counts: any other error has no reason and keeps its whole text.
func splitFailure(text string) (workflow.FailReason, string) {
	code, message, found := strings.Cut(text, ": ")
	if reason := workflow.FailReason(code); found && reason.Known() {
		return reason, message
	}
	return "", text
}

// truncateBytes cuts text to at most max bytes without splitting a character.
func truncateBytes(text string, max int) string {
	if len(text) <= max {
		return text
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// notify tells the notifier, if there is one, how the task stands.
func (w *Worker) notify(ctx context.Context, t Task) {
	if w.Notifier != nil {
		w.Notifier.TaskUpdated(ctx, t)
	}
}

// Cancel stops the task being run, if it is the one with this id, and reports
// whether it was. The worker then records it as cancelled, however the run
// ended. A task that is not being run here is not its concern.
func (w *Worker) Cancel(id string) bool {
	w.cancelMu.Lock()
	defer w.cancelMu.Unlock()
	if w.runningID != id || w.stop == nil {
		return false
	}
	w.cancelled = true
	w.stop()
	return true
}

// run calls the runner and turns a panic into a failure, so one broken
// workflow cannot stop the worker.
func (w *Worker) run(ctx context.Context, t Task) (outcome Outcome, err error) {
	defer func() {
		if r := recover(); r != nil {
			w.Log.Error("workflow panicked", "task_id", t.ID, "workflow", t.WorkflowID, "panic", fmt.Sprint(r))
			outcome, err = Outcome{}, fmt.Errorf("workflow panicked: %v", r)
		}
	}()
	return w.Runner.Run(ctx, t)
}

// Run works through the queue until ctx ends: tasks back to back while there
// are some, then a pause between looks. A task being run when ctx ends is
// finished and recorded first.
func (w *Worker) Run(ctx context.Context) {
	poll := w.Poll
	if poll <= 0 {
		poll = DefaultPoll
	}
	for ctx.Err() == nil {
		ran, err := w.RunOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			w.Log.Error("worker", "error", err)
		}
		if ran && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(poll):
		}
	}
}
