package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s-lab/shared/llm"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"

	"phraseforge/internal/config"
)

// twoModels has a strong model (priority 20, tags default and strong) and a
// fallback (priority 10, tag default), both on one provider.
const twoModels = `
providers:
  - id: p
    type: ollama
    base_url: http://127.0.0.1:1
    api_key_from: ""
models:
  - id: strong
    provider: p
    model: strong-name
    tags: [default, strong]
    priority: 20
  - id: fallback
    provider: p
    model: fallback-name
    tags: [default]
    priority: 10
`

// scriptedService is a Service whose router calls fake instead of Ollama.
func scriptedService(t *testing.T, modelsYAML string, fake *routertest.Provider, opts ...router.Option) *Service {
	t.Helper()
	models := mustModels(t, modelsYAML)
	r, err := router.NewRouter(models, map[string]router.Provider{"ollama": fake}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{}
	s.SetRouter(r, models)
	return s
}

var userHi = []llm.Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "hi"}}

func TestCallLLMSendsTheTagsAndPerCallSettingsToTheRouter(t *testing.T) {
	fake := routertest.NewProvider(map[string][]routertest.Result{"strong": {routertest.OK("done", 10, 5)}})
	s := scriptedService(t, twoModels, fake)
	timeout := 42
	prompt := Prompt{ModelTags: []string{"default", "strong"}, TimeoutSeconds: &timeout}
	def := config.PurposeConfig{MaxReplyTokens: 300, NumCtx: 8192, TimeoutSeconds: 1800}

	out, err := s.callLLM(context.Background(), "translation", prompt, def, userHi, json.RawMessage(`{"type":"object"}`), 1, 3)
	if err != nil || out != "done" {
		t.Fatalf("got (%q, %v)", out, err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].ModelID != "strong" {
		t.Fatalf("calls = %+v, want one to strong", calls)
	}
	req := calls[0].Request
	if !reflect.DeepEqual(req.Tags, []string{"default", "strong"}) {
		t.Errorf("Tags = %v", req.Tags)
	}
	if req.MaxTokens != 300 || req.NumCtx != 8192 {
		t.Errorf("MaxTokens = %d, NumCtx = %d, want 300 and 8192", req.MaxTokens, req.NumCtx)
	}
	if req.Timeout != 42*time.Second {
		t.Errorf("Timeout = %v, want the admin override 42s over the purpose's 1800s", req.Timeout)
	}
	if req.Think != nil {
		t.Errorf("Think = %v, want nil without an llm_prompts row", *req.Think)
	}
	if req.Schema["type"] != "object" {
		t.Errorf("Schema = %v", req.Schema)
	}
	want := []router.Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "hi"}}
	if !reflect.DeepEqual(req.Messages, want) {
		t.Errorf("Messages = %v, want the history with its roles", req.Messages)
	}
}

func TestCallLLMLeavesTimeoutAndSchemaUnsetWhenNothingConfiguresThem(t *testing.T) {
	fake := routertest.NewProvider(nil)
	s := scriptedService(t, twoModels, fake)
	if _, err := s.callLLM(context.Background(), "translation", Prompt{}, config.PurposeConfig{NumCtx: 4096}, userHi, nil, 1, 1); err != nil {
		t.Fatal(err)
	}
	req := fake.Calls()[0].Request
	if req.Timeout != 0 || req.Schema != nil || req.MaxTokens != 0 || req.Tags != nil {
		t.Errorf("request = %+v, want Timeout, Schema, MaxTokens and Tags unset", req)
	}
}

func TestCallLLMThinkIsAnOverrideOnlyWhenARowSetsIt(t *testing.T) {
	yes, no := true, false
	for name, tc := range map[string]struct {
		override *bool
		want     *bool
	}{"no row": {nil, nil}, "row think true": {&yes, &yes}, "row think false": {&no, &no}} {
		fake := routertest.NewProvider(nil)
		s := scriptedService(t, twoModels, fake)
		if _, err := s.callLLM(context.Background(), "translation", Prompt{ThinkOverride: tc.override}, config.PurposeConfig{}, userHi, nil, 1, 1); err != nil {
			t.Fatal(err)
		}
		got := fake.Calls()[0].Request.Think
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("%s: Think = %v, want %v", name, got, tc.want)
		}
	}
}

