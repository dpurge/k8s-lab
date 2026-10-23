package bus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"k8s-lab/shared/message"
)

func TestProcessHandlerSuccess(t *testing.T) {
	r, _ := newTestResponder(t)
	req, data := requestBytes(t, "echo", map[string]string{"text": "hello"})

	env := decodeReply(t, r.process(context.Background(), data))

	if env.Type != message.TypeResponse || env.CorrelationID != req.ID || env.Operation != "echo" {
		t.Fatalf("want a response to %s, got %+v", req.ID, env)
	}
	if env.From != testApp || env.To != testRequester {
		t.Fatalf("from/to = %s/%s, want %s/%s", env.From, env.To, testApp, testRequester)
	}
	if string(env.Payload) != `{"text":"hello"}` {
		t.Fatalf("payload = %s", env.Payload)
	}
}

func TestProcessKeepsContext(t *testing.T) {
	r, _ := newTestResponder(t)
	req, err := message.NewRequest(testRequester, testApp, "echo", map[string]string{"text": "x"}, message.WithContext("u-1", "t-9"))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := message.Encode(req)

	env := decodeReply(t, r.process(context.Background(), data))

	if env.Context == nil || env.Context.UserID != "u-1" || env.Context.TaskID != "t-9" {
		t.Fatalf("context = %+v", env.Context)
	}
}

func TestProcessErrorCodes(t *testing.T) {
	notFound := &message.Error{Code: message.CodeNotFound, Message: "No document has that id."}
	tests := []struct {
		name      string
		operation string
		payload   any
		handler   Handler
		code      string
		retryable bool
		text      string // the whole message, when it is fixed
	}{
		{name: "handler not_found", operation: "echo", payload: map[string]string{"text": "x"},
			handler: func(context.Context, message.Envelope) (any, error) { return nil, notFound },
			code:    message.CodeNotFound, text: "No document has that id."},
		{name: "wrapped not_found", operation: "echo", payload: map[string]string{"text": "x"},
			handler: func(context.Context, message.Envelope) (any, error) { return nil, fmt.Errorf("lookup: %w", notFound) },
			code:    message.CodeNotFound, text: "No document has that id."},
		{name: "handler unavailable is retryable", operation: "echo", payload: map[string]string{"text": "x"},
			handler: func(context.Context, message.Envelope) (any, error) {
				return nil, &message.Error{Code: message.CodeUnavailable, Message: "Database is down."}
			},
			code: message.CodeUnavailable, retryable: true, text: "Database is down."},
		{name: "other handler error", operation: "echo", payload: map[string]string{"text": "x"},
			handler: func(context.Context, message.Envelope) (any, error) { return nil, errors.New("boom") },
			code:    message.CodeInternal, text: internalMessage},
		{name: "handler internal code gets the fixed text", operation: "echo", payload: map[string]string{"text": "x"},
			handler: func(context.Context, message.Envelope) (any, error) {
				return nil, &message.Error{Code: message.CodeInternal, Message: "secret detail"}
			},
			code: message.CodeInternal, text: internalMessage},
		{name: "handler error with an invented code", operation: "echo", payload: map[string]string{"text": "x"},
			handler: func(context.Context, message.Envelope) (any, error) {
				return nil, &message.Error{Code: "made_up", Message: "x"}
			},
			code: message.CodeInternal, text: internalMessage},
		{name: "handler error with an empty message", operation: "echo", payload: map[string]string{"text": "x"},
			handler: func(context.Context, message.Envelope) (any, error) {
				return nil, &message.Error{Code: message.CodeNotFound}
			},
			code: message.CodeInternal, text: internalMessage},
		{name: "handler panic", operation: "echo", payload: map[string]string{"text": "x"},
			handler: func(context.Context, message.Envelope) (any, error) { panic("kaboom") },
			code:    message.CodeInternal, text: internalMessage},
		{name: "handler panic with an error value", operation: "echo", payload: map[string]string{"text": "x"},
			handler: func(context.Context, message.Envelope) (any, error) { panic(notFound) },
			code:    message.CodeInternal, text: internalMessage},
		{name: "handler panic with nil", operation: "echo", payload: map[string]string{"text": "x"},
			handler: func(context.Context, message.Envelope) (any, error) { panic(nil) },
			code:    message.CodeInternal, text: internalMessage},
		{name: "response breaks its schema", operation: "echo", payload: map[string]string{"text": "x"},
			handler: func(context.Context, message.Envelope) (any, error) { return map[string]int{"text": 1}, nil },
			code:    message.CodeInternal, text: internalMessage},
		{name: "response is not an object", operation: "echo", payload: map[string]string{"text": "x"},
			handler: func(context.Context, message.Envelope) (any, error) { return []string{"a"}, nil },
			code:    message.CodeInternal, text: internalMessage},
		{name: "response cannot be encoded", operation: "echo", payload: map[string]string{"text": "x"},
			handler: func(context.Context, message.Envelope) (any, error) { return make(chan int), nil },
			code:    message.CodeInternal, text: internalMessage},
		{name: "response too large", operation: "echo", payload: map[string]string{"text": "x"},
			handler: func(context.Context, message.Envelope) (any, error) {
				return map[string]string{"text": strings.Repeat("a", message.MaxEncodedBytes)}, nil
			},
			code: message.CodeInternal, text: internalMessage},
		{name: "bad request payload", operation: "echo", payload: map[string]int{"text": 1}, code: message.CodeInvalidPayload},
		{name: "unknown field in the payload", operation: "echo", payload: map[string]string{"text": "x", "more": "y"}, code: message.CodeInvalidPayload},
		{name: "operation not in the registry", operation: "nope", payload: map[string]string{}, code: message.CodeUnknownOperation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newTestResponder(t)
			if tt.handler != nil {
				r.handlers["echo"] = tt.handler
			}
			req, data := requestBytes(t, tt.operation, tt.payload)

			wantError(t, r.process(context.Background(), data), req, tt.code, tt.retryable, tt.text)
		})
	}
}

