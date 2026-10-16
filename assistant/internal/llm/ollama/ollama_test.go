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
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	sharedllm "k8s-lab/shared/llm"

	"assistant/internal/llm"
	"assistant/internal/models"
	"assistant/internal/workflow"
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

func complete(t *testing.T, p *Provider, baseURL string, opts models.Options, req workflow.Request) (workflow.Response, error) {
	t.Helper()
	return p.Complete(context.Background(),
		models.Provider{ID: "box", Type: "ollama", BaseURL: baseURL},
		models.Model{ID: "m", Provider: "box", Name: "gemma4:12b", Options: opts}, req)
}

func failureOf(t *testing.T, err error) *llm.Failure {
	t.Helper()
	var f *llm.Failure
	if !errors.As(err, &f) {
		t.Fatalf("want *llm.Failure, got %T: %v", err, err)
	}
	return f
}

// ── the request ──────────────────────────────────────────────────────────────

func TestRequestShape(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, chunk("hi"), done(7, 2)) })
	schema := map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object", "required": []any{"a"}}
	_, err := complete(t, New(), f.URL, models.Options{NumCtx: 8192, NumPredict: 512, Think: false}, workflow.Request{System: "SYS", Input: `{"x":1}`, Schema: schema})
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
	if _, err := complete(t, New(), f.URL, models.Options{}, workflow.Request{System: "s", Input: "i"}); err != nil {
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
	complete(t, New(), f.URL, models.Options{Think: true}, workflow.Request{})
	if f.last(t).Fields["think"] != true {
		t.Errorf("think = %v", f.last(t).Fields["think"])
	}
}

func TestATrailingSlashInTheBaseURLIsFine(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, done(1, 1)) })
	if _, err := complete(t, New(), f.URL+"/", models.Options{}, workflow.Request{}); err != nil {
		t.Fatal(err)
	}
	if f.last(t).Path != "/api/chat" {
		t.Errorf("path = %q", f.last(t).Path)
	}
}

func TestAnUnencodableSchemaIsAClientFailureAndNothingIsSent(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, done(1, 1)) })
	_, err := complete(t, New(), f.URL, models.Options{}, workflow.Request{Schema: map[string]any{"bad": func() {}}})
	if k := failureOf(t, err).Kind; k != llm.FailClient {
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
	resp, err := complete(t, New(), f.URL, models.Options{}, workflow.Request{})
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
	resp, err := complete(t, New(), f.URL, models.Options{Think: true}, workflow.Request{})
	if err != nil || resp.Text != "answer" {
		t.Errorf("%+v %v", resp, err)
	}
}

func TestNonASCIIText(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		stream(w, chunk("اكتب "), chunk("بالعربية — 你好"), done(1, 1))
	})
	resp, _ := complete(t, New(), f.URL, models.Options{}, workflow.Request{})
	if resp.Text != "اكتب بالعربية — 你好" {
		t.Errorf("text = %q", resp.Text)
	}
}

// ── failures ─────────────────────────────────────────────────────────────────

