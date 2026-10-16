package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"assistant/internal/tasks"
	"assistant/internal/workflow"
)

// Runner runs the workflow a task names: a built-in one (noop, ask), else a
// YAML workflow through the engine. It is what the worker runs tasks with.
type Runner struct {
	builtins *Registry
	engine   *EngineRunner
}

// NewRunner returns a Runner. engine may be nil, for no YAML workflows.
func NewRunner(builtins *Registry, engine *EngineRunner) *Runner {
	return &Runner{builtins: builtins, engine: engine}
}

// Run implements tasks.Runner. A workflow nobody knows fails the task.
func (r *Runner) Run(ctx context.Context, t tasks.Task) (tasks.Outcome, error) {
	if r.builtins.Has(t.WorkflowID) {
		result, err := r.builtins.Run(ctx, t)
		return tasks.Outcome{Result: result}, err
	}
	if r.engine != nil && r.engine.Has(t.WorkflowID) {
		return r.engine.Run(ctx, t)
	}
	return tasks.Outcome{}, fmt.Errorf("unknown workflow: %s", t.WorkflowID)
}

// Info describes a workflow a task can name.
type Info struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	// Kind is "builtin" or "yaml".
	Kind string `json:"kind"`
	// PayloadHint is a payload that works as a starting point.
	PayloadHint string `json:"payload_hint"`
}

// builtinInfo describes the built-in workflows; the built-ins themselves
// are in workflows.go.
var builtinInfo = map[string]Info{
	"noop": {ID: "noop", Description: "Returns its payload as the result, after waiting sleep_ms milliseconds (at most 10 s). For watching the queue work.", Kind: "builtin", PayloadHint: `{"sleep_ms": 1500}`},
	"ask":  {ID: "ask", Description: "One call to a model: payload prompt, optionally system and tags; the result is its text.", Kind: "builtin", PayloadHint: `{"prompt": "Say ready", "tags": ["chat"]}`},
}

