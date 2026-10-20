package ai

import (
	"context"
	"errors"
	"io"
	"net"

	"k8s-lab/shared/llm"
	"k8s-lab/shared/router"
)

// isRetryable reports whether a failed call is worth repeating after a backoff.
//
//   - A cancelled or expired context is not: the caller gave up.
//   - router.NoModelError is when every model that has the tags is held out by
//     the breaker (see NoModelError.Recoverable): that ends by itself, and
//     runWithCorrection waits for it. It is not when no model has the tags (a
//     configuration problem) or a credential is missing: waiting changes neither.
//   - router.AllFailedError is when any attempt's failure is.
//   - A failure whose cause is one shared/llm understands (an Ollama failure)
//     is judged by llm.IsRetryable, exactly as before the router.
//   - Any other provider's failure is judged by its kind: connection, server and
//     rate_limited are retryable; timeout, client and canceled are not.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var allFailed *router.AllFailedError
	if errors.As(err, &allFailed) {
		for _, a := range allFailed.Attempts {
			if isRetryableFailure(a.Err) {
				return true
			}
		}
		return false
	}
	var noModel *router.NoModelError
	if errors.As(err, &noModel) {
		return noModel.Recoverable()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return isRetryableFailure(err)
}

// isRetryableFailure judges one model's failure.
func isRetryableFailure(err error) bool {
	var failure *router.Failure
	if !errors.As(err, &failure) {
		return llm.IsRetryable(err)
	}
	if failure.Kind == router.FailCanceled {
		return false
	}
	if isSharedLLMError(failure.Err) {
		return llm.IsRetryable(failure.Err)
	}
	switch failure.Kind {
	case router.FailConnection, router.FailServer, router.FailRateLimited:
		return true
	}
	return false
}

// isSharedLLMError says whether err is one of the errors llm.IsRetryable has a
// rule for, as opposed to another provider's.
func isSharedLLMError(err error) bool {
	var timeout *llm.TimeoutError
	var status *llm.StatusError
	var netErr net.Error
	return errors.As(err, &timeout) || errors.As(err, &status) || errors.As(err, &netErr) ||
		errors.Is(err, llm.ErrStreamIncomplete) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
