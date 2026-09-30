package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestNewZeroTimeoutsApplyDefaults covers every caller that sets no
// timeouts (knowledge today): each limit falls back to its default.
func TestNewZeroTimeoutsApplyDefaults(t *testing.T) {
	c := New(Config{})
	if c.http.Timeout != defaultTimeout {
		t.Errorf("http.Timeout = %v, want %v", c.http.Timeout, defaultTimeout)
	}
	if c.firstTokenTimeout != defaultFirstTokenTimeout {
		t.Errorf("firstTokenTimeout = %v, want %v", c.firstTokenTimeout, defaultFirstTokenTimeout)
	}
	if c.idleTimeout != defaultIdleTimeout {
		t.Errorf("idleTimeout = %v, want %v", c.idleTimeout, defaultIdleTimeout)
	}
}

// TestNewExplicitTimeoutOverrides covers Config.Timeout actually reaching
// the underlying http.Client used by the non-streaming openAI path.
func TestNewExplicitTimeoutOverrides(t *testing.T) {
	c := New(Config{Timeout: 5 * time.Second})
	if c.http.Timeout != 5*time.Second {
		t.Errorf("http.Timeout = %v, want 5s", c.http.Timeout)
	}
}

// streamServer serves a scripted NDJSON /api/chat stream: each step waits
// its delay, then writes and flushes its line, stopping early if the client
// disconnects.
type streamStep struct {
	delay time.Duration
	line  string
}

func streamServer(t *testing.T, steps []streamStep) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		for _, s := range steps {
			select {
			case <-time.After(s.delay):
			case <-r.Context().Done():
				return
			}
			fmt.Fprintln(w, s.line)
			flusher.Flush()
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func contentLine(s string) string {
	return fmt.Sprintf(`{"message":{"role":"assistant","content":%q},"done":false}`, s)
}

const doneLine = `{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`

func complete(t *testing.T, cfg Config) (string, error) {
	t.Helper()
	cfg.Provider = "ollama"
	cfg.Model = "test-model"
	return New(cfg).Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
}

func wantLimit(t *testing.T, err error, limit string) {
	t.Helper()
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v (%T), want *TimeoutError with Limit %q", err, err, limit)
	}
	if te.Limit != limit {
		t.Fatalf("Limit = %q, want %q (err: %v)", te.Limit, limit, err)
	}
}

