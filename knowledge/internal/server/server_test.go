package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsAreServedWithoutALoginWhenAHandlerIsSet(t *testing.T) {
	s := New(nil, nil, nil, nil, nil, nil)
	s.SetMetricsHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("knowledge_llm_calls_total 1\n"))
	}))
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil)) // no cookie
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "knowledge_llm_calls_total") {
		t.Errorf("GET /metrics = %d %q, want 200 with the metrics and no session", rec.Code, rec.Body.String())
	}
}

func TestWithoutAMetricsHandlerThereIsNoMetricsRoute(t *testing.T) {
	s := New(nil, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code == http.StatusOK {
		t.Errorf("GET /metrics = %d without a handler, want it not to be served", rec.Code)
	}
}
