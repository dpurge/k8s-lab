package tasks_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"assistant/internal/dbtest"
	"assistant/internal/tasks"
	"assistant/internal/users"
)

type fixture struct {
	pool  *pgxpool.Pool
	tasks *tasks.Store
	user  users.User
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool := dbtest.Pool(t)
	u, err := users.NewStore(pool).Create(context.Background(), "Ada", nil)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{pool: pool, tasks: tasks.NewStore(pool), user: u}
}

func (f fixture) create(t *testing.T, workflow string) tasks.Task {
	t.Helper()
	task, err := f.tasks.Create(context.Background(), tasks.New{UserID: f.user.ID, WorkflowID: workflow, Source: tasks.SourceAPI})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// setCreatedAt gives a task a known age, since rows inserted back to back may
// share a timestamp.
func (f fixture) setCreatedAt(t *testing.T, id string, at time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `UPDATE tasks SET created_at = $2 WHERE id = $1::uuid`, id, at); err != nil {
		t.Fatal(err)
	}
}

func TestCreateQueuesAPendingTask(t *testing.T) {
	f := newFixture(t)
	task, err := f.tasks.Create(context.Background(), tasks.New{
		UserID: f.user.ID, WorkflowID: "  noop  ", Source: tasks.SourceTelegram, Payload: json.RawMessage(`{"sleep_ms": 5, "text": "héllo"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !users.ValidID(task.ID) || task.UserID != f.user.ID || task.WorkflowID != "noop" || task.Source != "telegram" || task.Status != "pending" {
		t.Errorf("task = %+v", task)
	}
	var payload map[string]any
	if err := json.Unmarshal(task.Payload, &payload); err != nil || payload["sleep_ms"] != float64(5) || payload["text"] != "héllo" {
		t.Errorf("payload = %s (%v)", task.Payload, err)
	}
	if task.Result != nil || task.Error != nil || task.StartedAt != nil || task.FinishedAt != nil {
		t.Errorf("a new task has no result, error or times: %+v", task)
	}
	if time.Since(task.CreatedAt) > time.Minute {
		t.Errorf("created_at = %v", task.CreatedAt)
	}
}

func TestCreateDefaultsAnEmptyPayload(t *testing.T) {
	f := newFixture(t)
	for _, p := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage("  \n")} {
		task, err := f.tasks.Create(context.Background(), tasks.New{UserID: f.user.ID, WorkflowID: "noop", Source: tasks.SourceAPI, Payload: p})
		if err != nil || string(task.Payload) != "{}" {
			t.Errorf("payload %q: %s, %v", p, task.Payload, err)
		}
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	base := tasks.New{UserID: f.user.ID, WorkflowID: "noop", Source: tasks.SourceAPI}
	with := func(mod func(*tasks.New)) tasks.New { n := base; mod(&n); return n }
	tests := []struct {
		name string
		in   tasks.New
		want error
	}{
		{"blank workflow", with(func(n *tasks.New) { n.WorkflowID = "  " }), tasks.ErrBlankWorkflow},
		{"empty workflow", with(func(n *tasks.New) { n.WorkflowID = "" }), tasks.ErrBlankWorkflow},
		{"unknown source", with(func(n *tasks.New) { n.Source = "fax" }), tasks.ErrInvalidSource},
		{"empty source", with(func(n *tasks.New) { n.Source = "" }), tasks.ErrInvalidSource},
		{"unknown user", with(func(n *tasks.New) { n.UserID = "11111111-1111-1111-1111-111111111111" }), tasks.ErrUnknownUser},
		{"malformed user id", with(func(n *tasks.New) { n.UserID = "not-a-uuid" }), tasks.ErrUnknownUser},
		{"payload is an array", with(func(n *tasks.New) { n.Payload = json.RawMessage(`[1]`) }), tasks.ErrPayloadNotJSON},
		{"payload is null", with(func(n *tasks.New) { n.Payload = json.RawMessage(`null`) }), tasks.ErrPayloadNotJSON},
		{"payload is a string", with(func(n *tasks.New) { n.Payload = json.RawMessage(`"x"`) }), tasks.ErrPayloadNotJSON},
		{"payload is broken", with(func(n *tasks.New) { n.Payload = json.RawMessage(`{"a":`) }), tasks.ErrPayloadNotJSON},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.tasks.Create(ctx, tc.in); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
	page, err := f.tasks.List(ctx, tasks.ListOptions{})
	if err != nil || page.Total != 0 {
		t.Errorf("a rejected task must not be stored: %+v, %v", page, err)
	}
}

func TestGet(t *testing.T) {
	f := newFixture(t)
	created := f.create(t, "noop")
	got, err := f.tasks.Get(context.Background(), created.ID)
	if err != nil || got.ID != created.ID || got.WorkflowID != "noop" {
		t.Errorf("get = %+v, %v", got, err)
	}
	for _, id := range []string{"", "nope", "11111111-1111-1111-1111-111111111111", "' OR 1=1 --"} {
		if _, err := f.tasks.Get(context.Background(), id); !errors.Is(err, tasks.ErrNotFound) {
			t.Errorf("%q: err = %v", id, err)
		}
	}
}

func TestListIsNewestFirstWithPaging(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var ids []string
	for i := 0; i < 7; i++ {
		task := f.create(t, fmt.Sprintf("wf%d", i))
		f.setCreatedAt(t, task.ID, base.Add(time.Duration(i)*time.Minute))
		ids = append(ids, task.ID)
	}

	page, err := f.tasks.List(ctx, tasks.ListOptions{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 7 || len(page.Tasks) != 3 {
		t.Fatalf("page = total %d, %d tasks", page.Total, len(page.Tasks))
	}
	for i, want := range []string{ids[6], ids[5], ids[4]} {
		if page.Tasks[i].ID != want {
			t.Errorf("task %d = %s, want newest first", i, page.Tasks[i].WorkflowID)
		}
	}
	second, _ := f.tasks.List(ctx, tasks.ListOptions{Limit: 3, Offset: 3})
	if len(second.Tasks) != 3 || second.Tasks[0].ID != ids[3] || second.Tasks[2].ID != ids[1] {
		t.Errorf("second page = %v", second.Tasks)
	}
	last, _ := f.tasks.List(ctx, tasks.ListOptions{Limit: 3, Offset: 6})
	if len(last.Tasks) != 1 || last.Tasks[0].ID != ids[0] || last.Total != 7 {
		t.Errorf("last page = %+v", last)
	}
	beyond, err := f.tasks.List(ctx, tasks.ListOptions{Limit: 3, Offset: 50})
	if err != nil || beyond.Tasks == nil || len(beyond.Tasks) != 0 || beyond.Total != 7 {
		t.Errorf("beyond the end = %+v, %v", beyond, err)
	}
}

func TestListDefaultLimit(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < tasks.DefaultLimit+2; i++ {
		f.create(t, "noop")
	}
	page, err := f.tasks.List(context.Background(), tasks.ListOptions{})
	if err != nil || len(page.Tasks) != tasks.DefaultLimit || page.Total != tasks.DefaultLimit+2 {
		t.Errorf("%d tasks of %d, %v", len(page.Tasks), page.Total, err)
	}
}

func TestListFiltersByStatus(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b, c := f.create(t, "a"), f.create(t, "b"), f.create(t, "c")
	f.pool.Exec(ctx, `UPDATE tasks SET status = 'failed' WHERE id = $1::uuid`, a.ID)
	f.pool.Exec(ctx, `UPDATE tasks SET status = 'failed' WHERE id = $1::uuid`, b.ID)
	failed, err := f.tasks.List(ctx, tasks.ListOptions{Status: tasks.StatusFailed})
	if err != nil || failed.Total != 2 || len(failed.Tasks) != 2 {
		t.Fatalf("failed = %+v, %v", failed, err)
	}
	pending, _ := f.tasks.List(ctx, tasks.ListOptions{Status: tasks.StatusPending})
	if pending.Total != 1 || pending.Tasks[0].ID != c.ID {
		t.Errorf("pending = %+v", pending)
	}
	none, _ := f.tasks.List(ctx, tasks.ListOptions{Status: tasks.StatusSucceeded})
	if none.Total != 0 || len(none.Tasks) != 0 {
		t.Errorf("succeeded = %+v", none)
	}
	paged, _ := f.tasks.List(ctx, tasks.ListOptions{Status: tasks.StatusFailed, Limit: 1})
	if paged.Total != 2 || len(paged.Tasks) != 1 {
		t.Errorf("the total counts every match, not the page: %+v", paged)
	}
}

func TestListRejectsBadOptions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.tasks.List(ctx, tasks.ListOptions{Status: "done"}); !errors.Is(err, tasks.ErrInvalidStatus) {
		t.Errorf("status: %v", err)
	}
	for _, o := range []tasks.ListOptions{{Limit: -1}, {Limit: tasks.MaxLimit + 1}, {Offset: -1}} {
		if _, err := f.tasks.List(ctx, o); err == nil {
			t.Errorf("%+v should be rejected", o)
		}
	}
	if _, err := f.tasks.List(ctx, tasks.ListOptions{Limit: tasks.MaxLimit}); err != nil {
		t.Errorf("the maximum limit is allowed: %v", err)
	}
}

func TestListStatusIsNotSQLInjectable(t *testing.T) {
	f := newFixture(t)
	f.create(t, "a")
	if _, err := f.tasks.List(context.Background(), tasks.ListOptions{Status: "pending' OR '1'='1"}); !errors.Is(err, tasks.ErrInvalidStatus) {
		t.Errorf("err = %v", err)
	}
}

func TestDerivedTimes(t *testing.T) {
	created := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	at := func(s time.Duration) *time.Time { v := created.Add(s); return &v }
	now := created.Add(100 * time.Second)

	tests := []struct {
		name    string
		task    tasks.Task
		queue   time.Duration
		exec    time.Duration
		hasExec bool
	}{
		{"pending: queue so far, no execution", tasks.Task{CreatedAt: created}, 100 * time.Second, 0, false},
		{"running: queue is final, execution so far", tasks.Task{CreatedAt: created, StartedAt: at(10 * time.Second)}, 10 * time.Second, 90 * time.Second, true},
		{"finished: both are final", tasks.Task{CreatedAt: created, StartedAt: at(10 * time.Second), FinishedAt: at(25 * time.Second)}, 10 * time.Second, 15 * time.Second, true},
		{"started at once", tasks.Task{CreatedAt: created, StartedAt: at(0), FinishedAt: at(0)}, 0, 0, true},
		{"a clock that went backwards is never negative", tasks.Task{CreatedAt: created.Add(time.Hour)}, 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.task.QueueTime(now); got != tc.queue {
				t.Errorf("QueueTime = %v, want %v", got, tc.queue)
			}
			got, ok := tc.task.ExecutionTime(now)
			if ok != tc.hasExec || got != tc.exec {
				t.Errorf("ExecutionTime = %v, %v; want %v, %v", got, ok, tc.exec, tc.hasExec)
			}
		})
	}
}

func TestTaskJSONUsesTheAgreedFieldNames(t *testing.T) {
	task := tasks.Task{ID: "i", UserID: "u", WorkflowID: "w", Source: "api", Status: "pending", Payload: json.RawMessage(`{}`)}
	b, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, key := range []string{"id", "user_id", "workflow_id", "source", "status", "payload", "result", "error", "created_at", "started_at", "finished_at", "run", "ask"} {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON lacks %q: %s", key, b)
		}
	}
	if len(m) != 13 { // the answer waiting to be delivered is internal and never shown
		t.Errorf("unexpected extra fields: %s", b)
	}
	if m["result"] != nil || m["error"] != nil || m["started_at"] != nil || m["run"] != nil || m["ask"] != nil {
		t.Errorf("absent values must be null: %s", b)
	}
}

func scrapeMetrics(t *testing.T, m interface{ Handler() http.Handler }) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}
