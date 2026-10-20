package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"k8s-lab/shared/modelcfg"
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
	// HTTPStatus is the status code of the provider's reply; 0 when unknown or
	// when there was no reply.
	HTTPStatus int
	// RequestID is the provider's own id of the call, for a support request;
	// empty when unknown.
	RequestID string
}

func (f *Failure) Error() string { return fmt.Sprintf("%s: %v%s", f.Kind, f.Err, f.detail()) }

// detail is the " (HTTP 529, request-id req_x)" suffix, empty when neither
// field is set.
func (f *Failure) detail() string {
	var parts []string
	if f.HTTPStatus != 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", f.HTTPStatus))
	}
	if f.RequestID != "" {
		parts = append(parts, "request-id "+f.RequestID)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func (f *Failure) Unwrap() error { return f.Err }

// Provider makes one call to a model. A new kind of provider implements this
// one method and is registered under its type name.
type Provider interface {
	Complete(ctx context.Context, p modelcfg.Provider, m modelcfg.Model, req Request) (Response, error)
}

// Clock tells the time; tests move it by hand.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// maxAttemptErrorBytes bounds Attempt.Error, so that a provider that returns a
// huge error body cannot fill rows, API replies or logs with it.
const maxAttemptErrorBytes = 1000

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
	// HTTPStatus and RequestID are the provider's, when the call failed with
	// them known; 0 and empty otherwise.
	HTTPStatus int
	RequestID  string
}

// Observer is told about every attempt, in the context of the call.
type Observer interface {
	Observe(ctx context.Context, a Attempt)
}

// Router routes calls over the configured models.
type Router struct {
	cfg       *modelcfg.Config
	providers map[string]Provider
	clock     Clock
	observer  Observer
	// callCheck, if set, may refuse a call before any model is chosen.
	callCheck func(ctx context.Context) error

	// logger is nil until set, meaning slog.Default() at the time of a call.
	logger    *slog.Logger
	logAttrs  func(ctx context.Context) []slog.Attr
	newCallID func() string

	state *breakerState

	// availability, if set, says why a provider cannot be used right now ("" when
	// it can); unusable remembers the providers it last reported, to log each
	// change once.
	availability func(modelcfg.Provider) string
	unusableMu   sync.Mutex
	unusable     map[string]string
}

// Option configures a Router.
type Option func(*Router)

// WithClock sets the clock the breaker uses.
func WithClock(c Clock) Option { return func(r *Router) { r.clock = c } }

// WithCallCheck sets a check run before models are selected; if it returns an
// error the call is refused with that error, unchanged, and nothing is called
// or recorded. A caller uses it to enforce a precondition on the context, such
// as that every call has an owner.
func WithCallCheck(check func(ctx context.Context) error) Option {
	return func(r *Router) { r.callCheck = check }
}

// WithObserver sets who hears about every attempt.
func WithObserver(o Observer) Option { return func(r *Router) { r.observer = o } }

// NewRouter returns a router over cfg. providers maps a provider type (such as
// "ollama") to its implementation; every type used in cfg must be present.
func NewRouter(cfg *modelcfg.Config, providers map[string]Provider, opts ...Option) (*Router, error) {
	for _, p := range cfg.Providers {
		if providers[p.Type] == nil {
			return nil, fmt.Errorf("provider %q has type %q, which has no implementation", p.ID, p.Type)
		}
	}
	r := &Router{cfg: cfg, providers: providers, clock: realClock{}, newCallID: randomCallID}
	for _, o := range opts {
		o(r)
	}
	r.state = newBreakerState(cfg.Routing.Breaker)
	r.unusable = map[string]string{}
	// The first evaluation lets the operator see at startup which providers
	// cannot be used.
	r.providerReasons()
	return r, nil
}

// NoModelError is returned when no model can take a call: none has all the
// tags, or the ones that do are all unavailable.
type NoModelError struct {
	Tags []string
	// Reasons has one entry per configured model that was considered.
	Reasons []string
	// EarliestAvailable is when the first model that has the tags, and that the
	// breaker or a provider hold holds out (ReasonCooldown), can be called
	// again; zero when no such model was skipped.
	EarliestAvailable time.Time
	// RetryAfter is EarliestAvailable less the router's clock when the error was
	// made, so a caller can wait without a clock of its own; zero when
	// EarliestAvailable is.
	RetryAfter time.Duration
	// SkippedForOtherReason is true when a model that has the tags was skipped for
	// a reason that waiting does not cure, such as ReasonMissingCredentials.
	SkippedForOtherReason bool
}

