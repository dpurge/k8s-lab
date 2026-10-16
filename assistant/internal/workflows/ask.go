package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"assistant/internal/tasks"
	"assistant/internal/workflow"
)

// askPayload is what an ask task carries.
type askPayload struct {
	Prompt string   `json:"prompt"`
	System string   `json:"system"`
	Tags   []string `json:"tags"`
}

// askResult is what an ask task returns.
type askResult struct {
	Text    string `json:"text"`
	ModelID string `json:"model_id"`
}

// ask makes one model call: the payload's prompt, with its optional system
// prompt, to a model that has all of its optional tags. It exists to exercise
// routing, fallback and call records before the engine is wired to the worker.
func (r *Registry) ask(ctx context.Context, t tasks.Task) (json.RawMessage, error) {
	var p askPayload
	dec := json.NewDecoder(bytes.NewReader(t.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("payload must be an object with prompt, and optionally system and tags: %w", err)
	}
	if strings.TrimSpace(p.Prompt) == "" {
		return nil, errors.New("payload.prompt is required and must not be blank")
	}
	resp, err := r.llm.Complete(ctx, workflow.Request{System: p.System, Input: p.Prompt, Tags: p.Tags})
	if err != nil {
		return nil, fmt.Errorf("model call failed: %w", err)
	}
	return json.Marshal(askResult{Text: resp.Text, ModelID: resp.ModelID})
}