// Catalog lists every workflow a task can name, by id, with what it is and a
// payload to start from.
func (r *Runner) Catalog() []Info {
	var out []Info
	for _, id := range r.builtins.Names() {
		if info, ok := builtinInfo[id]; ok {
			out = append(out, info)
		}
	}
	if r.engine != nil {
		for _, id := range r.engine.Names() {
			out = append(out, Info{ID: id, Description: r.engine.Workflows[id].Description, Kind: "yaml", PayloadHint: `{"message": "start"}`})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Names lists every workflow a task can name.
func (r *Runner) Names() []string {
	names := r.builtins.Names()
	if r.engine != nil {
		names = append(names, r.engine.Names()...)
	}
	sort.Strings(names)
	return names
}

// EngineRunner runs YAML workflows with the engine.
type EngineRunner struct {
	Engine    *workflow.Engine
	Workflows map[string]*workflow.Workflow
}

// Has reports whether a YAML workflow has the id.
func (e *EngineRunner) Has(id string) bool { _, ok := e.Workflows[id]; return ok }

// Names lists the YAML workflow ids.
func (e *EngineRunner) Names() []string {
	names := make([]string, 0, len(e.Workflows))
	for id := range e.Workflows {
		names = append(names, id)
	}
	sort.Strings(names)
	return names
}

// enginePayload is what a task for a YAML workflow carries.
type enginePayload struct {
	Message  any            `json:"message"`
	State    map[string]any `json:"state"`
	Language string         `json:"language"`
}

// Run starts the task's workflow, or, when the task already holds a run,
// resumes it with the answer the user sent. The run id is the task id. The
// engine's own failures fail the task with their reason, so they are shown;
// the run snapshot is returned either way, for inspection.
func (e *EngineRunner) Run(ctx context.Context, t tasks.Task) (tasks.Outcome, error) {
	wf, ok := e.Workflows[t.WorkflowID]
	if !ok {
		return tasks.Outcome{}, fmt.Errorf("unknown workflow: %s", t.WorkflowID)
	}

	var run *workflow.Run
	var res workflow.Result
	var err error
	if len(t.Run) == 0 {
		var p enginePayload
		dec := json.NewDecoder(bytes.NewReader(t.Payload))
		dec.UseNumber()
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return tasks.Outcome{}, fmt.Errorf("payload must be an object with message, and optionally state and language: %w", err)
		}
		run, res, err = e.Engine.Start(ctx, wf, t.ID, workflow.Input{State: p.State, Language: p.Language, Message: p.Message})
	} else {
		if len(t.ResumeMessage) == 0 {
			return tasks.Outcome{}, errors.New("the task holds a run but no answer to resume it with")
		}
		run, err = workflow.RestoreRun(t.Run)
		if err != nil {
			return tasks.Outcome{}, fmt.Errorf("the stored run cannot be read: %w", err)
		}
		var message any
		dec := json.NewDecoder(bytes.NewReader(t.ResumeMessage))
		dec.UseNumber()
		if err := dec.Decode(&message); err != nil {
			return tasks.Outcome{Run: t.Run}, fmt.Errorf("the stored answer cannot be read: %w", err)
		}
		res, err = e.Engine.Resume(ctx, wf, run, message)
	}
	snapshot := t.Run
	if run != nil {
		if raw, marshalErr := json.Marshal(run); marshalErr == nil {
			snapshot = raw
		}
	}
	if err != nil {
		return tasks.Outcome{Run: snapshot}, fmt.Errorf("engine: %w", err)
	}

	switch res.Status {
	case workflow.StatusWaiting:
		ask, err := json.Marshal(res.Ask)
		if err != nil {
			return tasks.Outcome{Run: snapshot}, fmt.Errorf("the question to the user cannot be encoded: %w", err)
		}
		return tasks.Outcome{Waiting: true, Run: snapshot, Ask: ask}, nil
	case workflow.StatusSucceeded:
		result, err := json.Marshal(map[string]any{"state": run.State})
		if err != nil {
			return tasks.Outcome{Run: snapshot}, fmt.Errorf("the final state cannot be encoded: %w", err)
		}
		return tasks.Outcome{Result: result, Run: snapshot}, nil
	case workflow.StatusFailed:
		reason, message := workflow.FailReason("unknown"), "the run failed without a reason"
		if res.Failure != nil {
			reason, message = res.Failure.Reason, res.Failure.Message
		}
		return tasks.Outcome{Run: snapshot}, fmt.Errorf("%s: %s", reason, message)
	}
	return tasks.Outcome{Run: snapshot}, fmt.Errorf("the engine returned status %q, which a task cannot end in", res.Status)
}

// LoadDir loads every *.yaml in dir as a workflow: each is checked against the
// workflow schema and linted with tools. Every problem of every file is
// reported together as file:line: path: message, so a deployment is fixed in
// one go. reserved are ids the built-in workflows own. No workflow file at all
// is not an error: an assistant may have only the built-ins.
func LoadDir(dir string, tools workflow.ToolSet, reserved []string) (map[string]*workflow.Workflow, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	taken := map[string]bool{}
	for _, id := range reserved {
		taken[id] = true
	}
	var problems []string
	out := map[string]*workflow.Workflow{}
	owner := map[string]string{}
	for _, file := range files {
		wf, err := workflow.Load(file)
		if err != nil {
			var lerr *workflow.LoadError
			if errors.As(err, &lerr) {
				for _, f := range lerr.Findings {
					problems = append(problems, fmt.Sprintf("%s:%d: %s: %s", file, f.Line, f.Path, f.Message))
				}
			} else {
				problems = append(problems, fmt.Sprintf("%s: %v", file, err))
			}
			continue
		}
		for _, f := range workflow.Lint(wf, tools) {
			problems = append(problems, fmt.Sprintf("%s:%d: %s: %s", file, f.Line, f.Path, f.Message))
		}
		switch {
		case taken[wf.ID] && owner[wf.ID] == "":
			problems = append(problems, fmt.Sprintf("%s: workflow id %q is a built-in workflow's", file, wf.ID))
		case owner[wf.ID] != "":
			problems = append(problems, fmt.Sprintf("%s: workflow id %q is already used by %s", file, wf.ID, owner[wf.ID]))
		default:
			owner[wf.ID] = file
			out[wf.ID] = wf
		}
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "\n"))
	}
	return out, nil
}