func TestCallLLMRejectsAReplySchemaThatIsNotJSONWithoutCallingAModel(t *testing.T) {
	fake := routertest.NewProvider(nil)
	s := scriptedService(t, twoModels, fake)
	_, err := s.callLLM(context.Background(), "translation", Prompt{}, config.PurposeConfig{}, userHi, json.RawMessage(`{not json`), 1, 1)
	if err == nil || !strings.Contains(err.Error(), "reply schema") {
		t.Fatalf("err = %v, want it to name the reply schema", err)
	}
	if len(fake.Calls()) != 0 {
		t.Error("a model was called")
	}
}

func TestCallLLMWithoutARouterIsAClearErrorNotAPanic(t *testing.T) {
	_, err := (&Service{}).callLLM(context.Background(), "translation", Prompt{}, config.PurposeConfig{}, userHi, nil, 1, 1)
	if err == nil || !strings.Contains(err.Error(), "router is not configured") {
		t.Fatalf("err = %v", err)
	}
}

func TestCallLLMFallsBackFromAFailingModelToALowerPriorityOne(t *testing.T) {
	fake := routertest.NewProvider(map[string][]routertest.Result{
		"strong":   {routertest.Fail(router.FailServer, "overloaded")},
		"fallback": {routertest.OK("from fallback", 1, 1)},
	})
	s := scriptedService(t, twoModels, fake)
	out, err := s.callLLM(context.Background(), "translation", Prompt{ModelTags: []string{"default"}}, config.PurposeConfig{}, userHi, nil, 1, 1)
	if err != nil || out != "from fallback" {
		t.Fatalf("got (%q, %v)", out, err)
	}
	if got := fake.Models(); !reflect.DeepEqual(got, []string{"strong", "fallback"}) {
		t.Errorf("models called = %v, want strong then fallback", got)
	}
}

func TestCallLLMDetectsContextOverflowFromTheRoutersTokenCounts(t *testing.T) {
	fake := routertest.NewProvider(map[string][]routertest.Result{"strong": {routertest.OK("reply", 600, 500)}})
	s := scriptedService(t, twoModels, fake)
	out, err := s.callLLM(context.Background(), "translation", Prompt{ModelTags: []string{"strong"}}, config.PurposeConfig{NumCtx: 1000}, userHi, nil, 1, 1)
	var overflow *ContextOverflowError
	if !errors.As(err, &overflow) || out != "" || overflow.NumCtx != 1000 || overflow.PromptTokens != 600 || overflow.ReplyTokens != 500 {
		t.Fatalf("got (%q, %v), want an empty reply and a ContextOverflowError", out, err)
	}
}

func TestACorrectionLoopRetriesAnAllModelsFailedServerError(t *testing.T) {
	fake := routertest.NewProvider(map[string][]routertest.Result{
		"fallback": {routertest.Fail(router.FailServer, "HTTP 503"), routertest.OK("second time", 1, 1)},
	})
	s := scriptedService(t, strings.Replace(twoModels, "tags: [default, strong]", "tags: [other]", 1), fake)
	rec := &sleepRecorder{}
	s.sleep = rec.sleep
	got, err := s.callWithRetry(context.Background(), "translation", Prompt{ModelTags: []string{"default"}}, config.PurposeConfig{MaxAttempts: 3}, userHi)
	if err != nil || got != "second time" {
		t.Fatalf("got (%q, %v)", got, err)
	}
	if len(fake.Calls()) != 2 || len(rec.waits) != 1 {
		t.Errorf("calls = %d, waits = %v, want 2 calls after one backoff", len(fake.Calls()), rec.waits)
	}
}

