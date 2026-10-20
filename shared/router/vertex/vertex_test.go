package vertex

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
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
)

const secretToken = "ya29.TOP-SECRET-TOKEN-VALUE"

type seen struct {
	Path   string
	Auth   string
	Fields map[string]any
}

// fake is a stand-in for Vertex AI that records requests and answers as told.
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
		f.mu.Lock()
		f.reqs = append(f.reqs, seen{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Fields: fields})
		status, body := f.status, f.body
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

const okBody = `{"candidates":[{"content":{"role":"model","parts":[{"text":"{\"a\":"},{"text":"1}"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":9,"thoughtsTokenCount":4}}`

func tokenFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readToken(string) ([]byte, error) { return []byte(secretToken), nil }

func provider(t *testing.T, f *fake, token string, opts ...Option) *Provider {
	t.Helper()
	opts = append(opts, WithEndpoint(func(string) string { return f.URL }), WithReadFile(func(string) ([]byte, error) { return []byte(token), nil }))
	return New(opts...)
}

var (
	prov  = modelcfg.Provider{ID: "vx", Type: "vertex", Project: "my-proj", Location: "us-central1", APIKeyFrom: "file:/run/secrets/vertex-token"}
	model = modelcfg.Model{ID: "flash", Provider: "vx", Name: "gemini-2.5-flash-lite"}
)

func failure(t *testing.T, err error) *router.Failure {
	t.Helper()
	var f *router.Failure
	if !errors.As(err, &f) {
		t.Fatalf("want *router.Failure, got %T: %v", err, err)
	}
	return f
}

func TestTheRequestGoesToTheProjectLocationAndModelWithTheBearerToken(t *testing.T) {
	f := newFake(t, 200, okBody)
	p := provider(t, f, "  "+secretToken+"\n")
	_, err := p.Complete(context.Background(), prov, modelcfg.Model{Name: "gemini-2.5-flash-lite", Options: modelcfg.Options{NumPredict: 256}}, router.Request{System: "SYS", Input: `{"x":1}`})
	if err != nil {
		t.Fatal(err)
	}
	got := f.reqs[0]
	if got.Path != "/v1/projects/my-proj/locations/us-central1/publishers/google/models/gemini-2.5-flash-lite:generateContent" {
		t.Errorf("path = %s", got.Path)
	}
	if got.Auth != "Bearer "+secretToken {
		t.Errorf("the token must be trimmed and sent as a bearer token: %q", got.Auth)
	}
	system := got.Fields["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"]
	user := got.Fields["contents"].([]any)[0].(map[string]any)
	if system != "SYS" || user["role"] != "user" || user["parts"].([]any)[0].(map[string]any)["text"] != `{"x":1}` {
		t.Errorf("messages = %v", got.Fields)
	}
	gen := got.Fields["generationConfig"].(map[string]any)
	if gen["maxOutputTokens"] != float64(256) || gen["temperature"] != float64(0) {
		t.Errorf("generationConfig = %v", gen)
	}
	if _, set := gen["responseMimeType"]; set {
		t.Errorf("no schema, no JSON mode: %v", gen)
	}
}

func TestAStepWithAReplySchemaAsksForJSON(t *testing.T) {
	f := newFake(t, 200, okBody)
	_, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, router.Request{Input: "i", Schema: map[string]any{"type": "object"}})
	if err != nil {
		t.Fatal(err)
	}
	gen := f.reqs[0].Fields["generationConfig"].(map[string]any)
	if gen["responseMimeType"] != "application/json" {
		t.Errorf("generationConfig = %v", gen)
	}
	if _, has := gen["responseSchema"]; has {
		t.Errorf("the engine validates the reply itself; the schema is not sent: %v", gen)
	}
}

func TestNoSystemPromptMeansNoSystemInstruction(t *testing.T) {
	f := newFake(t, 200, okBody)
	provider(t, f, secretToken).Complete(context.Background(), prov, model, router.Request{Input: "i"})
	if _, has := f.reqs[0].Fields["systemInstruction"]; has {
		t.Errorf("fields = %v", f.reqs[0].Fields)
	}
}

