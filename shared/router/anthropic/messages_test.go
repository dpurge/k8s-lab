package anthropic

import (
	"strings"
	"testing"

	"k8s-lab/shared/router"
)

func TestAThreeTurnHistoryIsSentAsMessagesWithSystemOnTop(t *testing.T) {
	f := newFake(t, 200, okBody)
	_, err := complete(t, f, model, router.Request{System: "SYS", Input: "ignored", Messages: []router.Message{
		{Role: "system", Content: "S2"}, {Role: "user", Content: "u1"}, {Role: "assistant", Content: "a1"}, {Role: "user", Content: "u2"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	fields := f.reqs[0].Fields
	if fields["system"] != "SYS\n\nS2" {
		t.Errorf("system = %v", fields["system"])
	}
	want := [][2]string{{"user", "u1"}, {"assistant", "a1"}, {"user", "u2"}}
	msgs := fields["messages"].([]any)
	if len(msgs) != len(want) {
		t.Fatalf("messages = %v", msgs)
	}
	for i, w := range want {
		m := msgs[i].(map[string]any)
		if m["role"] != w[0] || m["content"] != w[1] {
			t.Errorf("message %d = %v, want %v", i, m, w)
		}
	}
}

func TestAnInputOnlyRequestHasTheExactBodyItAlwaysHad(t *testing.T) {
	f := newFake(t, 200, okBody)
	if _, err := complete(t, f, model, router.Request{System: "SYS", Input: "in"}); err != nil {
		t.Fatal(err)
	}
	want := `{"model":"claude-some-model","max_tokens":16000,"system":"SYS","messages":[{"role":"user","content":"in"}]}`
	if got := strings.TrimSpace(f.reqs[0].RawBody); got != want {
		t.Errorf("body = %s", got)
	}
	f = newFake(t, 200, okBody)
	if _, err := complete(t, f, model, router.Request{Input: "in"}); err != nil {
		t.Fatal(err)
	}
	if _, has := f.reqs[0].Fields["system"]; has {
		t.Errorf("an empty system prompt is omitted: %v", f.reqs[0].Fields)
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
		_, err := complete(t, f, model, router.Request{Messages: msgs})
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
