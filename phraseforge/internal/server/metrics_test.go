package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"phraseforge/internal/auth"
	"phraseforge/internal/metrics"
)

func TestMetricsAreServedWithoutALoginAndLoginStillGuardsTheApp(t *testing.T) {
	m := metrics.New()
	m.ObserveLLM("gpu-box", "big", "ok", 10, 5, 0.01, time.Second)
	m.RecordError()
	m.SetModelAvailability(func() []metrics.ModelAvailability {
		return []metrics.ModelAvailability{{Model: "big", Available: true}}
	})
	s := &Server{auth: auth.New(nil, "test-session-key")}
	s.SetMetricsHandler(m.Handler())
	h := s.Router()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics without a session = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content type = %q, want the text exposition format", ct)
	}
	body, _ := io.ReadAll(rec.Body)
	for _, name := range []string{
		"phraseforge_llm_calls_total", "phraseforge_llm_tokens_total", "phraseforge_llm_cost_usd_total",
		"phraseforge_llm_call_duration_seconds", "phraseforge_llm_model_available", "phraseforge_llm_record_errors_total",
	} {
		if !strings.Contains(string(body), name) {
			t.Errorf("%s missing from /metrics", name)
		}
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Errorf("GET /api/v1/profile without a session = %d to %q, want a redirect to /login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestWithoutAMetricsHandlerThereIsNoMetricsRoute(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Server{auth: auth.New(nil, "k")}).Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /metrics with no handler = %d, want 404", rec.Code)
	}
}
