package bus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"k8s-lab/shared/message"
)

// offlineClient has no connection: every test using it must fail before sending.
func offlineClient(opts ...ClientOption) *Client {
	c := &Client{app: testRequester, timeout: defaultClientTimeout}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func wantCoded(t *testing.T, err error, code string) *message.Error {
	t.Helper()
	var coded *message.Error
	if !errors.As(err, &coded) {
		t.Fatalf("want *message.Error, got %T %v", err, err)
	}
	if coded.Code != code {
		t.Fatalf("code = %s (%s), want %s", coded.Code, coded.Message, code)
	}
	return coded
}

func TestRequestFailsBeforeSending(t *testing.T) {
	reg := testRegistry(t)
	tests := []struct {
		name      string
		client    *Client
		to        string
		operation string
		payload   any
		code      string
	}{
		{"payload that is not JSON", offlineClient(), testApp, "echo", make(chan int), message.CodeInvalidPayload},
		{"payload that is not an object", offlineClient(), testApp, "echo", []string{"a"}, message.CodeInvalidMessage},
		{"bad addressee", offlineClient(), "Bad App", "echo", map[string]string{"text": "x"}, message.CodeInvalidMessage},
		{"bad operation name", offlineClient(), testApp, "Bad-Op", map[string]string{"text": "x"}, message.CodeInvalidMessage},
		{"too large", offlineClient(), testApp, "echo", map[string]string{"text": strings.Repeat("a", message.MaxEncodedBytes)}, message.CodeTooLarge},
		{"registry: payload breaks the schema", offlineClient(WithRegistry(reg)), testApp, "echo", map[string]int{"text": 1}, message.CodeInvalidPayload},
		{"registry: unknown operation", offlineClient(WithRegistry(reg)), testApp, "nope", map[string]string{}, message.CodeUnknownOperation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// nc is nil: reaching the send would panic, so a pass proves nothing was sent.
			_, err := tt.client.Request(context.Background(), tt.to, tt.operation, tt.payload)
			wantCoded(t, err, tt.code)
		})
	}
}

