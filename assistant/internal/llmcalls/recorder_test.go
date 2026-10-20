package llmcalls_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"assistant/internal/callctx"
	"assistant/internal/llm"
	"assistant/internal/llm/llmtest"
	"assistant/internal/llmcalls"
	"assistant/internal/metrics"
	"assistant/internal/workflow"
	"k8s-lab/shared/modelcfg"
)

const recorderConfig = `providers:
  - {id: laptop, type: ollama, base_url: "http://localhost:11434", api_key_from: ""}
  - id: gpu-box
    type: ollama
    base_url: "http://gpu-box.lan:11434"
    api_key_from: ""
    headers: {X-Distinctive-Header-Name: distinctive-literal-header-value, X-Env-Header: "env:DISTINCTIVE_HEADER_VARIABLE", X-File-Header: "file:/distinctive-dir/header-file"}
models:
  - {id: paid, provider: gpu-box, model: big, tags: [chat], priority: 20, price: {input_per_mtok: 3, output_per_mtok: 15}}
  - {id: free, provider: laptop, model: small, tags: [chat], priority: 10}
`

// headerLeaks are the distinctive header names, values and sources of the test
// configuration; none may reach a table row, a metric or a log line.
var headerLeaks = []string{"X-Distinctive-Header-Name", "distinctive-literal-header-value", "X-Env-Header", "DISTINCTIVE_HEADER_VARIABLE", "X-File-Header", "/distinctive-dir/header-file"}

func assertNoHeaderLeak(t *testing.T, where, text string) {
	t.Helper()
	for _, s := range headerLeaks {
		if strings.Contains(text, s) {
			t.Errorf("%s holds the header detail %q", where, s)
		}
	}
}

func config(t *testing.T) *modelcfg.Config {
	t.Helper()
	cfg, err := modelcfg.LoadBytes("m.yaml", []byte(recorderConfig))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

type fakeStore struct {
	mu     sync.Mutex
	calls  []llmcalls.Call
	ctxErr []error
	err    error
}

func (s *fakeStore) Insert(ctx context.Context, c llmcalls.Call) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctxErr = append(s.ctxErr, ctx.Err())
	if s.err != nil {
		return s.err
	}
	s.calls = append(s.calls, c)
	return nil
}

type fakeMetrics struct {
	mu     sync.Mutex
	seen   []string
	errors int
	cost   float64
}

func (m *fakeMetrics) ObserveLLM(user, provider, model, outcome string, in, out int, cost float64, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen = append(m.seen, strings.Join([]string{user, provider, model, outcome}, "/"))
	m.cost += cost
}

func (m *fakeMetrics) RecordError() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errors++
}

func attempt(model, provider, outcome string, in, out int) llm.Attempt {
	return llm.Attempt{
		ModelID: model, ProviderID: provider, ModelName: model + "-name", Tags: []string{"chat"},
		StartedAt: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), Duration: 2500 * time.Millisecond,
		InputTokens: in, OutputTokens: out, Outcome: outcome,
	}
}

func recorderWith(t *testing.T) (*llmcalls.Recorder, *fakeStore, *fakeMetrics, *bytes.Buffer) {
	t.Helper()
	store, m, logs := &fakeStore{}, &fakeMetrics{}, &bytes.Buffer{}
	return llmcalls.NewRecorder(config(t), store, m, slog.New(slog.NewJSONHandler(logs, nil))), store, m, logs
}

var info = callctx.Info{TaskID: "11111111-2222-4333-8444-555555555555", UserID: "aaaaaaaa-2222-4333-8444-555555555555"}

func TestAnAttemptIsStoredWithItsCostAndOwner(t *testing.T) {
	rec, store, m, _ := recorderWith(t)
	rec.Observe(callctx.With(context.Background(), info), attempt("paid", "gpu-box", llm.OutcomeOK, 1200, 300))
	if len(store.calls) != 1 {
		t.Fatalf("stored %d", len(store.calls))
	}
	c := store.calls[0]
	if c.UserID != info.UserID || c.TaskID == nil || *c.TaskID != info.TaskID {
		t.Errorf("owner = %+v", c)
	}
	if c.ModelID != "paid" || c.ProviderID != "gpu-box" || c.ModelName != "paid-name" || strings.Join(c.Tags, ",") != "chat" {
		t.Errorf("model fields = %+v", c)
	}
	if c.InputTokens != 1200 || c.OutputTokens != 300 || c.DurationMS != 2500 || c.Outcome != "ok" || c.Error != nil {
		t.Errorf("usage fields = %+v", c)
	}
	if want := (1200*3.0 + 300*15.0) / 1e6; c.CostUSD < want-1e-12 || c.CostUSD > want+1e-12 {
		t.Errorf("cost = %v, want %v", c.CostUSD, want)
	}
	if len(m.seen) != 1 || m.seen[0] != info.UserID+"/gpu-box/paid/ok" || m.cost != c.CostUSD {
		t.Errorf("metrics = %v cost %v", m.seen, m.cost)
	}
}

