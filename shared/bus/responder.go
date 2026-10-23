package bus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"k8s-lab/shared/message"
)

const (
	defaultMaxConcurrent  = 8
	defaultRequestTimeout = 25 * time.Second
	defaultStopGrace      = 10 * time.Second

	// internalMessage is the whole text of every internal error: what went
	// wrong stays in the log, never in the reply.
	internalMessage = "the request could not be processed"
	busyMessage     = "the app is busy with other requests; try again"

	maxLoggedBytes = 500
	maxStackBytes  = 4096

	// neutralID answers a request whose own id could not be read.
	neutralID = "00000000-0000-0000-0000-000000000000"
)

// Handler answers one request: the payload of the response, or an error. An
// error that is a *message.Error (not internal) goes to the requester with its
// code and message, so its message must be safe to show; any other error becomes
// an internal error with a fixed text. The request has been validated, payload
// included. The context ends when the request outlives its time limit or the
// Responder stops, and a handler must watch it.
type Handler func(ctx context.Context, req message.Envelope) (payload any, err error)

// ResponderOption changes a Responder's defaults.
type ResponderOption func(*Responder)

// WithMaxConcurrent sets how many handlers may run at once (default 8). A
// request that finds them all busy is answered unavailable at once.
func WithMaxConcurrent(n int) ResponderOption {
	return func(r *Responder) { r.maxConcurrent = n }
}

// WithRequestTimeout sets the time a handler has before its context ends
// (default 25 s, shorter than the Client's 30 s so the requester still gets an answer).
func WithRequestTimeout(d time.Duration) ResponderOption {
	return func(r *Responder) { r.requestTimeout = d }
}

// WithStopGrace sets how long Stop lets requests in flight finish before it ends
// their contexts (default 10 s).
func WithStopGrace(d time.Duration) ResponderOption {
	return func(r *Responder) { r.stopGrace = d }
}

// Responder answers the requests sent to one app. Register the operations with
// Handle, then Start; Stop when the app shuts down, before closing the connection.
type Responder struct {
	nc  *nats.Conn
	app string
	reg *message.Registry
	log *slog.Logger

	maxConcurrent  int
	requestTimeout time.Duration
	stopGrace      time.Duration

	slots  chan struct{} // one entry per running handler
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // the messages being processed
	once   sync.Once      // Stop runs once

	mu       sync.Mutex
	handlers map[string]Handler
	sub      *nats.Subscription
	started  bool
	stopped  bool
}

// NewResponder returns a Responder of app on the connection. reg lists the
// operations the app answers; a nil log means slog.Default().
func NewResponder(nc *nats.Conn, app string, reg *message.Registry, log *slog.Logger, opts ...ResponderOption) (*Responder, error) {
	if nc == nil {
		return nil, errors.New("new responder: no NATS connection")
	}
	r, err := newResponder(app, reg, log, opts...)
	if err != nil {
		return nil, err
	}
	r.nc = nc
	return r, nil
}

