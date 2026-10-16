// Package workflows runs the workflow a task names. Its built-in workflows are
// noop and ask; the state-machine engine in internal/workflow replaces this
// registry in a later slice.
package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"assistant/internal/tasks"
	"assistant/internal/workflow"
)

// Clock lets noop wait without tests really waiting.
type Clock interface {
	// Sleep waits for d or until ctx ends, in which case it returns ctx's error.
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock waits in real time.
type RealClock struct{}

// Sleep implements Clock.
func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Handler runs one task and returns its result, a JSON value.
type Handler func(ctx context.Context, t tasks.Task) (json.RawMessage, error)

// Registry maps workflow ids to handlers.
type Registry struct {
	clock    Clock
	llm      workflow.LLM
	handlers map[string]Handler
}

// MaxSleep is the longest noop waits, whatever its payload asks.
const MaxSleep = 10 * time.Second

// BuiltinIDs are the ids of the built-in workflows. A YAML workflow may not use
// one.
var BuiltinIDs = []string{"noop", "ask"}

// Option configures a Registry.
type Option func(*Registry)

// WithLLM gives the registry a model, which enables the workflows that need
// one (ask). Without it they are not registered, so a task naming one fails as
// an unknown workflow.
func WithLLM(model workflow.LLM) Option { return func(r *Registry) { r.llm = model } }

// New returns a registry with the built-in workflows.
func New(clock Clock, opts ...Option) *Registry {
	r := &Registry{clock: clock, handlers: map[string]Handler{}}
	for _, opt := range opts {
		opt(r)
	}
	r.handlers["noop"] = r.noop
	if r.llm != nil {
		r.handlers["ask"] = r.ask
	}
	return r
}

// Run runs the handler registered for the task's workflow. A workflow with no
// handler fails the task.
func (r *Registry) Run(ctx context.Context, t tasks.Task) (json.RawMessage, error) {
	h, ok := r.handlers[t.WorkflowID]
	if !ok {
		return nil, fmt.Errorf("unknown workflow: %s", t.WorkflowID)
	}
	return h(ctx, t)
}

// Has reports whether a built-in workflow has the id.
func (r *Registry) Has(id string) bool { _, ok := r.handlers[id]; return ok }

// Names lists the registered workflow ids.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.handlers))
	for n := range r.handlers {
		names = append(names, n)
	}
	return names
}

// noop returns its payload as the result, after waiting payload.sleep_ms
// milliseconds (at most MaxSleep). It exists to watch the queue work before any
// real workflow does.
func (r *Registry) noop(ctx context.Context, t tasks.Task) (json.RawMessage, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(t.Payload, &payload); err != nil {
		return nil, fmt.Errorf("payload is not a JSON object: %w", err)
	}
	if raw, ok := payload["sleep_ms"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var ms int64
		// json.Number would also accept the quoted string "5"; only a JSON
		// number is a number of milliseconds.
		if first := bytes.TrimSpace(raw)[0]; first != '-' && (first < '0' || first > '9') {
			return nil, errors.New("sleep_ms must be a whole number of milliseconds, 0 or more")
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var n json.Number
		if err := dec.Decode(&n); err != nil {
			return nil, errors.New("sleep_ms must be a whole number of milliseconds, 0 or more")
		}
		parsed, err := n.Int64()
		if err != nil || parsed < 0 {
			return nil, errors.New("sleep_ms must be a whole number of milliseconds, 0 or more")
		}
		ms = parsed
		// Compare in milliseconds first: converting a huge value to a Duration
		// would overflow.
		wait := MaxSleep
		if ms <= MaxSleep.Milliseconds() {
			wait = time.Duration(ms) * time.Millisecond
		}
		if err := r.clock.Sleep(ctx, wait); err != nil {
			return nil, fmt.Errorf("interrupted while waiting: %w", err)
		}
	}
	return t.Payload, nil
}
