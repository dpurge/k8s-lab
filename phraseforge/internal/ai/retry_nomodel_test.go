package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"

	"phraseforge/internal/config"
)

// oneModelWithCooldown is a single model whose provider is held for
// cooldownSeconds after a connection failure, the case in which the correction
// loop's own backoff (5 s, then 15 s) is shorter than the hold.
func oneModelWithCooldown(cooldownSeconds string) string {
	return `
providers:
  - id: p
    type: ollama
    base_url: http://127.0.0.1:1
    api_key_from: ""
models:
  - id: only
    provider: p
    model: only-name
    tags: [default]
    priority: 10
routing:
  breaker: {failures: 3, cooldown_seconds: ` + cooldownSeconds + `}
`
}

// routerClockSleeper records the waits and moves the router's clock by them, as
// a real wait would move real time. The clock starts far from the wall clock,
// so a wait computed from the wall clock could not match.
type routerClockSleeper struct {
	clock *routertest.Clock
	waits []time.Duration
}

func newRouterClockSleeper() *routerClockSleeper {
	return &routerClockSleeper{clock: routertest.NewClock(time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC))}
}

func (s *routerClockSleeper) sleep(_ context.Context, d time.Duration) error {
	s.waits = append(s.waits, d)
	s.clock.Advance(d)
	return nil
}

func clockedService(t *testing.T, modelsYAML string, fake *routertest.Provider, sl *routerClockSleeper, opts ...router.Option) *Service {
	t.Helper()
	s := scriptedService(t, modelsYAML, fake, append([]router.Option{router.WithClock(sl.clock)}, opts...)...)
	s.sleep = sl.sleep
	return s
}

func callOnce(s *Service, tags ...string) (string, error) {
	return s.callWithRetry(context.Background(), "translation", Prompt{ModelTags: tags}, config.PurposeConfig{MaxAttempts: 3}, userHi)
}

func TestASingleModelHeldByAConnectionFailureIsWaitedForAndThenAnswers(t *testing.T) {
	fake := routertest.NewProvider(map[string][]routertest.Result{
		"only": {routertest.Fail(router.FailConnection, "connection refused"), routertest.OK("back again", 3, 4)},
	})
	sl := newRouterClockSleeper()
	got, err := callOnce(clockedService(t, oneModelWithCooldown("60"), fake, sl), "default")
	if err != nil || got != "back again" {
		t.Fatalf("got (%q, %v), want the answer after the cooldown", got, err)
	}
	// Attempt 1 fails; the 5 s backoff leaves the model held for 55 s more, which is
	// what the second wait covers, taken from the router's clock.
	if want := []time.Duration{5 * time.Second, 55 * time.Second}; !equalDurations(sl.waits, want) {
		t.Errorf("waits = %v, want %v", sl.waits, want)
	}
	if len(fake.Calls()) != 2 {
		t.Errorf("provider calls = %d, want 2 (the held attempt calls nobody)", len(fake.Calls()))
	}
}

func TestACooldownThatEndsWithinTheBackoffNeedsNoLongerWait(t *testing.T) {
	fake := routertest.NewProvider(map[string][]routertest.Result{
		"only": {routertest.Fail(router.FailConnection, "connection refused"), routertest.OK("ok", 1, 1)},
	})
	sl := newRouterClockSleeper()
	got, err := callOnce(clockedService(t, oneModelWithCooldown("3"), fake, sl), "default")
	if err != nil || got != "ok" {
		t.Fatalf("got (%q, %v)", got, err)
	}
	if want := []time.Duration{5 * time.Second}; !equalDurations(sl.waits, want) {
		t.Errorf("waits = %v, want only the backoff %v", sl.waits, want)
	}
}

