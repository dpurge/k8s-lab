// Package a2ui checks the interfaces the assistant sends to the chat page.
//
// A2UI (v1.0) is a protocol in which an agent describes a user interface in
// JSON messages and a renderer draws it. Whatever composes those messages, a
// workflow or a model, is untrusted: nothing is stored or sent to a page before
// it has passed the checks here, against the protocol's own JSON schemas
// (vendored under schemas/a2ui/v1_0) and against the rules a schema cannot say
// (ids, references, cycles, depth, size).
package a2ui

import (
	"fmt"
	"strings"
)

// Problem is one thing wrong with a message, located by a JSON pointer into it.
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	if p.Path == "" {
		return p.Message
	}
	return p.Path + ": " + p.Message
}

// ValidationError says why a message was refused, in words fit to show to the
// model that wrote it, so that it can correct itself.
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.String()
	}
	return strings.Join(parts, "; ")
}

func refuse(path, format string, args ...any) *ValidationError {
	return &ValidationError{Problems: []Problem{{Path: path, Message: fmt.Sprintf(format, args...)}}}
}

// Limits bound how much interface one agent may draw. They match the page's
// own limits (shared/ui/src/a2ui/limits.ts), so a message is refused here
// before the page would have to.
type Limits struct {
	// MaxMessageBytes is the size of one message as JSON.
	MaxMessageBytes int
	// MaxComponentsPerMessage is how many components one updateComponents (or
	// createSurface) may carry.
	MaxComponentsPerMessage int
	// MaxComponentsPerSurface is how many components one surface may hold.
	MaxComponentsPerSurface int
	// MaxDepth is how many levels of nesting the tree may have below the root.
	MaxDepth int
}

// DefaultLimits are the limits used unless a Validator is given others.
var DefaultLimits = Limits{
	MaxMessageBytes:         256 * 1024,
	MaxComponentsPerMessage: 500,
	MaxComponentsPerSurface: 1000,
	MaxDepth:                20,
}
