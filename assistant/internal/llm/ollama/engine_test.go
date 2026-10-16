package ollama

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"assistant/internal/llm"
	"assistant/internal/llm/llmtest"
	"assistant/internal/models"
	"assistant/internal/workflow"
)

const routedFlow = `id: routed
version: 1
description: Two steps that need different models.
state_schema:
  type: object
  properties:
    note: {type: string}
  additionalProperties: false
start: first
model_tags: [chat]
limits: {max_steps: 5, max_tokens: 100000, max_retries: 1, max_visits: 3}
states:
  first:
    instructions: Think hard.
    model_tags: [strong]
    next: [last]
  last:
    instructions: Wrap up.
    next: [done]
  done:
    instructions: Finished.
    end: true
`

// A workflow runs through the router and two fake Ollama machines: the better
// model's machine is failing, so every step falls back to the other one, and
// each step asks for the tags its state names.
func TestAWorkflowRunsThroughTheRouterWithAFallbackInTheMiddle(t *testing.T) {
	gpu := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "overloaded", http.StatusInternalServerError)
	})
	replies := []string{
		`{"state_patch":{"note":"thought"},"action":{"type":"goto","state":"last"}}`,
		`{"state_patch":{},"action":{"type":"goto","state":"done"}}`,
	}
	next := 0
	laptop := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		stream(w, chunk(replies[next]), done(100+next, 10+next))
		next++
	})

	cfg, err := models.LoadBytes("models.yaml", []byte(fmt.Sprintf(`providers:
  - {id: gpu, type: ollama, base_url: %q}
  - {id: laptop, type: ollama, base_url: %q}
models:
  - {id: big, provider: gpu, model: big-model, tags: [chat, strong], priority: 30}
  - {id: mid, provider: laptop, model: mid-model, tags: [chat, strong], priority: 20}
  - {id: small, provider: laptop, model: small-model, tags: [chat], priority: 10}
  - {id: huge, provider: laptop, model: huge-model, tags: [chat], priority: 40}
`, gpu.URL, laptop.URL)))
	if err != nil {
		t.Fatal(err)
	}
	observer := &llmtest.Observer{}
	router, err := llm.NewRouter(cfg, map[string]llm.Provider{"ollama": New()}, llm.WithObserver(observer))
	if err != nil {
		t.Fatal(err)
	}

	wf, err := workflow.LoadBytes("routed.yaml", []byte(routedFlow))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := workflow.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := workflow.DefaultPrompts()
	if err != nil {
		t.Fatal(err)
	}
	eng := &workflow.Engine{LLM: router, Tools: reg, Prompts: prompts}
	run, res, err := eng.Start(context.Background(), wf, "run-1", workflow.Input{Message: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != workflow.StatusSucceeded {
		t.Fatalf("status %v, failure %+v", res.Status, run.Failure)
	}

	var trail []string
	for _, a := range observer.Attempts() {
		trail = append(trail, a.ModelID+":"+a.Outcome)
	}
	// Step one asks for strong, which huge lacks: big fails, mid answers.
	// Step two asks for chat, which huge has and ranks first: it answers.
	want := "big:error mid:ok huge:ok"
	if got := strings.Join(trail, " "); got != want {
		t.Errorf("attempts = %q, want %q", got, want)
	}
	if len(gpu.requests) != 1 || len(laptop.requests) != 2 {
		t.Errorf("requests: gpu %d, laptop %d", len(gpu.requests), len(laptop.requests))
	}
	for i, r := range laptop.requests {
		if want := []string{"mid-model", "huge-model"}[i]; r.Fields["model"] != want {
			t.Errorf("request %d asked the laptop for %v, want %s", i, r.Fields["model"], want)
		}
		if _, ok := r.Fields["format"].(map[string]any); !ok {
			t.Errorf("the reply schema must reach Ollama as format, got %T", r.Fields["format"])
		}
	}
	if run.Steps != 2 || res.Status != workflow.StatusSucceeded {
		t.Errorf("steps = %d", run.Steps)
	}
}

// A state asking for tags nothing has fails the run with a message that names
// them, instead of calling a model that cannot do the job.
func TestAStateThatNeedsTagsNoModelHasFailsTheRunClearly(t *testing.T) {
	laptop := newFake(t, func(w http.ResponseWriter, r *http.Request) { stream(w, chunk("{}"), done(1, 1)) })
	cfg, err := models.LoadBytes("models.yaml", []byte(fmt.Sprintf(`providers:
  - {id: laptop, type: ollama, base_url: %q}
models:
  - {id: small, provider: laptop, model: small-model, tags: [chat], priority: 10}
`, laptop.URL)))
	if err != nil {
		t.Fatal(err)
	}
	router, _ := llm.NewRouter(cfg, map[string]llm.Provider{"ollama": New()})
	wf, err := workflow.LoadBytes("routed.yaml", []byte(routedFlow))
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := workflow.NewRegistry()
	prompts, _ := workflow.DefaultPrompts()
	eng := &workflow.Engine{LLM: router, Tools: reg, Prompts: prompts}
	run, res, err := eng.Start(context.Background(), wf, "run-1", workflow.Input{Message: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != workflow.StatusFailed || run.Failure == nil || !strings.Contains(run.Failure.Message, "strong") {
		t.Fatalf("status %v, failure %+v", res.Status, run.Failure)
	}
	if len(laptop.requests) != 0 {
		t.Errorf("no model was fit to call, but the laptop got %d requests", len(laptop.requests))
	}
}
