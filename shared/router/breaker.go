package router

import (
	"sync"
	"time"

	"k8s-lab/shared/modelcfg"
)

// breakerState remembers which models, and which providers, are set aside.
type breakerState struct {
	cfg modelcfg.Breaker

	mu sync.Mutex
	// models: consecutive failures and, once tripped, until when the model is
	// held out. Failures stay at the threshold after a trip, so the first
	// failure after the cooldown trips it again at once; a success clears it.
	failures map[string]int
	until    map[string]time.Time
	// providers held out because their machine could not be reached.
	providerUntil map[string]time.Time
}

func newBreakerState(cfg modelcfg.Breaker) *breakerState {
	return &breakerState{
		cfg:           cfg,
		failures:      map[string]int{},
		until:         map[string]time.Time{},
		providerUntil: map[string]time.Time{},
	}
}

// unavailableUntil reports whether the model is held out at now, and until when.
func (b *breakerState) unavailableUntil(m modelcfg.Model, now time.Time) (time.Time, bool) {
	latest, _ := b.hold(m, now)
	return latest, !latest.IsZero()
}

// hold is unavailableUntil that also says whether the provider's hold, rather
// than the model's own, is the one that lasts longest.
func (b *breakerState) hold(m modelcfg.Model, now time.Time) (until time.Time, byProvider bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t := b.until[m.ID]; t.After(now) {
		until = t
	}
	if t := b.providerUntil[m.Provider]; t.After(now) && t.After(until) {
		until, byProvider = t, true
	}
	return until, byProvider
}

// recovery says what a success cleared, so the router can report it.
type recovery struct {
	// wasHeld: the model had been held out at some point since its last success.
	wasHeld bool
	// failures is the streak the success ended.
	failures int
}

// success clears the model's failures and its provider's hold: the machine and
// the model are both evidently working.
func (b *breakerState) success(m modelcfg.Model) recovery {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, held := b.until[m.ID]
	rec := recovery{wasHeld: held, failures: b.failures[m.ID]}
	delete(b.failures, m.ID)
	delete(b.until, m.ID)
	delete(b.providerUntil, m.Provider)
	return rec
}

// transition says what a failure did to the breaker. The zero value means
// nothing was taken out of service.
type transition struct {
	// modelHeld: the model was held out until `until`, after `failures`
	// consecutive failures.
	modelHeld bool
	failures  int
	until     time.Time
	// providerHeld: the whole provider was held out until `until`.
	providerHeld bool
}

// failure records a failed call and reports whether it took anything out of
// service. Which kinds count, and how, is the breaker's whole policy:
//   - connection: the provider's machine is unreachable, so every model on it is
//     held out for the cooldown;
//   - server, timeout: counted; the threshold in a row holds the model out;
//   - rate limited: held out for the shorter rate-limit cooldown at once;
//   - client (a refused request) and canceled: the model is fine, nothing changes.
func (b *breakerState) failure(m modelcfg.Model, kind FailureKind, now time.Time) transition {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch kind {
	case FailConnection:
		until := now.Add(b.cfg.Cooldown())
		b.providerUntil[m.Provider] = until
		return transition{providerHeld: true, until: until}
	case FailRateLimited:
		until := now.Add(b.cfg.RateLimitedCooldown())
		b.until[m.ID] = until
		return transition{modelHeld: true, failures: b.failures[m.ID], until: until}
	case FailServer, FailTimeout:
		if b.failures[m.ID] < b.cfg.Failures {
			b.failures[m.ID]++
		}
		if b.failures[m.ID] >= b.cfg.Failures {
			until := now.Add(b.cfg.Cooldown())
			b.until[m.ID] = until
			return transition{modelHeld: true, failures: b.failures[m.ID], until: until}
		}
	}
	return transition{}
}
