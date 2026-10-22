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
	// Vector is what an embedding call returns; Response.InputTokens is its
	// token count.
	Vector []float32
}

// OK is a successful result with text and token counts.
func OK(text string, in, out int) Result {
	return Result{Response: router.Response{Text: text, InputTokens: in, OutputTokens: out}}
}

// Embedding is a successful embedding result with a vector of n numbers.
func Embedding(n, inputTokens int) Result {
	v := make([]float32, n)
	for i := range v {
		v[i] = float32(i + 1)
	}
	return Result{Response: router.Response{InputTokens: inputTokens}, Vector: v}
}

// Fail is a result that fails with the given kind.
func Fail(kind router.FailureKind, message string) Result {
	return Result{Err: &router.Failure{Kind: kind, Err: fmt.Errorf("%s", message)}}
}

// Call is one recorded call to a scripted provider.
type Call struct {
	ModelID    string
	ProviderID string
	// Request is set for a chat call, EmbedRequest for an embedding call.
	Request      router.Request
	EmbedRequest router.EmbedRequest
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

var _ router.EmbeddingProvider = (*Provider)(nil)

// NewProvider returns a provider with the given scripts.
func NewProvider(scripts map[string][]Result) *Provider {
	return &Provider{scripts: scripts, used: map[string]int{}}
}

// Complete implements router.Provider.
func (p *Provider) Complete(ctx context.Context, prov modelcfg.Provider, m modelcfg.Model, req router.Request) (router.Response, error) {
	res, err := p.take(ctx, Call{ModelID: m.ID, ProviderID: prov.ID, Request: req})
	if err != nil {
		return router.Response{}, err
	}
	return res.Response, res.Err
}

// Embed implements router.EmbeddingProvider: it answers from the same script as
// Complete, with the result's Vector.
func (p *Provider) Embed(ctx context.Context, prov modelcfg.Provider, m modelcfg.Model, req router.EmbedRequest) (router.EmbedResponse, error) {
	res, err := p.take(ctx, Call{ModelID: m.ID, ProviderID: prov.ID, EmbedRequest: req})
	if err != nil {
		return router.EmbedResponse{}, err
	}
	if res.Err != nil {
		return router.EmbedResponse{}, res.Err
	}
	return router.EmbedResponse{Vector: res.Vector, InputTokens: res.Response.InputTokens}, nil
}

// take records the call and returns the model's next scripted result, after
// honoring Block and Wait; the error is the context's, when it ended first.
func (p *Provider) take(ctx context.Context, call Call) (Result, error) {
	p.mu.Lock()
	p.calls = append(p.calls, call)
	script := p.scripts[call.ModelID]
	var res Result
	switch {
	case len(script) == 0:
		res = OK("", 0, 0)
	case p.used[call.ModelID] < len(script):
		res = script[p.used[call.ModelID]]
	default:
		res = script[len(script)-1]
	}
	p.used[call.ModelID]++
	p.mu.Unlock()

	if res.Block {
		<-ctx.Done()
		return Result{}, ctx.Err()
	}
	if res.Wait != nil {
		select {
		case <-res.Wait:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	return res, nil
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
