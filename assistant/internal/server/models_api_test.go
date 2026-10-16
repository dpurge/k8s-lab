package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"assistant/internal/llm"
	"assistant/internal/llmcalls"
	"assistant/internal/metrics"
	"assistant/internal/tasks"
)

func (a *api) addCall(task *string, user, model string, outcome string, in, out int, cost float64, at time.Time) {
	a.t.Helper()
	c := llmcalls.Call{
		TaskID: task, UserID: user, ModelID: model, ProviderID: "box", ModelName: model + "-name", Tags: []string{"chat"},
		InputTokens: in, OutputTokens: out, CostUSD: cost, StartedAt: at, DurationMS: 1500, Outcome: outcome,
	}
	if outcome != "ok" {
		msg := "overloaded"
		c.Error = &msg
	}
	if err := llmcalls.NewStore(a.pool).Insert(context.Background(), c); err != nil {
		a.t.Fatal(err)
	}
}

// ── task usage ───────────────────────────────────────────────────────────────

func TestATaskThatMadeNoCallsHasZeroUsage(t *testing.T) {
	a := newAPI(t)
	task := a.newTask(a.newUser("Ada").ID, "noop")
	for name, got := range map[string]taskResponse{
		"create": task,
		"get":    decode[taskResponse](t, a.call("GET", "/api/v1/tasks/{id}", "/api/v1/tasks/"+task.ID, nil)),
	} {
		if got.Usage.Calls != 0 || got.Usage.InputTokens != 0 || got.Usage.CostUSD != 0 || got.Usage.Models == nil || len(got.Usage.Models) != 0 {
			t.Errorf("%s: usage = %+v", name, got.Usage)
		}
	}
	raw := a.call("GET", "/api/v1/tasks/{id}", "/api/v1/tasks/"+task.ID, nil).Body.String()
	if !strings.Contains(raw, `"models":[]`) {
		t.Errorf("models must be an empty list, not null: %s", raw)
	}
}

func TestATasksUsageCountsOnlySuccessfulCallsAndNamesTheModelsThatAnswered(t *testing.T) {
	a := newAPI(t)
	user := a.newUser("Ada")
	task := a.newTask(user.ID, "ask")
	other := a.newTask(user.ID, "ask")
	at := fixedNow.Add(-time.Hour)
	a.addCall(&task.ID, user.ID, "big", "timeout", 0, 0, 0, at)
	a.addCall(&task.ID, user.ID, "mid", "ok", 100, 20, 0.5, at.Add(time.Second))
	a.addCall(&task.ID, user.ID, "mid", "ok", 50, 10, 0.25, at.Add(2*time.Second))
	a.addCall(&task.ID, user.ID, "small", "ok", 5, 1, 0, at.Add(3*time.Second))

	got := decode[taskResponse](t, a.call("GET", "/api/v1/tasks/{id}", "/api/v1/tasks/"+task.ID, nil)).Usage
	if got.Calls != 3 || got.InputTokens != 155 || got.OutputTokens != 31 || got.CostUSD != 0.75 || strings.Join(got.Models, ",") != "mid,small" {
		t.Errorf("usage = %+v", got)
	}

	list := decode[taskList](t, a.call("GET", "/api/v1/tasks", "/api/v1/tasks", nil))
	byID := map[string]taskResponse{}
	for _, it := range list.Items {
		byID[it.ID] = it
	}
	if byID[task.ID].Usage.Calls != 3 || byID[other.ID].Usage.Calls != 0 || byID[other.ID].Usage.Models == nil {
		t.Errorf("list usage: %+v / %+v", byID[task.ID].Usage, byID[other.ID].Usage)
	}
}

// ── llm-calls ────────────────────────────────────────────────────────────────

