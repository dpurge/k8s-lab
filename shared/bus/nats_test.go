package bus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"k8s-lab/shared/message"
)

// natsURL returns the server the real-NATS tests use, or skips the test.
func natsURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("NATS_URL")
	if !Enabled(url) {
		t.Skip("NATS_URL is not set: the NATS tests are skipped (task test-bus runs them)")
	}
	return url
}

var notAppName = regexp.MustCompile(`[^a-z0-9_-]+`)

// uniqueApp returns an app name no other test or run shares, so subjects never
// collide on a server that other tests use: the test name and a random suffix.
func uniqueApp(t *testing.T) string {
	t.Helper()
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name := notAppName.ReplaceAllString(strings.ToLower(t.Name()), "-")
	if len(name) > 40 {
		name = name[:40]
	}
	return "t-" + name + "-" + hex.EncodeToString(suffix[:])
}

// connect opens a connection that the test closes.
func connect(t *testing.T, name string) *nats.Conn {
	t.Helper()
	logger, _ := capturedLogger()
	nc, err := Connect(Config{URL: natsURL(t), Name: name, Logger: logger})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { Close(nc) })
	return nc
}

// startResponder starts a responder of app with echo handled (or the given handler).
func startResponder(t *testing.T, nc *nats.Conn, app string, h Handler, opts ...ResponderOption) *Responder {
	t.Helper()
	logger, _ := capturedLogger()
	r, err := NewResponder(nc, app, testRegistry(t), logger, opts...)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	if h == nil {
		h = echoHandler
	}
	if err := r.Handle("echo", h); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(r.Stop)
	return r
}