// Recoverable reports whether waiting RetryAfter can make a model available: at
// least one model that has the tags is held by the breaker, and none was left
// out for another reason. It is false when no model has the tags (a
// configuration problem) or a credential is missing.
func (e *NoModelError) Recoverable() bool {
	return !e.EarliestAvailable.IsZero() && !e.SkippedForOtherReason
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

// Select returns the models that have every tag and are available now, highest
// priority first, ties in the order of the file. No tags matches every model.
func (r *Router) Select(tags []string) ([]modelcfg.Model, error) {
	candidates, _, err := r.selectModels(tags)
	return candidates, err
}

// selectModels is Select that also reports the models left out, by the breaker
// or because their provider is unusable.
func (r *Router) selectModels(tags []string) ([]modelcfg.Model, []skippedModel, error) {
	now := r.clock.Now()
	unusable := r.providerReasons()
	var candidates []modelcfg.Model
	var reasons []string
	var skipped []skippedModel
	var earliest time.Time
	skippedForOtherReason := false
	for _, m := range r.cfg.Models {
		if !HasAllTags(m.Tags, tags) {
			reasons = append(reasons, fmt.Sprintf("%s lacks the tags", m.ID))
			continue
		}
		if reason := unusable[m.Provider]; reason != "" {
			reasons = append(reasons, fmt.Sprintf("%s is unavailable (%s)", m.ID, reason))
			skipped = append(skipped, skippedModel{ModelID: m.ID, Reason: reason})
			skippedForOtherReason = true
			continue
		}
		if until, byProvider := r.state.hold(m, now); !until.IsZero() {
			reasons = append(reasons, fmt.Sprintf("%s is unavailable until %s", m.ID, until.Format("15:04:05")))
			reason := skipModelUnavailable
			if byProvider {
				reason = skipProviderUnavailable
			}
			skipped = append(skipped, skippedModel{ModelID: m.ID, Reason: reason, AvailableAgain: until.UTC().Format(time.RFC3339)})
			if earliest.IsZero() || until.Before(earliest) {
				earliest = until
			}
			continue
		}
		candidates = append(candidates, m)
	}
	if len(candidates) == 0 {
		err := &NoModelError{Tags: tags, Reasons: reasons, EarliestAvailable: earliest, SkippedForOtherReason: skippedForOtherReason}
		if !earliest.IsZero() {
			err.RetryAfter = earliest.Sub(now)
		}
		return nil, skipped, err
	}
	// A stable insertion sort keeps file order among equal priorities.
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0 && candidates[j].Priority > candidates[j-1].Priority; j-- {
			candidates[j], candidates[j-1] = candidates[j-1], candidates[j]
		}
	}
	return candidates, skipped, nil
}

