package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"assistant/internal/workflow"
	"assistant/internal/workflow/workflowtest"
)

func TestRunToolThenGotoEnd(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		toolReply(`{"note":"started"}`, "verbs"),
		gotoReply(`{"n":1}`, "done"),
	)...)
	h.search.Fn = func(context.Context, map[string]any) (any, error) {
		return map[string]any{"hits": []any{"a", "b"}, "count": 2}, nil
	}

	run, res := h.start(workflow.Input{Message: "teach me verbs"})

	if res.Status != workflow.StatusSucceeded || run.Status != workflow.StatusSucceeded {
		t.Fatalf("status = %v / %v, failure %+v", res.Status, run.Status, run.Failure)
	}
	if got := stateJSON(t, run); got != `{"n":1,"note":"started"}` {
		t.Errorf("state = %s", got)
	}
	if run.StateName != "done" || run.Steps != 2 {
		t.Errorf("state=%s steps=%d", run.StateName, run.Steps)
	}
	if run.Visits["start"] != 1 || run.Visits["done"] != 1 {
		t.Errorf("visits = %v", run.Visits)
	}
	if calls := h.search.Calls(); len(calls) != 1 || calls[0].Args["query"] != "verbs" {
		t.Errorf("tool calls = %v", calls)
	}
	contains(t, requestInput(t, h, 0), `"observation":"teach me verbs"`, "first request carries the user's message")
	contains(t, requestInput(t, h, 1), `"count":2`, "second request carries the tool result")
	contains(t, requestInput(t, h, 1), `"hits":["a","b"]`, "second request carries the tool result")
	h.wantNoScriptProblems()
	wantOutcomes(t, h, workflow.OutcomeOK, workflow.OutcomeOK)
}

func TestTraceRecordsEveryStep(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		"not json",
		toolReply(`{"note":"x"}`, "q"),
		gotoReply(`{}`, "done"),
	)...)
	run, _ := h.start(workflow.Input{})

	recs := h.rec.Records()
	if len(recs) != 3 {
		t.Fatalf("want 3 records, got %d", len(recs))
	}
	if recs[0].Step != 1 || recs[0].Attempt != 1 || recs[0].Outcome != workflow.OutcomeRetry || recs[0].Error == "" || recs[0].Reply != "not json" {
		t.Errorf("record 0 = %+v", recs[0])
	}
	if recs[1].Step != 1 || recs[1].Attempt != 2 || recs[1].Outcome != workflow.OutcomeOK || recs[1].Tool != "search" || recs[1].ToolResult == "" {
		t.Errorf("record 1 = %+v", recs[1])
	}
	if recs[1].Action == nil || recs[1].Action.Type != workflow.ActionTool || recs[1].Patch["note"] != "x" {
		t.Errorf("record 1 action/patch = %+v %+v", recs[1].Action, recs[1].Patch)
	}
	if recs[2].Step != 2 || recs[2].Attempt != 1 || recs[2].Action.State != "done" {
		t.Errorf("record 2 = %+v", recs[2])
	}
	digests := map[string]bool{}
	sumTokens := 0
	for _, r := range recs {
		if r.RunID != "run-1" || r.State != "start" || r.RequestDigest == "" {
			t.Errorf("incomplete record %+v", r)
		}
		if r.Duration != clockStep {
			t.Errorf("duration = %v, want %v", r.Duration, clockStep)
		}
		digests[r.RequestDigest] = true
		sumTokens += r.InputTokens + r.OutputTokens
	}
	if len(digests) != 3 {
		t.Errorf("each request differs, so digests should too: %v", digests)
	}
	if run.Tokens != sumTokens || sumTokens == 0 {
		t.Errorf("run tokens %d != trace tokens %d", run.Tokens, sumTokens)
	}
}

func TestRequestsCarryNoHistory(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		toolReply(`{"note":"x"}`, "one"),
		toolReply(`{}`, "two"),
		gotoReply(`{}`, "done"),
	)...)
	calls := 0
	h.search.Fn = func(context.Context, map[string]any) (any, error) {
		calls++
		return map[string]any{"marker": []string{"MARKER-ONE", "MARKER-TWO"}[calls-1]}, nil
	}
	h.start(workflow.Input{})

	contains(t, requestInput(t, h, 1), "MARKER-ONE", "step 2 sees step 1's result")
	notContains(t, requestInput(t, h, 2), "MARKER-ONE", "step 3 must not see step 1's result")
	contains(t, requestInput(t, h, 2), "MARKER-TWO", "step 3 sees step 2's result")
	for i, req := range h.llm.Requests() {
		notContains(t, req.System, "MARKER", "system prompt of request "+string(rune('0'+i)))
	}
}

func TestToolErrorBecomesObservation(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		toolReply(`{"note":"x"}`, "q"),
		gotoReply(`{}`, "done"),
	)...)
	h.search.Fn = func(context.Context, map[string]any) (any, error) { return nil, errors.New("index offline") }
	run, res := h.start(workflow.Input{})

	if res.Status != workflow.StatusSucceeded {
		t.Fatalf("a tool error must not fail the run: %+v", run.Failure)
	}
	contains(t, requestInput(t, h, 1), `"error":"index offline"`, "tool error shown to the model")
	if rec := h.rec.Records()[0]; !strings.HasPrefix(rec.ToolResult, "error: index offline") {
		t.Errorf("trace tool result = %q", rec.ToolResult)
	}
}

