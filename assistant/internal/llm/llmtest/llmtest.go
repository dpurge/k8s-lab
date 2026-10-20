// Package llmtest holds the fakes tests use in place of real providers and
// real time. They live in shared/router/routertest; these are the same types.
package llmtest

import (
	"time"

	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"
)

type (
	Result   = routertest.Result
	Call     = routertest.Call
	Provider = routertest.Provider
	Clock    = routertest.Clock
	Observer = routertest.Observer
)

// OK is a successful result with text and token counts.
func OK(text string, in, out int) Result { return routertest.OK(text, in, out) }

// Fail is a result that fails with the given kind.
func Fail(kind router.FailureKind, message string) Result { return routertest.Fail(kind, message) }

// NewProvider returns a provider with the given scripts.
func NewProvider(scripts map[string][]Result) *Provider { return routertest.NewProvider(scripts) }

// NewClock starts at start.
func NewClock(start time.Time) *Clock { return routertest.NewClock(start) }
