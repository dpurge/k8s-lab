package router_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/anthropic"
	"k8s-lab/shared/router/ollama"
	"k8s-lab/shared/router/openai"
	"k8s-lab/shared/router/routertest"
	"k8s-lab/shared/router/vertex"
)

type logLine map[string]any

func (l logLine) str(key string) string { s, _ := l[key].(string); return s }

// num reads a JSON number; it fails the lookup, not the test, when absent.
func (l logLine) num(key string) float64 { n, _ := l[key].(float64); return n }

func (l logLine) has(key string) bool { _, ok := l[key]; return ok }

func (l logLine) list(key string) []string {
	raw, _ := l[key].([]any)
	out := make([]string, len(raw))
	for i, v := range raw {
		out[i], _ = v.(string)
	}
	return out
}

// logRig is a router whose log lines are captured as parsed JSON.
type logRig struct {
	*rig
	buf *bytes.Buffer
}

type ctxKey struct{}

func newLogRig(t *testing.T, config string, scripts map[string][]routertest.Result, extra ...router.Option) *logRig {
	t.Helper()
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(config))
	if err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	r := &rig{provider: routertest.NewProvider(scripts), clock: routertest.NewClock(t0), observer: &routertest.Observer{}}
	r.clock.Step = 5 * time.Millisecond
	n := 0
	opts := append([]router.Option{
		router.WithClock(r.clock), router.WithObserver(r.observer),
		router.WithLogger(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))),
		router.WithCallIDs(func() string { n++; return fmt.Sprintf("call%04d", n) }),
	}, extra...)
	r.router, err = router.NewRouter(cfg, map[string]router.Provider{"ollama": r.provider}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return &logRig{rig: r, buf: buf}
}

func (l *logRig) lines(t *testing.T) []logLine {
	t.Helper()
	var out []logLine
	for _, raw := range strings.Split(strings.TrimSpace(l.buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var line logLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", raw, err)
		}
		out = append(out, line)
	}
	return out
}

func (l *logRig) messages(t *testing.T, msg string) []logLine {
	t.Helper()
	var out []logLine
	for _, line := range l.lines(t) {
		if line.str("msg") == msg {
			out = append(out, line)
		}
	}
	return out
}

func (l *logRig) one(t *testing.T, msg string) logLine {
	t.Helper()
	got := l.messages(t, msg)
	if len(got) != 1 {
		t.Fatalf("want one %q line, got %d in:\n%s", msg, len(got), l.buf.String())
	}
	return got[0]
}

func failure(kind router.FailureKind, status int, requestID, text string) routertest.Result {
	return routertest.Result{Err: &router.Failure{Kind: kind, Err: fmt.Errorf("%s", text), HTTPStatus: status, RequestID: requestID}}
}

func TestAnOKAttemptLogsEveryField(t *testing.T) {
	l := newLogRig(t, baseConfig, map[string][]routertest.Result{"big-gpu": {routertest.OK("reply", 120, 9)}})
	if _, err := l.call("chat"); err != nil {
		t.Fatal(err)
	}
	a := l.one(t, "llm attempt")
	want := map[string]any{
		"level": "INFO", "call_id": "call0001", "attempt": 1.0, "of": 4.0, "model_id": "big-gpu", "provider_id": "gpu-box",
		"provider_type": "ollama", "model_name": "gemma4:12b", "outcome": "ok", "input_tokens": 120.0, "output_tokens": 9.0,
	}
	for k, v := range want {
		if a[k] != v {
			t.Errorf("%s = %v, want %v", k, a[k], v)
		}
	}
	if tags := a.list("tags"); len(tags) != 1 || tags[0] != "chat" {
		t.Errorf("tags = %v", a["tags"])
	}
	if !a.has("duration_ms") || a.num("duration_ms") <= 0 {
		t.Errorf("duration_ms = %v", a["duration_ms"])
	}
	for _, absent := range []string{"failure_kind", "http_status", "request_id", "error"} {
		if a.has(absent) {
			t.Errorf("an ok attempt must not carry %q", absent)
		}
	}
	done := l.one(t, "llm call done")
	if done.str("outcome") != "ok" || done.str("model_id") != "big-gpu" || done["fell_back"] != false || done.num("attempts") != 1 || done.str("level") != "INFO" {
		t.Errorf("done = %v", done)
	}
	cands := l.one(t, "llm candidates")
	if cands.str("level") != "DEBUG" || strings.Join(cands.list("candidates"), ",") != "big-gpu,mid-gpu,laptop-a,laptop-b" {
		t.Errorf("candidates = %v", cands)
	}
}

