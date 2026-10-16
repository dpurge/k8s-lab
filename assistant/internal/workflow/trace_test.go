package workflow

import (
	"strings"
	"testing"
	"time"
)

func sampleTrace() []StepRecord {
	return []StepRecord{
		{RunID: "run-7", Step: 1, Attempt: 1, State: "start", Outcome: OutcomeRetry,
			InputTokens: 100, OutputTokens: 5, Duration: 10 * time.Millisecond, Error: "reply is not a valid JSON object"},
		{RunID: "run-7", Step: 1, Attempt: 2, State: "start", Outcome: OutcomeOK,
			Patch:  map[string]any{"note": "x"},
			Action: &Action{Type: ActionTool, Tool: "search", Args: map[string]any{"query": "verbs"}},
			Tool:   "search", ToolResult: `{"count":2}`,
			InputTokens: 120, OutputTokens: 30, Duration: 20 * time.Millisecond},
		{RunID: "run-7", Step: 2, Attempt: 1, State: "start", Outcome: OutcomeOK,
			Action:      &Action{Type: ActionGoto, State: "done"},
			InputTokens: 130, OutputTokens: 10, Duration: 10 * time.Millisecond},
		{RunID: "run-7", Step: 3, Attempt: 0, State: "done", Outcome: OutcomeFailed, Error: "max_steps: run used its 2 steps"},
	}
}

func TestFormatTrace(t *testing.T) {
	want := `run run-7
step 1 attempt 1 in start: retry (100 in, 5 out tokens, 10ms)
  error:  reply is not a valid JSON object
step 1 attempt 2 in start: ok (120 in, 30 out tokens, 20ms)
  patch:  {"note":"x"}
  action: tool search {"query":"verbs"}
  tool:   search -> {"count":2}
step 2 attempt 1 in start: ok (130 in, 10 out tokens, 10ms)
  action: goto done
step 3 in done: failed
  error:  max_steps: run used its 2 steps
total: 3 model calls, 1 retries, 350 in + 45 out tokens, 40ms
`
	if got := FormatTrace(sampleTrace()); got != want {
		t.Errorf("trace differs.\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFormatTraceAskUser(t *testing.T) {
	out := FormatTrace([]StepRecord{{
		RunID: "r", Step: 1, Attempt: 1, State: "s", Outcome: OutcomeOK,
		Action: &Action{Type: ActionAskUser, Payload: map[string]any{"q": "Ready?"}},
	}})
	if !strings.Contains(out, `action: ask_user {"q":"Ready?"}`) {
		t.Errorf("trace = %s", out)
	}
}

func TestFormatTraceEmpty(t *testing.T) {
	if got := FormatTrace(nil); got != "(no steps)\n" {
		t.Errorf("got %q", got)
	}
}

func TestSumTrace(t *testing.T) {
	got := SumTrace(sampleTrace())
	want := Totals{InputTokens: 350, OutputTokens: 45, Duration: 40 * time.Millisecond, Attempts: 3, Retries: 1}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if SumTrace(nil) != (Totals{}) {
		t.Errorf("empty trace should sum to zero")
	}
}
