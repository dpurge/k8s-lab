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

func TestTheScriptedOrchestratorRoutesByKeywordAndOtherwiseAnswersOrAsks(t *testing.T) {
	offered := []any{map[string]any{"id": "hello"}, map[string]any{"id": "ui-demo"}}
	route := func(observation any, offer []any) string {
		input, _ := json.Marshal(map[string]any{
			"workflow": map[string]any{"id": "orchestrator"}, "state": map[string]any{"name": "route"},
			"run_state": map[string]any{}, "observation": observation, "delegates": offer,
		})
		resp, err := New().Complete(context.Background(), modelcfg.Provider{}, modelcfg.Model{}, workflow.Request{Input: string(input)})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Text
	}
	for text, want := range map[string]string{
		"please say hello to me": `"workflow":"hello"`,
		"What is my name?":       `"workflow":"hello"`,
		"show me the interface":  `"workflow":"ui-demo"`,
		"I want a form":          `"workflow":"ui-demo"`,
	} {
		got := route(text, offered)
		if !strings.Contains(got, `"type":"delegate"`) || !strings.Contains(got, want) || !strings.Contains(got, `"reason":"you mentioned`) || !strings.Contains(got, `"message":"`+text+`"`) {
			t.Errorf("%q: %s", text, got)
		}
	}
	if got := route("what is the weather", offered); !strings.Contains(got, `"type":"goto"`) || !strings.Contains(got, "I have no workflow for that") {
		t.Errorf("no match: %s", got)
	}
	if got := route("help", offered); !strings.Contains(got, `"type":"ask_user"`) || !strings.Contains(got, "hello, ui-demo") {
		t.Errorf("a question: %s", got)
	}
	if got := route("say hello", []any{map[string]any{"id": "ui-demo"}}); strings.Contains(got, `"type":"delegate"`) {
		t.Errorf("a workflow that is not offered must not be named: %s", got)
	}
	if got := route(map[string]any{"delegate_result": map[string]any{"workflow": "hello", "state": map[string]any{}}}, offered); !strings.Contains(got, "The hello workflow finished") {
		t.Errorf("a result: %s", got)
	}
	if got := route(map[string]any{"delegate_error": map[string]any{"workflow": "hello", "message": "boom"}}, offered); !strings.Contains(got, "did not work out: boom") {
		t.Errorf("an error: %s", got)
	}
}
