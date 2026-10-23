// Package responder answers the requests that other apps send to knowledge over
// the message bus (shared/bus): documents_search, url_ingest and task_status. A
// handler calls knowledge's own services directly, the same ones the HTTP
// handlers call, never knowledge over HTTP.
//
// The bus is off unless nats.url is configured (see Run). Nothing here logs a
// payload value: a handler logs the operation, the message id and the outcome.
package responder

import (
	"context"
	"log/slog"

	"github.com/nats-io/nats.go"

	"k8s-lab/shared/bus"
	"k8s-lab/shared/message"
	"k8s-lab/shared/ops"
	"knowledge/internal/jobs"
	"knowledge/internal/qdrant"
)

// App is knowledge's name on the bus: it listens on dp.knowledge.request.
const App = "knowledge"

// Searcher is what documents_search needs of the knowledge base; *qdrant.Client
// satisfies it, and it is the call the HTTP search handler makes.
type Searcher interface {
	Search(ctx context.Context, s qdrant.Search) ([]qdrant.ListItem, error)
}

// Ingester starts an ingest of a URL; *ingest.Service satisfies it, and it is the
// call the HTTP ingest/url handler makes (with the same URL validation).
type Ingester interface {
	StartURL(ctx context.Context, rawURL string, tags []string) (jobs.Job, error)
}

// JobReader looks a job up by id; *jobs.Service satisfies it.
type JobReader interface {
	Get(ctx context.Context, id string) (jobs.Job, error)
}

// Services are the knowledge services the handlers call.
type Services struct {
	Search Searcher
	Ingest Ingester
	Jobs   JobReader
}

// Observer counts one handled request: the operation and its outcome, "ok" or
// the error code the requester got.
type Observer func(operation, outcome string)

// operations are the ones knowledge answers; nothing else is registered.
var operations = []string{ops.DocumentsSearch, ops.URLIngest, ops.TaskStatus}

// NewResponder returns the Responder of app on nc with the three handlers
// registered; the caller starts it. observe may be nil and log may be nil
// (slog.Default()).
func NewResponder(nc *nats.Conn, app string, svc Services, observe Observer, log *slog.Logger) (*bus.Responder, error) {
	reg, err := newRegistry()
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	resp, err := bus.NewResponder(nc, app, reg, log)
	if err != nil {
		return nil, err
	}
	h := handlers{svc: svc, observe: observe, log: log}
	for operation, fn := range map[string]bus.Handler{
		ops.DocumentsSearch: h.documentsSearch,
		ops.URLIngest:       h.urlIngest,
		ops.TaskStatus:      h.taskStatus,
	} {
		if err := resp.Handle(operation, h.instrument(operation, fn)); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

// newRegistry registers the operations knowledge answers.
func newRegistry() (*message.Registry, error) {
	reg := message.NewRegistry()
	if err := ops.Register(reg, operations...); err != nil {
		return nil, err
	}
	return reg, nil
}

// connect is a variable so a test can prove that no connection is made when the
// bus is off.
var connect = bus.Connect

// Run starts the responder when url is configured (bus.Enabled) and returns the
// function that stops it: the responder drains first, then the connection. With
// an empty url it connects to nothing and the stop function does nothing. A
// connection or start failure never stops knowledge: it logs a warning and
// returns a no-op stop, and knowledge keeps serving HTTP without the bus. Once
// connected, shared/bus keeps reconnecting without limit, so a NATS that goes down
// and comes back needs no restart of knowledge; one that was down at start is
// not retried until knowledge restarts.
func Run(url, app string, svc Services, observe Observer, log *slog.Logger) (stop func()) {
	if log == nil {
		log = slog.Default()
	}
	none := func() {}
	if !bus.Enabled(url) {
		return none
	}
	nc, err := connect(bus.Config{URL: url, Name: app, Logger: log})
	if err != nil {
		log.Warn("message bus not started: knowledge serves HTTP without it", "error", err.Error())
		return none
	}
	resp, err := NewResponder(nc, app, svc, observe, log)
	if err == nil {
		err = resp.Start()
	}
	if err != nil {
		log.Warn("message bus responder not started: knowledge serves HTTP without it", "error", err.Error())
		_ = bus.Close(nc)
		return none
	}
	log.Info("message bus responder started", "app", app, "subject", bus.RequestSubject(app))
	return func() {
		resp.Stop()
		if err := bus.Close(nc); err != nil {
			log.Warn("message bus connection did not drain", "error", err.Error())
		}
	}
}