func TestToolPanicBecomesObservation(t *testing.T) {
	h := newHarness(t, flowYAML, replies(toolReply(`{"note":"x"}`, "q"), gotoReply(`{}`, "done"))...)
	h.search.Fn = func(context.Context, map[string]any) (any, error) { panic("kaboom") }
	_, res := h.start(workflow.Input{})
	if res.Status != workflow.StatusSucceeded {
		t.Fatalf("status = %v", res.Status)
	}
	contains(t, requestInput(t, h, 1), "tool panicked: kaboom", "panic reported")
}

func TestToolTimeoutHonoursContext(t *testing.T) {
	h := newHarness(t, flowYAML, replies(toolReply(`{"note":"x"}`, "q"), gotoReply(`{}`, "done"))...)
	h.eng.ToolTimeout = 20 * 1000 * 1000 // 20ms
	h.search.Fn = func(ctx context.Context, _ map[string]any) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	_, res := h.start(workflow.Input{})
	if res.Status != workflow.StatusSucceeded {
		t.Fatalf("status = %v", res.Status)
	}
	contains(t, requestInput(t, h, 1), "tool timed out after 20ms", "timeout reported")
}

func TestToolTimeoutEvenWhenToolIgnoresContext(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h := newHarness(t, flowYAML, replies(toolReply(`{"note":"x"}`, "q"), gotoReply(`{}`, "done"))...)
	h.eng.ToolTimeout = 20 * 1000 * 1000
	h.search.Fn = func(context.Context, map[string]any) (any, error) {
		<-release
		return nil, nil
	}
	_, res := h.start(workflow.Input{})
	if res.Status != workflow.StatusSucceeded {
		t.Fatalf("status = %v", res.Status)
	}
	contains(t, requestInput(t, h, 1), "tool timed out", "timeout reported for an uncooperative tool")
}

func TestLongToolResultIsTruncatedWithMarker(t *testing.T) {
	h := newHarness(t, flowYAML, replies(toolReply(`{"note":"x"}`, "q"), gotoReply(`{}`, "done"))...)
	h.eng.MaxObservationBytes = 101
	h.search.Fn = func(context.Context, map[string]any) (any, error) {
		return strings.Repeat("é", 500), nil
	}
	h.start(workflow.Input{})

	in := requestInput(t, h, 1)
	contains(t, in, "…[truncated:", "marker")
	if _, err := workflow.DecodeObject([]byte(in)); err != nil {
		t.Errorf("the step input must stay valid JSON after truncating mid-text: %v", err)
	}
	notContains(t, in, strings.Repeat("é", 100), "the full result must not be sent")
}

func TestUnencodableToolResultBecomesObservation(t *testing.T) {
	h := newHarness(t, flowYAML, replies(toolReply(`{"note":"x"}`, "q"), gotoReply(`{}`, "done"))...)
	h.search.Fn = func(context.Context, map[string]any) (any, error) { return make(chan int), nil }
	h.start(workflow.Input{})
	contains(t, requestInput(t, h, 1), "not JSON-encodable", "reported")
}

func TestInvalidReplyIsRetriedWithTheReason(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		"Sure, let's finish!",
		gotoReply(`{"note":"x"}`, "done"),
	)...)
	run, res := h.start(workflow.Input{})

	if res.Status != workflow.StatusSucceeded {
		t.Fatalf("failure: %+v", run.Failure)
	}
	retry := requestInput(t, h, 1)
	contains(t, retry, `"retry"`, "retry note present")
	contains(t, retry, "Sure, let's finish!", "the refused reply is echoed")
	contains(t, retry, "not a valid JSON object", "the reason is given")
	notContains(t, requestInput(t, h, 0), `"retry"`, "first attempt has no retry note")
	wantOutcomes(t, h, workflow.OutcomeRetry, workflow.OutcomeOK)
	if run.Steps != 1 {
		t.Errorf("a retry is not a step: steps = %d", run.Steps)
	}
}

