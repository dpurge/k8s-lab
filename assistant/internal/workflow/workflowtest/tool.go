package workflowtest

import (
	"context"
	"sync"
)

// Call records one invocation of a Tool.
type Call struct {
	Args map[string]any
}

// Tool is a fake workflow.Tool that records its calls and answers from Fn.
type Tool struct {
	ToolName string
	// Schema is the argument schema; nil means any object.
	Schema map[string]any
	// Fn produces the result. Nil returns {"ok": true}.
	Fn func(ctx context.Context, args map[string]any) (any, error)

	mu    sync.Mutex
	calls []Call
}

// Name implements workflow.Tool.
func (t *Tool) Name() string { return t.ToolName }

// ArgsSchema implements workflow.Tool.
func (t *Tool) ArgsSchema() map[string]any {
	if t.Schema == nil {
		return map[string]any{"type": "object"}
	}
	return t.Schema
}

// Call implements workflow.Tool.
func (t *Tool) Call(ctx context.Context, args map[string]any) (any, error) {
	t.mu.Lock()
	t.calls = append(t.calls, Call{Args: args})
	t.mu.Unlock()
	if t.Fn == nil {
		return map[string]any{"ok": true}, nil
	}
	return t.Fn(ctx, args)
}

// Calls returns the recorded calls.
func (t *Tool) Calls() []Call {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Call(nil), t.calls...)
}
