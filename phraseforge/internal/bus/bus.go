// Package bus answers the requests other apps send to phraseforge over NATS,
// in the envelope of k8s-lab/shared/message, by calling phraseforge's own
// services directly (not over HTTP).
//
// It answers task_status only. url_ingest is deliberately not answered, so the
// responder replies unknown_operation: phraseforge's text ingest
// (internal/server/ingest.go, apiIngest) needs a language and a script, that
// the ops.URLIngestRequest (url, tags) has no field for, and a user, because
// texts.user_id is NOT NULL and the HTTP path authorizes with the user's role
// for the language (roles.CanEdit). Nothing here invents either.
//
// A task_status answer carries the job's id, kind, status, step, progress,
// error and times; never its payload or result (they hold user text) and never
// the user it belongs to.
package bus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	sharedbus "k8s-lab/shared/bus"
	"k8s-lab/shared/message"
	"k8s-lab/shared/ops"

	"phraseforge/internal/jobs"
)

// App is the app name phraseforge listens under: the subject is dp.phraseforge.request.
const App = "phraseforge"

// outcomeOK is the outcome label of a request that was answered with a response.
const outcomeOK = "ok"

// JobStore is what task_status needs of the job queue; *jobs.Service has it.
type JobStore interface {
	Get(ctx context.Context, id string) (jobs.Job, error)
}

// Observer counts the requests a handler answered; *metrics.Metrics has it.
// Operation and outcome are from fixed sets: the operations answered here, and
// ok or an envelope error code.
type Observer interface {
	ObserveBusRequest(operation, outcome string)
}

// jobIDShape is the shape of a job id (a UUID). The jobs table has a uuid
// column, so any other text would make the database fail instead of finding nothing.
var jobIDShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// answered lists the operations phraseforge answers.
var answered = []string{ops.TaskStatus}

// handlers answers the operations of answered.
type handlers struct {
	store    JobStore
	observer Observer
	log      *slog.Logger
}

// newRegistry returns a registry holding only the operations phraseforge answers.
func newRegistry() (*message.Registry, error) {
	reg := message.NewRegistry()
	if err := ops.Register(reg, answered...); err != nil {
		return nil, err
	}
	return reg, nil
}

// taskStatus answers task_status: a job looked up by id, as metadata only.
func (h *handlers) taskStatus(ctx context.Context, req message.Envelope) (any, error) {
	request, err := ops.ParseTaskStatusRequest(req.Payload)
	if err != nil {
		h.finish(ops.TaskStatus, "", errorCode(err))
		return nil, err
	}
	if !jobIDShape.MatchString(request.ID) {
		return nil, h.fail(request.ID, message.CodeNotFound, "no task has this id")
	}
	job, err := h.store.Get(ctx, request.ID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, h.fail(request.ID, message.CodeNotFound, "no task has this id")
	case err != nil:
		// Not a *message.Error: the responder answers internal with a fixed
		// text and logs this error itself, so the database text never leaves the app.
		h.finish(ops.TaskStatus, request.ID, message.CodeInternal)
		return nil, fmt.Errorf("look up task: %w", err)
	}
	h.finish(ops.TaskStatus, request.ID, outcomeOK)
	return statusOf(job), nil
}

// statusOf maps a job to the task_status response. It copies the named fields
// only, so the job's payload, result and user cannot reach the answer.
func statusOf(job jobs.Job) ops.TaskStatusResponse {
	return ops.TaskStatusResponse{
		ID:        job.ID,
		Kind:      job.Kind,
		Status:    string(job.Status),
		Step:      job.Step,
		Progress:  job.Progress,
		Error:     truncateRunes(job.Error, ops.MaxTaskErrorChars),
		CreatedAt: job.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: job.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// fail logs and counts a refused request and returns its error.
func (h *handlers) fail(id, code, text string) error {
	h.finish(ops.TaskStatus, id, code)
	return &message.Error{Code: code, Message: text}
}

// finish logs the operation, the id and the outcome, nothing else, and counts it.
func (h *handlers) finish(operation, id, outcome string) {
	h.log.Info("bus request", "operation", operation, "id", id, "outcome", outcome)
	h.observe(operation, outcome)
}

func (h *handlers) observe(operation, outcome string) {
	if h.observer != nil {
		h.observer.ObserveBusRequest(operation, outcome)
	}
}

// errorCode is the code of a *message.Error, internal for any other error.
func errorCode(err error) string {
	var merr *message.Error
	if errors.As(err, &merr) {
		return merr.Code
	}
	return message.CodeInternal
}

// truncateRunes cuts s to at most n characters, on a character boundary.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.ToValidUTF8(string([]rune(s)[:n]), "")
}

// Service is a running responder of phraseforge. A nil *Service is a stopped
// one: it is what Start returns when the bus is off.
type Service struct {
	responder *sharedbus.Responder
	closeConn func()
}

// Start connects to NATS at url and starts answering the requests sent to app
// (App in main.go). An empty url means the bus is off: it returns a nil Service
// and no error, and never connects. It returns an error, and leaves nothing
// running, when the connection or the subscription fails; the caller decides
// whether that matters. A nil observer and a nil logger are allowed.
func Start(url, app string, store JobStore, observer Observer, logger *slog.Logger) (*Service, error) {
	if !sharedbus.Enabled(url) {
		return nil, nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	reg, err := newRegistry()
	if err != nil {
		return nil, err
	}
	nc, err := sharedbus.Connect(sharedbus.Config{URL: url, Name: app, Logger: logger})
	if err != nil {
		return nil, err
	}
	closeConn := func() {
		if err := sharedbus.Close(nc); err != nil {
			logger.Warn("bus: closing the connection", "error", err)
		}
	}
	responder, err := sharedbus.NewResponder(nc, app, reg, logger)
	if err != nil {
		closeConn()
		return nil, err
	}
	h := &handlers{store: store, observer: observer, log: logger}
	if err := responder.Handle(ops.TaskStatus, h.taskStatus); err != nil {
		closeConn()
		return nil, err
	}
	if err := responder.Start(); err != nil {
		closeConn()
		return nil, err
	}
	return &Service{responder: responder, closeConn: closeConn}, nil
}

// Stop stops taking requests, lets those in flight finish, then drains and
// closes the connection. It is safe on a nil Service and to call twice.
func (s *Service) Stop() {
	if s == nil {
		return
	}
	s.responder.Stop()
	s.closeConn()
}