func TestStreamConcatenatesContent(t *testing.T) {
	server := streamServer(t, []streamStep{{0, contentLine("Hi")}, {0, contentLine("!")}, {0, contentLine(" there")}, {0, doneLine}})
	got, err := complete(t, Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if got != "Hi! there" {
		t.Errorf("content = %q, want %q", got, "Hi! there")
	}
}

func TestStreamRequestsStreaming(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		fmt.Fprintln(w, doneLine)
	}))
	defer server.Close()
	if _, err := complete(t, Config{BaseURL: server.URL}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"stream":true`) {
		t.Errorf("request body = %s, want it to contain \"stream\":true", body)
	}
}

func TestStreamAccumulatesToolCalls(t *testing.T) {
	call := `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"search","arguments":{"q":"x"}}}]},"done":false}`
	server := streamServer(t, []streamStep{{0, call}, {0, doneLine}})
	resp, err := New(Config{Provider: "ollama", BaseURL: server.URL}).Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Name != "search" || resp.ToolCalls[0].Function.Arguments["q"] != "x" {
		t.Errorf("ToolCalls = %+v, want one search(q=x) call", resp.ToolCalls)
	}
}

// TestStreamSlowButSteadySucceeds is the prod case: total time well past
// both FirstTokenTimeout and IdleTimeout, but no single gap exceeds them.
func TestStreamSlowButSteadySucceeds(t *testing.T) {
	var steps []streamStep
	for i := 0; i < 8; i++ {
		steps = append(steps, streamStep{25 * time.Millisecond, contentLine("x")})
	}
	steps = append(steps, streamStep{25 * time.Millisecond, doneLine})
	server := streamServer(t, steps)
	got, err := complete(t, Config{BaseURL: server.URL, FirstTokenTimeout: 60 * time.Millisecond, IdleTimeout: 60 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if got != "xxxxxxxx" {
		t.Errorf("content = %q, want 8 x's", got)
	}
}

func TestStreamThinkingCountsAsProgress(t *testing.T) {
	thinking := `{"message":{"role":"assistant","content":"","thinking":"hmm"},"done":false}`
	var steps []streamStep
	for i := 0; i < 5; i++ {
		steps = append(steps, streamStep{25 * time.Millisecond, thinking})
	}
	steps = append(steps, streamStep{25 * time.Millisecond, contentLine("ok")}, streamStep{0, doneLine})
	server := streamServer(t, steps)
	got, err := complete(t, Config{BaseURL: server.URL, FirstTokenTimeout: 60 * time.Millisecond, IdleTimeout: 60 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" {
		t.Errorf("content = %q, want %q (thinking must not leak into content)", got, "ok")
	}
}

func TestStreamFirstTokenTimeoutFires(t *testing.T) {
	server := streamServer(t, []streamStep{{time.Second, contentLine("late")}, {0, doneLine}})
	start := time.Now()
	_, err := complete(t, Config{BaseURL: server.URL, FirstTokenTimeout: 30 * time.Millisecond, IdleTimeout: time.Second})
	wantLimit(t, err, LimitFirstToken)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("took %v, want it to fail near the 30ms first-token limit", elapsed)
	}
	if !strings.Contains(err.Error(), "test-model") || !strings.Contains(err.Error(), "model load") {
		t.Errorf("err = %q, want it to name the model and mention model load", err)
	}
}

func TestStreamIdleTimeoutFires(t *testing.T) {
	server := streamServer(t, []streamStep{{0, contentLine("a")}, {10 * time.Millisecond, contentLine("b")}, {time.Second, contentLine("late")}, {0, doneLine}})
	start := time.Now()
	_, err := complete(t, Config{BaseURL: server.URL, FirstTokenTimeout: time.Second, IdleTimeout: 50 * time.Millisecond})
	wantLimit(t, err, LimitIdle)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("took %v, want it to fail near the 50ms idle limit", elapsed)
	}
	if !strings.Contains(err.Error(), "stalled") {
		t.Errorf("err = %q, want it to say stalled", err)
	}
}

func TestStreamOverallTimeoutFires(t *testing.T) {
	var steps []streamStep
	for i := 0; i < 100; i++ {
		steps = append(steps, streamStep{10 * time.Millisecond, contentLine("x")})
	}
	server := streamServer(t, steps)
	start := time.Now()
	_, err := complete(t, Config{BaseURL: server.URL, Timeout: 80 * time.Millisecond, FirstTokenTimeout: time.Second, IdleTimeout: time.Second})
	wantLimit(t, err, LimitOverall)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("took %v, want it to fail near the 80ms overall limit", elapsed)
	}
}

// TestExplicitTimeoutActuallyFires keeps the original guarantee: a server
// that sends nothing at all is bounded by Config.Timeout when that is the
// tightest limit.
func TestExplicitTimeoutActuallyFires(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	start := time.Now()
	_, err := complete(t, Config{BaseURL: server.URL, Timeout: 20 * time.Millisecond})
	wantLimit(t, err, LimitOverall)
	if elapsed := time.Since(start); elapsed >= 200*time.Millisecond {
		t.Errorf("took %v, want it to fail fast around the 20ms timeout", elapsed)
	}
}

func TestStreamNon2xxKeepsErrorText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"model 'test-model' not found"}`)
	}))
	defer server.Close()
	_, err := complete(t, Config{BaseURL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "chat provider returned 404") || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want chat provider returned 404 ... not found", err)
	}
}

func TestStreamMidStreamError(t *testing.T) {
	server := streamServer(t, []streamStep{{0, contentLine("a")}, {0, `{"error":"out of memory"}`}})
	_, err := complete(t, Config{BaseURL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "out of memory") {
		t.Errorf("err = %v, want it to carry the stream's error message", err)
	}
}

func TestStreamEndedWithoutDone(t *testing.T) {
	server := streamServer(t, []streamStep{{0, contentLine("a")}})
	_, err := complete(t, Config{BaseURL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "ended before completion") {
		t.Errorf("err = %v, want a stream-ended-early error", err)
	}
}

// TestStreamCallerCancelIsNotATimeout: cancelling the caller's ctx (e.g. a
// cancelled job) still aborts the call, and is not misreported as a limit.
func TestStreamCallerCancelIsNotATimeout(t *testing.T) {
	server := streamServer(t, []streamStep{{time.Second, contentLine("late")}})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	start := time.Now()
	_, err := New(Config{Provider: "ollama", BaseURL: server.URL}).Complete(ctx, []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("err = nil, want a cancellation error")
	}
	var te *TimeoutError
	if errors.As(err, &te) {
		t.Errorf("err = %v, want a plain cancellation, not a *TimeoutError", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("took %v, want it to abort near the 20ms cancel", elapsed)
	}
}

// TestStreamSendsFormatOnlyWhenSet: a Format schema reaches Ollama as the
// "format" field verbatim, and an unset Format leaves the field out, so
// every existing caller's request is unchanged.
func TestStreamSendsFormatOnlyWhenSet(t *testing.T) {
	var body map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		fmt.Fprintln(w, doneLine)
	}))
	defer server.Close()

	schema := json.RawMessage(`{"type":"object","required":["translation"]}`)
	if _, err := complete(t, Config{BaseURL: server.URL, Format: schema}); err != nil {
		t.Fatal(err)
	}
	if string(body["format"]) != string(schema) {
		t.Errorf("format = %s, want %s", body["format"], schema)
	}

	if _, err := complete(t, Config{BaseURL: server.URL}); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["format"]; ok {
		t.Errorf("format = %s, want the field omitted when Format is empty", body["format"])
	}
}
