// Package tools holds the tools workflows can call. Only stand-ins exist so
// far: memory, the knowledge base and web search come in later slices, and a
// workflow that uses them must still be able to run in the meantime.
package tools

import (
	"context"
	"fmt"

	"assistant/internal/workflow"
)

// stubNote is in every stub result, so that neither the model nor a person
// reading the trace mistakes it for real data.
const stubNote = "STUB: this tool is not implemented yet and returned canned data; do not treat it as knowledge about the user or the world."

type stub struct {
	name   string
	schema map[string]any
	call   func(args map[string]any) any
}

func (s stub) Name() string               { return s.name }
func (s stub) ArgsSchema() map[string]any { return s.schema }
func (s stub) Call(_ context.Context, args map[string]any) (any, error) {
	return s.call(args), nil
}

func stringArg(name string, description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{name},
		"properties": map[string]any{
			name: map[string]any{"type": "string", "minLength": 1, "description": description},
		},
	}
}

// Stubs returns stand-ins for memory_search, memory_add, rag_search and
// web_search. Each result says it is a stub.
func Stubs() []workflow.Tool {
	return []workflow.Tool{
		stub{"memory_search", stringArg("query", "What to recall about the user."), func(args map[string]any) any {
			return map[string]any{"stub": true, "note": stubNote, "query": args["query"], "memories": []any{}}
		}},
		stub{"memory_add", stringArg("text", "A fact worth remembering about the user."), func(args map[string]any) any {
			return map[string]any{"stub": true, "note": stubNote, "saved": false}
		}},
		stub{"rag_search", stringArg("query", "What to look up in the knowledge base."), func(args map[string]any) any {
			return map[string]any{
				"stub": true, "note": stubNote, "query": args["query"],
				"documents": []any{map[string]any{
					"title": fmt.Sprintf("Canned outline for %q", args["query"]),
					"text":  "No knowledge base exists yet. Use a generic outline: 1. Foundations, 2. Core vocabulary or concepts, 3. Practice with short exercises. Write the lesson and the exercises yourself.",
				}},
			}
		}},
		stub{"web_search", stringArg("query", "What to search the web for."), func(args map[string]any) any {
			return map[string]any{"stub": true, "note": stubNote, "query": args["query"], "results": []any{}}
		}},
	}
}
