package llmcalls_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"k8s-lab/shared/retention"

	"knowledge/internal/db"
	"knowledge/internal/llmcalls"
	"knowledge/internal/metrics"
)

var schemaCounter atomic.Int64

// newPool is a pool on a throwaway schema with the knowledge schema applied
// (KNOWLEDGE_TEST_DSN, skipped without it: those tests need a Postgres).
func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("KNOWLEDGE_TEST_DSN")
	if dsn == "" {
		t.Skip("KNOWLEDGE_TEST_DSN not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	schema := fmt.Sprintf("kb_test_%d_llmcalls_%d", os.Getpid(), schemaCounter.Add(1))
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
	if _, err := pool.Exec(ctx, db.SchemaSQL()); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return pool
}

func countRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM llm_calls").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func daysAgo(d int) time.Time { return now.AddDate(0, 0, -d) }

func insertAt(t *testing.T, store *llmcalls.Store, started time.Time) {
	t.Helper()
	err := store.Insert(context.Background(), llmcalls.Call{ModelID: "m", ProviderID: "p", ModelName: "n", StartedAt: started, Outcome: "ok"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInsertStoresEveryColumnAndAllowsNulls(t *testing.T) {
	pool := newPool(t)
	store := llmcalls.NewStore(pool)
	op, msg, status, req := "11111111-2222-3333-4444-555555555555", "boom", 503, "req-1"
	full := llmcalls.Call{OperationID: &op, Kind: "chat", ModelID: "m", ProviderID: "p", ModelName: "n", Tags: []string{"chat", "big"},
		InputTokens: 12, OutputTokens: 3, CostUSD: 0.5, StartedAt: now, DurationMS: 42, Outcome: "error", Error: &msg, HTTPStatus: &status, RequestID: &req}
	if err := store.Insert(context.Background(), full); err != nil {
		t.Fatal(err)
	}
	// A call outside the queue, with no tags and no error.
	if err := store.Insert(context.Background(), llmcalls.Call{Kind: "embed", ModelID: "m", ProviderID: "p", ModelName: "n", StartedAt: now, Outcome: "ok"}); err != nil {
		t.Fatalf("a call with no operation, tags or error must be storable: %v", err)
	}
	var gotOp *string
	var tags []string
	var in, out, dur int
	var kind, outcome string
	if err := pool.QueryRow(context.Background(), `SELECT operation_id::text, kind, tags, input_tokens, output_tokens, duration_ms, outcome FROM llm_calls WHERE kind = 'chat'`).
		Scan(&gotOp, &kind, &tags, &in, &out, &dur, &outcome); err != nil {
		t.Fatal(err)
	}
	if gotOp == nil || *gotOp != op || len(tags) != 2 || in != 12 || out != 3 || dur != 42 || outcome != "error" {
		t.Errorf("row = op %v, tags %v, %d/%d tokens, %d ms, %s; want what was inserted", gotOp, tags, in, out, dur, outcome)
	}
	var nullOp bool
	if err := pool.QueryRow(context.Background(), `SELECT operation_id IS NULL FROM llm_calls WHERE kind = 'embed'`).Scan(&nullOp); err != nil || !nullOp {
		t.Errorf("operation_id of the embed row should be NULL (err %v)", err)
	}
}

func TestTheTableRefusesBadRows(t *testing.T) {
	pool := newPool(t)
	store := llmcalls.NewStore(pool)
	bad := []llmcalls.Call{
		{ModelID: "m", ProviderID: "p", ModelName: "n", StartedAt: now, Outcome: "weird"},
		{ModelID: "m", ProviderID: "p", ModelName: "n", StartedAt: now, Outcome: "ok", InputTokens: -1},
		{ModelID: "m", ProviderID: "p", ModelName: "n", StartedAt: now, Outcome: "ok", DurationMS: -5},
		{ModelID: "m", ProviderID: "p", ModelName: "n", StartedAt: now, Outcome: "ok", CostUSD: -0.1},
	}
	for i, c := range bad {
		if err := store.Insert(context.Background(), c); err == nil {
			t.Errorf("bad row %d was accepted", i)
		}
	}
	if n := countRows(t, pool); n != 0 {
		t.Errorf("%d rows stored, want none", n)
	}
}

func TestThePurgerDeletesRowsOlderThanTheRetentionAndKeepsNewerOnes(t *testing.T) {
	pool := newPool(t)
	store := llmcalls.NewStore(pool)
	for _, d := range []int{45, 31, 29, 1} {
		insertAt(t, store, daysAgo(d))
	}
	m, logs := metrics.New(), &bytes.Buffer{}
	p, err := retention.NewPurger(pool, []retention.Rule{llmcalls.RetentionRule(30 * 24 * time.Hour)},
		retention.WithClock(func() time.Time { return now }), retention.WithOnPurged(m.Purged),
		retention.WithLogger(slog.New(slog.NewJSONHandler(logs, nil))))
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := p.PurgeOnce(context.Background())
	if err != nil || deleted["llm_calls"] != 2 {
		t.Fatalf("PurgeOnce = %v, %v; want llm_calls=2, nil", deleted, err)
	}
	if n := countRows(t, pool); n != 2 {
		t.Errorf("%d rows left, want the 2 newer ones", n)
	}
	if !strings.Contains(scrape(m), "knowledge_llm_calls_purged_total 2\n") {
		t.Errorf("the purge is not counted:\n%s", scrape(m))
	}
}

func TestPurgeWorksInBatchesAndACutoffOfNowDeletesEverything(t *testing.T) {
	pool := newPool(t)
	store := llmcalls.NewStore(pool, llmcalls.WithBatchSize(2), llmcalls.WithBatchPause(0))
	for i := 0; i < 5; i++ {
		insertAt(t, store, daysAgo(40+i))
	}
	n, err := store.DeleteOlderThan(context.Background(), now)
	if err != nil || n != 5 {
		t.Fatalf("DeleteOlderThan = %d, %v; want all 5 in batches of 2", n, err)
	}
	if left := countRows(t, pool); left != 0 {
		t.Errorf("%d rows left", left)
	}
}

func TestARetentionOfZeroKeepsEveryRow(t *testing.T) {
	pool := newPool(t)
	store := llmcalls.NewStore(pool)
	insertAt(t, store, daysAgo(3650))
	p, err := retention.NewPurger(pool, []retention.Rule{llmcalls.RetentionRule(0)}, retention.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.PurgeOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, pool); n != 1 {
		t.Errorf("%d rows, want the old row kept (retention 0 keeps forever)", n)
	}
}
