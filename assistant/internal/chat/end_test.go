package chat_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"assistant/internal/chat"
	"assistant/internal/tasks"
)

// fakeCanceller is the shared cancel routine over the real task store, with a
// worker that is told to stop on demand.
type fakeCanceller struct {
	store     *tasks.Store
	stops     bool
	err       error
	cancelled []string
}

type stopper bool

func (s stopper) Cancel(string) bool { return bool(s) }

func (f *fakeCanceller) Cancel(ctx context.Context, taskID string) (tasks.Task, tasks.CancelOutcome, error) {
	f.cancelled = append(f.cancelled, taskID)
	if f.err != nil {
		return tasks.Task{}, 0, f.err
	}
	return tasks.CancelOrStop(ctx, f.store, stopper(f.stops), taskID)
}

const (
	endedNow    = "assistant/info: Ended the current run. Send a message to start again."
	endStopping = "assistant/info: Stopping the current run; it ends in a moment. Send a message to start again."
	endNothing  = "assistant/info: Nothing is running."
)

// lastText is the conversation's last message as role/kind: text.
func lastText(t *testing.T, r *rig, c chat.Conversation) string {
	t.Helper()
	got := r.texts(t, c)
	return got[len(got)-1]
}

func TestEndCancelsAWaitingRunAndTheNextMessageStartsANewOne(t *testing.T) {
	r := newRig(t)
	r.svc.Canceller = &fakeCanceller{store: r.tasks.store}
	ctx := context.Background()
	c := r.conversation(t, "hello")
	r.svc.Send(ctx, c.ID, "first")
	old := *mustView(t, r, c).TaskID
	r.tasks.setStatus(old, tasks.StatusWaiting)

	if _, err := r.svc.Send(ctx, c.ID, "/end"); err != nil {
		t.Fatal(err)
	}
	if got := lastText(t, r, c); got != endedNow {
		t.Errorf("reply = %q", got)
	}
	task, err := r.tasks.store.Get(ctx, old)
	if err != nil || task.Status != tasks.StatusCancelled {
		t.Fatalf("task = %+v err=%v", task, err)
	}
	if mustView(t, r, c).TaskID == nil || *mustView(t, r, c).TaskID != old {
		t.Errorf("/end must leave the conversation's task_id alone")
	}

	if _, err := r.svc.Send(ctx, c.ID, "again"); err != nil {
		t.Fatalf("the conversation stays open: %v", err)
	}
	now := *mustView(t, r, c).TaskID
	if len(r.tasks.created) != 2 || now == old {
		t.Errorf("%d tasks created, run %s -> %s", len(r.tasks.created), old, now)
	}
}

func TestEndOfAPendingRunCancelsItToo(t *testing.T) {
	r := newRig(t)
	r.svc.Canceller = &fakeCanceller{store: r.tasks.store}
	ctx := context.Background()
	c := r.conversation(t, "hello")
	r.svc.Send(ctx, c.ID, "first")
	r.svc.Send(ctx, c.ID, "/end")
	if got := lastText(t, r, c); got != endedNow {
		t.Errorf("reply = %q", got)
	}
}

func TestEndOfARunningRunSaysItIsStopping(t *testing.T) {
	r := newRig(t)
	canceller := &fakeCanceller{store: r.tasks.store, stops: true}
	r.svc.Canceller = canceller
	ctx := context.Background()
	c := r.conversation(t, "hello")
	r.svc.Send(ctx, c.ID, "first")
	id := *mustView(t, r, c).TaskID
	r.tasks.setStatus(id, tasks.StatusRunning)

	if _, err := r.svc.Send(ctx, c.ID, "/end"); err != nil {
		t.Fatal(err)
	}
	if got := lastText(t, r, c); got != endStopping {
		t.Errorf("reply = %q", got)
	}
	if len(canceller.cancelled) != 1 || canceller.cancelled[0] != id {
		t.Errorf("cancelled %v", canceller.cancelled)
	}
}

func TestEndWithNoRunOrAFinishedOrARemovedRunSaysNothingIsRunning(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	canceller := &fakeCanceller{store: r.tasks.store, stops: true}
	r.svc.Canceller = canceller

	none := r.conversation(t, "hello")
	r.svc.Send(ctx, none.ID, "/end")
	if got := lastText(t, r, none); got != endNothing {
		t.Errorf("no task: %q", got)
	}

	for _, status := range []string{tasks.StatusSucceeded, tasks.StatusFailed, tasks.StatusCancelled} {
		c := r.conversation(t, "hello")
		r.svc.Send(ctx, c.ID, "first")
		r.tasks.setStatus(*mustView(t, r, c).TaskID, status)
		r.svc.Send(ctx, c.ID, "/end")
		if got := lastText(t, r, c); got != endNothing {
			t.Errorf("%s: %q", status, got)
		}
	}

	gone := r.conversation(t, "hello")
	r.svc.Send(ctx, gone.ID, "first")
	if err := r.tasks.store.Remove(ctx, *mustView(t, r, gone).TaskID); err != nil {
		t.Fatal(err)
	}
	r.svc.Send(ctx, gone.ID, "/end")
	if got := lastText(t, r, gone); got != endNothing {
		t.Errorf("removed: %q", got)
	}
	if len(canceller.cancelled) != 0 {
		t.Errorf("nothing to cancel, yet cancelled %v", canceller.cancelled)
	}
}

func TestEndWhenTheRunFinishedMeanwhileSaysNothingIsRunning(t *testing.T) {
	for _, race := range []error{tasks.ErrNotCancellable, tasks.ErrNotFound} {
		r := newRig(t)
		r.svc.Canceller = &fakeCanceller{store: r.tasks.store, err: race}
		ctx := context.Background()
		c := r.conversation(t, "hello")
		r.svc.Send(ctx, c.ID, "first")
		if _, err := r.svc.Send(ctx, c.ID, "/end"); err != nil {
			t.Fatalf("%v: %v", race, err)
		}
		if got := lastText(t, r, c); got != endNothing {
			t.Errorf("%v: %q", race, got)
		}
	}
}

func TestEndThatFailsIsSaidInTheConversationAndReturned(t *testing.T) {
	r := newRig(t)
	boom := errors.New("database is down")
	r.svc.Canceller = &fakeCanceller{store: r.tasks.store, err: boom}
	ctx := context.Background()
	c := r.conversation(t, "hello")
	r.svc.Send(ctx, c.ID, "first")
	_, err := r.svc.Send(ctx, c.ID, "/end")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	got := lastText(t, r, c)
	if !strings.HasPrefix(got, "assistant/error: ⚠ ") || !strings.Contains(got, "database is down") {
		t.Errorf("reply = %q", got)
	}
}

func TestEndWithoutACancellerSaysSoAndDoesNotPanic(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	c := r.conversation(t, "hello")
	r.svc.Send(ctx, c.ID, "first")
	if _, err := r.svc.Send(ctx, c.ID, "/end"); err != nil {
		t.Fatal(err)
	}
	if got := lastText(t, r, c); got != "assistant/info: Ending a run is not available here." {
		t.Errorf("reply = %q", got)
	}
}

func TestHelpListsEnd(t *testing.T) {
	r := newRig(t)
	c := r.conversation(t, "hello")
	r.svc.Send(context.Background(), c.ID, "/help")
	if got := r.texts(t, c)[1]; !strings.Contains(got, "/end: end the current run") {
		t.Errorf("help lacks /end:\n%s", got)
	}
}