func TestAFailedAttemptCarriesKindStatusRequestIDAndError(t *testing.T) {
	l := newLogRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu": {failure(router.FailServer, 503, "req_abc", "upstream exploded")},
	})
	if _, err := l.call("review"); err != nil {
		t.Fatal(err)
	}
	var failed logLine
	for _, a := range l.messages(t, "llm attempt") {
		if a.str("model_id") == "big-gpu" {
			failed = a
		}
	}
	if failed.str("level") != "WARN" || failed.str("outcome") != "error" || failed.str("failure_kind") != "server" ||
		failed.num("http_status") != 503 || failed.str("request_id") != "req_abc" {
		t.Errorf("failed attempt = %v", failed)
	}
	if !strings.Contains(failed.str("error"), "upstream exploded") {
		t.Errorf("error = %q", failed.str("error"))
	}
	at := l.observer.Attempts()
	if at[0].HTTPStatus != 503 || at[0].RequestID != "req_abc" {
		t.Errorf("the observer's attempt = %+v", at[0])
	}
}

func TestAFallbackIsTwoAttemptsAndOneDoneLine(t *testing.T) {
	l := newLogRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu": {failure(router.FailTimeout, 0, "", "slow")},
	})
	if _, err := l.call("review"); err != nil {
		t.Fatal(err)
	}
	attempts := l.messages(t, "llm attempt")
	if len(attempts) != 2 || attempts[0].num("attempt") != 1 || attempts[1].num("attempt") != 2 || attempts[1].num("of") != 2 {
		t.Fatalf("attempts = %v", attempts)
	}
	if attempts[0].str("outcome") != "timeout" || attempts[1].str("outcome") != "ok" {
		t.Errorf("outcomes = %v, %v", attempts[0]["outcome"], attempts[1]["outcome"])
	}
	done := l.one(t, "llm call done")
	if done["fell_back"] != true || done.num("attempts") != 2 || done.str("model_id") != "laptop-a" {
		t.Errorf("done = %v", done)
	}
	for _, line := range l.lines(t) {
		if line.str("call_id") != "call0001" {
			t.Errorf("a line of the call has another call_id: %v", line)
		}
	}
}

func TestAFailedCallLogsTheChain(t *testing.T) {
	l := newLogRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu":  {failure(router.FailServer, 500, "", "x")},
		"laptop-a": {failure(router.FailRateLimited, 429, "", "y")},
	})
	if _, err := l.call("review"); err == nil {
		t.Fatal("want an error")
	}
	done := l.one(t, "llm call done")
	if done.str("level") != "WARN" || done.str("outcome") != "failed" || strings.Join(done.list("chain"), ",") != "big-gpu:server,laptop-a:rate_limited" {
		t.Errorf("done = %v", done)
	}
}

func TestTheBreakerOpenLineAppearsWhenTheThresholdTrips(t *testing.T) {
	l := newLogRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu": {failure(router.FailServer, 500, "", "boom")},
	})
	for i := 1; i <= 3; i++ {
		if _, err := l.call("review"); err != nil {
			t.Fatal(err)
		}
		want := 0
		if i == 3 {
			want = 1
		}
		if got := len(l.messages(t, "llm breaker open")); got != want {
			t.Fatalf("after failure %d: %d breaker open lines, want %d", i, got, want)
		}
	}
	open := l.one(t, "llm breaker open")
	if open.str("level") != "WARN" || open.str("model_id") != "big-gpu" || open.str("provider_id") != "gpu-box" ||
		open.str("reason") != "server" || open.num("consecutive_failures") != 3 || open.num("cooldown_seconds") != 60 {
		t.Errorf("open = %v", open)
	}
	again, err := time.Parse(time.RFC3339, open.str("available_again"))
	if err != nil || !again.After(t0.Add(59*time.Second)) || !again.Before(t0.Add(70*time.Second)) || again.Location() != time.UTC {
		t.Errorf("available_again = %q (%v)", open.str("available_again"), err)
	}
	// Once held out, the model is a skipped candidate, with when it is back.
	if _, err := l.call("review"); err != nil {
		t.Fatal(err)
	}
	cands := l.messages(t, "llm candidates")
	skipped, _ := cands[len(cands)-1]["skipped"].([]any)
	if len(skipped) != 1 || skipped[0].(map[string]any)["model_id"] != "big-gpu" ||
		skipped[0].(map[string]any)["reason"] != "model_unavailable" || skipped[0].(map[string]any)["available_again"] != open.str("available_again") {
		t.Errorf("skipped = %v", skipped)
	}
}

