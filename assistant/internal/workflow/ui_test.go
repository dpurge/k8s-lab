package workflow_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"assistant/internal/a2ui"
	"assistant/internal/workflow"
)

const uiFlowYAML = `id: uiflow
version: 1
description: Shows a form and records what the user did.
state_schema:
  type: object
  properties:
    chosen: {type: string}
  additionalProperties: false
start: show
limits: {max_steps: 10, max_tokens: 100000, max_retries: 2, max_visits: 3}
ui:
  catalogs: [basic]
states:
  show:
    instructions: Show a form, then record the button's name in chosen.
    next: [done]
  done:
    instructions: Finished.
    end: true
`

const plainFlowYAML = `id: plainflow
version: 1
description: No interfaces.
state_schema:
  type: object
  properties:
    chosen: {type: string}
  additionalProperties: false
start: show
limits: {max_steps: 10, max_tokens: 100000, max_retries: 1, max_visits: 3}
states:
  show:
    instructions: Ask.
    next: [done]
  done:
    instructions: Finished.
    end: true
`

func uiHarness(t *testing.T, yaml string, steps ...string) *harness {
	t.Helper()
	h := newHarness(t, yaml, replies(steps...)...)
	v, err := a2ui.New()
	if err != nil {
		t.Fatal(err)
	}
	h.eng.UI = v
	h.eng.Prompts.UIGuide = v.Guide()
	return h
}

// validReply is the guide's own example: a complete, valid reply.
const validReply = a2ui.GuideExample

func invalidReply(componentType string) string {
	return askReply(`{}`, `{"question":"Q","ui":[{"version":"v1.0","createSurface":{"surfaceId":"s","catalogId":"`+a2ui.CatalogID+`","components":[{"id":"root","component":"`+componentType+`"}]}}]}`)
}

func systemOf(t *testing.T, h *harness, i int) string {
	t.Helper()
	reqs := h.llm.Requests()
	if i >= len(reqs) {
		t.Fatalf("only %d requests", len(reqs))
	}
	return reqs[i].System
}

func TestAWorkflowThatDeclaresUIGetsTheGuideInItsPromptAndOthersDoNot(t *testing.T) {
	h := uiHarness(t, uiFlowYAML, validReply)
	_, res := h.start(workflow.Input{Message: "ask me for my name"})
	if res.Status != workflow.StatusWaiting {
		t.Fatalf("status %v", res.Status)
	}
	system := systemOf(t, h, 0)
	for _, want := range []string{"INTERFACES", "- Button: action (action)", "- TextField: label", "Complete reply, as one line"} {
		if !strings.Contains(system, want) {
			t.Errorf("the system prompt lacks %q", want)
		}
	}
	plain := uiHarness(t, plainFlowYAML, askReply(`{}`, `{"question":"Q"}`))
	plain.start(workflow.Input{Message: "hi"})
	if strings.Contains(systemOf(t, plain, 0), "INTERFACES") {
		t.Error("a workflow without ui must not get the guide")
	}
}

func TestAValidInterfaceIsAcceptedAndComesOutInTheAskPayload(t *testing.T) {
	h := uiHarness(t, uiFlowYAML, validReply)
	_, res := h.start(workflow.Input{Message: "x"})
	if res.Status != workflow.StatusWaiting {
		t.Fatalf("status %v failure %+v", res.Status, res.Failure)
	}
	ui, ok := res.Ask["ui"].([]any)
	if !ok || len(ui) != 1 || res.Ask["question"] != "Tell me where to send it." {
		t.Errorf("ask = %v", res.Ask)
	}
	wantOutcomes(t, h, workflow.OutcomeOK)
}

func TestAnInvalidInterfaceIsSentBackWithItsProblemsAndACorrectedOneIsAccepted(t *testing.T) {
	h := uiHarness(t, uiFlowYAML, invalidReply("Hologram"), validReply)
	_, res := h.start(workflow.Input{Message: "x"})
	if res.Status != workflow.StatusWaiting {
		t.Fatalf("status %v failure %+v", res.Status, res.Failure)
	}
	wantOutcomes(t, h, workflow.OutcomeRetry, workflow.OutcomeOK)
	var in struct {
		Retry struct{ Reply, Error string } `json:"retry"`
	}
	if err := json.Unmarshal([]byte(h.llm.Requests()[1].Input), &in); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"payload.ui is not valid A2UI", `unknown component type "Hologram"`, "ui[0]/createSurface/components/0/component"} {
		if !strings.Contains(in.Retry.Error, want) {
			t.Errorf("the retry error lacks %q: %s", want, in.Retry.Error)
		}
	}
	if !strings.Contains(in.Retry.Reply, "Hologram") {
		t.Errorf("the retry shows the refused reply: %s", in.Retry.Reply)
	}
}

