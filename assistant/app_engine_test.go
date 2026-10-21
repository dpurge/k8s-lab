package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"assistant/internal/dbtest"
	"assistant/internal/llm"
	"assistant/internal/llm/demo"
	"assistant/internal/llm/llmtest"
	"assistant/internal/llmcalls"
	"assistant/internal/tasksteps"
	"assistant/internal/workflow"
	"k8s-lab/shared/modelcfg"
)

const askFlow = `id: askflow
version: 1
description: Asks one question, then finishes.
state_schema:
  type: object
  properties:
    answer: {type: string}
  additionalProperties: false
start: ask
limits: {max_steps: 10, max_tokens: 100000, max_retries: 1, max_visits: 3}
states:
  ask:
    instructions: Ask the user their name.
    next: [done]
  done:
    instructions: Finished.
    end: true
`

func testWorkflows(t *testing.T) map[string]*workflow.Workflow {
	t.Helper()
	wf, err := workflow.LoadBytes("askflow.yaml", []byte(askFlow))
	if err != nil {
		t.Fatal(err)
	}
	hello, err := workflow.Load("workflows/hello.yaml")
	if err != nil {
		t.Fatal(err)
	}
	uiDemo, err := workflow.Load("workflows/ui-demo.yaml")
	if err != nil {
		t.Fatal(err)
	}
	orchestrator, err := workflow.Load("workflows/orchestrator.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return map[string]*workflow.Workflow{wf.ID: wf, hello.ID: hello, uiDemo.ID: uiDemo, orchestrator.ID: orchestrator}
}

const (
	askReply  = `{"state_patch":{},"action":{"type":"ask_user","payload":{"question":"What is your name?"}}}`
	doneReply = `{"state_patch":{"answer":"Ada"},"action":{"type":"goto","state":"done"}}`
)

func okScript(texts ...string) map[string][]llmtest.Result {
	results := make([]llmtest.Result, len(texts))
	for i, text := range texts {
		results[i] = llmtest.OK(text, 100, 20)
	}
	return map[string][]llmtest.Result{"big": results}
}

func getTask(t *testing.T, a *app, id string) map[string]any {
	t.Helper()
	rec := call(t, a.handler, "GET", "/api/v1/tasks/"+id, "")
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// The whole point of the engine in the worker: a YAML workflow runs, asks the
// user, parks the task, and a later answer resumes it to the end, with every
// step traced and every model call recorded.
func TestAWorkflowAsksTheUserIsParkedAndFinishedByTheAnswer(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, okScript(askReply, doneReply))
	id := newTask(t, a, "askflow", `{"message":"hello"}`)

	if ran, err := a.worker.RunOnce(context.Background()); !ran || err != nil {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	waiting := getTask(t, a, id)
	ask, _ := waiting["ask"].(map[string]any)
	if waiting["status"] != "waiting" || ask["question"] != "What is your name?" || waiting["run"] == nil || waiting["finished_at"] != nil {
		t.Fatalf("while waiting: %v", waiting)
	}
	run, _ := waiting["run"].(map[string]any)
	if run["state_name"] != "ask" || run["status"] != "waiting" {
		t.Errorf("run = %v", run)
	}
	list := call(t, a.handler, "GET", "/api/v1/tasks?status=waiting", "")
	if !strings.Contains(list.Body.String(), `"run":null`) || !strings.Contains(list.Body.String(), "What is your name?") {
		t.Errorf("the list leaves the run out and shows the question: %s", list.Body)
	}

	answered := call(t, a.handler, "POST", "/api/v1/tasks/"+id+"/messages", `{"message":"Ada"}`)
	if answered.Code != 200 || field(t, answered, "status") != "pending" {
		t.Fatalf("answer: %d %s", answered.Code, answered.Body)
	}
	if ran, err := a.worker.RunOnce(context.Background()); !ran || err != nil {
		t.Fatalf("resume: ran=%v err=%v", ran, err)
	}
	done := getTask(t, a, id)
	result, _ := done["result"].(map[string]any)
	state, _ := result["state"].(map[string]any)
	if done["status"] != "succeeded" || state["answer"] != "Ada" || done["ask"] != nil || done["error"] != nil {
		t.Fatalf("after the answer: %v", done)
	}
	if done["started_at"] != waiting["started_at"] {
		t.Errorf("a resumed task keeps its first start: %v then %v", waiting["started_at"], done["started_at"])
	}

	steps, err := tasksteps.NewStore(pool).ForTask(context.Background(), id)
	if err != nil || len(steps) != 2 {
		t.Fatalf("steps = %+v %v", steps, err)
	}
	if steps[0].State != "ask" || steps[0].Outcome != "ok" || !strings.Contains(string(steps[0].Action), "ask_user") || steps[1].State != "ask" || !strings.Contains(string(steps[1].Action), "goto") {
		t.Errorf("steps = %+v", steps)
	}
	calls, _ := llmcalls.NewStore(pool).ForTask(context.Background(), id)
	if len(calls) != 2 || calls[0].Outcome != "ok" {
		t.Errorf("both model calls belong to the task: %+v", calls)
	}
	metrics := call(t, a.handler, "GET", "/metrics", "").Body.String()
	if !strings.Contains(metrics, `assistant_tasks_total{source="api",status="succeeded",workflow="askflow"} 1`) || strings.Contains(metrics, `status="waiting"`) {
		t.Errorf("only the finished task is counted, once")
	}
}

func TestAWaitingTaskDoesNotBlockOtherTasks(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, okScript(askReply))
	waitingID := newTask(t, a, "askflow", `{}`)
	a.worker.RunOnce(context.Background())
	otherID := newTask(t, a, "noop", `{}`)
	if ran, err := a.worker.RunOnce(context.Background()); !ran || err != nil {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	if getTask(t, a, otherID)["status"] != "succeeded" || getTask(t, a, waitingID)["status"] != "waiting" {
		t.Error("the queue must go on while a run waits for the user")
	}
}

func TestAnAnswerIsRefusedWhenThereIsNothingToAnswer(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, okScript(askReply, doneReply))
	id := newTask(t, a, "askflow", `{}`)
	post := func(id, body string) (int, string) {
		rec := call(t, a.handler, "POST", "/api/v1/tasks/"+id+"/messages", body)
		return rec.Code, rec.Body.String()
	}
	if code, body := post(id, `{"message":"too early"}`); code != 409 || !strings.Contains(body, "not waiting") {
		t.Errorf("pending: %d %s", code, body)
	}
	a.worker.RunOnce(context.Background())
	for body, want := range map[string]int{`{}`: 400, `{"message":null}`: 400, ``: 400, `{"message":"a","extra":1}`: 400, `not json`: 400} {
		if code, _ := post(id, body); code != want {
			t.Errorf("%q: %d, want %d", body, code, want)
		}
	}
	if code, _ := post("11111111-1111-1111-1111-111111111111", `{"message":"x"}`); code != 404 {
		t.Errorf("unknown task: %d", code)
	}
	if code, _ := post(id, `{"message":"first"}`); code != 200 {
		t.Errorf("first answer: %d", code)
	}
	if code, body := post(id, `{"message":"second"}`); code != 409 {
		t.Errorf("answering twice: %d %s", code, body)
	}
	a.worker.RunOnce(context.Background())
	if code, _ := post(id, `{"message":"late"}`); code != 409 {
		t.Errorf("a finished task cannot be answered: %d", code)
	}
}

func TestAnAnswerCanBeAnyJSONValue(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, okScript(askReply, doneReply))
	id := newTask(t, a, "askflow", `{}`)
	a.worker.RunOnce(context.Background())
	if rec := call(t, a.handler, "POST", "/api/v1/tasks/"+id+"/messages", `{"message":{"choice":2,"text":"عربي"}}`); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	a.worker.RunOnce(context.Background())
	steps, _ := tasksteps.NewStore(pool).ForTask(context.Background(), id)
	if len(steps) != 2 {
		t.Fatalf("steps = %d", len(steps))
	}
}

