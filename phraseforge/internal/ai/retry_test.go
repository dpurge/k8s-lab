package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s-lab/shared/llm"
)

// scriptedCall returns replies in order and records the messages each call
// received (copied, so later appends can't alter what was recorded).
type scriptedCall struct {
	replies []string
	err     error
	seen    [][]llm.Message
}

func (s *scriptedCall) call(_ context.Context, msgs []llm.Message) (string, error) {
	s.seen = append(s.seen, append([]llm.Message(nil), msgs...))
	if s.err != nil {
		return "", s.err
	}
	return s.replies[len(s.seen)-1], nil
}

func rejectUnless(want string) func(string) error {
	return func(raw string) error {
		if raw != want {
			return errors.New("want " + want + " got " + raw)
		}
		return nil
	}
}

var userPrompt = []llm.Message{{Role: "user", Content: "translate"}}

func TestRunWithCorrectionFirstTrySuccess(t *testing.T) {
	c := &scriptedCall{replies: []string{"ok"}}
	got, err := runWithCorrection(context.Background(), 3, noSleep, c.call, rejectUnless("ok"), userPrompt)
	if err != nil || got != "ok" {
		t.Fatalf("got (%q, %v), want (ok, nil)", got, err)
	}
	if len(c.seen) != 1 {
		t.Errorf("calls = %d, want 1", len(c.seen))
	}
}

func TestRunWithCorrectionSendsBadReplyAndExactErrorBack(t *testing.T) {
	c := &scriptedCall{replies: []string{"bad", "ok"}}
	got, err := runWithCorrection(context.Background(), 3, noSleep, c.call, rejectUnless("ok"), userPrompt)
	if err != nil || got != "ok" {
		t.Fatalf("got (%q, %v), want (ok, nil)", got, err)
	}
	if len(c.seen) != 2 {
		t.Fatalf("calls = %d, want 2", len(c.seen))
	}
	second := c.seen[1]
	if len(second) != 3 {
		t.Fatalf("second call has %d messages, want 3 (original + assistant + user)", len(second))
	}
	if second[0].Role != userPrompt[0].Role || second[0].Content != userPrompt[0].Content {
		t.Errorf("original message changed: %+v", second[0])
	}
	if second[1].Role != "assistant" || second[1].Content != "bad" {
		t.Errorf("second[1] = %+v, want the bad reply as an assistant turn", second[1])
	}
	if second[2].Role != "user" || !strings.Contains(second[2].Content, "want ok got bad") {
		t.Errorf("second[2] = %+v, want a user turn containing the exact validation error", second[2])
	}
	if strings.Contains(second[2].Content, "{{error}}") {
		t.Errorf("correction still contains the placeholder: %q", second[2].Content)
	}
	if len(userPrompt) != 1 {
		t.Errorf("caller's msgs were modified: %+v", userPrompt)
	}
}

func TestRunWithCorrectionExhaustsBudget(t *testing.T) {
	c := &scriptedCall{replies: []string{"a", "b", "c", "d"}}
	_, err := runWithCorrection(context.Background(), 3, noSleep, c.call, rejectUnless("ok"), userPrompt)
	if err == nil || !strings.HasPrefix(err.Error(), "after 3 attempts: ") || !strings.Contains(err.Error(), "got c") {
		t.Fatalf("err = %v, want 'after 3 attempts: ...' wrapping the last validation error", err)
	}
	if len(c.seen) != 3 {
		t.Errorf("calls = %d, want exactly 3", len(c.seen))
	}
}

func TestRunWithCorrectionNonPositiveMaxAttemptsMeansThree(t *testing.T) {
	for _, n := range []int{0, -1} {
		c := &scriptedCall{replies: []string{"a", "b", "c", "d"}}
		_, _ = runWithCorrection(context.Background(), n, noSleep, c.call, rejectUnless("ok"), userPrompt)
		if len(c.seen) != 3 {
			t.Errorf("maxAttempts %d: calls = %d, want 3", n, len(c.seen))
		}
	}
}

func TestRunWithCorrectionSingleAttemptDoesNotCorrect(t *testing.T) {
	c := &scriptedCall{replies: []string{"a", "b"}}
	_, err := runWithCorrection(context.Background(), 1, noSleep, c.call, rejectUnless("ok"), userPrompt)
	if err == nil || len(c.seen) != 1 {
		t.Fatalf("err = %v, calls = %d, want an error after 1 call", err, len(c.seen))
	}
}

func TestRunWithCorrectionCallErrorReturnsImmediately(t *testing.T) {
	boom := errors.New("provider returned 500")
	c := &scriptedCall{err: boom}
	_, err := runWithCorrection(context.Background(), 3, noSleep, c.call, rejectUnless("ok"), userPrompt)
	if !errors.Is(err, boom) || len(c.seen) != 1 {
		t.Fatalf("err = %v, calls = %d, want the call error after 1 call", err, len(c.seen))
	}
}

func TestRunWithCorrectionStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	call := func(context.Context, []llm.Message) (string, error) {
		calls++
		cancel() // job cancelled while the first reply was being produced
		return "bad", nil
	}
	_, err := runWithCorrection(ctx, 3, noSleep, call, rejectUnless("ok"), userPrompt)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err = %v, calls = %d, want context.Canceled after 1 call", err, calls)
	}
}
