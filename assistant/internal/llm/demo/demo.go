// Package demo is a model that is not a model: it acts out three fixed workflows,
// hello, ui-demo and orchestrator, from inside the assistant, instantly and with no load. It exists so the
// admin interface, the worker and the engine can be tried by hand, and tested,
// without a real model, whose slowness and mistakes hide everything else. It
// refuses every other workflow, so it cannot be mistaken for the real thing.
package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"assistant/internal/llm"
	"assistant/internal/workflow"
	"k8s-lab/shared/modelcfg"
)

// Provider answers the steps of the hello, ui-demo and orchestrator workflows.
type Provider struct{}

// New returns the provider.
func New() *Provider { return &Provider{} }

// stepInput is the part of the engine's step input the demo reads.
type stepInput struct {
	Workflow struct {
		ID string `json:"id"`
	} `json:"workflow"`
	State struct {
		Name string `json:"name"`
	} `json:"state"`
	RunState    map[string]any `json:"run_state"`
	Observation any            `json:"observation"`
	// Delegates are the workflows a delegating state may hand a request to.
	Delegates []struct {
		ID string `json:"id"`
	} `json:"delegates"`
}

// Complete implements llm.Provider.
func (Provider) Complete(_ context.Context, _ modelcfg.Provider, _ modelcfg.Model, req workflow.Request) (workflow.Response, error) {
	var in stepInput
	if err := json.Unmarshal([]byte(req.Input), &in); err != nil {
		return workflow.Response{}, &llm.Failure{Kind: llm.FailClient, Err: fmt.Errorf("the demo model answers only workflow steps, and this input is not one: %w", err)}
	}
	var reply string
	switch in.Workflow.ID {
	case "hello":
	case "orchestrator":
		ids := make([]string, len(in.Delegates))
		for i, d := range in.Delegates {
			ids[i] = d.ID
		}
		r, err := orchestratorReply(in.State.Name, in.Observation, ids)
		if err != nil {
			return workflow.Response{}, &llm.Failure{Kind: llm.FailClient, Err: err}
		}
		return workflow.Response{Text: r, InputTokens: len(req.Input) / 4, OutputTokens: len(r) / 4}, nil
	case "ui-demo":
		r, err := uiDemoReply(in.State.Name, in.Observation, in.RunState)
		if err != nil {
			return workflow.Response{}, &llm.Failure{Kind: llm.FailClient, Err: err}
		}
		return workflow.Response{Text: r, InputTokens: len(req.Input) / 4, OutputTokens: len(r) / 4}, nil
	default:
		return workflow.Response{}, &llm.Failure{Kind: llm.FailClient, Err: fmt.Errorf("the demo model knows only the hello, ui-demo and orchestrator workflows, not %q", in.Workflow.ID)}
	}

	switch in.State.Name {
	case "ask":
		asked, _ := in.RunState["asked"].(bool)
		answer, isText := in.Observation.(string)
		if !asked || !isText || strings.TrimSpace(answer) == "" {
			reply = `{"state_patch":{"asked":true},"action":{"type":"ask_user","payload":{"question":"What is your name?"}}}`
		} else {
			reply = fmt.Sprintf(`{"state_patch":{"name":%s},"action":{"type":"goto","state":"greet"}}`, quote(strings.TrimSpace(answer)))
		}
	case "greet":
		name, _ := in.RunState["name"].(string)
		reply = fmt.Sprintf(`{"state_patch":{"greeting":%s},"action":{"type":"goto","state":"done"}}`, quote("Hello, "+name+"! (from the demo model)"))
	default:
		return workflow.Response{}, &llm.Failure{Kind: llm.FailClient, Err: fmt.Errorf("the demo model has no answer for state %q of hello", in.State.Name)}
	}
	return workflow.Response{Text: reply, InputTokens: len(req.Input) / 4, OutputTokens: len(reply) / 4}, nil
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
