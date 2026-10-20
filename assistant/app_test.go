package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"assistant/internal/dbtest"
	"assistant/internal/llm"
	"assistant/internal/llm/demo"
	"assistant/internal/llm/llmtest"
	"assistant/internal/llmcalls"
	"assistant/internal/tasks"
	"assistant/internal/workflows"
	"k8s-lab/shared/modelcfg"
)

const testModels = `providers:
  - {id: gpu, type: ollama, base_url: "http://gpu.lan:11434", api_key_from: ""}
  - {id: laptop, type: ollama, base_url: "http://localhost:11434", api_key_from: ""}
  - {id: demo, type: demo}
models:
  - {id: demo-hello, provider: demo, model: hello, tags: [demo], priority: 1}
  - {id: big, provider: gpu, model: big-model, tags: [chat, strong], priority: 20, price: {input_per_mtok: 2, output_per_mtok: 10}}
  - {id: small, provider: laptop, model: small-model, tags: [chat], priority: 10}
`

// testApp assembles the app over a scripted provider, so no test reaches a
// real model.
func testApp(t *testing.T, pool *pgxpool.Pool, clock workflows.Clock, scripts map[string][]llmtest.Result) *app {
	t.Helper()
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(testModels))
	if err != nil {
		t.Fatal(err)
	}
	setup := modelSetup{config: cfg, providers: map[string]llm.Provider{"ollama": llmtest.NewProvider(scripts), "demo": demo.New()}}
	a, err := newApp(pool, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), clock, setup, testWorkflows(t))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

type recordingClock struct{ slept []time.Duration }

func (c *recordingClock) Sleep(_ context.Context, d time.Duration) error {
	c.slept = append(c.slept, d)
	return nil
}

func call(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func field(t *testing.T, rec *httptest.ResponseRecorder, name string) any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %s", rec.Body)
	}
	return m[name]
}

