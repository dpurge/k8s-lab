package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestNewZeroTimeoutPreservesDefault covers every existing caller's
// unchanged behavior: a zero Config.Timeout must still produce the
// original 2-minute HTTP client timeout.
func TestNewZeroTimeoutPreservesDefault(t *testing.T) {
	c := New(Config{})
	if c.http.Timeout != defaultTimeout {
		t.Errorf("http.Timeout = %v, want %v", c.http.Timeout, defaultTimeout)
	}
}

// TestNewExplicitTimeoutOverrides covers Config.Timeout actually reaching
// the underlying http.Client.
func TestNewExplicitTimeoutOverrides(t *testing.T) {
	c := New(Config{Timeout: 5 * time.Second})
	if c.http.Timeout != 5*time.Second {
		t.Errorf("http.Timeout = %v, want 5s", c.http.Timeout)
	}
}

// TestExplicitTimeoutActuallyFires proves Config.Timeout isn't just stored
// but genuinely bounds a real request — a stub Ollama server sleeps longer
// than the configured timeout, and Complete must return before that sleep
// elapses.
func TestExplicitTimeoutActuallyFires(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte(`{"message":{"content":"too late"}}`)) //nolint:errcheck
	}))
	defer server.Close()

	c := New(Config{Provider: "ollama", BaseURL: server.URL, Timeout: 20 * time.Millisecond})
	start := time.Now()
	_, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Complete() with a request slower than Timeout = nil error, want a timeout error")
	}
	if elapsed >= 200*time.Millisecond {
		t.Errorf("Complete() took %v, want it to fail fast around the 20ms timeout, not wait for the 200ms server sleep", elapsed)
	}
}

