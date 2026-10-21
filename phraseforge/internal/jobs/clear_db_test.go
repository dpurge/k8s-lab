package jobs

import (
	"context"
	"testing"
)

func TestDeleteCompletedAndCancelledKeepsTheRest(t *testing.T) {
	pool := schemaPool(t)
	ctx := context.Background()
	done := insertJobAged(t, pool, "done", 1)
	cancelled := insertJobAged(t, pool, "cancelled", 1)
	failed := insertJobAged(t, pool, "failed", 1)
	pending := insertJobAged(t, pool, "pending", 1)
	running := insertJobAged(t, pool, "running", 1)

	n, err := New(pool).DeleteCompletedAndCancelled(ctx)
	if err != nil || n != 2 {
		t.Fatalf("removed %d rows (want 2: done and cancelled): %v", n, err)
	}
	left := jobIDs(t, pool)
	if left[done] || left[cancelled] {
		t.Errorf("a completed or cancelled task is still there: %v", left)
	}
	if !left[failed] || !left[pending] || !left[running] {
		t.Errorf("a failed, pending or running task was removed: %v", left)
	}
	if n, err := New(pool).DeleteCompletedAndCancelled(ctx); err != nil || n != 0 {
		t.Errorf("a second Clear removes nothing: %d %v", n, err)
	}
}
