package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s-lab/shared/router"
)

// Request is what the engine sends the model for one step. It is the router's
// request type, so the engine's LLM can be a router without conversion.
type Request = router.Request

// Response is the model's reply with the tokens it cost.
type Response = router.Response

// LLM is the only way the engine talks to a model. Real clients live outside
// this package; tests use a scripted fake.
type LLM interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// Retry tells the model why its previous reply for this step was refused.
type Retry struct {
	Reply string `json:"reply"`
	Error string `json:"error"`
}

// maxRetryReplyBytes bounds how much of a refused reply is echoed back.
const maxRetryReplyBytes = 2000

// BuildRequest builds the request for the run's current step. observation is
// the result of the previous action (a tool result, an error, the user's
// message) or nil. retry is non-nil when this is a retry of the same step.
func BuildRequest(p *Prompts, wf *Workflow, run *Run, tools ToolLookup, observation any, retry *Retry) (Request, error) {
	state, ok := wf.States[run.StateName]
	if !ok {
		return Request{}, fmt.Errorf("run is in unknown state %q of workflow %q", run.StateName, wf.ID)
	}

	toolDescriptions := make([]toolInfo, 0, len(state.Tools))
	for _, name := range state.Tools {
		tool, ok := tools.Get(name)
		if !ok {
			return Request{}, fmt.Errorf("state %q lists tool %q which is not registered", run.StateName, name)
		}
		toolDescriptions = append(toolDescriptions, toolInfo{Name: name, ArgsSchema: tool.ArgsSchema()})
	}

	in := stepInput{
		Workflow: workflowInfo{ID: wf.ID, Description: wf.Description},
		State: stateInfo{
			Name:         run.StateName,
			Instructions: state.Instructions,
			Tools:        toolDescriptions,
			Next:         nonNil(state.Next),
		},
		RunStateSchema: nonNilObject(wf.StateSchema),
		RunState:       nonNilObject(run.State),
		Observation:    observation,
	}
	if retry != nil {
		r := *retry
		if len(r.Reply) > maxRetryReplyBytes {
			r.Reply = r.Reply[:maxRetryReplyBytes] + "…(truncated)"
		}
		in.Retry = &r
	}
	input, err := json.Marshal(in)
	if err != nil {
		return Request{}, fmt.Errorf("encode step input: %w", err)
	}

	schema, err := ReplySchema(wf, run.StateName, tools)
	if err != nil {
		return Request{}, err
	}
	return Request{
		System: p.System(run.language(), state.Output),
		Input:  string(input),
		Schema: schema,
		Tags:   wf.TagsFor(run.StateName),
	}, nil
}

// stepInput is the JSON object the model receives as the user message. The
// run state's schema is part of it so the model sees every key and type the
// run state may hold, including keys that are not in it yet.
type stepInput struct {
	Workflow       workflowInfo   `json:"workflow"`
	State          stateInfo      `json:"state"`
	RunStateSchema map[string]any `json:"run_state_schema"`
	RunState       map[string]any `json:"run_state"`
	Observation    any            `json:"observation,omitempty"`
	Retry          *Retry         `json:"retry,omitempty"`
}

type workflowInfo struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

type stateInfo struct {
	Name         string     `json:"name"`
	Instructions string     `json:"instructions"`
	Tools        []toolInfo `json:"tools"`
	Next         []string   `json:"next"`
}

type toolInfo struct {
	Name       string         `json:"name"`
	ArgsSchema map[string]any `json:"args_schema"`
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilObject(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// ToolLookup finds a registered tool by name. *Registry implements it.
type ToolLookup interface {
	Get(name string) (Tool, bool)
}

// ReplySchema returns the JSON Schema of the replies the engine accepts in
// the named state: a state_patch object plus one action, limited to the
// state's own tools (with their argument schemas), next states and the
// ask_user action. An end state is an error: a run finishes on entering one
// and never asks the model anything there.
func ReplySchema(wf *Workflow, stateName string, tools ToolLookup) (map[string]any, error) {
	state, ok := wf.States[stateName]
	if !ok {
		return nil, fmt.Errorf("unknown state %q in workflow %q", stateName, wf.ID)
	}

	if state.End {
		return nil, fmt.Errorf("state %q is an end state; no reply is expected there", stateName)
	}

	var actions []any
	for _, name := range state.Tools {
		tool, ok := tools.Get(name)
		if !ok {
			return nil, fmt.Errorf("state %q lists tool %q which is not registered", stateName, name)
		}
		actions = append(actions, map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"type", "tool", "args"},
			"properties": map[string]any{
				"type": map[string]any{"const": "tool"},
				"tool": map[string]any{"const": name},
				"args": tool.ArgsSchema(),
			},
		})
	}
	if len(state.Next) > 0 {
		targets := make([]any, len(state.Next))
		for i, n := range state.Next {
			targets[i] = n
		}
		actions = append(actions, map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"type", "state"},
			"properties": map[string]any{
				"type":  map[string]any{"const": "goto"},
				"state": map[string]any{"enum": targets},
			},
		})
	}
	actions = append(actions, map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"type", "payload"},
		"properties": map[string]any{
			"type":    map[string]any{"const": "ask_user"},
			"payload": map[string]any{"type": "object"},
		},
	})

	return map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"state_patch", "action"},
		"properties": map[string]any{
			"state_patch": map[string]any{"type": "object"},
			"action":      map[string]any{"oneOf": actions},
		},
	}, nil
}