func TestALocalModelCostsNothing(t *testing.T) {
	rec, store, _, _ := recorderWith(t)
	rec.Observe(callctx.With(context.Background(), info), attempt("free", "laptop", llm.OutcomeOK, 5_000_000, 5_000_000))
	if store.calls[0].CostUSD != 0 {
		t.Errorf("cost = %v", store.calls[0].CostUSD)
	}
}

func TestAModelThatIsNotInTheConfigIsPricedAtZeroNotRefused(t *testing.T) {
	rec, store, _, _ := recorderWith(t)
	rec.Observe(callctx.With(context.Background(), info), attempt("removed-since", "laptop", llm.OutcomeOK, 10, 10))
	if len(store.calls) != 1 || store.calls[0].CostUSD != 0 {
		t.Errorf("%+v", store.calls)
	}
}

func TestAFailedAttemptIsStoredWithItsErrorAndNoCost(t *testing.T) {
	rec, store, _, _ := recorderWith(t)
	a := attempt("paid", "gpu-box", llm.OutcomeTimeout, 0, 0)
	a.Error = "model big stalled: no output for 60s"
	rec.Observe(callctx.With(context.Background(), info), a)
	c := store.calls[0]
	if c.Outcome != "timeout" || c.Error == nil || *c.Error != a.Error || c.CostUSD != 0 || c.InputTokens != 0 {
		t.Errorf("%+v", c)
	}
}

func TestACallOutsideATaskHasNoTaskID(t *testing.T) {
	rec, store, _, _ := recorderWith(t)
	rec.Observe(callctx.With(context.Background(), callctx.Info{UserID: info.UserID}), attempt("free", "laptop", llm.OutcomeOK, 1, 1))
	if store.calls[0].TaskID != nil {
		t.Errorf("task = %v", *store.calls[0].TaskID)
	}
}

func TestACanceledAttemptIsStillRecorded(t *testing.T) {
	rec, store, _, _ := recorderWith(t)
	ctx, cancel := context.WithCancel(callctx.With(context.Background(), info))
	cancel()
	rec.Observe(ctx, attempt("paid", "gpu-box", llm.OutcomeCanceled, 0, 0))
	if len(store.calls) != 1 {
		t.Fatalf("a canceled attempt must be recorded: %d", len(store.calls))
	}
	if store.ctxErr[0] != nil {
		t.Errorf("the write must not inherit the cancellation: %v", store.ctxErr[0])
	}
}

func TestAFailedWriteIsLoggedAndCountedAndNeverSurfaces(t *testing.T) {
	rec, store, m, logs := recorderWith(t)
	store.err = errors.New("connection to the database lost")
	rec.Observe(callctx.With(context.Background(), info), attempt("paid", "gpu-box", llm.OutcomeOK, 10, 10))
	if m.errors != 1 {
		t.Errorf("record errors = %d", m.errors)
	}
	if !strings.Contains(logs.String(), "could not record a model call") || !strings.Contains(logs.String(), "connection to the database lost") {
		t.Errorf("log = %s", logs)
	}
	if len(m.seen) != 1 {
		t.Errorf("the call is still counted in the metrics even if the row could not be written: %v", m.seen)
	}
}

func TestAnAttemptWithoutAUserIsNotRecordedAndIsReported(t *testing.T) {
	rec, store, m, logs := recorderWith(t)
	rec.Observe(context.Background(), attempt("paid", "gpu-box", llm.OutcomeOK, 10, 10))
	rec.Observe(callctx.With(context.Background(), callctx.Info{TaskID: info.TaskID}), attempt("paid", "gpu-box", llm.OutcomeOK, 10, 10))
	if len(store.calls) != 0 || len(m.seen) != 0 {
		t.Errorf("nobody to charge: stored %d, counted %d", len(store.calls), len(m.seen))
	}
	if m.errors != 2 || strings.Count(logs.String(), "without a user") != 2 {
		t.Errorf("errors %d, log: %s", m.errors, logs)
	}
}

func TestNoMetricsIsFine(t *testing.T) {
	store, logs := &fakeStore{}, &bytes.Buffer{}
	rec := llmcalls.NewRecorder(config(t), store, nil, slog.New(slog.NewJSONHandler(logs, nil)))
	rec.Observe(callctx.With(context.Background(), info), attempt("free", "laptop", llm.OutcomeOK, 1, 1))
	rec.Observe(context.Background(), attempt("free", "laptop", llm.OutcomeOK, 1, 1))
	store.err = errors.New("x")
	rec.Observe(callctx.With(context.Background(), info), attempt("free", "laptop", llm.OutcomeOK, 1, 1))
	if len(store.calls) != 1 {
		t.Errorf("stored %d", len(store.calls))
	}
}

