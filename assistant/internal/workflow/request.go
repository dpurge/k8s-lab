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
	return buildRequest(p, wf, run, tools, observation, retry, nil)
}

// buildRequest is BuildRequest with the workflows a state may delegate to.
func buildRequest(p *Prompts, wf *Workflow, run *Run, tools ToolLookup, observation any, retry *Retry, intents []IntentInfo) (Request, error) {
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
	if state.Delegate {
		if intents == nil {
			intents = []IntentInfo{}
		}
		in.Delegates = &intents
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

	var uiSchema map[string]any
	if wf.UI != nil {
		uiSchema = p.UISchema
	}
	schema, err := ReplySchemaWithUI(wf, run.StateName, tools, uiSchema)
	if err != nil {
		return Request{}, err
	}
	system := p.System(run.language(), state.Output)
	if wf.UI != nil && p.UIGuide != "" {
		system += "\n\n" + p.UIGuide
	}
	if state.Delegate {
		system += "\n\n" + delegateGuide
	}
	return Request{
		System: system,
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
	// Delegates are the workflows the delegate action may name, with what each is
	// for; present only in a state that may delegate.
	Delegates *[]IntentInfo `json:"delegates,omitempty"`
}

// IntentInfo is what the model sees of a workflow it may delegate to.
type IntentInfo struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Examples    []string `json:"examples,omitempty"`
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
	return ReplySchemaWithUI(wf, stateName, tools, nil)
}

// ReplySchemaWithUI is ReplySchema with the schema of the "ui" value of the
// ask_user payload, so that a model writing an interface is held to its shape.
// The payload still allows any other key, and "ui" stays optional. A nil
// uiSchema, or a workflow that does not declare ui, gives the plain ReplySchema.
func ReplySchemaWithUI(wf *Workflow, stateName string, tools ToolLookup, uiSchema map[string]any) (map[string]any, error) {
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
	payload := map[string]any{"type": "object"}
	if wf.UI != nil && uiSchema != nil {
		payload["properties"] = map[string]any{"ui": uiSchema}
	}
	actions = append(actions, map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"type", "payload"},
		"properties": map[string]any{
			"type":    map[string]any{"const": "ask_user"},
			"payload": payload,
		},
	})

	if state.Delegate {
		actions = append(actions, map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"type", "workflow", "message"},
			"properties": map[string]any{
				"type":     map[string]any{"const": "delegate"},
				"workflow": map[string]any{"type": "string"},
				"message":  map[string]any{},
				"reason":   map[string]any{"type": "string"},
			},
		})
	}

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

// delegateGuide teaches the delegate action to a model in a state that has it.
const delegateGuide = `DELEGATION
This state may hand the user's request to another workflow, which then talks to the user: {"type":"delegate","workflow":"<an id from delegates>","message":"<the request, in the user's words>","reason":"<a few words on why this workflow>"}. The input's "delegates" lists the workflows you may name and what each is for, with example requests. Choose by what the user asks for, not by the words of the example. When that workflow finishes, its final state comes back as your next observation, {"delegate_result":{"workflow":"<id>","state":{...}}}, or {"delegate_error":{...}} if it failed. If no workflow fits, do not delegate: ask one short question with ask_user, or answer yourself.`
