package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	sharedllm "k8s-lab/shared/llm"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
)

// fakeOllama is a local stand-in for an Ollama server: it records every request
// and answers with whatever the test scripted, as the NDJSON stream /api/chat
// returns.
type fakeOllama struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recorded
	handle   func(w http.ResponseWriter, r *http.Request)
}

type recorded struct {
	Path   string
	Auth   string
	Fields map[string]any
}

func newFake(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *fakeOllama {
	t.Helper()
	f := &fakeOllama{handle: handle}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var fields map[string]any
		_ = json.Unmarshal(body, &fields)
		f.mu.Lock()
		f.requests = append(f.requests, recorded{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Fields: fields})
		f.mu.Unlock()
		f.handle(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeOllama) last(t *testing.T) recorded {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("the fake Ollama received no request")
	}
	return f.requests[len(f.requests)-1]
}

// stream writes NDJSON chunks, flushing after each.
func stream(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, _ := w.(http.Flusher)
	for _, c := range chunks {
		fmt.Fprintln(w, c)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func chunk(content string) string {
	b, _ := json.Marshal(map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "done": false})
	return string(b)
}

func done(promptTokens, replyTokens int) string {
	b, _ := json.Marshal(map[string]any{"message": map[string]any{"role": "assistant", "content": ""}, "done": true, "prompt_eval_count": promptTokens, "eval_count": replyTokens})
	return string(b)
}

func complete(t *testing.T, p *Provider, baseURL string, opts modelcfg.Options, req router.Request) (router.Response, error) {
	t.Helper()
	return p.Complete(context.Background(),
		modelcfg.Provider{ID: "box", Type: "ollama", BaseURL: baseURL},
		modelcfg.Model{ID: "m", Provider: "box", Name: "gemma4:12b", Options: opts}, req)
}

func failureOf(t *testing.T, err error) *router.Failure {
	t.Helper()
	var f *router.Failure
	if !errors.As(err, &f) {
		t.Fatalf("want *router.Failure, got %T: %v", err, err)
	}
	return f
}

// ── the request ──────────────────────────────────────────────────────────────

func TestRequestShape(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, chunk("hi"), done(7, 2)) })
	schema := map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object", "required": []any{"a"}}
	_, err := complete(t, New(), f.URL, modelcfg.Options{NumCtx: 8192, NumPredict: 512, Think: false}, router.Request{System: "SYS", Input: `{"x":1}`, Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	got := f.last(t)
	if got.Path != "/api/chat" || got.Auth != "" {
		t.Errorf("path %q, authorization %q (Ollama takes none)", got.Path, got.Auth)
	}
	fields := got.Fields
	if fields["model"] != "gemma4:12b" || fields["stream"] != true || fields["think"] != false {
		t.Errorf("model/stream/think = %v %v %v", fields["model"], fields["stream"], fields["think"])
	}
	msgs, _ := fields["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", fields["messages"])
	}
	system, user := msgs[0].(map[string]any), msgs[1].(map[string]any)
	if system["role"] != "system" || system["content"] != "SYS" || user["role"] != "user" || user["content"] != `{"x":1}` {
		t.Errorf("messages = %v", msgs)
	}
	options, _ := fields["options"].(map[string]any)
	if options["num_ctx"] != float64(8192) || options["num_predict"] != float64(512) {
		t.Errorf("options = %v", options)
	}
	format, _ := fields["format"].(map[string]any)
	if format["type"] != "object" || format["required"] == nil {
		t.Errorf("format = %v", fields["format"])
	}
	if _, has := format["$schema"]; has {
		t.Errorf("the $schema declaration is left out of format")
	}
}

func TestOptionsAndFormatAreLeftOutWhenNotGiven(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, done(1, 1)) })
	if _, err := complete(t, New(), f.URL, modelcfg.Options{}, router.Request{System: "s", Input: "i"}); err != nil {
		t.Fatal(err)
	}
	fields := f.last(t).Fields
	if _, has := fields["format"]; has {
		t.Errorf("no schema, no format: %v", fields["format"])
	}
	if _, has := fields["options"]; has {
		t.Errorf("no options, none sent: %v", fields["options"])
	}
	if fields["think"] != false {
		t.Errorf("think defaults to off: %v", fields["think"])
	}
}

