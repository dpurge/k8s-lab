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
