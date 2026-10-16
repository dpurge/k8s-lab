package chat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"assistant/internal/chat"
	"assistant/internal/dbtest"
	"assistant/internal/tasks"
	"assistant/internal/users"
	"assistant/internal/workflows"

	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeTasks is the real queue with a record of what the service asked of it,
// and failures on demand. The tasks are real because a conversation refers to
// its run.
type fakeTasks struct {
	store     *tasks.Store
	pool      *pgxpool.Pool
	created   []tasks.New
	resumed   []string // id=message
	createErr error
	resumeErr error
}

func (f *fakeTasks) Create(ctx context.Context, n tasks.New) (tasks.Task, error) {
	if f.createErr != nil {
		return tasks.Task{}, f.createErr
	}
	f.created = append(f.created, n)
	return f.store.Create(ctx, n)
}

func (f *fakeTasks) Get(ctx context.Context, id string) (tasks.Task, error) {
	return f.store.Get(ctx, id)
}

func (f *fakeTasks) Resume(ctx context.Context, id string, message json.RawMessage) (tasks.Task, error) {
	if f.resumeErr != nil {
		return tasks.Task{}, f.resumeErr
	}
	f.resumed = append(f.resumed, id+"="+string(message))
	return f.store.Get(ctx, id)
}

func (f *fakeTasks) setStatus(id, status string) {
	if _, err := f.pool.Exec(context.Background(), `UPDATE tasks SET status = $2 WHERE id = $1::uuid`, id, status); err != nil {
		panic(err)
	}
}

type catalog []workflows.Info

func (c catalog) Catalog() []workflows.Info { return c }

type errCounter struct{ n int }

func (e *errCounter) ChatNotifyError() { e.n++ }

type rig struct {
	svc     *chat.Service
	tasks   *fakeTasks
	channel *chat.MockChannel
	errors  *errCounter
	logs    *bytes.Buffer
	user    users.User
}

