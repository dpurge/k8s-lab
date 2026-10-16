package tasksteps

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"assistant/internal/workflow"
)

type fakeStore struct {
	steps []Step
	err   error
	ctxs  []error
}

func (f *fakeStore) Insert(ctx context.Context, st Step) error {
	f.ctxs = append(f.ctxs, ctx.Err())
	if f.err != nil {
		return f.err
	}
	f.steps = append(f.steps, st)
	return nil
}

type counter struct{ n int }

func (c *counter) TraceRecordError() { c.n++ }

func recorder(store *fakeStore, c *counter) (*Recorder, *bytes.Buffer) {
	logs := &bytes.Buffer{}
	var ec ErrorCounter
	if c != nil {
		ec = c
	}
	return NewRecorder(store, ec, slog.New(slog.NewJSONHandler(logs, nil))), logs
}

var at = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func TestAToolStepIsStoredWithEverythingTheEngineKnewAboutIt(t *testing.T) {
	store := &fakeStore{}
	r, _ := recorder(store, nil)
	r.Record(context.Background(), workflow.StepRecord{
		RunID: "11111111-2222-4333-8444-555555555555", Step: 2, Attempt: 1, State: "plan",
		Reply: `{"action":{}}`, Patch: map[string]any{"goal": "Arabic"},
		Action:  &workflow.Action{Type: workflow.ActionTool, Tool: "rag_search", Args: map[string]any{"query": "arabic"}},
		Outcome: workflow.OutcomeOK, Tool: "rag_search", ToolResult: "ok: 1 document",
		InputTokens: 900, OutputTokens: 60, Duration: 2500 * time.Millisecond, At: at,
	})
	if len(store.steps) != 1 {
		t.Fatalf("stored %d", len(store.steps))
	}
	st := store.steps[0]
	if st.TaskID != "11111111-2222-4333-8444-555555555555" || st.Step != 2 || st.Attempt != 1 || st.State != "plan" || st.Outcome != "ok" {
		t.Errorf("step = %+v", st)
	}
	if string(st.Action) != `{"type":"tool","tool":"rag_search","args":{"query":"arabic"}}` || string(st.Patch) != `{"goal":"Arabic"}` {
		t.Errorf("action %s patch %s", st.Action, st.Patch)
	}
	if st.Tool == nil || *st.Tool != "rag_search" || st.ToolResult == nil || *st.ToolResult != "ok: 1 document" || st.Error != nil {
		t.Errorf("tool fields = %+v", st)
	}
	if st.InputTokens != 900 || st.OutputTokens != 60 || st.DurationMS != 2500 || !st.At.Equal(at) {
		t.Errorf("usage = %+v", st)
	}
}

func TestARefusedReplyKeepsItsErrorAndHasNoAction(t *testing.T) {
	store := &fakeStore{}
	r, _ := recorder(store, nil)
	r.Record(context.Background(), workflow.StepRecord{RunID: "r", Step: 1, Attempt: 1, State: "intake", Reply: "not json", Outcome: workflow.OutcomeRetry, Error: "the reply is not valid JSON"})
	st := store.steps[0]
	if st.Error == nil || *st.Error != "the reply is not valid JSON" || st.Action != nil || st.Patch != nil || st.Tool != nil || st.Outcome != "retry" {
		t.Errorf("step = %+v", st)
	}
	if st.Reply == nil || *st.Reply != "not json" {
		t.Errorf("the refused reply is what a person needs to see")
	}
}

func TestAFailureWithoutAModelCallIsStored(t *testing.T) {
	store := &fakeStore{}
	r, _ := recorder(store, nil)
	r.Record(context.Background(), workflow.StepRecord{RunID: "r", Step: 5, Attempt: 0, State: "teach", Outcome: workflow.OutcomeFailed, Error: "max_steps: the run used its 40 steps"})
	st := store.steps[0]
	if st.Attempt != 0 || st.Outcome != "failed" || st.Reply != nil || *st.Error == "" {
		t.Errorf("step = %+v", st)
	}
}