func TestProcessRegisteredOperationWithoutHandler(t *testing.T) {
	r, _ := newTestResponder(t)
	delete(r.handlers, "echo")
	req, data := requestBytes(t, "echo", map[string]string{"text": "x"})

	wantError(t, r.process(context.Background(), data), req, message.CodeUnknownOperation, false, "")
}

func TestProcessRefusesWhatIsNotAValidRequest(t *testing.T) {
	r, _ := newTestResponder(t)
	req, good := requestBytes(t, "echo", map[string]string{"text": "x"})

	response := req
	response.Type = message.TypeResponse
	response.ID = "b7a1e0d4-5c2f-4f6a-8b13-0d9e4c7a2f55"
	responseBytes, _ := message.Encode(response)

	event := response
	event.Type = message.TypeEvent
	eventBytes, _ := message.Encode(event)

	noPayload := req
	noPayload.Payload = nil
	noPayloadBytes, _ := message.Encode(noPayload)

	tests := []struct {
		name string
		data []byte
		code string
		// answers is the request the error must be matched to, when the bytes carry its id.
		answers message.Envelope
	}{
		{name: "not JSON", data: []byte("not json at all"), code: message.CodeInvalidMessage},
		{name: "empty", data: nil, code: message.CodeInvalidMessage},
		{name: "JSON array", data: []byte(`[1,2]`), code: message.CodeInvalidMessage},
		{name: "unknown field", data: []byte(string(good[:len(good)-1]) + `,"extra":1}`), code: message.CodeInvalidMessage, answers: req},
		{name: "data after the envelope", data: append(append([]byte{}, good...), []byte(` {}`)...), code: message.CodeInvalidMessage, answers: req},
		{name: "oversized", data: []byte(strings.Repeat("a", message.MaxEncodedBytes+1)), code: message.CodeTooLarge},
		{name: "a response sent to an app", data: responseBytes, code: message.CodeInvalidMessage},
		{name: "an event sent to an app", data: eventBytes, code: message.CodeInvalidMessage},
		{name: "request without payload", data: noPayloadBytes, code: message.CodeInvalidMessage, answers: req},
		{name: "wrong schema_version", data: []byte(strings.Replace(string(good), `"schema_version":1`, `"schema_version":2`, 1)), code: message.CodeInvalidMessage, answers: req},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantError(t, r.process(context.Background(), tt.data), tt.answers, tt.code, false, "")
		})
	}
}

func TestProcessAnswersAnUnreadableRequestWithANeutralError(t *testing.T) {
	r, _ := newTestResponder(t)
	// Valid JSON, but the id and the sender are not what an envelope allows.
	data := []byte(`{"schema_version":1,"id":"nope","from":"Bad Name","operation":"echo","payload":{}}`)

	env := wantError(t, r.process(context.Background(), data), message.Envelope{}, message.CodeInvalidMessage, false, "")

	if env.CorrelationID != neutralID || env.To != "unknown" {
		t.Fatalf("want the neutral addressee, got correlation_id=%s to=%s", env.CorrelationID, env.To)
	}
}