func TestACorrectionLoopDoesNotRetryANoModelError(t *testing.T) {
	fake := routertest.NewProvider(nil)
	s := scriptedService(t, twoModels, fake)
	rec := &sleepRecorder{}
	s.sleep = rec.sleep
	_, err := s.callWithRetry(context.Background(), "translation", Prompt{ModelTags: []string{"no-such-tag"}}, config.PurposeConfig{MaxAttempts: 3}, userHi)
	var noModel *router.NoModelError
	if !errors.As(err, &noModel) {
		t.Fatalf("err = %v, want a NoModelError", err)
	}
	if len(fake.Calls()) != 0 || len(rec.waits) != 0 {
		t.Errorf("calls = %d, waits = %v, want neither", len(fake.Calls()), rec.waits)
	}
}

func TestIsRetryable(t *testing.T) {
	failure := func(kind router.FailureKind, cause error) error { return &router.Failure{Kind: kind, Err: cause} }
	other := errors.New("provider said no")
	allFailed := func(errs ...error) error {
		e := &router.AllFailedError{}
		for i, err := range errs {
			e.Attempts = append(e.Attempts, router.AttemptError{ModelID: fmt.Sprint("m", i), Err: err})
		}
		return e
	}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		// What llm.IsRetryable decides, kept exactly, behind a router.Failure.
		{"ollama 5xx", failure(router.FailServer, &llm.StatusError{StatusCode: 503}), true},
		{"ollama 4xx", failure(router.FailClient, &llm.StatusError{StatusCode: 400}), false},
		{"ollama 429 is judged by llm.IsRetryable", failure(router.FailRateLimited, &llm.StatusError{StatusCode: 429}), false},
		{"ollama idle stall", failure(router.FailTimeout, &llm.TimeoutError{Limit: llm.LimitIdle}), true},
		{"ollama first token timeout", failure(router.FailTimeout, &llm.TimeoutError{Limit: llm.LimitFirstToken}), false},
		{"ollama overall timeout", failure(router.FailTimeout, &llm.TimeoutError{Limit: llm.LimitOverall}), false},
		{"ollama stream cut", failure(router.FailServer, llm.ErrStreamIncomplete), true},
		{"bare llm status error", &llm.StatusError{StatusCode: 502}, true},
		{"other error", other, false},
		// Another provider's failures, by kind.
		{"connection", failure(router.FailConnection, other), true},
		{"server", failure(router.FailServer, other), true},
		{"rate limited", failure(router.FailRateLimited, other), true},
		{"timeout", failure(router.FailTimeout, other), false},
		{"client", failure(router.FailClient, other), false},
		{"canceled", failure(router.FailCanceled, other), false},
		// The router's own errors.
		{"no model for the tags", &router.NoModelError{Tags: []string{"x"}}, false},
		{"no model, every match in cooldown", &router.NoModelError{Tags: []string{"x"}, EarliestAvailable: time.Unix(1, 0), RetryAfter: time.Second}, true},
		{"no model, cooldown but another match lacks a credential", &router.NoModelError{Tags: []string{"x"}, EarliestAvailable: time.Unix(1, 0), SkippedForOtherReason: true}, false},
		{"no model, only a missing credential", &router.NoModelError{Tags: []string{"x"}, SkippedForOtherReason: true}, false},
		{"all failed, one retryable", allFailed(failure(router.FailClient, other), failure(router.FailServer, other)), true},
		{"all failed, none retryable", allFailed(failure(router.FailClient, other), failure(router.FailTimeout, other)), false},
		{"all failed, ollama 5xx", allFailed(failure(router.FailServer, &llm.StatusError{StatusCode: 500})), true},
		{"all failed, skipped provider", allFailed(errors.New("skipped: its provider is unavailable")), false},
		{"canceled context", context.Canceled, false},
		{"deadline", fmt.Errorf("call: %w", context.DeadlineExceeded), false},
		{"wrapped", fmt.Errorf("x: %w", failure(router.FailServer, other)), true},
	}
	for _, tc := range cases {
		if got := isRetryable(tc.err); got != tc.want {
			t.Errorf("%s: isRetryable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// syncBuffer is a bytes.Buffer safe to write from the router and the test.
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

// captureLogs replaces the default logger with a JSON logger writing to the
// returned buffer, restoring it after the test.
func captureLogs(t *testing.T) (*syncBuffer, *slog.Logger) {
	t.Helper()
	buf := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	previous := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buf, logger
}

func logLines(t *testing.T, buf *syncBuffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("log line %q: %v", raw, err)
		}
		lines = append(lines, line)
	}
	return lines
}

func TestTheTelemetryLineCarriesTheModelIDAndNeverContent(t *testing.T) {
	buf, logger := captureLogs(t)
	fake := routertest.NewProvider(map[string][]routertest.Result{"strong": {routertest.OK("SECRET-REPLY-TEXT", 12, 7)}})
	s := scriptedService(t, twoModels, fake, router.WithLogger(logger), router.WithLogAttrs(LogAttrs))
	messages := []llm.Message{{Role: "system", Content: "SECRET-SYSTEM-PROMPT"}, {Role: "user", Content: "SECRET-USER-INPUT"}}
	prompt := Prompt{SourceLanguage: "deu", TargetLanguage: "pol", ModelTags: []string{"strong"}}
	if _, err := s.callLLM(context.Background(), "translation", prompt, config.PurposeConfig{NumCtx: 4096}, messages, json.RawMessage(`{"type":"object","description":"SECRET-SCHEMA"}`), 2, 3); err != nil {
		t.Fatal(err)
	}

	var telemetry map[string]any
	for _, line := range logLines(t, buf) {
		if line["msg"] == "llm call" {
			telemetry = line
		}
	}
	if telemetry == nil {
		t.Fatalf("no \"llm call\" line in %s", buf.String())
	}
	for key, want := range map[string]any{"kind": "translation", "model_id": "strong", "outcome": "success", "attempt": float64(2), "max_attempts": float64(3), "prompt_tokens": float64(12), "reply_tokens": float64(7), "num_ctx": float64(4096)} {
		if telemetry[key] != want {
			t.Errorf("%s = %v, want %v", key, telemetry[key], want)
		}
	}
	for _, gone := range []string{"provider", "model"} {
		if _, ok := telemetry[gone]; ok {
			t.Errorf("the telemetry line still has %q", gone)
		}
	}
	for _, secret := range []string{"SECRET-REPLY-TEXT", "SECRET-SYSTEM-PROMPT", "SECRET-USER-INPUT", "SECRET-SCHEMA"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("the logs contain %q", secret)
		}
	}
}

