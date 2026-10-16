package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"assistant/internal/tasks"
	"assistant/internal/workflow"
	"assistant/internal/workflow/workflowtest"
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

func engineFor(t *testing.T, yaml string, steps ...workflowtest.Step) (*EngineRunner, *workflowtest.ScriptedLLM, *workflowtest.MemoryRecorder) {
	t.Helper()
	wf, err := workflow.LoadBytes("askflow.yaml", []byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := workflow.NewRegistry()
	prompts, err := workflow.DefaultPrompts()
	if err != nil {
		t.Fatal(err)
	}
	llm := workflowtest.NewScriptedLLM(steps...)
	rec := &workflowtest.MemoryRecorder{}
	return &EngineRunner{
		Engine:    &workflow.Engine{LLM: llm, Tools: reg, Prompts: prompts, Recorder: rec},
		Workflows: map[string]*workflow.Workflow{wf.ID: wf},
	}, llm, rec
}

func reply(patch, action string) workflowtest.Step {
	return workflowtest.Reply(`{"state_patch":` + patch + `,"action":` + action + `}`)
}

func askTask(payload string) tasks.Task {
	return tasks.Task{ID: "11111111-2222-4333-8444-555555555555", WorkflowID: "askflow", Payload: json.RawMessage(payload)}
}

func TestARunThatAsksTheUserIsParkedWithItsQuestionAndResumedWithTheAnswer(t *testing.T) {
	e, llm, rec := engineFor(t, askFlow,
		reply(`{}`, `{"type":"ask_user","payload":{"question":"What is your name?"}}`),
		reply(`{"answer":"Ada"}`, `{"type":"goto","state":"done"}`),
	)

	first, err := e.Run(context.Background(), askTask(`{"message":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Waiting || string(first.Ask) != `{"question":"What is your name?"}` || len(first.Run) == 0 || first.Result != nil {
		t.Fatalf("first = %+v", first)
	}

	parked := askTask(`{"message":"hello"}`)
	parked.Run, parked.ResumeMessage = first.Run, json.RawMessage(`"Ada"`)
	second, err := e.Run(context.Background(), parked)
	if err != nil {
		t.Fatal(err)
	}
	if second.Waiting || string(second.Result) != `{"state":{"answer":"Ada"}}` {
		t.Fatalf("second = %+v", second)
	}
	reqs := llm.Requests()
	if !strings.Contains(reqs[1].Input, `"observation":"Ada"`) {
		t.Errorf("the resumed step must see the answer: %s", reqs[1].Input)
	}
	for _, r := range rec.Records() {
		if r.RunID != "11111111-2222-4333-8444-555555555555" {
			t.Errorf("the run id must be the task id, got %q", r.RunID)
		}
	}
}

func TestTheFinalRunSnapshotComesBackOnSuccess(t *testing.T) {
	e, _, _ := engineFor(t, askFlow, reply(`{"answer":"x"}`, `{"type":"goto","state":"done"}`))
	out, err := e.Run(context.Background(), askTask(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	run, err := workflow.RestoreRun(out.Run)
	if err != nil || run.StateName != "done" || run.Status != workflow.StatusSucceeded {
		t.Errorf("run = %+v %v", run, err)
	}
}

func TestAFailedRunFailsTheTaskWithTheEnginesReasonAndKeepsTheRun(t *testing.T) {
	e, _, _ := engineFor(t, askFlow,
		workflowtest.Reply("not json at all"),
		workflowtest.Reply("still not json"),
	)
	out, err := e.Run(context.Background(), askTask(`{}`))
	if err == nil || !strings.HasPrefix(err.Error(), "retries_exhausted: ") {
		t.Fatalf("err = %v", err)
	}
	if len(out.Run) == 0 || out.Waiting {
		t.Errorf("the failed run must be kept for inspection: %+v", out)
	}
	run, _ := workflow.RestoreRun(out.Run)
	if run.Status != workflow.StatusFailed || run.Failure == nil {
		t.Errorf("run = %+v", run)
	}
}

func TestAModelThatCannotBeCalledFailsTheTaskWithTheModelsError(t *testing.T) {
	e, _, _ := engineFor(t, askFlow, workflowtest.Step{Err: errors.New("no model has tags [chat]")})
	_, err := e.Run(context.Background(), askTask(`{}`))
	if err == nil || !strings.Contains(err.Error(), "llm_error") || !strings.Contains(err.Error(), "no model has tags [chat]") {
		t.Errorf("err = %v", err)
	}
}

func TestAPayloadThatIsNotAnEngineInputFailsBeforeAnyModelCall(t *testing.T) {
	for name, payload := range map[string]string{
		"unknown key": `{"message":"x","temperature":1}`,
		"not object":  `[]`,
		"state type":  `{"state":"nope"}`,
		"empty":       ``,
	} {
		e, llm, _ := engineFor(t, askFlow)
		_, err := e.Run(context.Background(), askTask(payload))
		if err == nil || !strings.Contains(err.Error(), "payload must be an object with message") {
			t.Errorf("%s: %v", name, err)
		}
		if len(llm.Requests()) != 0 {
			t.Errorf("%s: no model call for a bad payload", name)
		}
	}
}

func TestAnInitialStateThatBreaksTheSchemaFailsTheTask(t *testing.T) {
	e, _, _ := engineFor(t, askFlow)
	_, err := e.Run(context.Background(), askTask(`{"state":{"bogus":1}}`))
	if err == nil || !strings.Contains(err.Error(), "initial state") {
		t.Errorf("err = %v", err)
	}
}

func TestAnInitialStateAndLanguageReachTheRun(t *testing.T) {
	e, llm, _ := engineFor(t, askFlow, reply(`{}`, `{"type":"ask_user","payload":{"question":"?"}}`))
	if _, err := e.Run(context.Background(), askTask(`{"state":{"answer":"seed"},"language":"arb"}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llm.Requests()[0].Input, `"answer":"seed"`) {
		t.Errorf("input = %s", llm.Requests()[0].Input)
	}
}

func TestAResumeWithoutAnAnswerOrWithABrokenRunIsAnErrorNotAPanic(t *testing.T) {
	e, llm, _ := engineFor(t, askFlow)
	task := askTask(`{}`)
	task.Run = json.RawMessage(`{"run_id":"x"}`)
	if _, err := e.Run(context.Background(), task); err == nil || !strings.Contains(err.Error(), "no answer to resume") {
		t.Errorf("no answer: %v", err)
	}
	task.ResumeMessage = json.RawMessage(`"a"`)
	task.Run = json.RawMessage(`not json`)
	if _, err := e.Run(context.Background(), task); err == nil || !strings.Contains(err.Error(), "stored run cannot be read") {
		t.Errorf("broken run: %v", err)
	}
	task.Run = json.RawMessage(`{"run_id":"x","workflow_id":"askflow","status":"succeeded","state_name":"done"}`)
	_, err := e.Run(context.Background(), task)
	if err == nil || !errors.Is(err, workflow.ErrNotWaiting) {
		t.Errorf("a run that is not waiting cannot be resumed: %v", err)
	}
	if len(llm.Requests()) != 0 {
		t.Error("no model call")
	}
}

func TestRunnerPrefersBuiltinsAndFallsBackToYAMLWorkflowsAndThenToUnknown(t *testing.T) {
	e, _, _ := engineFor(t, askFlow, reply(`{}`, `{"type":"ask_user","payload":{"question":"?"}}`))
	r := NewRunner(New(&fakeClock{}), e)

	out, err := r.Run(context.Background(), task("noop", `{"hello":"world"}`))
	if err != nil || string(out.Result) != `{"hello":"world"}` || out.Waiting {
		t.Errorf("noop: %+v %v", out, err)
	}
	out, err = r.Run(context.Background(), askTask(`{}`))
	if err != nil || !out.Waiting {
		t.Errorf("yaml: %+v %v", out, err)
	}
	if _, err := r.Run(context.Background(), task("nope", `{}`)); err == nil || err.Error() != "unknown workflow: nope" {
		t.Errorf("unknown: %v", err)
	}
	if got := strings.Join(r.Names(), ","); got != "askflow,noop" {
		t.Errorf("names = %s", got)
	}
}

func TestARunnerWithNoEngineStillRunsBuiltinsAndRefusesOthers(t *testing.T) {
	r := NewRunner(New(&fakeClock{}), nil)
	if _, err := r.Run(context.Background(), task("noop", `{}`)); err != nil {
		t.Error(err)
	}
	if _, err := r.Run(context.Background(), task("askflow", `{}`)); err == nil || err.Error() != "unknown workflow: askflow" {
		t.Errorf("err = %v", err)
	}
}

// ── loading ──────────────────────────────────────────────────────────────────

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadDirLoadsEveryYAMLWorkflowById(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a.yaml":    askFlow,
		"b.yaml":    strings.Replace(askFlow, "id: askflow", "id: other", 1),
		"notes.txt": "ignored",
	})
	got, err := LoadDir(dir, workflow.NameSet{}, []string{"noop"})
	if err != nil || len(got) != 2 || got["askflow"] == nil || got["other"] == nil {
		t.Errorf("%v %v", got, err)
	}
}