func newRig(t *testing.T) *rig {
	t.Helper()
	pool := dbtest.Pool(t)
	user, err := users.NewStore(pool).Create(context.Background(), "Ada", nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{tasks: &fakeTasks{store: tasks.NewStore(pool), pool: pool}, channel: &chat.MockChannel{}, errors: &errCounter{}, logs: &bytes.Buffer{}, user: user}
	r.svc = &chat.Service{
		Store: chat.NewStore(pool), Tasks: r.tasks, Channel: r.channel, Errors: r.errors,
		Catalog: catalog{
			{ID: "ask", Description: "One call to a model.", Kind: "builtin"},
			{ID: "hello", Description: "Asks for a name.", Kind: "yaml"},
			{ID: "noop", Description: "Does nothing.", Kind: "builtin"},
		},
		Log: slog.New(slog.NewJSONHandler(r.logs, nil)),
	}
	return r
}

func (r *rig) conversation(t *testing.T, workflow string) chat.Conversation {
	t.Helper()
	c, err := r.svc.Start(context.Background(), r.user.ID, workflow, "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (r *rig) texts(t *testing.T, c chat.Conversation) []string {
	t.Helper()
	msgs, err := r.svc.Store.Messages(context.Background(), c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role + "/" + m.Kind + ": " + m.Text
	}
	return out
}

func TestAConversationRunsAYAMLWorkflowOrAskButNothingElse(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for _, ok := range []string{"hello", "ask"} {
		if _, err := r.svc.Start(ctx, r.user.ID, ok, "t"); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	if _, err := r.svc.Start(ctx, r.user.ID, "noop", ""); !errors.Is(err, chat.ErrNotConversational) {
		t.Errorf("noop: %v", err)
	}
	if _, err := r.svc.Start(ctx, r.user.ID, "nope", ""); !errors.Is(err, chat.ErrUnknownWorkflow) {
		t.Errorf("unknown: %v", err)
	}
	if _, err := r.svc.Start(ctx, "11111111-1111-1111-1111-111111111111", "hello", ""); !errors.Is(err, chat.ErrUnknownUser) {
		t.Errorf("unknown user: %v", err)
	}
	if _, err := r.svc.Start(ctx, "not-a-uuid", "hello", ""); !errors.Is(err, chat.ErrUnknownUser) {
		t.Errorf("bad user id: %v", err)
	}
}

func TestTheFirstMessageStartsARunWithTheTextAsItsMessage(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	c := r.conversation(t, "hello")
	msg, err := r.svc.Send(ctx, c.ID, "  hi there ")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Role != chat.RoleUser || msg.Text != "  hi there " {
		t.Errorf("the message is stored as typed: %+v", msg)
	}
	if len(r.tasks.created) != 1 {
		t.Fatalf("tasks = %d", len(r.tasks.created))
	}
	n := r.tasks.created[0]
	if n.UserID != r.user.ID || n.WorkflowID != "hello" || n.Source != tasks.SourceTelegram || string(n.Payload) != `{"message":"  hi there "}` {
		t.Errorf("task = %+v / %s", n, n.Payload)
	}
	v, _ := r.svc.Store.Get(ctx, c.ID)
	if v.TaskID == nil {
		t.Errorf("the conversation must be in the run: %+v", v)
	}
	if v.TaskStatus == nil || *v.TaskStatus != "pending" {
		t.Errorf("and its state shows: %+v", v)
	}
}

func TestAskCarriesTheTextAsItsPrompt(t *testing.T) {
	r := newRig(t)
	c := r.conversation(t, "ask")
	r.svc.Send(context.Background(), c.ID, "2+2?")
	if string(r.tasks.created[0].Payload) != `{"prompt":"2+2?"}` {
		t.Errorf("payload = %s", r.tasks.created[0].Payload)
	}
}

func TestWhileTheAssistantWorksAMessageIsRefusedAndNothingIsStored(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	c := r.conversation(t, "hello")
	r.svc.Send(ctx, c.ID, "first")
	id := *mustView(t, r, c).TaskID
	for _, status := range []string{tasks.StatusPending, tasks.StatusRunning} {
		r.tasks.setStatus(id, status)
		if _, err := r.svc.Send(ctx, c.ID, "impatient"); !errors.Is(err, chat.ErrBusy) {
			t.Errorf("%s: %v", status, err)
		}
	}
	if got := r.texts(t, c); len(got) != 1 {
		t.Errorf("a refused message is not stored: %v", got)
	}
	if len(r.tasks.created) != 1 || len(r.tasks.resumed) != 0 {
		t.Errorf("nothing else may start: %d created, %v resumed", len(r.tasks.created), r.tasks.resumed)
	}
}

func mustView(t *testing.T, r *rig, c chat.Conversation) chat.View {
	t.Helper()
	v, err := r.svc.Store.Get(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAMessageWhileTheRunWaitsIsItsAnswer(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	c := r.conversation(t, "hello")
	r.svc.Send(ctx, c.ID, "first")
	id := *mustView(t, r, c).TaskID
	r.tasks.setStatus(id, tasks.StatusWaiting)

	if _, err := r.svc.Send(ctx, c.ID, `Janusz "J"`); err != nil {
		t.Fatal(err)
	}
	if len(r.tasks.created) != 1 {
		t.Errorf("an answer does not start a new run")
	}
	if len(r.tasks.resumed) != 1 || r.tasks.resumed[0] != id+`="Janusz \"J\""` {
		t.Errorf("resumed = %v", r.tasks.resumed)
	}
	if got := r.texts(t, c); len(got) != 2 || got[1] != `user/text: Janusz "J"` {
		t.Errorf("texts = %v", got)
	}
}

func TestAfterTheRunEndedTheNextMessageStartsANewOne(t *testing.T) {
	for _, status := range []string{tasks.StatusSucceeded, tasks.StatusFailed, tasks.StatusCancelled} {
		r := newRig(t)
		ctx := context.Background()
		c := r.conversation(t, "hello")
		r.svc.Send(ctx, c.ID, "first")
		old := *mustView(t, r, c).TaskID
		r.tasks.setStatus(old, status)
		if _, err := r.svc.Send(ctx, c.ID, "again"); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		now := *mustView(t, r, c).TaskID
		if len(r.tasks.created) != 2 || now == old {
			t.Errorf("%s: %d tasks, run %s -> %s", status, len(r.tasks.created), old, now)
		}
	}
}

func TestIfTheRunWasRemovedTheConversationStartsANewOne(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	c := r.conversation(t, "hello")
	r.svc.Send(ctx, c.ID, "first")
	if err := r.tasks.store.Remove(ctx, *mustView(t, r, c).TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Send(ctx, c.ID, "again"); err != nil || len(r.tasks.created) != 2 {
		t.Errorf("err=%v created=%d", err, len(r.tasks.created))
	}
}

func TestHelpListsTheCommandsAndTheConversationalWorkflowsAndStartsNoRun(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	c := r.conversation(t, "hello")
	for _, cmd := range []string{"/help", "  /help  extra words"} {
		if _, err := r.svc.Send(ctx, c.ID, cmd); err != nil {
			t.Fatal(err)
		}
	}
	got := r.texts(t, c)
	if len(got) != 4 || got[0] != "user/text: /help" || !strings.HasPrefix(got[1], "assistant/info: ") {
		t.Fatalf("texts = %v", got)
	}
	help := got[1]
	for _, want := range []string{"/help", `"hello"`, "• hello: Asks for a name.", "• ask: One call to a model."} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q:\n%s", want, help)
		}
	}
	if strings.Contains(help, "noop") {
		t.Errorf("a workflow a conversation cannot run is not offered:\n%s", help)
	}
	if len(r.tasks.created) != 0 {
		t.Errorf("a command starts no run")
	}
	if len(r.channel.Delivered()) != 2 {
		t.Errorf("replies go through the channel: %d", len(r.channel.Delivered()))
	}
}

func TestACommandIsAnsweredEvenWhileTheAssistantWorks(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	c := r.conversation(t, "hello")
	r.svc.Send(ctx, c.ID, "first")
	if _, err := r.svc.Send(ctx, c.ID, "/help"); err != nil {
		t.Errorf("help must work while a run is busy: %v", err)
	}
}

func TestAnUnknownCommandIsSaidSoAndStartsNoRun(t *testing.T) {
	r := newRig(t)
	c := r.conversation(t, "hello")
	r.svc.Send(context.Background(), c.ID, "/frobnicate now")
	got := r.texts(t, c)
	if len(got) != 2 || got[1] != "assistant/info: I do not know the command /frobnicate. Try /help." || len(r.tasks.created) != 0 {
		t.Errorf("texts = %v", got)
	}
}

func TestABlankMessageAndAnUnknownConversationAreRefused(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	c := r.conversation(t, "hello")
	for _, blank := range []string{"", "   ", "\n\t"} {
		if _, err := r.svc.Send(ctx, c.ID, blank); !errors.Is(err, chat.ErrBlank) {
			t.Errorf("%q: %v", blank, err)
		}
	}
	for _, id := range []string{"11111111-1111-1111-1111-111111111111", "not-a-uuid"} {
		if _, err := r.svc.Send(ctx, id, "hi"); !errors.Is(err, chat.ErrNotFound) {
			t.Errorf("%s: %v", id, err)
		}
	}
	if got := r.texts(t, c); len(got) != 0 {
		t.Errorf("nothing was said: %v", got)
	}
}

func TestAFailureToStartOrResumeIsSaidInTheConversationAndReturned(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	c := r.conversation(t, "hello")
	r.tasks.createErr = errors.New("the queue is down")
	_, err := r.svc.Send(ctx, c.ID, "first")
	if err == nil || !strings.Contains(err.Error(), "the queue is down") {
		t.Fatalf("err = %v", err)
	}
	got := r.texts(t, c)
	if len(got) != 2 || !strings.HasPrefix(got[1], "assistant/error: ⚠ could not start the workflow") || !strings.Contains(got[1], "the queue is down") {
		t.Errorf("the user must see why: %v", got)
	}

	r.tasks.createErr = nil
	r.svc.Send(ctx, c.ID, "second")
	id := *mustView(t, r, c).TaskID
	r.tasks.setStatus(id, tasks.StatusWaiting)
	r.tasks.resumeErr = tasks.ErrNotWaiting
	_, err = r.svc.Send(ctx, c.ID, "answer")
	if !errors.Is(err, tasks.ErrNotWaiting) {
		t.Fatalf("err = %v", err)
	}
	if last := r.texts(t, c); !strings.Contains(last[len(last)-1], "could not deliver your answer") {
		t.Errorf("texts = %v", last)
	}
}

// ── what the assistant says when a run changes ───────────────────────────────

func task(id, status string) tasks.Task { return tasks.Task{ID: id, Status: status} }

func inRun(t *testing.T, r *rig) (chat.Conversation, string) {
	t.Helper()
	c := r.conversation(t, "hello")
	r.svc.Send(context.Background(), c.ID, "first")
	return c, *mustView(t, r, c).TaskID
}

func TestAWaitingRunSpeaksItsQuestionWithTheOptionsNumbered(t *testing.T) {
	r := newRig(t)
	c, id := inRun(t, r)
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = json.RawMessage(`{"question":"Which level?","options":["beginner","advanced"]}`)
	r.svc.TaskUpdated(context.Background(), tk)
	got := r.texts(t, c)
	if got[len(got)-1] != "assistant/question: Which level?\n1. beginner\n2. advanced" {
		t.Errorf("texts = %v", got)
	}
	msgs, _ := r.svc.Store.Messages(context.Background(), c.ID, 0)
	if msgs[len(msgs)-1].TaskID == nil || *msgs[len(msgs)-1].TaskID != id {
		t.Errorf("an assistant message says which run it came from")
	}
	if len(r.channel.Delivered()) != 1 {
		t.Errorf("delivered = %d", len(r.channel.Delivered()))
	}
}

func TestAQuestionWithoutAReadableTextShowsThePayload(t *testing.T) {
	r := newRig(t)
	c, id := inRun(t, r)
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = json.RawMessage(`{"exercise":3}`)
	r.svc.TaskUpdated(context.Background(), tk)
	if got := r.texts(t, c); !strings.Contains(got[len(got)-1], `"exercise": 3`) {
		t.Errorf("texts = %v", got)
	}
	tk.Ask = nil
	r.svc.TaskUpdated(context.Background(), tk)
	if got := r.texts(t, c); !strings.Contains(got[len(got)-1], "waiting for your answer") {
		t.Errorf("texts = %v", got)
	}
}

func TestAFinishedRunSpeaksItsResult(t *testing.T) {
	cases := map[string]struct{ result, want string }{
		"a greeting": {`{"state":{"name":"Ada","greeting":"Hello, Ada!"}}`, "Hello, Ada!\n\n(The run is finished. Write a message to start a new one.)"},
		"ask text":   {`{"text":"four","model_id":"m"}`, "four"},
		"state only": {`{"state":{"level":"beginner"}}`, "The run is finished. Its final state:\n{\n  \"level\": \"beginner\"\n}\n\n(Write a message to start a new run.)"},
		"not json":   {`nope`, "The run is finished."},
	}
	for name, c := range cases {
		r := newRig(t)
		conv, id := inRun(t, r)
		tk := task(id, tasks.StatusSucceeded)
		tk.Result = json.RawMessage(c.result)
		r.svc.TaskUpdated(context.Background(), tk)
		got := r.texts(t, conv)
		if got[len(got)-1] != "assistant/result: "+c.want {
			t.Errorf("%s: %q", name, got[len(got)-1])
		}
	}
}

func TestAFailedRunSpeaksItsErrorAndACancelledOneSaysSo(t *testing.T) {
	r := newRig(t)
	c, id := inRun(t, r)
	msg := "retries_exhausted: the model kept sending invalid JSON"
	tk := task(id, tasks.StatusFailed)
	tk.Error = &msg
	r.svc.TaskUpdated(context.Background(), tk)
	r.svc.TaskUpdated(context.Background(), task(id, tasks.StatusFailed))
	r.svc.TaskUpdated(context.Background(), task(id, tasks.StatusCancelled))
	got := r.texts(t, c)
	n := len(got)
	if got[n-3] != "assistant/error: ⚠ The run failed: "+msg || got[n-2] != "assistant/error: ⚠ The run failed: the run failed without a message" || got[n-1] != "assistant/info: The run was cancelled." {
		t.Errorf("texts = %v", got)
	}
}

func TestATaskWithNoConversationAndAnUninterestingStatusSayNothing(t *testing.T) {
	r := newRig(t)
	c, id := inRun(t, r)
	before := len(r.texts(t, c))
	r.svc.TaskUpdated(context.Background(), task("22222222-2222-4222-8222-222222222222", tasks.StatusFailed))
	r.svc.TaskUpdated(context.Background(), task(id, tasks.StatusRunning))
	if len(r.texts(t, c)) != before || r.errors.n != 0 {
		t.Errorf("nothing to say: %v", r.texts(t, c))
	}
}

type failingChannel struct{}

func (failingChannel) Deliver(context.Context, chat.Conversation, chat.Message) error {
	return errors.New("telegram is unreachable")
}

func TestAChannelThatFailsIsLoggedAndCountedAndTheMessageStaysInTheConversation(t *testing.T) {
	r := newRig(t)
	r.svc.Channel = failingChannel{}
	c, id := inRun(t, r)
	tk := task(id, tasks.StatusFailed)
	r.svc.TaskUpdated(context.Background(), tk)
	if got := r.texts(t, c); len(got) != 2 {
		t.Errorf("the message is stored even if it could not be delivered: %v", got)
	}
	if r.errors.n != 1 || !strings.Contains(r.logs.String(), "telegram is unreachable") {
		t.Errorf("counted %d, log %s", r.errors.n, r.logs)
	}
}

func TestAConversationIsRemovedWithItsMessagesAndItsTaskIsLeftAlone(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	c, taskID := inRun(t, r)
	if err := r.svc.Store.Remove(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Store.Get(ctx, c.ID); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	if msgs, _ := r.svc.Store.Messages(ctx, c.ID, 0); len(msgs) != 0 {
		t.Errorf("messages: %v", msgs)
	}
	if err := r.svc.Store.Remove(ctx, c.ID); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("twice: %v", err)
	}
	if _, err := r.tasks.Get(ctx, taskID); err != nil {
		t.Errorf("the run stays: %v", err)
	}
}

func TestTheListIsNewestActivityFirstPerUserWithTheRunsStateAndTheLastWords(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	first := r.conversation(t, "hello")
	second := r.conversation(t, "ask")
	r.svc.Send(ctx, first.ID, "hello from the first")
	r.svc.Send(ctx, second.ID, "hello from the second")
	r.svc.Send(ctx, first.ID, "/help")

	other, _ := users.NewStore(r.svc.Store.Pool()).Create(ctx, "Bob", nil)
	if _, err := r.svc.Start(ctx, other.ID, "hello", "bobs"); err != nil {
		t.Fatal(err)
	}

	list, err := r.svc.Store.List(ctx, r.user.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("%+v %v", list, err)
	}
	if list[0].ID != first.ID || list[1].ID != second.ID {
		t.Errorf("the one with the latest activity comes first: %s, %s", list[0].WorkflowID, list[1].WorkflowID)
	}
	if list[0].LastText == nil || !strings.HasPrefix(*list[0].LastText, "Commands:") || list[0].Messages != 3 {
		t.Errorf("first = %+v", list[0])
	}
	if list[1].TaskStatus == nil || *list[1].TaskStatus != "pending" {
		t.Errorf("the run's state is in the list: %+v", list[1])
	}
	all, _ := r.svc.Store.List(ctx, "")
	if len(all) != 3 {
		t.Errorf("without a user filter all are listed: %d", len(all))
	}
	if none, err := r.svc.Store.List(ctx, "not-a-uuid"); err != nil || len(none) != 0 {
		t.Errorf("a bad user id lists nothing: %v %v", none, err)
	}
}

func TestMessagesCanBeAskedForAfterAnId(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	c := r.conversation(t, "hello")
	r.svc.Send(ctx, c.ID, "/help")
	all, _ := r.svc.Store.Messages(ctx, c.ID, 0)
	later, _ := r.svc.Store.Messages(ctx, c.ID, all[0].ID)
	if len(all) != 2 || len(later) != 1 || later[0].ID != all[1].ID {
		t.Errorf("all %d later %d", len(all), len(later))
	}
	none, _ := r.svc.Store.Messages(ctx, c.ID, all[1].ID)
	if none == nil || len(none) != 0 {
		t.Errorf("nothing newer is an empty list, not nil: %v", none)
	}
}

func TestTheMockChannelKeepsOnlyTheLastMessages(t *testing.T) {
	m := &chat.MockChannel{}
	for i := 0; i < 250; i++ {
		m.Deliver(context.Background(), chat.Conversation{}, chat.Message{ID: int64(i)})
	}
	got := m.Delivered()
	if len(got) != 200 || got[0].ID != 50 || got[199].ID != 249 {
		t.Errorf("kept %d, first %d", len(got), got[0].ID)
	}
}