// The whole path of a task: queued through the API, run by the worker with the
// real noop workflow, and read back with its times.
func TestATaskGoesFromTheAPIThroughTheWorkerToASucceededResult(t *testing.T) {
	pool := dbtest.Pool(t)
	clock := &recordingClock{}
	a := testApp(t, pool, clock, nil)

	user := call(t, a.handler, "POST", "/api/v1/users", `{"display_name":"Ada"}`)
	userID := field(t, user, "id").(string)
	created := call(t, a.handler, "POST", "/api/v1/tasks", `{"user_id":"`+userID+`","workflow_id":"noop","payload":{"sleep_ms":1500,"hello":"world"}}`)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	id := field(t, created, "id").(string)

	pending := call(t, a.handler, "GET", "/api/v1/tasks/"+id, "")
	if field(t, pending, "status") != "pending" || field(t, pending, "execution_seconds") != nil {
		t.Errorf("before the worker runs: %s", pending.Body)
	}

	ran, err := a.worker.RunOnce(context.Background())
	if !ran || err != nil {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	if len(clock.slept) != 1 || clock.slept[0] != 1500*time.Millisecond {
		t.Errorf("noop waited %v", clock.slept)
	}

	done := call(t, a.handler, "GET", "/api/v1/tasks/"+id, "")
	if field(t, done, "status") != "succeeded" || field(t, done, "error") != nil {
		t.Fatalf("after the worker ran: %s", done.Body)
	}
	if res, _ := field(t, done, "result").(map[string]any); res["hello"] != "world" {
		t.Errorf("result = %v", field(t, done, "result"))
	}
	if _, ok := field(t, done, "queue_seconds").(float64); !ok {
		t.Errorf("queue_seconds missing: %s", done.Body)
	}
	if _, ok := field(t, done, "execution_seconds").(float64); !ok {
		t.Errorf("execution_seconds missing once started: %s", done.Body)
	}

	metrics := call(t, a.handler, "GET", "/metrics", "").Body.String()
	if !strings.Contains(metrics, `assistant_tasks_total{source="api",status="succeeded",workflow="noop"} 1`) {
		t.Errorf("the finished task is not in the metrics")
	}
}

func TestATaskWithAnUnknownWorkflowFailsWithTheExactError(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	userID := field(t, call(t, a.handler, "POST", "/api/v1/users", `{"display_name":"Ada"}`), "id").(string)
	id := field(t, call(t, a.handler, "POST", "/api/v1/tasks", `{"user_id":"`+userID+`","workflow_id":"does-not-exist"}`), "id").(string)
	a.worker.RunOnce(context.Background())
	got := call(t, a.handler, "GET", "/api/v1/tasks/"+id, "")
	if field(t, got, "status") != "failed" || field(t, got, "error") != "unknown workflow: does-not-exist" {
		t.Errorf("task = %s", got.Body)
	}
}

func TestStartupFailsTasksAPreviousProcessLeftRunning(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	userID := field(t, call(t, a.handler, "POST", "/api/v1/users", `{"display_name":"Ada"}`), "id").(string)
	call(t, a.handler, "POST", "/api/v1/tasks", `{"user_id":"`+userID+`","workflow_id":"noop"}`)
	if _, ok, err := a.tasks.Claim(context.Background()); !ok || err != nil { // "the old process" started it and died
		t.Fatalf("claim: %v %v", ok, err)
	}
	if ran, _ := a.worker.RunOnce(context.Background()); ran {
		t.Fatal("while a task is marked running, no other task starts: the queue would be stuck")
	}
	if err := a.recoverCrashed(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, _ := a.tasks.List(context.Background(), tasks.ListOptions{Status: tasks.StatusFailed})
	if page.Total != 1 || *page.Tasks[0].Error != tasks.InterruptedMessage {
		t.Errorf("failed tasks = %+v", page)
	}
}

func newTask(t *testing.T, a *app, workflow, payload string) string {
	t.Helper()
	userID := field(t, call(t, a.handler, "POST", "/api/v1/users", `{"display_name":"Ada"}`), "id").(string)
	created := call(t, a.handler, "POST", "/api/v1/tasks", `{"user_id":"`+userID+`","workflow_id":"`+workflow+`","payload":`+payload+`}`)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	return field(t, created, "id").(string)
}

// An ask task goes through the router: the best model for its tags is called
// after the better one fails, and both attempts are recorded for the task and
// its user, with the cost.
func TestAnAskTaskIsRoutedAndEveryAttemptIsRecordedForTheTaskAndUser(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, map[string][]llmtest.Result{
		"big":   {llmtest.Fail(llm.FailServer, "overloaded")},
		"small": {llmtest.OK("four", 12, 3)},
	})
	id := newTask(t, a, "ask", `{"prompt":"2+2?","tags":["chat"]}`)
	if ran, err := a.worker.RunOnce(context.Background()); !ran || err != nil {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	got := call(t, a.handler, "GET", "/api/v1/tasks/"+id, "")
	res, _ := field(t, got, "result").(map[string]any)
	if field(t, got, "status") != "succeeded" || res["text"] != "four" || res["model_id"] != "small" {
		t.Fatalf("task = %s", got.Body)
	}

	rows, err := llmcalls.NewStore(pool).ForTask(context.Background(), id)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	if rows[0].ModelID != "big" || rows[0].Outcome != "error" || rows[1].ModelID != "small" || rows[1].Outcome != "ok" || rows[1].OutputTokens != 3 {
		t.Errorf("rows = %+v", rows)
	}
	metrics := call(t, a.handler, "GET", "/metrics", "").Body.String()
	for _, want := range []string{
		`assistant_llm_calls_total{model="big",outcome="error",provider="gpu"} 1`,
		`assistant_llm_calls_total{model="small",outcome="ok",provider="laptop"} 1`,
		`assistant_llm_model_available{model="big"} 1`,
		`assistant_llm_model_available{model="small"} 1`,
	} {
		if !strings.Contains(metrics, want+"\n") {
			t.Errorf("metrics lack %s", want)
		}
	}
}

func TestAnAskTaskNeedingTagsNoModelHasFailsWithAClearError(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	id := newTask(t, a, "ask", `{"prompt":"x","tags":["vision"]}`)
	a.worker.RunOnce(context.Background())
	got := call(t, a.handler, "GET", "/api/v1/tasks/"+id, "")
	msg, _ := field(t, got, "error").(string)
	if field(t, got, "status") != "failed" || !strings.Contains(msg, "vision") {
		t.Errorf("task = %s", got.Body)
	}
}

func TestAnAskTaskWithoutAPromptFailsBeforeAnyCall(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	id := newTask(t, a, "ask", `{}`)
	a.worker.RunOnce(context.Background())
	got := call(t, a.handler, "GET", "/api/v1/tasks/"+id, "")
	if msg, _ := field(t, got, "error").(string); !strings.Contains(msg, "payload.prompt is required") {
		t.Errorf("task = %s", got.Body)
	}
	if rows, _ := llmcalls.NewStore(pool).ForTask(context.Background(), id); len(rows) != 0 {
		t.Errorf("rows = %+v", rows)
	}
}

func TestLoadModelsNamesTheSettingAndTheFileWhenItIsMissingOrInvalid(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	_, err := loadModels(missing)
	if err == nil || !strings.Contains(err.Error(), "ASSISTANT_MODELS_FILE") || !strings.Contains(err.Error(), missing) {
		t.Errorf("missing: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(bad, []byte("providers: []\nmodels: 3\n"), 0o600)
	_, err = loadModels(bad)
	if err == nil || !strings.Contains(err.Error(), "ASSISTANT_MODELS_FILE") || !strings.Contains(err.Error(), bad) {
		t.Errorf("invalid: %v", err)
	}
	good, err := loadModels("models.example.yaml")
	if err != nil || good.providers["ollama"] == nil {
		t.Errorf("the committed example must load: %v", err)
	}
}

func TestLoadModelsStartsWithoutAnyCredentialAndTheRouterSaysWhichProvidersAreSkipped(t *testing.T) {
	file := filepath.Join(t.TempDir(), "models.yaml")
	yaml := `providers:
  - {id: or, type: openai, base_url: 'https://x.test/v1', api_key_from: env:ASSISTANT_TEST_KEY_VAR}
  - {id: claude, type: anthropic, api_key_from: env:ASSISTANT_TEST_KEY_VAR}
  - {id: gcp, type: vertex, project: p, location: us-central1, api_key_from: ""}
  - {id: gcp-file, type: vertex, project: p, location: us-central1, api_key_from: file:` + filepath.Join(t.TempDir(), "absent-token") + `}
  - {id: gcp-ok, type: vertex, project: p, location: us-central1, api_key_from: file:` + writeTokenFile(t) + `}
models:
  - {id: m-or, provider: or, model: x, tags: [chat], priority: 1}
  - {id: m-claude, provider: claude, model: x, tags: [chat], priority: 1}
  - {id: m-gcp, provider: gcp, model: x, tags: [chat], priority: 1}
  - {id: m-gcp-file, provider: gcp-file, model: x, tags: [chat], priority: 1}
  - {id: m-gcp-ok, provider: gcp-ok, model: x, tags: [chat], priority: 1}
`
	if err := os.WriteFile(file, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASSISTANT_TEST_KEY_VAR", "")
	setup, err := loadModels(file)
	if err != nil {
		t.Fatalf("a missing credential must not stop startup: %v", err)
	}
	router, err := llm.NewRouter(setup.config, setup.providers,
		llm.WithAvailability(llm.CredentialAvailability(os.Getenv, os.ReadFile)), llm.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range router.Status() {
		got[s.ID] = s.Reason
	}
	// An openai provider needs no key, so its empty variable does not hold it out.
	want := map[string]string{"m-or": "", "m-claude": "missing_credentials", "m-gcp": "missing_credentials", "m-gcp-file": "missing_credentials", "m-gcp-ok": ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reasons = %v, want %v", got, want)
	}
	t.Setenv("ASSISTANT_TEST_KEY_VAR", "set")
	if reason := router.Status()[1].Reason; reason != "" {
		t.Errorf("the key appeared: reason = %q", reason)
	}
}

func TestLoadModelsStartsWithAHeaderBearingProviderAndSkipsItWhileAHeaderVariableIsUnset(t *testing.T) {
	file := filepath.Join(t.TempDir(), "models.yaml")
	yaml := `providers:
  - {id: gateway, type: openai, base_url: 'https://gw.test/v1', api_key_from: "", headers: {X-Title: app, X-Gw: "env:ASSISTANT_TEST_HEADER_VAR"}}
  - {id: local, type: ollama, base_url: 'http://localhost:11434', api_key_from: ""}
models:
  - {id: m-gw, provider: gateway, model: x, tags: [chat], priority: 2}
  - {id: m-local, provider: local, model: x, tags: [chat], priority: 1}
`
	if err := os.WriteFile(file, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASSISTANT_TEST_HEADER_VAR", "")
	setup, err := loadModels(file)
	if err != nil {
		t.Fatalf("a header-bearing provider must load: %v", err)
	}
	logs := &bytes.Buffer{}
	router, err := llm.NewRouter(setup.config, setup.providers,
		llm.WithAvailability(llm.CredentialAvailability(os.Getenv, os.ReadFile)), llm.WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	if err != nil {
		t.Fatal(err)
	}
	if got := router.Status(); got[0].Reason != "missing_credentials" || got[1].Reason != "" {
		t.Errorf("status = %+v", got)
	}
	if !strings.Contains(logs.String(), "header X-Gw from env:ASSISTANT_TEST_HEADER_VAR") {
		t.Errorf("the warning must name the header source: %s", logs)
	}
	t.Setenv("ASSISTANT_TEST_HEADER_VAR", "a-value")
	if reason := router.Status()[0].Reason; reason != "" {
		t.Errorf("the variable appeared: reason = %q", reason)
	}
}

func writeTokenFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("a-test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The dev deployment's pieces must agree: the committed dev models file loads,
// and the Deployment mounts the ConfigMap where the setting says the file is.
func TestTheDevModelsFileLoadsAndTheDeploymentMountsItWhereTheSettingSays(t *testing.T) {
	setup, err := loadModels("models.dev.yaml")
	if err != nil {
		t.Fatalf("models.dev.yaml: %v", err)
	}
	gcp, ok := setup.config.Provider("gcp")
	if !ok || gcp.Type != "vertex" || gcp.APIKeyFrom != "file:/etc/assistant-vertex/token" {
		t.Errorf("the dev vertex provider must read its token from the mounted secret: %+v", gcp)
	}
	raw, err := os.ReadFile("k8s/deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	deployment := string(raw)
	file := "/etc/assistant/models.yaml"
	if strings.Contains(deployment, "ASSISTANT_VERTEX_TOKEN_FILE") {
		t.Error("deployment.yaml still sets the removed ASSISTANT_VERTEX_TOKEN_FILE")
	}
	if filepath.Dir(strings.TrimPrefix(gcp.APIKeyFrom, "file:")) != "/etc/assistant-vertex" {
		t.Errorf("the token file %s is not inside the mounted secret", gcp.APIKeyFrom)
	}
	for _, want := range []string{
		"name: " + "ASSISTANT_MODELS_FILE" + "\n              value: " + file,
		"mountPath: " + filepath.Dir(file),
		"name: assistant-models",
		"name: ASSISTANT_WORKFLOWS_DIR\n              value: /etc/assistant-workflows",
		"mountPath: /etc/assistant-workflows",
		"name: assistant-workflows",
		"mountPath: /etc/assistant-vertex",
		"secretName: assistant-vertex",
	} {
		if !strings.Contains(deployment, want) {
			t.Errorf("deployment.yaml lacks %q", want)
		}
	}
}

// The purger the app starts deletes model calls older than the configured days,
// counts them, and leaves the task and the user they belong to alone.
func TestThePurgerStartedByTheAppDeletesOldCallsOnly(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	ctx := context.Background()
	taskID := newTask(t, a, "noop", `{}`)
	var userID string
	if err := pool.QueryRow(ctx, "SELECT user_id::text FROM tasks WHERE id = $1::uuid", taskID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	store := llmcalls.NewStore(pool)
	for _, age := range []time.Duration{100 * 24 * time.Hour, 89 * 24 * time.Hour} {
		err := store.Insert(ctx, llmcalls.Call{TaskID: &taskID, UserID: userID, ModelID: "m", ProviderID: "p", ModelName: "n",
			StartedAt: time.Now().Add(-age), Outcome: "ok"})
		if err != nil {
			t.Fatal(err)
		}
	}
	purger, err := a.newPurger(90)
	if err != nil {
		t.Fatal(err)
	}
	if deleted, err := purger.PurgeOnce(ctx); err != nil || deleted["llm_calls"] != 1 {
		t.Fatalf("PurgeOnce = %v, %v; want llm_calls=1, nil", deleted, err)
	}
	var calls, taskRows int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM llm_calls), (SELECT count(*) FROM tasks)").Scan(&calls, &taskRows); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || taskRows != 1 {
		t.Errorf("calls = %d, tasks = %d; want 1, 1", calls, taskRows)
	}
	rec := call(t, a.handler, "GET", "/metrics", "")
	if !strings.Contains(rec.Body.String(), "assistant_llm_calls_purged_total 1\n") {
		t.Errorf("the purged counter does not show 1:\n%s", rec.Body)
	}
}

func TestARetentionOfZeroDaysKeepsEveryCall(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	purger, err := a.newPurger(0)
	if err != nil {
		t.Fatal(err)
	}
	if deleted, err := purger.PurgeOnce(context.Background()); err != nil || len(deleted) != 0 {
		t.Errorf("PurgeOnce = %v, %v; want nothing deleted", deleted, err)
	}
}
