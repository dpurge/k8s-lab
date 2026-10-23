package bus

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"

	"k8s-lab/shared/message"
)

const (
	testApp       = "testapp"
	testRequester = "tester"
)

// canary is a payload value that must never show up in a log line or an error reply.
const canary = "PAYLOAD-CANARY-7f3a91"

// testRegistry knows one operation, echo: {text} in, {text} out.
func testRegistry(t *testing.T) *message.Registry {
	t.Helper()
	reg := message.NewRegistry()
	err := reg.Register(message.Operation{
		Name:     "echo",
		Request:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
		Response: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
	})
	if err != nil {
		t.Fatalf("register echo: %v", err)
	}
	return reg
}

// syncBuffer is a bytes.Buffer that handler goroutines can log to.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// capturedLogger logs everything, debug included, into the returned buffer.
func capturedLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// echoHandler answers {text} with the same {text}.
func echoHandler(_ context.Context, req message.Envelope) (any, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(req.Payload, &in); err != nil {
		return nil, err
	}
	return map[string]string{"text": in.Text}, nil
}

// newTestResponder returns a Responder without a connection, with echo handled.
func newTestResponder(t *testing.T, opts ...ResponderOption) (*Responder, *syncBuffer) {
	t.Helper()
	logger, logs := capturedLogger()
	r, err := newResponder(testApp, testRegistry(t), logger, opts...)
	if err != nil {
		t.Fatalf("newResponder: %v", err)
	}
	if err := r.Handle("echo", echoHandler); err != nil {
		t.Fatalf("Handle echo: %v", err)
	}
	return r, logs
}

// requestBytes encodes a request from testRequester to testApp.
func requestBytes(t *testing.T, operation string, payload any) (message.Envelope, []byte) {
	t.Helper()
	req, err := message.NewRequest(testRequester, testApp, operation, payload)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	data, err := message.Encode(req)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return req, data
}

// decodeReply decodes and validates reply bytes, failing the test if they are not an envelope.
func decodeReply(t *testing.T, data []byte) message.Envelope {
	t.Helper()
	if data == nil {
		t.Fatal("no reply bytes")
	}
	env, err := message.Decode(data)
	if err != nil {
		t.Fatalf("reply does not decode: %v\n%s", err, data)
	}
	if err := message.Validate(env, nil); err != nil {
		t.Fatalf("reply is not a valid envelope: %v\n%s", err, data)
	}
	return env
}

// wantError checks that reply is an error message with the code, retryable flag
// and (when text is not empty) message, answering the request.
func wantError(t *testing.T, reply []byte, req message.Envelope, code string, retryable bool, text string) message.Envelope {
	t.Helper()
	env := decodeReply(t, reply)
	if env.Type != message.TypeError || env.Error == nil {
		t.Fatalf("want an error message, got %s", reply)
	}
	if env.Error.Code != code || env.Error.Retryable != retryable {
		t.Fatalf("error = {%s retryable=%v}, want {%s retryable=%v}: %s", env.Error.Code, env.Error.Retryable, code, retryable, env.Error.Message)
	}
	if text != "" && env.Error.Message != text {
		t.Fatalf("error message = %q, want %q", env.Error.Message, text)
	}
	if req.ID != "" && (env.CorrelationID != req.ID || env.Operation != req.Operation) {
		t.Fatalf("error answers %s/%s, want %s/%s", env.CorrelationID, env.Operation, req.ID, req.Operation)
	}
	return env
}
