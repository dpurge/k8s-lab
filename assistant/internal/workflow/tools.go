package workflow

import (
	"context"
	"fmt"
	"sort"
)

// Tool is something the model can call from a state that lists it.
type Tool interface {
	// Name is how workflows and the model refer to the tool.
	Name() string
	// ArgsSchema is the JSON Schema the call's arguments must satisfy.
	ArgsSchema() map[string]any
	// Call runs the tool. The result must be JSON-encodable.
	Call(ctx context.Context, args map[string]any) (any, error)
}

// ToolSet answers whether a tool name is known.
type ToolSet interface {
	Has(name string) bool
}

// Registry holds the tools available to the engine, by name.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry returns a registry of the given tools. It returns an error for
// an empty or duplicate name.
func NewRegistry(tools ...Tool) (*Registry, error) {
	r := &Registry{tools: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		if t.Name() == "" {
			return nil, fmt.Errorf("register tool: empty name")
		}
		if _, dup := r.tools[t.Name()]; dup {
			return nil, fmt.Errorf("register tool: duplicate name %q", t.Name())
		}
		r.tools[t.Name()] = t
	}
	return r, nil
}

// Get returns the named tool.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Has reports whether a tool with the name is registered.
func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}

// Names returns the registered tool names, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// NameSet is a ToolSet made of plain names, for checks that need no tool
// implementations, such as the validate command.
type NameSet map[string]bool

// Has reports whether name is in the set.
func (s NameSet) Has(name string) bool { return s[name] }
