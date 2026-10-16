// Package workflowtest holds the fakes tests use in place of a real model and
// real tools. Nothing here talks to a network.
package workflowtest

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"assistant/internal/workflow"
)

// Step is one scripted model reply.
type Step struct {
	// Text is the reply the engine receives.
	Text string
	// InputTokens and OutputTokens are reported as used. Zero means "derive a
	// deterministic count from the text lengths".
	InputTokens, OutputTokens int
	// Err, if set, is returned instead of a reply.
	Err error
	// Check, if set, is run on the request this step answers; a non-nil error
	// is recorded and fails the step.
	Check func(workflow.Request) error
}

// Reply is a Step that just answers with text.
func Reply(text string) Step { return Step{Text: text} }

// ScriptedLLM answers each call with the next scripted Step and records every
// request, so a test can assert on exactly what the engine sent. It is safe
// for concurrent use.
type ScriptedLLM struct {
	mu       sync.Mutex
	steps    []Step
	requests []workflow.Request
	checks   []error
}

// NewScriptedLLM returns a fake that will give the steps in order.
func NewScriptedLLM(steps ...Step) *ScriptedLLM {
	return &ScriptedLLM{steps: steps}
}

// Complete implements workflow.LLM.
func (s *ScriptedLLM) Complete(ctx context.Context, req workflow.Request) (workflow.Response, error) {
	if err := ctx.Err(); err != nil {
		return workflow.Response{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.requests = append(s.requests, req)
	call := len(s.requests)
	if call > len(s.steps) {
		return workflow.Response{}, fmt.Errorf("scripted LLM: unexpected call %d, the script has only %d replies", call, len(s.steps))
	}
	step := s.steps[call-1]
	if step.Check != nil {
		if err := step.Check(req); err != nil {
			s.checks = append(s.checks, fmt.Errorf("call %d: %w", call, err))
			return workflow.Response{}, fmt.Errorf("scripted LLM: request check failed on call %d: %w", call, err)
		}
	}
	if step.Err != nil {
		return workflow.Response{}, step.Err
	}
	in, out := step.InputTokens, step.OutputTokens
	if in == 0 {
		in = (len(req.System)+len(req.Input))/4 + 1
	}
	if out == 0 {
		out = len(step.Text)/4 + 1
	}
	return workflow.Response{Text: step.Text, InputTokens: in, OutputTokens: out}, nil
}

// Requests returns the requests received so far.
func (s *ScriptedLLM) Requests() []workflow.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]workflow.Request(nil), s.requests...)
}

// Calls returns how many times the fake was called.
func (s *ScriptedLLM) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// Remaining returns how many scripted replies were not used.
func (s *ScriptedLLM) Remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if left := len(s.steps) - len(s.requests); left > 0 {
		return left
	}
	return 0
}

// Err returns the first failed request check or an unused-reply problem, so a
// test can finish with one assertion. Call it when the run is over.
func (s *ScriptedLLM) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.checks) > 0 {
		return errors.Join(s.checks...)
	}
	if left := len(s.steps) - len(s.requests); left > 0 {
		return fmt.Errorf("scripted LLM: %d scripted replies were never used", left)
	}
	if extra := len(s.requests) - len(s.steps); extra > 0 {
		return fmt.Errorf("scripted LLM: %d unexpected extra calls", extra)
	}
	return nil
}
