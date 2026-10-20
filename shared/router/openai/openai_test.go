package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	secretKey = "sk-TOP-SECRET-KEY-VALUE"
	keyEnv    = "TEST_API_KEY"
)

type seen struct {
	Method      string
	Path        string
	Auth        string
	HasAuth     bool
	ContentType string
	Fields      map[string]any
}

// fake is a stand-in for an OpenAI-compatible API that records requests and
// answers as told.
type fake struct {
	*httptest.Server
	mu     sync.Mutex
	reqs   []seen
	status int
	body   string
}

func newFake(t *testing.T, status int, body string) *fake {
	t.Helper()
	f := &fake{status: status, body: body}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		_, hasAuth := r.Header["Authorization"]
		f.mu.Lock()
		f.reqs = append(f.reqs, seen{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), HasAuth: hasAuth, ContentType: r.Header.Get("Content-Type"), Fields: fields})
		status, body := f.status, f.body
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "req_test_1")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

const okBody = `{"choices":[{"message":{"role":"assistant","content":"{\"a\":1}"}}],"usage":{"prompt_tokens":120,"completion_tokens":9}}`

func envWith(value string) Option {
	return WithGetenv(func(name string) string {
		if name == keyEnv {
			return value
		}
		return ""
	})
}

func keyed(f *fake) modelcfg.Provider {
	return modelcfg.Provider{ID: "or", Type: "openai", BaseURL: f.URL, APIKeyFrom: "env:" + keyEnv}
}

var model = modelcfg.Model{ID: "gpt", Provider: "or", Name: "vendor/some-model"}

func failure(t *testing.T, err error) *router.Failure {
	t.Helper()
	var f *router.Failure
	if !errors.As(err, &f) {
		t.Fatalf("want *router.Failure, got %T: %v", err, err)
	}
	return f
}

func TestTheRequestIsAChatCompletionWithTheBearerKey(t *testing.T) {
	f := newFake(t, 200, okBody)
	_, err := New(envWith(" "+secretKey+"\n")).Complete(context.Background(), keyed(f), model, router.Request{System: "SYS", Input: `{"x":1}`})
	if err != nil {
		t.Fatal(err)
	}
	got := f.reqs[0]
	if got.Method != http.MethodPost || got.Path != "/chat/completions" || got.ContentType != "application/json" {
		t.Errorf("request = %+v", got)
	}
	if got.Auth != "Bearer "+secretKey {
		t.Errorf("the key must be trimmed and sent as a bearer token (value not shown)")
	}
	if got.Fields["model"] != "vendor/some-model" || got.Fields["stream"] != false {
		t.Errorf("fields = %v", got.Fields)
	}
	msgs := got.Fields["messages"].([]any)
	sys, user := msgs[0].(map[string]any), msgs[1].(map[string]any)
	if len(msgs) != 2 || sys["role"] != "system" || sys["content"] != "SYS" || user["role"] != "user" || user["content"] != `{"x":1}` {
		t.Errorf("messages = %v", msgs)
	}
	for _, field := range []string{"response_format", "max_tokens"} {
		if _, set := got.Fields[field]; set {
			t.Errorf("%s must be left out when unset: %v", field, got.Fields)
		}
	}
}

func TestNoApiKeyEnvMeansNoAuthorizationHeader(t *testing.T) {
	f := newFake(t, 200, okBody)
	prov := modelcfg.Provider{ID: "local", Type: "openai", BaseURL: f.URL}
	if _, err := New(envWith(secretKey)).Complete(context.Background(), prov, model, router.Request{Input: "i"}); err != nil {
		t.Fatal(err)
	}
	if f.reqs[0].HasAuth {
		t.Error("a provider without api_key_from must send no Authorization header")
	}
}

func TestATrailingSlashInTheBaseURLIsTrimmed(t *testing.T) {
	f := newFake(t, 200, okBody)
	prov := keyed(f)
	prov.BaseURL += "/v1//"
	if _, err := New(envWith(secretKey)).Complete(context.Background(), prov, model, router.Request{Input: "i"}); err != nil {
		t.Fatal(err)
	}
	if f.reqs[0].Path != "/v1/chat/completions" {
		t.Errorf("path = %s", f.reqs[0].Path)
	}
}

