package router_test

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"
)

func noModel(t *testing.T, err error) *router.NoModelError {
	t.Helper()
	var e *router.NoModelError
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want a NoModelError", err)
	}
	return e
}

func TestNoModelAfterAConnectionFailureIsRecoverableAndSaysWhenToRetry(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"embed": {routertest.Fail(router.FailConnection, "connection refused")},
	})
	if _, err := r.call("embed"); err == nil {
		t.Fatal("the first call must fail")
	}
	r.clock.Advance(10 * time.Second)
	_, err := r.call("embed")
	e := noModel(t, err)
	if !e.Recoverable() || e.SkippedForOtherReason {
		t.Fatalf("a model held by the cooldown is recoverable: %+v", e)
	}
	want := t0.Add(60 * time.Second)
	if !e.EarliestAvailable.Equal(want) {
		t.Errorf("EarliestAvailable = %v, want %v", e.EarliestAvailable, want)
	}
	// RetryAfter comes from the router's clock (a 2026 fixed time here), never
	// from the wall clock.
	if e.RetryAfter != 50*time.Second {
		t.Errorf("RetryAfter = %v, want 50s", e.RetryAfter)
	}
}

func TestNoModelReportsTheEarliestOfSeveralCooldowns(t *testing.T) {
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu":  {routertest.Fail(router.FailRateLimited, "429")},
		"mid-gpu":  {routertest.Fail(router.FailRateLimited, "429")},
		"laptop-a": {routertest.Fail(router.FailConnection, "refused")},
	})
	r.call("review") // big-gpu 429 (30s), then laptop-a refused: laptop held 60s
	r.clock.Advance(time.Second)
	_, err := r.call("review")
	e := noModel(t, err)
	if !e.Recoverable() {
		t.Fatalf("both matching models are in cooldown: %+v", e)
	}
	if e.RetryAfter != 29*time.Second {
		t.Errorf("RetryAfter = %v, want 29s (big-gpu's rate-limit hold is the earliest)", e.RetryAfter)
	}
}

func TestNoModelForTagsNoModelHasIsNotRecoverable(t *testing.T) {
	_, err := newRig(t, baseConfig, nil).router.Select([]string{"vision"})
	e := noModel(t, err)
	if e.Recoverable() || !e.EarliestAvailable.IsZero() || e.RetryAfter != 0 {
		t.Errorf("no model with the tags is a configuration problem: %+v", e)
	}
}

func TestNoModelBecauseOfMissingCredentialsIsNotRecoverable(t *testing.T) {
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(baseConfig))
	if err != nil {
		t.Fatal(err)
	}
	prov := routertest.NewProvider(nil)
	r, err := router.NewRouter(cfg, map[string]router.Provider{"ollama": prov},
		router.WithClock(routertest.NewClock(t0)),
		router.WithAvailability(func(p modelcfg.Provider) string {
			if p.ID == "laptop" {
				return router.ReasonMissingCredentials
			}
			return ""
		}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Select([]string{"embed"})
	e := noModel(t, err)
	if e.Recoverable() || !e.SkippedForOtherReason {
		t.Errorf("a missing credential does not recover by waiting: %+v", e)
	}
}

func TestACooldownDoesNotMakeNoModelRecoverableWhenAnotherMatchingModelLacksCredentials(t *testing.T) {
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(baseConfig))
	if err != nil {
		t.Fatal(err)
	}
	prov := routertest.NewProvider(map[string][]routertest.Result{"big-gpu": {routertest.Fail(router.FailConnection, "refused")}})
	r, err := router.NewRouter(cfg, map[string]router.Provider{"ollama": prov},
		router.WithClock(routertest.NewClock(t0)),
		router.WithAvailability(func(p modelcfg.Provider) string {
			if p.ID == "laptop" {
				return router.ReasonMissingCredentials
			}
			return ""
		}))
	if err != nil {
		t.Fatal(err)
	}
	r.Complete(t.Context(), router.Request{Tags: []string{"review"}})
	_, err = r.Select([]string{"review"})
	e := noModel(t, err)
	if e.Recoverable() || e.EarliestAvailable.IsZero() || !e.SkippedForOtherReason {
		t.Errorf("one model in cooldown, one without credentials: %+v", e)
	}
}

func TestTruncateTextCutsOnARuneBoundary(t *testing.T) {
	text := strings.Repeat("żółć", 50) // 2-byte runes
	for max := 1; max < 40; max++ {
		got := router.TruncateText(text, max)
		if !utf8.ValidString(got) {
			t.Fatalf("max %d: invalid UTF-8 %q", max, got)
		}
		if !strings.HasSuffix(got, "…") || len(got) > max+len("…") {
			t.Fatalf("max %d: got %q (%d bytes)", max, got, len(got))
		}
	}
	if got := router.TruncateText("short", 10); got != "short" {
		t.Errorf("text within the bound is untouched: %q", got)
	}
	if got := router.TruncateText("ab\xffcd", 10); got != "abcd" {
		t.Errorf("invalid UTF-8 is dropped: %q", got)
	}
	if got := router.TruncateText("ab\xffcd", 3); !utf8.ValidString(got) {
		t.Errorf("invalid UTF-8 in cut text: %q", got)
	}
}

func TestHasAllTags(t *testing.T) {
	cases := []struct {
		have, want []string
		ok         bool
	}{
		{[]string{"a", "b"}, nil, true},
		{nil, nil, true},
		{[]string{"a", "b"}, []string{"b"}, true},
		{[]string{"a", "b"}, []string{"b", "a"}, true},
		{[]string{"a"}, []string{"a", "c"}, false},
		{nil, []string{"a"}, false},
	}
	for _, c := range cases {
		if got := router.HasAllTags(c.have, c.want); got != c.ok {
			t.Errorf("HasAllTags(%v, %v) = %v", c.have, c.want, got)
		}
	}
}

func TestAHugeProviderErrorIsBoundedInTheAttempt(t *testing.T) {
	huge := strings.Repeat("é", 500000) // 1 MB of two-byte runes
	r := newRig(t, baseConfig, map[string][]routertest.Result{
		"embed": {routertest.Fail(router.FailServer, huge)},
	})
	r.call("embed")
	attempts := r.observer.Attempts()
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d", len(attempts))
	}
	got := attempts[0].Error
	if len(got) > 1000+len("…") || !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Errorf("Attempt.Error is %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
}
