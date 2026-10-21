package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Totals sums the cost of a trace.
type Totals struct {
	InputTokens  int
	OutputTokens int
	Duration     time.Duration
	Attempts     int // model calls
	Retries      int // refused replies that were retried
}

// SumTrace adds up a run's records.
func SumTrace(records []StepRecord) Totals {
	var t Totals
	for _, r := range records {
		t.InputTokens += r.InputTokens
		t.OutputTokens += r.OutputTokens
		t.Duration += r.Duration
		if r.Attempt > 0 {
			t.Attempts++
		}
		if r.Outcome == OutcomeRetry {
			t.Retries++
		}
	}
	return t
}

// FormatTrace renders a run's records as text for a human, one block per
// attempt, in the order they happened.
func FormatTrace(records []StepRecord) string {
	if len(records) == 0 {
		return "(no steps)\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "run %s\n", records[0].RunID)
	for _, r := range records {
		fmt.Fprintf(&b, "step %d", r.Step)
		if r.Attempt > 0 {
			fmt.Fprintf(&b, " attempt %d", r.Attempt)
		}
		if r.Depth > 0 {
			fmt.Fprintf(&b, " [%s, depth %d]", r.Workflow, r.Depth)
		}
		fmt.Fprintf(&b, " in %s: %s", r.State, r.Outcome)
		if r.Attempt > 0 {
			fmt.Fprintf(&b, " (%d in, %d out tokens, %s)", r.InputTokens, r.OutputTokens, r.Duration)
		}
		b.WriteString("\n")
		if r.Patch != nil && len(r.Patch) > 0 {
			fmt.Fprintf(&b, "  patch:  %s\n", compactJSON(r.Patch))
		}
		if r.Action != nil {
			fmt.Fprintf(&b, "  action: %s\n", describeAction(r.Action))
		}
		if r.Tool != "" {
			fmt.Fprintf(&b, "  tool:   %s -> %s\n", r.Tool, r.ToolResult)
		}
		if r.Error != "" {
			fmt.Fprintf(&b, "  error:  %s\n", r.Error)
		}
	}
	t := SumTrace(records)
	fmt.Fprintf(&b, "total: %d model calls, %d retries, %d in + %d out tokens, %s\n",
		t.Attempts, t.Retries, t.InputTokens, t.OutputTokens, t.Duration)
	return b.String()
}

func describeAction(a *Action) string {
	switch a.Type {
	case ActionTool:
		return fmt.Sprintf("tool %s %s", a.Tool, compactJSON(a.Args))
	case ActionGoto:
		return "goto " + a.State
	case ActionAskUser:
		return "ask_user " + compactJSON(a.Payload)
	case ActionDelegate:
		out := "delegate to " + a.Workflow + " " + compactJSON(a.Message)
		if a.Reason != "" {
			out += " (" + a.Reason + ")"
		}
		return out
	}
	return string(a.Type)
}

func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("(unprintable: %v)", err)
	}
	return string(b)
}