func TestThinkIsPassedOn(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, done(1, 1)) })
	complete(t, New(), f.URL, modelcfg.Options{Think: true}, router.Request{})
	if f.last(t).Fields["think"] != true {
		t.Errorf("think = %v", f.last(t).Fields["think"])
	}
}

func TestATrailingSlashInTheBaseURLIsFine(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, done(1, 1)) })
	if _, err := complete(t, New(), f.URL+"/", modelcfg.Options{}, router.Request{}); err != nil {
		t.Fatal(err)
	}
	if f.last(t).Path != "/api/chat" {
		t.Errorf("path = %q", f.last(t).Path)
	}
}

func TestAnUnencodableSchemaIsAClientFailureAndNothingIsSent(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, done(1, 1)) })
	_, err := complete(t, New(), f.URL, modelcfg.Options{}, router.Request{Schema: map[string]any{"bad": func() {}}})
	if k := failureOf(t, err).Kind; k != router.FailClient {
		t.Errorf("kind = %s", k)
	}
	f.mu.Lock()
	n := len(f.requests)
	f.mu.Unlock()
	if n != 0 {
		t.Errorf("a request that cannot be built must not be sent")
	}
}

// ── the answer ───────────────────────────────────────────────────────────────

func TestTheTextIsTheChunksJoinedAndTheTokensAreOllamasCounts(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		stream(w, chunk(`{"state_`), chunk(`patch": {}, `), chunk(`"action": {}}`), done(839, 83))
	})
	resp, err := complete(t, New(), f.URL, modelcfg.Options{}, router.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"state_patch": {}, "action": {}}` || resp.InputTokens != 839 || resp.OutputTokens != 83 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestThinkingChunksAreNotPartOfTheText(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		stream(w, `{"message":{"role":"assistant","content":"","thinking":"hmm"},"done":false}`, chunk("answer"), done(1, 1))
	})
	resp, err := complete(t, New(), f.URL, modelcfg.Options{Think: true}, router.Request{})
	if err != nil || resp.Text != "answer" {
		t.Errorf("%+v %v", resp, err)
	}
}

func TestNonASCIIText(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		stream(w, chunk("اكتب "), chunk("بالعربية — 你好"), done(1, 1))
	})
	resp, _ := complete(t, New(), f.URL, modelcfg.Options{}, router.Request{})
	if resp.Text != "اكتب بالعربية — 你好" {
		t.Errorf("text = %q", resp.Text)
	}
}

// ── failures ─────────────────────────────────────────────────────────────────

func TestStatusRepliesAreClassified(t *testing.T) {
	tests := []struct {
		status int
		kind   router.FailureKind
	}{
		{429, router.FailRateLimited},
		{500, router.FailServer},
		{502, router.FailServer},
		{503, router.FailServer},
		{400, router.FailClient},
		{401, router.FailClient},
		{404, router.FailClient},
		{413, router.FailClient},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"error":"model 'gemma4:12b' not found"}`)
			})
			_, err := complete(t, New(), f.URL, modelcfg.Options{}, router.Request{})
			fl := failureOf(t, err)
			if fl.Kind != tc.kind {
				t.Errorf("kind = %s, want %s", fl.Kind, tc.kind)
			}
			if fl.HTTPStatus != tc.status || fl.RequestID != "" {
				t.Errorf("HTTPStatus = %d, RequestID = %q", fl.HTTPStatus, fl.RequestID)
			}
			if !strings.Contains(err.Error(), "not found") {
				t.Errorf("the provider's own message must reach the error text: %v", err)
			}
		})
	}
}