// answerFor builds the bytes of an answer to req, with one thing changed by edit.
func answerFor(t *testing.T, req message.Envelope, edit func(*message.Envelope)) []byte {
	t.Helper()
	reply, err := message.Reply(req, testApp, map[string]string{"text": "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(&reply)
	}
	data, err := message.Encode(reply)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestClientAnswer(t *testing.T) {
	reg := testRegistry(t)
	req, err := message.NewRequest(testRequester, testApp, "echo", map[string]string{"text": "x"})
	if err != nil {
		t.Fatal(err)
	}
	otherID := "b7a1e0d4-5c2f-4f6a-8b13-0d9e4c7a2f55"

	t.Run("a response", func(t *testing.T) {
		env, err := offlineClient().answer(req, answerFor(t, req, nil))
		if err != nil || env.Type != message.TypeResponse || string(env.Payload) != `{"text":"ok"}` {
			t.Fatalf("got %+v, %v", env, err)
		}
	})
	t.Run("an error message becomes a *message.Error", func(t *testing.T) {
		data, _ := message.Encode(message.ErrorReply(req, testApp, message.CodeNotFound, "No document has that id.", false))
		env, err := offlineClient().answer(req, data)
		coded := wantCoded(t, err, message.CodeNotFound)
		if coded.Message != "No document has that id." || env.ID != "" || Retryable(err) {
			t.Fatalf("got %+v %+v retryable=%v", coded, env, Retryable(err))
		}
	})
	t.Run("an unavailable error is retryable", func(t *testing.T) {
		data, _ := message.Encode(message.ErrorReply(req, testApp, message.CodeUnavailable, "Busy.", true))
		_, err := offlineClient().answer(req, data)
		wantCoded(t, err, message.CodeUnavailable)
		if !Retryable(err) {
			t.Fatal("unavailable must be retryable")
		}
	})
	invalid := map[string][]byte{
		"not JSON":              []byte("hello"),
		"empty":                 nil,
		"oversized":             []byte(strings.Repeat("a", message.MaxEncodedBytes+1)),
		"another correlation":   answerFor(t, req, func(e *message.Envelope) { e.CorrelationID = otherID }),
		"another operation":     answerFor(t, req, func(e *message.Envelope) { e.Operation = "other" }),
		"an event":              answerFor(t, req, func(e *message.Envelope) { e.Type = message.TypeEvent }),
		"a request":             answerFor(t, req, func(e *message.Envelope) { e.Type = message.TypeRequest }),
		"breaks the rules":      answerFor(t, req, func(e *message.Envelope) { e.From = "Bad Name" }),
		"response schema break": answerFor(t, req, func(e *message.Envelope) { e.Payload = []byte(`{"text":5}`) }),
	}
	for name, data := range invalid {
		t.Run("invalid: "+name, func(t *testing.T) {
			// reg makes the response schema check part of the test.
			_, err := offlineClient(WithRegistry(reg)).answer(req, data)
			wantCoded(t, err, message.CodeInvalidMessage)
			if Retryable(err) {
				t.Fatal("an invalid answer is not retryable")
			}
		})
	}
	t.Run("without a registry the response schema is not checked", func(t *testing.T) {
		data := answerFor(t, req, func(e *message.Envelope) { e.Payload = []byte(`{"text":5}`) })
		if _, err := offlineClient().answer(req, data); err != nil {
			t.Fatalf("got %v", err)
		}
	})
}

func TestSendErrorMapping(t *testing.T) {
	tests := []struct {
		err       error
		code      string
		retryable bool
	}{
		{nats.ErrNoResponders, message.CodeUnavailable, true},
		{fmt.Errorf("wrapped: %w", nats.ErrNoResponders), message.CodeUnavailable, true},
		{context.DeadlineExceeded, message.CodeTimeout, true},
		{nats.ErrTimeout, message.CodeTimeout, true},
		{context.Canceled, message.CodeTimeout, true},
		{nats.ErrMaxPayload, message.CodeTooLarge, false},
		{nats.ErrConnectionClosed, message.CodeUnavailable, true},
		{nats.ErrConnectionDraining, message.CodeUnavailable, true},
		{nats.ErrConnectionReconnecting, message.CodeUnavailable, true},
		{nats.ErrNoServers, message.CodeUnavailable, true},
		{errors.New("something else"), message.CodeInternal, false},
	}
	for _, tt := range tests {
		err := sendError(tt.err)
		wantCoded(t, err, tt.code)
		if Retryable(err) != tt.retryable {
			t.Errorf("%v: retryable = %v, want %v", tt.err, Retryable(err), tt.retryable)
		}
		if strings.Contains(err.Error(), "something else") {
			t.Errorf("the Go error text leaked: %v", err)
		}
	}
}

func TestRetryable(t *testing.T) {
	if Retryable(nil) || Retryable(errors.New("x")) {
		t.Fatal("only a *message.Error with a retryable code is retryable")
	}
	for _, code := range []string{message.CodeUnavailable, message.CodeTimeout} {
		if !Retryable(&message.Error{Code: code, Message: "m"}) || !Retryable(fmt.Errorf("w: %w", &message.Error{Code: code, Message: "m"})) {
			t.Errorf("%s must be retryable", code)
		}
	}
	for _, code := range []string{message.CodeInvalidMessage, message.CodeNotFound, message.CodeInternal, message.CodeTooLarge} {
		if Retryable(&message.Error{Code: code, Message: "m"}) {
			t.Errorf("%s must not be retryable", code)
		}
	}
}

func TestAsError(t *testing.T) {
	coded := &message.Error{Code: message.CodeTooLarge, Message: "m"}
	if got := asError(fmt.Errorf("w: %w", coded)); got != coded {
		t.Fatalf("got %v", got)
	}
	wantCoded(t, asError(errors.New("plain")), message.CodeInternal)
}

func TestNewClientValidates(t *testing.T) {
	if _, err := NewClient(nil, testRequester); err == nil {
		t.Fatal("no connection must fail")
	}
	// A non-nil connection value is needed to reach the other checks.
	nc := &nats.Conn{}
	if _, err := NewClient(nc, "Bad App"); err == nil {
		t.Fatal("a bad app name must fail")
	}
	if _, err := NewClient(nc, testRequester, WithTimeout(0)); err == nil {
		t.Fatal("a zero timeout must fail")
	}
	c, err := NewClient(nc, testRequester, WithTimeout(time.Second), WithRegistry(testRegistry(t)))
	if err != nil || c.timeout != time.Second || c.reg == nil || c.app != testRequester {
		t.Fatalf("got %+v, %v", c, err)
	}
}
