// Package llm routes a model call to one of several configured models: the
// highest-priority available model that has the tags the call needs, falling
// back to lower ones when a model fails. A circuit breaker, not pings, decides
// what is available.
package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"assistant/internal/callctx"
	"assistant/internal/models"
	"assistant/internal/workflow"
)

// FailureKind says what went wrong with a call, which decides how the router
// reacts.
type FailureKind string

const (
	// FailConnection: the provider's endpoint could not be reached (refused, no
	// route, DNS, dial timeout). It is a fault of the machine, not of one model.
	FailConnection FailureKind = "connection"
	// FailTimeout: the endpoint answered but the call took too long.
	FailTimeout FailureKind = "timeout"
	// FailServer: the provider reported an error of its own (5xx, a broken stream).
	FailServer FailureKind = "server"
	// FailRateLimited: the provider asked to slow down (429).
	FailRateLimited FailureKind = "rate_limited"
	// FailClient: the request was refused as invalid (4xx other than 429). The
	// model is healthy.
	FailClient FailureKind = "client"
	// FailCanceled: the caller's context ended.
	FailCanceled FailureKind = "canceled"
)

// Failure is the error a Provider returns, classified.
type Failure struct {
	Kind FailureKind
	Err  error
}

func (f *Failure) Error() string { return fmt.Sprintf("%s: %v", f.Kind, f.Err) }
func (f *Failure) Unwrap() error { return f.Err }

// Provider makes one call to a model. A new kind of provider implements this
// one method and is registered under its type name.
type Provider interface {
	Complete(ctx context.Context, p models.Provider, m models.Model, req workflow.Request) (workflow.Response, error)
}

// Clock tells the time; tests move it by hand.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Outcome of one attempt, as recorded.
const (
	OutcomeOK          = "ok"
	OutcomeError       = "error"
	OutcomeTimeout     = "timeout"
	OutcomeRateLimited = "rate_limited"
	OutcomeCanceled    = "canceled"
)

// Attempt is one call to one model, successful or not. The router reports every
// attempt to its Observer; a fallback therefore leaves several.
type Attempt struct {
	ModelID      string
	ProviderID   string
	ModelName    string
	Tags         []string // the tags the call asked for
	StartedAt    time.Time
	Duration     time.Duration
	InputTokens  int
	OutputTokens int
	Outcome      string
	Error        string // the provider's error text, empty for ok
}

// Observer is told about every attempt, in the context of the call.
type Observer interface {
	Observe(ctx context.Context, a Attempt)
}

// Router implements workflow.LLM over the configured models.
type Router struct {
	cfg       *models.Config
	providers map[string]Provider
	clock     Clock
	observer  Observer
	// requireAttribution refuses a call that does not say which user it is for.
	requireAttribution bool

	state *breakerState
}

var _ workflow.LLM = (*Router)(nil)

// Option configures a Router.
type Option func(*Router)

// WithClock sets the clock the breaker uses.
func WithClock(c Clock) Option { return func(r *Router) { r.clock = c } }

// RequireAttribution makes the router refuse a call whose context does not
// carry a user (see package callctx): every call must be someone's, so its
// cost has an owner.
func RequireAttribution() Option { return func(r *Router) { r.requireAttribution = true } }

// ErrNoAttribution is returned when attribution is required and missing.
var ErrNoAttribution = errors.New("a model call must be made for a user: its context carries none")

// WithObserver sets who hears about every attempt.
func WithObserver(o Observer) Option { return func(r *Router) { r.observer = o } }

// NewRouter returns a router over cfg. providers maps a provider type (such as
// "ollama") to its implementation; every type used in cfg must be present.
func NewRouter(cfg *models.Config, providers map[string]Provider, opts ...Option) (*Router, error) {
	for _, p := range cfg.Providers {
		if providers[p.Type] == nil {
			return nil, fmt.Errorf("provider %q has type %q, which has no implementation", p.ID, p.Type)
		}
	}
	r := &Router{cfg: cfg, providers: providers, clock: realClock{}}
	for _, o := range opts {
		o(r)
	}
	r.state = newBreakerState(cfg.Routing.Breaker)
	return r, nil
}

// NoModelError is returned when no model can take a call: none has all the
// tags, or the ones that do are all unavailable.
type NoModelError struct {
	Tags []string
	// Reasons has one entry per configured model that was considered.
	Reasons []string
}

func (e *NoModelError) Error() string {
	return fmt.Sprintf("no model can take a call tagged %s: %s", tagList(e.Tags), strings.Join(e.Reasons, "; "))
}

// AttemptError is why one model failed.
type AttemptError struct {
	ModelID string
	Err     error
}

// AllFailedError is returned when every candidate failed.
type AllFailedError struct {
	Tags     []string
	Attempts []AttemptError
}

func (e *AllFailedError) Error() string {
	parts := make([]string, len(e.Attempts))
	for i, a := range e.Attempts {
		parts[i] = fmt.Sprintf("%s: %v", a.ModelID, a.Err)
	}
	return fmt.Sprintf("every model failed for a call tagged %s: %s", tagList(e.Tags), strings.Join(parts, "; "))
}

// Unwrap lets errors.Is and errors.As look into the attempts.
func (e *AllFailedError) Unwrap() []error {
	errs := make([]error, len(e.Attempts))
	for i, a := range e.Attempts {
		errs[i] = a.Err
	}
	return errs
}

