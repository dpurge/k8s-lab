package llmcalls_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"

	"knowledge/internal/config"
	"knowledge/internal/llmcalls"
	"knowledge/internal/metrics"
	"knowledge/internal/modelrouter"
	"knowledge/internal/queue"
)

const testConfig = `providers:
  - {id: gpu-box, type: ollama, base_url: "http://gpu-box.lan:11434", api_key_from: ""}
  - {id: laptop, type: ollama, base_url: "http://localhost:11434", api_key_from: ""}
models:
  - {id: paid, provider: gpu-box, model: big, tags: [chat], priority: 20, price: {input_per_mtok: 3, output_per_mtok: 15}}
  - {id: free, provider: laptop, model: small, tags: [chat], priority: 10}
  - {id: bge-gpu, provider: gpu-box, model: bge-m3, tags: [embed], priority: 20, embedding_space: bge-m3, dimension: 3, price: {input_per_mtok: 1}}
routing:
  breaker: {failures: 1, cooldown_seconds: 60, rate_limited_cooldown_seconds: 30}
`

// fakeStore records the calls it is given, or fails them.
type fakeStore struct {
	mu    sync.Mutex
	calls []llmcalls.Call
	err   error
	// ctxErr is the context's error at the time of the last insert.
	ctxErr error
}

func (s *fakeStore) Insert(ctx context.Context, c llmcalls.Call) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctxErr = ctx.Err()
	if s.err != nil {
		return s.err
	}
	s.calls = append(s.calls, c)
	return nil
}

func (s *fakeStore) all() []llmcalls.Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]llmcalls.Call(nil), s.calls...)
}

type rig struct {
	router   *router.Router
	provider *routertest.Provider
	store    *fakeStore
	metrics  *metrics.Metrics
	logs     *bytes.Buffer
}

func newRig(t *testing.T, scripts map[string][]routertest.Result) *rig {
	t.Helper()
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{provider: routertest.NewProvider(scripts), store: &fakeStore{}, metrics: metrics.New(), logs: &bytes.Buffer{}}
	recorder := llmcalls.NewRecorder(cfg, r.store, r.metrics, slog.New(slog.NewJSONHandler(r.logs, nil)))
	r.router, err = router.NewRouter(cfg, map[string]router.Provider{"ollama": r.provider}, router.WithObserver(recorder))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) chat(ctx context.Context) error {
	_, err := modelrouter.New(r.router, modelrouter.KindChat, configPurpose("chat")).Complete(ctx,
		[]modelrouter.Message{{Role: "user", Content: "PROMPT-SECRET-7f3a"}})
	return err
}

func scrape(m *metrics.Metrics) string {
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	return string(body)
}

func TestAFallbackLeavesARowPerAttemptWithCostKindAndOperation(t *testing.T) {
	r := newRig(t, map[string][]routertest.Result{
		"paid": {routertest.Fail(router.FailServer, "gpu box is down")},
		"free": {routertest.OK("REPLY-SECRET-91bc", 100, 20)},
	})
	ctx := queue.WithOperationID(context.Background(), "11111111-2222-3333-4444-555555555555")
	if err := r.chat(ctx); err != nil {
		t.Fatal(err)
	}
	calls := r.store.all()
	if len(calls) != 2 {
		t.Fatalf("%d rows, want one per attempt (2)", len(calls))
	}
	first, second := calls[0], calls[1]
	if first.ModelID != "paid" || first.Outcome != router.OutcomeError || first.Error == nil || !strings.Contains(*first.Error, "gpu box is down") {
		t.Errorf("first = %+v, want the failed attempt on paid with its error", first)
	}
	if second.ModelID != "free" || second.Outcome != router.OutcomeOK || second.InputTokens != 100 || second.OutputTokens != 20 || second.Error != nil {
		t.Errorf("second = %+v, want the ok attempt on free with 100/20 tokens", second)
	}
	for _, c := range calls {
		if c.Kind != modelrouter.KindChat || c.OperationID == nil || *c.OperationID != "11111111-2222-3333-4444-555555555555" || c.ProviderID == "" || c.ModelName == "" || len(c.Tags) != 1 {
			t.Errorf("row %+v: want the kind, the queue operation, provider, model name and tags", c)
		}
	}
	if first.CostUSD != 0 || second.CostUSD != 0 {
		t.Errorf("costs = %v and %v, want 0 (the failed paid attempt used no tokens; free costs nothing)", first.CostUSD, second.CostUSD)
	}
}

