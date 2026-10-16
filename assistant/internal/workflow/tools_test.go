package workflow

import (
	"context"
	"strings"
	"testing"
)

type fakeTool struct{ name string }

func (f fakeTool) Name() string               { return f.name }
func (f fakeTool) ArgsSchema() map[string]any { return map[string]any{"type": "object"} }
func (f fakeTool) Call(context.Context, map[string]any) (any, error) {
	return map[string]any{"ok": true}, nil
}

func TestRegistryLookup(t *testing.T) {
	reg, err := NewRegistry(fakeTool{"b_tool"}, fakeTool{"a_tool"})
	if err != nil {
		t.Fatal(err)
	}
	if !reg.Has("a_tool") || reg.Has("c_tool") {
		t.Errorf("Has gave the wrong answer")
	}
	if tool, ok := reg.Get("b_tool"); !ok || tool.Name() != "b_tool" {
		t.Errorf("Get(b_tool) = %v, %v", tool, ok)
	}
	if _, ok := reg.Get("c_tool"); ok {
		t.Errorf("Get(c_tool) should not find anything")
	}
	if got := strings.Join(reg.Names(), ","); got != "a_tool,b_tool" {
		t.Errorf("Names = %q, want sorted", got)
	}
}

func TestRegistryRejectsBadTools(t *testing.T) {
	if _, err := NewRegistry(fakeTool{"x"}, fakeTool{"x"}); err == nil || !strings.Contains(err.Error(), `duplicate name "x"`) {
		t.Errorf("duplicate: err = %v", err)
	}
	if _, err := NewRegistry(fakeTool{""}); err == nil || !strings.Contains(err.Error(), "empty name") {
		t.Errorf("empty: err = %v", err)
	}
}

func TestEmptyRegistry(t *testing.T) {
	reg, err := NewRegistry()
	if err != nil || reg.Has("x") || len(reg.Names()) != 0 {
		t.Errorf("empty registry misbehaves: %v %v", err, reg.Names())
	}
}
