package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"
)

const (
	secretKey = "sk-ant-TOP-SECRET-KEY-VALUE"
	keyEnv    = "TEST_ANTHROPIC_KEY"
)

type seen struct {
	Method  string
	Path    string
	Header  http.Header
	Fields  map[string]any
	RawBody string
}

// fake is a stand-in for the Messages API that records requests and answers as
// told.
type fake struct {
	*httptest.Server
	mu        sync.Mutex
	reqs      []seen
	status    int
	body      string
	requestID string
}

func newFake(t *testing.T, status int, body string) *fake {
	t.Helper()
	f := &fake{status: status, body: body}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		f.mu.Lock()
		f.reqs = append(f.reqs, seen{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Fields: fields, RawBody: string(raw)})
		status, body, id := f.status, f.body, f.requestID
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if id != "" {
			w.Header().Set("request-id", id)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

const okBody = `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"{\"a\":"},{"type":"text","text":"1}"}],"stop_reason":"end_turn","usage":{"input_tokens":120,"output_tokens":9}}`

func envWith(value string) Option {
	return WithGetenv(func(name string) string {
		if name == keyEnv {
			return value
		}
		return ""
	})
}

func keyed(f *fake) modelcfg.Provider {
	return modelcfg.Provider{ID: "claude", Type: "anthropic", BaseURL: f.URL, APIKeyFrom: "env:" + keyEnv}
}

var model = modelcfg.Model{ID: "opus", Provider: "claude", Name: "claude-some-model"}

func failure(t *testing.T, err error) *router.Failure {
	t.Helper()
	var f *router.Failure
	if !errors.As(err, &f) {
		t.Fatalf("want *router.Failure, got %T: %v", err, err)
	}
	return f
}

func complete(t *testing.T, f *fake, m modelcfg.Model, req router.Request) (router.Response, error) {
	t.Helper()
	return New(envWith(secretKey)).Complete(context.Background(), keyed(f), m, req)
}

func TestTheRequestIsAMessageWithTheThreeHeaders(t *testing.T) {
	f := newFake(t, 200, okBody)
	prov := keyed(f)
	prov.BaseURL += "//"
	_, err := New(envWith(" "+secretKey+"\n")).Complete(context.Background(), prov, model, router.Request{System: "SYS", Input: `{"x":1}`})
	if err != nil {
		t.Fatal(err)
	}
	got := f.reqs[0]
	if got.Method != http.MethodPost || got.Path != "/v1/messages" {
		t.Errorf("request = %s %s", got.Method, got.Path)
	}
	if got.Header.Get("x-api-key") != secretKey {
		t.Errorf("the key must be trimmed and sent in x-api-key (value not shown)")
	}
	if got.Header.Get("anthropic-version") != "2023-06-01" || got.Header.Get("Content-Type") != "application/json" {
		t.Errorf("headers = %v", got.Header)
	}
	if _, set := got.Header["Authorization"]; set {
		t.Error("no Authorization header is sent")
	}
	for name := range got.Header {
		if strings.HasPrefix(strings.ToLower(name), "anthropic-beta") {
			t.Errorf("no beta headers: %s", name)
		}
	}
	if got.Fields["model"] != "claude-some-model" || got.Fields["system"] != "SYS" || got.Fields["max_tokens"] != float64(16000) {
		t.Errorf("fields = %v", got.Fields)
	}
	msgs := got.Fields["messages"].([]any)
	user := msgs[0].(map[string]any)
	if len(msgs) != 1 || user["role"] != "user" || user["content"] != `{"x":1}` {
		t.Errorf("messages = %v", msgs)
	}
}

func TestNumPredictBecomesMaxTokensAndOllamaOptionsAreNotSent(t *testing.T) {
	f := newFake(t, 200, okBody)
	m := model
	m.Options = modelcfg.Options{NumPredict: 256, NumCtx: 4096, Think: true}
	if _, err := complete(t, f, m, router.Request{Input: "i"}); err != nil {
		t.Fatal(err)
	}
	fields := f.reqs[0].Fields
	if fields["max_tokens"] != float64(256) {
		t.Errorf("fields = %v", fields)
	}
	for _, ollamaOnly := range []string{"num_ctx", "think", "options", "thinking"} {
		if _, set := fields[ollamaOnly]; set {
			t.Errorf("%s must not be sent", ollamaOnly)
		}
	}
}

func TestSystemIsOmittedWhenEmptyAndNoSamplingOrToolKeysAreEverSent(t *testing.T) {
	f := newFake(t, 200, okBody)
	schema := map[string]any{"type": "object"}
	for _, req := range []router.Request{{Input: "i"}, {Input: "i", Schema: schema}} {
		if _, err := complete(t, f, model, req); err != nil {
			t.Fatal(err)
		}
	}
	for i, got := range f.reqs {
		if _, set := got.Fields["system"]; set {
			t.Errorf("request %d: system must be omitted when empty", i)
		}
		for _, banned := range []string{"temperature", "top_p", "top_k", "thinking", "tool_choice", "tools"} {
			if _, set := got.Fields[banned]; set {
				t.Errorf("request %d: %s must never be sent", i, banned)
			}
		}
	}
}

func TestOutputConfigIsSentOnlyWithASchema(t *testing.T) {
	f := newFake(t, 200, okBody)
	if _, err := complete(t, f, model, router.Request{Input: "i"}); err != nil {
		t.Fatal(err)
	}
	if _, set := f.reqs[0].Fields["output_config"]; set {
		t.Errorf("output_config without a schema: %v", f.reqs[0].Fields)
	}

	schema := map[string]any{"type": "object", "additionalProperties": false}
	if _, err := complete(t, f, model, router.Request{Input: "i", Schema: schema}); err != nil {
		t.Fatal(err)
	}
	oc := f.reqs[1].Fields["output_config"].(map[string]any)
	format := oc["format"].(map[string]any)
	sent := format["schema"].(map[string]any)
	if len(oc) != 1 || format["type"] != "json_schema" || sent["type"] != "object" || sent["additionalProperties"] != false {
		t.Errorf("output_config = %v", oc)
	}
}

func TestTheDefaultBaseURLIsUsedWhenTheProviderHasNone(t *testing.T) {
	f := newFake(t, 200, okBody)
	prov := keyed(f)
	prov.BaseURL = ""
	if _, err := New(envWith(secretKey), WithBaseURL(f.URL)).Complete(context.Background(), prov, model, router.Request{Input: "i"}); err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 1 || f.reqs[0].Path != "/v1/messages" {
		t.Errorf("reqs = %+v", f.reqs)
	}
	if DefaultBaseURL != "https://api.anthropic.com" || New().baseURL != DefaultBaseURL {
		t.Errorf("default base URL = %q", New().baseURL)
	}
}

func TestTextBlocksAreConcatenatedAndThinkingIsIgnored(t *testing.T) {
	f := newFake(t, 200, `{"content":[{"type":"thinking","thinking":"hmm","text":"NOT THIS"},{"type":"text","text":"a"},{"type":"tool_use","name":"x"},{"type":"text","text":"b"}],"stop_reason":"end_turn","usage":{"input_tokens":120,"output_tokens":9}}`)
	resp, err := complete(t, f, model, router.Request{Input: "i"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "ab" || resp.InputTokens != 120 || resp.OutputTokens != 9 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestAReplyWithoutUsageSucceedsWithZeroTokens(t *testing.T) {
	f := newFake(t, 200, `{"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn"}`)
	resp, err := complete(t, f, model, router.Request{Input: "i"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hi" || resp.InputTokens != 0 || resp.OutputTokens != 0 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestMaxTokensStopIsStillASuccess(t *testing.T) {
	f := newFake(t, 200, `{"content":[{"type":"text","text":"{\"cut"}],"stop_reason":"max_tokens","usage":{"input_tokens":1,"output_tokens":2}}`)
	resp, err := complete(t, f, model, router.Request{Input: "i"})
	if err != nil || resp.Text != `{"cut` {
		t.Errorf("resp = %+v, err = %v", resp, err)
	}
}

func TestARefusalIsAClientFailureNamingTheCategory(t *testing.T) {
	f := newFake(t, 200, `{"content":[],"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber"}}`)
	f.requestID = "req_refused"
	_, err := complete(t, f, model, router.Request{Input: "i"})
	got := failure(t, err)
	if got.Kind != router.FailClient || !strings.Contains(err.Error(), "cyber") || got.RequestID != "req_refused" || got.HTTPStatus != 200 {
		t.Errorf("err = %v (%+v)", err, got)
	}

	f2 := newFake(t, 200, `{"content":[{"type":"text","text":"partial"}],"stop_reason":"refusal"}`)
	_, err = complete(t, f2, model, router.Request{Input: "i"})
	if got := failure(t, err); got.Kind != router.FailClient || strings.Contains(err.Error(), "category") {
		t.Errorf("a refusal without a category: %v", err)
	}
}

func TestNoTextBlockIsAServerFailure(t *testing.T) {
	f := newFake(t, 200, `{"content":[{"type":"thinking","thinking":"hmm"}],"stop_reason":"end_turn"}`)
	_, err := complete(t, f, model, router.Request{Input: "i"})
	if failure(t, err).Kind != router.FailServer || !strings.Contains(err.Error(), "no text block") {
		t.Errorf("err = %v", err)
	}
}

func TestAReplyThatIsNotJSONIsAServerFault(t *testing.T) {
	f := newFake(t, 200, "not json")
	_, err := complete(t, f, model, router.Request{Input: "i"})
	if failure(t, err).Kind != router.FailServer {
		t.Errorf("err = %v", err)
	}
}

func errorBody(errType, message, requestID string) string {
	b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": errType, "message": message}, "request_id": requestID})
	return string(b)
}

func TestEachHTTPStatusIsClassifiedForTheBreaker(t *testing.T) {
	cases := []struct {
		status int
		kind   router.FailureKind
	}{
		{429, router.FailRateLimited}, {529, router.FailRateLimited},
		{500, router.FailServer}, {503, router.FailServer},
		{400, router.FailClient}, {401, router.FailClient}, {402, router.FailClient}, {403, router.FailClient},
		{404, router.FailClient}, {413, router.FailClient},
	}
	for _, c := range cases {
		f := newFake(t, c.status, errorBody("some_error", "the message", "req_body"))
		_, err := complete(t, f, model, router.Request{Input: "i"})
		got := failure(t, err)
		if got.Kind != c.kind || got.HTTPStatus != c.status || got.RequestID != "req_body" {
			t.Errorf("%d: %+v", c.status, got)
		}
		for _, want := range []string{"some_error", "the message", "HTTP " + strconv.Itoa(c.status), "request-id req_body"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%d: %q missing from %v", c.status, want, err)
			}
		}
	}
}

func TestTheRequestIDComesFromTheHeaderOrTheBody(t *testing.T) {
	f := newFake(t, 529, errorBody("overloaded_error", "Overloaded", "req_body"))
	f.requestID = "req_header"
	_, err := complete(t, f, model, router.Request{Input: "i"})
	if got := failure(t, err); got.RequestID != "req_header" {
		t.Errorf("the header wins: %+v", got)
	}

	f = newFake(t, 529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	f.requestID = "req_only_header"
	_, err = complete(t, f, model, router.Request{Input: "i"})
	if got := failure(t, err); got.RequestID != "req_only_header" || got.HTTPStatus != 529 || !strings.Contains(err.Error(), "(HTTP 529, request-id req_only_header)") {
		t.Errorf("err = %v", err)
	}

	f = newFake(t, 400, errorBody("invalid_request_error", "bad", "req_body"))
	_, err = complete(t, f, model, router.Request{Input: "i"})
	if got := failure(t, err); got.RequestID != "req_body" {
		t.Errorf("the body id is used without a header: %+v", got)
	}
}

func TestARejectedSchemaSurfacesAnthropicsMessage(t *testing.T) {
	f := newFake(t, 400, errorBody("invalid_request_error", "output_config.format.schema: recursion is not supported", "req_s"))
	_, err := complete(t, f, model, router.Request{Input: "i", Schema: map[string]any{"type": "object"}})
	if failure(t, err).Kind != router.FailClient || !strings.Contains(err.Error(), "recursion is not supported") {
		t.Errorf("err = %v", err)
	}
}

func TestAnErrorBodyThatIsNotAnAPIErrorIsKeptShort(t *testing.T) {
	f := newFake(t, 502, strings.Repeat("<html>bad gateway</html>", 100))
	_, err := complete(t, f, model, router.Request{Input: "i"})
	if got := failure(t, err); got.Kind != router.FailServer || got.HTTPStatus != 502 || len(err.Error()) > 600 {
		t.Errorf("%v len %d", got.Kind, len(err.Error()))
	}
}

func TestALongErrorMessageIsCut(t *testing.T) {
	f := newFake(t, 400, errorBody("invalid_request_error", strings.Repeat("x", 5000), "req_1"))
	_, err := complete(t, f, model, router.Request{Input: "i"})
	if len(err.Error()) > 600 {
		t.Errorf("len %d", len(err.Error()))
	}
}

func TestTheKeyAndTheRequestNeverAppearInAnyError(t *testing.T) {
	req := router.Request{System: "PROMPT-TEXT", Input: "INPUT-TEXT"}
	for _, status := range []int{400, 401, 429, 500, 529} {
		f := newFake(t, status, errorBody("e", "nope", "req_1"))
		_, err := complete(t, f, model, req)
		assertClean(t, err)
	}
	f := newFake(t, 200, "not json")
	_, err := complete(t, f, model, req)
	assertClean(t, err)

	f = newFake(t, 200, `{"content":[],"stop_reason":"refusal"}`)
	_, err = complete(t, f, model, req)
	assertClean(t, err)

	prov := keyed(f)
	f.Close()
	_, err = New(envWith(secretKey)).Complete(context.Background(), prov, model, req)
	assertClean(t, err)

	_, err = New(envWith("")).Complete(context.Background(), prov, model, req)
	assertClean(t, err)
}

func assertClean(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error")
	}
	for _, secret := range []string{secretKey, "PROMPT-TEXT", "INPUT-TEXT"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("an error holds the key or the request (error text withheld)")
		}
	}
}

func TestAMissingKeyVariableIsAClientErrorNamingItAndNoRequestIsMade(t *testing.T) {
	f := newFake(t, 200, okBody)
	for _, value := range []string{"", "  \n"} {
		_, err := New(envWith(value)).Complete(context.Background(), keyed(f), model, router.Request{Input: "i"})
		if failure(t, err).Kind != router.FailClient || !strings.Contains(err.Error(), keyEnv) || !strings.Contains(err.Error(), `"claude"`) {
			t.Errorf("err = %v", err)
		}
	}
	prov := keyed(f)
	prov.APIKeyFrom = ""
	_, err := New(envWith(secretKey)).Complete(context.Background(), prov, model, router.Request{Input: "i"})
	if failure(t, err).Kind != router.FailClient {
		t.Errorf("a provider without api_key_from: %v", err)
	}
	if len(f.reqs) != 0 {
		t.Errorf("no key, no request: %d", len(f.reqs))
	}
}

func TestTheKeyIsReadOnEveryCall(t *testing.T) {
	f := newFake(t, 200, okBody)
	var mu sync.Mutex
	current := "first"
	p := New(WithGetenv(func(string) string { mu.Lock(); defer mu.Unlock(); return current }))
	_, _ = p.Complete(context.Background(), keyed(f), model, router.Request{Input: "i"})
	mu.Lock()
	current = "second"
	mu.Unlock()
	_, _ = p.Complete(context.Background(), keyed(f), model, router.Request{Input: "i"})
	if f.reqs[0].Header.Get("x-api-key") != "first" || f.reqs[1].Header.Get("x-api-key") != "second" {
		t.Errorf("x-api-key = %q, %q", f.reqs[0].Header.Get("x-api-key"), f.reqs[1].Header.Get("x-api-key"))
	}
}

func TestAnUnreachableAddressIsAConnectionFailure(t *testing.T) {
	f := newFake(t, 200, okBody)
	prov := keyed(f)
	f.Close()
	_, err := New(envWith(secretKey)).Complete(context.Background(), prov, model, router.Request{Input: "i"})
	if got := failure(t, err); got.Kind != router.FailConnection || got.HTTPStatus != 0 {
		t.Errorf("err = %v", err)
	}
}

func TestASlowAnswerIsATimeoutAndACancelledCallIsNot(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer slow.Close()
	defer close(release)
	prov := modelcfg.Provider{ID: "claude", Type: "anthropic", BaseURL: slow.URL, APIKeyFrom: "env:" + keyEnv}

	p := New(envWith(secretKey), WithTimeoutUnit(20*time.Millisecond))
	_, err := p.Complete(context.Background(), prov, modelcfg.Model{Name: "m", Options: modelcfg.Options{OverallTimeoutSeconds: 1}}, router.Request{Input: "i"})
	if failure(t, err).Kind != router.FailTimeout || !strings.Contains(err.Error(), "no answer within") {
		t.Errorf("timeout: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	_, err = New(envWith(secretKey)).Complete(ctx, prov, model, router.Request{Input: "i"})
	if failure(t, err).Kind != router.FailCanceled {
		t.Errorf("cancel: %v", err)
	}
}

const twoModels = `providers:
  - id: claude
    type: anthropic
    base_url: %s
    api_key_from: env:TEST_ANTHROPIC_KEY
  - id: backup
    type: ollama
    base_url: http://localhost:11434
    api_key_from: ""
models:
  - id: remote
    provider: claude
    model: claude-some-model
    tags: [chat]
    priority: 20
  - id: spare
    provider: backup
    model: small
    tags: [chat]
    priority: 10
`

func newRouter(t *testing.T, f *fake, scripted router.Provider, opts ...router.Option) *router.Router {
	t.Helper()
	cfg, err := modelcfg.LoadBytes("m.yaml", []byte(strings.Replace(twoModels, "%s", f.URL, 1)))
	if err != nil {
		t.Fatal(err)
	}
	r, err := router.NewRouter(cfg, map[string]router.Provider{"anthropic": New(envWith(secretKey)), "ollama": scripted}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestARouterFallsBackFromAnOverloadedAnthropicModel(t *testing.T) {
	f := newFake(t, 529, errorBody("overloaded_error", "Overloaded", "req_529"))
	scripted := routertest.NewProvider(map[string][]routertest.Result{"spare": {routertest.OK("from the spare", 1, 2)}})
	observer := &routertest.Observer{}
	r := newRouter(t, f, scripted, router.WithObserver(observer))
	resp, err := r.Complete(context.Background(), router.Request{Input: "i", Tags: []string{"chat"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "from the spare" || resp.ModelID != "spare" {
		t.Errorf("resp = %+v", resp)
	}
	if len(f.reqs) != 1 || f.reqs[0].Path != "/v1/messages" {
		t.Errorf("the anthropic model must be tried first, once: %+v", f.reqs)
	}
	attempts := observer.Attempts()
	if len(attempts) != 2 || attempts[0].Outcome != router.OutcomeRateLimited || !strings.Contains(attempts[0].Error, "HTTP 529") || !strings.Contains(attempts[0].Error, "req_529") {
		t.Errorf("attempts = %+v", attempts)
	}
}

func TestTheAttemptsFailureCarriesTheStatusAndRequestID(t *testing.T) {
	f := newFake(t, 529, errorBody("overloaded_error", "Overloaded", "req_529"))
	scripted := routertest.NewProvider(map[string][]routertest.Result{"spare": {routertest.Fail(router.FailServer, "down")}})
	r := newRouter(t, f, scripted)
	_, err := r.Complete(context.Background(), router.Request{Input: "i", Tags: []string{"chat"}})
	var all *router.AllFailedError
	if !errors.As(err, &all) {
		t.Fatalf("err = %v", err)
	}
	first := failure(t, all.Attempts[0].Err)
	if all.Attempts[0].ModelID != "remote" || first.HTTPStatus != 529 || first.RequestID != "req_529" || first.Kind != router.FailRateLimited {
		t.Errorf("first attempt = %+v", first)
	}
}

func TestARefusalFallsBackWithoutTrippingTheBreaker(t *testing.T) {
	f := newFake(t, 200, `{"content":[],"stop_reason":"refusal","stop_details":{"category":"cyber"}}`)
	scripted := routertest.NewProvider(map[string][]routertest.Result{"spare": {routertest.OK("from the spare", 1, 2)}})
	r := newRouter(t, f, scripted)
	for i := 0; i < 5; i++ {
		resp, err := r.Complete(context.Background(), router.Request{Input: "i", Tags: []string{"chat"}})
		if err != nil || resp.ModelID != "spare" {
			t.Fatalf("call %d: %+v, %v", i, resp, err)
		}
	}
	if len(f.reqs) != 5 {
		t.Errorf("a client failure must not set the model aside: %d requests", len(f.reqs))
	}
}

func TestTheRequestsMaxTokensWinsOverNumPredictAndTheDefaultStillApplies(t *testing.T) {
	for _, tc := range []struct {
		name       string
		numPredict int
		maxTokens  int
		want       float64
	}{
		{"override wins", 256, 77, 77},
		{"zero keeps the option", 256, 0, 256},
		{"override without an option", 0, 77, 77},
		{"neither gives the default", 0, 0, 16000},
	} {
		f := newFake(t, 200, okBody)
		m := model
		m.Options = modelcfg.Options{NumPredict: tc.numPredict}
		if _, err := complete(t, f, m, router.Request{Input: "i", MaxTokens: tc.maxTokens}); err != nil {
			t.Fatal(err)
		}
		if got := f.reqs[0].Fields["max_tokens"]; got != tc.want {
			t.Errorf("%s: max_tokens = %v", tc.name, got)
		}
	}
}

func TestTheRequestsThinkIsIgnored(t *testing.T) {
	f := newFake(t, 200, okBody)
	on := true
	if _, err := complete(t, f, model, router.Request{Input: "i", Think: &on}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"think", "thinking"} {
		if _, set := f.reqs[0].Fields[field]; set {
			t.Errorf("%s must not be sent: %v", field, f.reqs[0].Fields)
		}
	}
}

func TestTheRequestsTimeoutWinsOverTheOverallTimeoutAndZeroLeavesIt(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(300 * time.Millisecond):
			_, _ = io.WriteString(w, okBody)
		}
	}))
	defer slow.Close()
	prov := modelcfg.Provider{ID: "claude", Type: "anthropic", BaseURL: slow.URL, APIKeyFrom: "env:" + keyEnv}
	p := New(envWith(secretKey), WithTimeoutUnit(30*time.Millisecond))
	call := func(overallSeconds int, req router.Request) error {
		req.Input = "i"
		_, err := p.Complete(context.Background(), prov, modelcfg.Model{Name: "m", Options: modelcfg.Options{OverallTimeoutSeconds: overallSeconds}}, req)
		return err
	}
	if err := call(1000, router.Request{Timeout: 30 * time.Millisecond}); failure(t, err).Kind != router.FailTimeout {
		t.Errorf("shorter override: %v", err)
	}
	if err := call(1, router.Request{Timeout: 5 * time.Second}); err != nil {
		t.Errorf("longer override: %v", err)
	}
	if err := call(1, router.Request{}); failure(t, err).Kind != router.FailTimeout {
		t.Errorf("zero value: %v", err)
	}
}

func TestTheKeyOfAnApiKeyFileIsSentAsTheXApiKeyHeaderAndReReadOnEveryCall(t *testing.T) {
	f := newFake(t, 200, okBody)
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(" "+secretKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prov := modelcfg.Provider{ID: "claude", Type: "anthropic", BaseURL: f.URL, APIKeyFrom: "file:" + path}
	p := New(envWith("must-not-be-used"))
	if _, err := p.Complete(context.Background(), prov, model, router.Request{Input: "i"}); err != nil {
		t.Fatal(err)
	}
	if f.reqs[0].Header.Get("x-api-key") != secretKey {
		t.Errorf("the file content must be trimmed and sent as x-api-key (value not shown)")
	}
	if err := os.WriteFile(path, []byte("rotated-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), prov, model, router.Request{Input: "i"}); err != nil {
		t.Fatal(err)
	}
	if f.reqs[1].Header.Get("x-api-key") != "rotated-key" {
		t.Errorf("the file must be read on every call")
	}
}

func TestAnUnreadableOrEmptyKeyFileIsAClientErrorNamingTheFileAndNoRequestIsMade(t *testing.T) {
	f := newFake(t, 200, okBody)
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{empty, filepath.Join(t.TempDir(), "missing")} {
		prov := modelcfg.Provider{ID: "claude", Type: "anthropic", BaseURL: f.URL, APIKeyFrom: "file:" + path}
		_, err := New().Complete(context.Background(), prov, model, router.Request{Input: "i"})
		if failure(t, err).Kind != router.FailClient || !strings.Contains(err.Error(), "file:"+path) {
			t.Errorf("%s: %v", path, err)
		}
	}
	if len(f.reqs) != 0 {
		t.Errorf("no key, no request: %d", len(f.reqs))
	}
}

func TestOllamaOnlyOptionsAreIgnored(t *testing.T) {
	f := newFake(t, 200, okBody)
	think := true
	opts := modelcfg.Options{NumCtx: 4096, Think: true, FirstTokenTimeoutSeconds: 1, IdleTimeoutSeconds: 1}
	_, err := New(envWith(secretKey)).Complete(context.Background(), keyed(f), modelcfg.Model{Name: "m", Options: opts}, router.Request{Input: "i", NumCtx: 8192, Think: &think})
	if err != nil {
		t.Fatal(err)
	}
	body := f.reqs[0].RawBody
	for _, name := range []string{"num_ctx", "think", "4096", "8192"} {
		if strings.Contains(body, name) {
			t.Errorf("the request must not carry the ollama-only option %s: %s", name, body)
		}
	}
}

func TestACutErrorBodyStaysValidUTF8(t *testing.T) {
	// 3-byte runes: the cut at maxErrorBytes (400) falls inside one.
	f := newFake(t, 502, strings.Repeat("€", 1000))
	_, err := complete(t, f, model, router.Request{Input: "i"})
	if !utf8.ValidString(err.Error()) || !strings.Contains(err.Error(), "…") {
		t.Errorf("the cut error is not valid UTF-8 with an ellipsis: %q", err.Error())
	}
}
