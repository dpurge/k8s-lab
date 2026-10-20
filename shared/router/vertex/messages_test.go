package vertex

import (
	"context"
	"strings"
	"testing"

	"k8s-lab/shared/router"
)

func TestAThreeTurnHistoryMapsAssistantToModelAndSystemToTheInstruction(t *testing.T) {
	f := newFake(t, 200, okBody)
	_, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, router.Request{System: "SYS", Input: "ignored", Messages: []router.Message{
		{Role: "system", Content: "S2"}, {Role: "user", Content: "u1"}, {Role: "assistant", Content: "a1"}, {Role: "user", Content: "u2"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	fields := f.reqs[0].Fields
	if text := fields["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"]; text != "SYS\n\nS2" {
		t.Errorf("systemInstruction = %v", text)
	}
	want := [][2]string{{"user", "u1"}, {"model", "a1"}, {"user", "u2"}}
	contents := fields["contents"].([]any)
	if len(contents) != len(want) {
		t.Fatalf("contents = %v", contents)
	}
	for i, w := range want {
		c := contents[i].(map[string]any)
		if c["role"] != w[0] || c["parts"].([]any)[0].(map[string]any)["text"] != w[1] {
			t.Errorf("content %d = %v, want %v", i, c, w)
		}
	}
}

func TestAnInputOnlyRequestIsOneUserContent(t *testing.T) {
	f := newFake(t, 200, okBody)
	if _, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, router.Request{Input: "in"}); err != nil {
		t.Fatal(err)
	}
	contents := f.reqs[0].Fields["contents"].([]any)
	if len(contents) != 1 || contents[0].(map[string]any)["role"] != "user" {
		t.Errorf("contents = %v", contents)
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
		_, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, router.Request{Messages: msgs})
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