func TestRetryReasonsForEachKindOfRefusal(t *testing.T) {
	tests := []struct {
		name, bad, want string
	}{
		{"tool not allowed", `{"state_patch":{},"action":{"type":"tool","tool":"delete_all","args":{}}}`, `tool "delete_all" is not available in state "start"`},
		{"bad args", `{"state_patch":{},"action":{"type":"tool","tool":"search","args":{"query":7}}}`, `arguments for tool "search" are invalid`},
		{"goto not allowed", gotoReply(`{}`, "recover"), `cannot go to "recover" from state "start"`},
		{"unknown action", `{"state_patch":{},"action":{"type":"fly"}}`, `unknown action type "fly"`},
		{"patch breaks schema", gotoReply(`{"n":"one"}`, "work"), "the state_patch would make the run state invalid: state.n"},
		{"end without required key", gotoReply(`{}`, "done"), `is missing required "note"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, flowYAML, replies(tc.bad, gotoReply(`{"note":"x"}`, "done"))...)
			run, res := h.start(workflow.Input{})
			if res.Status != workflow.StatusSucceeded {
				t.Fatalf("failure: %+v", run.Failure)
			}
			in, err := workflow.DecodeObject([]byte(requestInput(t, h, 1)))
			if err != nil {
				t.Fatal(err)
			}
			retry, ok := in["retry"].(map[string]any)
			if !ok {
				t.Fatalf("no retry note in the second request: %v", in)
			}
			contains(t, retry["error"].(string), tc.want, "retry reason")
		})
	}
}

func TestRefusedPatchLeavesNoTrace(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		gotoReply(`{"note":"first"}`, "work"),
		gotoReply(`{"note":"changed","n":"bad"}`, "done"),
		gotoReply(`{"n":2}`, "done"),
	)...)
	run, res := h.start(workflow.Input{})
	if res.Status != workflow.StatusSucceeded {
		t.Fatalf("failure: %+v", run.Failure)
	}
	if got := stateJSON(t, run); got != `{"n":2,"note":"first"}` {
		t.Errorf("a refused patch leaked into the state: %s", got)
	}
	contains(t, requestInput(t, h, 2), `"note":"first"`, "the retry sees the unchanged state")
}

func TestRetriesExhaustedFailsTheRun(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		gotoReply(`{"note":"x"}`, "work"),
		"nope 1",
		"nope 2",
	)...)
	run, res := h.start(workflow.Input{})

	if res.Status != workflow.StatusFailed || res.Failure.Reason != workflow.FailRetriesExhausted {
		t.Fatalf("result = %+v", res)
	}
	if run.Status != workflow.StatusFailed || run.Failure == nil || run.Failure.Reason != workflow.FailRetriesExhausted {
		t.Errorf("run = %+v", run)
	}
	contains(t, run.Failure.Message, "2 attempts", "failure message")
	if run.StateName != "work" || run.Steps != 1 {
		t.Errorf("the failed step must not count: state=%s steps=%d", run.StateName, run.Steps)
	}
	h.wantNoScriptProblems()
	wantOutcomes(t, h, workflow.OutcomeOK, workflow.OutcomeRetry, workflow.OutcomeRejected)
}

func TestZeroRetriesMeansOneAttempt(t *testing.T) {
	h := newHarness(t, flowYAML, replies(gotoReply(`{"note":"x"}`, "work"), "nope")...)
	h.wf.Limits.MaxRetries = 0
	_, res := h.start(workflow.Input{})
	if res.Status != workflow.StatusFailed || res.Failure.Reason != workflow.FailRetriesExhausted {
		t.Fatalf("result = %+v", res)
	}
	h.wantNoScriptProblems()
}

func TestMaxStepsStopsTheRun(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		gotoReply(`{"note":"x"}`, "work"),
		gotoReply(`{}`, "start"),
		gotoReply(`{}`, "work"),
	)...)
	h.wf.Limits.MaxSteps = 2
	run, res := h.start(workflow.Input{})

	if res.Failure == nil || res.Failure.Reason != workflow.FailMaxSteps {
		t.Fatalf("result = %+v", res)
	}
	if run.Steps != 2 || h.llm.Calls() != 2 {
		t.Errorf("steps=%d calls=%d: no model call may be made once the budget is used", run.Steps, h.llm.Calls())
	}
	recs := h.rec.Records()
	if last := recs[len(recs)-1]; last.Outcome != workflow.OutcomeFailed || !strings.Contains(last.Error, "max_steps") {
		t.Errorf("last record = %+v", last)
	}
}

func TestMaxTokensStopsTheRunDuringACall(t *testing.T) {
	h := newHarness(t, flowYAML, workflowtest.Step{
		Text: gotoReply(`{"note":"x"}`, "done"), InputTokens: 40, OutputTokens: 20,
	})
	h.wf.Limits.MaxTokens = 50
	run, res := h.start(workflow.Input{})

	if res.Failure == nil || res.Failure.Reason != workflow.FailMaxTokens {
		t.Fatalf("result = %+v", res)
	}
	if len(run.State) != 0 || run.StateName != "start" {
		t.Errorf("a reply that blew the budget must not be applied: %v in %s", run.State, run.StateName)
	}
	if run.Tokens != 60 {
		t.Errorf("tokens = %d, the spent call still counts", run.Tokens)
	}
}

func TestMaxTokensStopsTheRunBetweenSteps(t *testing.T) {
	h := newHarness(t, flowYAML,
		workflowtest.Step{Text: gotoReply(`{"note":"x"}`, "work"), InputTokens: 50, OutputTokens: 10},
		workflowtest.Step{Text: gotoReply(`{}`, "start"), InputTokens: 50, OutputTokens: 10},
	)
	h.wf.Limits.MaxTokens = 100
	run, res := h.start(workflow.Input{})
	if res.Failure == nil || res.Failure.Reason != workflow.FailMaxTokens {
		t.Fatalf("result = %+v", res)
	}
	if run.Steps != 1 || run.StateName != "work" {
		t.Errorf("steps=%d state=%s", run.Steps, run.StateName)
	}
}

func TestMaxTokensCountsRetries(t *testing.T) {
	h := newHarness(t, flowYAML,
		workflowtest.Step{Text: "bad", InputTokens: 30, OutputTokens: 10},
		workflowtest.Step{Text: "bad again", InputTokens: 30, OutputTokens: 10},
	)
	h.wf.Limits.MaxTokens = 70
	h.wf.Limits.MaxRetries = 5
	_, res := h.start(workflow.Input{})
	if res.Failure == nil || res.Failure.Reason != workflow.FailMaxTokens {
		t.Fatalf("retries must spend the token budget: %+v", res)
	}
}

func TestMaxVisitsStopsALoop(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		gotoReply(`{"note":"x"}`, "work"),
		gotoReply(`{}`, "start"),
		gotoReply(`{}`, "work"),
		gotoReply(`{}`, "start"),
	)...)
	h.wf.Limits.MaxVisits = 2
	run, res := h.start(workflow.Input{})

	if res.Failure == nil || res.Failure.Reason != workflow.FailMaxVisits {
		t.Fatalf("result = %+v", res)
	}
	contains(t, res.Failure.Message, `"start"`, "names the state")
	if run.StateName != "work" || run.Visits["start"] != 2 {
		t.Errorf("the over-visited state must not be entered: state=%s visits=%v", run.StateName, run.Visits)
	}
	recs := h.rec.Records()
	if last := recs[len(recs)-1]; last.Outcome != workflow.OutcomeFailed {
		t.Errorf("last record = %+v", last)
	}
}

func TestOnErrorEdgeIsTakenOnce(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		"bad 1", "bad 2", // retries exhausted in start, which has on_error: recover
		gotoReply(`{"note":"recovered"}`, "done"),
	)...)
	run, res := h.start(workflow.Input{})

	if res.Status != workflow.StatusSucceeded {
		t.Fatalf("failure: %+v", run.Failure)
	}
	if !run.ErrorEdgeUsed || run.Visits["recover"] != 1 {
		t.Errorf("edge not recorded: used=%v visits=%v", run.ErrorEdgeUsed, run.Visits)
	}
	in := requestInput(t, h, 2)
	contains(t, in, `"error":{"message":`, "failure shown as the observation")
	contains(t, in, `"reason":"retries_exhausted"`, "reason shown")
	contains(t, in, `"name":"recover"`, "now in the recover state")
	wantOutcomes(t, h, workflow.OutcomeRetry, workflow.OutcomeRejected, workflow.OutcomeOK)
}

func TestSecondFailureAfterOnErrorIsFinal(t *testing.T) {
	yaml := strings.Replace(flowYAML, "    instructions: Recover.\n    next: [done]\n", "    instructions: Recover.\n    next: [done]\n    on_error: done\n", 1)
	h := newHarness(t, yaml, replies("bad 1", "bad 2", "bad 3", "bad 4")...)
	run, res := h.start(workflow.Input{})

	if res.Failure == nil || res.Failure.Reason != workflow.FailRetriesExhausted {
		t.Fatalf("result = %+v", res)
	}
	if run.StateName != "recover" {
		t.Errorf("the run must stay where it failed, got %s", run.StateName)
	}
	h.wantNoScriptProblems()
}

func TestOnErrorToAnEndStateSucceedsWhenStateIsComplete(t *testing.T) {
	yaml := strings.Replace(flowYAML, "on_error: recover", "on_error: done", 1)
	h := newHarness(t, yaml, replies(toolReply(`{"note":"have it"}`, "q"), "bad 1", "bad 2")...)
	run, res := h.start(workflow.Input{})
	if res.Status != workflow.StatusSucceeded || run.StateName != "done" {
		t.Fatalf("result = %+v state=%s failure=%+v", res, run.StateName, run.Failure)
	}
}

func TestOnErrorToAnEndStateFailsWhenStateIsIncomplete(t *testing.T) {
	yaml := strings.Replace(flowYAML, "on_error: recover", "on_error: done", 1)
	h := newHarness(t, yaml, replies("bad 1", "bad 2")...)
	_, res := h.start(workflow.Input{})
	if res.Failure == nil || res.Failure.Reason != workflow.FailIncompleteState {
		t.Fatalf("result = %+v", res)
	}
	contains(t, res.Failure.Message, `missing required "note"`, "says what is missing")
}

func TestBudgetFailuresAreNotRedirected(t *testing.T) {
	h := newHarness(t, flowYAML, workflowtest.Step{Text: gotoReply(`{"note":"x"}`, "done"), InputTokens: 90, OutputTokens: 20})
	h.wf.Limits.MaxTokens = 100
	run, res := h.start(workflow.Input{})
	if res.Failure == nil || res.Failure.Reason != workflow.FailMaxTokens || run.ErrorEdgeUsed {
		t.Fatalf("a spent budget cannot be recovered from: %+v used=%v", res, run.ErrorEdgeUsed)
	}
}

func TestLLMErrorFailsTheRun(t *testing.T) {
	h := newHarness(t, flowYAML, replies(gotoReply(`{"note":"x"}`, "work"))...)
	h.llm = workflowtest.NewScriptedLLM(
		workflowtest.Reply(gotoReply(`{"note":"x"}`, "work")),
		workflowtest.Step{Err: errors.New("provider unavailable")},
	)
	h.eng.LLM = h.llm
	run, res := h.start(workflow.Input{})

	if res.Failure == nil || res.Failure.Reason != workflow.FailLLM {
		t.Fatalf("result = %+v", res)
	}
	contains(t, res.Failure.Message, "provider unavailable", "message")
	if run.StateName != "work" {
		t.Errorf("state = %s", run.StateName)
	}
	recs := h.rec.Records()
	if last := recs[len(recs)-1]; last.Outcome != workflow.OutcomeFailed || !strings.Contains(last.Error, "provider unavailable") {
		t.Errorf("last record = %+v", last)
	}
}

func TestLLMErrorTakesTheOnErrorEdge(t *testing.T) {
	h := newHarness(t, flowYAML)
	h.llm = workflowtest.NewScriptedLLM(
		workflowtest.Step{Err: errors.New("provider unavailable")},
		workflowtest.Reply(gotoReply(`{"note":"x"}`, "done")),
	)
	h.eng.LLM = h.llm
	run, res := h.start(workflow.Input{})
	if res.Status != workflow.StatusSucceeded {
		t.Fatalf("failure: %+v", run.Failure)
	}
	contains(t, requestInput(t, h, 1), `"reason":"llm_error"`, "reason shown")
}

func TestCancelledBeforeStart(t *testing.T) {
	h := newHarness(t, flowYAML)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run, res, err := h.eng.Start(ctx, h.wf, "r", workflow.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failure == nil || res.Failure.Reason != workflow.FailCancelled || run.Status != workflow.StatusFailed {
		t.Fatalf("result = %+v", res)
	}
	if h.llm.Calls() != 0 {
		t.Errorf("no model call after cancellation")
	}
}

func TestCancelledDuringATool(t *testing.T) {
	h := newHarness(t, flowYAML, replies(toolReply(`{"note":"x"}`, "q"), gotoReply(`{}`, "done"))...)
	ctx, cancel := context.WithCancel(context.Background())
	h.search.Fn = func(context.Context, map[string]any) (any, error) {
		cancel()
		return map[string]any{"ok": true}, nil
	}
	run, res, err := h.eng.Start(ctx, h.wf, "r", workflow.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failure == nil || res.Failure.Reason != workflow.FailCancelled {
		t.Fatalf("result = %+v", res)
	}
	if h.llm.Calls() != 1 || run.Steps != 1 {
		t.Errorf("calls=%d steps=%d", h.llm.Calls(), run.Steps)
	}
}

func TestCancelledDuringTheModelCall(t *testing.T) {
	h := newHarness(t, flowYAML)
	ctx, cancel := context.WithCancel(context.Background())
	h.llm = workflowtest.NewScriptedLLM(workflowtest.Step{Err: context.Canceled, Check: func(workflow.Request) error {
		cancel()
		return nil
	}})
	h.eng.LLM = h.llm
	_, res, err := h.eng.Start(ctx, h.wf, "r", workflow.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failure == nil || res.Failure.Reason != workflow.FailCancelled {
		t.Fatalf("a cancelled model call must report cancellation, not an LLM error: %+v", res)
	}
}

func TestAskUserSuspendsAndResumeContinues(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		askReply(`{"note":"asked"}`, `{"question":"Ready?"}`),
		gotoReply(`{"n":3}`, "done"),
	)...)
	run, res := h.start(workflow.Input{Message: "hello"})

	if res.Status != workflow.StatusWaiting || run.Status != workflow.StatusWaiting {
		t.Fatalf("status = %v", res.Status)
	}
	if res.Ask["question"] != "Ready?" {
		t.Errorf("payload = %v", res.Ask)
	}
	if run.StateName != "start" || run.Steps != 1 || run.Observation != nil {
		t.Errorf("waiting run: state=%s steps=%d obs=%v", run.StateName, run.Steps, run.Observation)
	}

	res, err := h.eng.Resume(context.Background(), h.wf, run, "yes, ready")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != workflow.StatusSucceeded || run.Status != workflow.StatusSucceeded {
		t.Fatalf("after resume: %+v failure=%+v", res, run.Failure)
	}
	contains(t, requestInput(t, h, 1), `"observation":"yes, ready"`, "the user's message is the observation")
	contains(t, requestInput(t, h, 1), `"note":"asked"`, "the run state survived the pause")
	if run.Steps != 2 {
		t.Errorf("steps = %d", run.Steps)
	}
	h.wantNoScriptProblems()
}

func TestSnapshotRoundTripThroughJSON(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		toolReply(`{"note":"x","n":12345}`, "q"),
		askReply(`{}`, `{"question":"Ready?"}`),
		gotoReply(`{}`, "done"),
	)...)
	run, res := h.start(workflow.Input{})
	if res.Status != workflow.StatusWaiting {
		t.Fatalf("status = %v", res.Status)
	}

	before := mustJSON(t, run)
	restored, err := workflow.RestoreRun([]byte(before))
	if err != nil {
		t.Fatal(err)
	}
	if after := mustJSON(t, restored); after != before {
		t.Errorf("snapshot changed in the round trip:\nbefore %s\nafter  %s", before, after)
	}
	if !reflect.DeepEqual(run.Visits, restored.Visits) || restored.Tokens != run.Tokens || restored.Steps != run.Steps {
		t.Errorf("restored run differs: %+v vs %+v", restored, run)
	}
	contains(t, before, `"n":12345`, "integers stay integers")

	res, err = h.eng.Resume(context.Background(), h.wf, restored, "go on")
	if err != nil || res.Status != workflow.StatusSucceeded {
		t.Fatalf("resume of the restored run: %v %+v", err, res)
	}
}

func TestSnapshotOfAFailedRunKeepsTheFailure(t *testing.T) {
	h := newHarness(t, flowYAML, replies(gotoReply(`{"note":"x"}`, "work"), "bad", "bad")...)
	run, _ := h.start(workflow.Input{})
	restored, err := workflow.RestoreRun([]byte(mustJSON(t, run)))
	if err != nil {
		t.Fatal(err)
	}
	if restored.Failure == nil || restored.Failure.Reason != workflow.FailRetriesExhausted || restored.Status != workflow.StatusFailed {
		t.Errorf("restored = %+v", restored)
	}
}

func TestRestoreRunRejectsGarbage(t *testing.T) {
	if _, err := workflow.RestoreRun([]byte(`{"run_id":`)); err == nil {
		t.Errorf("a truncated snapshot must be refused")
	}
	r, err := workflow.RestoreRun([]byte(`{"run_id":"r","status":"waiting"}`))
	if err != nil || r.State == nil || r.Visits == nil {
		t.Errorf("missing maps should be filled in: %v %+v", err, r)
	}
}

func TestResumeIsRefusedUnlessWaiting(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		askReply(`{"note":"x"}`, `{"q":"?"}`),
		gotoReply(`{}`, "done"),
	)...)
	run, _ := h.start(workflow.Input{})
	if _, err := h.eng.Resume(context.Background(), h.wf, run, "first"); err != nil {
		t.Fatal(err)
	}
	callsBefore := h.llm.Calls()
	_, err := h.eng.Resume(context.Background(), h.wf, run, "second")
	if !errors.Is(err, workflow.ErrNotWaiting) {
		t.Fatalf("resuming a finished run: err = %v", err)
	}
	if h.llm.Calls() != callsBefore || run.Status != workflow.StatusSucceeded {
		t.Errorf("a refused resume must change nothing")
	}

	running := workflow.NewRun(h.wf, "r2", nil)
	if _, err := h.eng.Resume(context.Background(), h.wf, running, "x"); !errors.Is(err, workflow.ErrNotWaiting) {
		t.Errorf("resuming a run that never waited: %v", err)
	}
}

func TestResumeChecksTheWorkflowMatches(t *testing.T) {
	h := newHarness(t, flowYAML)
	run := workflow.NewRun(h.wf, "r", nil)
	run.Status = workflow.StatusWaiting
	run.WorkflowID = "other"
	if _, err := h.eng.Resume(context.Background(), h.wf, run, "x"); err == nil || !strings.Contains(err.Error(), `belongs to workflow "other"`) {
		t.Errorf("err = %v", err)
	}
	run.WorkflowID = "flow"
	run.StateName = "ghost"
	if _, err := h.eng.Resume(context.Background(), h.wf, run, "x"); err == nil || !strings.Contains(err.Error(), "unknown state") {
		t.Errorf("err = %v", err)
	}
}

func TestStartMisuse(t *testing.T) {
	h := newHarness(t, flowYAML)
	ctx := context.Background()

	bare := &workflow.Engine{}
	if _, _, err := bare.Start(ctx, h.wf, "r", workflow.Input{}); err == nil || !strings.Contains(err.Error(), "no LLM") {
		t.Errorf("no LLM: %v", err)
	}
	noTools := &workflow.Engine{LLM: h.llm}
	if _, _, err := noTools.Start(ctx, h.wf, "r", workflow.Input{}); err == nil || !strings.Contains(err.Error(), "no tools") {
		t.Errorf("no tools: %v", err)
	}
	noPrompts := &workflow.Engine{LLM: h.llm, Tools: h.eng.Tools}
	if _, _, err := noPrompts.Start(ctx, h.wf, "r", workflow.Input{}); err == nil || !strings.Contains(err.Error(), "no prompts") {
		t.Errorf("no prompts: %v", err)
	}
	if _, _, err := h.eng.Start(ctx, h.wf, "r", workflow.Input{State: map[string]any{"n": "x"}}); err == nil || !strings.Contains(err.Error(), "initial state") {
		t.Errorf("bad initial state: %v", err)
	}
	broken := *h.wf
	broken.Start = "ghost"
	if _, _, err := h.eng.Start(ctx, &broken, "r", workflow.Input{}); err == nil || !strings.Contains(err.Error(), "no start state") {
		t.Errorf("unknown start: %v", err)
	}
	if h.llm.Calls() != 0 {
		t.Errorf("misuse must never reach the model")
	}
}

func TestInitialStateAcceptsPlainGoNumbers(t *testing.T) {
	h := newHarness(t, flowYAML, replies(gotoReply(`{}`, "done"))...)
	run, res := h.start(workflow.Input{State: map[string]any{"note": "given", "n": 4}})
	if res.Status != workflow.StatusSucceeded {
		t.Fatalf("failure: %+v", run.Failure)
	}
	if got := stateJSON(t, run); got != `{"n":4,"note":"given"}` {
		t.Errorf("state = %s", got)
	}
}

func TestStartingInAnEndState(t *testing.T) {
	yaml := strings.Replace(flowYAML, "start: start", "start: done", 1)
	h := newHarness(t, yaml)
	run, res := h.start(workflow.Input{State: map[string]any{"note": "ok"}})
	if res.Status != workflow.StatusSucceeded || h.llm.Calls() != 0 {
		t.Fatalf("result = %+v calls=%d", res, h.llm.Calls())
	}
	if run.Steps != 0 {
		t.Errorf("steps = %d", run.Steps)
	}

	_, res = h.start(workflow.Input{})
	if res.Failure == nil || res.Failure.Reason != workflow.FailIncompleteState {
		t.Errorf("an incomplete state cannot start finished: %+v", res)
	}
}

func TestRunsAreDeterministic(t *testing.T) {
	script := []string{"bad", toolReply(`{"note":"x"}`, "q"), askReply(`{}`, `{"q":"?"}`)}
	run1 := func() ([]workflow.StepRecord, string) {
		h := newHarness(t, flowYAML, replies(script...)...)
		run, _ := h.start(workflow.Input{Message: "hi", Language: "xaa"})
		return h.rec.Records(), mustJSON(t, run)
	}
	r1, s1 := run1()
	r2, s2 := run1()
	if !reflect.DeepEqual(r1, r2) || s1 != s2 {
		t.Errorf("two identical runs differ:\n%+v\n%+v", r1, r2)
	}
}

func TestLanguageAndOutputSnippetsReachTheModel(t *testing.T) {
	prompts, err := workflow.LoadPrompts(fstest.MapFS{
		"step.system.txt":         {Data: []byte("Base.\n{{languageRules}}\n{{outputRules}}")},
		"lang/xaa/rules.txt":      {Data: []byte("Write in A.")},
		"output/lesson/rules.txt": {Data: []byte("Make a lesson.")},
		"output/quiz/rules.txt":   {Data: []byte("Make a quiz.")},
	})
	if err != nil {
		t.Fatal(err)
	}
	yaml := strings.Replace(flowYAML, "    instructions: Begin.\n", "    instructions: Begin.\n    output: lesson\n", 1)
	yaml = strings.Replace(yaml, "    instructions: Work.\n", "    instructions: Work.\n    output: quiz\n", 1)
	h := newHarness(t, yaml, replies(gotoReply(`{"note":"x"}`, "work"), gotoReply(`{}`, "done"))...)
	h.eng.Prompts = prompts
	h.start(workflow.Input{Language: "xaa"})

	reqs := h.llm.Requests()
	for i, wantOutput := range []string{"Make a lesson.", "Make a quiz."} {
		contains(t, reqs[i].System, "Write in A.", "language snippet")
		contains(t, reqs[i].System, wantOutput, "output snippet")
	}
	notContains(t, reqs[0].System, "Make a quiz.", "the lesson state must not get quiz rules")
	notContains(t, reqs[1].System, "Make a lesson.", "the quiz state must not get lesson rules")
}

func TestEveryRequestCarriesTheReplySchema(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		"bad",
		gotoReply(`{"note":"x"}`, "work"),
		gotoReply(`{}`, "done"),
	)...)
	h.start(workflow.Input{})
	for i, req := range h.llm.Requests() {
		if req.Schema == nil || req.Schema["required"] == nil {
			t.Errorf("request %d has no reply schema", i)
		}
	}
	startSchema := h.llm.Requests()[0].Schema
	workSchema := h.llm.Requests()[2].Schema
	if reflect.DeepEqual(startSchema, workSchema) {
		t.Errorf("the schema must follow the state: start and work allow different next states")
	}
}

func TestConcurrentRunsOnOneEngine(t *testing.T) {
	const runs = 8
	errs := make(chan error, runs)
	for i := 0; i < runs; i++ {
		go func() {
			h := newHarness(t, flowYAML, replies(
				toolReply(`{"note":"x"}`, "q"),
				gotoReply(`{}`, "done"),
			)...)
			run, res, err := h.eng.Start(context.Background(), h.wf, "r", workflow.Input{})
			if err == nil && res.Status != workflow.StatusSucceeded {
				err = errors.New("run did not succeed: " + string(run.Status))
			}
			errs <- err
		}()
	}
	for i := 0; i < runs; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

func TestTokenBudgetMayBeUsedExactly(t *testing.T) {
	h := newHarness(t, flowYAML, workflowtest.Step{
		Text: gotoReply(`{"note":"x"}`, "done"), InputTokens: 40, OutputTokens: 20,
	})
	h.wf.Limits.MaxTokens = 60
	run, res := h.start(workflow.Input{})
	if res.Status != workflow.StatusSucceeded {
		t.Fatalf("using exactly the budget is allowed, got %+v", res.Failure)
	}
	if run.Tokens != 60 {
		t.Errorf("tokens = %d", run.Tokens)
	}
}

func TestObservationIsClearedByATransition(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		toolReply(`{"note":"x"}`, "q"),
		gotoReply(`{}`, "work"),
		gotoReply(`{}`, "done"),
	)...)
	h.search.Fn = func(context.Context, map[string]any) (any, error) {
		return map[string]any{"marker": "STALE"}, nil
	}
	h.start(workflow.Input{})

	contains(t, requestInput(t, h, 1), "STALE", "the step after the tool sees its result")
	notContains(t, requestInput(t, h, 2), "STALE", "a transition clears the observation")
	notContains(t, requestInput(t, h, 2), `"observation"`, "a transition clears the observation")
}

func TestSeveralSuspensionsInOneRun(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		askReply(`{"note":"turn1"}`, `{"q":"first?"}`),
		askReply(`{"n":2}`, `{"q":"second?"}`),
		gotoReply(`{"n":3}`, "done"),
	)...)
	run, res := h.start(workflow.Input{})
	if res.Status != workflow.StatusWaiting || res.Ask["q"] != "first?" {
		t.Fatalf("first: %+v", res)
	}

	res, err := h.eng.Resume(context.Background(), h.wf, run, "answer one")
	if err != nil || res.Status != workflow.StatusWaiting || res.Ask["q"] != "second?" {
		t.Fatalf("second: %v %+v", err, res)
	}
	contains(t, requestInput(t, h, 1), `"observation":"answer one"`, "turn 2 sees answer one")

	res, err = h.eng.Resume(context.Background(), h.wf, run, "answer two")
	if err != nil || res.Status != workflow.StatusSucceeded {
		t.Fatalf("third: %v %+v", err, res)
	}
	notContains(t, requestInput(t, h, 2), "answer one", "turn 3 must not see turn 2's answer: no history")
	contains(t, requestInput(t, h, 2), `"observation":"answer two"`, "turn 3 sees answer two")
	if got := stateJSON(t, run); got != `{"n":3,"note":"turn1"}` {
		t.Errorf("state = %s", got)
	}
	if run.Steps != 3 {
		t.Errorf("steps = %d", run.Steps)
	}
}

func TestLimitsSpanSuspensions(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		askReply(`{"note":"x"}`, `{"q":"1"}`),
		askReply(`{}`, `{"q":"2"}`),
		askReply(`{}`, `{"q":"3"}`),
		askReply(`{}`, `{"q":"4"}`),
	)...)
	h.wf.Limits.MaxSteps = 3
	run, _ := h.start(workflow.Input{})
	for i := 0; i < 2; i++ {
		if res, err := h.eng.Resume(context.Background(), h.wf, run, "go"); err != nil || res.Status != workflow.StatusWaiting {
			t.Fatalf("resume %d: %v %+v", i, err, res)
		}
	}
	res, err := h.eng.Resume(context.Background(), h.wf, run, "go")
	if err != nil {
		t.Fatal(err)
	}
	if res.Failure == nil || res.Failure.Reason != workflow.FailMaxSteps {
		t.Fatalf("the step budget belongs to the whole run, not to one call: %+v", res)
	}
	if h.llm.Calls() != 3 {
		t.Errorf("calls = %d", h.llm.Calls())
	}
}

func TestTokenBudgetSpansSuspensions(t *testing.T) {
	h := newHarness(t, flowYAML,
		workflowtest.Step{Text: askReply(`{"note":"x"}`, `{"q":"1"}`), InputTokens: 60, OutputTokens: 10},
		workflowtest.Step{Text: askReply(`{}`, `{"q":"2"}`), InputTokens: 60, OutputTokens: 10},
	)
	h.wf.Limits.MaxTokens = 100
	run, res := h.start(workflow.Input{})
	if res.Status != workflow.StatusWaiting || run.Tokens != 70 {
		t.Fatalf("first call: %+v tokens=%d", res, run.Tokens)
	}
	res, err := h.eng.Resume(context.Background(), h.wf, run, "go")
	if err != nil {
		t.Fatal(err)
	}
	if res.Failure == nil || res.Failure.Reason != workflow.FailMaxTokens {
		t.Fatalf("tokens must accumulate across calls: %+v", res)
	}
}

func TestResumeOfACancelledContextFailsTheRun(t *testing.T) {
	h := newHarness(t, flowYAML, replies(askReply(`{"note":"x"}`, `{"q":"1"}`))...)
	run, _ := h.start(workflow.Input{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := h.eng.Resume(ctx, h.wf, run, "go")
	if err != nil {
		t.Fatal(err)
	}
	if res.Failure == nil || res.Failure.Reason != workflow.FailCancelled || run.Status != workflow.StatusFailed {
		t.Errorf("result = %+v", res)
	}
}

func TestWaitingRunCanBeStoredAndResumedByAnotherEngine(t *testing.T) {
	first := newHarness(t, flowYAML, replies(askReply(`{"note":"saved"}`, `{"q":"?"}`))...)
	run, _ := first.start(workflow.Input{Language: "xaa"})
	stored := mustJSON(t, run)

	second := newHarness(t, flowYAML, replies(gotoReply(`{}`, "done"))...)
	restored, err := workflow.RestoreRun([]byte(stored))
	if err != nil {
		t.Fatal(err)
	}
	res, err := second.eng.Resume(context.Background(), second.wf, restored, "later")
	if err != nil || res.Status != workflow.StatusSucceeded {
		t.Fatalf("resume elsewhere: %v %+v", err, res)
	}
	contains(t, requestInput(t, second, 0), `"note":"saved"`, "state survived the move")
	if restored.Language != "xaa" {
		t.Errorf("language lost in the snapshot: %q", restored.Language)
	}
}

// failedStepObservation parses the request sent at index i and returns its
// "observation", which is nil when the request has none.
func failedStepObservation(t *testing.T, h *harness, i int) map[string]any {
	t.Helper()
	var in map[string]any
	if err := json.Unmarshal([]byte(requestInput(t, h, i)), &in); err != nil {
		t.Fatalf("request %d is not JSON: %v", i, err)
	}
	obs, _ := in["observation"].(map[string]any)
	return obs
}

func TestOnErrorStateSeesTheObservationTheFailedStepHad(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		"bad 1", "bad 2", // retries exhausted in start, which was given the user's text
		gotoReply(`{"note":"recovered"}`, "done"),
	)...)
	run, res := h.start(workflow.Input{Message: "make a form that asks for my name"})

	if res.Status != workflow.StatusSucceeded {
		t.Fatalf("failure: %+v", run.Failure)
	}
	obs := failedStepObservation(t, h, 2)
	if got := obs[workflow.FailedObservationKey]; got != "make a form that asks for my name" {
		t.Errorf("observation under %q = %v", workflow.FailedObservationKey, got)
	}
	failure, _ := obs["error"].(map[string]any)
	if failure["reason"] != "retries_exhausted" {
		t.Errorf("error = %v", obs["error"])
	}
}

func TestOnErrorStateHasNoObservationKeyWhenTheFailedStepHadNone(t *testing.T) {
	h := newHarness(t, flowYAML, replies(
		"bad 1", "bad 2",
		gotoReply(`{"note":"recovered"}`, "done"),
	)...)
	h.start(workflow.Input{})

	obs := failedStepObservation(t, h, 2)
	if _, found := obs[workflow.FailedObservationKey]; found {
		t.Errorf("the key %q must be omitted for a nil observation: %v", workflow.FailedObservationKey, obs)
	}
	if _, found := obs["error"]; !found {
		t.Errorf("error missing: %v", obs)
	}
}
