package chat_test

import (
	"context"
	"errors"
	"testing"

	"assistant/internal/chat"
	"assistant/internal/tasks"
)

// conversationWithRun starts a conversation, sends one message so it has a
// task, and puts the task in the given status.
func conversationWithRun(t *testing.T, r *rig, status string) (chat.Conversation, string) {
	t.Helper()
	c := r.conversation(t, "hello")
	if _, err := r.svc.Send(context.Background(), c.ID, "first"); err != nil {
		t.Fatal(err)
	}
	id := *mustView(t, r, c).TaskID
	if status != tasks.StatusPending {
		r.tasks.setStatus(id, status)
	}
	return c, id
}

func assertConversationGone(t *testing.T, r *rig, c chat.Conversation) {
	t.Helper()
	if _, err := r.svc.Store.Get(context.Background(), c.ID); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("the conversation must be gone, Get err = %v", err)
	}
}

func assertTaskStatus(t *testing.T, r *rig, id, want string) {
	t.Helper()
	task, err := r.tasks.store.Get(context.Background(), id)
	if err != nil || task.Status != want {
		t.Fatalf("the task must still exist as %s: %+v err=%v", want, task, err)
	}
}

func TestRemoveCancelsAWaitingOrPendingRunAndKeepsItsRecord(t *testing.T) {
	for _, status := range []string{tasks.StatusWaiting, tasks.StatusPending} {
		r := newRig(t)
		canceller := &fakeCanceller{store: r.tasks.store}
		r.svc.Canceller = canceller
		c, id := conversationWithRun(t, r, status)

		if err := r.svc.Remove(context.Background(), c.ID); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		assertConversationGone(t, r, c)
		assertTaskStatus(t, r, id, tasks.StatusCancelled)
	}
}

func TestRemoveOfARunningRunAsksTheCancellerOnceAndDeletes(t *testing.T) {
	r := newRig(t)
	canceller := &fakeCanceller{store: r.tasks.store, stops: true}
	r.svc.Canceller = canceller
	c, id := conversationWithRun(t, r, tasks.StatusRunning)

	if err := r.svc.Remove(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	assertConversationGone(t, r, c)
	if len(canceller.cancelled) != 1 || canceller.cancelled[0] != id {
		t.Errorf("cancelled %v, want [%s]", canceller.cancelled, id)
	}
	assertTaskStatus(t, r, id, tasks.StatusRunning) // the worker ends it later
}

// A finished run is still offered to the canceller, which answers
// ErrNotCancellable; that is not a failure.
func TestRemoveOfAFinishedRunNeedsNoCancel(t *testing.T) {
	for _, status := range []string{tasks.StatusSucceeded, tasks.StatusFailed, tasks.StatusCancelled} {
		r := newRig(t)
		canceller := &fakeCanceller{store: r.tasks.store}
		r.svc.Canceller = canceller
		c, id := conversationWithRun(t, r, status)

		if err := r.svc.Remove(context.Background(), c.ID); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		assertConversationGone(t, r, c)
		assertTaskStatus(t, r, id, status)
		if len(canceller.cancelled) != 1 {
			t.Errorf("%s: the canceller was asked %d times, want 1", status, len(canceller.cancelled))
		}
	}
}

func TestRemoveWhenTheRunIsGoneOrFinishedMeanwhileStillDeletes(t *testing.T) {
	for _, race := range []error{tasks.ErrNotCancellable, tasks.ErrNotFound} {
		r := newRig(t)
		r.svc.Canceller = &fakeCanceller{store: r.tasks.store, err: race}
		c, _ := conversationWithRun(t, r, tasks.StatusWaiting)
		if err := r.svc.Remove(context.Background(), c.ID); err != nil {
			t.Fatalf("%v: %v", race, err)
		}
		assertConversationGone(t, r, c)
	}
}

func TestRemoveThatCannotCancelReturnsTheErrorAndKeepsTheConversation(t *testing.T) {
	r := newRig(t)
	boom := errors.New("database is down")
	r.svc.Canceller = &fakeCanceller{store: r.tasks.store, err: boom}
	c, id := conversationWithRun(t, r, tasks.StatusWaiting)

	err := r.svc.Remove(context.Background(), c.ID)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	want := "cancel the conversation's run before removing it: database is down"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	if v := mustView(t, r, c); v.ID != c.ID {
		t.Errorf("the conversation must stay: %+v", v)
	}
	assertTaskStatus(t, r, id, tasks.StatusWaiting)
}

func TestRemoveWithoutARunOrWithoutACancellerJustDeletes(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	canceller := &fakeCanceller{store: r.tasks.store, stops: true}
	r.svc.Canceller = canceller
	none := r.conversation(t, "hello")
	if err := r.svc.Remove(ctx, none.ID); err != nil {
		t.Fatal(err)
	}
	assertConversationGone(t, r, none)
	if len(canceller.cancelled) != 0 {
		t.Errorf("no run, yet cancelled %v", canceller.cancelled)
	}

	r = newRig(t) // Canceller stays nil
	c, id := conversationWithRun(t, r, tasks.StatusWaiting)
	if err := r.svc.Remove(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	assertConversationGone(t, r, c)
	assertTaskStatus(t, r, id, tasks.StatusWaiting)
}

func TestRemoveOfAnUnknownOrInvalidIDIsNotFound(t *testing.T) {
	r := newRig(t)
	r.svc.Canceller = &fakeCanceller{store: r.tasks.store}
	for _, id := range []string{"11111111-1111-1111-1111-111111111111", "not-an-id", ""} {
		if err := r.svc.Remove(context.Background(), id); !errors.Is(err, chat.ErrNotFound) {
			t.Errorf("%q: %v", id, err)
		}
	}
}

// A run still stopping when its conversation is deleted ends later and the
// worker tells the chat; there is no conversation, so nothing happens.
func TestARunEndingAfterItsConversationWasRemovedIsIgnoredQuietly(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	canceller := &fakeCanceller{store: r.tasks.store, stops: true}
	r.svc.Canceller = canceller
	c, id := conversationWithRun(t, r, tasks.StatusRunning)
	if err := r.svc.Remove(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	var before int
	count := `SELECT count(*) FROM messages`
	if err := r.tasks.pool.QueryRow(ctx, count).Scan(&before); err != nil {
		t.Fatal(err)
	}
	logged, delivered := r.logs.Len(), len(r.channel.Delivered())

	r.tasks.setStatus(id, tasks.StatusCancelled) // what the worker does when the run stops
	task, err := r.tasks.store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	r.svc.TaskUpdated(ctx, task)

	var after int
	if err := r.tasks.pool.QueryRow(ctx, count).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before || len(r.channel.Delivered()) != delivered {
		t.Errorf("messages %d -> %d, delivered %d -> %d", before, after, delivered, len(r.channel.Delivered()))
	}
	if r.logs.Len() != logged {
		t.Errorf("logged: %s", r.logs.String()[logged:])
	}
	if r.errors.n != 0 {
		t.Errorf("ChatNotifyError called %d times", r.errors.n)
	}
}
