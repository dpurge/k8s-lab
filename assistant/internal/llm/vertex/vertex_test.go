package vertex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"assistant/internal/llm"
	"assistant/internal/models"
	"assistant/internal/workflow"
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

func provider(t *testing.T, f *fake, token string, opts ...Option) *Provider {
	t.Helper()
	opts = append(opts, WithEndpoint(func(string) string { return f.URL }))
	return New(tokenFile(t, token), opts...)
}

var (
	prov  = models.Provider{ID: "vx", Type: "vertex", Project: "my-proj", Location: "us-central1"}
	model = models.Model{ID: "flash", Provider: "vx", Name: "gemini-2.5-flash-lite"}
)

func failure(t *testing.T, err error) *llm.Failure {
	t.Helper()
	var f *llm.Failure
	if !errors.As(err, &f) {
		t.Fatalf("want *llm.Failure, got %T: %v", err, err)
	}
	return f
}

func TestTheRequestGoesToTheProjectLocationAndModelWithTheBearerToken(t *testing.T) {
	f := newFake(t, 200, okBody)
	p := provider(t, f, "  "+secretToken+"\n")
	_, err := p.Complete(context.Background(), prov, models.Model{Name: "gemini-2.5-flash-lite", Options: models.Options{NumPredict: 256}}, workflow.Request{System: "SYS", Input: `{"x":1}`})
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
	_, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, workflow.Request{Input: "i", Schema: map[string]any{"type": "object"}})
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
	provider(t, f, secretToken).Complete(context.Background(), prov, model, workflow.Request{Input: "i"})
	if _, has := f.reqs[0].Fields["systemInstruction"]; has {
		t.Errorf("fields = %v", f.reqs[0].Fields)
	}
}

func TestTheReplyTextAndTokensAreReturnedThinkingCountsAsOutput(t *testing.T) {
	f := newFake(t, 200, okBody)
	resp, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, workflow.Request{Input: "i"})
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
		kind   llm.FailureKind
	}{
		{429, llm.FailRateLimited}, {500, llm.FailServer}, {503, llm.FailServer},
		{400, llm.FailClient}, {404, llm.FailClient}, {401, llm.FailClient}, {403, llm.FailClient},
	}
	for _, c := range cases {
		f := newFake(t, c.status, `{"error":{"code":1,"message":"the message","status":"SOME_STATUS"}}`)
		_, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, workflow.Request{Input: "i"})
		got := failure(t, err)
		if got.Kind != c.kind || !strings.Contains(err.Error(), "SOME_STATUS: the message") {
			t.Errorf("%d: %v / %v", c.status, got.Kind, err)
		}
	}
}

func TestAnExpiredTokenSaysHowToRefreshIt(t *testing.T) {
	f := newFake(t, 401, `{"error":{"message":"invalid authentication credentials","status":"UNAUTHENTICATED"}}`)
	_, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, workflow.Request{Input: "i"})
	if !strings.Contains(err.Error(), "refresh-assistant-vertex-token") || !strings.Contains(err.Error(), "UNAUTHENTICATED") {
		t.Errorf("err = %v", err)
	}
}

func TestAnErrorBodyThatIsNotJSONIsKeptShortAndTheTokenNeverAppears(t *testing.T) {
	f := newFake(t, 502, strings.Repeat("<html>bad gateway</html>", 100))
	_, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, workflow.Request{Input: "i"})
	if got := failure(t, err); got.Kind != llm.FailServer || len(err.Error()) > 600 {
		t.Errorf("%v len %d", got.Kind, len(err.Error()))
	}
	if strings.Contains(err.Error(), secretToken) {
		t.Errorf("the token must never be in an error: %v", err)
	}
}

func TestNoCandidatesIsAClientErrorWithTheBlockReason(t *testing.T) {
	f := newFake(t, 200, `{"promptFeedback":{"blockReason":"SAFETY"}}`)
	_, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, workflow.Request{Input: "i"})
	if failure(t, err).Kind != llm.FailClient || !strings.Contains(err.Error(), "SAFETY") {
		t.Errorf("err = %v", err)
	}
	f2 := newFake(t, 200, `{}`)
	_, err = provider(t, f2, secretToken).Complete(context.Background(), prov, model, workflow.Request{Input: "i"})
	if !strings.Contains(err.Error(), "no candidates") {
		t.Errorf("err = %v", err)
	}
}