func TestProcessMatchesAnInvalidRequestToItsSender(t *testing.T) {
	r, _ := newTestResponder(t)
	req, _ := requestBytes(t, "echo", map[string]string{"text": "x"})
	// A request that breaks a rule (no created_at) but whose id, sender and operation can be read.
	data := []byte(fmt.Sprintf(`{"schema_version":1,"id":%q,"type":"request","correlation_id":%q,"from":%q,"to":"testapp","operation":"echo","payload":{}}`, req.ID, req.ID, testRequester))

	env := wantError(t, r.process(context.Background(), data), req, message.CodeInvalidMessage, false, "")

	if env.To != testRequester {
		t.Fatalf("to = %s, want %s", env.To, testRequester)
	}
}

func TestProcessSaturated(t *testing.T) {
	r, _ := newTestResponder(t, WithMaxConcurrent(1))
	started, release := make(chan struct{}), make(chan struct{})
	r.handlers["echo"] = func(ctx context.Context, req message.Envelope) (any, error) {
		close(started)
		<-release
		return echoHandler(ctx, req)
	}
	_, first := requestBytes(t, "echo", map[string]string{"text": "first"})
	firstDone := make(chan []byte)
	go func() { firstDone <- r.process(context.Background(), first) }()
	<-started

	req, second := requestBytes(t, "echo", map[string]string{"text": "second"})
	begin := time.Now()
	reply := r.process(context.Background(), second)
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("a saturated responder took %v to refuse; it must not wait for a slot", elapsed)
	}
	wantError(t, reply, req, message.CodeUnavailable, true, busyMessage)

	close(release)
	if env := decodeReply(t, <-firstDone); env.Type != message.TypeResponse {
		t.Fatalf("the first request must still be answered, got %+v", env)
	}
	// The slot is free again.
	r.handlers["echo"] = echoHandler
	_, third := requestBytes(t, "echo", map[string]string{"text": "third"})
	if env := decodeReply(t, r.process(context.Background(), third)); env.Type != message.TypeResponse {
		t.Fatalf("after a slot is free want a response, got %+v", env)
	}
}

func TestProcessRunsAtMostMaxConcurrentHandlers(t *testing.T) {
	const max = 3
	r, _ := newTestResponder(t, WithMaxConcurrent(max))
	running, release := make(chan struct{}, 100), make(chan struct{})
	r.handlers["echo"] = func(ctx context.Context, req message.Envelope) (any, error) {
		running <- struct{}{}
		<-release
		return echoHandler(ctx, req)
	}
	replies := make(chan []byte, 10)
	for i := 0; i < 10; i++ {
		_, data := requestBytes(t, "echo", map[string]string{"text": "x"})
		go func() { replies <- r.process(context.Background(), data) }()
	}
	// 7 requests are refused at once; 3 hold the slots until released.
	refused := 0
	for refused < 10-max {
		env := decodeReply(t, <-replies)
		if env.Type != message.TypeError || env.Error.Code != message.CodeUnavailable {
			t.Fatalf("want unavailable, got %+v", env)
		}
		refused++
	}
	if len(running) != max {
		t.Fatalf("%d handlers ran at once, want %d", len(running), max)
	}
	close(release)
	for i := 0; i < max; i++ {
		if env := decodeReply(t, <-replies); env.Type != message.TypeResponse {
			t.Fatalf("want a response, got %+v", env)
		}
	}
}

func TestProcessHandlerContext(t *testing.T) {
	t.Run("has the request deadline", func(t *testing.T) {
		r, _ := newTestResponder(t, WithRequestTimeout(30*time.Millisecond))
		var deadlineSet bool
		r.handlers["echo"] = func(ctx context.Context, _ message.Envelope) (any, error) {
			_, deadlineSet = ctx.Deadline()
			<-ctx.Done()
			return nil, ctx.Err()
		}
		req, data := requestBytes(t, "echo", map[string]string{"text": "x"})

		reply := r.process(context.Background(), data)

		if !deadlineSet {
			t.Fatal("the handler's context has no deadline")
		}
		wantError(t, reply, req, message.CodeInternal, false, internalMessage)
	})
	t.Run("ends when the responder's context ends", func(t *testing.T) {
		r, _ := newTestResponder(t)
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{})
		r.handlers["echo"] = func(ctx context.Context, _ message.Envelope) (any, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		_, data := requestBytes(t, "echo", map[string]string{"text": "x"})
		done := make(chan []byte)
		go func() { done <- r.process(ctx, data) }()
		<-started
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the handler did not see its context end")
		}
	})
}