func TestStatusRepliesAreClassified(t *testing.T) {
	tests := []struct {
		status int
		kind   llm.FailureKind
	}{
		{429, llm.FailRateLimited},
		{500, llm.FailServer},
		{502, llm.FailServer},
		{503, llm.FailServer},
		{400, llm.FailClient},
		{401, llm.FailClient},
		{404, llm.FailClient},
		{413, llm.FailClient},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"error":"model 'gemma4:12b' not found"}`)
			})
			_, err := complete(t, New(), f.URL, models.Options{}, workflow.Request{})
			fl := failureOf(t, err)
			if fl.Kind != tc.kind {
				t.Errorf("kind = %s, want %s", fl.Kind, tc.kind)
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
			_, err := complete(t, New(), f.URL, models.Options{}, workflow.Request{})
			if k := failureOf(t, err).Kind; k != llm.FailServer {
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
	_, err := complete(t, p, f.URL, models.Options{FirstTokenTimeoutSeconds: 2}, workflow.Request{})
	fl := failureOf(t, err)
	if fl.Kind != llm.FailTimeout || !strings.Contains(err.Error(), "no output") {
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
	_, err := complete(t, p, f.URL, models.Options{FirstTokenTimeoutSeconds: 100, IdleTimeoutSeconds: 2}, workflow.Request{})
	fl := failureOf(t, err)
	if fl.Kind != llm.FailTimeout || !strings.Contains(err.Error(), "stalled") {
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
	_, err := complete(t, p, f.URL, models.Options{OverallTimeoutSeconds: 3, IdleTimeoutSeconds: 100, FirstTokenTimeoutSeconds: 100}, workflow.Request{})
	fl := failureOf(t, err)
	if fl.Kind != llm.FailTimeout || !strings.Contains(err.Error(), "overall timeout") {
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
	_, err := New().Complete(ctx, models.Provider{BaseURL: f.URL}, models.Model{Name: "m"}, workflow.Request{})
	if k := failureOf(t, err).Kind; k != llm.FailCanceled {
		t.Errorf("kind = %s: %v", k, err)
	}
}

func TestACallersDeadlineIsAlsoNotTheModelsFault(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := New().Complete(ctx, models.Provider{BaseURL: f.URL}, models.Model{Name: "m"}, workflow.Request{})
	if k := failureOf(t, err).Kind; k != llm.FailCanceled {
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
	_, err = complete(t, New(), url, models.Options{}, workflow.Request{})
	fl := failureOf(t, err)
	if fl.Kind != llm.FailConnection {
		t.Errorf("kind = %s: %v", fl.Kind, err)
	}
}

func TestClassify(t *testing.T) {
	timeout := &sharedllm.TimeoutError{Limit: sharedllm.LimitOverall, Model: "m", After: time.Second}
	tests := []struct {
		name string
		err  error
		kind llm.FailureKind
	}{
		{"first token", &sharedllm.TimeoutError{Limit: sharedllm.LimitFirstToken}, llm.FailTimeout},
		{"wrapped timeout", fmt.Errorf("call: %w", timeout), llm.FailTimeout},
		{"canceled", context.Canceled, llm.FailCanceled},
		{"deadline", context.DeadlineExceeded, llm.FailCanceled},
		{"wrapped canceled", fmt.Errorf("x: %w", context.Canceled), llm.FailCanceled},
		{"429", &sharedllm.StatusError{StatusCode: 429, Status: "429 Too Many Requests"}, llm.FailRateLimited},
		{"500", &sharedllm.StatusError{StatusCode: 500}, llm.FailServer},
		{"404", &sharedllm.StatusError{StatusCode: 404}, llm.FailClient},
		{"unexpected EOF", io.ErrUnexpectedEOF, llm.FailServer},
		{"dns", &net.DNSError{Err: "no such host", Name: "gpu-box.lan", IsNotFound: true}, llm.FailConnection},
		{"dial refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, llm.FailConnection},
		{"dial no route", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EHOSTUNREACH}, llm.FailConnection},
		{"dial timed out", &net.OpError{Op: "dial", Net: "tcp", Err: timeoutErr{}}, llm.FailConnection},
		{"reset while reading", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, llm.FailServer},
		{"something else", errors.New("odd"), llm.FailServer},
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

	cfg, err := models.LoadBytes("m.yaml", []byte(fmt.Sprintf(`providers:
  - {id: remote, type: ollama, base_url: %q}
  - {id: local, type: ollama, base_url: %q}
models:
  - {id: remote-big, provider: remote, model: gemma4:12b, tags: [chat], priority: 20}
  - {id: remote-small, provider: remote, model: gemma4:e4b, tags: [chat], priority: 15}
  - {id: local-big, provider: local, model: gemma4:12b, tags: [chat], priority: 10}
`, dead, live.URL)))
	if err != nil {
		t.Fatal(err)
	}
	router, err := llm.NewRouter(cfg, map[string]llm.Provider{"ollama": New()})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		resp, err := router.Complete(context.Background(), workflow.Request{System: "s", Input: "i", Tags: []string{"chat"}})
		if err != nil || resp.Text != "from the live one" || resp.InputTokens != 11 {
			t.Fatalf("call %d: %+v %v", i, resp, err)
		}
	}
	for _, s := range router.Status() {
		wantUp := s.Provider == "local"
		if s.Available != wantUp {
			t.Errorf("%s available = %v, want %v", s.ID, s.Available, wantUp)
		}
	}
}

func TestNoSystemPromptMeansNoSystemMessage(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, chunk("hi"), done(1, 1)) })
	if _, err := complete(t, New(), f.URL, models.Options{}, workflow.Request{Input: "question"}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := f.last(t).Fields["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Errorf("messages = %v", msgs)
	}
}
