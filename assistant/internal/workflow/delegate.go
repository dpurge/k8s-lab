package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Delegation: a run in a state that has `delegate: true` may hand the user's
// request to another workflow (a subagent) with the delegate action. The other
// workflow runs as a child of this run, with its own state and limits, and
// talks to the user in the same conversation: its questions come out of this
// run's Wait, and the user's answers are given to it. When it ends, its final
// state (or its failure) comes back to this run as the next observation. The
// child's steps continue the numbering of the parent's trace, its tokens count
// against the parent's budget, and the depth of nesting is limited.

// Delegates finds the workflows a run can delegate to.
type Delegates interface {
	// Workflow returns a workflow by id, whether or not it is offered: a child
	// that was started must be found again to be resumed.
	Workflow(id string) (*Workflow, bool)
	// Intents lists the workflows that are offered for delegation, with what
	// each is for, in a stable order.
	Intents() []IntentInfo
}

const defaultMaxDepth = 2

func (e *Engine) maxDepth() int {
	if e.MaxDepth > 0 {
		return e.MaxDepth
	}
	return defaultMaxDepth
}

// tokenLimit is the tokens a run may use: its workflow's limit, or the part of
// its parent's budget that was left when it started, whichever is smaller.
func tokenLimit(wf *Workflow, run *Run) int {
	limit := wf.Limits.MaxTokens
	if run.TokenCap > 0 && run.TokenCap < limit {
		limit = run.TokenCap
	}
	return limit
}

// intentsFor lists what the model of this step may delegate to: nothing outside
// a state that delegates, or when the run is as deep as it may go.
func (e *Engine) intentsFor(wf *Workflow, run *Run) []IntentInfo {
	if !wf.States[run.StateName].Delegate || e.Delegates == nil || run.Depth >= e.maxDepth() {
		return nil
	}
	return e.Delegates.Intents()
}

// checkDelegate refuses a delegate action that cannot be carried out, in words
// the model can act on.
func (e *Engine) checkDelegate(run *Run, reply *Reply) string {
	if reply.Action.Type != ActionDelegate {
		return ""
	}
	if e.Delegates == nil {
		return "no workflow can be delegated to here"
	}
	if run.Depth >= e.maxDepth() {
		return fmt.Sprintf("this run is %d levels down, which is as deep as delegation goes: do not delegate, answer or ask the user instead", run.Depth)
	}
	offered := e.Delegates.Intents()
	names := make([]string, len(offered))
	for i, in := range offered {
		if in.ID == reply.Action.Workflow {
			return ""
		}
		names[i] = in.ID
	}
	sort.Strings(names)
	return fmt.Sprintf("there is no workflow %q to delegate to; delegates are: %s", reply.Action.Workflow, strings.Join(names, ", "))
}

// delegation is what handing a request on came to: done says the run's drive
// is over (the child waits for the user, or the run ended), and Result is what
// the drive returns then.
type delegation struct {
	Result
	done bool
}

// delegate starts the child run for a delegate action and drives it.
func (e *Engine) delegate(ctx context.Context, wf *Workflow, run *Run, v *StateValidator, a Action) (delegation, error) {
	childWF, ok := e.Delegates.Workflow(a.Workflow)
	if !ok {
		return delegation{}, fmt.Errorf("workflow %q vanished from the delegates", a.Workflow)
	}
	if err := e.check(childWF); err != nil {
		return delegation{}, err
	}
	childValidator, err := NewStateValidator(childWF.StateSchema)
	if err != nil {
		return delegation{}, err
	}
	message, err := normalizeJSON(a.Message)
	if err != nil {
		return delegation{}, fmt.Errorf("delegated message: %w", err)
	}
	child := NewRun(childWF, run.RunID, nil)
	child.Depth = run.Depth + 1
	child.StepBase = run.StepBase + run.Steps
	child.Language = run.Language
	child.TokenCap = max(tokenLimit(wf, run)-run.Tokens, 1)
	child.Observation = message
	routing := &Routing{Workflow: a.Workflow, Reason: a.Reason}
	child.Routing = routing
	run.Child = child

	res, err := e.begin(ctx, childWF, child, childValidator)
	if err != nil {
		return delegation{}, err
	}
	res = e.named(childWF, res)
	res.Routed = routing
	return e.afterChild(run, childWF, res), nil
}

// resumeChild gives the user's message to the child run of a waiting run, and
// drives the parent on if the child ended.
func (e *Engine) resumeChild(ctx context.Context, wf *Workflow, run *Run, v *StateValidator, message any) (Result, error) {
	child := run.Child
	childWF, ok := e.lookup(child.WorkflowID)
	if !ok {
		return e.fail(ctx, run, Failure{FailLLM, fmt.Sprintf("the workflow %q that this run delegated to is not known any more", child.WorkflowID)}, false), nil
	}
	res, err := e.Resume(ctx, childWF, child, message)
	if err != nil {
		return Result{}, err
	}
	d := e.afterChild(run, childWF, res)
	if d.done {
		return d.Result, nil
	}
	res, err = e.drive(ctx, wf, run, v)
	return e.named(wf, res), err
}

func (e *Engine) lookup(id string) (*Workflow, bool) {
	if e.Delegates == nil {
		return nil, false
	}
	return e.Delegates.Workflow(id)
}

// afterChild takes what the child's drive returned: a child that waits keeps
// this run waiting, with the child's question; one that ended is folded into
// this run's accounting and handed back to it as an observation.
func (e *Engine) afterChild(run *Run, childWF *Workflow, res Result) delegation {
	child := run.Child
	if res.Status == StatusWaiting {
		run.Status = StatusWaiting
		return delegation{Result: res, done: true}
	}
	run.Steps = child.StepBase + child.Steps - run.StepBase
	run.Tokens += child.Tokens
	run.Child = nil
	run.Status = StatusRunning
	switch res.Status {
	case StatusSucceeded:
		run.Observation = map[string]any{"delegate_result": map[string]any{"workflow": childWF.ID, "state": child.State}}
	default:
		failure := child.Failure
		if res.Failure != nil {
			failure = res.Failure
		}
		reason, message := FailReason("unknown"), "the run failed without a reason"
		if failure != nil {
			reason, message = failure.Reason, failure.Message
		}
		run.Observation = map[string]any{"delegate_error": map[string]any{"workflow": childWF.ID, "reason": string(reason), "message": message}}
	}
	return delegation{Result: Result{Routed: res.Routed}}
}