// Errors must be visible: the task carries the engine's reason and message, and
// the trace shows each refused reply with its error and the reply itself.
func TestAFailedRunShowsItsReasonOnTheTaskAndEveryRefusedReplyInTheTrace(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, okScript("this is not json", "neither is this"))
	id := newTask(t, a, "askflow", `{}`)
	a.worker.RunOnce(context.Background())

	got := getTask(t, a, id)
	msg, _ := got["error"].(string)
	if got["status"] != "failed" || !strings.HasPrefix(msg, "retries_exhausted: ") || got["run"] == nil {
		t.Fatalf("task = %v", got)
	}
	steps, err := tasksteps.NewStore(pool).ForTask(context.Background(), id)
	if err != nil || len(steps) < 2 {
		t.Fatalf("steps = %+v %v", steps, err)
	}
	if steps[0].Outcome != "retry" || steps[0].Error == nil || *steps[0].Error == "" || steps[0].Reply == nil || *steps[0].Reply != "this is not json" {
		t.Errorf("first refused reply = %+v", steps[0])
	}
	last := steps[len(steps)-1]
	if last.Outcome != "rejected" && last.Outcome != "failed" || last.Error == nil {
		t.Errorf("the step that ended the run says why: %+v", last)
	}
}

func TestAModelThatCannotBeCalledFailsTheTaskWithItsError(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, map[string][]llmtest.Result{
		"big":   {llmtest.Fail(llm.FailServer, "overloaded")},
		"small": {llmtest.Fail(llm.FailServer, "also overloaded")},
	})
	id := newTask(t, a, "askflow", `{}`)
	a.worker.RunOnce(context.Background())
	got := getTask(t, a, id)
	msg, _ := got["error"].(string)
	if got["status"] != "failed" || !strings.Contains(msg, "llm_error") || !strings.Contains(msg, "overloaded") {
		t.Errorf("task = %v", got)
	}
}