func TestAReplyThatIsNotJSONIsAServerFault(t *testing.T) {
	f := newFake(t, 200, "not json")
	_, err := provider(t, f, secretToken).Complete(context.Background(), prov, model, workflow.Request{Input: "i"})
	if failure(t, err).Kind != llm.FailServer {
		t.Errorf("err = %v", err)
	}
}

func TestTheTokenFileIsReadOnEveryCallSoARefreshNeedsNoRestart(t *testing.T) {
	f := newFake(t, 200, okBody)
	path := tokenFile(t, "first")
	p := New(path, WithEndpoint(func(string) string { return f.URL }))
	p.Complete(context.Background(), prov, model, workflow.Request{Input: "i"})
	os.WriteFile(path, []byte("second\n"), 0o600)
	p.Complete(context.Background(), prov, model, workflow.Request{Input: "i"})
	if f.reqs[0].Auth != "Bearer first" || f.reqs[1].Auth != "Bearer second" {
		t.Errorf("auth = %q, %q", f.reqs[0].Auth, f.reqs[1].Auth)
	}
}

func TestAMissingOrEmptyTokenFileIsAClearClientErrorAndNoRequestIsMade(t *testing.T) {
	f := newFake(t, 200, okBody)
	missing := New(filepath.Join(t.TempDir(), "nope"), WithEndpoint(func(string) string { return f.URL }))
	_, err := missing.Complete(context.Background(), prov, model, workflow.Request{Input: "i"})
	if failure(t, err).Kind != llm.FailClient || !strings.Contains(err.Error(), "cannot read the access token file") || !strings.Contains(err.Error(), "refresh-assistant-vertex-token") {
		t.Errorf("missing: %v", err)
	}
	empty := New(tokenFile(t, " \n"), WithEndpoint(func(string) string { return f.URL }))
	if _, err := empty.Complete(context.Background(), prov, model, workflow.Request{Input: "i"}); failure(t, err).Kind != llm.FailClient || !strings.Contains(err.Error(), "is empty") {
		t.Errorf("empty: %v", err)
	}
	none := New("", WithEndpoint(func(string) string { return f.URL }))
	if _, err := none.Complete(context.Background(), prov, model, workflow.Request{Input: "i"}); failure(t, err).Kind != llm.FailClient || !strings.Contains(err.Error(), "no access token file is configured") {
		t.Errorf("unset: %v", err)
	}
	if len(f.reqs) != 0 {
		t.Errorf("no token, no request: %d", len(f.reqs))
	}
}

func TestAnUnreachableAddressIsAConnectionFailure(t *testing.T) {
	f := newFake(t, 200, okBody)
	url := f.URL
	f.Close()
	p := New(tokenFile(t, secretToken), WithEndpoint(func(string) string { return url }))
	_, err := p.Complete(context.Background(), prov, model, workflow.Request{Input: "i"})
	if failure(t, err).Kind != llm.FailConnection {
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
	p := New(tokenFile(t, secretToken), WithEndpoint(func(string) string { return slow.URL }), WithTimeoutUnit(20*time.Millisecond))
	_, err := p.Complete(context.Background(), prov, models.Model{Name: "m", Options: models.Options{OverallTimeoutSeconds: 1}}, workflow.Request{Input: "i"})
	if failure(t, err).Kind != llm.FailTimeout || !strings.Contains(err.Error(), "no answer within") {
		t.Errorf("timeout: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	p2 := New(tokenFile(t, secretToken), WithEndpoint(func(string) string { return slow.URL }))
	_, err = p2.Complete(ctx, prov, model, workflow.Request{Input: "i"})
	if failure(t, err).Kind != llm.FailCanceled {
		t.Errorf("cancel: %v", err)
	}
}
