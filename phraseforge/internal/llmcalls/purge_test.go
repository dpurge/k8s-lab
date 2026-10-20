package llmcalls_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s-lab/shared/retention"

	"phraseforge/internal/llmcalls"
	"phraseforge/internal/metrics"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func clock() time.Time { return now }

func daysAgo(d int) time.Time { return now.AddDate(0, 0, -d) }

func insertAt(t *testing.T, store *llmcalls.Store, started time.Time) {
	t.Helper()
	err := store.Insert(context.Background(), llmcalls.Call{ModelID: "m", ProviderID: "p", ModelName: "n", StartedAt: started, Outcome: "ok"})
	if err != nil {
		t.Fatal(err)
	}
}

func scrapeMetrics(m *metrics.Metrics) string {
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	return string(body)
}

func TestThePurgerDeletesRowsOlderThanTheRetentionAndKeepsNewerOnes(t *testing.T) {
	pool := newPool(t)
	store := llmcalls.NewStore(pool)
	for _, d := range []int{45, 31, 29, 1} {
		insertAt(t, store, daysAgo(d))
	}
	m, logs := metrics.New(), &bytes.Buffer{}
	p, err := retention.NewPurger(pool, []retention.Rule{llmcalls.RetentionRule(30 * 24 * time.Hour)},
		retention.WithClock(clock), retention.WithOnPurged(m.Purged), retention.WithLogger(slog.New(slog.NewJSONHandler(logs, nil))))
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := p.PurgeOnce(context.Background())
	if err != nil || deleted["llm_calls"] != 2 {
		t.Fatalf("PurgeOnce = %v, %v; want llm_calls=2, nil", deleted, err)
	}
	if got := rows(t, pool); len(got) != 2 {
		t.Errorf("%d rows left, want the 2 newer ones", len(got))
	}
	if !strings.Contains(scrapeMetrics(m), "phraseforge_llm_calls_purged_total 2\n") {
		t.Errorf("the purged counter does not show 2:\n%s", scrapeMetrics(m))
	}
	line := logs.String()
	for _, want := range []string{`"msg":"rows purged"`, `"rule":"llm_calls"`, `"deleted":2`, `"retention_days":30`, `"older_than":"2026-09-20T12:00:00Z"`} {
		if !strings.Contains(line, want) {
			t.Errorf("log %s lacks %s", line, want)
		}
	}
}

func TestPurgeWorksInBatches(t *testing.T) {
	pool := newPool(t)
	store := llmcalls.NewStore(pool, llmcalls.WithBatchSize(2), llmcalls.WithBatchPause(0))
	for d := 40; d < 47; d++ { // 7 old rows: batches of 2, 2, 2, 1
		insertAt(t, store, daysAgo(d))
	}
	insertAt(t, store, daysAgo(1))
	deleted, err := store.DeleteOlderThan(context.Background(), daysAgo(30))
	if err != nil || deleted != 7 {
		t.Fatalf("DeleteOlderThan = %d, %v; want 7, nil", deleted, err)
	}
	if got := rows(t, pool); len(got) != 1 {
		t.Errorf("%d rows left, want 1", len(got))
	}
}

func TestPurgeOfExactlyOneFullBatchEndsWithAnEmptyOne(t *testing.T) {
	pool := newPool(t)
	store := llmcalls.NewStore(pool, llmcalls.WithBatchSize(2), llmcalls.WithBatchPause(0))
	insertAt(t, store, daysAgo(40))
	insertAt(t, store, daysAgo(41))
	if deleted, err := store.DeleteOlderThan(context.Background(), daysAgo(30)); err != nil || deleted != 2 {
		t.Errorf("DeleteOlderThan = %d, %v; want 2, nil", deleted, err)
	}
}

func TestACutoffOfNowDeletesEverything(t *testing.T) {
	pool := newPool(t)
	store := llmcalls.NewStore(pool)
	insertAt(t, store, daysAgo(0).Add(-time.Second))
	insertAt(t, store, daysAgo(10))
	if deleted, err := store.DeleteOlderThan(context.Background(), now); err != nil || deleted != 2 {
		t.Errorf("DeleteOlderThan(now) = %d, %v; want 2, nil", deleted, err)
	}
}

func TestStoreStopsBetweenBatchesWhenTheContextIsCanceled(t *testing.T) {
	pool := newPool(t)
	store := llmcalls.NewStore(pool, llmcalls.WithBatchSize(1), llmcalls.WithBatchPause(time.Hour))
	insertAt(t, store, daysAgo(40))
	insertAt(t, store, daysAgo(41))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	deleted, err := store.DeleteOlderThan(ctx, daysAgo(30))
	if !errors.Is(err, context.Canceled) || deleted != 1 {
		t.Errorf("DeleteOlderThan = %d, %v; want 1 and context.Canceled", deleted, err)
	}
}

func TestRetentionOfZeroKeepsEveryRow(t *testing.T) {
	pool := newPool(t)
	store := llmcalls.NewStore(pool)
	insertAt(t, store, daysAgo(400))
	p, _ := retention.NewPurger(pool, []retention.Rule{llmcalls.RetentionRule(0)}, retention.WithClock(clock),
		retention.WithLogger(slog.New(slog.NewJSONHandler(io.Discard, nil))))
	if deleted, err := p.PurgeOnce(context.Background()); err != nil || len(deleted) != 0 {
		t.Errorf("PurgeOnce = %v, %v; want nothing", deleted, err)
	}
	if got := rows(t, pool); len(got) != 1 {
		t.Errorf("%d rows left, want 1", len(got))
	}
}
