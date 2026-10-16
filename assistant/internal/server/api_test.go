package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"assistant/internal/dbtest"
	"assistant/internal/llm"
	"assistant/internal/llmcalls"
	"assistant/internal/metrics"
	"assistant/internal/tasks"
	"assistant/internal/users"
)

type api struct {
	t    *testing.T
	h    http.Handler
	spec *spec
	pool *pgxpool.Pool
	m    *metrics.Metrics
	now  time.Time
	logs *bytes.Buffer
	// models is what GET /api/v1/models reports; tests change it.
	models *fakeModels
}

type fakeModels struct{ status []llm.ModelStatus }

func (f *fakeModels) Status() []llm.ModelStatus { return f.status }

var fixedNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func newAPI(t *testing.T) *api {
	t.Helper()
	pool := dbtest.Pool(t)
	a := &api{t: t, spec: loadSpec(t), pool: pool, m: metrics.New(), now: fixedNow, logs: &bytes.Buffer{}, models: &fakeModels{}}
	a.h = NewRouter(Deps{
		DB: pool, Users: users.NewStore(pool), Tasks: tasks.NewStore(pool), Calls: llmcalls.NewStore(pool), Models: a.models, Metrics: a.m,
		Log: slog.New(slog.NewJSONHandler(a.logs, nil)), Now: func() time.Time { return a.now },
	})
	return a
}

// call makes a request and holds the response to openapi.yaml. route is the
// route template (what the spec documents), path the concrete URL.
func (a *api) call(method, route, path string, body any) *httptest.ResponseRecorder {
	a.t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			a.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, httptest.NewRequest(method, path, reader))
	a.spec.check(method, route, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes())
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("response is not the expected JSON: %v\n%s", err, rec.Body.String())
	}
	return v
}