func TestAReplySchemaIsSentAsAStrictJSONSchema(t *testing.T) {
	f := newFake(t, 200, okBody)
	schema := map[string]any{"type": "object"}
	if _, err := New(envWith(secretKey)).Complete(context.Background(), keyed(f), model, router.Request{Input: "i", Schema: schema}); err != nil {
		t.Fatal(err)
	}
	rf := f.reqs[0].Fields["response_format"].(map[string]any)
	js := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["name"] != "reply" || js["strict"] != true || js["schema"].(map[string]any)["type"] != "object" {
		t.Errorf("response_format = %v", rf)
	}
}

func TestNumPredictBecomesMaxTokens(t *testing.T) {
	f := newFake(t, 200, okBody)
	m := model
	m.Options = modelcfg.Options{NumPredict: 256, NumCtx: 4096, Think: true}
	if _, err := New(envWith(secretKey)).Complete(context.Background(), keyed(f), m, router.Request{Input: "i"}); err != nil {
		t.Fatal(err)
	}
	fields := f.reqs[0].Fields
	if fields["max_tokens"] != float64(256) {
		t.Errorf("fields = %v", fields)
	}
	for _, ollamaOnly := range []string{"num_ctx", "think", "options"} {
		if _, set := fields[ollamaOnly]; set {
			t.Errorf("%s is Ollama-only and must not be sent", ollamaOnly)
		}
	}
}

