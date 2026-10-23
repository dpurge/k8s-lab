package llm_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"assistant/internal/a2ui"
	"assistant/internal/llm"
	"assistant/internal/llm/llmtest"
	"assistant/internal/workflow"
	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router/ollama"
)

// TestARealModelComposesAnInterface is a trial, not a test: it asks a real
// Ollama model to write an interface for the ui-compose workflow, through the
// real router and engine, in this process (no cluster, no database), and prints
// what happened. It is skipped unless UI_TRIAL_OLLAMA names an Ollama address
// (for example http://localhost:11434), because a call to a 12B model on a
// laptop takes minutes and starves everything else that runs there. UI_TRIAL_MODEL
// names the model (default gemma4:12b) and UI_TRIAL_RETRIES the retries the
// workflow may use (default 1, so at most two calls).
//
//	UI_TRIAL_OLLAMA=http://localhost:11434 go test ./internal/llm -run TestARealModelComposes -v -timeout 30m
func TestARealModelComposesAnInterface(t *testing.T) {
	address := os.Getenv("UI_TRIAL_OLLAMA")
	if address == "" {
		t.Skip("set UI_TRIAL_OLLAMA to an Ollama address to run the trial")
	}
	model := os.Getenv("UI_TRIAL_MODEL")
	if model == "" {
		model = "gemma4:12b"
	}
	retries := 1
	if v := os.Getenv("UI_TRIAL_RETRIES"); v != "" {
		fmt.Sscanf(v, "%d", &retries)
	}

	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(fmt.Sprintf(`providers:
  - {id: ollama, type: ollama, base_url: %q, api_key_from: ""}
models:
  - id: trial
    provider: ollama
    model: %s
    tags: [ui]
    priority: 10
    options: {num_ctx: 8192, num_predict: 2048, think: false, first_token_timeout_seconds: 600, idle_timeout_seconds: 120, overall_timeout_seconds: 1200}
`, address, model)))
	if err != nil {
		t.Fatal(err)
	}
	observer := &llmtest.Observer{}
	router, err := llm.NewRouter(cfg, map[string]llm.Provider{"ollama": ollama.New()}, llm.WithObserver(observer))
	if err != nil {
		t.Fatal(err)
	}
	wf, err := workflow.Load("../../workflows/ui-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	wf.Limits.MaxRetries = retries

	validator, err := a2ui.New()
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := workflow.DefaultPrompts()
	if err != nil {
		t.Fatal(err)
	}
	prompts.UIGuide = validator.Guide()
	prompts.UISchema = validator.InterfaceSchema()
	reg, _ := workflow.NewRegistry()
	recorder := &trialRecorder{t: t}
	eng := &workflow.Engine{LLM: router, Tools: reg, Prompts: prompts, UI: validator, Recorder: recorder}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	started := time.Now()
	run, res, err := eng.Start(ctx, wf, "trial-1", workflow.Input{Message: "Ask me for my name and which of three colours I like: red, green or blue."})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("finished in %s: status %v, %d model calls, %d steps", time.Since(started).Round(time.Second), res.Status, len(recorder.lines), run.Steps)
	if res.Failure != nil {
		t.Logf("failure: %s: %s", res.Failure.Reason, res.Failure.Message)
	}
	if res.Status == workflow.StatusWaiting {
		t.Logf("the model's question: %v", res.Ask["question"])
		list, _ := res.Ask["ui"].([]any)
		t.Logf("the interface has %d message(s)", len(list))
	}
	for _, line := range recorder.lines {
		t.Log(line)
	}
}

// trialRecorder prints each step as the engine records it.
type trialRecorder struct {
	t     *testing.T
	lines []string
}

func (r *trialRecorder) Record(_ context.Context, rec workflow.StepRecord) {
	line := fmt.Sprintf("call: step %d attempt %d outcome=%s tokens=%d+%d took=%s", rec.Step, rec.Attempt, rec.Outcome, rec.InputTokens, rec.OutputTokens, rec.Duration.Round(time.Second))
	if rec.Error != "" {
		line += "\n  refused because: " + rec.Error
	}
	line += "\n  reply: " + strings.ReplaceAll(rec.Reply, "\n", " ")
	r.lines = append(r.lines, line)
}
