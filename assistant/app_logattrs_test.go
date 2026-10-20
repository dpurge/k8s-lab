package main

import (
	"context"
	"testing"

	"assistant/internal/callctx"
)

func TestCallLogAttrsCarryOnlyIDs(t *testing.T) {
	got := callLogAttrs(callctx.With(context.Background(), callctx.Info{UserID: "u1", TaskID: "t1"}))
	if len(got) != 2 || got[0].Key != "user_id" || got[0].Value.String() != "u1" || got[1].Key != "task_id" || got[1].Value.String() != "t1" {
		t.Errorf("attrs = %v", got)
	}
	if got := callLogAttrs(callctx.With(context.Background(), callctx.Info{UserID: "u1"})); len(got) != 1 {
		t.Errorf("a call outside a task has no task_id: %v", got)
	}
	if got := callLogAttrs(context.Background()); got != nil {
		t.Errorf("no info, no attrs: %v", got)
	}
}