func TestACooldownLongerThanTheCapStillFailsAfterTheAttemptBudget(t *testing.T) {
	fake := routertest.NewProvider(map[string][]routertest.Result{
		"only": {routertest.Fail(router.FailConnection, "connection refused")},
	})
	sl := newRouterClockSleeper()
	_, err := callOnce(clockedService(t, oneModelWithCooldown("600"), fake, sl), "default")
	var noModel *router.NoModelError
	if !errors.As(err, &noModel) || !strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("err = %v, want a NoModelError after 3 attempts", err)
	}
	if want := []time.Duration{5 * time.Second, maxRecoveryWait}; !equalDurations(sl.waits, want) {
		t.Errorf("waits = %v, want the backoff then the cap %v", sl.waits, want)
	}
	if len(fake.Calls()) != 1 {
		t.Errorf("provider calls = %d, want 1", len(fake.Calls()))
	}
}

func TestNoModelForTheTagsFailsAtOnce(t *testing.T) {
	fake := routertest.NewProvider(nil)
	sl := newRouterClockSleeper()
	_, err := callOnce(clockedService(t, oneModelWithCooldown("60"), fake, sl), "no-such-tag")
	var noModel *router.NoModelError
	if !errors.As(err, &noModel) {
		t.Fatalf("err = %v", err)
	}
	if len(sl.waits) != 0 || len(fake.Calls()) != 0 || strings.Contains(err.Error(), "attempts") {
		t.Errorf("waits = %v, calls = %d, err = %v: a configuration problem is not retried", sl.waits, len(fake.Calls()), err)
	}
}

func TestMissingCredentialsFailAtOnce(t *testing.T) {
	fake := routertest.NewProvider(nil)
	sl := newRouterClockSleeper()
	s := clockedService(t, oneModelWithCooldown("60"), fake, sl,
		router.WithAvailability(func(modelcfg.Provider) string { return router.ReasonMissingCredentials }))
	_, err := callOnce(s, "default")
	var noModel *router.NoModelError
	if !errors.As(err, &noModel) || !strings.Contains(err.Error(), router.ReasonMissingCredentials) {
		t.Fatalf("err = %v", err)
	}
	if len(sl.waits) != 0 || len(fake.Calls()) != 0 {
		t.Errorf("waits = %v, calls = %d: waiting does not bring a credential", sl.waits, len(fake.Calls()))
	}
}

func TestTheWaitComesFromTheRoutersClockNotTheWallClock(t *testing.T) {
	// A hold that ends at a fixed instant in 2001 on the router's clock: against
	// the wall clock that instant is long past and the wait would be the plain
	// backoff.
	until := time.Date(2001, 1, 1, 0, 0, 40, 0, time.UTC)
	err := &router.NoModelError{
		Tags: []string{"default"}, EarliestAvailable: until, RetryAfter: 35 * time.Second,
	}
	if got := waitBefore(1, err); got != 35*time.Second {
		t.Errorf("wait = %v, want 35s from RetryAfter", got)
	}
	if got := waitBefore(1, &router.NoModelError{EarliestAvailable: until, RetryAfter: time.Second}); got != 5*time.Second {
		t.Errorf("a short release keeps the backoff: %v", got)
	}
	if got := waitBefore(2, &router.NoModelError{EarliestAvailable: until, RetryAfter: time.Hour}); got != maxRecoveryWait {
		t.Errorf("a long release is capped: %v", got)
	}
	if got := waitBefore(2, errors.New("x")); got != 15*time.Second {
		t.Errorf("another error keeps the backoff: %v", got)
	}
}

func TestACanceledContextCutsTheRecoveryWaitShort(t *testing.T) {
	fake := routertest.NewProvider(map[string][]routertest.Result{
		"only": {routertest.Fail(router.FailConnection, "connection refused")},
	})
	sl := newRouterClockSleeper()
	s := clockedService(t, oneModelWithCooldown("60"), fake, sl)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	s.sleep = func(ctx context.Context, d time.Duration) error {
		calls++
		if calls == 2 { // the recovery wait of the held attempt
			cancel()
			return ctx.Err()
		}
		return sl.sleep(ctx, d)
	}
	_, err := s.callWithRetry(ctx, "translation", Prompt{ModelTags: []string{"default"}}, config.PurposeConfig{MaxAttempts: 5}, userHi)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