func newTestClient(t *testing.T, nc *nats.Conn, opts ...ClientOption) *Client {
	t.Helper()
	c, err := NewClient(nc, testRequester, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func echoPayload(text string) map[string]string { return map[string]string{"text": text} }

func TestNATSConnectAndClose(t *testing.T) {
	logger, logs := capturedLogger()
	nc, err := Connect(Config{URL: natsURL(t), Name: "bus-test-connect", Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	if nc.Opts.Name != "bus-test-connect" {
		t.Fatalf("client name = %q", nc.Opts.Name)
	}
	if nc.Opts.MaxReconnect != -1 || nc.Opts.ReconnectWait != reconnectWait || nc.Opts.Timeout != connectTimeout {
		t.Fatalf("options = reconnects %d, wait %v, timeout %v", nc.Opts.MaxReconnect, nc.Opts.ReconnectWait, nc.Opts.Timeout)
	}
	if nc.Status() != nats.CONNECTED {
		t.Fatalf("status = %v", nc.Status())
	}
	if err := Close(nc); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !nc.IsClosed() {
		t.Fatal("the connection is not closed")
	}
	if !strings.Contains(logs.String(), "bus closed") {
		t.Fatalf("the closed handler did not log: %s", logs.String())
	}
	if err := Close(nc); err != nil {
		t.Fatalf("closing twice: %v", err)
	}
}

func TestNATSRoundTrip(t *testing.T) {
	nc := connect(t, "bus-test-roundtrip")
	app := uniqueApp(t)
	var sawContext atomic.Value
	startResponder(t, nc, app, func(ctx context.Context, req message.Envelope) (any, error) {
		sawContext.Store(req.Context)
		return echoHandler(ctx, req)
	})
	client := newTestClient(t, nc, WithRegistry(testRegistry(t)))

	resp, err := client.Request(context.Background(), app, "echo", echoPayload("hello"), message.WithContext("u-1", "t-2"))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	if resp.Type != message.TypeResponse || resp.From != app || resp.To != testRequester || resp.Operation != "echo" {
		t.Fatalf("response = %+v", resp)
	}
	if string(resp.Payload) != `{"text":"hello"}` {
		t.Fatalf("payload = %s", resp.Payload)
	}
	if got, _ := sawContext.Load().(*message.Context); got == nil || got.UserID != "u-1" || got.TaskID != "t-2" {
		t.Fatalf("the handler saw context %+v", got)
	}
	if resp.Context == nil || resp.Context.TaskID != "t-2" {
		t.Fatalf("the response lost the context: %+v", resp.Context)
	}
}

func TestNATSErrorsRoundTrip(t *testing.T) {
	nc := connect(t, "bus-test-errors")
	app := uniqueApp(t)
	startResponder(t, nc, app, func(_ context.Context, req message.Envelope) (any, error) {
		var in struct{ Text string }
		_ = json.Unmarshal(req.Payload, &in)
		switch in.Text {
		case "missing":
			return nil, &message.Error{Code: message.CodeNotFound, Message: "No such thing."}
		case "panic":
			panic("secret panic text")
		}
		return nil, errors.New("secret error text")
	})
	client := newTestClient(t, nc)

	tests := []struct {
		name      string
		operation string
		payload   any
		code      string
	}{
		{"not_found from the handler", "echo", echoPayload("missing"), message.CodeNotFound},
		{"internal from a panic", "echo", echoPayload("panic"), message.CodeInternal},
		{"internal from another error", "echo", echoPayload("other"), message.CodeInternal},
		{"invalid_payload", "echo", map[string]int{"text": 1}, message.CodeInvalidPayload},
		{"unknown_operation", "other_op", echoPayload("x"), message.CodeUnknownOperation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.Request(context.Background(), app, tt.operation, tt.payload)

			coded := wantCoded(t, err, tt.code)
			if strings.Contains(coded.Message, "secret") {
				t.Fatalf("the error leaked: %s", coded.Message)
			}
			if Retryable(err) {
				t.Fatal("must not be retryable")
			}
		})
	}
}

func TestNATSTimeout(t *testing.T) {
	nc := connect(t, "bus-test-timeout")
	app := uniqueApp(t)
	release := make(chan struct{})
	startResponder(t, nc, app, func(ctx context.Context, req message.Envelope) (any, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return echoHandler(ctx, req)
	}, WithStopGrace(100*time.Millisecond))
	t.Cleanup(func() { close(release) })
	client := newTestClient(t, nc, WithTimeout(150*time.Millisecond))

	begin := time.Now()
	_, err := client.Request(context.Background(), app, "echo", echoPayload("slow"))

	wantCoded(t, err, message.CodeTimeout)
	if !Retryable(err) {
		t.Fatal("timeout must be retryable")
	}
	if elapsed := time.Since(begin); elapsed > 5*time.Second {
		t.Fatalf("took %v for a 150 ms timeout", elapsed)
	}
}

func TestNATSContextDeadlineBeatsTimeout(t *testing.T) {
	nc := connect(t, "bus-test-ctx-deadline")
	app := uniqueApp(t)
	release := make(chan struct{})
	startResponder(t, nc, app, func(ctx context.Context, req message.Envelope) (any, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return echoHandler(ctx, req)
	}, WithStopGrace(100*time.Millisecond))
	t.Cleanup(func() { close(release) })
	client := newTestClient(t, nc) // the default 30 s timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	begin := time.Now()
	_, err := client.Request(ctx, app, "echo", echoPayload("slow"))

	wantCoded(t, err, message.CodeTimeout)
	if elapsed := time.Since(begin); elapsed > 5*time.Second {
		t.Fatalf("took %v: the context deadline was ignored", elapsed)
	}
}

func TestNATSCanceledContext(t *testing.T) {
	nc := connect(t, "bus-test-canceled")
	client := newTestClient(t, nc)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Request(ctx, uniqueApp(t), "echo", echoPayload("x"))

	if err == nil {
		t.Fatal("want an error")
	}
	var coded *message.Error
	if !errors.As(err, &coded) {
		t.Fatalf("want *message.Error, got %T", err)
	}
}

func TestNATSNoResponders(t *testing.T) {
	nc := connect(t, "bus-test-noresponders")
	client := newTestClient(t, nc)

	begin := time.Now()
	_, err := client.Request(context.Background(), uniqueApp(t), "echo", echoPayload("x"))

	wantCoded(t, err, message.CodeUnavailable)
	if !Retryable(err) {
		t.Fatal("unavailable must be retryable")
	}
	if elapsed := time.Since(begin); elapsed > 5*time.Second {
		t.Fatalf("no responders took %v: it must answer at once, not wait for the timeout", elapsed)
	}
}

func TestNATSQueueGroupSharesTheWork(t *testing.T) {
	app := uniqueApp(t)
	var counts [2]atomic.Int64
	for i := range counts {
		nc := connect(t, "bus-test-queue-"+string(rune('a'+i)))
		startResponder(t, nc, app, func(ctx context.Context, req message.Envelope) (any, error) {
			counts[i].Add(1)
			return echoHandler(ctx, req)
		})
	}
	client := newTestClient(t, connect(t, "bus-test-queue-client"))

	const requests = 60
	for i := 0; i < requests; i++ {
		if _, err := client.Request(context.Background(), app, "echo", echoPayload("x")); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}

	// Each request reaches exactly one responder; with 60 requests the chance that
	// one of two responders gets none is 2^-59.
	a, b := counts[0].Load(), counts[1].Load()
	if a+b != requests || a == 0 || b == 0 {
		t.Fatalf("responders handled %d and %d of %d requests: want every request once and both responders used", a, b, requests)
	}
}

func TestNATSStopDrains(t *testing.T) {
	nc := connect(t, "bus-test-stop")
	app := uniqueApp(t)
	started := make(chan struct{})
	const handlerTime = 300 * time.Millisecond
	responder := startResponder(t, nc, app, func(ctx context.Context, req message.Envelope) (any, error) {
		close(started)
		time.Sleep(handlerTime)
		return echoHandler(ctx, req)
	})
	client := newTestClient(t, nc)
	type result struct {
		resp message.Envelope
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := client.Request(context.Background(), app, "echo", echoPayload("in flight"))
		done <- result{resp, err}
	}()
	<-started

	begin := time.Now()
	responder.Stop()
	stopTook := time.Since(begin)

	res := <-done
	if res.err != nil {
		t.Fatalf("the request in flight must be answered after Stop: %v", res.err)
	}
	if string(res.resp.Payload) != `{"text":"in flight"}` {
		t.Fatalf("payload = %s", res.resp.Payload)
	}
	if stopTook < handlerTime/2 {
		t.Fatalf("Stop returned after %v, before the handler (%v) finished", stopTook, handlerTime)
	}
	// After Stop nobody listens.
	_, err := client.Request(context.Background(), app, "echo", echoPayload("late"))
	wantCoded(t, err, message.CodeUnavailable)
	responder.Stop() // twice is fine
}

func TestNATSStopEndsHandlerContexts(t *testing.T) {
	nc := connect(t, "bus-test-stop-ctx")
	app := uniqueApp(t)
	started, ended := make(chan struct{}), make(chan struct{})
	responder := startResponder(t, nc, app, func(ctx context.Context, _ message.Envelope) (any, error) {
		close(started)
		<-ctx.Done()
		close(ended)
		return nil, ctx.Err()
	}, WithStopGrace(100*time.Millisecond))
	client := newTestClient(t, nc, WithTimeout(10*time.Second))
	go client.Request(context.Background(), app, "echo", echoPayload("x"))
	<-started

	responder.Stop()

	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler's context did not end on Stop")
	}
}

func TestNATSRequestWithoutReplySubject(t *testing.T) {
	nc := connect(t, "bus-test-noreply")
	app := uniqueApp(t)
	handled := make(chan struct{})
	logger, logs := capturedLogger()
	r, err := NewResponder(nc, app, testRegistry(t), logger)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	if err := r.Handle("echo", func(ctx context.Context, req message.Envelope) (any, error) {
		once.Do(func() { close(handled) })
		return echoHandler(ctx, req)
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Stop)
	req, err := message.NewRequest(testRequester, app, "echo", echoPayload("fire and forget"))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := message.Encode(req)

	if err := nc.Publish(RequestSubject(app), data); err != nil {
		t.Fatal(err)
	}

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not run for a message without a reply subject")
	}
	r.Stop() // waits for the request in flight, so the debug line is written
	if !strings.Contains(logs.String(), "no reply subject") {
		t.Fatalf("the discarded answer is not logged: %s", logs.String())
	}
}

func TestNATSSaturatedResponder(t *testing.T) {
	nc := connect(t, "bus-test-saturated")
	app := uniqueApp(t)
	started, release := make(chan struct{}), make(chan struct{})
	startResponder(t, nc, app, func(ctx context.Context, req message.Envelope) (any, error) {
		close(started)
		<-release
		return echoHandler(ctx, req)
	}, WithMaxConcurrent(1))
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	client := newTestClient(t, nc)
	first := make(chan error, 1)
	go func() {
		_, err := client.Request(context.Background(), app, "echo", echoPayload("first"))
		first <- err
	}()
	<-started

	_, err := client.Request(context.Background(), app, "echo", echoPayload("second"))

	wantCoded(t, err, message.CodeUnavailable)
	if !Retryable(err) {
		t.Fatal("a busy responder's error must be retryable")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("the first request: %v", err)
	}
}

func TestNATSStartTwiceAndAfterStop(t *testing.T) {
	nc := connect(t, "bus-test-start")
	app := uniqueApp(t)
	r := startResponder(t, nc, app, nil)

	if err := r.Start(); err == nil {
		t.Fatal("a second Start must fail")
	}
	r.Stop()
	if err := r.Start(); err == nil {
		t.Fatal("Start after Stop must fail")
	}
}
