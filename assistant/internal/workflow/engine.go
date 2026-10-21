package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	defaultToolTimeout         = 30 * time.Second
	defaultMaxObservationBytes = 8000
	maxToolSummaryBytes        = 200
)

// Engine runs workflows. It holds no per-run state and starts no goroutines
// of its own, so one Engine can serve many runs; the caller drives each run
// with Start and Resume and supplies the context.
type Engine struct {
	LLM      LLM
	Tools    ToolLookup
	Prompts  *Prompts
	Recorder Recorder // optional
	Clock    Clock    // optional, defaults to the real clock
	// UI checks the interface in an ask_user payload of a workflow that
	// declares ui; optional (without it the interface is checked when it is
	// shown, and a model that got it wrong is not told).
	UI InterfaceChecker
	// Delegates finds the workflows a state that may delegate can hand a request
	// to; optional (without it no delegation is possible).
	Delegates Delegates
	// MaxDepth is how many runs may be nested: a run at this depth cannot
	// delegate. Zero means 2.
	MaxDepth int

	// ToolTimeout bounds one tool call; zero means 30 seconds.
	ToolTimeout time.Duration
	// MaxObservationBytes bounds a tool result shown to the model, which is
	// cut with a visible marker beyond it; zero means 8000.
	MaxObservationBytes int
}

// Input starts a run.
type Input struct {
	// State is the initial run state; nil means empty.
	State map[string]any
	// Language is the language code for prompt snippets.
	Language string
	// Message is the first observation, typically the user's request.
	Message any
}

// Result says how a call to Start or Resume ended.
type Result struct {
	Status  Status
	Ask     map[string]any // the ask_user payload when Status is StatusWaiting
	Failure *Failure       // set when Status is StatusFailed
	// Workflow is the workflow that is asking, when Status is StatusWaiting: the
	// run's own, or the one it delegated to (the deepest one that is going).
	Workflow string
	// Routed is set when this call handed a request to another workflow: which,
	// and why.
	Routed *Routing
}

// ErrNotWaiting is returned by Resume for a run that is not waiting.
var ErrNotWaiting = errors.New("run is not waiting for the user")

// Start begins a run of wf and drives it until it finishes, fails or waits
// for the user. An error is returned only for misuse or a broken
// configuration, never for a run that fails: that is Result.Status.
func (e *Engine) Start(ctx context.Context, wf *Workflow, runID string, in Input) (*Run, Result, error) {
	if err := e.check(wf); err != nil {
		return nil, Result{}, err
	}
	v, err := NewStateValidator(wf.StateSchema)
	if err != nil {
		return nil, Result{}, err
	}
	state, err := normalizeObject(in.State)
	if err != nil {
		return nil, Result{}, fmt.Errorf("initial state: %w", err)
	}
	if err := v.Validate(state, false); err != nil {
		return nil, Result{}, fmt.Errorf("initial state: %w", err)
	}
	message, err := normalizeJSON(in.Message)
	if err != nil {
		return nil, Result{}, fmt.Errorf("initial message: %w", err)
	}
	run := NewRun(wf, runID, state)
	run.Language = in.Language
	run.Observation = message

	res, err := e.begin(ctx, wf, run, v)
	return run, e.named(wf, res), err
}

// begin drives a new run from its start state.
func (e *Engine) begin(ctx context.Context, wf *Workflow, run *Run, v *StateValidator) (Result, error) {
	if wf.States[wf.Start].End {
		return e.finish(ctx, wf, run, v)
	}
	return e.drive(ctx, wf, run, v)
}

