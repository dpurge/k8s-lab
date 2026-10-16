package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"assistant/internal/dbtest"
	"assistant/internal/llm"
	"assistant/internal/llm/demo"
	"assistant/internal/llm/llmtest"
	"assistant/internal/llmcalls"
	"assistant/internal/models"
	"assistant/internal/tasks"
	"assistant/internal/workflows"
)

const testModels = `providers:
  - {id: gpu, type: ollama, base_url: "http://gpu.lan:11434"}
  - {id: laptop, type: ollama, base_url: "http://localhost:11434"}
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
	cfg, err := models.LoadBytes("models.yaml", []byte(testModels))
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
	_, err := loadModels(missing, "")
	if err == nil || !strings.Contains(err.Error(), "ASSISTANT_MODELS_FILE") || !strings.Contains(err.Error(), missing) {
		t.Errorf("missing: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(bad, []byte("providers: []\nmodels: 3\n"), 0o600)
	_, err = loadModels(bad, "")
	if err == nil || !strings.Contains(err.Error(), "ASSISTANT_MODELS_FILE") || !strings.Contains(err.Error(), bad) {
		t.Errorf("invalid: %v", err)
	}
	good, err := loadModels("models.example.yaml", "")
	if err != nil || good.providers["ollama"] == nil {
		t.Errorf("the committed example must load: %v", err)
	}
}

// The dev deployment's pieces must agree: the committed dev models file loads,
// and the Deployment mounts the ConfigMap where the setting says the file is.
func TestTheDevModelsFileLoadsAndTheDeploymentMountsItWhereTheSettingSays(t *testing.T) {
	if _, err := loadModels("models.dev.yaml", "/etc/assistant-vertex/token"); err != nil {
		t.Fatalf("models.dev.yaml: %v", err)
	}
	raw, err := os.ReadFile("k8s/deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	deployment := string(raw)
	file := "/etc/assistant/models.yaml"
	for _, want := range []string{
		"name: " + "ASSISTANT_MODELS_FILE" + "\n              value: " + file,
		"mountPath: " + filepath.Dir(file),
		"name: assistant-models",
		"name: ASSISTANT_WORKFLOWS_DIR\n              value: /etc/assistant-workflows",
		"mountPath: /etc/assistant-workflows",
		"name: assistant-workflows",
		"name: ASSISTANT_VERTEX_TOKEN_FILE\n              value: /etc/assistant-vertex/token",
		"mountPath: /etc/assistant-vertex",
		"secretName: assistant-vertex",
	} {
		if !strings.Contains(deployment, want) {
			t.Errorf("deployment.yaml lacks %q", want)
		}
	}
}

func TestAVertexProviderWithoutATokenFileSettingStopsStartupNamingBoth(t *testing.T) {
	file := filepath.Join(t.TempDir(), "models.yaml")
	os.WriteFile(file, []byte("providers:\n  - {id: gcp, type: vertex, project: p, location: us-central1}\nmodels:\n  - {id: m, provider: gcp, model: gemini-2.5-flash-lite, tags: [chat], priority: 1}\n"), 0o600)
	_, err := loadModels(file, "")
	if err == nil || !strings.Contains(err.Error(), "ASSISTANT_VERTEX_TOKEN_FILE") || !strings.Contains(err.Error(), "ASSISTANT_MODELS_FILE") || !strings.Contains(err.Error(), `"gcp"`) {
		t.Errorf("err = %v", err)
	}
	if _, err := loadModels(file, "/tmp/t"); err != nil {
		t.Errorf("with the setting it loads: %v", err)
	}
}
