package server

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"assistant/internal/chat"
	"assistant/internal/llmcalls"
	"assistant/internal/tasks"
	"assistant/internal/users"
)

// failingCanceller cannot end any run.
type failingCanceller struct{}

func (failingCanceller) Cancel(context.Context, string) (tasks.Task, tasks.CancelOutcome, error) {
	return tasks.Task{}, 0, errors.New("database is down")
}

// withChat routes the API through a chat service whose canceller fails.
func (a *api) withChat() *chat.Store {
	a.t.Helper()
	store := chat.NewStore(a.pool)
	svc := &chat.Service{Store: store, Canceller: failingCanceller{}}
	a.h = NewRouter(Deps{
		DB: a.pool, Users: users.NewStore(a.pool), Tasks: tasks.NewStore(a.pool), Calls: llmcalls.NewStore(a.pool), Chat: svc,
		Models: a.models, Metrics: a.m, Log: slog.New(slog.NewJSONHandler(a.logs, nil)), Now: func() time.Time { return a.now },
	})
	return store
}

func TestDeletingAConversationWhoseRunCannotBeCancelledIs500AndKeepsIt(t *testing.T) {
	a := newAPI(t)
	store := a.withChat()
	ctx := context.Background()
	user := a.newUser("Ada")
	task := a.newTask(user.ID, "noop")
	conv, err := store.Create(ctx, user.ID, "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetTask(ctx, conv.ID, task.ID); err != nil {
		t.Fatal(err)
	}

	path := "/api/v1/conversations/" + conv.ID
	a.wantError(a.call("DELETE", "/api/v1/conversations/{id}", path, nil), 500, codeInternal, "")
	if _, err := store.Get(ctx, conv.ID); err != nil {
		t.Errorf("the conversation must stay: %v", err)
	}
	if rec := a.call("GET", "/api/v1/tasks/{id}", "/api/v1/tasks/"+task.ID, nil); rec.Code != 200 || decode[taskResponse](t, rec).Status != tasks.StatusPending {
		t.Errorf("the task is untouched: %d %s", rec.Code, rec.Body)
	}
}

func TestDeletingAnUnknownConversationIs404(t *testing.T) {
	a := newAPI(t)
	a.withChat()
	path := "/api/v1/conversations/11111111-1111-1111-1111-111111111111"
	a.wantError(a.call("DELETE", "/api/v1/conversations/{id}", path, nil), 404, codeNotFound, "conversation not found")
}
