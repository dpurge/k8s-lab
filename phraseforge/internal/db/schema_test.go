package db

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// previousLLMPromptsTable is the llm_prompts table as the schema created it
// before model_tags replaced provider and model.
const previousLLMPromptsTable = `CREATE TABLE llm_prompts (
    kind            text NOT NULL,
    source_language text NOT NULL,
    target_language text NOT NULL,
    provider        text NOT NULL DEFAULT 'ollama',
    model           text NOT NULL DEFAULT '',
    think           boolean NOT NULL DEFAULT false,
    timeout_seconds integer CHECK (timeout_seconds IS NULL OR (timeout_seconds > 0 AND timeout_seconds <= 3600)),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, source_language, target_language)
)`

// newSchemaPool is a pool on a throwaway schema (PHRASEFORGE_TEST_DSN, skipped
// without it), as the other database tests do.
func newSchemaPool(t *testing.T) *pgxpool.Pool {
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
	schema := fmt.Sprintf("pf_test_%d_schema", os.Getpid())
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
	return pool
}

// TestSchemaMigratesLLMPromptsFromProviderAndModelToModelTags: an existing
// table keeps think and timeout, loses provider and model, gets model_tags
// NULL (inherit) and its check, and a second application changes nothing.
func TestSchemaMigratesLLMPromptsFromProviderAndModelToModelTags(t *testing.T) {
	pool := newSchemaPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, previousLLMPromptsTable); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO llm_prompts(kind, source_language, target_language, provider, model, think, timeout_seconds) VALUES('title','deu','title','openrouter','some/model',true,600)`); err != nil {
		t.Fatal(err)
	}

	for run := 1; run <= 2; run++ {
		if _, err := pool.Exec(ctx, schemaSQL); err != nil {
			t.Fatalf("apply schema, run %d: %v", run, err)
		}
	}

	var oldColumns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'llm_prompts' AND column_name IN ('provider', 'model')`).Scan(&oldColumns); err != nil {
		t.Fatal(err)
	}
	if oldColumns != 0 {
		t.Errorf("%d of the provider and model columns survived the migration", oldColumns)
	}
	var tags []string
	var think bool
	var timeout *int
	if err := pool.QueryRow(ctx, `SELECT model_tags, think, timeout_seconds FROM llm_prompts WHERE kind='title' AND source_language='deu'`).Scan(&tags, &think, &timeout); err != nil {
		t.Fatal(err)
	}
	if tags != nil || !think || timeout == nil || *timeout != 600 {
		t.Errorf("migrated row = tags %v think %v timeout %v, want NULL tags, think true, timeout 600", tags, think, timeout)
	}
}

func TestLLMPromptsModelTagsCheck(t *testing.T) {
	pool := newSchemaPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		t.Fatal(err)
	}
	insert := func(language, tags string) error {
		_, err := pool.Exec(ctx, `INSERT INTO llm_prompts(kind, source_language, target_language, model_tags) VALUES('title','`+language+`','title',`+tags+`)`)
		return err
	}
	for i, tags := range []string{"NULL", "'{a}'", "'{a,b}'"} {
		if err := insert(fmt.Sprintf("ok%d", i), tags); err != nil {
			t.Errorf("model_tags %s rejected: %v", tags, err)
		}
	}
	for i, tags := range []string{"'{}'", `'{""}'`, `'{"a",""}'`, `'{"a",NULL}'`} {
		if err := insert(fmt.Sprintf("bad%d", i), tags); err == nil {
			t.Errorf("model_tags %s accepted", tags)
		}
	}
}

// TestLLMCallsTableIsCreatedAndAppliedTwice: the second application changes
// nothing, a call without a job is allowed, and an unknown outcome is not.
func TestLLMCallsTableIsCreatedAndAppliedTwice(t *testing.T) {
	pool := newSchemaPool(t)
	ctx := context.Background()
	for run := 1; run <= 2; run++ {
		if _, err := pool.Exec(ctx, schemaSQL); err != nil {
			t.Fatalf("apply schema, run %d: %v", run, err)
		}
	}
	insert := func(outcome string) error {
		_, err := pool.Exec(ctx, `INSERT INTO llm_calls(model_id, provider_id, model_name, started_at, duration_ms, outcome) VALUES('m','p','n',now(),1,$1)`, outcome)
		return err
	}
	for _, outcome := range []string{"ok", "error", "timeout", "rate_limited", "canceled"} {
		if err := insert(outcome); err != nil {
			t.Errorf("outcome %q rejected: %v", outcome, err)
		}
	}
	if err := insert("bogus"); err == nil {
		t.Error("outcome bogus accepted")
	}
	var rows int
	var id string
	if err := pool.QueryRow(ctx, `SELECT count(*), min(id::text) FROM llm_calls WHERE job_id IS NULL`).Scan(&rows, &id); err != nil || rows != 5 || id == "" {
		t.Errorf("rows without a job = %d, id %q, err %v; want 5 with generated ids", rows, id, err)
	}
}
