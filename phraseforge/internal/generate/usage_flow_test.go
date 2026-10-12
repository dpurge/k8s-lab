package generate

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"phraseforge/internal/ai"
	"phraseforge/internal/config"
	"phraseforge/internal/jobs"
	"phraseforge/internal/vocabulary"
)

// The item job's usage limit lives in ai (which does not import the
// vocabulary store); it must stay the examples' limit.
func TestUsageLimitIsTheExampleLimit(t *testing.T) {
	if ai.MaxUsageChars != vocabulary.MaxExampleChars {
		t.Fatalf("ai.MaxUsageChars = %d, vocabulary.MaxExampleChars = %d, want them equal", ai.MaxUsageChars, vocabulary.MaxExampleChars)
	}
}

func TestUsagesByPositionPairsStoredItemsWithTheirCandidates(t *testing.T) {
	candidates := []Candidate{{Usage: "one"}, {}, {Usage: "three"}}
	stored := make([]vocabulary.Item, 3)
	if got := usagesByPosition(stored, candidates); !reflect.DeepEqual(got, []string{"one", "", "three"}) {
		t.Errorf("usages = %q, want one, none, three", got)
	}
	// A count that doesn't match can't be paired safely: nobody gets a usage.
	if got := usagesByPosition(stored[:2], candidates); !reflect.DeepEqual(got, []string{"", ""}) {
		t.Errorf("usages for a mismatched count = %q, want none", got)
	}
}

var generateDBSchemaCounter atomic.Int64

// newGenerateDB is a generate Service over a real database: the real vocabulary
// store, ai service and job service on a throwaway schema (PHRASEFORGE_TEST_DSN,
// skipped without it). Nothing runs a job worker, so queueing a job calls no model.
func newGenerateDB(t *testing.T) (*Service, *pgxpool.Pool, int64) {
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
	schema := fmt.Sprintf("pf_test_%d_%d", os.Getpid(), generateDBSchemaCounter.Add(1))
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
		t.Fatalf("apply schema.sql: %v", err)
	}
	var userID int64
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ('tester', 'x') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	aiSvc := ai.New(pool, config.Config{Providers: map[string]config.ProviderConfig{"ollama": {}}}, nil, nil, nil, nil)
	return New(nil, nil, vocabulary.New(pool), nil, aiSvc, jobs.New(pool), pool), pool, userID
}

func TestSyncVocabItemsQueuesItemJobsCarryingTheirCandidatesUsage(t *testing.T) {
	svc, pool, uid := newGenerateDB(t)
	ctx := context.Background()
	listID, err := svc.vocab.Create(ctx, uid, "Test", "arb", "arab")
	if err != nil {
		t.Fatal(err)
	}
	candidates := []Candidate{
		{Item: vocabulary.Item{Phrase: "كتاب"}, Usage: "كِتَابٌ جَدِيدٌ"},
		{Item: vocabulary.Item{Phrase: "قلم"}}, // no usage
		{Item: vocabulary.Item{Phrase: "بيت"}, Usage: "بَيْتٌ كَبِيرٌ"},
	}

	if err := svc.syncVocabItems(ctx, listID, "arb", candidates); err != nil {
		t.Fatal(err)
	}

	items, err := svc.vocab.Items(ctx, listID)
	if err != nil || len(items) != len(candidates) {
		t.Fatalf("items = %+v (err %v), want %d", items, err, len(candidates))
	}
	type queued struct {
		locale   string
		hasUsage bool
		usage    string
	}
	rows, err := pool.Query(ctx, `
		SELECT (payload->>'item_id')::bigint, payload->>'locale', payload ? 'usage', coalesce(payload->>'usage', '')
		FROM jobs WHERE kind = 'generate_vocabulary_item' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	byItem := map[int64][]queued{}
	for rows.Next() {
		var itemID int64
		var q queued
		if err := rows.Scan(&itemID, &q.locale, &q.hasUsage, &q.usage); err != nil {
			t.Fatal(err)
		}
		byItem[itemID] = append(byItem[itemID], q)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for i, it := range items {
		got := byItem[it.ID]
		if len(got) != 2 {
			t.Fatalf("item %d (%q) has %d jobs, want one per site locale (2): %+v", it.ID, it.Phrase, len(got), got)
		}
		for _, q := range got {
			if q.usage != candidates[i].Usage || q.hasUsage != (candidates[i].Usage != "") {
				t.Errorf("item %q job %+v, want usage %q (key present: %v)", it.Phrase, q, candidates[i].Usage, candidates[i].Usage != "")
			}
		}
	}
}
