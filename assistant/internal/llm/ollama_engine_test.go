package llm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"assistant/internal/llm"
	"assistant/internal/llm/llmtest"
	"assistant/internal/workflow"
	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router/ollama"
)

// fakeOllama is a minimal Ollama server that records each request's JSON body.
type fakeOllama struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
}

type recordedRequest struct {
	Fields map[string]any
}

func newFakeOllama(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *fakeOllama {
	t.Helper()
	f := &fakeOllama{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var fields map[string]any
		_ = json.Unmarshal(body, &fields)
		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{Fields: fields})
		f.mu.Unlock()
		handle(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func ollamaStream(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, _ := w.(http.Flusher)
	for _, c := range chunks {
		fmt.Fprintln(w, c)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func ollamaChunk(content string) string {
	b, _ := json.Marshal(map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "done": false})
	return string(b)
}

func ollamaDone(promptTokens, replyTokens int) string {
	b, _ := json.Marshal(map[string]any{"message": map[string]any{"role": "assistant", "content": ""}, "done": true, "prompt_eval_count": promptTokens, "eval_count": replyTokens})
	return string(b)
}

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
	gpu := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "overloaded", http.StatusInternalServerError)
	})
	replies := []string{
		`{"state_patch":{"note":"thought"},"action":{"type":"goto","state":"last"}}`,
		`{"state_patch":{},"action":{"type":"goto","state":"done"}}`,
	}
	next := 0
	laptop := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request) {
		ollamaStream(w, ollamaChunk(replies[next]), ollamaDone(100+next, 10+next))
		next++
	})

	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(fmt.Sprintf(`providers:
  - {id: gpu, type: ollama, base_url: %q, api_key_from: ""}
  - {id: laptop, type: ollama, base_url: %q, api_key_from: ""}
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
	router, err := llm.NewRouter(cfg, map[string]llm.Provider{"ollama": ollama.New()}, llm.WithObserver(observer))
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
	laptop := newFakeOllama(t, func(w http.ResponseWriter, r *http.Request) { ollamaStream(w, ollamaChunk("{}"), ollamaDone(1, 1)) })
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(fmt.Sprintf(`providers:
  - {id: laptop, type: ollama, base_url: %q, api_key_from: ""}
models:
  - {id: small, provider: laptop, model: small-model, tags: [chat], priority: 10}
`, laptop.URL)))
	if err != nil {
		t.Fatal(err)
	}
	router, _ := llm.NewRouter(cfg, map[string]llm.Provider{"ollama": ollama.New()})
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