func TestARateLimitOpensTheBreakerAtOnceWithTheShortCooldown(t *testing.T) {
	l := newLogRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu": {failure(router.FailRateLimited, 429, "", "slow down")},
	})
	if _, err := l.call("review"); err != nil {
		t.Fatal(err)
	}
	open := l.one(t, "llm breaker open")
	if open.str("reason") != "rate_limited" || open.num("cooldown_seconds") != 30 || open.num("consecutive_failures") != 0 {
		t.Errorf("open = %v", open)
	}
}

func TestAClientErrorDoesNotOpenTheBreaker(t *testing.T) {
	l := newLogRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu": {failure(router.FailClient, 400, "", "bad request")},
	})
	for i := 0; i < 5; i++ {
		_, _ = l.call("review")
	}
	if got := len(l.messages(t, "llm breaker open")); got != 0 {
		t.Errorf("%d breaker open lines for client errors", got)
	}
}

func TestAConnectionFailureLogsTheProviderDownWithItsModels(t *testing.T) {
	l := newLogRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu": {routertest.Fail(router.FailConnection, "connection refused")},
	})
	if _, err := l.call("chat"); err != nil {
		t.Fatal(err)
	}
	down := l.one(t, "llm provider down")
	if down.str("level") != "WARN" || down.str("provider_id") != "gpu-box" || strings.Join(down.list("models"), ",") != "big-gpu,mid-gpu" ||
		down.num("cooldown_seconds") != 60 || down.str("available_again") == "" {
		t.Errorf("down = %v", down)
	}
	if len(l.messages(t, "llm breaker open")) != 0 {
		t.Errorf("a provider going down is not also a model breaker line")
	}
	done := l.one(t, "llm call done")
	if done.str("model_id") != "laptop-a" || done["fell_back"] != true {
		t.Errorf("done = %v", done)
	}
}

func TestTheBreakerClosesWhenAModelRecovers(t *testing.T) {
	l := newLogRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu": {failure(router.FailServer, 500, "", "a"), failure(router.FailServer, 500, "", "b"), routertest.OK("", 1, 1)},
	})
	for i := 0; i < 3; i++ {
		if _, err := l.call("review"); err != nil {
			t.Fatal(err)
		}
	}
	closed := l.one(t, "llm breaker closed")
	if closed.str("level") != "INFO" || closed.str("model_id") != "big-gpu" || closed.num("previous_consecutive_failures") != 2 {
		t.Errorf("closed = %v", closed)
	}
}

func TestTheBreakerClosesAfterACooldownTrial(t *testing.T) {
	l := newLogRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu": {failure(router.FailRateLimited, 429, "", "a"), routertest.OK("", 1, 1)},
	})
	_, _ = l.call("review")
	l.clock.Advance(31 * time.Second)
	if _, err := l.call("review"); err != nil {
		t.Fatal(err)
	}
	if closed := l.one(t, "llm breaker closed"); closed.num("previous_consecutive_failures") != 0 {
		t.Errorf("closed = %v", closed)
	}
}

func TestAHealthyModelNeverLogsBreakerClosed(t *testing.T) {
	l := newLogRig(t, baseConfig, nil)
	_, _ = l.call("chat")
	if got := len(l.messages(t, "llm breaker closed")); got != 0 {
		t.Errorf("%d closed lines", got)
	}
}

