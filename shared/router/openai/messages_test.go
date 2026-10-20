package openai

import (
	"context"
	"strings"
	"testing"

	"k8s-lab/shared/router"
)

func wire(t *testing.T, req router.Request) [][2]string {
	t.Helper()
	f := newFake(t, 200, okBody)
	if _, err := New(envWith(secretKey)).Complete(context.Background(), keyed(f), model, req); err != nil {
		t.Fatal(err)
	}
	var out [][2]string
	for _, m := range f.reqs[0].Fields["messages"].([]any) {
		mm := m.(map[string]any)
		out = append(out, [2]string{mm["role"].(string), mm["content"].(string)})
	}
	return out
}

func TestAThreeTurnHistoryIsSentInOrderWithSystemFirst(t *testing.T) {
	got := wire(t, router.Request{System: "SYS", Input: "ignored", Messages: []router.Message{
		{Role: "system", Content: "S2"}, {Role: "user", Content: "u1"}, {Role: "assistant", Content: "a1"}, {Role: "user", Content: "u2"},
	}})
	want := [][2]string{{"system", "SYS\n\nS2"}, {"user", "u1"}, {"assistant", "a1"}, {"user", "u2"}}
	if len(got) != len(want) {
		t.Fatalf("messages = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestNoSystemPromptMeansNoSystemMessage(t *testing.T) {
	got := wire(t, router.Request{Input: "question"})
	if len(got) != 1 || got[0] != [2]string{"user", "question"} {
		t.Errorf("messages = %v", got)
	}
}

func TestAnInvalidHistoryIsAClientFailureAndNothingIsSent(t *testing.T) {
	u, a := router.Message{Role: "user", Content: "PROMPT-TEXT"}, router.Message{Role: "assistant", Content: "REPLY-TEXT"}
	for name, msgs := range map[string][]router.Message{
		"starts with assistant": {a, u},
		"two users":             {u, u},
		"no turns":              {{Role: "system", Content: "PROMPT-TEXT"}},
	} {
		f := newFake(t, 200, okBody)
		_, err := New(envWith(secretKey)).Complete(context.Background(), keyed(f), model, router.Request{Messages: msgs})
		if k := failure(t, err).Kind; k != router.FailClient {
			t.Errorf("%s: kind = %s", name, k)
		}
		if strings.Contains(err.Error(), "PROMPT-TEXT") || strings.Contains(err.Error(), "REPLY-TEXT") {
			t.Errorf("%s: the error carries message text: %v", name, err)
		}
		f.mu.Lock()
		n := len(f.reqs)
		f.mu.Unlock()
		if n != 0 {
			t.Errorf("%s: an invalid history must not be sent", name)
		}
	}
}
