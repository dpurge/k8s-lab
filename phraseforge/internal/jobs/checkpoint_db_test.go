package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool connects to the database named by PHRASEFORGE_TEST_DB (a
// connection URL of a throwaway/dev database whose schema is applied) and
// skips the test when it is unset, so the normal suite needs no database.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("PHRASEFORGE_TEST_DB")
	if url == "" {
		t.Skip("PHRASEFORGE_TEST_DB not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// insertJob adds a job in a terminal status, which no worker will claim, and
// removes it with everything Retry derived from it when the test ends.
func insertJob(t *testing.T, pool *pgxpool.Pool, status string) string {
	t.Helper()
	id, err := uuid()
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(context.Background(), "INSERT INTO jobs(id,kind,priority,status,payload) VALUES($1,'checkpoint_db_test','background',$2,'{}')", id, status)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM jobs WHERE kind='checkpoint_db_test'")
	})
	return id
}

func rawList(s ...string) []json.RawMessage {
	var out []json.RawMessage
	for _, v := range s {
		out = append(out, json.RawMessage(fmt.Sprintf("%q", v)))
	}
	return out
}

func TestJobCheckpointSaveAndLoad(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	c := jobCheckpoint{db: pool, id: insertJob(t, pool, "failed")}

	if got, err := c.Load(ctx, "k1"); err != nil || got != nil {
		t.Fatalf("Load before any save = (%v, %v), want nothing", got, err)
	}
	if err := c.Save(ctx, "k1", rawList("a")); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(ctx, "k1", rawList("a", "b")); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(ctx, "k2", rawList("x")); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string][]json.RawMessage{"k1": rawList("a", "b"), "k2": rawList("x")} {
		got, err := c.Load(ctx, key)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Load(%s) = (%s, %v), want %s", key, got, err, want)
		}
	}
	if got, err := c.Load(ctx, "other"); err != nil || got != nil {
		t.Errorf("Load of an unknown key = (%v, %v), want nothing", got, err)
	}
}

// A Retry carries the checkpoint onto the new job in the same INSERT.
func TestRetryCopiesTheCheckpoint(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := New(pool)
	failed := insertJob(t, pool, "failed")
	if err := (jobCheckpoint{db: pool, id: failed}).Save(ctx, "k", rawList("one", "two")); err != nil {
		t.Fatal(err)
	}

	retried, err := s.Retry(ctx, failed)
	if err != nil {
		t.Fatal(err)
	}
	got, err := jobCheckpoint{db: pool, id: retried}.Load(ctx, "k")
	if err != nil || !reflect.DeepEqual(got, rawList("one", "two")) {
		t.Fatalf("retried job checkpoint = (%s, %v), want the original's", got, err)
	}
}

func TestRetryOfAJobWithoutACheckpointStillWorks(t *testing.T) {
	pool := testPool(t)
	s := New(pool)
	retried, err := s.Retry(context.Background(), insertJob(t, pool, "failed"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := (jobCheckpoint{db: pool, id: retried}).Load(context.Background(), "k"); err != nil || got != nil {
		t.Fatalf("got (%v, %v), want nothing", got, err)
	}
}

// A done job drops its checkpoint; a failed one keeps it for its Retry.
func TestCompleteClearsTheCheckpointOnlyWhenDone(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := New(pool)
	for _, tt := range []struct {
		name     string
		jobErr   error
		wantKept bool
	}{{"done", nil, false}, {"failed", fmt.Errorf("boom"), true}} {
		id := insertJob(t, pool, "running")
		c := jobCheckpoint{db: pool, id: id}
		if err := c.Save(ctx, "k", rawList("a")); err != nil {
			t.Fatal(err)
		}
		s.complete(ctx, id, nil, tt.jobErr, false)
		got, err := c.Load(ctx, "k")
		if err != nil || (got != nil) != tt.wantKept {
			t.Errorf("%s: checkpoint after complete = (%v, %v), want kept=%v", tt.name, got, err, tt.wantKept)
		}
	}
}

// Progress is written as "done/total", read back with the job, and cleared
// only when the job is done: a failed job keeps it to show where it stopped.
func TestProgressIsStoredReadBackAndClearedOnlyWhenDone(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := New(pool)
	for _, tt := range []struct {
		name     string
		jobErr   error
		wantKept bool
	}{{"done", nil, false}, {"failed", fmt.Errorf("boom"), true}} {
		id := insertJob(t, pool, "running")
		if err := (jobCheckpoint{db: pool, id: id}).SetProgress(ctx, 3, 12); err != nil {
			t.Fatal(err)
		}
		if j, err := s.Get(ctx, id); err != nil || j.Progress != "3/12" {
			t.Fatalf("%s: Get after SetProgress = (%q, %v), want 3/12", tt.name, j.Progress, err)
		}
		s.complete(ctx, id, nil, tt.jobErr, false)
		j, err := s.Get(ctx, id)
		if err != nil || (j.Progress != "") != tt.wantKept {
			t.Errorf("%s: progress after complete = (%q, %v), want kept=%v", tt.name, j.Progress, err, tt.wantKept)
		}
	}
}