// TestNoErrorReplyHoldsGoErrorOrPanicText is the security property: whatever a
// handler says in a Go error or a panic stays on the server.
func TestNoErrorReplyHoldsGoErrorOrPanicText(t *testing.T) {
	const errorText, panicText = "ERRTEXT-db-password-hunter2", "PANICTEXT-index-out-of-range-99"
	failures := map[string]Handler{
		"plain error": func(context.Context, message.Envelope) (any, error) { return nil, errors.New(errorText) },
		"wrapped error": func(context.Context, message.Envelope) (any, error) {
			return nil, fmt.Errorf("outer: %w", errors.New(errorText))
		},
		"panic string": func(context.Context, message.Envelope) (any, error) { panic(panicText) },
		"panic error":  func(context.Context, message.Envelope) (any, error) { panic(errors.New(panicText)) },
		"internal-coded": func(context.Context, message.Envelope) (any, error) {
			return nil, &message.Error{Code: message.CodeInternal, Message: errorText}
		},
		"invented code": func(context.Context, message.Envelope) (any, error) {
			return nil, &message.Error{Code: errorText, Message: errorText}
		},
	}
	for name, h := range failures {
		t.Run(name, func(t *testing.T) {
			r, logs := newTestResponder(t)
			r.handlers["echo"] = h
			req, data := requestBytes(t, "echo", map[string]string{"text": "x"})

			reply := r.process(context.Background(), data)

			wantError(t, reply, req, message.CodeInternal, false, internalMessage)
			for _, secret := range []string{errorText, panicText, "goroutine", "runtime"} {
				if strings.Contains(string(reply), secret) {
					t.Fatalf("the reply holds %q: %s", secret, reply)
				}
			}
			// The cause is in the log, with the operation and the id.
			logged := logs.String()
			if !strings.Contains(logged, `"operation":"echo"`) || !strings.Contains(logged, req.ID) {
				t.Fatalf("the log must name the operation and id: %s", logged)
			}
		})
	}
}

func TestPanicIsLoggedWithItsText(t *testing.T) {
	r, logs := newTestResponder(t)
	r.handlers["echo"] = func(context.Context, message.Envelope) (any, error) { panic("kaboom-text") }
	_, data := requestBytes(t, "echo", map[string]string{"text": "x"})

	r.process(context.Background(), data)

	logged := logs.String()
	for _, want := range []string{"bus handler panicked", "kaboom-text", `"stack"`} {
		if !strings.Contains(logged, want) {
			t.Fatalf("the log lacks %q: %s", want, logged)
		}
	}
}

// TestLogsHoldNoPayloadValues runs every path with a canary in the payload (and
// in garbage bytes) and requires it nowhere in the log or in an error reply.
func TestLogsHoldNoPayloadValues(t *testing.T) {
	paths := map[string]struct {
		operation string
		payload   any
		handler   Handler
	}{
		"success":       {operation: "echo", payload: map[string]string{"text": canary}},
		"bad payload":   {operation: "echo", payload: map[string]string{"text": canary, canary: canary}},
		"unknown":       {operation: "nope", payload: map[string]string{"text": canary}},
		"handler error": {operation: "echo", payload: map[string]string{"text": canary}, handler: func(context.Context, message.Envelope) (any, error) { return nil, errors.New("failed") }},
		"handler panic": {operation: "echo", payload: map[string]string{"text": canary}, handler: func(context.Context, message.Envelope) (any, error) { panic("failed") }},
		"bad response":  {operation: "echo", payload: map[string]string{"text": canary}, handler: func(context.Context, message.Envelope) (any, error) { return map[string]string{canary: canary}, nil }},
		"typed error": {operation: "echo", payload: map[string]string{"text": canary}, handler: func(context.Context, message.Envelope) (any, error) {
			return nil, &message.Error{Code: message.CodeNotFound, Message: "none"}
		}},
		"payload not an object": {operation: "echo", payload: []string{canary}},
	}
	for name, p := range paths {
		t.Run(name, func(t *testing.T) {
			r, logs := newTestResponder(t)
			if p.handler != nil {
				r.handlers["echo"] = p.handler
			}
			_, data := requestBytes(t, p.operation, p.payload)

			reply := r.process(context.Background(), data)

			if strings.Contains(logs.String(), canary) {
				t.Fatalf("the log holds the payload value: %s", logs.String())
			}
			// The registry's invalid_payload text may name the offending property back to
			// its own sender (slice 1); every other reply must not hold the payload.
			if env := decodeReply(t, reply); env.Type == message.TypeError && env.Error.Code != message.CodeInvalidPayload && strings.Contains(string(reply), canary) {
				t.Fatalf("an error reply holds the payload value: %s", reply)
			}
		})
	}
	t.Run("saturated", func(t *testing.T) {
		r, logs := newTestResponder(t, WithMaxConcurrent(1))
		r.slots <- struct{}{} // the only slot is taken
		_, data := requestBytes(t, "echo", map[string]string{"text": canary})

		reply := r.process(context.Background(), data)

		if strings.Contains(logs.String(), canary) || strings.Contains(string(reply), canary) {
			t.Fatalf("log or reply holds the payload value: %s | %s", logs.String(), reply)
		}
	})
	t.Run("garbage bytes", func(t *testing.T) {
		r, logs := newTestResponder(t)
		for _, data := range [][]byte{
			[]byte(canary),
			[]byte(`{"id":"` + canary + `","from":"` + canary + `","operation":"` + canary + `"}`),
			[]byte(`{"schema_version":1,"extra":"` + canary + `"}`),
		} {
			reply := r.process(context.Background(), data)
			if strings.Contains(logs.String(), canary) || strings.Contains(string(reply), canary) {
				t.Fatalf("log or reply holds the garbage: %s | %s", logs.String(), reply)
			}
		}
	})
}