// Resume continues a waiting run with the user's message as the observation
// and drives it on. It changes run in place. A run that is not waiting
// yields ErrNotWaiting and is left untouched, so resuming twice is refused.
func (e *Engine) Resume(ctx context.Context, wf *Workflow, run *Run, message any) (Result, error) {
	if err := e.check(wf); err != nil {
		return Result{}, err
	}
	if run.WorkflowID != wf.ID {
		return Result{}, fmt.Errorf("run %s belongs to workflow %q, not %q", run.RunID, run.WorkflowID, wf.ID)
	}
	if run.Status != StatusWaiting {
		return Result{}, fmt.Errorf("%w: run %s is %s", ErrNotWaiting, run.RunID, run.Status)
	}
	if _, ok := wf.States[run.StateName]; !ok {
		return Result{}, fmt.Errorf("run %s is in unknown state %q of workflow %q", run.RunID, run.StateName, wf.ID)
	}
	v, err := NewStateValidator(wf.StateSchema)
	if err != nil {
		return Result{}, err
	}
	if run.Child != nil {
		return e.resumeChild(ctx, wf, run, v, message)
	}
	run.Status = StatusRunning
	run.Observation = message
	res, err := e.drive(ctx, wf, run, v)
	return e.named(wf, res), err
}

// named says, for a run that waits, which workflow is asking.
func (e *Engine) named(wf *Workflow, res Result) Result {
	if res.Status == StatusWaiting && res.Workflow == "" {
		res.Workflow = wf.ID
	}
	return res
}

func (e *Engine) check(wf *Workflow) error {
	switch {
	case e.LLM == nil:
		return errors.New("engine has no LLM")
	case e.Tools == nil:
		return errors.New("engine has no tools (use an empty Registry for none)")
	case e.Prompts == nil:
		return errors.New("engine has no prompts")
	}
	if _, ok := wf.States[wf.Start]; !ok {
		return fmt.Errorf("workflow %q has no start state %q", wf.ID, wf.Start)
	}
	return nil
}

func (e *Engine) clock() Clock {
	if e.Clock != nil {
		return e.Clock
	}
	return realClock{}
}

func (e *Engine) recorder() Recorder {
	if e.Recorder != nil {
		return e.Recorder
	}
	return discardRecorder{}
}

func (e *Engine) toolTimeout() time.Duration {
	if e.ToolTimeout > 0 {
		return e.ToolTimeout
	}
	return defaultToolTimeout
}

func (e *Engine) maxObservationBytes() int {
	if e.MaxObservationBytes > 0 {
		return e.MaxObservationBytes
	}
	return defaultMaxObservationBytes
}

// stepFailure is a failure to complete one step, before it becomes a run
// failure or an on_error detour.
type stepFailure struct {
	failure  Failure
	recorded bool // a StepRecord already shows this failure
}

func (e *Engine) drive(ctx context.Context, wf *Workflow, run *Run, v *StateValidator) (Result, error) {
	var routed *Routing // set when this drive handed a request to another workflow
	for {
		if err := ctx.Err(); err != nil {
			return e.fail(ctx, run, Failure{FailCancelled, "context ended: " + err.Error()}, false), nil
		}
		if run.Steps >= wf.Limits.MaxSteps {
			return e.fail(ctx, run, Failure{FailMaxSteps, fmt.Sprintf("run used its %d steps", wf.Limits.MaxSteps)}, false), nil
		}
		if limit := tokenLimit(wf, run); run.Tokens >= limit {
			return e.fail(ctx, run, Failure{FailMaxTokens, fmt.Sprintf("run used its %d tokens", limit)}, false), nil
		}

		state := wf.States[run.StateName]
		reply, pending, sf, err := e.attemptStep(ctx, wf, run, v)
		if err != nil {
			return Result{}, err
		}
		if sf == nil {
			if sf = e.checkTransition(wf, run, reply); sf != nil {
				pending.Outcome, pending.Error = OutcomeFailed, fmt.Sprintf("%s: %s", sf.failure.Reason, sf.failure.Message)
				e.emit(ctx, pending)
				sf.recorded = true
			}
		}
		if sf != nil {
			if e.isRedirectable(sf.failure.Reason) && state.OnError != "" && !run.ErrorEdgeUsed {
				res, done, err := e.redirect(ctx, wf, run, state.OnError, sf.failure, v)
				if err != nil {
					return Result{}, err
				}
				if done {
					return res, nil
				}
				continue
			}
			return e.fail(ctx, run, sf.failure, sf.recorded), nil
		}

		run.Steps++
		switch reply.Action.Type {
		case ActionTool:
			tool, ok := e.Tools.Get(reply.Action.Tool)
			if !ok {
				return Result{}, fmt.Errorf("tool %q vanished from the registry", reply.Action.Tool)
			}
			obs, summary := e.callTool(ctx, tool, reply.Action.Args)
			run.Observation = obs
			pending.Tool, pending.ToolResult = reply.Action.Tool, summary
			e.emit(ctx, pending)
		case ActionGoto:
			target := reply.Action.State
			run.StateName = target
			run.Visits[target]++
			run.Observation = nil
			e.emit(ctx, pending)
			if wf.States[target].End {
				return e.finish(ctx, wf, run, v)
			}
		case ActionAskUser:
			run.Status = StatusWaiting
			run.Observation = nil
			e.emit(ctx, pending)
			return Result{Status: StatusWaiting, Ask: reply.Action.Payload, Routed: routed}, nil
		case ActionDelegate:
			run.Observation = nil
			e.emit(ctx, pending)
			res, err := e.delegate(ctx, wf, run, v, reply.Action)
			if err != nil {
				return Result{}, err
			}
			if res.Routed != nil {
				routed = res.Routed
			}
			if res.done {
				return res.Result, nil
			}
		}
	}
}

