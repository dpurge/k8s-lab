package generate

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"phraseforge/internal/ai"
	"phraseforge/internal/config"
)

var transcriptionSchemaCounter atomic.Int64

// transcriptionService is a generate.Service with only the real ai.Service on a
// throwaway schema; dropUnneededTranscription reads nothing else.
func transcriptionService(t *testing.T) (*Service, *pgxpool.Pool) {
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
	schema := fmt.Sprintf("pf_test_%d_%d", os.Getpid(), transcriptionSchemaCounter.Add(1))
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
	return &Service{ai: ai.New(pool, config.Config{}, nil, nil, nil, nil), db: pool}, pool
}

func TestDropUnneededTranscriptionFollowsTheLanguage(t *testing.T) {
	s, pool := transcriptionService(t)
	ctx := context.Background()
	dropped := func(language string) bool {
		t.Helper()
		cleared := false
		if err := s.dropUnneededTranscription(ctx, language, func() { cleared = true }); err != nil {
			t.Fatal(err)
		}
		return cleared
	}

	if _, err := pool.Exec(ctx, `INSERT INTO ime_config(language, script, needs_transcription) VALUES ('ell','grek',false), ('arb','arab',true)`); err != nil {
		t.Fatal(err)
	}
	if !dropped("ell") {
		t.Error("ell needs no transcription and has no section: it must be dropped")
	}
	if dropped("arb") {
		t.Error("arb needs a transcription: it must be kept")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO language_llm_sections(language, transcription_prompt) VALUES ('ell','Use ELOT 743.')`); err != nil {
		t.Fatal(err)
	}
	if dropped("ell") {
		t.Error("a transcription section keeps the transcription")
	}
}
