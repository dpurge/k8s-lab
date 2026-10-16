package llmcalls_test

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"assistant/internal/dbtest"
	"assistant/internal/llmcalls"
	"assistant/internal/tasks"
	"assistant/internal/users"
)

type fixture struct {
	pool  *pgxpool.Pool
	store *llmcalls.Store
	user  users.User
	other users.User
	task  tasks.Task
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool := dbtest.Pool(t)
	us := users.NewStore(pool)
	ada, err := us.Create(context.Background(), "Ada", nil)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := us.Create(context.Background(), "Bob", nil)
	if err != nil {
		t.Fatal(err)
	}
	task, err := tasks.NewStore(pool).Create(context.Background(), tasks.New{UserID: ada.ID, WorkflowID: "ask", Source: tasks.SourceAPI})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{pool: pool, store: llmcalls.NewStore(pool), user: ada, other: bob, task: task}
}

var base = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func (f fixture) call(model, outcome string, in, out int, cost float64, at time.Duration) llmcalls.Call {
	taskID := f.task.ID
	return llmcalls.Call{
		TaskID: &taskID, UserID: f.user.ID, ModelID: model, ProviderID: "box", ModelName: model + ":tag",
		Tags: []string{"chat"}, InputTokens: in, OutputTokens: out, CostUSD: cost,
		StartedAt: base.Add(at), DurationMS: 1500, Outcome: outcome,
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestInsertAndReadBackEverything(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	msg := "model gemma4:12b not found"
	c := f.call("big", "error", 0, 0, 0, 0)
	c.Error = &msg
	if err := f.store.Insert(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.ForTask(ctx, f.task.ID)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	g := got[0]
	if !users.ValidID(g.ID) || g.TaskID == nil || *g.TaskID != f.task.ID || g.UserID != f.user.ID || g.ModelID != "big" || g.ProviderID != "box" || g.ModelName != "big:tag" {
		t.Errorf("identity fields: %+v", g)
	}
	if strings.Join(g.Tags, ",") != "chat" || g.DurationMS != 1500 || g.Outcome != "error" || g.Error == nil || *g.Error != msg || !g.StartedAt.Equal(base) {
		t.Errorf("other fields: %+v", g)
	}
}

func TestACostKeepsItsPrecision(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.store.Insert(ctx, f.call("m", "ok", 1200, 300, 0.0081, 0)); err != nil {
		t.Fatal(err)
	}
	got, _ := f.store.ForTask(ctx, f.task.ID)
	if !near(got[0].CostUSD, 0.0081) {
		t.Errorf("cost = %v", got[0].CostUSD)
	}
	tiny := f.call("m", "ok", 1, 1, 0.00000003, time.Second)
	if err := f.store.Insert(ctx, tiny); err != nil {
		t.Fatal(err)
	}
	got, _ = f.store.ForTask(ctx, f.task.ID)
	if !near(got[1].CostUSD, 0.00000003) {
		t.Errorf("a very small cost is not rounded away: %v", got[1].CostUSD)
	}
}

func TestACallWithoutATaskAndWithoutTags(t *testing.T) {
	f := newFixture(t)
	c := f.call("m", "ok", 1, 1, 0, 0)
	c.TaskID, c.Tags = nil, nil
	if err := f.store.Insert(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	var tags string
	var hasTask bool
	if err := f.pool.QueryRow(context.Background(), `SELECT tags::text, task_id IS NOT NULL FROM llm_calls`).Scan(&tags, &hasTask); err != nil || tags != "{}" || hasTask {
		t.Errorf("tags=%q hasTask=%v err=%v", tags, hasTask, err)
	}
}

func TestInsertRefusesAnUnknownUserAndTask(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.call("m", "ok", 1, 1, 0, 0)
	c.UserID = "11111111-1111-1111-1111-111111111111"
	if err := f.store.Insert(ctx, c); err == nil {
		t.Errorf("an unknown user must be refused")
	}
	c = f.call("m", "ok", 1, 1, 0, 0)
	bad := "11111111-1111-1111-1111-111111111111"
	c.TaskID = &bad
	if err := f.store.Insert(ctx, c); err == nil {
		t.Errorf("an unknown task must be refused")
	}
}

func TestForTaskIsOldestFirstAndOnlyThatTask(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i, m := range []string{"third", "first", "second"} {
		at := []time.Duration{30, 10, 20}[i] * time.Second
		if err := f.store.Insert(ctx, f.call(m, "ok", 1, 1, 0, at)); err != nil {
			t.Fatal(err)
		}
	}
	otherTask, _ := tasks.NewStore(f.pool).Create(ctx, tasks.New{UserID: f.user.ID, WorkflowID: "ask", Source: tasks.SourceAPI})
	other := f.call("elsewhere", "ok", 1, 1, 0, 0)
	other.TaskID = &otherTask.ID
	f.store.Insert(ctx, other)

	got, err := f.store.ForTask(ctx, f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, c := range got {
		order = append(order, c.ModelID)
	}
	if strings.Join(order, ",") != "first,second,third" {
		t.Errorf("order = %v", order)
	}
	none, err := f.store.ForTask(ctx, "22222222-2222-2222-2222-222222222222")
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("a task with no calls gives an empty list, not nil: %#v %v", none, err)
	}
}

func TestUsageSumsOnlySuccessfulCallsPerUserAndModel(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	insert := func(c llmcalls.Call) {
		t.Helper()
		if err := f.store.Insert(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	insert(f.call("big", "ok", 100, 10, 0.5, 0))
	insert(f.call("big", "ok", 200, 20, 1.0, time.Minute))
	insert(f.call("big", "timeout", 0, 0, 0, 2*time.Minute))
	insert(f.call("small", "ok", 50, 5, 0.1, 3*time.Minute))
	bobs := f.call("big", "ok", 7, 7, 0.25, 4*time.Minute)
	bobs.UserID = f.other.ID
	insert(bobs)

	usage, err := f.store.UsageByUserAndModel(ctx, nil)
	if err != nil || len(usage) != 3 {
		t.Fatalf("%+v %v", usage, err)
	}
	if u := usage[0]; u.UserID != f.user.ID || u.ModelID != "big" || u.Calls != 2 || u.InputTokens != 300 || u.OutputTokens != 30 || !near(u.CostUSD, 1.5) {
		t.Errorf("first (highest cost) = %+v", u)
	}
	if u := usage[1]; u.UserID != f.other.ID || u.Calls != 1 || !near(u.CostUSD, 0.25) {
		t.Errorf("second = %+v", u)
	}
	if u := usage[2]; u.ModelID != "small" || u.Calls != 1 || u.InputTokens != 50 {
		t.Errorf("third = %+v", u)
	}
}

func TestUsageFromADate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.store.Insert(ctx, f.call("m", "ok", 10, 1, 0.1, 0))
	f.store.Insert(ctx, f.call("m", "ok", 20, 2, 0.2, time.Hour))
	f.store.Insert(ctx, f.call("m", "ok", 40, 4, 0.4, 2*time.Hour))
	since := base.Add(time.Hour)
	usage, err := f.store.UsageByUserAndModel(ctx, &since)
	if err != nil || len(usage) != 1 {
		t.Fatalf("%+v %v", usage, err)
	}
	if u := usage[0]; u.Calls != 2 || u.InputTokens != 60 || !near(u.CostUSD, 0.6) {
		t.Errorf("a call exactly at since is included: %+v", u)
	}
	none := base.Add(24 * time.Hour)
	if usage, _ := f.store.UsageByUserAndModel(ctx, &none); usage == nil || len(usage) != 0 {
		t.Errorf("nothing after that date: %#v", usage)
	}
}

func TestUsageWithNoCallsIsAnEmptyList(t *testing.T) {
	f := newFixture(t)
	usage, err := f.store.UsageByUserAndModel(context.Background(), nil)
	if err != nil || usage == nil || len(usage) != 0 {
		t.Errorf("%#v %v", usage, err)
	}
}

func TestUsageForTasks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tstore := tasks.NewStore(f.pool)
	second, _ := tstore.Create(ctx, tasks.New{UserID: f.user.ID, WorkflowID: "ask", Source: tasks.SourceAPI})
	silent, _ := tstore.Create(ctx, tasks.New{UserID: f.user.ID, WorkflowID: "noop", Source: tasks.SourceAPI})

	f.store.Insert(ctx, f.call("gpu", "timeout", 0, 0, 0, 0))
	f.store.Insert(ctx, f.call("laptop", "ok", 100, 10, 0.5, time.Second))
	f.store.Insert(ctx, f.call("laptop", "ok", 50, 5, 0.25, 2*time.Second))
	f.store.Insert(ctx, f.call("gpu", "ok", 10, 1, 0.05, 3*time.Second))
	other := f.call("gpu", "ok", 7, 7, 1, 0)
	other.TaskID = &second.ID
	f.store.Insert(ctx, other)

	got, err := f.store.UsageForTasks(ctx, []string{f.task.ID, second.ID, silent.ID})
	if err != nil {
		t.Fatal(err)
	}
	u := got[f.task.ID]
	if u.Calls != 3 || u.InputTokens != 160 || u.OutputTokens != 16 || !near(u.CostUSD, 0.8) {
		t.Errorf("first task = %+v", u)
	}
	if strings.Join(u.Models, ",") != "laptop,gpu" {
		t.Errorf("models answer in the order they first did, once each, and a failed attempt does not count: %v", u.Models)
	}
	if s := got[second.ID]; s.Calls != 1 || s.InputTokens != 7 || strings.Join(s.Models, ",") != "gpu" {
		t.Errorf("second task = %+v", s)
	}
	if _, has := got[silent.ID]; has {
		t.Errorf("a task that made no calls is absent: %+v", got)
	}
	if none, err := f.store.UsageForTasks(ctx, nil); err != nil || len(none) != 0 {
		t.Errorf("no ids, no usage: %v %v", none, err)
	}
}

func newScrape(t *testing.T, m interface{ Handler() http.Handler }) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

func grep(body, prefix string) string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