func TestAStreamThatCarriesAnErrorOrEndsEarlyIsAServerFailure(t *testing.T) {
	for name, chunks := range map[string][]string{
		"error chunk": {chunk("par"), `{"error":"out of memory"}`},
		"ends early":  {chunk("par")},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, chunks...) })
			_, err := complete(t, New(), f.URL, modelcfg.Options{}, router.Request{})
			if k := failureOf(t, err).Kind; k != router.FailServer {
				t.Errorf("kind = %s: %v", k, err)
			}
		})
	}
}

func TestNoFirstTokenInTimeIsATimeout(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	p := New(WithTimeoutUnit(30 * time.Millisecond))
	start := time.Now()
	_, err := complete(t, p, f.URL, modelcfg.Options{FirstTokenTimeoutSeconds: 2}, router.Request{})
	fl := failureOf(t, err)
	if fl.Kind != router.FailTimeout || !strings.Contains(err.Error(), "no output") {
		t.Errorf("failure = %v", fl)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("the 2-unit limit (60 ms) took %v", time.Since(start))
	}
}

func TestAStallAfterTheFirstTokenIsATimeout(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		stream(w, chunk("partial"))
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	p := New(WithTimeoutUnit(30 * time.Millisecond))
	_, err := complete(t, p, f.URL, modelcfg.Options{FirstTokenTimeoutSeconds: 100, IdleTimeoutSeconds: 2}, router.Request{})
	fl := failureOf(t, err)
	if fl.Kind != router.FailTimeout || !strings.Contains(err.Error(), "stalled") {
		t.Errorf("failure = %v", fl)
	}
}

func TestAnOverallTimeoutEvenWhileTokensFlow(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 200; i++ {
			stream(w, chunk("x"))
			select {
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	})
	p := New(WithTimeoutUnit(40 * time.Millisecond))
	_, err := complete(t, p, f.URL, modelcfg.Options{OverallTimeoutSeconds: 3, IdleTimeoutSeconds: 100, FirstTokenTimeoutSeconds: 100}, router.Request{})
	fl := failureOf(t, err)
	if fl.Kind != router.FailTimeout || !strings.Contains(err.Error(), "overall timeout") {
		t.Errorf("failure = %v", fl)
	}
}

func TestACallersCancellationIsCanceledNotATimeout(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	_, err := New().Complete(ctx, modelcfg.Provider{BaseURL: f.URL}, modelcfg.Model{Name: "m"}, router.Request{})
	if k := failureOf(t, err).Kind; k != router.FailCanceled {
		t.Errorf("kind = %s: %v", k, err)
	}
}

func TestACallersDeadlineIsAlsoNotTheModelsFault(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := New().Complete(ctx, modelcfg.Provider{BaseURL: f.URL}, modelcfg.Model{Name: "m"}, router.Request{})
	if k := failureOf(t, err).Kind; k != router.FailCanceled {
		t.Errorf("kind = %s: %v", k, err)
	}
}

func TestAMachineThatRefusesTheConnectionIsAConnectionFailure(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + l.Addr().String()
	l.Close() // nothing listens there now
	_, err = complete(t, New(), url, modelcfg.Options{}, router.Request{})
	fl := failureOf(t, err)
	if fl.Kind != router.FailConnection {
		t.Errorf("kind = %s: %v", fl.Kind, err)
	}
}

