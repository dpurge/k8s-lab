package tasksteps_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"assistant/internal/dbtest"
	"assistant/internal/tasks"
	"assistant/internal/tasksteps"
	"assistant/internal/users"
)

func TestStepsAreReadBackInTheOrderTheyWereWrittenAndOnlyForTheirTask(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	user, err := users.NewStore(pool).Create(ctx, "Ada", nil)
	if err != nil {
		t.Fatal(err)
	}
	taskStore := tasks.NewStore(pool)
	a, _ := taskStore.Create(ctx, tasks.New{UserID: user.ID, WorkflowID: "w", Source: tasks.SourceAPI})
	b, _ := taskStore.Create(ctx, tasks.New{UserID: user.ID, WorkflowID: "w", Source: tasks.SourceAPI})
	store := tasksteps.NewStore(pool)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	tool, errText, reply := "rag_search", "bad", "r"
	rows := []tasksteps.Step{
		{TaskID: a.ID, Step: 1, Attempt: 1, State: "intake", Outcome: "retry", Error: &errText, Reply: &reply, At: now},
		{TaskID: a.ID, Step: 1, Attempt: 2, State: "intake", Outcome: "ok", Tool: &tool, Action: []byte(`{"type":"tool"}`), Patch: []byte(`{"n":1}`), InputTokens: 10, OutputTokens: 2, DurationMS: 5, At: now.Add(time.Second)},
		{TaskID: b.ID, Step: 1, Attempt: 1, State: "other", Outcome: "ok", At: now},
	}
	for _, r := range rows {
		if err := store.Insert(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ForTask(ctx, a.ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if got[0].Attempt != 1 || got[1].Attempt != 2 || got[0].Error == nil || *got[0].Error != "bad" || got[1].Tool == nil || *got[1].Tool != "rag_search" {
		t.Errorf("rows = %+v", got)
	}
	if string(got[1].Action) != `{"type": "tool"}` || got[0].Action != nil || got[1].InputTokens != 10 {
		t.Errorf("action %s / %v", got[1].Action, got[0].Action)
	}
	empty, err := store.ForTask(ctx, "11111111-1111-1111-1111-111111111111")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("no steps is an empty list, not nil: %v %v", empty, err)
	}
}

func TestAStepForAnUnknownTaskIsRefusedByTheDatabase(t *testing.T) {
	pool := dbtest.Pool(t)
	err := tasksteps.NewStore(pool).Insert(context.Background(), tasksteps.Step{TaskID: "11111111-1111-1111-1111-111111111111", State: "a", Outcome: "ok", At: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "insert task step") {
		t.Errorf("err = %v", err)
	}
}

func TestTheDatabaseRefusesAnUnknownOutcome(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	user, _ := users.NewStore(pool).Create(ctx, "Ada", nil)
	task, _ := tasks.NewStore(pool).Create(ctx, tasks.New{UserID: user.ID, WorkflowID: "w", Source: tasks.SourceAPI})
	if err := tasksteps.NewStore(pool).Insert(ctx, tasksteps.Step{TaskID: task.ID, State: "a", Outcome: "great", At: time.Now()}); err == nil {
		t.Error("an outcome the engine never sends must be refused")
	}
}
