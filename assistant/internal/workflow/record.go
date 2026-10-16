package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Outcome is how one model attempt ended.
type Outcome string

const (
	// OutcomeOK: the reply was valid and its action was carried out.
	OutcomeOK Outcome = "ok"
	// OutcomeRetry: the reply was refused and the step will be retried.
	OutcomeRetry Outcome = "retry"
	// OutcomeRejected: the reply was refused and no retries are left.
	OutcomeRejected Outcome = "rejected"
	// OutcomeFailed: the run failed here, for a reason other than a refused reply.
	OutcomeFailed Outcome = "failed"
)

// StepRecord is one entry of a run's trace: one model attempt, or one run
// failure that happened without a model call.
type StepRecord struct {
	RunID   string
	Step    int // 1-based number of the step this attempt belongs to
	Attempt int // 1-based attempt within the step; 0 for a failure with no model call
	State   string

	RequestDigest string // short hash of the request, to spot changes without storing prompts
	Reply         string // the model's raw reply
	Patch         map[string]any
	Action        *Action
	Outcome       Outcome
	Error         string // why the reply was refused, or why the run failed

	Tool       string // set when the action was a tool call
	ToolResult string // a short summary of the tool result or error

	InputTokens  int
	OutputTokens int
	Duration     time.Duration
	At           time.Time
}

// Recorder receives every StepRecord as it happens. The engine ignores its
// return value but a Recorder must not block for long.
type Recorder interface {
	Record(ctx context.Context, rec StepRecord)
}

// discardRecorder is used when an Engine has no Recorder.
type discardRecorder struct{}

func (discardRecorder) Record(context.Context, StepRecord) {}

// Clock tells the time. Tests inject a fake.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func digestRequest(req Request) string {
	h := sha256.New()
	h.Write([]byte(req.System))
	h.Write([]byte{0})
	h.Write([]byte(req.Input))
	return hex.EncodeToString(h.Sum(nil))[:12]
}