func tagList(tags []string) string {
	if len(tags) == 0 {
		return "(no tags)"
	}
	return "[" + strings.Join(tags, ", ") + "]"
}

func hasAll(have, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Select returns the models that have every tag and are available now, highest
// priority first, ties in the order of the file. No tags matches every model.
func (r *Router) Select(tags []string) ([]models.Model, error) {
	now := r.clock.Now()
	var candidates []models.Model
	var reasons []string
	for _, m := range r.cfg.Models {
		if !hasAll(m.Tags, tags) {
			reasons = append(reasons, fmt.Sprintf("%s lacks the tags", m.ID))
			continue
		}
		if until, down := r.state.unavailableUntil(m, now); down {
			reasons = append(reasons, fmt.Sprintf("%s is unavailable until %s", m.ID, until.Format("15:04:05")))
			continue
		}
		candidates = append(candidates, m)
	}
	if len(candidates) == 0 {
		return nil, &NoModelError{Tags: tags, Reasons: reasons}
	}
	// A stable insertion sort keeps file order among equal priorities.
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0 && candidates[j].Priority > candidates[j-1].Priority; j-- {
			candidates[j], candidates[j-1] = candidates[j-1], candidates[j]
		}
	}
	return candidates, nil
}

// Complete sends the request to the best available model and falls back down
// the list when one fails. A canceled context ends the call at once; it is not
// held against any model.
func (r *Router) Complete(ctx context.Context, req workflow.Request) (workflow.Response, error) {
	if err := ctx.Err(); err != nil {
		return workflow.Response{}, err
	}
	if r.requireAttribution {
		if _, ok := callctx.From(ctx); !ok {
			return workflow.Response{}, ErrNoAttribution
		}
	}
	candidates, err := r.Select(req.Tags)
	if err != nil {
		return workflow.Response{}, err
	}

	var failed []AttemptError
	for _, m := range candidates {
		if err := ctx.Err(); err != nil {
			return workflow.Response{}, err
		}
		// An earlier attempt in this call may have taken the machine down.
		if until, down := r.state.unavailableUntil(m, r.clock.Now()); down {
			failed = append(failed, AttemptError{ModelID: m.ID, Err: fmt.Errorf("skipped: its provider is unavailable until %s", until.Format("15:04:05"))})
			continue
		}
		provider, _ := r.cfg.Provider(m.Provider)
		started := r.clock.Now()
		resp, err := r.providers[provider.Type].Complete(ctx, provider, m, req)
		elapsed := r.clock.Now().Sub(started)

		attempt := Attempt{
			ModelID: m.ID, ProviderID: m.Provider, ModelName: m.Name, Tags: req.Tags,
			StartedAt: started, Duration: elapsed,
		}
		if err == nil {
			attempt.Outcome = OutcomeOK
			attempt.InputTokens, attempt.OutputTokens = resp.InputTokens, resp.OutputTokens
			r.observe(ctx, attempt)
			r.state.success(m)
			resp.ModelID = m.ID
			return resp, nil
		}

		failure := classify(err)
		if ctx.Err() != nil {
			failure = &Failure{Kind: FailCanceled, Err: ctx.Err()}
		}
		attempt.Outcome, attempt.Error = outcomeOf(failure.Kind), failure.Err.Error()
		r.observe(ctx, attempt)
		if failure.Kind == FailCanceled {
			return workflow.Response{}, ctx.Err()
		}
		r.state.failure(m, failure.Kind, r.clock.Now())
		failed = append(failed, AttemptError{ModelID: m.ID, Err: failure})
	}
	return workflow.Response{}, &AllFailedError{Tags: req.Tags, Attempts: failed}
}

func (r *Router) observe(ctx context.Context, a Attempt) {
	if r.observer != nil {
		r.observer.Observe(ctx, a)
	}
}

// classify turns any error from a provider into a Failure. An error that is
// not one is treated as the provider's own fault, so that a model that keeps
// returning something unexpected is set aside.
func classify(err error) *Failure {
	var f *Failure
	if errors.As(err, &f) {
		return f
	}
	if errors.Is(err, context.Canceled) {
		return &Failure{Kind: FailCanceled, Err: err}
	}
	return &Failure{Kind: FailServer, Err: err}
}

func outcomeOf(k FailureKind) string {
	switch k {
	case FailTimeout:
		return OutcomeTimeout
	case FailRateLimited:
		return OutcomeRateLimited
	case FailCanceled:
		return OutcomeCanceled
	}
	return OutcomeError
}

// ModelStatus is one model's current standing.
type ModelStatus struct {
	ID       string
	Provider string
	Tags     []string
	Priority int
	// Available is false while the breaker holds the model, or its provider,
	// out of service.
	Available bool
	// Until is when it becomes available again; zero if it is available now.
	Until time.Time
}

// Status reports every configured model, in the order of the file.
func (r *Router) Status() []ModelStatus {
	now := r.clock.Now()
	out := make([]ModelStatus, len(r.cfg.Models))
	for i, m := range r.cfg.Models {
		until, down := r.state.unavailableUntil(m, now)
		out[i] = ModelStatus{ID: m.ID, Provider: m.Provider, Tags: m.Tags, Priority: m.Priority, Available: !down, Until: until}
	}
	return out
}
