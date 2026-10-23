// Package bus is the assistant's typed client for calling other apps over
// NATS. It wraps k8s-lab/shared/bus, which carries the message envelope, with
// one method per operation of k8s-lab/shared/ops: each returns the typed result
// or a *message.Error with one of the fixed codes (use Retryable to decide
// whether to try again). Nothing in a workflow calls it yet; the connection is
// made only when ASSISTANT_NATS_URL is set.
package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"assistant/internal/callctx"
	sharedbus "k8s-lab/shared/bus"
	"k8s-lab/shared/message"
	"k8s-lab/shared/ops"
)

// appName is how the assistant names itself in the envelopes it sends.
const appName = "assistant"

// The apps that answer the operations, by the name the typed methods use.
const (
	Knowledge   = "knowledge"
	Phraseforge = "phraseforge"
)

// Retryable reports whether a failed request may be tried again: err is a
// *message.Error with the code unavailable or timeout.
func Retryable(err error) bool { return sharedbus.Retryable(err) }

// requester is the part of shared/bus.Client the typed methods use; tests give
// it a fake that returns scripted envelopes.
type requester interface {
	Request(ctx context.Context, to, operation string, payload any, opts ...message.Option) (message.Envelope, error)
}

// Observer records the outcome of each request: outcome is "ok" or the error
// code. metrics.Metrics implements it.
type Observer interface {
	ObserveBus(app, operation, outcome string, elapsed time.Duration)
}

// Option changes a Client's defaults.
type Option func(*Client)

// WithObserver makes the Client report every request to o.
func WithObserver(o Observer) Option {
	return func(c *Client) { c.observer = o }
}

// WithDestinations changes the app name each of Knowledge and Phraseforge is
// sent to; it exists for tests, which share a NATS server and so need a name of
// their own. Keys other than those two are ignored.
func WithDestinations(names map[string]string) Option {
	return func(c *Client) {
		for logical, actual := range names {
			if _, known := c.destinations[logical]; known {
				c.destinations[logical] = actual
			}
		}
	}
}

// Client calls the other apps. Close it when done.
type Client struct {
	requests     requester
	destinations map[string]string
	observer     Observer
	close        func() error
}

func newClient(r requester, opts ...Option) *Client {
	c := &Client{
		requests:     r,
		destinations: map[string]string{Knowledge: Knowledge, Phraseforge: Phraseforge},
		close:        func() error { return nil },
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Dial connects to the NATS server at url and returns a Client over the
// connection. The error never holds the URL, which may hold credentials.
func Dial(url string, log *slog.Logger, opts ...Option) (*Client, error) {
	nc, err := sharedbus.Connect(sharedbus.Config{URL: url, Name: appName, Logger: log})
	if err != nil {
		return nil, err
	}
	reg := message.NewRegistry()
	if err := ops.Register(reg); err != nil {
		_ = sharedbus.Close(nc)
		return nil, fmt.Errorf("register the operations: %w", err)
	}
	requests, err := sharedbus.NewClient(nc, appName, sharedbus.WithRegistry(reg))
	if err != nil {
		_ = sharedbus.Close(nc)
		return nil, err
	}
	c := newClient(requests, opts...)
	c.close = func() error { return sharedbus.Close(nc) }
	return c, nil
}

// Close drains the connection. It is safe on a nil Client.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	return c.close()
}

// SearchDocuments asks knowledge to search its documents.
func (c *Client) SearchDocuments(ctx context.Context, req ops.DocumentsSearchRequest) (ops.DocumentsSearchResponse, error) {
	return call(ctx, c, Knowledge, ops.DocumentsSearch, req, ops.ParseDocumentsSearchResponse)
}

// IngestURL asks app (Knowledge or Phraseforge) to ingest the page at req.URL.
// The answer only says the work was accepted; follow it with TaskStatus.
func (c *Client) IngestURL(ctx context.Context, app string, req ops.URLIngestRequest) (ops.URLIngestResponse, error) {
	if err := checkApp(app); err != nil {
		return ops.URLIngestResponse{}, err
	}
	return call(ctx, c, app, ops.URLIngest, req, ops.ParseURLIngestResponse)
}

// TaskStatus asks app (Knowledge or Phraseforge) for the state of the task id.
func (c *Client) TaskStatus(ctx context.Context, app, id string) (ops.TaskStatusResponse, error) {
	if err := checkApp(app); err != nil {
		return ops.TaskStatusResponse{}, err
	}
	return call(ctx, c, app, ops.TaskStatus, ops.TaskStatusRequest{ID: id}, ops.ParseTaskStatusResponse)
}

// checkApp refuses an app that answers none of these operations, before
// anything is sent.
func checkApp(app string) error {
	if app == Knowledge || app == Phraseforge {
		return nil
	}
	return &message.Error{Code: message.CodeInvalidMessage, Message: fmt.Sprintf("app must be %q or %q", Knowledge, Phraseforge)}
}

// call sends one request to a logical app and parses the response payload.
// The context's task and user ids go into the envelope's context.
func call[Resp any](ctx context.Context, c *Client, app, operation string, payload any, parse func(json.RawMessage) (Resp, error)) (Resp, error) {
	var opts []message.Option
	if info, ok := callctx.From(ctx); ok {
		opts = append(opts, message.WithContext(info.UserID, info.TaskID))
	}
	started := time.Now()
	env, err := c.requests.Request(ctx, c.destinations[app], operation, payload, opts...)
	if err == nil {
		var resp Resp
		if resp, err = parse(env.Payload); err == nil {
			c.observe(app, operation, "ok", started)
			return resp, nil
		}
		err = &message.Error{Code: message.CodeInvalidMessage, Message: "the response payload does not fit the operation's schema"}
	}
	c.observe(app, operation, codeOf(err), started)
	var zero Resp
	return zero, err
}

func (c *Client) observe(app, operation, outcome string, started time.Time) {
	if c.observer != nil {
		c.observer.ObserveBus(app, operation, outcome, time.Since(started))
	}
}

// codeOf returns the code of a *message.Error, else internal.
func codeOf(err error) string {
	var coded *message.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return message.CodeInternal
}