func TestClassify(t *testing.T) {
	timeout := &sharedllm.TimeoutError{Limit: sharedllm.LimitOverall, Model: "m", After: time.Second}
	tests := []struct {
		name string
		err  error
		kind router.FailureKind
	}{
		{"first token", &sharedllm.TimeoutError{Limit: sharedllm.LimitFirstToken}, router.FailTimeout},
		{"wrapped timeout", fmt.Errorf("call: %w", timeout), router.FailTimeout},
		{"canceled", context.Canceled, router.FailCanceled},
		{"deadline", context.DeadlineExceeded, router.FailCanceled},
		{"wrapped canceled", fmt.Errorf("x: %w", context.Canceled), router.FailCanceled},
		{"429", &sharedllm.StatusError{StatusCode: 429, Status: "429 Too Many Requests"}, router.FailRateLimited},
		{"500", &sharedllm.StatusError{StatusCode: 500}, router.FailServer},
		{"404", &sharedllm.StatusError{StatusCode: 404}, router.FailClient},
		{"unexpected EOF", io.ErrUnexpectedEOF, router.FailServer},
		{"dns", &net.DNSError{Err: "no such host", Name: "gpu-box.lan", IsNotFound: true}, router.FailConnection},
		{"dial refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, router.FailConnection},
		{"dial no route", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EHOSTUNREACH}, router.FailConnection},
		{"dial timed out", &net.OpError{Op: "dial", Net: "tcp", Err: timeoutErr{}}, router.FailConnection},
		{"reset while reading", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, router.FailServer},
		{"something else", errors.New("odd"), router.FailServer},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.err).Kind; got != tc.kind {
				t.Errorf("kind = %s, want %s", got, tc.kind)
			}
		})
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// ── with the router ──────────────────────────────────────────────────────────

func TestTheRouterFallsBackFromADeadMachineToALiveOneThroughRealHTTP(t *testing.T) {
	live := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, chunk("from the live one"), done(11, 4)) })
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + l.Addr().String()
	l.Close()

	cfg, err := modelcfg.LoadBytes("m.yaml", []byte(fmt.Sprintf(`providers:
  - {id: remote, type: ollama, base_url: %q, api_key_from: ""}
  - {id: local, type: ollama, base_url: %q, api_key_from: ""}
models:
  - {id: remote-big, provider: remote, model: gemma4:12b, tags: [chat], priority: 20}
  - {id: remote-small, provider: remote, model: gemma4:e4b, tags: [chat], priority: 15}
  - {id: local-big, provider: local, model: gemma4:12b, tags: [chat], priority: 10}
`, dead, live.URL)))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := router.NewRouter(cfg, map[string]router.Provider{"ollama": New()})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		resp, err := rt.Complete(context.Background(), router.Request{System: "s", Input: "i", Tags: []string{"chat"}})
		if err != nil || resp.Text != "from the live one" || resp.InputTokens != 11 {
			t.Fatalf("call %d: %+v %v", i, resp, err)
		}
	}
	for _, s := range rt.Status() {
		wantUp := s.Provider == "local"
		if s.Available != wantUp {
			t.Errorf("%s available = %v, want %v", s.ID, s.Available, wantUp)
		}
	}
}

func TestNoSystemPromptMeansNoSystemMessage(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, chunk("hi"), done(1, 1)) })
	if _, err := complete(t, New(), f.URL, modelcfg.Options{}, router.Request{Input: "question"}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := f.last(t).Fields["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Errorf("messages = %v", msgs)
	}
}

// ── api_key_from ──────────────────────────────────────────────────────────────

const (
	secretKey = "ollama-TOP-SECRET-KEY-VALUE"
	keyEnv    = "TEST_OLLAMA_KEY"
)

func envWith(value string) Option {
	return WithGetenv(func(name string) string {
		if name == keyEnv {
			return value
		}
		return ""
	})
}

func keyedProvider(baseURL string) modelcfg.Provider {
	return modelcfg.Provider{ID: "box", Type: "ollama", BaseURL: baseURL, APIKeyFrom: "env:" + keyEnv}
}

var testModel = modelcfg.Model{ID: "m", Provider: "box", Name: "gemma4:12b"}