// checkTransition refuses a goto that would enter a state too often.
func (e *Engine) checkTransition(wf *Workflow, run *Run, reply *Reply) *stepFailure {
	if reply.Action.Type != ActionGoto {
		return nil
	}
	target := reply.Action.State
	if run.Visits[target]+1 > wf.Limits.MaxVisits {
		return &stepFailure{failure: Failure{FailMaxVisits, fmt.Sprintf("state %q would be entered more than %d times", target, wf.Limits.MaxVisits)}}
	}
	return nil
}

func (e *Engine) isRedirectable(reason FailReason) bool {
	switch reason {
	case FailRetriesExhausted, FailLLM, FailMaxVisits:
		return true
	}
	return false
}

// redirect takes the on_error edge: the run moves to target with the failure
// as its observation. It reports done when the run ended as a result.
func (e *Engine) redirect(ctx context.Context, wf *Workflow, run *Run, target string, f Failure, v *StateValidator) (Result, bool, error) {
	run.ErrorEdgeUsed = true
	run.Steps++
	if run.Visits[target]+1 > wf.Limits.MaxVisits {
		return e.fail(ctx, run, Failure{FailMaxVisits, fmt.Sprintf("on_error state %q would be entered more than %d times (after: %s)", target, wf.Limits.MaxVisits, f.Message)}, false), true, nil
	}
	run.StateName = target
	run.Visits[target]++
	run.Observation = map[string]any{"error": map[string]any{"reason": string(f.Reason), "message": f.Message}}
	if wf.States[target].End {
		res, err := e.finish(ctx, wf, run, v)
		return res, true, err
	}
	return Result{}, false, nil
}

// finish ends a run that reached an end state, checking that its state is
// complete.
func (e *Engine) finish(ctx context.Context, wf *Workflow, run *Run, v *StateValidator) (Result, error) {
	if err := v.Validate(run.State, true); err != nil {
		return e.fail(ctx, run, Failure{FailIncompleteState, err.Error()}, false), nil
	}
	run.Status = StatusSucceeded
	run.Observation = nil
	return Result{Status: StatusSucceeded}, nil
}

func (e *Engine) fail(ctx context.Context, run *Run, f Failure, recorded bool) Result {
	run.Status = StatusFailed
	run.Failure = &f
	if !recorded {
		e.emit(ctx, StepRecord{
			RunID: run.RunID, Step: run.StepBase + run.Steps + 1, State: run.StateName, Depth: run.Depth,
			Outcome: OutcomeFailed, Error: fmt.Sprintf("%s: %s", f.Reason, f.Message),
			At: e.clock().Now(),
		})
	}
	return Result{Status: StatusFailed, Failure: &f}
}

