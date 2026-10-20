package jobs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"k8s-lab/shared/retention"
)

var retentionSchemaCounter atomic.Int64

// schemaPool is a pool on a throwaway schema with schema.sql applied
// (PHRASEFORGE_TEST_DSN, skipped without it). Unlike testPool it never touches
// the tables of a shared database, so it may hold a running job.
func schemaPool(t *testing.T) *pgxpool.Pool {
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
	schema := fmt.Sprintf("pf_test_%d_jobs_%d", os.Getpid(), retentionSchemaCounter.Add(1))
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

var retentionNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// insertJobAged adds a job in status whose updated_at is daysOld days old, and
// returns its id.
func insertJobAged(t *testing.T, pool *pgxpool.Pool, status string, daysOld int) string {
	t.Helper()
	id, err := uuid()
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(context.Background(),
		`INSERT INTO jobs(id,kind,priority,status,payload,checkpoint,created_at,updated_at)
		 VALUES($1,'retention_test','background',$2,'{}','["chunk"]',$3,$3)`,
		id, status, retentionNow.AddDate(0, 0, -daysOld))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func jobIDs(t *testing.T, pool *pgxpool.Pool) map[string]bool {
	t.Helper()
	res, err := pool.Query(context.Background(), "SELECT id::text FROM jobs")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	out := map[string]bool{}
	for res.Next() {
		var id string
		if err := res.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out[id] = true
	}
	return out
}

func purgeJobs(t *testing.T, pool *pgxpool.Pool, keepDone, keepFailed int) map[string]int64 {
	t.Helper()
	const day = 24 * time.Hour
	p, err := retention.NewPurger(pool, RetentionRules(time.Duration(keepDone)*day, time.Duration(keepFailed)*day),
		retention.WithClock(func() time.Time { return retentionNow }), retention.WithBatchPause(0),
		retention.WithLogger(slog.New(slog.NewJSONHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := p.PurgeOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return deleted
}

func TestRetentionDeletesOldDoneJobsAndKeepsRecentOnes(t *testing.T) {
	pool := schemaPool(t)
	oldDone := insertJobAged(t, pool, "done", 8)
	recentDone := insertJobAged(t, pool, "done", 6)
	deleted := purgeJobs(t, pool, 7, 30)
	got := jobIDs(t, pool)
	if got[oldDone] || !got[recentDone] {
		t.Errorf("old done job kept = %v, recent done job kept = %v; want false, true", got[oldDone], got[recentDone])
	}
	if deleted["jobs_done"] != 1 || deleted["jobs_failed"] != 0 {
		t.Errorf("deleted = %v, want jobs_done=1", deleted)
	}
}

func TestRetentionOfFailedAndCancelledJobsUsesTheirOwnPeriod(t *testing.T) {
	pool := schemaPool(t)
	failed10, cancelled10 := insertJobAged(t, pool, "failed", 10), insertJobAged(t, pool, "cancelled", 10)
	failed31, cancelled31 := insertJobAged(t, pool, "failed", 31), insertJobAged(t, pool, "cancelled", 31)
	deleted := purgeJobs(t, pool, 7, 30)
	got := jobIDs(t, pool)
	if !got[failed10] || !got[cancelled10] {
		t.Error("a failed or cancelled job of 10 days was purged: the done period (7) must not apply to it")
	}
	if got[failed31] || got[cancelled31] {
		t.Error("a failed or cancelled job of 31 days was kept")
	}
	if deleted["jobs_failed"] != 2 || deleted["jobs_done"] != 0 {
		t.Errorf("deleted = %v, want jobs_failed=2", deleted)
	}
}

func TestRetentionNeverDeletesPendingOrRunningJobs(t *testing.T) {
	pool := schemaPool(t)
	pending := insertJobAged(t, pool, "pending", 400)
	running := insertJobAged(t, pool, "running", 400)
	oldDone := insertJobAged(t, pool, "done", 400)
	purgeJobs(t, pool, 1, 1)
	got := jobIDs(t, pool)
	if !got[pending] || !got[running] {
		t.Errorf("pending kept = %v, running kept = %v; want both kept however old", got[pending], got[running])
	}
	if got[oldDone] {
		t.Error("an old done job was kept")
	}
}

func TestRetentionOfZeroKeepsFinishedJobsForever(t *testing.T) {
	pool := schemaPool(t)
	done, failed := insertJobAged(t, pool, "done", 400), insertJobAged(t, pool, "failed", 400)
	purgeJobs(t, pool, 0, 0)
	if got := jobIDs(t, pool); !got[done] || !got[failed] {
		t.Errorf("done kept = %v, failed kept = %v; want both kept", got[done], got[failed])
	}
	purgeJobs(t, pool, 0, 30)
	if got := jobIDs(t, pool); !got[done] || got[failed] {
		t.Errorf("done kept = %v, failed kept = %v; want true, false", got[done], got[failed])
	}
}

// A job's checkpoint is a column of its own row and llm_calls.job_id has no
// foreign key, so deleting a job leaves nothing dangling and breaks nothing.
func TestPurgingAJobTakesItsCheckpointAndKeepsItsCallHistory(t *testing.T) {
	pool := schemaPool(t)
	ctx := context.Background()
	id := insertJobAged(t, pool, "failed", 40)
	if _, err := pool.Exec(ctx, `INSERT INTO llm_calls(job_id, model_id, provider_id, model_name, started_at, duration_ms, outcome)
		VALUES($1,'m','p','n',$2,1,'ok')`, id, retentionNow.AddDate(0, 0, -40)); err != nil {
		t.Fatal(err)
	}
	purgeJobs(t, pool, 7, 30)
	if jobIDs(t, pool)[id] {
		t.Fatal("the failed job of 40 days was kept")
	}
	var kept int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM llm_calls WHERE job_id = $1", id).Scan(&kept); err != nil || kept != 1 {
		t.Errorf("llm_calls rows of the purged job = %d, %v; want 1", kept, err)
	}
	// Retry of a purged job is refused like any unknown job, and does not panic.
	if _, err := New(pool).Retry(ctx, id); err == nil {
		t.Error("Retry of a purged job succeeded")
	}
}
