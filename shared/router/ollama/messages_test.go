package ollama

import (
	"net/http"
	"strings"
	"testing"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
)

func wireMessages(t *testing.T, req router.Request) []any {
	t.Helper()
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, chunk("hi"), done(1, 1)) })
	if _, err := complete(t, New(), f.URL, modelcfg.Options{}, req); err != nil {
		t.Fatal(err)
	}
	msgs, _ := f.last(t).Fields["messages"].([]any)
	return msgs
}

func roleContent(m any) [2]string {
	mm := m.(map[string]any)
	return [2]string{mm["role"].(string), mm["content"].(string)}
}

func TestAThreeTurnHistoryIsSentInOrderWithSystemFirst(t *testing.T) {
	msgs := wireMessages(t, router.Request{System: "SYS", Input: "ignored", Messages: []router.Message{
		{Role: "system", Content: "S2"}, {Role: "user", Content: "u1"}, {Role: "assistant", Content: "a1"}, {Role: "user", Content: "u2"},
	}})
	want := [][2]string{{"system", "SYS\n\nS2"}, {"user", "u1"}, {"assistant", "a1"}, {"user", "u2"}}
	if len(msgs) != len(want) {
		t.Fatalf("messages = %v", msgs)
	}
	for i, w := range want {
		if got := roleContent(msgs[i]); got != w {
			t.Errorf("message %d = %v, want %v", i, got, w)
		}
	}
}

func TestAHistoryWithoutASystemPromptHasNoSystemMessage(t *testing.T) {
	msgs := wireMessages(t, router.Request{Messages: []router.Message{{Role: "user", Content: "u1"}}})
	if len(msgs) != 1 || roleContent(msgs[0]) != [2]string{"user", "u1"} {
		t.Errorf("messages = %v", msgs)
	}
}

func TestAnInvalidHistoryIsAClientFailureAndNothingIsSent(t *testing.T) {
	u, a := router.Message{Role: "user", Content: "PROMPT-TEXT"}, router.Message{Role: "assistant", Content: "REPLY-TEXT"}
	for name, msgs := range map[string][]router.Message{
		"starts with assistant": {a, u},
		"two users":             {u, u},
		"no turns":              {{Role: "system", Content: "PROMPT-TEXT"}},
	} {
		f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, done(1, 1)) })
		_, err := complete(t, New(), f.URL, modelcfg.Options{}, router.Request{Messages: msgs})
		if k := failureOf(t, err).Kind; k != router.FailClient {
			t.Errorf("%s: kind = %s", name, k)
		}
		if strings.Contains(err.Error(), "PROMPT-TEXT") || strings.Contains(err.Error(), "REPLY-TEXT") {
			t.Errorf("%s: the error carries message text: %v", name, err)
		}
		f.mu.Lock()
		n := len(f.requests)
		f.mu.Unlock()
		if n != 0 {
			t.Errorf("%s: an invalid history must not be sent", name)
		}
	}
}