func TestTheStepsEndpointShowsTheTraceAndAnUnknownTaskIs404(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, okScript(askReply))
	id := newTask(t, a, "askflow", `{}`)
	a.worker.RunOnce(context.Background())
	rec := call(t, a.handler, "GET", "/api/v1/tasks/"+id+"/steps", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"ask"`) || !strings.Contains(rec.Body.String(), "ask_user") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(t, a.handler, "GET", "/api/v1/tasks/11111111-1111-1111-1111-111111111111/steps", ""); rec.Code != 404 {
		t.Errorf("unknown task: %d", rec.Code)
	}
	noop := newTask(t, a, "noop", `{}`)
	empty := call(t, a.handler, "GET", "/api/v1/tasks/"+noop+"/steps", "")
	if strings.TrimSpace(empty.Body.String()) != `{"items":[]}` {
		t.Errorf("a task with no run has an empty trace: %s", empty.Body)
	}
}

func TestAWorkflowOfTheSameIdAsABuiltinOrAnUnknownOneIsNotRunByTheEngine(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	id := newTask(t, a, "learn-topic", `{}`)
	a.worker.RunOnce(context.Background())
	got := getTask(t, a, id)
	if got["error"] != "unknown workflow: learn-topic" {
		t.Errorf("only the loaded workflows run: %v", got)
	}
}

// ── loading ──────────────────────────────────────────────────────────────────

func TestLoadWorkflowsNamesTheSettingWhenTheDirectoryIsMissing(t *testing.T) {
	reg, _ := newToolRegistry()
	missing := filepath.Join(t.TempDir(), "nope")
	_, err := loadWorkflows(missing, reg)
	if err == nil || !strings.Contains(err.Error(), "ASSISTANT_WORKFLOWS_DIR") || !strings.Contains(err.Error(), missing) {
		t.Errorf("err = %v", err)
	}
	file := filepath.Join(t.TempDir(), "a-file")
	os.WriteFile(file, nil, 0o600)
	if _, err := loadWorkflows(file, reg); err == nil || !strings.Contains(err.Error(), "ASSISTANT_WORKFLOWS_DIR") {
		t.Errorf("a file is not a directory: %v", err)
	}
}

func TestLoadWorkflowsShowsEveryProblemWithFileAndLine(t *testing.T) {
	reg, _ := newToolRegistry()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte(strings.Replace(askFlow, "next: [done]", "next: [missing]", 1)), 0o600)
	_, err := loadWorkflows(dir, reg)
	if err == nil || !strings.Contains(err.Error(), "ASSISTANT_WORKFLOWS_DIR") || !strings.Contains(err.Error(), "broken.yaml:") || !strings.Contains(err.Error(), `next state "missing"`) {
		t.Errorf("err = %v", err)
	}
}

func TestTheCommittedWorkflowsLoadWithTheRealToolRegistry(t *testing.T) {
	reg, err := newToolRegistry()
	if err != nil {
		t.Fatal(err)
	}
	wfs, err := loadWorkflows("workflows", reg)
	if err != nil || wfs["learn-topic"] == nil || wfs["hello"] == nil {
		t.Errorf("%v %v", wfs, err)
	}
}

// The committed hello workflow with the demo model: the whole interface can be
// tried without a real model. It must wait for the user, take the answer exactly
// as typed, and greet.
func TestHelloWithTheDemoModelWaitsForTheNameAndGreets(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	id := newTask(t, a, "hello", `{"message":"start"}`)
	a.worker.RunOnce(context.Background())

	waiting := getTask(t, a, id)
	ask, _ := waiting["ask"].(map[string]any)
	if waiting["status"] != "waiting" || ask["question"] != "What is your name?" {
		t.Fatalf("hello must ask first: %v", waiting)
	}
	if rec := call(t, a.handler, "POST", "/api/v1/tasks/"+id+"/messages", `{"message":"Janusz \"J\" عربي"}`); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	a.worker.RunOnce(context.Background())

	done := getTask(t, a, id)
	result, _ := done["result"].(map[string]any)
	state, _ := result["state"].(map[string]any)
	if done["status"] != "succeeded" || state["name"] != `Janusz "J" عربي` || state["greeting"] != `Hello, Janusz "J" عربي! (from the demo model)` {
		t.Fatalf("done = %v", done)
	}
	steps, _ := tasksteps.NewStore(pool).ForTask(context.Background(), id)
	if len(steps) != 3 || steps[0].Outcome != "ok" {
		t.Errorf("a clean run has three steps and no retries: %+v", steps)
	}
	for _, s := range steps {
		if s.Error != nil {
			t.Errorf("no step should have failed: %+v", s)
		}
	}
	usage, _ := done["usage"].(map[string]any)
	if usage["calls"] != float64(3) {
		t.Errorf("usage = %v", usage)
	}
}

func TestTheDemoModelRefusesOtherWorkflowsAndTheFailureIsVisible(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	// askflow has no tags, so it goes to the first model of any kind: the scripted one has nothing
	// scripted, so ask for the demo model explicitly by a payload-independent route: a workflow
	// that carries the demo tag.
	wf, err := workflow.LoadBytes("other.yaml", []byte(strings.Replace(strings.Replace(askFlow, "id: askflow", "id: other", 1), "start: ask", "model_tags: [demo]\nstart: ask", 1)))
	if err != nil {
		t.Fatal(err)
	}
	a2, err := newApp(pool, a.log, &recordingClock{}, demoSetup(t), map[string]*workflow.Workflow{"other": wf}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := newTask(t, a2, "other", `{}`)
	a2.worker.RunOnce(context.Background())
	got := getTask(t, a2, id)
	msg, _ := got["error"].(string)
	if got["status"] != "failed" || !strings.Contains(msg, "demo model knows only the hello, ui-demo and orchestrator workflows") {
		t.Errorf("task = %v", got)
	}
}

func demoSetup(t *testing.T) modelSetup {
	t.Helper()
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(testModels))
	if err != nil {
		t.Fatal(err)
	}
	return modelSetup{config: cfg, providers: map[string]llm.Provider{"ollama": llmtest.NewProvider(nil), "demo": demo.New()}}
}

// ── tidying the queue over HTTP ──────────────────────────────────────────────

func TestAWaitingTaskCanBeCancelledThenRetriedThenRemoved(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	id := newTask(t, a, "hello", `{"message":"start"}`)
	a.worker.RunOnce(context.Background())
	if getTask(t, a, id)["status"] != "waiting" {
		t.Fatal("hello must be waiting for the name")
	}

	cancelled := call(t, a.handler, "POST", "/api/v1/tasks/"+id+"/cancel", "")
	if cancelled.Code != 200 || field(t, cancelled, "status") != "cancelled" || field(t, cancelled, "ask") != nil {
		t.Fatalf("cancel: %d %s", cancelled.Code, cancelled.Body)
	}
	if rec := call(t, a.handler, "POST", "/api/v1/tasks/"+id+"/messages", `{"message":"too late"}`); rec.Code != 409 {
		t.Errorf("a cancelled task cannot be answered: %d", rec.Code)
	}
	if rec := call(t, a.handler, "POST", "/api/v1/tasks/"+id+"/cancel", ""); rec.Code != 409 {
		t.Errorf("cancelling twice: %d", rec.Code)
	}

	retried := call(t, a.handler, "POST", "/api/v1/tasks/"+id+"/retry", "")
	if retried.Code != 201 || field(t, retried, "status") != "pending" || field(t, retried, "workflow_id") != "hello" || field(t, retried, "id") == id {
		t.Fatalf("retry: %d %s", retried.Code, retried.Body)
	}
	newID := field(t, retried, "id").(string)
	a.worker.RunOnce(context.Background())
	if getTask(t, a, newID)["status"] != "waiting" {
		t.Error("the retried task runs like a new one")
	}

	if rec := call(t, a.handler, "DELETE", "/api/v1/tasks/"+id, ""); rec.Code != 204 {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, a.handler, "GET", "/api/v1/tasks/"+id, ""); rec.Code != 404 {
		t.Errorf("a removed task is gone: %d", rec.Code)
	}
	steps, _ := tasksteps.NewStore(pool).ForTask(context.Background(), id)
	if len(steps) != 0 {
		t.Errorf("its trace goes with it: %d", len(steps))
	}
	if rec := call(t, a.handler, "DELETE", "/api/v1/tasks/"+id, ""); rec.Code != 404 {
		t.Errorf("removing twice: %d", rec.Code)
	}
}

func TestRetryAndBulkRemoveOverHTTPRefuseWhatTheyShould(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	pending := newTask(t, a, "noop", `{}`)
	if rec := call(t, a.handler, "POST", "/api/v1/tasks/"+pending+"/retry", ""); rec.Code != 409 {
		t.Errorf("retrying a pending task: %d", rec.Code)
	}
	for _, status := range []string{"", "pending", "running", "waiting", "bogus"} {
		if rec := call(t, a.handler, "DELETE", "/api/v1/tasks?status="+status, ""); rec.Code != 400 {
			t.Errorf("%q: %d %s", status, rec.Code, rec.Body)
		}
	}
	if rec := call(t, a.handler, "POST", "/api/v1/tasks/11111111-1111-1111-1111-111111111111/cancel", ""); rec.Code != 404 {
		t.Errorf("cancel unknown: %d", rec.Code)
	}
	a.worker.RunOnce(context.Background())
	for _, status := range []string{"succeeded"} {
		rec := call(t, a.handler, "DELETE", "/api/v1/tasks?status="+status, "")
		if rec.Code != 200 || field(t, rec, "removed") != float64(1) {
			t.Errorf("bulk %s: %d %s", status, rec.Code, rec.Body)
		}
	}
	if rec := call(t, a.handler, "GET", "/api/v1/tasks", ""); !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Errorf("list after removing: %s", rec.Body)
	}
}

func TestACancelledRunningTaskEndsCancelledAndTheWorkerGoesOn(t *testing.T) {
	pool := dbtest.Pool(t)
	started := make(chan struct{})
	release := make(chan struct{})
	slow := &blockingClock{started: started, release: release}
	a := testApp(t, pool, slow, nil)
	id := newTask(t, a, "noop", `{"sleep_ms": 5000}`)
	done := make(chan struct{})
	go func() { a.worker.RunOnce(context.Background()); close(done) }()
	<-started
	rec := call(t, a.handler, "POST", "/api/v1/tasks/"+id+"/cancel", "")
	if rec.Code != 202 || field(t, rec, "status") != "running" {
		t.Fatalf("a running task is told to stop: %d %s", rec.Code, rec.Body)
	}
	<-done
	got := getTask(t, a, id)
	if got["status"] != "cancelled" {
		t.Errorf("task = %v", got)
	}
	next := newTask(t, a, "noop", `{}`)
	a.worker.RunOnce(context.Background())
	if getTask(t, a, next)["status"] != "succeeded" {
		t.Error("the queue goes on after a cancel")
	}
}

type blockingClock struct {
	started chan struct{}
	release chan struct{}
	once    bool
}

func (c *blockingClock) Sleep(ctx context.Context, _ time.Duration) error {
	if !c.once {
		c.once = true
		close(c.started)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.release:
		}
	}
	return nil
}

func TestTheWorkflowsListHasEveryBuiltinAndYAMLWorkflowWithAHint(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	rec := call(t, a.handler, "GET", "/api/v1/workflows", "")
	var body struct {
		Items []struct {
			ID, Description, Kind string
			PayloadHint           string `json:"payload_hint"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var ids []string
	byID := map[string]string{}
	for _, it := range body.Items {
		ids = append(ids, it.ID)
		byID[it.ID] = it.Kind
		if it.Description == "" || it.PayloadHint == "" {
			t.Errorf("%s lacks a description or a hint", it.ID)
		}
		var probe map[string]any
		if err := json.Unmarshal([]byte(it.PayloadHint), &probe); err != nil {
			t.Errorf("%s: the hint is not a JSON object: %v", it.ID, err)
		}
	}
	if strings.Join(ids, ",") != "ask,askflow,hello,noop,orchestrator,ui-demo" || byID["noop"] != "builtin" || byID["hello"] != "yaml" {
		t.Errorf("ids = %v kinds = %v", ids, byID)
	}
}

