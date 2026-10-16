// Package server is the assistant's HTTP surface: the JSON API, the admin
// interface, the health check and the metrics endpoint.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5"

	"assistant/internal/metrics"
)

// Pinger checks that the database answers. *pgxpool.Pool implements it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps is what the handlers need.
type Deps struct {
	DB     Pinger
	Users  UserStore
	Tasks  TaskStore
	Calls  CallStore
	Steps  StepStore
	Worker Stopper
	// Workflows lists what a task can name; nil means an empty list.
	Workflows WorkflowCatalog
	Chat      ChatService
	Models    ModelStatuses
	Metrics   *metrics.Metrics
	Log       *slog.Logger
	// Now is the clock for derived times; nil means time.Now.
	Now func() time.Time
}

const healthTimeout = 2 * time.Second

// NewRouter builds the router with its middleware.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(observe(d.Metrics))
	r.Use(recoverPanics(d.Log))

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, codeNotFound, "no such route")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, codeInvalidRequest, "method not allowed")
	})

	r.Get("/healthz", health(d))
	r.Get("/metrics", d.Metrics.Handler().ServeHTTP)
	d.mountAPI(r)
	d.mountAdmin(r)
	return r
}

// recoverPanics turns a panic in a handler into a logged error and a 500 in the
// API's error shape, instead of a dropped connection. It sits inside observe, so
// the 500 is counted.
func recoverPanics(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					log.Error("handler panicked", "method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
					writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder remembers the status code a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// observe records every request's count and latency by route template. A path
// no route matched is reported as "unmatched" so that a scanner cannot create
// unbounded label values.
func observe(m *metrics.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "unmatched"
			}
			m.ObserveHTTP(r.Method, route, rec.status, time.Since(start))
		})
	}
}

func health(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
		defer cancel()
		if err := d.DB.Ping(ctx); err != nil {
			d.Log.Error("health check: database did not answer", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