func TestAskUserAndGotoActionsAreStored(t *testing.T) {
	store := &fakeStore{}
	r, _ := recorder(store, nil)
	ctx := context.Background()
	r.Record(ctx, workflow.StepRecord{RunID: "r", State: "a", Outcome: workflow.OutcomeOK, Action: &workflow.Action{Type: workflow.ActionGoto, State: "b"}})
	r.Record(ctx, workflow.StepRecord{RunID: "r", State: "a", Outcome: workflow.OutcomeOK, Action: &workflow.Action{Type: workflow.ActionAskUser, Payload: map[string]any{"question": "Which?"}}})
	if string(store.steps[0].Action) != `{"type":"goto","state":"b"}` || string(store.steps[1].Action) != `{"type":"ask_user","payload":{"question":"Which?"}}` {
		t.Errorf("%s / %s", store.steps[0].Action, store.steps[1].Action)
	}
}

func TestALongReplyIsCutOnACharacterAndSaysSo(t *testing.T) {
	store := &fakeStore{}
	r, _ := recorder(store, nil)
	long := strings.Repeat("ع", MaxReplyBytes) // two bytes each
	r.Record(context.Background(), workflow.StepRecord{RunID: "r", State: "a", Outcome: workflow.OutcomeOK, Reply: long})
	got := *store.steps[0].Reply
	if !strings.HasSuffix(got, replyCutMarker) || !utf8.ValidString(got) || len(got) > MaxReplyBytes+len(replyCutMarker) {
		t.Errorf("len %d valid %v", len(got), utf8.ValidString(got))
	}
	exact := strings.Repeat("a", MaxReplyBytes)
	r.Record(context.Background(), workflow.StepRecord{RunID: "r", State: "a", Outcome: workflow.OutcomeOK, Reply: exact})
	if *store.steps[1].Reply != exact {
		t.Errorf("a reply that fits is kept whole")
	}
}

func TestAWriteThatFailsIsLoggedAndCountedAndNeverReachesTheRun(t *testing.T) {
	store := &fakeStore{err: errors.New("connection to the database lost")}
	c := &counter{}
	r, logs := recorder(store, c)
	r.Record(context.Background(), workflow.StepRecord{RunID: "r", Step: 3, Attempt: 2, State: "a", Outcome: workflow.OutcomeOK, Reply: "SECRET lesson text"})
	if c.n != 1 {
		t.Errorf("counted %d", c.n)
	}
	out := logs.String()
	if !strings.Contains(out, "could not record a workflow step") || !strings.Contains(out, "connection to the database lost") || !strings.Contains(out, `"step":3`) {
		t.Errorf("log = %s", out)
	}
	if strings.Contains(out, "SECRET") {
		t.Errorf("a reply must never be logged: %s", out)
	}
}

func TestAnUnencodablePatchIsReportedLikeAFailedWrite(t *testing.T) {
	store := &fakeStore{}
	c := &counter{}
	r, logs := recorder(store, c)
	r.Record(context.Background(), workflow.StepRecord{RunID: "r", State: "a", Outcome: workflow.OutcomeOK, Patch: map[string]any{"f": func() {}}})
	if c.n != 1 || len(store.steps) != 0 || !strings.Contains(logs.String(), "encode state patch") {
		t.Errorf("counted %d stored %d log %s", c.n, len(store.steps), logs)
	}
}

func TestTheWriteDoesNotInheritACancelledRun(t *testing.T) {
	store := &fakeStore{}
	r, _ := recorder(store, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.Record(ctx, workflow.StepRecord{RunID: "r", State: "a", Outcome: workflow.OutcomeFailed, Error: "cancelled"})
	if len(store.steps) != 1 || store.ctxs[0] != nil {
		t.Errorf("the failure of a cancelled run is the row most worth keeping: %v", store.ctxs)
	}
}

func TestNoCounterIsFine(t *testing.T) {
	store := &fakeStore{err: errors.New("x")}
	r, _ := recorder(store, nil)
	r.Record(context.Background(), workflow.StepRecord{RunID: "r", State: "a", Outcome: workflow.OutcomeOK})
}
