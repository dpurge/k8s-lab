package jobs

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func insertUser(t *testing.T, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `INSERT INTO users(username, password_hash) VALUES($1, 'x') RETURNING id`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func jobUser(t *testing.T, pool *pgxpool.Pool, id string) *int64 {
	t.Helper()
	var uid *int64
	if err := pool.QueryRow(context.Background(), `SELECT user_id FROM jobs WHERE id = $1::uuid`, id).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	return uid
}

func TestEnqueueRecordsTheUserInTheContextAndNoneWithoutOne(t *testing.T) {
	pool := schemaPool(t)
	svc := New(pool)
	ada := insertUser(t, pool, "ada")

	forAda, err := svc.Enqueue(WithUserID(context.Background(), ada), "k", PriorityBackground, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := jobUser(t, pool, forAda); got == nil || *got != ada {
		t.Errorf("user of a job queued for Ada = %v, want %d", got, ada)
	}
	bySystem, err := svc.Enqueue(context.Background(), "k", PriorityBackground, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := jobUser(t, pool, bySystem); got != nil {
		t.Errorf("a job queued with no user has user %d, want none", *got)
	}
}

func TestRetryKeepsTheUserOfTheJobItRetries(t *testing.T) {
	pool := schemaPool(t)
	svc := New(pool)
	ctx := context.Background()
	ada, admin := insertUser(t, pool, "ada"), insertUser(t, pool, "admin")

	original, err := svc.Enqueue(WithUserID(ctx, ada), "k", PriorityBackground, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE jobs SET status = 'failed' WHERE id = $1::uuid`, original); err != nil {
		t.Fatal(err)
	}
	retried, err := svc.Retry(WithUserID(ctx, admin), original)
	if err != nil {
		t.Fatal(err)
	}
	if got := jobUser(t, pool, retried); got == nil || *got != ada {
		t.Errorf("a retry pressed by the admin is for %v, want the original user %d", got, ada)
	}

	// A job from before the user was recorded takes the user who retries it.
	if _, err := pool.Exec(ctx, `UPDATE jobs SET user_id = NULL, status = 'failed' WHERE id = $1::uuid`, original); err != nil {
		t.Fatal(err)
	}
	again, err := svc.Retry(WithUserID(ctx, admin), original)
	if err != nil {
		t.Fatal(err)
	}
	if got := jobUser(t, pool, again); got == nil || *got != admin {
		t.Errorf("a retry of a job with no user is for %v, want %d", got, admin)
	}
}

func TestAHandlerSeesTheUserTheJobWasQueuedFor(t *testing.T) {
	pool := schemaPool(t)
	svc := New(pool)
	ada := insertUser(t, pool, "ada")
	var seen int64
	var seenOK bool
	svc.Register("k", func(ctx context.Context, id string, payload json.RawMessage) (json.RawMessage, error) {
		seen, seenOK = UserID(ctx)
		return nil, nil
	})
	if _, err := svc.Enqueue(WithUserID(context.Background(), ada), "k", PriorityBackground, nil); err != nil {
		t.Fatal(err)
	}
	if !svc.runOne(context.Background()) {
		t.Fatal("no job was run")
	}
	if !seenOK || seen != ada {
		t.Errorf("handler saw user %d, %v; want %d, true", seen, seenOK, ada)
	}
}

// The schema is applied at every start, so adding the user columns must be
// repeatable, and must keep the rows that are already there.
func TestTheSchemaCanBeAppliedAgainAndKeepsTheUserColumns(t *testing.T) {
	pool := schemaPool(t)
	ctx := context.Background()
	ada := insertUser(t, pool, "ada")
	id, err := New(pool).Enqueue(WithUserID(ctx, ada), "k", PriorityBackground, nil)
	if err != nil {
		t.Fatal(err)
	}
	schemaSQL, err := os.ReadFile("../db/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(schemaSQL)); err != nil {
		t.Fatalf("applying the schema a second time: %v", err)
	}
	if got := jobUser(t, pool, id); got == nil || *got != ada {
		t.Errorf("user after the schema was applied again = %v, want %d", got, ada)
	}
}