func TestTheRoutersLogLinesCarryTheKindAndLanguagesFromTheContext(t *testing.T) {
	buf, logger := captureLogs(t)
	fake := routertest.NewProvider(nil)
	s := scriptedService(t, twoModels, fake, router.WithLogger(logger), router.WithLogAttrs(LogAttrs))
	prompt := Prompt{SourceLanguage: "deu", TargetLanguage: "pol"}
	if _, err := s.callLLM(context.Background(), "review_vocabulary", prompt, config.PurposeConfig{}, userHi, nil, 1, 1); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range logLines(t, buf) {
		if line["msg"] != "llm call done" {
			continue
		}
		found = true
		if line["kind"] != "review_vocabulary" || line["source_language"] != "deu" || line["target_language"] != "pol" {
			t.Errorf("router line = %v, want kind and languages", line)
		}
	}
	if !found {
		t.Fatalf("no router \"llm call done\" line in %s", buf.String())
	}
}

func TestLogAttrsOmitsWhatIsUnknownAndNeedsNoCallContext(t *testing.T) {
	if got := LogAttrs(context.Background()); got != nil {
		t.Errorf("LogAttrs without a call = %v, want nil", got)
	}
	ctx := withCallInfo(context.Background(), callInfo{kind: "title"})
	got := LogAttrs(ctx)
	if len(got) != 1 || got[0].Key != "kind" {
		t.Errorf("LogAttrs = %v, want only kind", got)
	}
}