// Complete sends the request to the best available model and falls back down
// the list when one fails. A canceled context ends the call at once; it is not
// held against any model. Every call after model selection leaves log lines
// that share a call id; see the package documentation.
func (r *Router) Complete(ctx context.Context, req Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if r.callCheck != nil {
		if err := r.callCheck(ctx); err != nil {
			return Response{}, err
		}
	}
	started := r.clock.Now()
	log := r.newCallLog(r.newCallID(), req.Tags)
	candidates, skipped, err := r.selectModels(req.Tags)
	if err != nil {
		log.noModel(ctx, r.modelIDs())
		log.done(ctx, callSummary{outcome: callFailed, total: r.clock.Now().Sub(started)})
		return Response{}, err
	}
	candidateIDs := make([]string, len(candidates))
	for i, m := range candidates {
		candidateIDs[i] = m.ID
	}
	log.candidates(ctx, candidateIDs, skipped)

	var failed []AttemptError
	var chain []string
	attempts := 0
	finish := func(outcome, winner string, fellBack bool) {
		log.done(ctx, callSummary{
			outcome: outcome, attempts: attempts, winner: winner,
			total: r.clock.Now().Sub(started), fellBack: fellBack, chain: chain,
		})
	}
	for i, m := range candidates {
		if err := ctx.Err(); err != nil {
			finish(callCanceled, "", attempts > 1)
			return Response{}, err
		}
		// An earlier attempt in this call may have taken the machine down.
		if until, down := r.state.unavailableUntil(m, r.clock.Now()); down {
			failed = append(failed, AttemptError{ModelID: m.ID, Err: fmt.Errorf("skipped: its provider is unavailable until %s", until.Format("15:04:05"))})
			chain = append(chain, m.ID+":skipped")
			continue
		}
		provider, _ := r.cfg.Provider(m.Provider)
		attemptStart := r.clock.Now()
		attempts++
		resp, err := r.providers[provider.Type].Complete(ctx, provider, m, req)
		elapsed := r.clock.Now().Sub(attemptStart)

		attempt := Attempt{
			ModelID: m.ID, ProviderID: m.Provider, ModelName: m.Name, Tags: req.Tags,
			StartedAt: attemptStart, Duration: elapsed,
		}
		if err == nil {
			attempt.Outcome = OutcomeOK
			attempt.InputTokens, attempt.OutputTokens = resp.InputTokens, resp.OutputTokens
			r.observe(ctx, attempt)
			log.attempt(ctx, attempts, len(candidates), provider.Type, attempt, nil)
			if rec := r.state.success(m); rec.wasHeld || rec.failures > 0 {
				log.breakerClosed(ctx, m.ID, rec.failures)
			}
			resp.ModelID = m.ID
			finish(callOK, m.ID, i > 0)
			return resp, nil
		}

		failure := classify(err)
		if ctx.Err() != nil {
			failure = &Failure{Kind: FailCanceled, Err: ctx.Err()}
		}
		attempt.Outcome, attempt.Error = outcomeOf(failure.Kind), TruncateText(failure.Err.Error()+failure.detail(), maxAttemptErrorBytes)
		attempt.HTTPStatus, attempt.RequestID = failure.HTTPStatus, failure.RequestID
		r.observe(ctx, attempt)
		log.attempt(ctx, attempts, len(candidates), provider.Type, attempt, failure)
		chain = append(chain, m.ID+":"+string(failure.Kind))
		if failure.Kind == FailCanceled {
			if ctxErr := ctx.Err(); ctxErr != nil {
				finish(callCanceled, "", attempts > 1)
				return Response{}, ctxErr
			}
			// The provider gave up on its own while the caller still waits: not the
			// model's fault, so the breaker is not told, but the next model may answer.
			failed = append(failed, AttemptError{ModelID: m.ID, Err: failure})
			continue
		}
		r.reportTransition(ctx, log, m, failure.Kind, r.state.failure(m, failure.Kind, r.clock.Now()))
		failed = append(failed, AttemptError{ModelID: m.ID, Err: failure})
	}
	finish(callFailed, "", attempts > 1)
	return Response{}, &AllFailedError{Tags: req.Tags, Attempts: failed}
}

// reportTransition logs a model or provider the breaker has just taken out of
// service.
func (r *Router) reportTransition(ctx context.Context, log *callLog, m modelcfg.Model, kind FailureKind, t transition) {
	switch {
	case t.providerHeld:
		var ids []string
		for _, other := range r.cfg.Models {
			if other.Provider == m.Provider {
				ids = append(ids, other.ID)
			}
		}
		log.providerDown(ctx, m.Provider, ids, t, r.cfg.Routing.Breaker.Cooldown())
	case t.modelHeld:
		cooldown := r.cfg.Routing.Breaker.Cooldown()
		if kind == FailRateLimited {
			cooldown = r.cfg.Routing.Breaker.RateLimitedCooldown()
		}
		log.breakerOpen(ctx, m.ID, m.Provider, kind, t, cooldown)
	}
}

func (r *Router) modelIDs() []string {
	ids := make([]string, len(r.cfg.Models))
	for i, m := range r.cfg.Models {
		ids[i] = m.ID
	}
	return ids
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
	// out of service, or while its provider lacks a credential it needs.
	Available bool
	// Reason says why it is not available: ReasonCooldown while the breaker
	// holds it, ReasonMissingCredentials; empty when it is available.
	Reason string
	// Until is when it becomes available again after a cooldown; zero if it is
	// available now or waits for a credential.
	Until time.Time
}

// Status reports every configured model, in the order of the file.
func (r *Router) Status() []ModelStatus {
	now := r.clock.Now()
	out := make([]ModelStatus, len(r.cfg.Models))
	unusable := r.providerReasons()
	for i, m := range r.cfg.Models {
		st := ModelStatus{ID: m.ID, Provider: m.Provider, Tags: m.Tags, Priority: m.Priority, Available: true}
		if reason := unusable[m.Provider]; reason != "" {
			st.Available, st.Reason = false, reason
		} else if until, down := r.state.unavailableUntil(m, now); down {
			st.Available, st.Reason, st.Until = false, ReasonCooldown, until
		}
		out[i] = st
	}
	return out
}
