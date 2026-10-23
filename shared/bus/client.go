package bus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"k8s-lab/shared/message"
)

const defaultClientTimeout = 30 * time.Second

// ClientOption changes a Client's defaults.
type ClientOption func(*Client)

// WithTimeout sets how long Request waits for the answer (default 30 s); a
// sooner deadline of the caller's context wins.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) { c.timeout = d }
}

// WithRegistry gives the client the operations of the app it calls: Request
// then checks the request payload before it sends it, and the response payload
// when it arrives. Without one only the envelope rules are checked.
func WithRegistry(reg *message.Registry) ClientOption {
	return func(c *Client) { c.reg = reg }
}

// Client sends requests from one app to the others.
type Client struct {
	nc      *nats.Conn
	app     string
	timeout time.Duration
	reg     *message.Registry
}

// NewClient returns a Client that sends as app.
func NewClient(nc *nats.Conn, app string, opts ...ClientOption) (*Client, error) {
	if nc == nil {
		return nil, errors.New("new client: no NATS connection")
	}
	if !validApp(app) {
		return nil, fmt.Errorf("new client: %q is not a valid app name (1 to 64 characters of a-z, 0-9, _ and -)", app)
	}
	c := &Client{nc: nc, app: app, timeout: defaultClientTimeout}
	for _, opt := range opts {
		opt(c)
	}
	if c.timeout <= 0 {
		return nil, errors.New("new client: the timeout must be positive")
	}
	return c, nil
}

// Request asks the app `to` to run operation and waits for its one answer. It returns
// the response envelope. Every error is a *message.Error: the error the other
// app answered with, unavailable (no app listens, or the bus is down), timeout
// (no answer in time or the context ended), too_large, or invalid_message for
// an answer that is not a valid response to this request. Retryable says
// whether to try again.
func (c *Client) Request(ctx context.Context, to, operation string, payload any, opts ...message.Option) (message.Envelope, error) {
	req, err := message.NewRequest(c.app, to, operation, payload, opts...)
	if err != nil {
		return message.Envelope{}, &message.Error{Code: message.CodeInvalidPayload, Message: "the payload cannot be encoded as JSON"}
	}
	if err := message.Validate(req, c.reg); err != nil {
		return message.Envelope{}, asError(err)
	}
	data, err := message.Encode(req)
	if err != nil {
		return message.Envelope{}, asError(err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	msg, err := c.nc.RequestWithContext(ctx, RequestSubject(to), data)
	if err != nil {
		return message.Envelope{}, sendError(err)
	}
	return c.answer(req, msg.Data)
}

// answer checks the bytes received against the request they should answer.
func (c *Client) answer(req message.Envelope, data []byte) (message.Envelope, error) {
	invalid := func(why string) (message.Envelope, error) {
		return message.Envelope{}, &message.Error{Code: message.CodeInvalidMessage, Message: "the answer is not a valid response to the request: " + why}
	}
	env, err := message.Decode(data)
	if err != nil {
		return invalid("it does not decode or is too large")
	}
	if err := message.Validate(env, c.reg); err != nil {
		return invalid("it breaks the message rules or the operation's schema")
	}
	if env.CorrelationID != req.ID || env.Operation != req.Operation {
		return invalid("its correlation_id or operation differ from the request's")
	}
	switch env.Type {
	case message.TypeResponse:
		return env, nil
	case message.TypeError:
		return message.Envelope{}, &message.Error{Code: env.Error.Code, Message: env.Error.Message}
	}
	return invalid("its type is neither response nor error")
}

// Retryable reports whether a failed request may be tried again: err is a
// *message.Error with the code unavailable or timeout.
func Retryable(err error) bool {
	var coded *message.Error
	return errors.As(err, &coded) && retryableCode(coded.Code)
}

// asError returns err as a *message.Error.
func asError(err error) error {
	var coded *message.Error
	if errors.As(err, &coded) {
		return coded
	}
	return &message.Error{Code: message.CodeInternal, Message: internalMessage}
}

// sendError maps a failure of the NATS request to a *message.Error.
func sendError(err error) error {
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return &message.Error{Code: message.CodeUnavailable, Message: "no app is listening for requests to that app"}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, nats.ErrTimeout):
		return &message.Error{Code: message.CodeTimeout, Message: "no answer arrived in time"}
	case errors.Is(err, context.Canceled):
		return &message.Error{Code: message.CodeTimeout, Message: "the request was canceled before an answer arrived"}
	case errors.Is(err, nats.ErrMaxPayload):
		return &message.Error{Code: message.CodeTooLarge, Message: "the message bus does not accept a message this large"}
	case errors.Is(err, nats.ErrConnectionClosed), errors.Is(err, nats.ErrConnectionDraining),
		errors.Is(err, nats.ErrConnectionReconnecting), errors.Is(err, nats.ErrNoServers):
		return &message.Error{Code: message.CodeUnavailable, Message: "the message bus is not available"}
	}
	return &message.Error{Code: message.CodeInternal, Message: "the request could not be sent"}
}