func TestHandleRejectsMistakes(t *testing.T) {
	r, _ := newTestResponder(t)
	tests := []struct {
		name      string
		operation string
		handler   Handler
	}{
		{"nil handler", "echo", nil},
		{"operation not in the registry", "nope", echoHandler},
		{"second handler for an operation", "echo", echoHandler},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := r.Handle(tt.operation, tt.handler); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestNewResponderValidates(t *testing.T) {
	reg := testRegistry(t)
	tests := []struct {
		name string
		app  string
		reg  bool
		opts []ResponderOption
	}{
		{"bad app name", "Bad App", true, nil},
		{"empty app name", "", true, nil},
		{"no registry", testApp, false, nil},
		{"zero concurrency", testApp, true, []ResponderOption{WithMaxConcurrent(0)}},
		{"negative timeout", testApp, true, []ResponderOption{WithRequestTimeout(-time.Second)}},
		{"zero grace", testApp, true, []ResponderOption{WithStopGrace(0)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var useReg *message.Registry
			if tt.reg {
				useReg = reg
			}
			if _, err := newResponder(tt.app, useReg, nil, tt.opts...); err == nil {
				t.Fatal("want an error")
			}
		})
	}
	if _, err := NewResponder(nil, testApp, reg, nil); err == nil {
		t.Fatal("NewResponder without a connection must fail")
	}
}

func TestErrorReplyAlwaysValid(t *testing.T) {
	r, _ := newTestResponder(t)
	long := strings.Repeat("é", 2000) // over the 1000 character limit of an error message
	for _, text := range []string{"", long} {
		reply := r.errorReply(askedBy("bad", "Bad", "Bad", nil), message.CodeNotFound, text)
		env := decodeReply(t, reply)
		if env.Error.Code != message.CodeInternal || env.Error.Message != internalMessage {
			t.Fatalf("want the internal error, got %+v", env.Error)
		}
	}
}

func TestSalvageAndRetryableCode(t *testing.T) {
	// A wrong-typed field spoils the decode but not the fields before and after it.
	got := salvage([]byte(`{"id":"abc","from":5,"operation":"y"}`))
	if got.CorrelationID != "abc" || got.Operation != "y" || got.From != "" {
		t.Fatalf("salvage = %+v", got)
	}
	if got := salvage([]byte(`{"id":"abc"`)); got.CorrelationID != "" {
		t.Fatalf("salvage of broken JSON = %+v, want nothing", got)
	}
	var codes = map[string]bool{
		message.CodeUnavailable: true, message.CodeTimeout: true,
		message.CodeInvalidMessage: false, message.CodeUnknownOperation: false, message.CodeInvalidPayload: false,
		message.CodeNotFound: false, message.CodeTooLarge: false, message.CodeInternal: false,
	}
	for code, want := range codes {
		if retryableCode(code) != want {
			t.Errorf("retryableCode(%s) = %v, want %v", code, !want, want)
		}
	}
	if s := cut(strings.Repeat("a", 10), 4); s != "aaaa..." {
		t.Fatalf("cut = %q", s)
	}
	if s := cut("abc", 4); s != "abc" {
		t.Fatalf("cut = %q", s)
	}
}