func TestTheReplyTextAndTokensAreReturnedThinkingCountsAsOutput(t *testing.T) {
	f := newFake(t, 200, okBody)
	resp, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, router.Request{Input: "i"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"a":1}` || resp.InputTokens != 120 || resp.OutputTokens != 13 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestTheGlobalLocationHasItsOwnAddress(t *testing.T) {
	if defaultEndpoint("global") != "https://aiplatform.googleapis.com" || defaultEndpoint("europe-west4") != "https://europe-west4-aiplatform.googleapis.com" {
		t.Error("addresses")
	}
}

func TestEachHTTPStatusIsClassifiedForTheBreaker(t *testing.T) {
	cases := []struct {
		status int
		kind   router.FailureKind
	}{
		{429, router.FailRateLimited}, {500, router.FailServer}, {503, router.FailServer},
		{400, router.FailClient}, {404, router.FailClient}, {401, router.FailClient}, {403, router.FailClient},
	}
	for _, c := range cases {
		f := newFake(t, c.status, `{"error":{"code":1,"message":"the message","status":"SOME_STATUS"}}`)
		_, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, router.Request{Input: "i"})
		got := failure(t, err)
		if got.HTTPStatus != c.status || got.RequestID != "" {
			t.Errorf("%d: HTTPStatus = %d, RequestID = %q", c.status, got.HTTPStatus, got.RequestID)
		}
		if got.Kind != c.kind || !strings.Contains(err.Error(), "SOME_STATUS: the message") {
			t.Errorf("%d: %v / %v", c.status, got.Kind, err)
		}
	}
}

func TestAnExpiredTokenSaysHowToRefreshIt(t *testing.T) {
	f := newFake(t, 401, `{"error":{"message":"invalid authentication credentials","status":"UNAUTHENTICATED"}}`)
	_, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, router.Request{Input: "i"})
	if !strings.Contains(err.Error(), "refresh-assistant-vertex-token") || !strings.Contains(err.Error(), "UNAUTHENTICATED") {
		t.Errorf("err = %v", err)
	}
}

func TestAnErrorBodyThatIsNotJSONIsKeptShortAndTheTokenNeverAppears(t *testing.T) {
	f := newFake(t, 502, strings.Repeat("<html>bad gateway</html>", 100))
	_, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, router.Request{Input: "i"})
	if got := failure(t, err); got.Kind != router.FailServer || len(err.Error()) > 600 {
		t.Errorf("%v len %d", got.Kind, len(err.Error()))
	}
	if strings.Contains(err.Error(), secretToken) {
		t.Errorf("the token must never be in an error: %v", err)
	}
}

func TestNoCandidatesIsAClientErrorWithTheBlockReason(t *testing.T) {
	f := newFake(t, 200, `{"promptFeedback":{"blockReason":"SAFETY"}}`)
	_, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, router.Request{Input: "i"})
	if failure(t, err).Kind != router.FailClient || !strings.Contains(err.Error(), "SAFETY") {
		t.Errorf("err = %v", err)
	}
	f2 := newFake(t, 200, `{}`)
	_, err = provider(t, f2, secretToken).Complete(context.Background(), prov, model, router.Request{Input: "i"})
	if !strings.Contains(err.Error(), "no candidates") {
		t.Errorf("err = %v", err)
	}
}

func TestAReplyThatIsNotJSONIsAServerFault(t *testing.T) {
	f := newFake(t, 200, "not json")
	_, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, router.Request{Input: "i"})
	if failure(t, err).Kind != router.FailServer {
		t.Errorf("err = %v", err)
	}
}

func TestTheTokenFileIsReadOnEveryCallSoARefreshNeedsNoRestart(t *testing.T) {
	f := newFake(t, 200, okBody)
	path := tokenFile(t, "first")
	fromFile := modelcfg.Provider{ID: "vx", Type: "vertex", Project: "my-proj", Location: "us-central1", APIKeyFrom: "file:" + path}
	p := New(WithEndpoint(func(string) string { return f.URL }))
	p.Complete(context.Background(), fromFile, model, router.Request{Input: "i"})
	os.WriteFile(path, []byte("second\n"), 0o600)
	p.Complete(context.Background(), fromFile, model, router.Request{Input: "i"})
	if f.reqs[0].Auth != "Bearer first" || f.reqs[1].Auth != "Bearer second" {
		t.Errorf("auth = %q, %q", f.reqs[0].Auth, f.reqs[1].Auth)
	}
}

func TestTheTokenCanComeFromAnEnvironmentVariableAndIsReadOnEveryCall(t *testing.T) {
	f := newFake(t, 200, okBody)
	env := map[string]string{"VX_TOKEN": "  " + secretToken + " "}
	fromEnv := modelcfg.Provider{ID: "vx", Type: "vertex", Project: "my-proj", Location: "us-central1", APIKeyFrom: "env:VX_TOKEN"}
	p := New(WithEndpoint(func(string) string { return f.URL }), WithGetenv(func(n string) string { return env[n] }))
	p.Complete(context.Background(), fromEnv, model, router.Request{Input: "i"})
	env["VX_TOKEN"] = "rotated"
	p.Complete(context.Background(), fromEnv, model, router.Request{Input: "i"})
	if f.reqs[0].Auth != "Bearer "+secretToken || f.reqs[1].Auth != "Bearer rotated" {
		t.Errorf("auth = %q, %q", f.reqs[0].Auth, f.reqs[1].Auth)
	}
}

func TestAMissingOrEmptyTokenIsAClearClientErrorNamingTheSourceAndNoRequestIsMade(t *testing.T) {
	f := newFake(t, 200, okBody)
	newP := func() *Provider {
		return New(WithEndpoint(func(string) string { return f.URL }), WithGetenv(func(string) string { return "" }))
	}
	missingPath := filepath.Join(t.TempDir(), "nope")
	cases := map[string]struct {
		prov modelcfg.Provider
		want string
	}{
		"missing file": {modelcfg.Provider{ID: "vx", APIKeyFrom: "file:" + missingPath}, "file:" + missingPath},
		"empty file":   {modelcfg.Provider{ID: "vx", APIKeyFrom: "file:" + tokenFile(t, " \n")}, "file:"},
		"unset env":    {modelcfg.Provider{ID: "vx", APIKeyFrom: "env:VX_UNSET"}, "env:VX_UNSET"},
		"none":         {modelcfg.Provider{ID: "vx"}, "api_key_from"},
	}
	for name, c := range cases {
		c.prov.Type, c.prov.Project, c.prov.Location = "vertex", "p", "l"
		_, err := newP().Complete(context.Background(), c.prov, model, router.Request{Input: "i"})
		if failure(t, err).Kind != router.FailClient || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "refresh-assistant-vertex-token") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.reqs) != 0 {
		t.Errorf("no token, no request: %d", len(f.reqs))
	}
}

func TestAnUnreachableAddressIsAConnectionFailure(t *testing.T) {
	f := newFake(t, 200, okBody)
	url := f.URL
	f.Close()
	p := provider(t, f, secretToken, WithEndpoint(func(string) string { return url }))
	_, err := p.Complete(context.Background(), prov, model, router.Request{Input: "i"})
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
	p := New(WithEndpoint(func(string) string { return slow.URL }), WithTimeoutUnit(20*time.Millisecond), WithReadFile(readToken))
	_, err := p.Complete(context.Background(), prov, modelcfg.Model{Name: "m", Options: modelcfg.Options{OverallTimeoutSeconds: 1}}, router.Request{Input: "i"})
	if failure(t, err).Kind != router.FailTimeout || !strings.Contains(err.Error(), "no answer within") {
		t.Errorf("timeout: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	p2 := New(WithEndpoint(func(string) string { return slow.URL }), WithReadFile(readToken))
	_, err = p2.Complete(ctx, prov, model, router.Request{Input: "i"})
	if failure(t, err).Kind != router.FailCanceled {
		t.Errorf("cancel: %v", err)
	}
}

func TestTheRequestsMaxTokensWinsOverNumPredictAndZeroLeavesIt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		numPredict int
		maxTokens  int
		want       any
	}{
		{"override wins", 256, 77, float64(77)},
		{"zero keeps the option", 256, 0, float64(256)},
		{"override without an option", 0, 77, float64(77)},
		{"neither", 0, 0, nil},
	} {
		f := newFake(t, 200, okBody)
		m := model
		m.Options = modelcfg.Options{NumPredict: tc.numPredict}
		if _, err := provider(t, f, secretToken).Complete(context.Background(), prov, m, router.Request{Input: "i", MaxTokens: tc.maxTokens}); err != nil {
			t.Fatal(err)
		}
		gen := f.reqs[0].Fields["generationConfig"].(map[string]any)
		if got := gen["maxOutputTokens"]; got != tc.want {
			t.Errorf("%s: maxOutputTokens = %v", tc.name, got)
		}
	}
}

func TestTheRequestsThinkIsIgnored(t *testing.T) {
	f := newFake(t, 200, okBody)
	on := true
	if _, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, router.Request{Input: "i", Think: &on}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprint(f.reqs[0].Fields), "think") {
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
	p := New(WithEndpoint(func(string) string { return slow.URL }), WithTimeoutUnit(30*time.Millisecond), WithReadFile(readToken))
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

func TestOllamaOnlyOptionsAreIgnored(t *testing.T) {
	f := newFake(t, 200, okBody)
	think := true
	opts := modelcfg.Options{NumCtx: 4096, Think: true, FirstTokenTimeoutSeconds: 1, IdleTimeoutSeconds: 1}
	_, err := provider(t, f, secretToken).Complete(context.Background(), prov, modelcfg.Model{Name: "m", Options: opts}, router.Request{Input: "i", NumCtx: 8192, Think: &think})
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
	_, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, router.Request{Input: "i"})
	if !utf8.ValidString(err.Error()) || !strings.Contains(err.Error(), "…") {
		t.Errorf("the cut error is not valid UTF-8 with an ellipsis: %q", err.Error())
	}
}