func TestTheKeyOfAnApiKeyEnvIsSentAsABearerTokenAndReadOnEveryCall(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, done(1, 1)) })
	current := " " + secretKey + "\n"
	var mu sync.Mutex
	p := New(WithGetenv(func(string) string { mu.Lock(); defer mu.Unlock(); return current }))
	if _, err := p.Complete(context.Background(), keyedProvider(f.URL), testModel, router.Request{Input: "i"}); err != nil {
		t.Fatal(err)
	}
	if f.last(t).Auth != "Bearer "+secretKey {
		t.Errorf("the key must be trimmed and sent as a bearer token (value not shown)")
	}
	mu.Lock()
	current = "rotated-key"
	mu.Unlock()
	if _, err := p.Complete(context.Background(), keyedProvider(f.URL), testModel, router.Request{Input: "i"}); err != nil {
		t.Fatal(err)
	}
	if f.last(t).Auth != "Bearer rotated-key" {
		t.Errorf("the key must be read on every call")
	}
}

func TestNoApiKeyEnvMeansNoAuthorizationHeader(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, done(1, 1)) })
	prov := modelcfg.Provider{ID: "box", Type: "ollama", BaseURL: f.URL}
	if _, err := New(envWith(secretKey)).Complete(context.Background(), prov, testModel, router.Request{Input: "i"}); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t).Auth; got != "" {
		t.Errorf("a provider without api_key_from must send no Authorization header: %q", got)
	}
}

func TestAnEmptyKeyVariableIsAClientFailureNamingItAndNoRequestIsMade(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, done(1, 1)) })
	for _, value := range []string{"", "  \n"} {
		_, err := New(envWith(value)).Complete(context.Background(), keyedProvider(f.URL), testModel, router.Request{Input: "i"})
		fl := failureOf(t, err)
		if fl.Kind != router.FailClient || !strings.Contains(err.Error(), keyEnv) || !strings.Contains(err.Error(), `"box"`) {
			t.Errorf("value %q: failure = %v", value, fl)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 0 {
		t.Errorf("no request may be made without the key, got %d", len(f.requests))
	}
}

func TestTheKeyNeverAppearsInAnyError(t *testing.T) {
	for _, status := range []int{401, 429, 500} {
		f := newFake(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "denied", status) })
		_, err := New(envWith(secretKey)).Complete(context.Background(), keyedProvider(f.URL), testModel, router.Request{Input: "i"})
		if err == nil || strings.Contains(err.Error(), secretKey) {
			t.Errorf("status %d: err = %v", status, err)
		}
	}
	// A refused connection carries the address, which must not carry the key either.
	_, err := New(envWith(secretKey)).Complete(context.Background(), keyedProvider("http://127.0.0.1:1"), testModel, router.Request{Input: "i"})
	if err == nil || strings.Contains(err.Error(), secretKey) {
		t.Errorf("connection error = %v", err)
	}
}

// ── per-call overrides ───────────────────────────────────────────────────────

func boolPtr(b bool) *bool { return &b }

func TestTheRequestsMaxTokensWinsOverNumPredict(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, done(1, 1)) })
	numPredict := func() any {
		options, _ := f.last(t).Fields["options"].(map[string]any)
		return options["num_predict"]
	}
	complete(t, New(), f.URL, modelcfg.Options{NumPredict: 512}, router.Request{MaxTokens: 77})
	if numPredict() != float64(77) {
		t.Errorf("override: num_predict = %v", numPredict())
	}
	complete(t, New(), f.URL, modelcfg.Options{NumPredict: 512}, router.Request{})
	if numPredict() != float64(512) {
		t.Errorf("zero value: num_predict = %v", numPredict())
	}
	complete(t, New(), f.URL, modelcfg.Options{}, router.Request{MaxTokens: 77})
	if numPredict() != float64(77) {
		t.Errorf("override over unset option: num_predict = %v", numPredict())
	}
}

func TestTheRequestsThinkWinsOverTheModelOptionAndNilLeavesIt(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, done(1, 1)) })
	for _, tc := range []struct {
		name   string
		option bool
		think  *bool
		want   bool
	}{
		{"true over false", false, boolPtr(true), true},
		{"false over true", true, boolPtr(false), false},
		{"nil keeps true", true, nil, true},
		{"nil keeps false", false, nil, false},
	} {
		complete(t, New(), f.URL, modelcfg.Options{Think: tc.option}, router.Request{Think: tc.think})
		if got := f.last(t).Fields["think"]; got != tc.want {
			t.Errorf("%s: think = %v", tc.name, got)
		}
	}
}

