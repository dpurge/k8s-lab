package message

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"
)

// SchemaVersion is the only envelope version this package reads and writes.
const SchemaVersion = 1

// MaxEncodedBytes is the largest encoded envelope Decode and Encode accept.
const MaxEncodedBytes = 512 << 10

// Type tells what a message is for.
type Type string

const (
	TypeRequest  Type = "request"
	TypeResponse Type = "response"
	TypeEvent    Type = "event"
	TypeError    Type = "error"
)

// The error codes an Error and an ErrorBody can carry.
const (
	CodeInvalidMessage   = "invalid_message"
	CodeUnknownOperation = "unknown_operation"
	CodeInvalidPayload   = "invalid_payload"
	CodeNotFound         = "not_found"
	CodeTooLarge         = "too_large"
	CodeTimeout          = "timeout"
	CodeUnavailable      = "unavailable"
	CodeInternal         = "internal"
)

// knownCodes is the closed set of codes; the envelope schema lists the same.
var knownCodes = map[string]bool{
	CodeInvalidMessage: true, CodeUnknownOperation: true, CodeInvalidPayload: true, CodeNotFound: true,
	CodeTooLarge: true, CodeTimeout: true, CodeUnavailable: true, CodeInternal: true,
}

// Envelope is one message between two apps. Payload stays a json.RawMessage
// because its shape belongs to the operation: the bytes a sender wrote are the
// bytes a receiver (or a log line) sees, and a receiver decodes them into its own
// type only after the registry has checked them.
type Envelope struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"id"`
	Type          Type            `json:"type"`
	CorrelationID string          `json:"correlation_id"`
	From          string          `json:"from"`
	To            string          `json:"to"`
	CreatedAt     string          `json:"created_at"`
	Operation     string          `json:"operation"`
	Context       *Context        `json:"context,omitempty"`
	Summary       string          `json:"summary,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	Error         *ErrorBody      `json:"error,omitempty"`
}

// Context says who and what a message is about. Both fields are informational:
// no one authenticates them.
type Context struct {
	UserID string `json:"user_id,omitempty"`
	TaskID string `json:"task_id,omitempty"`
}

// ErrorBody is the failure an error message reports.
type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Error is what Decode, Encode, Validate and the Registry return: a code from
// the fixed set and a message that is safe to show to a user.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func newError(code, message string) *Error { return &Error{Code: code, Message: message} }

// Option sets an optional field of a new message.
type Option func(*Envelope)

// WithContext sets the context of a message; empty values are left out.
func WithContext(userID, taskID string) Option {
	return func(e *Envelope) {
		if userID == "" && taskID == "" {
			e.Context = nil
			return
		}
		e.Context = &Context{UserID: userID, TaskID: taskID}
	}
}

// WithSummary sets the one-line human-readable text of a message.
func WithSummary(summary string) Option {
	return func(e *Envelope) { e.Summary = summary }
}

// now and newID are variables so tests can fix them.
var (
	now   = time.Now
	newID = newUUID
)

// NewRequest builds a request: a new id, and a correlation_id equal to it.
func NewRequest(from, to, operation string, payload any, opts ...Option) (Envelope, error) {
	id := newID()
	return build(Envelope{ID: id, Type: TypeRequest, CorrelationID: id, From: from, To: to, Operation: operation}, payload, opts)
}

// Reply builds the response to req: addressed to its sender, with its
// correlation_id, operation and context.
func Reply(req Envelope, from string, payload any, opts ...Option) (Envelope, error) {
	return build(answer(req, TypeResponse, from), payload, opts)
}

// Event builds a progress message about req, addressed like a Reply.
func Event(req Envelope, from string, payload any, opts ...Option) (Envelope, error) {
	return build(answer(req, TypeEvent, from), payload, opts)
}

// ErrorReply builds the error message that answers req.
func ErrorReply(req Envelope, from, code, message string, retryable bool) Envelope {
	e := answer(req, TypeError, from)
	e.Error = &ErrorBody{Code: code, Message: message, Retryable: retryable}
	return stamp(e)
}

// answer is the part Reply, Event and ErrorReply share.
func answer(req Envelope, t Type, from string) Envelope {
	e := Envelope{Type: t, CorrelationID: req.CorrelationID, From: from, To: req.From, Operation: req.Operation}
	if req.Context != nil {
		c := *req.Context
		e.Context = &c
	}
	return e
}

func build(e Envelope, payload any, opts []Option) (Envelope, error) {
	if payload == nil {
		payload = struct{}{}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("encode %s payload of %q: %w", e.Type, e.Operation, err)
	}
	e.Payload = raw
	for _, opt := range opts {
		opt(&e)
	}
	return stamp(e), nil
}

// stamp sets the fields every new message gets.
func stamp(e Envelope) Envelope {
	e.SchemaVersion = SchemaVersion
	if e.ID == "" {
		e.ID = newID()
	}
	e.CreatedAt = now().UTC().Format(time.RFC3339Nano)
	return e
}

// newUUID returns a random (version 4) UUID in its canonical lower-case form.
func newUUID() string {
	var b [16]byte
	rand.Read(b[:]) // never returns an error (see its documentation)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