func (e *Engine) emit(ctx context.Context, rec StepRecord) {
	e.recorder().Record(ctx, rec)
}

// attemptStep asks the model until it gives a valid reply or the retries run
// out. On success it returns the reply and a record of the attempt, not yet
// emitted because the action's outcome is still to be added.
func (e *Engine) attemptStep(ctx context.Context, wf *Workflow, run *Run, v *StateValidator) (*Reply, StepRecord, *stepFailure, error) {
	var retry *Retry
	step := run.StepBase + run.Steps + 1
	for attempt := 1; ; attempt++ {
		req, err := buildRequest(e.Prompts, wf, run, e.Tools, run.Observation, retry, e.intentsFor(wf, run))
		if err != nil {
			return nil, StepRecord{}, nil, err
		}
		start := e.clock().Now()
		resp, err := e.LLM.Complete(ctx, req)
		rec := StepRecord{
			RunID: run.RunID, Step: step, Attempt: attempt, State: run.StateName, Workflow: wf.ID, Depth: run.Depth,
			RequestDigest: digestRequest(req), Reply: resp.Text,
			InputTokens: resp.InputTokens, OutputTokens: resp.OutputTokens,
			Duration: e.clock().Now().Sub(start), At: start,
		}
		if err != nil {
			rec.Outcome, rec.Error = OutcomeFailed, "model call failed: "+err.Error()
			e.emit(ctx, rec)
			if ctx.Err() != nil {
				return nil, rec, &stepFailure{Failure{FailCancelled, "context ended: " + ctx.Err().Error()}, true}, nil
			}
			return nil, rec, &stepFailure{Failure{FailLLM, err.Error()}, true}, nil
		}
		run.Tokens += resp.InputTokens + resp.OutputTokens
		if limit := tokenLimit(wf, run); run.Tokens > limit {
			rec.Outcome, rec.Error = OutcomeFailed, fmt.Sprintf("%s: run used %d of %d tokens", FailMaxTokens, run.Tokens, limit)
			e.emit(ctx, rec)
			return nil, rec, &stepFailure{Failure{FailMaxTokens, fmt.Sprintf("run used %d tokens, over its %d", run.Tokens, limit)}, true}, nil
		}

		reply, refusal := e.parseAndApply(wf, run, v, resp.Text)
		if refusal == "" {
			rec.Patch, rec.Action, rec.Outcome = reply.StatePatch, &reply.Action, OutcomeOK
			return reply, rec, nil, nil
		}

		rec.Error = refusal
		if attempt > wf.Limits.MaxRetries {
			rec.Outcome = OutcomeRejected
			e.emit(ctx, rec)
			return nil, rec, &stepFailure{Failure{FailRetriesExhausted, fmt.Sprintf("model gave no valid reply in %d attempts; last problem: %s", attempt, refusal)}, true}, nil
		}
		rec.Outcome = OutcomeRetry
		e.emit(ctx, rec)
		retry = &Retry{Reply: resp.Text, Error: refusal}
	}
}

// parseAndApply parses the reply and commits its patch to the run state. It
// returns the model-facing reason when the reply is refused; the run state is
// untouched in that case. An internal problem is reported the same way,
// because the engine cannot tell the model anything better.
func (e *Engine) parseAndApply(wf *Workflow, run *Run, v *StateValidator, text string) (*Reply, string) {
	reply, err := ParseReply(wf, run.StateName, e.Tools, text)
	if err != nil {
		return nil, err.Error()
	}
	if refusal := e.checkInterface(wf, reply); refusal != "" {
		return nil, refusal
	}
	if refusal := e.checkDelegate(run, reply); refusal != "" {
		return nil, refusal
	}
	complete := reply.Action.Type == ActionGoto && wf.States[reply.Action.State].End
	if err := run.ApplyPatch(v, reply.StatePatch, complete); err != nil {
		var serr *StateError
		if errors.As(err, &serr) {
			return nil, "the state_patch would make the run state invalid: " + strings.Join(serr.Problems, "; ")
		}
		return nil, err.Error()
	}
	return reply, ""
}