func TestCostIsTokensTimesThePriceOfTheModel(t *testing.T) {
	r := newRig(t, map[string][]routertest.Result{"paid": {routertest.OK("x", 1_000_000, 100_000)}})
	if err := r.chat(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 1M input tokens at $3 plus 100k output tokens at $15 per million.
	if got := r.store.all()[0].CostUSD; got < 4.4999 || got > 4.5001 {
		t.Errorf("cost = %v, want 4.5", got)
	}
	if !strings.Contains(scrape(r.metrics), `knowledge_llm_cost_usd_total{model="paid",provider="gpu-box"} 4.5`) {
		t.Errorf("the cost is not in the metrics:\n%s", scrape(r.metrics))
	}
}

func TestACallOutsideTheQueueHasNoOperation(t *testing.T) {
	r := newRig(t, nil)
	if err := r.chat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := r.store.all()[0]; c.OperationID != nil {
		t.Errorf("OperationID = %v, want nil outside a queue operation", *c.OperationID)
	}
}

func TestAnEmbeddingAttemptIsRecordedAsEmbedWithItsTokensAndCost(t *testing.T) {
	r := newRig(t, map[string][]routertest.Result{"bge-gpu": {routertest.Embedding(3, 8)}})
	emb := modelrouter.NewEmbedder(r.router, "bge-m3", 3)
	if _, err := emb.Embed(context.Background(), "TEXT-SECRET-5d1e"); err != nil {
		t.Fatal(err)
	}
	c := r.store.all()[0]
	if c.Kind != modelrouter.KindEmbed || c.ModelID != "bge-gpu" || c.InputTokens != 8 || c.OutputTokens != 0 || c.OperationID != nil || len(c.Tags) != 1 || c.Tags[0] != "embedding_space=bge-m3" {
		t.Errorf("row = %+v, want an embed row on bge-gpu, 8 input tokens, no operation, tagged with the space", c)
	}
	if c.CostUSD < 0.000007 || c.CostUSD > 0.000009 {
		t.Errorf("cost = %v, want 8 tokens at $1 per million", c.CostUSD)
	}
	// A direct router call, without the adapter's context, is still recognised as an embedding.
	if _, err := r.router.Embed(context.Background(), router.EmbedRequest{Space: "bge-m3", Input: "x"}); err != nil {
		t.Fatal(err)
	}
	if last := r.store.all()[1]; last.Kind != modelrouter.KindEmbed {
		t.Errorf("kind = %q, want embed from the attempt itself", last.Kind)
	}
}

func TestACanceledAttemptIsStillRecorded(t *testing.T) {
	r := newRig(t, map[string][]routertest.Result{"paid": {{Block: true}}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := r.chat(ctx); err == nil {
		t.Fatal("want the context's error")
	}
	calls := r.store.all()
	if len(calls) != 1 || calls[0].Outcome != router.OutcomeCanceled {
		t.Fatalf("rows = %+v, want one canceled attempt", calls)
	}
	if r.store.ctxErr != nil {
		t.Errorf("the insert ran with a finished context (%v): a canceled attempt would be lost", r.store.ctxErr)
	}
}

func TestNoPromptReplyOrTextIsStoredLoggedOrExposed(t *testing.T) {
	r := newRig(t, map[string][]routertest.Result{
		"paid":    {routertest.Fail(router.FailServer, "boom")},
		"free":    {routertest.OK("REPLY-SECRET-91bc", 5, 5)},
		"bge-gpu": {routertest.Embedding(3, 2)},
	})
	if err := r.chat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := modelrouter.NewEmbedder(r.router, "bge-m3", 3).Embed(context.Background(), "TEXT-SECRET-5d1e"); err != nil {
		t.Fatal(err)
	}
	everything := scrape(r.metrics) + r.logs.String()
	for _, c := range r.store.all() {
		everything += c.Kind + c.ModelID + c.ProviderID + c.ModelName + strings.Join(c.Tags, ",")
		if c.Error != nil {
			everything += *c.Error
		}
	}
	for _, secret := range []string{"PROMPT-SECRET-7f3a", "REPLY-SECRET-91bc", "TEXT-SECRET-5d1e"} {
		if strings.Contains(everything, secret) {
			t.Errorf("%q leaked into a record, a log line or the metrics", secret)
		}
	}
}

func TestAFailingDatabaseNeverFailsTheCallAndIsCounted(t *testing.T) {
	r := newRig(t, nil)
	r.store.err = errors.New("connection refused")
	if err := r.chat(context.Background()); err != nil {
		t.Fatalf("the call failed because recording did: %v", err)
	}
	if !strings.Contains(scrape(r.metrics), "knowledge_llm_record_errors_total 1\n") {
		t.Errorf("the failed write is not counted:\n%s", scrape(r.metrics))
	}
	if !strings.Contains(r.logs.String(), "could not record a model call") || !strings.Contains(r.logs.String(), "connection refused") {
		t.Errorf("the failed write is not logged: %s", r.logs.String())
	}
	if !strings.Contains(scrape(r.metrics), `knowledge_llm_calls_total{model="paid",outcome="ok",provider="gpu-box"} 1`) {
		t.Errorf("the attempt must still be counted in the metrics:\n%s", scrape(r.metrics))
	}
}

func TestTheAvailabilityGaugeFollowsTheBreaker(t *testing.T) {
	r := newRig(t, map[string][]routertest.Result{"paid": {routertest.Fail(router.FailServer, "boom")}, "free": {routertest.OK("x", 1, 1)}})
	r.metrics.SetModelAvailability(func() []metrics.ModelAvailability {
		var out []metrics.ModelAvailability
		for _, s := range r.router.Status() {
			out = append(out, metrics.ModelAvailability{Model: s.ID, Available: s.Available})
		}
		return out
	})
	if !strings.Contains(scrape(r.metrics), `knowledge_llm_model_available{model="paid"} 1`) {
		t.Fatalf("paid should start available:\n%s", scrape(r.metrics))
	}
	if err := r.chat(context.Background()); err != nil { // one failure trips the breaker (failures: 1)
		t.Fatal(err)
	}
	body := scrape(r.metrics)
	if !strings.Contains(body, `knowledge_llm_model_available{model="paid"} 0`) || !strings.Contains(body, `knowledge_llm_model_available{model="free"} 1`) {
		t.Errorf("the gauge should show paid held out and free available:\n%s", body)
	}
}

func configPurpose(tags ...string) config.PurposeConfig { return config.PurposeConfig{ModelTags: tags} }
