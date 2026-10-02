package ai

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"k8s-lab/shared/llm"
)

func noSleep(context.Context, time.Duration) error { return nil }

// sleepRecorder is a sleepFunc that records the waits instead of waiting.
type sleepRecorder struct{ waits []time.Duration }

func (r *sleepRecorder) sleep(_ context.Context, d time.Duration) error {
	r.waits = append(r.waits, d)
	return nil
}

type step struct {
	reply string
	err   error
}

// stepCall plays steps in order and records the messages of each call.
type stepCall struct {
	steps []step
	seen  [][]llm.Message
}

func (s *stepCall) call(_ context.Context, msgs []llm.Message) (string, error) {
	s.seen = append(s.seen, append([]llm.Message(nil), msgs...))
	st := s.steps[len(s.seen)-1]
	return st.reply, st.err
}

var errServerDown = &llm.StatusError{StatusCode: 503, Status: "503 Service Unavailable", Body: []byte("loading")}

func TestTransientErrorRetriesAfterBackoff(t *testing.T) {
	c := &stepCall{steps: []step{{err: errServerDown}, {reply: "ok"}}}
	rec := &sleepRecorder{}
	got, err := runWithCorrection(context.Background(), 3, rec.sleep, c.call, nil, userPrompt)
	if err != nil || got != "ok" {
		t.Fatalf("got (%q, %v), want (ok, nil)", got, err)
	}
	if want := []time.Duration{5 * time.Second}; !reflect.DeepEqual(rec.waits, want) {
		t.Errorf("waits = %v, want %v", rec.waits, want)
	}
	if len(c.seen[1]) != 1 {
		t.Errorf("retry sent %d messages, want the original 1 (no correction turn)", len(c.seen[1]))
	}
}

func TestTransientErrorExhaustsBudget(t *testing.T) {
	c := &stepCall{steps: []step{{err: errServerDown}, {err: errServerDown}, {err: errServerDown}, {err: errServerDown}}}
	rec := &sleepRecorder{}
	_, err := runWithCorrection(context.Background(), 3, rec.sleep, c.call, nil, userPrompt)
	var se *llm.StatusError
	if err == nil || !strings.HasPrefix(err.Error(), "after 3 attempts: ") || !errors.As(err, &se) {
		t.Fatalf("err = %v, want 'after 3 attempts: ...' wrapping the status error", err)
	}
	if len(c.seen) != 3 {
		t.Errorf("calls = %d, want exactly 3", len(c.seen))
	}
	if want := []time.Duration{5 * time.Second, 15 * time.Second}; !reflect.DeepEqual(rec.waits, want) {
		t.Errorf("waits = %v, want %v (no wait after the last attempt)", rec.waits, want)
	}
}

func TestNonRetryableCallErrorsAreNotRetried(t *testing.T) {
	cases := map[string]error{
		"first token timeout": &llm.TimeoutError{Limit: llm.LimitFirstToken, Model: "m", After: time.Minute},
		"overall timeout":     &llm.TimeoutError{Limit: llm.LimitOverall, Model: "m", After: time.Minute},
		"4xx":                 &llm.StatusError{StatusCode: 404, Status: "404 Not Found"},
	}
	for name, callErr := range cases {
		c := &stepCall{steps: []step{{err: callErr}}}
		rec := &sleepRecorder{}
		_, err := runWithCorrection(context.Background(), 3, rec.sleep, c.call, nil, userPrompt)
		if !errors.Is(err, callErr) || len(c.seen) != 1 || len(rec.waits) != 0 {
			t.Errorf("%s: err = %v, calls = %d, waits = %v, want the error after 1 call and no wait", name, err, len(c.seen), rec.waits)
		}
	}
}

func TestTransientAndValidationShareOneBudget(t *testing.T) {
	// transient, invalid, valid: 3 attempts, the last carrying the correction.
	c := &stepCall{steps: []step{{err: errServerDown}, {reply: "bad"}, {reply: "ok"}}}
	rec := &sleepRecorder{}
	got, err := runWithCorrection(context.Background(), 3, rec.sleep, c.call, rejectUnless("ok"), userPrompt)
	if err != nil || got != "ok" {
		t.Fatalf("got (%q, %v), want (ok, nil)", got, err)
	}
	if len(c.seen[1]) != 1 || len(c.seen[2]) != 3 {
		t.Errorf("messages per call = %d, %d, %d, want 1, 1, 3", len(c.seen[0]), len(c.seen[1]), len(c.seen[2]))
	}

	// invalid, transient, then out of budget: the retry resends the corrected history.
	c = &stepCall{steps: []step{{reply: "bad"}, {err: errServerDown}, {err: errServerDown}}}
	rec = &sleepRecorder{}
	_, err = runWithCorrection(context.Background(), 3, rec.sleep, c.call, rejectUnless("ok"), userPrompt)
	if err == nil || !strings.HasPrefix(err.Error(), "after 3 attempts: ") || len(c.seen) != 3 {
		t.Fatalf("err = %v, calls = %d, want 3 calls then 'after 3 attempts'", err, len(c.seen))
	}
	if len(c.seen[1]) != 3 || len(c.seen[2]) != 3 {
		t.Errorf("retry after a rejection sent %d and %d messages, want the same 3 both times", len(c.seen[1]), len(c.seen[2]))
	}
}

func TestNilValidateAcceptsAnyReply(t *testing.T) {
	c := &stepCall{steps: []step{{reply: "anything"}}}
	got, err := runWithCorrection(context.Background(), 3, noSleep, c.call, nil, userPrompt)
	if err != nil || got != "anything" {
		t.Fatalf("got (%q, %v), want (anything, nil)", got, err)
	}
}

func TestCancelDuringBackoffStopsRetrying(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	call := func(context.Context, []llm.Message) (string, error) {
		calls++
		cancel() // the job is cancelled while the failing call returns
		return "", errServerDown
	}
	start := time.Now()
	_, err := runWithCorrection(ctx, 3, defaultSleep, call, nil, userPrompt)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err = %v, calls = %d, want context.Canceled after 1 call", err, calls)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v, want the 5s backoff cut short", elapsed)
	}
}

func TestDefaultSleepWaitsAndHonoursCancel(t *testing.T) {
	if err := defaultSleep(context.Background(), 10*time.Millisecond); err != nil {
		t.Errorf("uncancelled sleep err = %v, want nil", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)
	start := time.Now()
	if err := defaultSleep(ctx, time.Hour); !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Errorf("cancelled sleep err = %v after %v, want context.Canceled promptly", err, time.Since(start))
	}
}

func TestBackoffAfter(t *testing.T) {
	for failed, want := range map[int]time.Duration{1: 5 * time.Second, 2: 15 * time.Second, 3: 15 * time.Second, 9: 15 * time.Second} {
		if got := backoffAfter(failed); got != want {
			t.Errorf("backoffAfter(%d) = %v, want %v", failed, got, want)
		}
	}
}