func (a *api) wantError(rec *httptest.ResponseRecorder, status int, code, messageContains string) {
	a.t.Helper()
	if rec.Code != status {
		a.t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	e := decode[apiError](a.t, rec)
	if e.Error.Code != code || !strings.Contains(e.Error.Message, messageContains) {
		a.t.Errorf("error = %+v, want code %q and a message containing %q", e.Error, code, messageContains)
	}
}

func (a *api) newUser(name string) users.User {
	a.t.Helper()
	rec := a.call("POST", "/api/v1/users", "/api/v1/users", map[string]any{"display_name": name})
	if rec.Code != 201 {
		a.t.Fatalf("create user: %d %s", rec.Code, rec.Body)
	}
	return decode[users.User](a.t, rec)
}

func (a *api) newTask(userID, workflow string) taskResponse {
	a.t.Helper()
	rec := a.call("POST", "/api/v1/tasks", "/api/v1/tasks", map[string]any{"user_id": userID, "workflow_id": workflow})
	if rec.Code != 201 {
		a.t.Fatalf("create task: %d %s", rec.Code, rec.Body)
	}
	return decode[taskResponse](a.t, rec)
}

// ── users ────────────────────────────────────────────────────────────────────

func TestCreateUser(t *testing.T) {
	a := newAPI(t)
	rec := a.call("POST", "/api/v1/users", "/api/v1/users", `{"display_name":"  Janusz ","telegram_id":123456789012}`)
	if rec.Code != 201 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	u := decode[users.User](t, rec)
	if !users.ValidID(u.ID) || u.DisplayName != "Janusz" || u.TelegramID == nil || *u.TelegramID != 123456789012 {
		t.Errorf("user = %+v", u)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestCreateUserWithoutATelegramID(t *testing.T) {
	a := newAPI(t)
	for _, body := range []string{`{"display_name":"Ada"}`, `{"display_name":"Ada","telegram_id":null}`} {
		rec := a.call("POST", "/api/v1/users", "/api/v1/users", body)
		if rec.Code != 201 || decode[users.User](t, rec).TelegramID != nil {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
}

func TestCreateUserRejectsBadRequests(t *testing.T) {
	a := newAPI(t)
	tests := []struct {
		name, body, contains string
	}{
		{"empty body", ``, "empty"},
		{"not JSON", `not json`, "not valid"},
		{"truncated JSON", `{"display_name":`, "not valid"},
		{"an array", `["Ada"]`, "not valid"},
		{"unknown field", `{"display_name":"Ada","admin":true}`, "unknown field"},
		{"two objects", `{"display_name":"Ada"} {"display_name":"Bob"}`, "one JSON object only"},
		{"no display_name", `{}`, "display_name is required"},
		{"blank display_name", `{"display_name":"   "}`, "display_name is required"},
		{"null display_name", `{"display_name":null}`, "display_name is required"},
		{"display_name is a number", `{"display_name":5}`, "not valid"},
		{"telegram_id is a string", `{"display_name":"Ada","telegram_id":"123"}`, "not valid"},
		{"telegram_id is fractional", `{"display_name":"Ada","telegram_id":1.5}`, "not valid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a.wantError(a.call("POST", "/api/v1/users", "/api/v1/users", tc.body), 400, "invalid_request", tc.contains)
		})
	}
	if list := decode[userList](t, a.call("GET", "/api/v1/users", "/api/v1/users", nil)); len(list.Items) != 0 {
		t.Errorf("a rejected request must not create a user: %v", list.Items)
	}
}

func TestCreateUserRejectsAnOversizedBody(t *testing.T) {
	a := newAPI(t)
	huge := `{"display_name":"` + strings.Repeat("x", maxBodyBytes+10) + `"}`
	a.wantError(a.call("POST", "/api/v1/users", "/api/v1/users", huge), 400, "invalid_request", "not valid")
}

func TestDuplicateTelegramIDIsAConflict(t *testing.T) {
	a := newAPI(t)
	if rec := a.call("POST", "/api/v1/users", "/api/v1/users", `{"display_name":"First","telegram_id":9}`); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	a.wantError(a.call("POST", "/api/v1/users", "/api/v1/users", `{"display_name":"Second","telegram_id":9}`), 409, "conflict", "telegram_id")
}

func TestListUsers(t *testing.T) {
	a := newAPI(t)
	empty := a.call("GET", "/api/v1/users", "/api/v1/users", nil)
	if strings.TrimSpace(empty.Body.String()) != `{"items":[]}` {
		t.Errorf("an empty list is {\"items\":[]}, got %s", empty.Body)
	}
	a.newUser("One")
	a.newUser("Two")
	list := decode[userList](t, a.call("GET", "/api/v1/users", "/api/v1/users", nil))
	if len(list.Items) != 2 || list.Items[0].DisplayName != "One" || list.Items[1].DisplayName != "Two" {
		t.Errorf("list = %+v", list.Items)
	}
}

// ── tasks ────────────────────────────────────────────────────────────────────

func TestCreateTaskQueuesAPendingAPITask(t *testing.T) {
	a := newAPI(t)
	u := a.newUser("Ada")
	rec := a.call("POST", "/api/v1/tasks", "/api/v1/tasks", map[string]any{"user_id": u.ID, "workflow_id": "noop", "payload": map[string]any{"sleep_ms": 5}})
	if rec.Code != 201 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	task := decode[taskResponse](t, rec)
	if task.Source != "api" || task.Status != "pending" || task.UserID != u.ID || task.WorkflowID != "noop" {
		t.Errorf("task = %+v", task)
	}
	if string(task.Payload) != `{"sleep_ms": 5}` && string(task.Payload) != `{"sleep_ms":5}` {
		t.Errorf("payload = %s", task.Payload)
	}
	if !isNull(task.Result) || task.Error != nil || task.StartedAt != nil || task.FinishedAt != nil || task.ExecutionSeconds != nil {
		t.Errorf("a new task has no result, error, start, finish or execution time: %+v", task)
	}
}

func TestCreateTaskDefaultsThePayload(t *testing.T) {
	a := newAPI(t)
	u := a.newUser("Ada")
	task := a.newTask(u.ID, "noop")
	if string(task.Payload) != "{}" {
		t.Errorf("payload = %s", task.Payload)
	}
}

func TestTheSourceOfAnAPITaskCannotBeForged(t *testing.T) {
	a := newAPI(t)
	u := a.newUser("Ada")
	rec := a.call("POST", "/api/v1/tasks", "/api/v1/tasks", map[string]any{"user_id": u.ID, "workflow_id": "noop", "source": "telegram"})
	a.wantError(rec, 400, "invalid_request", "unknown field")
	rec = a.call("POST", "/api/v1/tasks", "/api/v1/tasks", map[string]any{"user_id": u.ID, "workflow_id": "noop", "status": "succeeded"})
	a.wantError(rec, 400, "invalid_request", "unknown field")
}

func TestCreateTaskRejectsBadRequests(t *testing.T) {
	a := newAPI(t)
	u := a.newUser("Ada")
	tests := []struct {
		name   string
		body   any
		status int
		code   string
		text   string
	}{
		{"empty body", ``, 400, "invalid_request", "empty"},
		{"not JSON", `nope`, 400, "invalid_request", "not valid"},
		{"no user_id", map[string]any{"workflow_id": "noop"}, 400, "invalid_request", "user_id is required"},
		{"blank user_id", map[string]any{"user_id": " ", "workflow_id": "noop"}, 400, "invalid_request", "user_id is required"},
		{"malformed user_id", map[string]any{"user_id": "ada", "workflow_id": "noop"}, 400, "invalid_request", "user_id is not a valid id"},
		{"no workflow_id", map[string]any{"user_id": u.ID}, 400, "invalid_request", "workflow_id is required"},
		{"blank workflow_id", map[string]any{"user_id": u.ID, "workflow_id": "  "}, 400, "invalid_request", "workflow_id is required"},
		{"payload is an array", map[string]any{"user_id": u.ID, "workflow_id": "noop", "payload": []int{1}}, 400, "invalid_request", "payload must be a JSON object"},
		{"payload is a string", map[string]any{"user_id": u.ID, "workflow_id": "noop", "payload": "x"}, 400, "invalid_request", "payload must be a JSON object"},
		{"payload is null", `{"user_id":"` + u.ID + `","workflow_id":"noop","payload":null}`, 400, "invalid_request", "payload must be a JSON object"},
		{"user_id is a number", `{"user_id":5,"workflow_id":"noop"}`, 400, "invalid_request", "not valid"},
		{"unknown user", map[string]any{"user_id": "11111111-1111-1111-1111-111111111111", "workflow_id": "noop"}, 404, "not_found", "user not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a.wantError(a.call("POST", "/api/v1/tasks", "/api/v1/tasks", tc.body), tc.status, tc.code, tc.text)
		})
	}
	if page := decode[taskList](t, a.call("GET", "/api/v1/tasks", "/api/v1/tasks", nil)); page.Total != 0 {
		t.Errorf("a rejected request must not queue a task: %+v", page)
	}
}

func TestGetTask(t *testing.T) {
	a := newAPI(t)
	task := a.newTask(a.newUser("Ada").ID, "noop")
	got := decode[taskResponse](t, a.call("GET", "/api/v1/tasks/{id}", "/api/v1/tasks/"+task.ID, nil))
	if got.ID != task.ID || got.WorkflowID != "noop" {
		t.Errorf("got %+v", got)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	a := newAPI(t)
	for _, id := range []string{"11111111-1111-1111-1111-111111111111", "nope", "1", "%27%20OR%201%3D1%20--"} {
		a.wantError(a.call("GET", "/api/v1/tasks/{id}", "/api/v1/tasks/"+id, nil), 404, "not_found", "task not found")
	}
}

func TestListTasksPagesNewestFirst(t *testing.T) {
	a := newAPI(t)
	u := a.newUser("Ada")
	var ids []string
	for i, w := range []string{"w0", "w1", "w2", "w3", "w4"} {
		task := a.newTask(u.ID, w)
		if _, err := a.pool.Exec(context.Background(), `UPDATE tasks SET created_at = $2 WHERE id = $1::uuid`, task.ID, fixedNow.Add(-time.Hour+time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
	}
	first := decode[taskList](t, a.call("GET", "/api/v1/tasks", "/api/v1/tasks?limit=2", nil))
	if first.Total != 5 || first.Limit != 2 || first.Offset != 0 || len(first.Items) != 2 || first.Items[0].ID != ids[4] || first.Items[1].ID != ids[3] {
		t.Errorf("first page = %+v", first)
	}
	second := decode[taskList](t, a.call("GET", "/api/v1/tasks", "/api/v1/tasks?limit=2&offset=2", nil))
	if second.Offset != 2 || len(second.Items) != 2 || second.Items[0].ID != ids[2] {
		t.Errorf("second page = %+v", second)
	}
	beyond := a.call("GET", "/api/v1/tasks", "/api/v1/tasks?offset=99", nil)
	if !strings.Contains(beyond.Body.String(), `"items":[]`) {
		t.Errorf("a page beyond the end has an empty items array, got %s", beyond.Body)
	}
}

func TestListTasksDefaults(t *testing.T) {
	a := newAPI(t)
	page := decode[taskList](t, a.call("GET", "/api/v1/tasks", "/api/v1/tasks", nil))
	if page.Limit != tasks.DefaultLimit || page.Offset != 0 || page.Total != 0 || page.Items == nil {
		t.Errorf("empty queue = %+v", page)
	}
}

func TestListTasksFiltersByStatus(t *testing.T) {
	a := newAPI(t)
	u := a.newUser("Ada")
	failed := a.newTask(u.ID, "a")
	a.newTask(u.ID, "b")
	if _, err := a.pool.Exec(context.Background(), `UPDATE tasks SET status = 'failed', error = 'boom' WHERE id = $1::uuid`, failed.ID); err != nil {
		t.Fatal(err)
	}
	got := decode[taskList](t, a.call("GET", "/api/v1/tasks", "/api/v1/tasks?status=failed", nil))
	if got.Total != 1 || len(got.Items) != 1 || got.Items[0].ID != failed.ID || got.Items[0].Error == nil || *got.Items[0].Error != "boom" {
		t.Errorf("failed tasks = %+v", got)
	}
}

func TestListTasksRejectsBadParameters(t *testing.T) {
	a := newAPI(t)
	for _, q := range []struct{ query, contains string }{
		{"status=bogus", "status must be one of"},
		{"status=pending%27%20OR%20%271%27%3D%271", "status must be one of"},
		{"limit=0", "limit must be"},
		{"limit=101", "limit must be"},
		{"limit=abc", "limit must be"},
		{"limit=-1", "limit must be"},
		{"offset=-1", "offset must be"},
		{"offset=x", "offset must be"},
	} {
		a.wantError(a.call("GET", "/api/v1/tasks", "/api/v1/tasks?"+q.query, nil), 400, "invalid_request", q.contains)
	}
}

func TestDerivedTimesAreComputedByTheServer(t *testing.T) {
	a := newAPI(t)
	u := a.newUser("Ada")
	set := func(id string, created time.Time, started, finished *time.Time, status string) {
		t.Helper()
		if _, err := a.pool.Exec(context.Background(),
			`UPDATE tasks SET created_at = $2, started_at = $3, finished_at = $4, status = $5 WHERE id = $1::uuid`,
			id, created, started, finished, status); err != nil {
			t.Fatal(err)
		}
	}
	at := func(d time.Duration) *time.Time { v := fixedNow.Add(d); return &v }

	pending := a.newTask(u.ID, "pending")
	set(pending.ID, fixedNow.Add(-90*time.Second), nil, nil, "pending")
	running := a.newTask(u.ID, "running")
	set(running.ID, fixedNow.Add(-90*time.Second), at(-60*time.Second), nil, "running")
	done := a.newTask(u.ID, "done")
	set(done.ID, fixedNow.Add(-90*time.Second), at(-60*time.Second), at(-45*time.Second), "succeeded")

	get := func(id string) taskResponse {
		return decode[taskResponse](t, a.call("GET", "/api/v1/tasks/{id}", "/api/v1/tasks/"+id, nil))
	}
	if p := get(pending.ID); p.QueueSeconds != 90 || p.ExecutionSeconds != nil {
		t.Errorf("pending: queue %v, execution %v; want 90 so far and null", p.QueueSeconds, p.ExecutionSeconds)
	}
	if r := get(running.ID); r.QueueSeconds != 30 || r.ExecutionSeconds == nil || *r.ExecutionSeconds != 60 {
		t.Errorf("running: queue %v, execution %v; want 30 and 60 so far", r.QueueSeconds, r.ExecutionSeconds)
	}
	if d := get(done.ID); d.QueueSeconds != 30 || d.ExecutionSeconds == nil || *d.ExecutionSeconds != 15 {
		t.Errorf("finished: queue %v, execution %v; want 30 and 15", d.QueueSeconds, d.ExecutionSeconds)
	}
	a.now = fixedNow.Add(time.Hour)
	if p := get(pending.ID); p.QueueSeconds != 3690 {
		t.Errorf("a pending task's queue time keeps growing: %v", p.QueueSeconds)
	}
	if d := get(done.ID); *d.ExecutionSeconds != 15 {
		t.Errorf("a finished task's times are final: %v", *d.ExecutionSeconds)
	}
}

func TestTaskResultAndErrorAreReturned(t *testing.T) {
	a := newAPI(t)
	u := a.newUser("Ada")
	ok := a.newTask(u.ID, "ok")
	if _, err := a.pool.Exec(context.Background(), `UPDATE tasks SET status = 'succeeded', result = '{"echo":{"a":1}}' WHERE id = $1::uuid`, ok.ID); err != nil {
		t.Fatal(err)
	}
	got := decode[taskResponse](t, a.call("GET", "/api/v1/tasks/{id}", "/api/v1/tasks/"+ok.ID, nil))
	if !strings.Contains(string(got.Result), `"echo"`) {
		t.Errorf("result = %s", got.Result)
	}
}

// ── routing, errors and metrics ──────────────────────────────────────────────

func TestUnknownRoutesAndWrongMethodsAnswerWithTheErrorShape(t *testing.T) {
	a := newAPI(t)
	for _, path := range []string{"/api/v1/nothing", "/api/v2/tasks", "/api/v1/tasks/a/b", "/elsewhere"} {
		rec := httptest.NewRecorder()
		a.h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		a.wantError(rec, 404, "not_found", "no such route")
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, httptest.NewRequest("PATCH", "/api/v1/tasks", nil))
	a.wantError(rec, 405, "invalid_request", "method not allowed")
	rec = httptest.NewRecorder()
	a.h.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/v1/tasks/11111111-1111-1111-1111-111111111111", nil))
	a.wantError(rec, 405, "invalid_request", "method not allowed")
}

func TestAPIRequestsAreCountedByRouteTemplate(t *testing.T) {
	a := newAPI(t)
	task := a.newTask(a.newUser("Ada").ID, "noop")
	a.call("GET", "/api/v1/tasks/{id}", "/api/v1/tasks/"+task.ID, nil)
	a.call("GET", "/api/v1/tasks/{id}", "/api/v1/tasks/11111111-1111-1111-1111-111111111111", nil)
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `route="/api/v1/tasks/{id}",status="200"} 1`) || !strings.Contains(body, `route="/api/v1/tasks/{id}",status="404"} 1`) {
		t.Errorf("task requests are not counted by template:\n%s", grepLines(body, "assistant_http_requests_total"))
	}
	if strings.Contains(body, task.ID) {
		t.Errorf("a task id became a metric label")
	}
}

func isNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

func grepLines(body, prefix string) string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// ── failures of the stores ───────────────────────────────────────────────────

type failingStores struct{}

var errSecret = errors.New("pq: connection to db.internal.example:5432 refused, password=hunter2")

func (failingStores) Create(context.Context, string, *int64) (users.User, error) {
	return users.User{}, errSecret
}
func (failingStores) List(context.Context) ([]users.User, error) { return nil, errSecret }

type failingTasks struct{}

func (failingTasks) Create(context.Context, tasks.New) (tasks.Task, error) {
	return tasks.Task{}, errSecret
}
func (failingTasks) Get(context.Context, string) (tasks.Task, error) { return tasks.Task{}, errSecret }
func (failingTasks) Cancel(context.Context, string) (tasks.Task, error) {
	return tasks.Task{}, errSecret
}
func (failingTasks) Remove(context.Context, string) error                { return errSecret }
func (failingTasks) RemoveFinished(context.Context, string) (int, error) { return 0, errSecret }
func (failingTasks) Retry(context.Context, string) (tasks.Task, error) {
	return tasks.Task{}, errSecret
}
func (failingTasks) Resume(context.Context, string, json.RawMessage) (tasks.Task, error) {
	return tasks.Task{}, errSecret
}
func (failingTasks) List(context.Context, tasks.ListOptions) (tasks.Page, error) {
	return tasks.Page{}, errSecret
}

func TestStoreFailuresAreAnswered500WithoutLeakingTheCause(t *testing.T) {
	logs := &bytes.Buffer{}
	h := NewRouter(Deps{
		DB: fakePinger{}, Users: failingStores{}, Tasks: failingTasks{}, Metrics: metrics.New(),
		Log: slog.New(slog.NewJSONHandler(logs, nil)),
	})
	s := loadSpec(t)
	requests := []struct {
		method, route, path, body string
	}{
		{"GET", "/api/v1/users", "/api/v1/users", ""},
		{"POST", "/api/v1/users", "/api/v1/users", `{"display_name":"Ada"}`},
		{"GET", "/api/v1/tasks", "/api/v1/tasks", ""},
		{"POST", "/api/v1/tasks", "/api/v1/tasks", `{"user_id":"11111111-1111-1111-1111-111111111111","workflow_id":"noop"}`},
		{"GET", "/api/v1/tasks/{id}", "/api/v1/tasks/11111111-1111-1111-1111-111111111111", ""},
		{"POST", "/api/v1/tasks/{id}/messages", "/api/v1/tasks/11111111-1111-1111-1111-111111111111/messages", `{"message":"x"}`},
		{"POST", "/api/v1/tasks/{id}/cancel", "/api/v1/tasks/11111111-1111-1111-1111-111111111111/cancel", ""},
		{"POST", "/api/v1/tasks/{id}/retry", "/api/v1/tasks/11111111-1111-1111-1111-111111111111/retry", ""},
		{"DELETE", "/api/v1/tasks/{id}", "/api/v1/tasks/11111111-1111-1111-1111-111111111111", ""},
		{"DELETE", "/api/v1/tasks", "/api/v1/tasks?status=failed", ""},
		{"GET", "/api/v1/tasks/{id}/steps", "/api/v1/tasks/11111111-1111-1111-1111-111111111111/steps", ""},
	}
	for _, rq := range requests {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(rq.method, rq.path, strings.NewReader(rq.body)))
		s.check(rq.method, rq.route, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes())
		if rec.Code != 500 {
			t.Errorf("%s %s: status = %d", rq.method, rq.path, rec.Code)
		}
		e := decode[apiError](t, rec)
		if e.Error.Code != "internal" || e.Error.Message != "internal error" {
			t.Errorf("%s %s: error = %+v", rq.method, rq.path, e.Error)
		}
		if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "db.internal") {
			t.Errorf("%s %s: the cause leaked to the client: %s", rq.method, rq.path, rec.Body)
		}
	}
	if !strings.Contains(logs.String(), "hunter2") || strings.Count(logs.String(), `"level":"ERROR"`) != len(requests) {
		t.Errorf("each failure must be logged once, with its cause:\n%s", logs)
	}
}