func TestNoModelIsLogged(t *testing.T) {
	l := newLogRig(t, baseConfig, nil)
	if _, err := l.call("vision"); err == nil {
		t.Fatal("want an error")
	}
	line := l.one(t, "llm no model")
	if line.str("level") != "WARN" || strings.Join(line.list("models_checked"), ",") != "big-gpu,mid-gpu,laptop-a,laptop-b,embed" ||
		strings.Join(line.list("tags"), ",") != "vision" {
		t.Errorf("no model = %v", line)
	}
	if done := l.one(t, "llm call done"); done.str("outcome") != "failed" || done.num("attempts") != 0 {
		t.Errorf("done = %v", done)
	}
}

type cancelingProvider struct{ cancel context.CancelFunc }

func (p cancelingProvider) Complete(ctx context.Context, _ modelcfg.Provider, _ modelcfg.Model, _ router.Request) (router.Response, error) {
	p.cancel()
	return router.Response{}, ctx.Err()
}

func TestACanceledCallLogsInfoNotWarn(t *testing.T) {
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(baseConfig))
	if err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := router.NewRouter(cfg, map[string]router.Provider{"ollama": cancelingProvider{cancel}},
		router.WithClock(routertest.NewClock(t0)),
		router.WithLogger(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Complete(ctx, router.Request{Tags: []string{"chat"}}); err == nil {
		t.Fatal("want an error")
	}
	l := &logRig{buf: buf}
	attempt := l.one(t, "llm attempt")
	if attempt.str("level") != "INFO" || attempt.str("outcome") != "canceled" || attempt.str("failure_kind") != "canceled" {
		t.Errorf("attempt = %v", attempt)
	}
	done := l.one(t, "llm call done")
	if done.str("level") != "INFO" || done.str("outcome") != "canceled" {
		t.Errorf("done = %v", done)
	}
	for _, line := range l.lines(t) {
		if line.str("level") == "WARN" || line.str("level") == "ERROR" {
			t.Errorf("a canceled call must not warn: %v", line)
		}
	}
}

func TestLogAttrsAppearOnEveryLineOfACall(t *testing.T) {
	l := newLogRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu": {failure(router.FailRateLimited, 429, "", "a")},
	}, router.WithLogAttrs(func(ctx context.Context) []slog.Attr {
		id, _ := ctx.Value(ctxKey{}).(string)
		return []slog.Attr{slog.String("task_id", id)}
	}))
	ctx := context.WithValue(context.Background(), ctxKey{}, "task-42")
	if _, err := l.router.Complete(ctx, router.Request{Tags: []string{"review"}}); err != nil {
		t.Fatal(err)
	}
	lines := l.lines(t)
	if len(lines) < 5 {
		t.Fatalf("want candidates, two attempts, breaker open and done, got %d lines", len(lines))
	}
	for _, line := range lines {
		if line.str("task_id") != "task-42" {
			t.Errorf("line without the attribute: %v", line)
		}
	}
}

func TestTheLoggedErrorIsBoundedTo500Characters(t *testing.T) {
	long := strings.Repeat("é", 2000)
	l := newLogRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu": {failure(router.FailServer, 500, "", long)},
	})
	_, _ = l.call("review")
	var got string
	for _, a := range l.messages(t, "llm attempt") {
		if a.has("error") {
			got = a.str("error")
		}
	}
	if len(got) == 0 || len(got) > 500 || !strings.HasSuffix(got, "…") {
		t.Errorf("logged error is %d bytes: %.40q", len(got), got)
	}
	// The observer, and so the recorder, gets a longer bound (1000 bytes), set once
	// in the router.
	if seen := l.observer.Attempts()[0].Error; len(seen) <= 500 || len(seen) > 1000+len("…") {
		t.Errorf("the observer's error is %d bytes, want the 1000-byte bound", len(seen))
	}
}