// callTool runs a tool with a timeout and turns its result or failure into
// the observation for the next step, plus a short summary for the trace. The
// tool runs in its own goroutine so that a tool ignoring its context still
// cannot hold the run past the timeout.
func (e *Engine) callTool(ctx context.Context, tool Tool, args map[string]any) (any, string) {
	timeout := e.toolTimeout()
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type outcome struct {
		result any
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- outcome{err: fmt.Errorf("tool panicked: %v", r)}
			}
		}()
		result, err := tool.Call(cctx, args)
		done <- outcome{result: result, err: err}
	}()

	select {
	case out := <-done:
		if out.err != nil {
			return errorObservation(out.err.Error())
		}
		return e.resultObservation(out.result)
	case <-cctx.Done():
		if ctx.Err() != nil {
			return errorObservation("tool call cancelled: " + ctx.Err().Error())
		}
		return errorObservation(fmt.Sprintf("tool timed out after %s", timeout))
	}
}

func errorObservation(msg string) (any, string) {
	return map[string]any{"error": msg}, "error: " + shorten(msg, maxToolSummaryBytes)
}

func (e *Engine) resultObservation(result any) (any, string) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return errorObservation("tool result is not JSON-encodable: " + err.Error())
	}
	limit := e.maxObservationBytes()
	if len(encoded) > limit {
		cut := truncateUTF8(string(encoded), limit)
		omitted := len(encoded) - len(cut)
		obs := fmt.Sprintf("%s…[truncated: %d bytes omitted]", cut, omitted)
		return obs, shorten(string(encoded), maxToolSummaryBytes)
	}
	normalised, err := normalizeJSON(result)
	if err != nil {
		return errorObservation("tool result could not be decoded: " + err.Error())
	}
	return normalised, shorten(string(encoded), maxToolSummaryBytes)
}

func shorten(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return truncateUTF8(s, max) + "…"
}

// truncateUTF8 cuts s to at most n bytes without splitting a character.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// normalizeJSON round-trips v through JSON so that numbers become
// json.Number, the form the validator and run snapshots use.
func normalizeJSON(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return jsonschema.UnmarshalJSON(strings.NewReader(string(encoded)))
}

func normalizeObject(obj map[string]any) (map[string]any, error) {
	if obj == nil {
		return nil, nil
	}
	v, err := normalizeJSON(obj)
	if err != nil {
		return nil, err
	}
	return v.(map[string]any), nil
}

// InterfaceChecker validates the A2UI messages of an interface on their own
// (the assistant's validator implements it).
type InterfaceChecker interface {
	CheckInterface(messages []json.RawMessage) error
}

// checkInterface refuses a reply whose ask_user payload holds an interface that
// is not valid, in words the model can act on. Only workflows that declare ui
// are checked here.
func (e *Engine) checkInterface(wf *Workflow, reply *Reply) string {
	if wf.UI == nil || e.UI == nil || reply.Action.Type != ActionAskUser {
		return ""
	}
	raw, present := reply.Action.Payload["ui"]
	if !present {
		return ""
	}
	list, ok := raw.([]any)
	if !ok {
		return `payload.ui must be an array of A2UI messages, each an object with "version": "v1.0"`
	}
	messages := make([]json.RawMessage, len(list))
	for i, item := range list {
		b, err := json.Marshal(item)
		if err != nil {
			return fmt.Sprintf("payload.ui[%d] cannot be encoded: %v", i, err)
		}
		messages[i] = b
	}
	if err := e.UI.CheckInterface(messages); err != nil {
		return "the interface in payload.ui is not valid A2UI: " + err.Error()
	}
	return ""
}
