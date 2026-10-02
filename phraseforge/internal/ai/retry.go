package ai

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s-lab/shared/llm"
)

// defaultMaxAttempts is the budget used when a purpose's configured
// MaxAttempts is zero or negative.
const defaultMaxAttempts = 3

// effectiveMaxAttempts applies defaultMaxAttempts to a zero or negative
// configured value.
func effectiveMaxAttempts(configured int) int {
	if configured <= 0 {
		return defaultMaxAttempts
	}
	return configured
}

// retryBackoff is the wait before the second, then the third (and any later)
// attempt after a transient failure. Short on purpose: a failure that needs
// longer, such as a first-token timeout, is not retried at all.
var retryBackoff = []time.Duration{5 * time.Second, 15 * time.Second}

// backoffAfter returns the wait after the given number of failed attempts.
func backoffAfter(failedAttempts int) time.Duration {
	i := failedAttempts - 1
	if i >= len(retryBackoff) {
		i = len(retryBackoff) - 1
	}
	return retryBackoff[i]
}

// sleepFunc waits d or until ctx ends, whichever is first, returning ctx's
// error in the latter case. Injected so tests don't really wait.
type sleepFunc func(ctx context.Context, d time.Duration) error

func defaultSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// correctionTemplate is the follow-up user turn sent after a reply fails
// validation. Named placeholder ({{error}}, not %s) — this is prompt text,
// see userMessageTemplate in ai.go.
const correctionTemplate = "Your previous reply was rejected: {{error}}\nReturn only the corrected JSON object."

// runWithCorrection asks call for a reply and checks it with validate (nil
// accepts any reply), making at most maxAttempts calls in total (<= 0 means
// defaultMaxAttempts) — one budget shared by two kinds of retry:
//   - a reply validate rejects goes back to the model, with the exact
//     rejection reason, as two more chat turns (no wait);
//   - a transient call error (llm.IsRetryable) repeats the same messages
//     after a backoff, which a cancelled ctx cuts short.
//
// Any other call error is returned as-is. An exhausted budget returns the
// last error wrapped as "after N attempts". msgs is never modified.
func runWithCorrection(
	ctx context.Context,
	maxAttempts int,
	sleep sleepFunc,
	call func(context.Context, []llm.Message) (string, error),
	validate func(raw string) error,
	msgs []llm.Message,
) (string, error) {
	maxAttempts = effectiveMaxAttempts(maxAttempts)
	history := append([]llm.Message(nil), msgs...)
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		raw, err := call(ctx, history)
		if err != nil {
			if !llm.IsRetryable(err) {
				return "", err
			}
			lastErr = err
			if attempt < maxAttempts {
				if err := sleep(ctx, backoffAfter(attempt)); err != nil {
					return "", err
				}
			}
			continue
		}
		if validate == nil {
			return raw, nil
		}
		if lastErr = validate(raw); lastErr == nil {
			return raw, nil
		}
		history = append(history,
			llm.Message{Role: "assistant", Content: raw},
			llm.Message{Role: "user", Content: strings.NewReplacer("{{error}}", lastErr.Error()).Replace(correctionTemplate)},
		)
	}
	return "", fmt.Errorf("after %d attempts: %w", maxAttempts, lastErr)
}

// sleeper returns the Service's injected sleep, or defaultSleep.
func (s *Service) sleeper() sleepFunc {
	if s.sleep != nil {
		return s.sleep
	}
	return defaultSleep
}