func TestNoSecretReachesTheLog(t *testing.T) {
	const (
		prompt    = "PROMPT-SENTINEL-4711"
		input     = "INPUT-SENTINEL-4711"
		schemaKey = "SCHEMA-SENTINEL-4711"
		reply     = "REPLY-SENTINEL-4711"
		apiKey    = "sk-KEY-SENTINEL-4711"
	)
	var seenKeys []string
	openaiAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		seenKeys = append(seenKeys, r.Header.Get("Authorization"))
		w.Header().Set("x-request-id", "req_openai_1")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"provider is down"}}`)
	}))
	defer openaiAPI.Close()
	anthropicAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		seenKeys = append(seenKeys, r.Header.Get("x-api-key"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","content":[{"type":"text","text":"`+reply+`"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":4}}`)
	}))
	defer anthropicAPI.Close()

	config := fmt.Sprintf(`providers:
  - {id: or, type: openai, base_url: %q, api_key_from: env:OPENAI_TEST_KEY}
  - {id: claude, type: anthropic, base_url: %q, api_key_from: env:ANTHROPIC_TEST_KEY}
  - {id: laptop, type: ollama, base_url: "http://localhost:11434", api_key_from: ""}
models:
  - {id: remote, provider: or, model: vendor/m, tags: [chat], priority: 30}
  - {id: sonnet, provider: claude, model: some-claude, tags: [chat], priority: 20}
  - {id: local, provider: laptop, model: gemma4:12b, tags: [chat], priority: 10}
`, openaiAPI.URL, anthropicAPI.URL)
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(config))
	if err != nil {
		t.Fatal(err)
	}
	getenv := func(string) string { return apiKey }
	buf := &bytes.Buffer{}
	scripted := routertest.NewProvider(map[string][]routertest.Result{"local": {routertest.OK(reply, 1, 1)}})
	r, err := router.NewRouter(cfg, map[string]router.Provider{
		"openai":    openai.New(openai.WithGetenv(getenv)),
		"anthropic": anthropic.New(anthropic.WithGetenv(getenv)),
		"ollama":    scripted,
	}, router.WithLogger(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	if err != nil {
		t.Fatal(err)
	}
	req := router.Request{
		System: prompt, Input: input, Tags: []string{"chat"},
		Schema: map[string]any{"type": "object", "properties": map[string]any{schemaKey: map[string]any{"type": "string"}}},
	}
	// First call: openai fails, anthropic answers. Second: the same again.
	for i := 0; i < 2; i++ {
		resp, err := r.Complete(context.Background(), req)
		if err != nil || resp.Text != reply {
			t.Fatalf("call %d: %q, %v", i, resp.Text, err)
		}
	}
	// A multi-turn call: every turn carries its own sentinel.
	turns := []string{"SYSMSG-SENTINEL-4711", "TURN1-SENTINEL-4711", "TURN2-SENTINEL-4711", "TURN3-SENTINEL-4711"}
	multi := router.Request{
		System: prompt, Tags: []string{"chat"},
		Messages: []router.Message{
			{Role: router.RoleSystem, Content: turns[0]}, {Role: router.RoleUser, Content: turns[1]},
			{Role: router.RoleAssistant, Content: turns[2]}, {Role: router.RoleUser, Content: turns[3]},
		},
	}
	if resp, err := r.Complete(context.Background(), multi); err != nil || resp.Text != reply {
		t.Fatalf("multi-turn call: %q, %v", resp.Text, err)
	}
	for _, key := range seenKeys {
		if !strings.Contains(key, apiKey) {
			t.Fatalf("the fake APIs did not receive the key (%q); the test proves nothing", key)
		}
	}
	logged := buf.String()
	if !strings.Contains(logged, "req_openai_1") || !strings.Contains(logged, "provider is down") {
		t.Fatalf("the failure was not logged as expected:\n%s", logged)
	}
	for _, secret := range append([]string{prompt, input, schemaKey, reply, apiKey}, turns...) {
		if strings.Contains(logged, secret) {
			t.Errorf("the log holds %q:\n%s", secret, logged)
		}
	}
}

func TestAnAnswerAfterAProviderReportedCanceledLogsAFallback(t *testing.T) {
	l := newLogRig(t, baseConfig, map[string][]routertest.Result{
		"big-gpu": {failure(router.FailCanceled, 0, "", "gave up")},
	})
	if _, err := l.call("review"); err != nil {
		t.Fatal(err)
	}
	done := l.one(t, "llm call done")
	if done.str("outcome") != "ok" || done["fell_back"] != true || done.str("model_id") != "laptop-a" {
		t.Errorf("done = %v", done)
	}
}

// TestNoSecretReachesTheLogOfOllamaAndVertex is TestNoSecretReachesTheLog for the
// two providers it does not cover: the key and a custom header value of each go
// over the wire, and neither, nor the prompt or the reply, is ever in a log line.
func TestNoSecretReachesTheLogOfOllamaAndVertex(t *testing.T) {
	const (
		prompt      = "PROMPT-SENTINEL-4712"
		reply       = "REPLY-SENTINEL-4712"
		ollamaKey   = "OLLAMA-KEY-SENTINEL-4712"
		ollamaHdr   = "OLLAMA-HEADER-SENTINEL-4712"
		vertexToken = "VERTEX-TOKEN-SENTINEL-4712"
		vertexHdr   = "VERTEX-HEADER-SENTINEL-4712"
	)
	var mu sync.Mutex
	var ollamaSeen, vertexSeen []http.Header
	ollamaAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		ollamaSeen = append(ollamaSeen, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"`+reply+`"},"done":true,"prompt_eval_count":3,"eval_count":4}`+"\n")
	}))
	defer ollamaAPI.Close()
	vertexAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		vertexSeen = append(vertexSeen, r.Header.Clone())
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"vertex is down","status":"UNAVAILABLE"}}`)
	}))
	defer vertexAPI.Close()

	config := fmt.Sprintf(`providers:
  - {id: vx, type: vertex, project: p, location: us-central1, api_key_from: "env:VERTEX_TEST_TOKEN", headers: {X-Secret: "env:VERTEX_TEST_HEADER"}}
  - {id: laptop, type: ollama, base_url: %q, api_key_from: "env:OLLAMA_TEST_KEY", headers: {X-Secret: "env:OLLAMA_TEST_HEADER"}}
models:
  - {id: cloud, provider: vx, model: gemini-x, tags: [chat], priority: 30}
  - {id: local, provider: laptop, model: gemma4:12b, tags: [chat], priority: 20}
`, ollamaAPI.URL)
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(config))
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"VERTEX_TEST_TOKEN": vertexToken, "VERTEX_TEST_HEADER": vertexHdr,
		"OLLAMA_TEST_KEY": ollamaKey, "OLLAMA_TEST_HEADER": ollamaHdr,
	}
	getenv := func(name string) string { return env[name] }
	buf := &bytes.Buffer{}
	r, err := router.NewRouter(cfg, map[string]router.Provider{
		"vertex": vertex.New(vertex.WithGetenv(getenv), vertex.WithEndpoint(func(string) string { return vertexAPI.URL })),
		"ollama": ollama.New(ollama.WithGetenv(getenv)),
	}, router.WithLogger(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		resp, err := r.Complete(context.Background(), router.Request{System: prompt, Input: prompt, Tags: []string{"chat"}})
		if err != nil || resp.Text != reply {
			t.Fatalf("call %d: %q, %v", i, resp.Text, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(vertexSeen) == 0 || len(ollamaSeen) == 0 {
		t.Fatalf("both fake APIs must be called (vertex %d, ollama %d); the test proves nothing otherwise", len(vertexSeen), len(ollamaSeen))
	}
	if got := vertexSeen[0].Get("Authorization"); !strings.Contains(got, vertexToken) || vertexSeen[0].Get("X-Secret") != vertexHdr {
		t.Fatalf("vertex did not receive its token and header; the test proves nothing")
	}
	if got := ollamaSeen[0].Get("Authorization"); !strings.Contains(got, ollamaKey) || ollamaSeen[0].Get("X-Secret") != ollamaHdr {
		t.Fatalf("ollama did not receive its key and header; the test proves nothing")
	}
	logged := buf.String()
	if !strings.Contains(logged, "vertex is down") {
		t.Fatalf("the vertex failure was not logged as expected:\n%s", logged)
	}
	for _, secret := range []string{prompt, reply, ollamaKey, ollamaHdr, vertexToken, vertexHdr} {
		if strings.Contains(logged, secret) {
			t.Errorf("the log holds %q:\n%s", secret, logged)
		}
	}
}
