// Package tasksteps stores the trace of a workflow run: one row per engine
// step attempt, so a person can see what the model answered and what the
// engine made of it.
package tasksteps

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"assistant/internal/workflow"
)

// MaxReplyBytes is how much of a model reply is kept.
const MaxReplyBytes = 8 << 10

const replyCutMarker = "…(cut)"

// Step is one stored attempt.
type Step struct {
	ID           int64           `json:"id"`
	TaskID       string          `json:"task_id"`
	Step         int             `json:"step"`
	Attempt      int             `json:"attempt"`
	State        string          `json:"state"`
	Outcome      string          `json:"outcome"`
	Error        *string         `json:"error"`
	Action       json.RawMessage `json:"action"`
	Patch        json.RawMessage `json:"patch"`
	Tool         *string         `json:"tool"`
	ToolResult   *string         `json:"tool_result"`
	Reply        *string         `json:"reply"`
	InputTokens  int             `json:"input_tokens"`
	OutputTokens int             `json:"output_tokens"`
	DurationMS   int             `json:"duration_ms"`
	At           time.Time       `json:"at"`
	// WorkflowID is the workflow the step ran in, and Depth how many runs deep:
	// above 0 for the steps of a workflow the request was handed to.
	WorkflowID *string `json:"workflow_id"`
	Depth      int     `json:"depth"`
}

// Store reads and writes steps.
type Store struct{ pool *pgxpool.Pool }

// NewStore returns a store on pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const columns = `id, task_id::text, step, attempt, state, outcome, error, action, patch, tool, tool_result, reply, input_tokens, output_tokens, duration_ms, at, workflow_id, depth`

// Insert stores one step.
func (s *Store) Insert(ctx context.Context, st Step) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO task_steps (task_id, step, attempt, state, outcome, error, action, patch, tool, tool_result, reply, input_tokens, output_tokens, duration_ms, at, workflow_id, depth)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		st.TaskID, st.Step, st.Attempt, st.State, st.Outcome, st.Error, nullJSON(st.Action), nullJSON(st.Patch),
		st.Tool, st.ToolResult, st.Reply, st.InputTokens, st.OutputTokens, st.DurationMS, st.At, st.WorkflowID, st.Depth)
	if err != nil {
		return fmt.Errorf("insert task step: %w", err)
	}
	return nil
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

// ForTask returns the task's steps in the order they happened.
func (s *Store) ForTask(ctx context.Context, taskID string) ([]Step, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+columns+` FROM task_steps WHERE task_id = $1::uuid ORDER BY id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list task steps: %w", err)
	}
	defer rows.Close()
	out := []Step{}
	for rows.Next() {
		st, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list task steps: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func scan(row pgx.Row) (Step, error) {
	var st Step
	var action, patch []byte
	err := row.Scan(&st.ID, &st.TaskID, &st.Step, &st.Attempt, &st.State, &st.Outcome, &st.Error, &action, &patch,
		&st.Tool, &st.ToolResult, &st.Reply, &st.InputTokens, &st.OutputTokens, &st.DurationMS, &st.At, &st.WorkflowID, &st.Depth)
	if action != nil {
		st.Action = action
	}
	if patch != nil {
		st.Patch = patch
	}
	return st, err
}

// Inserter is what the recorder needs from a store.
type Inserter interface {
	Insert(ctx context.Context, st Step) error
}

// ErrorCounter counts rows that could not be written.
type ErrorCounter interface{ TraceRecordError() }

// insertTimeout bounds one write, which must not hold up the run.
const insertTimeout = 5 * time.Second

// Recorder writes the engine's trace to the database. It implements
// workflow.Recorder. A row that cannot be written is logged and counted, never
// dropped silently, and never fails the run.
type Recorder struct {
	store  Inserter
	errors ErrorCounter
	log    *slog.Logger
}

// NewRecorder returns a recorder. errors may be nil.
func NewRecorder(store Inserter, errors ErrorCounter, log *slog.Logger) *Recorder {
	return &Recorder{store: store, errors: errors, log: log}
}

// Record implements workflow.Recorder. The run id is the task's id.
func (r *Recorder) Record(ctx context.Context, rec workflow.StepRecord) {
	insertCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), insertTimeout)
	defer cancel()
	st, err := toStep(rec)
	if err == nil {
		err = r.store.Insert(insertCtx, st)
	}
	if err != nil {
		// The reply is not logged: it can hold what the user wrote.
		r.log.Error("could not record a workflow step", "task_id", rec.RunID, "step", rec.Step, "attempt", rec.Attempt, "error", err)
		if r.errors != nil {
			r.errors.TraceRecordError()
		}
	}
}

// actionJSON is how an action is stored.
type actionJSON struct {
	Type    string         `json:"type"`
	Tool    string         `json:"tool,omitempty"`
	Args    map[string]any `json:"args,omitempty"`
	State   string         `json:"state,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
	// A delegate action: the workflow, the request and why.
	Workflow string `json:"workflow,omitempty"`
	Message  any    `json:"message,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func toStep(rec workflow.StepRecord) (Step, error) {
	st := Step{
		TaskID: rec.RunID, Step: rec.Step, Attempt: rec.Attempt, State: rec.State, Outcome: string(rec.Outcome),
		InputTokens: rec.InputTokens, OutputTokens: rec.OutputTokens, DurationMS: int(rec.Duration.Milliseconds()), At: rec.At,
	}
	var err error
	if rec.Error != "" {
		st.Error = &rec.Error
	}
	if rec.Action != nil {
		a := rec.Action
		if st.Action, err = json.Marshal(actionJSON{Type: string(a.Type), Tool: a.Tool, Args: a.Args, State: a.State, Payload: a.Payload, Workflow: a.Workflow, Message: a.Message, Reason: a.Reason}); err != nil {
			return Step{}, fmt.Errorf("encode action: %w", err)
		}
	}
	if len(rec.Patch) > 0 {
		if st.Patch, err = json.Marshal(rec.Patch); err != nil {
			return Step{}, fmt.Errorf("encode state patch: %w", err)
		}
	}
	if rec.Workflow != "" {
		st.WorkflowID = &rec.Workflow
	}
	st.Depth = rec.Depth
	if rec.Tool != "" {
		st.Tool = &rec.Tool
	}
	if rec.ToolResult != "" {
		st.ToolResult = &rec.ToolResult
	}
	if rec.Reply != "" {
		reply := cutReply(rec.Reply)
		st.Reply = &reply
	}
	return st, nil
}

// cutReply keeps at most MaxReplyBytes of a reply, ending on a whole character
// and saying that it was cut.
func cutReply(reply string) string {
	if len(reply) <= MaxReplyBytes {
		return reply
	}
	cut := MaxReplyBytes
	for cut > 0 && !utf8.RuneStart(reply[cut]) {
		cut--
	}
	return reply[:cut] + replyCutMarker
}