// ── the whole path: router, recorder, database, metrics ─────────────────────

func TestAFallbackLeavesAFailedRowAndASuccessfulOneInTheDatabaseAndTheMetrics(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cfg := config(t)
	m := metrics.New()
	recorder := llmcalls.NewRecorder(cfg, f.store, m, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	provider := llmtest.NewProvider(map[string][]llmtest.Result{
		"paid": {llmtest.Fail(llm.FailTimeout, "no first token")},
		"free": {llmtest.OK(`{"ok":true}`, 1000, 200)},
	})
	router, err := llm.NewRouter(cfg, map[string]llm.Provider{"ollama": provider}, llm.WithObserver(recorder), llm.RequireAttribution())
	if err != nil {
		t.Fatal(err)
	}
	callCtx := callctx.With(ctx, callctx.Info{TaskID: f.task.ID, UserID: f.user.ID})
	resp, err := router.Complete(callCtx, workflow.Request{System: "s", Input: "i", Tags: []string{"chat"}})
	if err != nil || resp.Text != `{"ok":true}` {
		t.Fatalf("%+v %v", resp, err)
	}

	rows, err := f.store.ForTask(ctx, f.task.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows = %+v %v", rows, err)
	}
	if rows[0].ModelID != "paid" || rows[0].Outcome != "timeout" || rows[0].Error == nil || *rows[0].Error != "no first token" {
		t.Errorf("failed row = %+v", rows[0])
	}
	if rows[1].ModelID != "free" || rows[1].Outcome != "ok" || rows[1].InputTokens != 1000 || rows[1].CostUSD != 0 {
		t.Errorf("ok row = %+v", rows[1])
	}
	for _, r := range rows {
		if r.UserID != f.user.ID || r.TaskID == nil || *r.TaskID != f.task.ID {
			t.Errorf("every attempt belongs to the task and the user: %+v", r)
		}
	}

	assertNoHeaderLeak(t, "the stored rows", fmt.Sprintf("%+v", rows))
	assertNoHeaderLeak(t, "the metrics", newScrape(t, m))

	usage, _ := f.store.UsageByUserAndModel(ctx, nil)
	if len(usage) != 1 || usage[0].ModelID != "free" || usage[0].InputTokens != 1000 {
		t.Errorf("only the successful attempt counts as usage: %+v", usage)
	}
}

func TestAPaidCallsCostReachesTheDatabaseAndTheMetrics(t *testing.T) {
	f := newFixture(t)
	cfg := config(t)
	m := metrics.New()
	recorder := llmcalls.NewRecorder(cfg, f.store, m, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	provider := llmtest.NewProvider(map[string][]llmtest.Result{"paid": {llmtest.OK("x", 2000, 500)}})
	router, _ := llm.NewRouter(cfg, map[string]llm.Provider{"ollama": provider}, llm.WithObserver(recorder))
	callCtx := callctx.With(context.Background(), callctx.Info{TaskID: f.task.ID, UserID: f.user.ID})
	if _, err := router.Complete(callCtx, workflow.Request{Tags: []string{"chat"}}); err != nil {
		t.Fatal(err)
	}
	rows, _ := f.store.ForTask(context.Background(), f.task.ID)
	wantCost := (2000*3.0 + 500*15.0) / 1e6
	if len(rows) != 1 || !near(rows[0].CostUSD, wantCost) {
		t.Fatalf("rows = %+v, want cost %v", rows, wantCost)
	}
	var body strings.Builder
	rec := newScrape(t, m)
	body.WriteString(rec)
	for _, want := range []string{
		`assistant_llm_calls_total{model="paid",outcome="ok",provider="gpu-box"} 1`,
		`assistant_llm_tokens_total{direction="input",model="paid",provider="gpu-box",user="` + f.user.ID + `"} 2000`,
		`assistant_llm_tokens_total{direction="output",model="paid",provider="gpu-box",user="` + f.user.ID + `"} 500`,
	} {
		if !strings.Contains(body.String(), want+"\n") {
			t.Errorf("missing %s in:\n%s", want, grep(body.String(), "assistant_llm"))
		}
	}
	if !strings.Contains(body.String(), `assistant_llm_cost_usd_total{model="paid",provider="gpu-box",user="`+f.user.ID+`"} 0.0135`) {
		t.Errorf("cost not in metrics:\n%s", grep(body.String(), "assistant_llm_cost"))
	}
}
