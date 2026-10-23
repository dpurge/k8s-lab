package workflow_test

import (
	"os"
	"path/filepath"
	"testing"

	"assistant/internal/workflow"
)

const shippedDir = "../../workflows"

// A shipped workflow without an on_error edge can leave a run failed, or a task
// waiting for ever, when the model cannot follow it. Lint already checks that
// every on_error target exists, so this only checks that one is declared.
var noOnErrorAllowed = map[string]string{
	"hello": "deterministic demo, no model step, so nothing can fail the way a model does",
}

func TestEveryShippedWorkflowDeclaresOnErrorOrIsAllowListed(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(shippedDir, "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no shipped workflows found in %s: %v", shippedDir, err)
	}
	for _, file := range files {
		wf, err := workflow.Load(file)
		if err != nil {
			t.Errorf("%s: %v", file, err)
			continue
		}
		declared := false
		for _, st := range wf.States {
			declared = declared || st.OnError != ""
		}
		reason, allowed := noOnErrorAllowed[wf.ID]
		switch {
		case declared && allowed:
			t.Errorf("%s: %q is on the allow-list (%s) but now declares on_error; remove it from noOnErrorAllowed", file, wf.ID, reason)
		case !declared && !allowed:
			t.Errorf("%s: no state declares on_error; add an on_error fallback state, or add %q to noOnErrorAllowed with the reason", file, wf.ID)
		}
	}
}

// shippedHarness is a real shipped workflow run with the UI validator and the
// given scripted replies.
func shippedHarness(t *testing.T, name string, steps ...string) *harness {
	t.Helper()
	path := filepath.Join(shippedDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := uiHarness(t, uiFlowYAML, steps...)
	wf, err := workflow.LoadBytes(path, data)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	h.wf = wf
	return h
}

// wantFallbackRun checks a run that took the on_error edge once, the target
// was given the failed step's input, and the run succeeded.
func wantFallbackRun(t *testing.T, h *harness, run *workflow.Run, res workflow.Result, userText string) {
	t.Helper()
	if res.Status != workflow.StatusSucceeded {
		t.Fatalf("status = %s, failure %+v", res.Status, run.Failure)
	}
	if !run.ErrorEdgeUsed {
		t.Error("the on_error edge was not taken")
	}
	wantOutcomes(t, h,
		workflow.OutcomeRetry, workflow.OutcomeRetry, workflow.OutcomeRejected, // three refused replies
		workflow.OutcomeOK, // the fallback finishes the run
	)
	obs := failedStepObservation(t, h, 3)
	if obs[workflow.FailedObservationKey] != userText {
		t.Errorf("the fallback was not given the user's text: %v", obs)
	}
	h.wantNoScriptProblems()
}

func TestOrchestratorGivesUpWithAnApologyWhenItCannotRoute(t *testing.T) {
	const message = "make me a form that asks for my name"
	steps := append([]string{}, workflow.ProdInvalidReplies...)
	steps = append(steps, gotoReply(`{"reply":"Sorry, I could not route or answer that. Please rephrase it or try again."}`, "done"))
	h := shippedHarness(t, "orchestrator.yaml", steps...)

	run, res := h.start(workflow.Input{Message: message})

	wantFallbackRun(t, h, run, res, message)
	if reply, _ := run.State["reply"].(string); reply == "" {
		t.Errorf("reply is empty: %v", run.State)
	}
	if got := h.rec.Records()[3].State; got != "give_up" {
		t.Errorf("the fourth record is for %q, want give_up", got)
	}
	contains(t, requestInput(t, h, 3), message, "request sent to give_up")
}

func TestHelloLiveFinishesInPlainWordsWhenTheModelCannotFollowIt(t *testing.T) {
	const message = "hi"
	steps := append([]string{}, workflow.ProdInvalidReplies...)
	steps = append(steps, gotoReply(`{"name":"unknown","greeting":"The full greeting could not be completed."}`, "done"))
	h := shippedHarness(t, "hello-live.yaml", steps...)

	run, res := h.start(workflow.Input{Message: message})

	wantFallbackRun(t, h, run, res, message)
	if got := h.rec.Records()[3].State; got != "fallback" {
		t.Errorf("the fourth record is for %q, want fallback", got)
	}
	if run.State["greeting"] == "" || run.State["name"] == "" {
		t.Errorf("required keys not recorded: %v", run.State)
	}
}

func TestUIDemoFinishesInPlainWordsWhenTheInterfaceCannotBeWritten(t *testing.T) {
	const message = "show me the interface components"
	steps := append([]string{}, workflow.ProdInvalidReplies...)
	steps = append(steps, gotoReply(`{"summary":"The interface demo could not be completed."}`, "done"))
	h := shippedHarness(t, "ui-demo.yaml", steps...)

	run, res := h.start(workflow.Input{Message: message})

	wantFallbackRun(t, h, run, res, message)
	if got := h.rec.Records()[3].State; got != "fallback" {
		t.Errorf("the fourth record is for %q, want fallback", got)
	}
	if run.State["summary"] == "" {
		t.Errorf("summary not recorded: %v", run.State)
	}
}