func TestTaskCallsAreListedOldestFirstWithTheFailedOnes(t *testing.T) {
	a := newAPI(t)
	user := a.newUser("Ada")
	task := a.newTask(user.ID, "ask")
	other := a.newTask(user.ID, "ask")
	at := fixedNow.Add(-time.Hour)
	a.addCall(&task.ID, user.ID, "mid", "ok", 100, 20, 0.5, at.Add(time.Second))
	a.addCall(&task.ID, user.ID, "big", "timeout", 0, 0, 0, at)
	a.addCall(&other.ID, user.ID, "small", "ok", 1, 1, 0, at)

	rec := a.call("GET", "/api/v1/tasks/{id}/llm-calls", "/api/v1/tasks/"+task.ID+"/llm-calls", nil)
	list := decode[callList](t, rec)
	if len(list.Items) != 2 || list.Items[0].ModelID != "big" || list.Items[1].ModelID != "mid" {
		t.Fatalf("items = %+v", list.Items)
	}
	first := list.Items[0]
	if first.Outcome != "timeout" || first.Error == nil || *first.Error != "overloaded" || first.ModelName != "big-name" ||
		first.TaskID == nil || *first.TaskID != task.ID || first.UserID != user.ID || strings.Join(first.Tags, ",") != "chat" {
		t.Errorf("first = %+v", first)
	}
	if list.Items[1].CostUSD != 0.5 || list.Items[1].InputTokens != 100 || list.Items[1].Error != nil {
		t.Errorf("second = %+v", list.Items[1])
	}
}