func TestTheRequestsTimeoutWinsOverTheOverallTimeoutAndZeroLeavesIt(t *testing.T) {
	slow := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(300 * time.Millisecond):
			stream(w, done(1, 1))
		}
	})
	unit := WithTimeoutUnit(30 * time.Millisecond)
	long := modelcfg.Options{OverallTimeoutSeconds: 1000}

	// Shorter than the model's: times out.
	_, err := complete(t, New(unit), slow.URL, long, router.Request{Timeout: 30 * time.Millisecond})
	if fl := failureOf(t, err); fl.Kind != router.FailTimeout {
		t.Errorf("shorter override: %v", err)
	}
	// Longer than the model's: the call completes.
	if _, err := complete(t, New(unit), slow.URL, modelcfg.Options{OverallTimeoutSeconds: 1}, router.Request{Timeout: 5 * time.Second}); err != nil {
		t.Errorf("longer override: %v", err)
	}
	// Zero leaves the model's.
	_, err = complete(t, New(unit), slow.URL, modelcfg.Options{OverallTimeoutSeconds: 1}, router.Request{})
	if fl := failureOf(t, err); fl.Kind != router.FailTimeout {
		t.Errorf("zero value: %v", err)
	}
}

func TestTheRequestsNumCtxWinsOverTheModelOptionAndZeroLeavesIt(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, done(1, 1)) })
	numCtx := func() any {
		options, _ := f.last(t).Fields["options"].(map[string]any)
		return options["num_ctx"]
	}
	complete(t, New(), f.URL, modelcfg.Options{NumCtx: 32768}, router.Request{NumCtx: 8192})
	if numCtx() != float64(8192) {
		t.Errorf("override: num_ctx = %v", numCtx())
	}
	complete(t, New(), f.URL, modelcfg.Options{NumCtx: 32768}, router.Request{})
	if numCtx() != float64(32768) {
		t.Errorf("zero value: num_ctx = %v", numCtx())
	}
	complete(t, New(), f.URL, modelcfg.Options{}, router.Request{NumCtx: 8192})
	if numCtx() != float64(8192) {
		t.Errorf("override over unset option: num_ctx = %v", numCtx())
	}
}

func TestTheKeyOfAnApiKeyFileIsSentAsABearerTokenAndReReadOnEveryCall(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, done(1, 1)) })
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(" "+secretKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prov := modelcfg.Provider{ID: "box", Type: "ollama", BaseURL: f.URL, APIKeyFrom: "file:" + path}
	p := New(envWith("must-not-be-used"))
	if _, err := p.Complete(context.Background(), prov, testModel, router.Request{Input: "i"}); err != nil {
		t.Fatal(err)
	}
	if f.last(t).Auth != "Bearer "+secretKey {
		t.Errorf("the file content must be trimmed and sent as a bearer token (value not shown)")
	}
	if err := os.WriteFile(path, []byte("rotated-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), prov, testModel, router.Request{Input: "i"}); err != nil {
		t.Fatal(err)
	}
	if f.last(t).Auth != "Bearer rotated-key" {
		t.Errorf("the file must be read on every call")
	}
}

func TestAnUnreadableOrEmptyKeyFileIsAClientFailureNamingTheFileAndNoRequestIsMade(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, done(1, 1)) })
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{empty, filepath.Join(t.TempDir(), "missing")} {
		prov := modelcfg.Provider{ID: "box", Type: "ollama", BaseURL: f.URL, APIKeyFrom: "file:" + path}
		_, err := New().Complete(context.Background(), prov, testModel, router.Request{Input: "i"})
		if failureOf(t, err).Kind != router.FailClient || !strings.Contains(err.Error(), "file:"+path) {
			t.Errorf("%s: %v", path, err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 0 {
		t.Errorf("no key, no request: %d", len(f.requests))
	}
}