// newResponder is NewResponder without a connection: enough for process.
func newResponder(app string, reg *message.Registry, log *slog.Logger, opts ...ResponderOption) (*Responder, error) {
	if !validApp(app) {
		return nil, fmt.Errorf("new responder: %q is not a valid app name (1 to 64 characters of a-z, 0-9, _ and -)", app)
	}
	if reg == nil {
		return nil, errors.New("new responder: no operation registry")
	}
	if log == nil {
		log = slog.Default()
	}
	r := &Responder{
		app: app, reg: reg, log: log,
		maxConcurrent: defaultMaxConcurrent, requestTimeout: defaultRequestTimeout, stopGrace: defaultStopGrace,
		handlers: map[string]Handler{},
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.maxConcurrent < 1 {
		return nil, fmt.Errorf("new responder: the maximum of concurrent handlers must be at least 1, got %d", r.maxConcurrent)
	}
	if r.requestTimeout <= 0 || r.stopGrace <= 0 {
		return nil, errors.New("new responder: the request timeout and the stop grace must be positive")
	}
	r.slots = make(chan struct{}, r.maxConcurrent)
	r.ctx, r.cancel = context.WithCancel(context.Background())
	return r, nil
}

// Handle sets the handler of an operation. The operation must be in the
// registry, and each has one handler. Register all before Start.
func (r *Responder) Handle(operation string, h Handler) error {
	if h == nil {
		return fmt.Errorf("handle %q: the handler is nil", operation)
	}
	if !r.reg.Has(operation) {
		return fmt.Errorf("handle %q: the operation is not in the registry", operation)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.handlers[operation]; dup {
		return fmt.Errorf("handle %q: the operation already has a handler", operation)
	}
	r.handlers[operation] = h
	return nil
}

// Start subscribes to dp.<app>.request in the queue group <app>, so replicas
// of the app share the requests, and returns once the server knows about it.
func (r *Responder) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.stopped {
		return errors.New("start responder: it was already started or stopped")
	}
	subject := RequestSubject(r.app)
	sub, err := r.nc.QueueSubscribe(subject, r.app, r.onMessage)
	if err != nil {
		return fmt.Errorf("start responder: subscribe to %s: %w", subject, err)
	}
	if err := r.nc.FlushTimeout(connectTimeout); err != nil {
		_ = sub.Unsubscribe()
		return fmt.Errorf("start responder: the server did not confirm the subscription to %s: %w", subject, err)
	}
	r.sub, r.started = sub, true
	return nil
}

// Stop stops taking requests, lets those in flight finish for the stop grace,
// then ends their contexts and returns (after one more second at most, if a
// handler ignores its context). Replies of requests in flight are still sent, so
// call it before closing the connection. It is safe to call more than once.
func (r *Responder) Stop() {
	r.once.Do(r.stop)
}

func (r *Responder) stop() {
	r.mu.Lock()
	sub := r.sub
	r.mu.Unlock()

	deadline := time.Now().Add(r.stopGrace)
	if sub != nil {
		// Drain keeps the callbacks running until the messages already received are handed over.
		if err := sub.Drain(); err != nil {
			r.log.Warn("bus responder drain failed", "app", r.app, "error", errText(err))
		} else {
			for sub.IsValid() && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
		}
	}
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Until(deadline)):
	}
	r.cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		r.log.Warn("bus responder stopped with handlers still running", "app", r.app)
	}
}

// onMessage runs in NATS's callback goroutine: it only hands the message to its
// own goroutine, so a slow handler never holds up the others.
func (r *Responder) onMessage(msg *nats.Msg) {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		r.log.Warn("bus request dropped: the responder is stopped", "app", r.app)
		return
	}
	r.wg.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.wg.Done()
		reply := r.process(r.ctx, msg.Data)
		switch {
		case msg.Reply == "":
			r.log.Debug("bus request had no reply subject: the answer is discarded", "app", r.app)
		case reply == nil:
			r.log.Error("bus request has no answer: the reply could not be built", "app", r.app)
		default:
			if err := msg.Respond(reply); err != nil {
				r.log.Warn("bus reply not sent", "app", r.app, "error", errText(err))
			}
		}
	}()
}

// process is everything between a received message and its reply: it decodes
// and validates the request, runs its handler and builds the response or the
// error message. It returns nil only if not even an error reply can be built.
func (r *Responder) process(ctx context.Context, data []byte) []byte {
	env, err := message.Decode(data)
	if err != nil {
		r.log.Warn("bus request refused", "app", r.app, "code", codeOf(err), "bytes", len(data))
		return r.errorReply(salvage(data), codeOf(err), textOf(err))
	}
	if err := message.Validate(env, nil); err != nil {
		r.log.Warn("bus request refused", "app", r.app, "code", codeOf(err), "bytes", len(data))
		return r.errorReply(salvage(data), codeOf(err), textOf(err))
	}
	asked := askedBy(env.ID, env.From, env.Operation, env.Context)
	if env.Type != message.TypeRequest {
		r.log.Warn("bus message refused: not a request", "app", r.app, "operation", env.Operation, "id", env.ID, "type", string(env.Type))
		return r.errorReply(asked, message.CodeInvalidMessage, "only a message of type request can be sent to an app")
	}
	if err := r.reg.ValidateRequestPayload(env.Operation, env.Payload); err != nil {
		r.log.Warn("bus request refused", "app", r.app, "operation", env.Operation, "id", env.ID, "code", codeOf(err))
		return r.errorReply(asked, codeOf(err), textOf(err))
	}
	r.mu.Lock()
	h := r.handlers[env.Operation]
	r.mu.Unlock()
	if h == nil {
		r.log.Warn("bus request refused: no handler", "app", r.app, "operation", env.Operation, "id", env.ID)
		return r.errorReply(asked, message.CodeUnknownOperation, "this app does not answer that operation")
	}

	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	default:
		r.log.Warn("bus request refused: all handlers are busy", "app", r.app, "operation", env.Operation, "id", env.ID, "max_concurrent", r.maxConcurrent)
		return r.errorReply(asked, message.CodeUnavailable, busyMessage)
	}

	payload, err := r.call(ctx, h, env)
	if err != nil {
		var coded *message.Error
		if errors.As(err, &coded) && coded.Code != message.CodeInternal {
			return r.errorReply(asked, coded.Code, coded.Message)
		}
		if !errors.Is(err, errHandlerPanicked) {
			r.log.Error("bus handler failed", "app", r.app, "operation", env.Operation, "id", env.ID, "error", cut(err.Error(), maxLoggedBytes))
		}
		return r.errorReply(asked, message.CodeInternal, internalMessage)
	}

	reply, err := message.Reply(env, r.app, payload)
	if err != nil {
		r.log.Error("bus response not built", "app", r.app, "operation", env.Operation, "id", env.ID, "error", cut(err.Error(), maxLoggedBytes))
		return r.errorReply(asked, message.CodeInternal, internalMessage)
	}
	if err := message.Validate(reply, r.reg); err != nil {
		r.log.Error("bus response breaks its schema", "app", r.app, "operation", env.Operation, "id", env.ID, "code", codeOf(err))
		return r.errorReply(asked, message.CodeInternal, internalMessage)
	}
	encoded, err := message.Encode(reply)
	if err != nil {
		r.log.Error("bus response not encoded", "app", r.app, "operation", env.Operation, "id", env.ID, "code", codeOf(err))
		return r.errorReply(asked, message.CodeInternal, internalMessage)
	}
	return encoded
}

