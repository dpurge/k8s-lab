package message

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderGolden(t *testing.T) {
	cases := []struct {
		name string
		env  func(t *testing.T) Envelope
	}{
		{"request_documents_search", func(t *testing.T) Envelope { return decodeTestdata(t, "request_documents_search.json") }},
		{"response_documents_search", func(t *testing.T) Envelope { return decodeTestdata(t, "response_documents_search.json") }},
		{"error_not_found", func(t *testing.T) Envelope { return decodeTestdata(t, "error_not_found.json") }},
		{"error_retryable", func(t *testing.T) Envelope {
			e := decodeTestdata(t, "error_not_found.json")
			e.Error = &ErrorBody{Code: CodeUnavailable, Message: "Knowledge is busy.", Retryable: true}
			e.Summary = "Task status"
			return e
		}},
		{"long_summary", func(t *testing.T) Envelope {
			e := decodeTestdata(t, "request_documents_search.json")
			e.Summary = strings.Repeat("long summary ", 40)
			return e
		}},
		{"no_payload", func(t *testing.T) Envelope {
			e := decodeTestdata(t, "response_documents_search.json")
			e.Payload = nil
			return e
		}},
	}
	for _, c := range cases {
		got := Render(c.env(t))
		want := strings.TrimSuffix(string(readTestdata(t, "render_"+c.name+".golden")), "\n")
		if got != want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, want)
		}
		if strings.ContainsAny(got, "\n\r") || utf8.RuneCountInString(got) > 300 {
			t.Errorf("%s: not one line of at most 300 characters (%d)", c.name, utf8.RuneCountInString(got))
		}
	}
}

func decodeTestdata(t *testing.T, name string) Envelope {
	t.Helper()
	env, err := Decode(readTestdata(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestRenderNeverPrintsPayloadValues(t *testing.T) {
	env := validRequest()
	env.Payload = json.RawMessage(`{"query":"my secret diary","tags":["private-tag"],"nested":{"inner":"hidden-inner"}}`)
	got := Render(env)
	for _, leaked := range []string{"secret", "diary", "private-tag", "nested\":", "hidden-inner", "inner"} {
		if strings.Contains(got, leaked) {
			t.Errorf("%q leaked into %q", leaked, got)
		}
	}
	if !strings.HasSuffix(got, " payload keys: nested, query, tags") {
		t.Errorf("got %q", got)
	}
}

func TestRenderEdgeCases(t *testing.T) {
	env := validRequest()
	env.Summary = "line one\nline two\ttabbed"
	if got := Render(env); strings.ContainsAny(got, "\n\t") || !strings.Contains(got, `summary: "line one\nline two\ttabbed"`) {
		t.Errorf("control characters: %q", got)
	}
	env = validError()
	env.Error.Message = "first\nsecond"
	if got := Render(env); strings.ContainsAny(got, "\n") || !strings.Contains(got, "error not_found: first second") {
		t.Errorf("newline in an error message: %q", got)
	}

	for name, payload := range map[string]string{"array": `[1,2]`, "null": `null`, "empty object": `{}`, "not json": `{x`} {
		env := validResponse()
		env.Payload = json.RawMessage(payload)
		if got := Render(env); strings.Contains(got, "payload keys") {
			t.Errorf("%s: %q", name, got)
		}
	}

	// A short or empty correlation_id does not panic; the 300 limit holds with multibyte text.
	env = Envelope{Type: TypeEvent, Operation: "x", From: "a", To: "b", CorrelationID: "ab"}
	if got := Render(env); got != "event x a->b [ab]" {
		t.Errorf("short id: %q", got)
	}
	env.CorrelationID = ""
	if got := Render(env); got != "event x a->b []" {
		t.Errorf("empty id: %q", got)
	}
	env.Summary = strings.Repeat("é", 1000)
	got := Render(env)
	if utf8.RuneCountInString(got) != 300 || !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
		t.Errorf("multibyte truncation: %d %q", utf8.RuneCountInString(got), got)
	}
}
