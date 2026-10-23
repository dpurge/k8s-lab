package workflow_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"assistant/internal/workflow"
)

const uiComposePath = "../../workflows/ui-compose.yaml"

const composeRequest = "make a form that asks for my name"

// uiComposeHarness is the real ui-compose workflow run with the UI validator
// and the given scripted replies.
func uiComposeHarness(t *testing.T, steps ...string) *harness {
	t.Helper()
	data, err := os.ReadFile(uiComposePath)
	if err != nil {
		t.Fatal(err)
	}
	h := uiHarness(t, uiFlowYAML, steps...)
	wf, err := workflow.LoadBytes(uiComposePath, data)
	if err != nil {
		t.Fatalf("load ui-compose: %v", err)
	}
	h.wf = wf
	return h
}

// The model cannot write the interface three times in a row: the run falls
// back to asking the request as a plain question, then ends with the answer.
func TestUICompose_FallsBackToAPlainQuestionWhenTheInterfaceCannotBeWritten(t *testing.T) {
	steps := append([]string{}, workflow.ProdInvalidReplies...)
	steps = append(steps,
		askReply(`{"request":"`+composeRequest+`"}`, `{"question":"What is your name?"}`),
		gotoReply(`{"received":{"answer":"Ada"}}`, "summarise"),
		gotoReply(`{"summary":"The user entered the name Ada."}`, "done"),
	)
	h := uiComposeHarness(t, steps...)

	run, res := h.start(workflow.Input{Message: composeRequest})
	if res.Status != workflow.StatusWaiting {
		t.Fatalf("status = %s, failure %+v", res.Status, run.Failure)
	}
	if _, found := res.Ask["question"]; !found {
		t.Errorf("the ask has no question: %v", res.Ask)
	}
	if _, found := res.Ask["ui"]; found {
		t.Errorf("the fallback must ask in plain words, got an interface: %v", res.Ask)
	}
	if !run.ErrorEdgeUsed || run.StateName != "plain" {
		t.Errorf("on_error edge: used=%v state=%s", run.ErrorEdgeUsed, run.StateName)
	}
	// What the fallback state was given: the failure and the user's text.
	obs := failedStepObservation(t, h, 3)
	if obs[workflow.FailedObservationKey] != composeRequest {
		t.Errorf("the plain state was not given the user's text: %v", obs)
	}
	contains(t, requestInput(t, h, 3), composeRequest, "request sent to plain")
	contains(t, requestInput(t, h, 3), `"name":"plain"`, "the fourth request is for plain")

	res, err := h.eng.Resume(context.Background(), h.wf, run, "Ada")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.Status != workflow.StatusSucceeded {
		t.Fatalf("status = %s, failure %+v", res.Status, run.Failure)
	}
	received, _ := run.State["received"].(map[string]any)
	if received["answer"] != "Ada" {
		t.Errorf("received = %v", run.State["received"])
	}
	if run.State["request"] != composeRequest {
		t.Errorf("request = %v", run.State["request"])
	}
	wantOutcomes(t, h,
		workflow.OutcomeRetry, workflow.OutcomeRetry, workflow.OutcomeRejected, // compose: three refused replies
		workflow.OutcomeOK, workflow.OutcomeOK, workflow.OutcomeOK, // plain asks, plain goes on, summarise
	)
	recs := h.rec.Records()
	if recs[2].State != "compose" || recs[3].State != "plain" {
		t.Errorf("states of the rejected and the next record: %s, %s", recs[2].State, recs[3].State)
	}
	h.wantNoScriptProblems()
}

// The instructions of the shipped workflow name the observation key the engine
// uses; if either is renamed alone, the model is told a key that is not there.
func TestUIComposeInstructionsNameTheFailedObservationKey(t *testing.T) {
	wf, err := workflow.Load(uiComposePath)
	if err != nil {
		t.Fatal(err)
	}
	plain, ok := wf.States["plain"]
	if !ok {
		t.Fatal("ui-compose has no state plain")
	}
	if want := `"` + workflow.FailedObservationKey + `"`; !strings.Contains(plain.Instructions, want) {
		t.Errorf("the plain instructions do not mention %s:\n%s", want, plain.Instructions)
	}
	if wf.States["compose"].OnError != "plain" {
		t.Errorf("compose on_error = %q", wf.States["compose"].OnError)
	}
}
