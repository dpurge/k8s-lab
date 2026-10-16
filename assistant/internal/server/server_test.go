package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"assistant/internal/metrics"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func newTestServer(t *testing.T, db Pinger) (http.Handler, *metrics.Metrics) {
	t.Helper()
	m := metrics.New()
	return NewRouter(Deps{DB: db, Metrics: m, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}), m
}

func do(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHealthIsOKWhenTheDatabaseAnswers(t *testing.T) {
	h, _ := newTestServer(t, fakePinger{})
	rec := do(h, "GET", "/healthz")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Errorf("status %d body %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestHealthIs503WhenTheDatabaseDoesNotAnswer(t *testing.T) {
	h, _ := newTestServer(t, fakePinger{err: errors.New("connection refused")})
	rec := do(h, "GET", "/healthz")
	if rec.Code != 503 || strings.TrimSpace(rec.Body.String()) != `{"status":"unavailable"}` {
		t.Errorf("status %d body %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("the error detail must stay in the log, not the response")
	}
}

func TestHealthTimesOutAHangingDatabase(t *testing.T) {
	h, _ := newTestServer(t, pingFunc(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	rec := do(h, "GET", "/healthz")
	if rec.Code != 503 {
		t.Errorf("status = %d", rec.Code)
	}
}

type pingFunc func(context.Context) error

func (f pingFunc) Ping(ctx context.Context) error { return f(ctx) }

func TestMetricsEndpointCountsRequestsByRouteTemplate(t *testing.T) {
	h, _ := newTestServer(t, fakePinger{})
	do(h, "GET", "/healthz")
	do(h, "GET", "/healthz")
	body := do(h, "GET", "/metrics").Body.String()
	if !strings.Contains(body, `assistant_http_requests_total{method="GET",route="/healthz",status="200"} 2`) {
		t.Errorf("health requests not counted:\n%s", body)
	}
}

func TestMetricsRecordTheStatusCode(t *testing.T) {
	h, _ := newTestServer(t, fakePinger{err: errors.New("down")})
	do(h, "GET", "/healthz")
	body := do(h, "GET", "/metrics").Body.String()
	if !strings.Contains(body, `route="/healthz",status="503"} 1`) {
		t.Errorf("503 not counted:\n%s", body)
	}
}

func TestUnknownPathsShareOneMetricLabel(t *testing.T) {
	h, _ := newTestServer(t, fakePinger{})
	for _, p := range []string{"/nope", "/wp-admin", "/.env", "/a/b/c"} {
		if rec := do(h, "GET", p); rec.Code != 404 {
			t.Errorf("%s: status %d", p, rec.Code)
		}
	}
	body := do(h, "GET", "/metrics").Body.String()
	if !strings.Contains(body, `route="unmatched",status="404"} 4`) {
		t.Errorf("unmatched paths must share one label:\n%s", body)
	}
	for _, p := range []string{"wp-admin", ".env"} {
		if strings.Contains(body, p) {
			t.Errorf("a scanned path %q became a label value", p)
		}
	}
}

func TestAPanicIsAnswered500InTheErrorShapeLoggedAndCounted(t *testing.T) {
	m := metrics.New()
	logs := &bytes.Buffer{}
	h := NewRouter(Deps{DB: pingFunc(func(context.Context) error { panic("boom") }), Metrics: m, Log: slog.New(slog.NewJSONHandler(logs, nil))})
	rec := do(h, "GET", "/healthz")
	if rec.Code != 500 || strings.TrimSpace(rec.Body.String()) != `{"error":{"code":"internal","message":"internal error"}}` {
		t.Errorf("status %d body %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(logs.String(), `"panic":"boom"`) || !strings.Contains(logs.String(), "stack") {
		t.Errorf("the panic must be logged with its value and stack: %s", logs)
	}
	if !strings.Contains(do(h, "GET", "/metrics").Body.String(), `route="/healthz",status="500"} 1`) {
		t.Errorf("the 500 must be counted")
	}
}

func TestWrongMethodIsNotAHealthCheck(t *testing.T) {
	h, _ := newTestServer(t, fakePinger{})
	if rec := do(h, "POST", "/healthz"); rec.Code != 405 {
		t.Errorf("status = %d", rec.Code)
	}
}