// ── the chat, end to end ─────────────────────────────────────────────────────

func chatTexts(t *testing.T, a *app, convID string) []string {
	t.Helper()
	rec := call(t, a.handler, "GET", "/api/v1/conversations/"+convID+"/messages", "")
	var body struct {
		Items []struct{ Role, Kind, Text string } `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(body.Items))
	for i, m := range body.Items {
		out[i] = m.Role + "/" + m.Kind + ": " + m.Text
	}
	return out
}

func startChat(t *testing.T, a *app, workflow string) (conversationID string) {
	t.Helper()
	userID := field(t, call(t, a.handler, "POST", "/api/v1/users", `{"display_name":"Ada"}`), "id").(string)
	rec := call(t, a.handler, "POST", "/api/v1/conversations", `{"user_id":"`+userID+`","workflow_id":"`+workflow+`","title":"Trying hello"}`)
	if rec.Code != 201 {
		t.Fatalf("create conversation: %d %s", rec.Code, rec.Body)
	}
	return field(t, rec, "id").(string)
}

func say(t *testing.T, a *app, convID, text string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"text": text})
	return call(t, a.handler, "POST", "/api/v1/conversations/"+convID+"/messages", string(body)).Code
}

// The whole point: a person types, the assistant asks, the person answers, the
// assistant finishes, all through the conversation, with the real engine and
// the demo model.
func TestAConversationWithHelloAsksForTheNameAndGreetsBackThroughTheChat(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	conv := startChat(t, a, "hello")

	if code := say(t, a, conv, "hi there"); code != 201 {
		t.Fatalf("first message: %d", code)
	}
	if code := say(t, a, conv, "too soon"); code != 409 {
		t.Errorf("while the assistant works a new message is refused: %d", code)
	}
	a.worker.RunOnce(context.Background())
	got := chatTexts(t, a, conv)
	if len(got) != 2 || got[0] != "user/text: hi there" || got[1] != "assistant/question: What is your name?" {
		t.Fatalf("after the first run step: %v", got)
	}
	view := call(t, a.handler, "GET", "/api/v1/conversations/"+conv, "")
	if field(t, view, "task_status") != "waiting" {
		t.Errorf("the conversation shows its run waits: %s", view.Body)
	}

	if code := say(t, a, conv, "Janusz"); code != 201 {
		t.Fatalf("answer: %d", code)
	}
	a.worker.RunOnce(context.Background())
	got = chatTexts(t, a, conv)
	last := got[len(got)-1]
	if len(got) != 4 || !strings.HasPrefix(last, "assistant/result: Hello, Janusz! (from the demo model)") {
		t.Fatalf("after the answer: %v", got)
	}

	// The run is over: the next message starts a new one.
	if code := say(t, a, conv, "again"); code != 201 {
		t.Fatalf("a new run: %d", code)
	}
	a.worker.RunOnce(context.Background())
	got = chatTexts(t, a, conv)
	if got[len(got)-1] != "assistant/question: What is your name?" {
		t.Errorf("the second run asks again: %v", got)
	}
	if rec := call(t, a.handler, "GET", "/api/v1/tasks?limit=10", ""); !strings.Contains(rec.Body.String(), `"source":"telegram"`) {
		t.Errorf("a conversation's tasks come from the telegram channel")
	}
}

func TestHelpIsAnsweredAtOnceAndAFailedRunSpeaksItsErrorInTheChat(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, okScript("not json", "not json either", "still not json"))
	conv := startChat(t, a, "askflow")
	if code := say(t, a, conv, "/help"); code != 201 {
		t.Fatal(code)
	}
	got := chatTexts(t, a, conv)
	if len(got) != 2 || !strings.Contains(got[1], "assistant/info: Commands:") || !strings.Contains(got[1], "askflow") {
		t.Errorf("help: %v", got)
	}
	say(t, a, conv, "go")
	a.worker.RunOnce(context.Background())
	got = chatTexts(t, a, conv)
	last := got[len(got)-1]
	if !strings.HasPrefix(last, "assistant/error: ⚠ The run failed: retries_exhausted") {
		t.Errorf("the failure must be said in the chat: %v", got)
	}
	if status := field(t, call(t, a.handler, "GET", "/api/v1/conversations/"+conv, ""), "task_status"); status != "failed" {
		t.Errorf("task_status = %v", status)
	}
}

func TestAConversationOffersOnlyWhatItCanRunAndReportsMistakes(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	userID := field(t, call(t, a.handler, "POST", "/api/v1/users", `{"display_name":"Ada"}`), "id").(string)
	post := func(body string) (int, string) {
		rec := call(t, a.handler, "POST", "/api/v1/conversations", body)
		return rec.Code, rec.Body.String()
	}
	if code, body := post(`{"user_id":"` + userID + `","workflow_id":"noop"}`); code != 400 || !strings.Contains(body, "YAML workflow") {
		t.Errorf("noop: %d %s", code, body)
	}
	if code, body := post(`{"user_id":"` + userID + `","workflow_id":"nope"}`); code != 400 || !strings.Contains(body, "no such workflow") {
		t.Errorf("unknown: %d %s", code, body)
	}
	if code, _ := post(`{"user_id":"11111111-1111-1111-1111-111111111111","workflow_id":"hello"}`); code != 404 {
		t.Errorf("unknown user: %d", code)
	}
	for _, body := range []string{`{}`, `{"user_id":"` + userID + `"}`, `{"workflow_id":"hello"}`, `nope`} {
		if code, _ := post(body); code != 400 {
			t.Errorf("%s: %d", body, code)
		}
	}
	conv := startChat(t, a, "hello")
	for body, want := range map[string]int{`{}`: 400, `{"text":""}`: 400, `{"text":"  "}`: 400, `nope`: 400} {
		rec := call(t, a.handler, "POST", "/api/v1/conversations/"+conv+"/messages", body)
		if rec.Code != want {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if code := say(t, a, "11111111-1111-1111-1111-111111111111", "hi"); code != 404 {
		t.Errorf("unknown conversation: %d", code)
	}
	if rec := call(t, a.handler, "GET", "/api/v1/conversations/"+conv+"/messages?after=x", ""); rec.Code != 400 {
		t.Errorf("bad after: %d", rec.Code)
	}
}

func TestConversationsAreListedPerUserAndRemovedWithTheirMessages(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	conv := startChat(t, a, "hello")
	say(t, a, conv, "/help")
	list := call(t, a.handler, "GET", "/api/v1/conversations", "")
	if !strings.Contains(list.Body.String(), conv) || !strings.Contains(list.Body.String(), `"messages":2`) || !strings.Contains(list.Body.String(), "Commands:") {
		t.Errorf("list: %s", list.Body)
	}
	if rec := call(t, a.handler, "GET", "/api/v1/conversations?user_id=11111111-1111-1111-1111-111111111111", ""); strings.TrimSpace(rec.Body.String()) != `{"items":[]}` {
		t.Errorf("another user's list: %s", rec.Body)
	}
	if rec := call(t, a.handler, "DELETE", "/api/v1/conversations/"+conv, ""); rec.Code != 204 {
		t.Fatalf("remove: %d", rec.Code)
	}
	if rec := call(t, a.handler, "GET", "/api/v1/conversations/"+conv, ""); rec.Code != 404 {
		t.Errorf("a removed conversation is gone: %d", rec.Code)
	}
	if rec := call(t, a.handler, "DELETE", "/api/v1/conversations/"+conv, ""); rec.Code != 404 {
		t.Errorf("twice: %d", rec.Code)
	}
}

func TestRemovingAConversationsTaskLeavesTheConversationUsable(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	conv := startChat(t, a, "hello")
	say(t, a, conv, "hi")
	a.worker.RunOnce(context.Background())
	taskID := field(t, call(t, a.handler, "GET", "/api/v1/conversations/"+conv, ""), "task_id").(string)
	if rec := call(t, a.handler, "POST", "/api/v1/tasks/"+taskID+"/cancel", ""); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if rec := call(t, a.handler, "DELETE", "/api/v1/tasks/"+taskID, ""); rec.Code != 204 {
		t.Fatal(rec.Body)
	}
	view := call(t, a.handler, "GET", "/api/v1/conversations/"+conv, "")
	if field(t, view, "task_id") != nil || field(t, view, "task_status") != nil {
		t.Errorf("the conversation no longer points at the removed run: %s", view.Body)
	}
	if code := say(t, a, conv, "hello again"); code != 201 {
		t.Errorf("it can start a new run: %d", code)
	}
}

func TestCancellingAConversationsWaitingRunFromTheTasksPageShowsInTheConversationState(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	conv := startChat(t, a, "hello")
	say(t, a, conv, "hi")
	a.worker.RunOnce(context.Background())
	taskID := field(t, call(t, a.handler, "GET", "/api/v1/conversations/"+conv, ""), "task_id").(string)
	call(t, a.handler, "POST", "/api/v1/tasks/"+taskID+"/cancel", "")
	if status := field(t, call(t, a.handler, "GET", "/api/v1/conversations/"+conv, ""), "task_status"); status != "cancelled" {
		t.Errorf("task_status = %v", status)
	}
	if code := say(t, a, conv, "again"); code != 201 {
		t.Errorf("after a cancel the next message starts a new run: %d", code)
	}
}

// lastInterface returns the surface ids and the component ids of the most
// recent interface message of a conversation.
func lastInterface(t *testing.T, a *app, convID string) (surface string, componentIDs map[string]bool) {
	t.Helper()
	rec := call(t, a.handler, "GET", "/api/v1/conversations/"+convID+"/messages", "")
	var body struct {
		Items []struct {
			Kind string          `json:"kind"`
			UI   json.RawMessage `json:"ui"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for i := len(body.Items) - 1; i >= 0; i-- {
		if body.Items[i].Kind != "ui" {
			continue
		}
		var list []map[string]any
		if err := json.Unmarshal(body.Items[i].UI, &list); err != nil {
			t.Fatal(err)
		}
		componentIDs = map[string]bool{}
		for _, m := range list {
			for _, kind := range []string{"createSurface", "updateComponents"} {
				body, ok := m[kind].(map[string]any)
				if !ok {
					continue
				}
				if kind == "createSurface" {
					surface, _ = body["surfaceId"].(string)
				}
				for _, comp := range anySlice(body["components"]) {
					componentIDs[comp.(map[string]any)["id"].(string)] = true
				}
			}
		}
		return surface, componentIDs
	}
	t.Fatalf("no interface message in %s", rec.Body)
	return "", nil
}

// lastRendererCall is the id of the most recent function the run asked the page to run.
func lastRendererCall(t *testing.T, a *app, convID string) string {
	t.Helper()
	rec := call(t, a.handler, "GET", "/api/v1/conversations/"+convID+"/messages", "")
	var body struct {
		Items []struct {
			Kind string          `json:"kind"`
			UI   json.RawMessage `json:"ui"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for i := len(body.Items) - 1; i >= 0; i-- {
		if body.Items[i].Kind != "ui" {
			continue
		}
		var list []map[string]any
		if err := json.Unmarshal(body.Items[i].UI, &list); err != nil {
			t.Fatal(err)
		}
		for _, m := range list {
			if c, ok := m["callRendererFunction"].(map[string]any); ok {
				return c["functionCallId"].(string)
			}
		}
	}
	t.Fatalf("the run asked the page for nothing: %s", rec.Body)
	return ""
}

func anySlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func uiEvent(t *testing.T, a *app, convID, body string) *httptest.ResponseRecorder {
	t.Helper()
	return call(t, a.handler, "POST", "/api/v1/conversations/"+convID+"/ui-events", body)
}

func actionBody(surface, component, name, userMessage, context string) string {
	return `{"version":"v1.0","action":{"name":"` + name + `","userMessage":"` + userMessage + `","surfaceId":"` + surface + `","sourceComponentId":"` + component + `","timestamp":"2026-10-21T12:00:00Z","context":` + context + `}}`
}

// The interface toolkit end to end, with the real engine, the demo model and
// the real HTTP API: the run shows a tour, the page's action moves it on to a
// form, and the form's answers end it.
func TestUIDemoShowsATourThenAFormAndEndsWithTheAnswers(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	conv := startChat(t, a, "ui-demo")
	ctx := context.Background()

	if code := say(t, a, conv, "start"); code != 201 {
		t.Fatalf("first message: %d", code)
	}
	a.worker.RunOnce(ctx)
	got := chatTexts(t, a, conv)
	if len(got) != 3 || got[1] != "assistant/question: Here is every component of the toolkit. Try them, then press Continue." || got[2] != "assistant/ui: " {
		t.Fatalf("after the first step: %v", got)
	}
	tourSurface, tourComponents := lastInterface(t, a, conv)
	for _, id := range []string{"root", "name", "picks", "chips", "volume", "when", "image", "video", "audio", "modal", "tabs", "icons", "next"} {
		if !tourComponents[id] {
			t.Errorf("the tour lacks component %q", id)
		}
	}

	// Nothing but a component of a surface the conversation drew can act.
	for name, body := range map[string]string{
		"unknown surface":   actionBody("elsewhere", "next", "continue", "", "{}"),
		"unknown component": actionBody(tourSurface, "ghost", "continue", "", "{}"),
		"not a message":     `{"version":"v1.0"}`,
	} {
		if rec := uiEvent(t, a, conv, body); rec.Code != 400 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := uiEvent(t, a, conv, actionBody(tourSurface, "next", "continue", "Continue to the form", "{}")); rec.Code != 201 {
		t.Fatalf("the action: %d %s", rec.Code, rec.Body)
	}
	if rec := uiEvent(t, a, conv, actionBody(tourSurface, "next", "continue", "", "{}")); rec.Code != 409 {
		t.Errorf("while the run works another action is refused: %d", rec.Code)
	}
	a.worker.RunOnce(ctx)

	// The run now asks the page, without a word to the user, how big its window
	// is. The engine gave the call its own id; the page answers with it.
	callID := lastRendererCall(t, a, conv)
	if !strings.HasPrefix(callID, "call-") {
		t.Fatalf("the call id was not assigned by the assistant: %q", callID)
	}
	answer := `{"version":"v1.0","rendererFunctionResponse":{"functionCallId":"` + callID + `","value":{"width":1280,"height":900,"pixelRatio":2,"colorScheme":"dark"}}}`
	if rec := uiEvent(t, a, conv, answer); rec.Code != 201 {
		t.Fatalf("the answer: %d %s", rec.Code, rec.Body)
	}
	if rec := uiEvent(t, a, conv, answer); rec.Code != 400 {
		t.Errorf("an answer is taken once: %d", rec.Code)
	}
	a.worker.RunOnce(ctx)
	formSurface, formComponents := lastInterface(t, a, conv)
	if formSurface == tourSurface || !formComponents["submit"] {
		t.Fatalf("the form was not shown: surface %q components %v", formSurface, formComponents)
	}

	context := `{"name":"Janusz","topics":["languages","hiking"],"level":4,"agree":true}`
	if rec := uiEvent(t, a, conv, actionBody(formSurface, "submit", "submit_form", "Submitted the form", context)); rec.Code != 201 {
		t.Fatalf("the submit: %d %s", rec.Code, rec.Body)
	}
	a.worker.RunOnce(ctx)
	got = chatTexts(t, a, conv)
	last := got[len(got)-1]
	if !strings.HasPrefix(last, "assistant/result: Thank you, Janusz. You chose hiking, languages, at level 4.") {
		t.Fatalf("the end: %v", got)
	}
	var kinds []string
	for _, line := range got {
		kinds = append(kinds, strings.SplitN(line, ":", 2)[0])
	}
	want := "user/text,assistant/question,assistant/ui,user/ui_action,assistant/ui,user/ui_action,assistant/question,assistant/ui,user/ui_action,assistant/result"
	if strings.Join(kinds, ",") != want {
		t.Errorf("the conversation read:\n got %s\nwant %s", strings.Join(kinds, ","), want)
	}
	if got[3] != "user/ui_action: Continue to the form" || got[8] != "user/ui_action: Submitted the form" {
		t.Errorf("an action shows as the user's own words: %v", got)
	}
}

func TestAnInterfaceErrorFromThePageIsRecordedThroughTheAPI(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	conv := startChat(t, a, "ui-demo")
	say(t, a, conv, "start")
	a.worker.RunOnce(context.Background())
	rec := uiEvent(t, a, conv, `{"version":"v1.0","error":{"code":"RENDERER_ERROR","surfaceId":"x","message":"it broke"}}`)
	if rec.Code != 201 || field(t, rec, "kind") != "ui_action" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := uiEvent(t, a, conv, `{"version":"v1.0","callAgentFunction":{"surfaceId":"x","functionCallId":"c","callFunction":{"@call":"formatNumber","catalogId":"https://a2ui.org/specification/v1_0/catalogs/basic/catalog.json","args":{"value":1}}}}`); rec.Code != 400 {
		t.Errorf("function calls are not handled yet: %d", rec.Code)
	}
	if rec := uiEvent(t, a, "00000000-0000-0000-0000-000000000000", `{"version":"v1.0","error":{"code":"X","surfaceId":"x","message":"m"}}`); rec.Code != 404 {
		t.Errorf("unknown conversation: %d", rec.Code)
	}
}

// The page calls the assistant: the tour's own Text asks for the workflows, and
// the answer is in the conversation.
func TestThePageCanAskTheAssistantForTheWorkflowsThroughTheAPI(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	conv := startChat(t, a, "ui-demo")
	say(t, a, conv, "start")
	a.worker.RunOnce(context.Background())
	surface, _ := lastInterface(t, a, conv)
	catalog := "https://assistant.k8s-lab.local/a2ui/catalog.json"
	callBody := `{"version":"v1.0","callAgentFunction":{"surfaceId":"` + surface + `","functionCallId":"page-1","callFunction":{"@call":"listWorkflows","catalogId":"` + catalog + `","args":{}}}}`
	if rec := uiEvent(t, a, conv, callBody); rec.Code != 201 {
		t.Fatalf("the call: %d %s", rec.Code, rec.Body)
	}
	if rec := uiEvent(t, a, conv, callBody); rec.Code != 400 {
		t.Errorf("a used id: %d", rec.Code)
	}
	rec := call(t, a.handler, "GET", "/api/v1/conversations/"+conv+"/messages", "")
	if !strings.Contains(rec.Body.String(), `agentFunctionResponse`) || !strings.Contains(rec.Body.String(), `ui-demo: `) || !strings.Contains(rec.Body.String(), `"functionCallId":"page-1"`) {
		t.Errorf("the answer is not in the conversation: %s", rec.Body)
	}
}

// The orchestrator, end to end with the real engine, the demo model and the HTTP
// API: a request is routed to the workflow that fits, the conversation says so
// and follows it, the result comes back to the orchestrator, and a request that
// fits nothing is answered by the orchestrator itself.
func TestTheOrchestratorHandsARequestToAWorkflowAndTheConversationFollowsIt(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	conv := startChat(t, a, "orchestrator")
	ctx := context.Background()
	handledBy := func() any {
		return field(t, call(t, a.handler, "GET", "/api/v1/conversations/"+conv, ""), "handled_by")
	}

	if code := say(t, a, conv, "please say hello to me"); code != 201 {
		t.Fatalf("first message: %d", code)
	}
	a.worker.RunOnce(ctx)
	got := chatTexts(t, a, conv)
	want := []string{
		"user/text: please say hello to me",
		`assistant/info: → hello: you mentioned "hello"`,
		"assistant/question: What is your name?",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("after routing:\n got %q\nwant %q", got, want)
	}
	if handledBy() != "hello" {
		t.Errorf("handled_by = %v: the question comes from the hello workflow", handledBy())
	}

	// The answer goes to the workflow that asked, not to the orchestrator.
	if code := say(t, a, conv, "Ada"); code != 201 {
		t.Fatalf("answer: %d", code)
	}
	a.worker.RunOnce(ctx)
	got = chatTexts(t, a, conv)
	if last := got[len(got)-1]; !strings.HasPrefix(last, "assistant/result: The hello workflow finished.") {
		t.Fatalf("the end: %v", got)
	}
	if handledBy() != nil {
		t.Errorf("handled_by = %v after the run ended", handledBy())
	}

	// One trace for the whole run, the child's steps marked and in order.
	view := call(t, a.handler, "GET", "/api/v1/conversations/"+conv, "")
	steps := call(t, a.handler, "GET", "/api/v1/tasks/"+field(t, view, "task_id").(string)+"/steps", "")
	var body struct {
		Items []struct {
			Step       int             `json:"step"`
			State      string          `json:"state"`
			WorkflowID string          `json:"workflow_id"`
			Depth      int             `json:"depth"`
			Action     json.RawMessage `json:"action"`
		} `json:"items"`
	}
	if err := json.Unmarshal(steps.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var trail []string
	for _, s := range body.Items {
		trail = append(trail, fmt.Sprintf("%d:%s:%d", s.Step, s.WorkflowID, s.Depth))
	}
	if got := strings.Join(trail, " "); got != "1:orchestrator:0 2:hello:1 3:hello:1 4:hello:1 5:orchestrator:0" {
		t.Errorf("trace = %s", got)
	}
	if !strings.Contains(string(body.Items[0].Action), `"type":"delegate"`) || !strings.Contains(string(body.Items[0].Action), `"workflow":"hello"`) {
		t.Errorf("the delegation is in the trace: %s", body.Items[0].Action)
	}

	// A request nothing fits is answered by the orchestrator, with no routing line.
	say(t, a, conv, "what is the weather")
	a.worker.RunOnce(ctx)
	got = chatTexts(t, a, conv)
	if last := got[len(got)-1]; !strings.HasPrefix(last, "assistant/result: I have no workflow for that. I can hand you to: hello, ui-demo.") {
		t.Errorf("no match: %v", got)
	}
	for _, line := range got[len(got)-3:] {
		if strings.HasPrefix(line, "assistant/info: →") {
			t.Errorf("a routing line without routing: %v", got)
		}
	}

	// An unclear request gets one question from the orchestrator itself.
	say(t, a, conv, "help")
	a.worker.RunOnce(ctx)
	got = chatTexts(t, a, conv)
	if last := got[len(got)-1]; !strings.HasPrefix(last, "assistant/question: What would you like to do? I can hand you to: hello, ui-demo.") {
		t.Errorf("a question: %v", got)
	}
	if handledBy() != nil {
		t.Errorf("the orchestrator itself asks: %v", handledBy())
	}
	// Its answer is routed like any message.
	say(t, a, conv, "the interface components please")
	a.worker.RunOnce(ctx)
	got = chatTexts(t, a, conv)
	if !strings.Contains(strings.Join(got[len(got)-3:], "\n"), `assistant/info: → ui-demo: you mentioned "interface"`) {
		t.Errorf("routed to the interface demo: %v", got)
	}
	if handledBy() != "ui-demo" {
		t.Errorf("handled_by = %v", handledBy())
	}
	surface, components := lastInterface(t, a, conv)
	if !components["next"] {
		t.Fatalf("the child's interface was not shown: %q %v", surface, components)
	}
	// The user's action on the child's interface goes to the child.
	if rec := uiEvent(t, a, conv, actionBody(surface, "next", "continue", "Continue to the form", "{}")); rec.Code != 201 {
		t.Fatalf("the action: %d %s", rec.Code, rec.Body)
	}
	a.worker.RunOnce(ctx)
	if handledBy() != "ui-demo" {
		t.Errorf("handled_by = %v after the action", handledBy())
	}
	if id := lastRendererCall(t, a, conv); !strings.HasPrefix(id, "call-") {
		t.Errorf("the child asked the page for its window: %q", id)
	}
}

func TestOnlyWorkflowsThatDeclareIntentsAreOfferedForRouting(t *testing.T) {
	pool := dbtest.Pool(t)
	a := testApp(t, pool, &recordingClock{}, nil)
	rec := call(t, a.handler, "GET", "/api/v1/workflows", "")
	var body struct {
		Items []struct {
			ID      string `json:"id"`
			Intents *struct {
				Description string   `json:"description"`
				Examples    []string `json:"examples"`
			} `json:"intents"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	offered := map[string]bool{}
	for _, w := range body.Items {
		offered[w.ID] = w.Intents != nil
	}
	if !offered["hello"] || !offered["ui-demo"] || offered["orchestrator"] || offered["noop"] || offered["ask"] || offered["askflow"] {
		t.Errorf("offered = %v", offered)
	}
	for _, w := range body.Items {
		if w.ID == "hello" && (w.Intents == nil || len(w.Intents.Examples) == 0 || w.Intents.Description == "") {
			t.Errorf("hello's intents = %+v", w.Intents)
		}
	}
}