func TestATaskWithoutCallsHasAnEmptyList(t *testing.T) {
	a := newAPI(t)
	task := a.newTask(a.newUser("Ada").ID, "noop")
	rec := a.call("GET", "/api/v1/tasks/{id}/llm-calls", "/api/v1/tasks/"+task.ID+"/llm-calls", nil)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"items":[]}` {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestCallsOfAnUnknownTaskAre404(t *testing.T) {
	a := newAPI(t)
	for _, id := range []string{"11111111-1111-1111-1111-111111111111", "not-a-uuid"} {
		a.wantError(a.call("GET", "/api/v1/tasks/{id}/llm-calls", "/api/v1/tasks/"+id+"/llm-calls", nil), 404, "not_found", "task not found")
	}
}

// ── usage ────────────────────────────────────────────────────────────────────

func TestUsageIsGroupedByUserAndModelAndSkipsFailures(t *testing.T) {
	a := newAPI(t)
	ada, bob := a.newUser("Ada"), a.newUser("Bob")
	at := fixedNow.Add(-48 * time.Hour)
	a.addCall(nil, ada.ID, "big", "ok", 1000, 200, 2.0, at)
	a.addCall(nil, ada.ID, "big", "ok", 500, 100, 1.0, at.Add(time.Hour))
	a.addCall(nil, ada.ID, "big", "error", 0, 0, 0, at)
	a.addCall(nil, bob.ID, "small", "ok", 10, 2, 0, at)

	items := decode[usageList](t, a.call("GET", "/api/v1/usage", "/api/v1/usage", nil)).Items
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	want := llmcalls.Usage{UserID: ada.ID, ModelID: "big", Calls: 2, InputTokens: 1500, OutputTokens: 300, CostUSD: 3.0}
	if items[0] != want {
		t.Errorf("first = %+v, want %+v", items[0], want)
	}
	if items[1].UserID != bob.ID || items[1].Calls != 1 {
		t.Errorf("second = %+v", items[1])
	}
}

func TestUsageSinceKeepsOnlyLaterCalls(t *testing.T) {
	a := newAPI(t)
	user := a.newUser("Ada")
	a.addCall(nil, user.ID, "big", "ok", 1, 1, 1, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	a.addCall(nil, user.ID, "big", "ok", 2, 2, 2, time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))
	items := decode[usageList](t, a.call("GET", "/api/v1/usage", "/api/v1/usage?since=2026-10-05T00:00:00Z", nil)).Items
	if len(items) != 1 || items[0].Calls != 1 || items[0].CostUSD != 2 {
		t.Errorf("items = %+v", items)
	}
	empty := a.call("GET", "/api/v1/usage", "/api/v1/usage?since=2027-01-01T00:00:00Z", nil)
	if strings.TrimSpace(empty.Body.String()) != `{"items":[]}` {
		t.Errorf("empty = %s", empty.Body)
	}
}

func TestUsageRejectsABadSince(t *testing.T) {
	a := newAPI(t)
	for _, v := range []string{"yesterday", "2026-10-05", "1"} {
		a.wantError(a.call("GET", "/api/v1/usage", "/api/v1/usage?since="+v, nil), 400, "invalid_request", "since must be a timestamp")
	}
}

// ── models ───────────────────────────────────────────────────────────────────

func TestModelsShowTheConfigurationAndAvailabilityButNeverAnAddress(t *testing.T) {
	a := newAPI(t)
	until := fixedNow.Add(time.Minute)
	a.models.status = []llm.ModelStatus{
		{ID: "big", Provider: "gpu", Tags: []string{"chat", "strong"}, Priority: 20, Available: false, Until: until},
		{ID: "small", Provider: "laptop", Tags: []string{"chat"}, Priority: 10, Available: true},
	}
	rec := a.call("GET", "/api/v1/models", "/api/v1/models", nil)
	items := decode[modelList](t, rec).Items
	if len(items) != 2 || items[0].ID != "big" || items[0].Available || items[0].UnavailableUntil == nil || !items[0].UnavailableUntil.Equal(until) {
		t.Fatalf("items = %+v", items)
	}
	if items[1].ID != "small" || !items[1].Available || items[1].UnavailableUntil != nil || items[1].Priority != 10 || items[1].Provider != "laptop" {
		t.Errorf("small = %+v", items[1])
	}
	if strings.Contains(rec.Body.String(), "http") || strings.Contains(rec.Body.String(), "base_url") {
		t.Errorf("the response must not carry an address: %s", rec.Body)
	}
}

func TestNoModelsIsAnEmptyList(t *testing.T) {
	a := newAPI(t)
	if rec := a.call("GET", "/api/v1/models", "/api/v1/models", nil); strings.TrimSpace(rec.Body.String()) != `{"items":[]}` {
		t.Errorf("%s", rec.Body)
	}
}

// ── failures ─────────────────────────────────────────────────────────────────

type failingCalls struct{}

func (failingCalls) ForTask(context.Context, string) ([]llmcalls.Call, error) { return nil, errSecret }
func (failingCalls) UsageByUserAndModel(context.Context, *time.Time) ([]llmcalls.Usage, error) {
	return nil, errSecret
}
func (failingCalls) UsageForTasks(context.Context, []string) (map[string]llmcalls.TaskUsage, error) {
	return nil, errSecret
}

type okTasks struct{ failingTasks }

func (okTasks) Get(_ context.Context, id string) (tasks.Task, error) { return tasks.Task{ID: id}, nil }
func (okTasks) List(context.Context, tasks.ListOptions) (tasks.Page, error) {
	return tasks.Page{Tasks: []tasks.Task{{ID: "11111111-1111-1111-1111-111111111111"}}, Total: 1}, nil
}

func TestCallStoreFailuresAreAnswered500WithoutLeakingTheCause(t *testing.T) {
	logs := &bytes.Buffer{}
	h := NewRouter(Deps{
		DB: fakePinger{}, Users: failingStores{}, Tasks: okTasks{}, Calls: failingCalls{}, Models: &fakeModels{}, Metrics: metrics.New(),
		Log: slog.New(slog.NewJSONHandler(logs, nil)),
	})
	s := loadSpec(t)
	id := "11111111-1111-1111-1111-111111111111"
	for _, rq := range []struct{ route, path string }{
		{"/api/v1/tasks/{id}/llm-calls", "/api/v1/tasks/" + id + "/llm-calls"},
		{"/api/v1/usage", "/api/v1/usage"},
		{"/api/v1/tasks/{id}", "/api/v1/tasks/" + id},
		{"/api/v1/tasks", "/api/v1/tasks"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, rq.path, nil))
		s.check("GET", rq.route, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes())
		if rec.Code != 500 || strings.Contains(rec.Body.String(), errSecret.Error()) {
			t.Errorf("%s: %d %s", rq.path, rec.Code, rec.Body)
		}
	}
	if !strings.Contains(logs.String(), errSecret.Error()) {
		t.Errorf("the cause must be logged")
	}
}
