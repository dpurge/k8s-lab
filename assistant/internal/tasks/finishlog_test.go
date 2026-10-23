package tasks

import (
	"strings"
	"testing"
	"time"
)

// These tests need no database: they build the log attributes from a task value.

// prodRefusal is what the model's refusal looked like in the first failed
// ui-compose run in production.
const prodRefusal = `reply is not a valid JSON object: invalid character '}' after array element (at byte 510). Around it: ":"Text","text":"GO!"}]},"dataModel":{}}<<HERE>>}}]}}}. Open at that point: reply > action > payload > ui[0]. Send the whole reply again as ONE JSON object with nothing before or after it, and count the closing brackets against the example in the guide.`

func attrsMap(t *testing.T, attrs []any) map[string]any {
	t.Helper()
	if len(attrs)%2 != 0 {
		t.Fatalf("odd number of log arguments: %v", attrs)
	}
	m := map[string]any{}
	for i := 0; i < len(attrs); i += 2 {
		m[attrs[i].(string)] = attrs[i+1]
	}
	return m
}

func failedTask(message string) Task {
	return Task{ID: "t1", WorkflowID: "ui-compose", Status: StatusFailed, Error: &message}
}

func TestFinishedLogAttrsOfASucceededTaskHaveNoFailure(t *testing.T) {
	got := attrsMap(t, finishedLogAttrs(Task{ID: "t1", WorkflowID: "hello", Status: StatusSucceeded}, 1500*time.Millisecond))
	want := map[string]any{"task_id": "t1", "workflow": "hello", "outcome": StatusSucceeded, "duration_ms": int64(1500)}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

func TestFinishedLogAttrsOfAWorkflowFailureNameTheReasonAndRedactTheReply(t *testing.T) {
	message := "model gave no valid reply in 3 attempts; last problem: " + prodRefusal
	attrs := finishedLogAttrs(failedTask("retries_exhausted: "+message), time.Second)
	got := attrsMap(t, attrs)

	if got["outcome"] != StatusFailed || got["failure_reason"] != "retries_exhausted" {
		t.Errorf("got %v", got)
	}
	text, _ := got["failure_message"].(string)
	if strings.HasPrefix(text, "retries_exhausted") || !strings.HasPrefix(text, "model gave no valid reply in 3 attempts") {
		t.Errorf("the message should come without the reason prefix: %s", text)
	}
	for _, leaked := range []string{"GO!", "<<HERE>>", "dataModel"} {
		if strings.Contains(text, leaked) {
			t.Errorf("failure_message leaks %q: %s", leaked, text)
		}
	}
	for _, kept := range []string{"(at byte 510)", "reply > action > payload > ui[0]", "Send the whole reply again"} {
		if !strings.Contains(text, kept) {
			t.Errorf("failure_message lost %q: %s", kept, text)
		}
	}
	// What a failed prod-like task logs now.
	t.Logf("task finished %v", attrs)
}

func TestFinishedLogAttrsOfAPlainErrorSayNothingAboutTheFailure(t *testing.T) {
	// Such an error can quote a payload or a result, so none of it is logged.
	for _, message := range []string{
		"engine: connection refused",
		"workflow panicked: boom",
		"made_up_reason: not one of the reasons",
		"workflow failed without a message",
		"failed on PAYLOAD-SECRET",
	} {
		got := attrsMap(t, finishedLogAttrs(failedTask(message), time.Second))
		for _, key := range []string{"failure_reason", "failure_message"} {
			if _, has := got[key]; has {
				t.Errorf("%q: %s = %v, want it absent", message, key, got[key])
			}
		}
	}
}

func TestFinishedLogAttrsCutTheMessageOnARuneBoundary(t *testing.T) {
	// A 3-byte character straddles the limit: 599 bytes, then "€" (bytes 599-601).
	message := strings.Repeat("a", maxFailureLogBytes-1) + "€€"
	got := attrsMap(t, finishedLogAttrs(failedTask("max_steps: "+message), time.Second))
	text := got["failure_message"].(string)
	if text != strings.Repeat("a", maxFailureLogBytes-1) {
		t.Errorf("len %d: %q", len(text), text[max(len(text)-5, 0):])
	}

	exact := strings.Repeat("b", maxFailureLogBytes)
	got = attrsMap(t, finishedLogAttrs(failedTask("max_steps: "+exact), time.Second))
	if got["failure_message"] != exact {
		t.Errorf("a message of exactly the limit was cut: %d bytes", len(got["failure_message"].(string)))
	}
}