var errHandlerPanicked = errors.New("the handler panicked")

// call runs the handler under the request's time limit and turns a panic into
// errHandlerPanicked after logging it.
func (r *Responder) call(ctx context.Context, h Handler, req message.Envelope) (payload any, err error) {
	ctx, cancel := context.WithTimeout(ctx, r.requestTimeout)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			r.log.Error("bus handler panicked", "app", r.app, "operation", req.Operation, "id", req.ID,
				"panic", cut(fmt.Sprint(p), maxLoggedBytes), "stack", cut(string(debug.Stack()), maxStackBytes))
			payload, err = nil, errHandlerPanicked
		}
	}()
	return h(ctx, req)
}

// errorReply encodes the error message answering asked. A reply that would not
// be a valid envelope (an app name or id the sender got wrong, a code or message
// a handler made up) falls back to a neutral addressee, then to the internal error.
func (r *Responder) errorReply(asked message.Envelope, code, text string) []byte {
	neutral := askedBy(neutralID, "unknown", "unknown", nil)
	for _, candidate := range []message.Envelope{asked, neutral} {
		reply := message.ErrorReply(candidate, r.app, code, text, retryableCode(code))
		if message.Validate(reply, nil) != nil {
			continue
		}
		if data, err := message.Encode(reply); err == nil {
			return data
		}
	}
	if code == message.CodeInternal && text == internalMessage {
		return nil
	}
	r.log.Error("bus error reply replaced by an internal error", "app", r.app, "operation", asked.Operation, "id", asked.CorrelationID, "code", cut(code, 64))
	return r.errorReply(asked, message.CodeInternal, internalMessage)
}

// askedBy is the stand-in for the request an error reply answers.
func askedBy(id, from, operation string, context *message.Context) message.Envelope {
	return message.Envelope{CorrelationID: id, From: from, Operation: operation, Context: context}
}

// salvage reads the id, sender and operation of a message that did not decode
// or validate, as far as they can be read, so the requester can match the error
// to its request. errorReply drops what is not valid.
func salvage(data []byte) message.Envelope {
	if len(data) > message.MaxEncodedBytes {
		return askedBy(neutralID, "unknown", "unknown", nil)
	}
	var head struct {
		ID        string `json:"id"`
		From      string `json:"from"`
		Operation string `json:"operation"`
	}
	// A partial read is all that is wanted: the decoder fills what it can
	// before it reports a wrong type, and ignores what follows the first value.
	_ = json.NewDecoder(bytes.NewReader(data)).Decode(&head)
	return askedBy(head.ID, head.From, head.Operation, nil)
}

// retryableCode says whether a requester may try again: the app was busy or
// away, or the answer did not come in time. Both ends derive the wire's
// error.retryable from the code with this function.
func retryableCode(code string) bool {
	return code == message.CodeUnavailable || code == message.CodeTimeout
}

// codeOf and textOf read an error that comes from package message.
func codeOf(err error) string {
	var coded *message.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return message.CodeInternal
}

func textOf(err error) string {
	var coded *message.Error
	if errors.As(err, &coded) {
		return coded.Message
	}
	return internalMessage
}

// cut shortens s to at most n bytes for a log line.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
