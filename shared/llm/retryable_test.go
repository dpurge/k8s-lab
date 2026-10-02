package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func statusServer(t *testing.T, code int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

// TestIsRetryableFromRealCalls classifies the error each failure mode
// actually produces, so the table can't drift from what Complete returns.
func TestIsRetryableFromRealCalls(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	idle := streamServer(t, []streamStep{{0, contentLine("a")}, {time.Second, contentLine("b")}})
	firstToken := streamServer(t, []streamStep{{time.Second, contentLine("a")}})
	overall := streamServer(t, []streamStep{{0, contentLine("a")}, {time.Second, contentLine("b")}})

	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"500", Config{BaseURL: statusServer(t, 500, "boom").URL}, true},
		{"503", Config{BaseURL: statusServer(t, 503, "loading").URL}, true},
		{"400", Config{BaseURL: statusServer(t, 400, "bad").URL}, false},
		{"404", Config{BaseURL: statusServer(t, 404, "no model").URL}, false},
		{"stream error chunk", Config{BaseURL: streamServer(t, []streamStep{{0, contentLine("a")}, {0, `{"error":"out of memory"}`}}).URL}, true},
		{"stream ended early", Config{BaseURL: streamServer(t, []streamStep{{0, contentLine("a")}}).URL}, true},
		{"idle stall", Config{BaseURL: idle.URL, IdleTimeout: 30 * time.Millisecond}, true},
		{"first token timeout", Config{BaseURL: firstToken.URL, FirstTokenTimeout: 30 * time.Millisecond}, false},
		{"overall timeout", Config{BaseURL: overall.URL, Timeout: 30 * time.Millisecond}, false},
		{"connection refused", Config{BaseURL: closedURL}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := complete(t, c.cfg)
			if err == nil {
				t.Fatal("err = nil, want a failure")
			}
			if got := IsRetryable(err); got != c.want {
				t.Errorf("IsRetryable(%v) = %v, want %v", err, got, c.want)
			}
		})
	}
}

func TestIsRetryableCallerCancelIsNot(t *testing.T) {
	server := streamServer(t, []streamStep{{time.Second, contentLine("late")}})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	_, err := New(Config{Provider: "ollama", BaseURL: server.URL}).Complete(ctx, []Message{{Role: "user", Content: "hi"}})
	if err == nil || IsRetryable(err) {
		t.Errorf("IsRetryable(%v) = true, want false for the caller's own cancellation", err)
	}
}

func TestIsRetryableOtherErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("chat provider returned no choices"), false},
		{"context canceled", context.Canceled, false},
		{"context deadline", context.DeadlineExceeded, false},
		{"unexpected EOF mid-body", io.ErrUnexpectedEOF, true},
		{"wrapped 502", fmt.Errorf("outer: %w", &StatusError{StatusCode: 502, Status: "502 Bad Gateway"}), true},
	}
	for _, c := range cases {
		if got := IsRetryable(c.err); got != c.want {
			t.Errorf("%s: IsRetryable = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestErrorTextUnchanged pins the exact messages callers (knowledge
// included) have always received.
func TestErrorTextUnchanged(t *testing.T) {
	_, err := complete(t, Config{BaseURL: statusServer(t, 500, "boom").URL})
	if want := "chat provider returned 500 Internal Server Error: boom"; err == nil || err.Error() != want {
		t.Errorf("status error = %v, want %q", err, want)
	}
	_, err = complete(t, Config{BaseURL: streamServer(t, []streamStep{{0, contentLine("a")}}).URL})
	if want := "chat provider stream ended before completion"; err == nil || err.Error() != want {
		t.Errorf("stream ended error = %v, want %q", err, want)
	}
	_, err = complete(t, Config{BaseURL: streamServer(t, []streamStep{{0, `{"error":"out of memory"}`}}).URL})
	if want := "chat provider stream error: out of memory"; err == nil || err.Error() != want {
		t.Errorf("stream error = %v, want %q", err, want)
	}
}
