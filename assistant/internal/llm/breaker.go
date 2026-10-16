package llm

import (
	"sync"
	"time"

	"assistant/internal/models"
)

// breakerState remembers which models, and which providers, are set aside.
type breakerState struct {
	cfg models.Breaker

	mu sync.Mutex
	// models: consecutive failures and, once tripped, until when the model is
	// held out. Failures stay at the threshold after a trip, so the first
	// failure after the cooldown trips it again at once; a success clears it.
	failures map[string]int
	until    map[string]time.Time
	// providers held out because their machine could not be reached.
	providerUntil map[string]time.Time
}

func newBreakerState(cfg models.Breaker) *breakerState {
	return &breakerState{
		cfg:           cfg,
		failures:      map[string]int{},
		until:         map[string]time.Time{},
		providerUntil: map[string]time.Time{},
	}
}

// unavailableUntil reports whether the model is held out at now, and until when.
func (b *breakerState) unavailableUntil(m models.Model, now time.Time) (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var latest time.Time
	if t := b.until[m.ID]; t.After(now) {
		latest = t
	}
	if t := b.providerUntil[m.Provider]; t.After(now) && t.After(latest) {
		latest = t
	}
	return latest, !latest.IsZero()
}

// success clears the model's failures and its provider's hold: the machine and
// the model are both evidently working.
func (b *breakerState) success(m models.Model) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.failures, m.ID)
	delete(b.until, m.ID)
	delete(b.providerUntil, m.Provider)
}

// failure records a failed call. Which kinds count, and how, is the breaker's
// whole policy:
//   - connection: the provider's machine is unreachable, so every model on it is
//     held out for the cooldown;
//   - server, timeout: counted; the threshold in a row holds the model out;
//   - rate limited: held out for the shorter rate-limit cooldown at once;
//   - client (a refused request) and canceled: the model is fine, nothing changes.
func (b *breakerState) failure(m models.Model, kind FailureKind, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch kind {
	case FailConnection:
		b.providerUntil[m.Provider] = now.Add(b.cfg.Cooldown())
	case FailRateLimited:
		b.until[m.ID] = now.Add(b.cfg.RateLimitedCooldown())
	case FailServer, FailTimeout:
		if b.failures[m.ID] < b.cfg.Failures {
			b.failures[m.ID]++
		}
		if b.failures[m.ID] >= b.cfg.Failures {
			b.until[m.ID] = now.Add(b.cfg.Cooldown())
		}
	}
}