func TestLoadDirWithNoWorkflowsIsNotAnError(t *testing.T) {
	got, err := LoadDir(t.TempDir(), nil, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("%v %v", got, err)
	}
}

func TestLoadDirReportsEveryProblemOfEveryFileWithFileAndLine(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"bad-schema.yaml": strings.Replace(askFlow, "version: 1", "version: zero", 1),
		"bad-lint.yaml":   strings.Replace(askFlow, "next: [done]", "next: [missing]", 1),
		"good.yaml":       strings.Replace(askFlow, "id: askflow", "id: good", 1),
	})
	_, err := LoadDir(dir, workflow.NameSet{}, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	for _, want := range []string{"bad-schema.yaml:", "version", "bad-lint.yaml:", `next state "missing"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "good.yaml") {
		t.Errorf("a good file is not a problem:\n%s", msg)
	}
	if strings.Count(msg, "\n") < 1 {
		t.Errorf("each problem is on its own line:\n%s", msg)
	}
}

func TestLoadDirChecksToolNames(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.yaml": strings.Replace(askFlow, "    next: [done]", "    tools: [mystery]\n    next: [done]", 1)})
	_, err := LoadDir(dir, workflow.NameSet{"known": true}, nil)
	if err == nil || !strings.Contains(err.Error(), `unknown tool "mystery"`) {
		t.Errorf("err = %v", err)
	}
}

func TestLoadDirRefusesAnIdTheBuiltinsOwnAndADuplicate(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a.yaml": strings.Replace(askFlow, "id: askflow", "id: noop", 1),
		"b.yaml": strings.Replace(askFlow, "id: askflow", "id: twice", 1),
		"c.yaml": strings.Replace(askFlow, "id: askflow", "id: twice", 1),
	})
	_, err := LoadDir(dir, nil, []string{"noop"})
	if err == nil || !strings.Contains(err.Error(), `a.yaml: workflow id "noop" is a built-in workflow's`) || !strings.Contains(err.Error(), `c.yaml: workflow id "twice" is already used by`) {
		t.Errorf("err = %v", err)
	}
}

func TestTheCommittedLearnTopicLoadsWithTheStubToolNames(t *testing.T) {
	got, err := LoadDir("../../workflows", workflow.NameSet{"memory_search": true, "memory_add": true, "rag_search": true, "web_search": true}, []string{"noop", "ask"})
	if err != nil || got["learn-topic"] == nil || got["hello"] == nil {
		t.Errorf("%v %v", got, err)
	}
}