func TestTheReplyTextAndTokensAreReturned(t *testing.T) {
	f := newFake(t, 200, okBody)
	resp, err := New(envWith(secretKey)).Complete(context.Background(), keyed(f), model, router.Request{Input: "i"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"a":1}` || resp.InputTokens != 120 || resp.OutputTokens != 9 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestAReplyWithoutUsageSucceedsWithZeroTokens(t *testing.T) {
	f := newFake(t, 200, `{"choices":[{"message":{"content":"hi"}}]}`)
	resp, err := New(envWith(secretKey)).Complete(context.Background(), keyed(f), model, router.Request{Input: "i"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hi" || resp.InputTokens != 0 || resp.OutputTokens != 0 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestNoChoicesIsAServerFailure(t *testing.T) {
	f := newFake(t, 200, `{"choices":[]}`)
	_, err := New(envWith(secretKey)).Complete(context.Background(), keyed(f), model, router.Request{Input: "i"})
	if failure(t, err).Kind != router.FailServer || !strings.Contains(err.Error(), "no choices") {
		t.Errorf("err = %v", err)
	}
}

func TestAReplyThatIsNotJSONIsAServerFault(t *testing.T) {
	f := newFake(t, 200, "not json")
	_, err := New(envWith(secretKey)).Complete(context.Background(), keyed(f), model, router.Request{Input: "i"})
	if failure(t, err).Kind != router.FailServer {
		t.Errorf("err = %v", err)
	}
}

func TestEachHTTPStatusIsClassifiedForTheBreaker(t *testing.T) {
	cases := []struct {
		status int
		kind   router.FailureKind
	}{
		{429, router.FailRateLimited}, {500, router.FailServer}, {503, router.FailServer},
		{400, router.FailClient}, {401, router.FailClient}, {403, router.FailClient}, {404, router.FailClient},
		{301, router.FailClient},
	}
	for _, c := range cases {
		f := newFake(t, c.status, `{"error":{"message":"the message","code":"x"}}`)
		_, err := New(envWith(secretKey)).Complete(context.Background(), keyed(f), model, router.Request{Input: "i"})
		got := failure(t, err)
		if got.HTTPStatus != c.status || got.RequestID != "req_test_1" {
			t.Errorf("%d: HTTPStatus = %d, RequestID = %q", c.status, got.HTTPStatus, got.RequestID)
		}
		if got.Kind != c.kind || !strings.Contains(err.Error(), "the message") || !strings.Contains(err.Error(), "HTTP "+strconv.Itoa(c.status)) {
			t.Errorf("%d: %v / %v", c.status, got.Kind, err)
		}
	}
}

func TestAnErrorBodyThatIsNotJSONIsKeptShort(t *testing.T) {
	f := newFake(t, 502, strings.Repeat("<html>bad gateway</html>", 100))
	_, err := New(envWith(secretKey)).Complete(context.Background(), keyed(f), model, router.Request{Input: "i"})
	if got := failure(t, err); got.Kind != router.FailServer || len(err.Error()) > 600 {
		t.Errorf("%v len %d", got.Kind, len(err.Error()))
	}
}

func TestTheKeyAndTheRequestNeverAppearInAnyError(t *testing.T) {
	statuses := []int{401, 429, 500}
	for _, status := range statuses {
		f := newFake(t, status, `{"error":{"message":"nope"}}`)
		_, err := New(envWith(secretKey)).Complete(context.Background(), keyed(f), model, router.Request{System: "PROMPT-TEXT", Input: "INPUT-TEXT"})
		assertClean(t, err)
	}
	f := newFake(t, 200, "not json")
	_, err := New(envWith(secretKey)).Complete(context.Background(), keyed(f), model, router.Request{Input: "INPUT-TEXT"})
	assertClean(t, err)

	prov := keyed(f)
	f.Close()
	_, err = New(envWith(secretKey)).Complete(context.Background(), prov, model, router.Request{Input: "INPUT-TEXT"})
	assertClean(t, err)

	_, err = New(envWith("")).Complete(context.Background(), prov, model, router.Request{Input: "INPUT-TEXT"})
	assertClean(t, err)
}

func assertClean(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), secretKey) || strings.Contains(err.Error(), "PROMPT-TEXT") || strings.Contains(err.Error(), "INPUT-TEXT") {
		t.Errorf("an error holds the key or the request (error text withheld)")
	}
}

func TestAMissingKeyVariableIsAClientErrorNamingItAndNoRequestIsMade(t *testing.T) {
	f := newFake(t, 200, okBody)
	for _, value := range []string{"", "  \n"} {
		_, err := New(envWith(value)).Complete(context.Background(), keyed(f), model, router.Request{Input: "i"})
		if failure(t, err).Kind != router.FailClient || !strings.Contains(err.Error(), keyEnv) || !strings.Contains(err.Error(), `"or"`) {
			t.Errorf("err = %v", err)
		}
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
	p.Complete(context.Background(), keyed(f), model, router.Request{Input: "i"})
	mu.Lock()
	current = "second"
	mu.Unlock()
	p.Complete(context.Background(), keyed(f), model, router.Request{Input: "i"})
	if f.reqs[0].Auth != "Bearer first" || f.reqs[1].Auth != "Bearer second" {
		t.Errorf("auth = %q, %q", f.reqs[0].Auth, f.reqs[1].Auth)
	}
}

func TestAnUnreachableAddressIsAConnectionFailure(t *testing.T) {
	f := newFake(t, 200, okBody)
	prov := keyed(f)
	f.Close()
	_, err := New(envWith(secretKey)).Complete(context.Background(), prov, model, router.Request{Input: "i"})
	if failure(t, err).Kind != router.FailConnection {
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
	prov := modelcfg.Provider{ID: "or", Type: "openai", BaseURL: slow.URL}

	p := New(WithTimeoutUnit(20 * time.Millisecond))
	_, err := p.Complete(context.Background(), prov, modelcfg.Model{Name: "m", Options: modelcfg.Options{OverallTimeoutSeconds: 1}}, router.Request{Input: "i"})
	if failure(t, err).Kind != router.FailTimeout || !strings.Contains(err.Error(), "no answer within") {
		t.Errorf("timeout: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	_, err = New().Complete(ctx, prov, model, router.Request{Input: "i"})
	if failure(t, err).Kind != router.FailCanceled {
		t.Errorf("cancel: %v", err)
	}
}

const twoModels = `providers:
  - id: or
    type: openai
    base_url: %s
    api_key_from: env:TEST_API_KEY
  - id: backup
    type: ollama
    base_url: http://localhost:11434
    api_key_from: ""
models:
  - id: remote
    provider: or
    model: vendor/some-model
    tags: [chat]
    priority: 20
  - id: spare
    provider: backup
    model: small
    tags: [chat]
    priority: 10
`

func TestARouterFallsBackFromAFailingOpenAIModel(t *testing.T) {
	f := newFake(t, 500, `{"error":{"message":"overloaded"}}`)
	cfg, err := modelcfg.LoadBytes("m.yaml", []byte(strings.Replace(twoModels, "%s", f.URL, 1)))
	if err != nil {
		t.Fatal(err)
	}
	scripted := routertest.NewProvider(map[string][]routertest.Result{"spare": {routertest.OK("from the spare", 1, 2)}})
	r, err := router.NewRouter(cfg, map[string]router.Provider{"openai": New(envWith(secretKey)), "ollama": scripted})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := r.Complete(context.Background(), router.Request{Input: "i", Tags: []string{"chat"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "from the spare" || resp.ModelID != "spare" {
		t.Errorf("resp = %+v", resp)
	}
	if len(f.reqs) != 1 || f.reqs[0].Path != "/chat/completions" {
		t.Errorf("the openai model must be tried first, once: %+v", f.reqs)
	}
}

func TestTheRequestsMaxTokensWinsOverNumPredictAndZeroLeavesIt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		numPredict int
		maxTokens  int
		want       any
	}{
		{"override wins", 512, 77, float64(77)},
		{"zero keeps the option", 512, 0, float64(512)},
		{"override without an option", 0, 77, float64(77)},
		{"neither", 0, 0, nil},
	} {
		f := newFake(t, 200, okBody)
		m := model
		m.Options = modelcfg.Options{NumPredict: tc.numPredict}
		if _, err := New(envWith(secretKey)).Complete(context.Background(), keyed(f), m, router.Request{Input: "i", MaxTokens: tc.maxTokens}); err != nil {
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
	if _, err := New(envWith(secretKey)).Complete(context.Background(), keyed(f), model, router.Request{Input: "i", Think: &on}); err != nil {
		t.Fatal(err)
	}
	if _, set := f.reqs[0].Fields["think"]; set {
		t.Errorf("think must not be sent: %v", f.reqs[0].Fields)
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
	prov := modelcfg.Provider{ID: "or", Type: "openai", BaseURL: slow.URL}
	p := New(WithTimeoutUnit(30 * time.Millisecond))
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

func TestTheKeyOfAnApiKeyFileIsSentAsABearerTokenAndReReadOnEveryCall(t *testing.T) {
	f := newFake(t, 200, okBody)
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(" "+secretKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prov := modelcfg.Provider{ID: "or", Type: "openai", BaseURL: f.URL, APIKeyFrom: "file:" + path}
	p := New(envWith("must-not-be-used"))
	if _, err := p.Complete(context.Background(), prov, model, router.Request{Input: "i"}); err != nil {
		t.Fatal(err)
	}
	if f.reqs[0].Auth != "Bearer "+secretKey {
		t.Errorf("the file content must be trimmed and sent as a bearer token (value not shown)")
	}
	if err := os.WriteFile(path, []byte("rotated-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), prov, model, router.Request{Input: "i"}); err != nil {
		t.Fatal(err)
	}
	if f.reqs[1].Auth != "Bearer rotated-key" {
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
		prov := modelcfg.Provider{ID: "or", Type: "openai", BaseURL: f.URL, APIKeyFrom: "file:" + path}
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
	body := fmt.Sprint(f.reqs[0].Fields)
	for _, name := range []string{"num_ctx", "think", "4096", "8192"} {
		if strings.Contains(body, name) {
			t.Errorf("the request must not carry the ollama-only option %s: %s", name, body)
		}
	}
}

func TestACutErrorBodyStaysValidUTF8(t *testing.T) {
	// 3-byte runes: the cut at maxErrorBytes (400) falls inside one.
	f := newFake(t, 502, strings.Repeat("€", 1000))
	_, err := New(envWith(secretKey)).Complete(context.Background(), keyed(f), model, router.Request{Input: "i"})
	if !utf8.ValidString(err.Error()) || !strings.Contains(err.Error(), "…") {
		t.Errorf("the cut error is not valid UTF-8 with an ellipsis: %q", err.Error())
	}
}
