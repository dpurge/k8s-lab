package workflowtest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"assistant/internal/workflow"
)

var _ workflow.LLM = (*ScriptedLLM)(nil)
var _ workflow.Tool = (*Tool)(nil)

func TestScriptedLLMAnswersInOrderAndRecords(t *testing.T) {
	llm := NewScriptedLLM(Reply("one"), Reply("two"))
	for i, want := range []string{"one", "two"} {
		resp, err := llm.Complete(context.Background(), workflow.Request{System: "s", Input: "in"})
		if err != nil || resp.Text != want {
			t.Fatalf("call %d: %q %v", i+1, resp.Text, err)
		}
	}
	if llm.Calls() != 2 || llm.Remaining() != 0 || len(llm.Requests()) != 2 {
		t.Errorf("calls=%d remaining=%d", llm.Calls(), llm.Remaining())
	}
	if err := llm.Err(); err != nil {
		t.Errorf("Err() = %v", err)
	}
}

func TestScriptedLLMTokenCounts(t *testing.T) {
	llm := NewScriptedLLM(Reply("abcdefgh"), Step{Text: "x", InputTokens: 7, OutputTokens: 3})
	first, _ := llm.Complete(context.Background(), workflow.Request{System: "12345678", Input: "12345678"})
	if first.InputTokens != 5 || first.OutputTokens != 3 {
		t.Errorf("derived tokens = %d in, %d out", first.InputTokens, first.OutputTokens)
	}
	second, _ := llm.Complete(context.Background(), workflow.Request{})
	if second.InputTokens != 7 || second.OutputTokens != 3 {
		t.Errorf("explicit tokens = %d in, %d out", second.InputTokens, second.OutputTokens)
	}
}

func TestScriptedLLMRunsOutOfReplies(t *testing.T) {
	llm := NewScriptedLLM(Reply("only"))
	_, _ = llm.Complete(context.Background(), workflow.Request{})
	_, err := llm.Complete(context.Background(), workflow.Request{})
	if err == nil || !strings.Contains(err.Error(), "unexpected call 2") {
		t.Fatalf("err = %v", err)
	}
	if err := llm.Err(); err == nil || !strings.Contains(err.Error(), "unexpected extra calls") {
		t.Errorf("Err() = %v", err)
	}
}

func TestScriptedLLMUnusedRepliesAreReported(t *testing.T) {
	llm := NewScriptedLLM(Reply("a"), Reply("b"))
	_, _ = llm.Complete(context.Background(), workflow.Request{})
	if llm.Remaining() != 1 {
		t.Errorf("Remaining = %d", llm.Remaining())
	}
	if err := llm.Err(); err == nil || !strings.Contains(err.Error(), "never used") {
		t.Errorf("Err() = %v", err)
	}
}

func TestScriptedLLMReturnsScriptedError(t *testing.T) {
	boom := errors.New("provider down")
	llm := NewScriptedLLM(Step{Err: boom})
	if _, err := llm.Complete(context.Background(), workflow.Request{}); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestScriptedLLMCheck(t *testing.T) {
	llm := NewScriptedLLM(Step{Text: "x", Check: func(r workflow.Request) error {
		if !strings.Contains(r.Input, "expected") {
			return errors.New("input lacks the expected text")
		}
		return nil
	}})
	if _, err := llm.Complete(context.Background(), workflow.Request{Input: "something else"}); err == nil {
		t.Fatal("a failed check should fail the call")
	}
	if err := llm.Err(); err == nil || !strings.Contains(err.Error(), "input lacks the expected text") {
		t.Errorf("Err() = %v", err)
	}

	ok := NewScriptedLLM(Step{Text: "x", Check: func(workflow.Request) error { return nil }})
	if _, err := ok.Complete(context.Background(), workflow.Request{}); err != nil {
		t.Errorf("passing check: %v", err)
	}
}

func TestScriptedLLMHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	llm := NewScriptedLLM(Reply("x"))
	if _, err := llm.Complete(ctx, workflow.Request{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if llm.Calls() != 0 {
		t.Errorf("a cancelled call must not consume a scripted reply")
	}
}

func TestToolRecordsCalls(t *testing.T) {
	tool := &Tool{ToolName: "rag_search"}
	res, err := tool.Call(context.Background(), map[string]any{"query": "q"})
	if err != nil || res.(map[string]any)["ok"] != true {
		t.Errorf("default result = %v %v", res, err)
	}
	if calls := tool.Calls(); len(calls) != 1 || calls[0].Args["query"] != "q" {
		t.Errorf("calls = %v", calls)
	}
	if tool.Name() != "rag_search" || tool.ArgsSchema()["type"] != "object" {
		t.Errorf("name or default schema wrong")
	}
}

func TestToolUsesFnAndSchema(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"q"}}
	tool := &Tool{ToolName: "t", Schema: schema, Fn: func(context.Context, map[string]any) (any, error) {
		return nil, errors.New("tool failed")
	}}
	if _, err := tool.Call(context.Background(), nil); err == nil || err.Error() != "tool failed" {
		t.Errorf("err = %v", err)
	}
	if tool.ArgsSchema()["required"] == nil {
		t.Errorf("custom schema not returned")
	}
}
