// Package routertest holds the fakes tests use in place of real providers and
// real time.
package routertest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
)

// Result is what a scripted provider returns for one call.
type Result struct {
	Response router.Response
	Err      error
	// Block, if set, makes the call wait for the context to end and return its
	// error, to test cancellation.
	Block bool
	// Wait, if set, holds the call until it is closed (or the context ends), so
	// a test can finish other calls first.
	Wait <-chan struct{}
}

// OK is a successful result with text and token counts.
func OK(text string, in, out int) Result {
	return Result{Response: router.Response{Text: text, InputTokens: in, OutputTokens: out}}
}

// Fail is a result that fails with the given kind.
func Fail(kind router.FailureKind, message string) Result {
	return Result{Err: &router.Failure{Kind: kind, Err: fmt.Errorf("%s", message)}}
}

// Call is one recorded call to a scripted provider.
type Call struct {
	ModelID    string
	ProviderID string
	Request    router.Request
}

// Provider answers each call from a script keyed by model id: the next result
// for that model, the last one repeated when the script runs out. A model with
// no script answers with an empty success.
type Provider struct {
	mu      sync.Mutex
	scripts map[string][]Result
	used    map[string]int
	calls   []Call
}

// NewProvider returns a provider with the given scripts.
func NewProvider(scripts map[string][]Result) *Provider {
	return &Provider{scripts: scripts, used: map[string]int{}}
}

// Complete implements router.Provider.
func (p *Provider) Complete(ctx context.Context, prov modelcfg.Provider, m modelcfg.Model, req router.Request) (router.Response, error) {
	p.mu.Lock()
	p.calls = append(p.calls, Call{ModelID: m.ID, ProviderID: prov.ID, Request: req})
	script := p.scripts[m.ID]
	var res Result
	switch {
	case len(script) == 0:
		res = OK("", 0, 0)
	case p.used[m.ID] < len(script):
		res = script[p.used[m.ID]]
	default:
		res = script[len(script)-1]
	}
	p.used[m.ID]++
	p.mu.Unlock()

	if res.Block {
		<-ctx.Done()
		return router.Response{}, ctx.Err()
	}
	if res.Wait != nil {
		select {
		case <-res.Wait:
		case <-ctx.Done():
			return router.Response{}, ctx.Err()
		}
	}
	return res.Response, res.Err
}

// Calls returns the calls made so far.
func (p *Provider) Calls() []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Call(nil), p.calls...)
}

// Models returns the ids of the models called, in order.
func (p *Provider) Models() []string {
	calls := p.Calls()
	ids := make([]string, len(calls))
	for i, c := range calls {
		ids[i] = c.ModelID
	}
	return ids
}

// Clock is a router.Clock that only moves when told to. Every call to Now also
// advances it by Step, so a call has a measurable duration.
type Clock struct {
	mu   sync.Mutex
	now  time.Time
	Step time.Duration
}

// NewClock starts at start.
func NewClock(start time.Time) *Clock { return &Clock{now: start} }

// Now implements router.Clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.now
	c.now = c.now.Add(c.Step)
	return t
}

// Advance moves time forward.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Observer records the attempts it hears about.
type Observer struct {
	mu       sync.Mutex
	attempts []router.Attempt
}

// Observe implements router.Observer.
func (o *Observer) Observe(_ context.Context, a router.Attempt) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.attempts = append(o.attempts, a)
}

// Attempts returns what was observed.
func (o *Observer) Attempts() []router.Attempt {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]router.Attempt(nil), o.attempts...)
}
