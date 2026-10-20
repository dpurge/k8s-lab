package demo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"assistant/internal/llm"
	"assistant/internal/workflow"
	"k8s-lab/shared/modelcfg"
)

func step(t *testing.T, workflowID, state string, runState map[string]any, observation any) (workflow.Response, error) {
	t.Helper()
	input, _ := json.Marshal(map[string]any{
		"workflow": map[string]any{"id": workflowID}, "state": map[string]any{"name": state},
		"run_state": runState, "observation": observation,
	})
	return New().Complete(context.Background(), modelcfg.Provider{}, modelcfg.Model{}, workflow.Request{Input: string(input)})
}

func TestTheFirstMessageWhateverItIsMakesItAskForTheNameAndNoteThatItAsked(t *testing.T) {
	for _, first := range []string{"start", "hi", "Janusz", "I want to learn Arabic"} {
		resp, err := step(t, "hello", "ask", map[string]any{}, first)
		if err != nil || !strings.Contains(resp.Text, `"type":"ask_user"`) || !strings.Contains(resp.Text, "What is your name?") || !strings.Contains(resp.Text, `"asked":true`) {
			t.Errorf("%q: %+v %v", first, resp, err)
		}
	}
}

func TestAnyOtherTextInTheAskStateIsTheAnswerAndIsRecordedAsTyped(t *testing.T) {
	resp, err := step(t, "hello", "ask", map[string]any{"asked": true}, `  Janusz "J" عربي `)
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Patch  map[string]string `json:"state_patch"`
		Action map[string]string `json:"action"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &reply); err != nil {
		t.Fatalf("the reply must be valid JSON even for awkward names: %v\n%s", err, resp.Text)
	}
	if reply.Patch["name"] != `Janusz "J" عربي` || reply.Action["type"] != "goto" || reply.Action["state"] != "greet" {
		t.Errorf("reply = %+v", reply)
	}
}

func TestAnEmptyOrNonTextObservationIsAskedAgain(t *testing.T) {
	for _, obs := range []any{"", "   ", nil, 5, map[string]any{"a": 1}} {
		resp, err := step(t, "hello", "ask", map[string]any{"asked": true}, obs)
		if err != nil || !strings.Contains(resp.Text, "ask_user") {
			t.Errorf("%v: %+v %v", obs, resp, err)
		}
	}
}

func TestTheGreetingNamesTheUserAndSaysItIsTheDemo(t *testing.T) {
	resp, err := step(t, "hello", "greet", map[string]any{"name": "Ada"}, nil)
	if err != nil || !strings.Contains(resp.Text, `"greeting":"Hello, Ada! (from the demo model)"`) || !strings.Contains(resp.Text, `"state":"done"`) {
		t.Errorf("%+v %v", resp, err)
	}
}

func TestItRefusesAnyOtherWorkflowAndStateWithAClearError(t *testing.T) {
	for name, tc := range map[string][2]string{"workflow": {"learn-topic", "intake"}, "state": {"hello", "elsewhere"}} {
		_, err := step(t, tc[0], tc[1], map[string]any{}, nil)
		var f *llm.Failure
		if !errors.As(err, &f) || f.Kind != llm.FailClient || !strings.Contains(err.Error(), "demo model") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestInputThatIsNotAStepIsRefused(t *testing.T) {
	_, err := New().Complete(context.Background(), modelcfg.Provider{}, modelcfg.Model{}, workflow.Request{Input: "hello there"})
	var f *llm.Failure
	if !errors.As(err, &f) || f.Kind != llm.FailClient {
		t.Errorf("err = %v", err)
	}
}

func TestItReportsTokensSoCostAndUsageHaveSomethingToCount(t *testing.T) {
	resp, _ := step(t, "hello", "ask", map[string]any{}, "start")
	if resp.InputTokens == 0 || resp.OutputTokens == 0 {
		t.Errorf("%+v", resp)
	}
}
