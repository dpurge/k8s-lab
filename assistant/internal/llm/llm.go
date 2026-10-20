// Package llm is the assistant's view of the shared model router
// (k8s-lab/shared/router): the router's names, plus the one policy only the
// assistant has, that every call is made for a user.
package llm

import (
	"context"
	"errors"

	"assistant/internal/callctx"
	"assistant/internal/workflow"
	"k8s-lab/shared/router"
)

type (
	Router       = router.Router
	Option       = router.Option
	Provider     = router.Provider
	Clock        = router.Clock
	Observer     = router.Observer
	Attempt      = router.Attempt
	Failure      = router.Failure
	FailureKind  = router.FailureKind
	ModelStatus  = router.ModelStatus
	NoModelError = router.NoModelError
	AttemptError = router.AttemptError

	AllFailedError = router.AllFailedError
)

const (
	FailConnection  = router.FailConnection
	FailTimeout     = router.FailTimeout
	FailServer      = router.FailServer
	FailRateLimited = router.FailRateLimited
	FailClient      = router.FailClient
	FailCanceled    = router.FailCanceled

	OutcomeOK          = router.OutcomeOK
	OutcomeError       = router.OutcomeError
	OutcomeTimeout     = router.OutcomeTimeout
	OutcomeRateLimited = router.OutcomeRateLimited
	OutcomeCanceled    = router.OutcomeCanceled

	ReasonCooldown           = router.ReasonCooldown
	ReasonMissingCredentials = router.ReasonMissingCredentials
)

var (
	NewRouter    = router.NewRouter
	WithClock    = router.WithClock
	WithObserver = router.WithObserver
	WithLogger   = router.WithLogger
	WithLogAttrs = router.WithLogAttrs

	WithAvailability       = router.WithAvailability
	CredentialAvailability = router.CredentialAvailability
)

var _ workflow.LLM = (*Router)(nil)

// ErrNoAttribution is returned when attribution is required and missing.
var ErrNoAttribution = errors.New("a model call must be made for a user: its context carries none")

// RequireAttribution makes the router refuse a call whose context does not
// carry a user (see package callctx): every call must be someone's, so its
// cost has an owner.
func RequireAttribution() Option {
	return router.WithCallCheck(func(ctx context.Context) error {
		if _, ok := callctx.From(ctx); !ok {
			return ErrNoAttribution
		}
		return nil
	})
}