func TestAnInterfaceThatStaysInvalidFailsTheRunWhenTheRetriesRunOut(t *testing.T) {
	h := uiHarness(t, uiFlowYAML, invalidReply("Hologram"), invalidReply("Hologram"), invalidReply("Hologram"), validReply)
	_, res := h.start(workflow.Input{Message: "x"})
	if res.Status != workflow.StatusFailed || res.Failure == nil || res.Failure.Reason != workflow.FailRetriesExhausted || !strings.Contains(res.Failure.Message, "Hologram") {
		t.Fatalf("result %+v failure %+v", res, res.Failure)
	}
	if got := len(h.llm.Requests()); got != 3 {
		t.Errorf("max_retries 2 means three calls, not %d", got)
	}
	wantOutcomes(t, h, workflow.OutcomeRetry, workflow.OutcomeRetry, workflow.OutcomeRejected)
}

func TestAnInterfaceThatIsNotAnArrayOrIsIncompleteIsRefused(t *testing.T) {
	notArray := askReply(`{}`, `{"question":"Q","ui":{"version":"v1.0"}}`)
	noRoot := askReply(`{}`, `{"question":"Q","ui":[{"version":"v1.0","createSurface":{"surfaceId":"s","catalogId":"`+a2ui.CatalogID+`","components":[{"id":"main","component":"Text","text":"x"}]}}]}`)
	for name, bad := range map[string]string{"not an array": notArray, "no root": noRoot} {
		h := uiHarness(t, uiFlowYAML, bad, validReply)
		_, res := h.start(workflow.Input{Message: "x"})
		if res.Status != workflow.StatusWaiting {
			t.Fatalf("%s: status %v", name, res.Status)
		}
		wantOutcomes(t, h, workflow.OutcomeRetry, workflow.OutcomeOK)
		if !strings.Contains(h.rec.Records()[0].Error, "payload.ui") {
			t.Errorf("%s: error %q", name, h.rec.Records()[0].Error)
		}
	}
}

func TestAWorkflowWithoutUIIsNotCheckedHere(t *testing.T) {
	h := uiHarness(t, plainFlowYAML, invalidReply("Hologram"))
	_, res := h.start(workflow.Input{Message: "x"})
	if res.Status != workflow.StatusWaiting {
		t.Errorf("status %v: the interface is checked when it is shown", res.Status)
	}
}

func TestTheUsersActionComesBackAsTheObservationAndTheRunGoesOn(t *testing.T) {
	h := uiHarness(t, uiFlowYAML, validReply, gotoReply(`{"chosen":"send"}`, "done"))
	run, res := h.start(workflow.Input{Message: "x"})
	if res.Status != workflow.StatusWaiting {
		t.Fatalf("status %v", res.Status)
	}
	action := map[string]any{"ui_action": map[string]any{"name": "send", "context": map[string]any{"name": "Ada", "topics": []any{"news"}}}}
	res, err := h.eng.Resume(context.Background(), h.wf, run, action)
	if err != nil || res.Status != workflow.StatusSucceeded {
		t.Fatalf("resume: %v %+v", err, res)
	}
	contains(t, requestInput(t, h, 1), `"ui_action":{"context":{"name":"Ada","topics":["news"]},"name":"send"}`, "the action reaches the model as the observation")
	if got := stateJSON(t, run); got != `{"chosen":"send"}` {
		t.Errorf("state = %s", got)
	}
}

func TestAnUnknownCatalogInTheDeclarationIsRefusedWhenTheFileIsLoaded(t *testing.T) {
	_, err := workflow.LoadBytes("x.yaml", []byte(strings.Replace(uiFlowYAML, "[basic]", "[fancy]", 1)))
	if err == nil {
		t.Fatal("an unknown catalog was accepted")
	}
	if _, err := workflow.LoadBytes("x.yaml", []byte(strings.Replace(uiFlowYAML, "  catalogs: [basic]", "  catalogs: []", 1))); err == nil {
		t.Error("an empty catalog list was accepted")
	}
}
