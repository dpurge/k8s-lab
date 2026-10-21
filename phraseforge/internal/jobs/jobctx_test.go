package jobs

import (
	"context"
	"testing"
)

func TestJobIDRoundTripsThroughTheContext(t *testing.T) {
	ctx := WithJobID(context.Background(), "job-1")
	if id, ok := JobID(ctx); !ok || id != "job-1" {
		t.Errorf("JobID = %q, %v; want job-1, true", id, ok)
	}
}

func TestJobIDIsAbsentOutsideAJob(t *testing.T) {
	if id, ok := JobID(context.Background()); ok || id != "" {
		t.Errorf("JobID of a bare context = %q, %v; want empty, false", id, ok)
	}
	if _, ok := JobID(WithJobID(context.Background(), "")); ok {
		t.Error("an empty job id must count as absent")
	}
}

func TestUserIDRoundTripsThroughTheContextAndIsAbsentWithoutOne(t *testing.T) {
	if id, ok := UserID(WithUserID(context.Background(), 7)); !ok || id != 7 {
		t.Errorf("UserID = %d, %v; want 7, true", id, ok)
	}
	if id, ok := UserID(context.Background()); ok || id != 0 {
		t.Errorf("UserID of a bare context = %d, %v; want 0, false", id, ok)
	}
}
