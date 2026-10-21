package llmcalls_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"

	"phraseforge/internal/ai"
	"phraseforge/internal/jobs"
	"phraseforge/internal/llmcalls"
	"phraseforge/internal/metrics"
)

const (
	secretPrompt = "PROMPT-SECRET-7f3a"
	secretReply  = "REPLY-SECRET-91bc"
)

const testConfig = `providers:
  - id: gpu-box
    type: ollama
    base_url: "http://gpu-box.lan:11434"
    api_key_from: ""
    headers: {X-Distinctive-Header-Name: distinctive-literal-header-value, X-Env-Header: "env:DISTINCTIVE_HEADER_VARIABLE", X-File-Header: "file:/distinctive-dir/header-file"}
  - {id: laptop, type: ollama, base_url: "http://localhost:11434", api_key_from: ""}
models:
  - {id: paid, provider: gpu-box, model: big, tags: [chat], priority: 20, price: {input_per_mtok: 3, output_per_mtok: 15}}
  - {id: free, provider: laptop, model: small, tags: [chat], priority: 10}
routing:
  breaker: {failures: 1, cooldown_seconds: 60, rate_limited_cooldown_seconds: 30}
`

var t0 = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

var schemaCounter atomic.Int64

// newPool is a pool on a throwaway schema with schema.sql applied
// (PHRASEFORGE_TEST_DSN, skipped without it).
func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHRASEFORGE_TEST_DSN")
	if dsn == "" {
		t.Skip("PHRASEFORGE_TEST_DSN not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	schema := fmt.Sprintf("pf_test_%d_llmcalls_%d", os.Getpid(), schemaCounter.Add(1))
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to schema: %v", err)
	}
	t.Cleanup(pool.Close)
	schemaSQL, err := os.ReadFile("../db/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(schemaSQL)); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return pool
}

type rig struct {
	router   *router.Router
	provider *routertest.Provider
	metrics  *metrics.Metrics
	logs     *bytes.Buffer
}

func newRig(t *testing.T, pool *pgxpool.Pool, scripts map[string][]routertest.Result) *rig {
	t.Helper()
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{provider: routertest.NewProvider(scripts), metrics: metrics.New(), logs: &bytes.Buffer{}}
	recorder := llmcalls.NewRecorder(cfg, llmcalls.NewStore(pool), r.metrics, slog.New(slog.NewJSONHandler(r.logs, nil)))
	clock := routertest.NewClock(t0)
	clock.Step = 2 * time.Second
	r.router, err = router.NewRouter(cfg, map[string]router.Provider{"ollama": r.provider}, router.WithClock(clock), router.WithObserver(recorder))
	if err != nil {
		t.Fatal(err)
	}
	r.metrics.SetModelAvailability(func() []metrics.ModelAvailability {
		var out []metrics.ModelAvailability
		for _, s := range r.router.Status() {
			out = append(out, metrics.ModelAvailability{Model: s.ID, Available: s.Available})
		}
		return out
	})
	return r
}

func (r *rig) call(ctx context.Context) (router.Response, error) {
	return r.router.Complete(ctx, router.Request{Messages: []router.Message{{Role: router.RoleUser, Content: secretPrompt}}, Tags: []string{"chat"}})
}

func (r *rig) scrape(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	r.metrics.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	return string(body)
}

func serverFailure() routertest.Result {
	return routertest.Result{Err: &router.Failure{Kind: router.FailServer, Err: errors.New("overloaded"), HTTPStatus: 529, RequestID: "req_abc"}}
}

type row struct {
	jobID, kind, source, target, model, provider, outcome string
	input, output, duration                               int
	cost                                                  float64
	tags                                                  []string
	errText, requestID                                    *string
	httpStatus                                            *int
}

func rows(t *testing.T, pool *pgxpool.Pool) []row {
	t.Helper()
	res, err := pool.Query(context.Background(), `SELECT coalesce(job_id::text, ''), kind, source_language, target_language, model_id, provider_id, outcome,
		input_tokens, output_tokens, duration_ms, cost_usd::float8, tags, error, request_id, http_status FROM llm_calls ORDER BY started_at, model_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	var out []row
	for res.Next() {
		var r row
		if err := res.Scan(&r.jobID, &r.kind, &r.source, &r.target, &r.model, &r.provider, &r.outcome, &r.input, &r.output, &r.duration, &r.cost, &r.tags, &r.errText, &r.requestID, &r.httpStatus); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

const jobID = "11111111-2222-4333-8444-555555555555"

func TestAFallbackLeavesARowPerAttemptWithCostAndContext(t *testing.T) {
	pool := newPool(t)
	r := newRig(t, pool, map[string][]routertest.Result{
		"paid": {serverFailure()},
		"free": {routertest.OK(secretReply, 1000, 200)},
	})
	ctx := ai.WithCallDetails(jobs.WithJobID(context.Background(), jobID), "vocabulary_item", "deu", "pol")
	if _, err := r.call(ctx); err != nil {
		t.Fatal(err)
	}
	got := rows(t, pool)
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2 (the failed attempt and the fallback): %+v", len(got), got)
	}
	failed, ok := got[0], got[1]
	if failed.model != "paid" || failed.outcome != "error" || failed.provider != "gpu-box" || failed.cost != 0 {
		t.Errorf("failed attempt = %+v", failed)
	}
	if failed.httpStatus == nil || *failed.httpStatus != 529 || failed.requestID == nil || *failed.requestID != "req_abc" || failed.errText == nil {
		t.Errorf("failed attempt lacks its troubleshooting fields: %+v", failed)
	}
	if ok.model != "free" || ok.outcome != "ok" || ok.input != 1000 || ok.output != 200 || ok.errText != nil || ok.httpStatus != nil || ok.requestID != nil {
		t.Errorf("fallback attempt = %+v", ok)
	}
	for _, x := range got {
		if x.jobID != jobID || x.kind != "vocabulary_item" || x.source != "deu" || x.target != "pol" || strings.Join(x.tags, ",") != "chat" || x.duration <= 0 {
			t.Errorf("row context = %+v", x)
		}
	}
}

func TestCostIsTokensTimesThePriceOfTheModel(t *testing.T) {
	pool := newPool(t)
	r := newRig(t, pool, map[string][]routertest.Result{"paid": {routertest.OK("x", 1200, 300)}})
	if _, err := r.call(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := rows(t, pool)
	want := (1200*3.0 + 300*15.0) / 1e6
	if len(got) != 1 || got[0].cost < want-1e-9 || got[0].cost > want+1e-9 {
		t.Fatalf("rows = %+v, want one costing %v", got, want)
	}
	wantMetric := fmt.Sprintf("phraseforge_llm_cost_usd_total{model=%q,provider=%q} %v\n", "paid", "gpu-box", want)
	if body := r.scrape(t); !strings.Contains(body, wantMetric) {
		t.Errorf("missing %q in:\n%s", wantMetric, body)
	}
}

func TestAnInteractiveCallHasNoJobAndEmptyDetails(t *testing.T) {
	pool := newPool(t)
	r := newRig(t, pool, map[string][]routertest.Result{"paid": {routertest.OK("x", 1, 1)}})
	if _, err := r.call(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := rows(t, pool)
	if len(got) != 1 || got[0].jobID != "" || got[0].kind != "" || got[0].source != "" || got[0].target != "" {
		t.Errorf("rows = %+v", got)
	}
}

func TestACanceledAttemptIsStillRecorded(t *testing.T) {
	pool := newPool(t)
	r := newRig(t, pool, map[string][]routertest.Result{"paid": {{Block: true}}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := r.call(ctx); done <- err }()
	for len(r.provider.Calls()) == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("a canceled call succeeded")
	}
	got := rows(t, pool)
	if len(got) != 1 || got[0].outcome != "canceled" || got[0].model != "paid" {
		t.Errorf("rows = %+v, want one canceled attempt", got)
	}
}

func TestNoPromptOrReplyIsStoredOrExposed(t *testing.T) {
	pool := newPool(t)
	r := newRig(t, pool, map[string][]routertest.Result{"paid": {routertest.OK(secretReply, 10, 10)}})
	ctx := ai.WithCallDetails(jobs.WithJobID(context.Background(), jobID), "title", "deu", "pol")
	if _, err := r.call(ctx); err != nil {
		t.Fatal(err)
	}

	columns, err := pool.Query(context.Background(), `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'llm_calls'`)
	if err != nil {
		t.Fatal(err)
	}
	for columns.Next() {
		var name string
		if err := columns.Scan(&name); err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"prompt", "reply", "response", "payload", "message", "content", "input_text", "output_text"} {
			if strings.Contains(name, banned) {
				t.Errorf("column %q may hold content", name)
			}
		}
	}
	columns.Close()

	var dump string
	if err := pool.QueryRow(context.Background(), `SELECT coalesce(string_agg(t::text, E'\n'), '') FROM llm_calls t`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"X-Distinctive-Header-Name", "distinctive-literal-header-value", "X-Env-Header", "DISTINCTIVE_HEADER_VARIABLE", "X-File-Header", "/distinctive-dir/header-file"} {
		if strings.Contains(dump, s) || strings.Contains(r.scrape(t), s) || strings.Contains(r.logs.String(), s) {
			t.Errorf("the header detail %q leaked into the table, the metrics or the log", s)
		}
	}
	for _, s := range []string{secretPrompt, secretReply} {
		if strings.Contains(dump, s) || strings.Contains(r.scrape(t), s) || strings.Contains(r.logs.String(), s) {
			t.Errorf("%q leaked into the table, the metrics or the log", s)
		}
	}
}

func TestAFailingDatabaseNeverFailsTheCall(t *testing.T) {
	pool := newPool(t)
	r := newRig(t, pool, map[string][]routertest.Result{"paid": {routertest.OK(secretReply, 100, 50)}})
	pool.Close()
	resp, err := r.call(context.Background())
	if err != nil || resp.Text != secretReply {
		t.Fatalf("call = %q, %v; the failed record must not reach the caller", resp.Text, err)
	}
	body := r.scrape(t)
	if !strings.Contains(body, "phraseforge_llm_record_errors_total 1\n") {
		t.Errorf("the record error is not counted:\n%s", body)
	}
	if !strings.Contains(body, `phraseforge_llm_calls_total{model="paid",outcome="ok",provider="gpu-box"} 1`+"\n") {
		t.Errorf("the attempt must still be counted in the metrics:\n%s", body)
	}
	logs := r.logs.String()
	if !strings.Contains(logs, "could not record a model call") || strings.Contains(logs, secretPrompt) || strings.Contains(logs, secretReply) {
		t.Errorf("log = %s", logs)
	}
}

func TestMetricsCountAttemptsAndTheAvailabilityGaugeFollowsTheBreaker(t *testing.T) {
	pool := newPool(t)
	r := newRig(t, pool, map[string][]routertest.Result{
		"paid": {serverFailure()},
		"free": {routertest.OK(secretReply, 1000, 200)},
	})
	before := r.scrape(t)
	for _, line := range []string{`phraseforge_llm_model_available{model="paid"} 1`, `phraseforge_llm_model_available{model="free"} 1`} {
		if !strings.Contains(before, line+"\n") {
			t.Errorf("before any failure, missing %q", line)
		}
	}
	ctx := ai.WithCallDetails(jobs.WithJobID(context.Background(), jobID), "title", "deu", "pol")
	if _, err := r.call(ctx); err != nil {
		t.Fatal(err)
	}
	body := r.scrape(t)
	for _, line := range []string{
		`phraseforge_llm_calls_total{model="paid",outcome="error",provider="gpu-box"} 1`,
		`phraseforge_llm_calls_total{model="free",outcome="ok",provider="laptop"} 1`,
		`phraseforge_llm_tokens_total{direction="input",model="free",provider="laptop"} 1000`,
		`phraseforge_llm_tokens_total{direction="output",model="free",provider="laptop"} 200`,
		`phraseforge_llm_call_duration_seconds_count{model="paid",provider="gpu-box"} 1`,
		`phraseforge_llm_model_available{model="paid"} 0`,
		`phraseforge_llm_model_available{model="free"} 1`,
	} {
		if !strings.Contains(body, line+"\n") {
			t.Errorf("missing %q in:\n%s", line, body)
		}
	}
	// The labels are the configured ids; nothing of the call leaks into them.
	for _, s := range []string{secretPrompt, secretReply, jobID, "vocabulary_item", "title", "deu", "pol", "overloaded", "req_abc"} {
		for _, l := range strings.Split(body, "\n") {
			if strings.HasPrefix(l, "phraseforge_") && strings.Contains(l, s) {
				t.Errorf("metric line %q carries %q", l, s)
			}
		}
	}
}

func TestACallIsRecordedForTheUserInTheContextAndNoneWithoutOne(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()
	var ada int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(username, password_hash) VALUES('ada', 'x') RETURNING id`).Scan(&ada); err != nil {
		t.Fatal(err)
	}
	r := newRig(t, pool, map[string][]routertest.Result{"paid": {routertest.OK("x", 1, 1), routertest.OK("y", 1, 1)}})
	if _, err := r.call(jobs.WithUserID(ctx, ada)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.call(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := pool.Query(ctx, `SELECT user_id FROM llm_calls ORDER BY started_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	var got []*int64
	for res.Next() {
		var u *int64
		if err := res.Scan(&u); err != nil {
			t.Fatal(err)
		}
		got = append(got, u)
	}
	if len(got) != 2 || got[0] == nil || *got[0] != ada || got[1] != nil {
		t.Errorf("users = %v, want [%d, none]", got, ada)
	}
}
