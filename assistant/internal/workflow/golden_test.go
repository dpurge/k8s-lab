package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"assistant/internal/workflow"
	"assistant/internal/workflow/workflowtest"
)

// recordGolden runs the script once through a RecordingLLM and saves the
// transcript, as a developer would to create a golden file.
func recordGolden(t *testing.T, script ...string) (path string, final string) {
	t.Helper()
	h := newHarness(t, flowYAML, replies(script...)...)
	recorder := &workflowtest.RecordingLLM{Inner: h.llm}
	h.eng.LLM = recorder
	run, res := h.start(workflow.Input{Message: "hello", Language: "xaa"})
	path = filepath.Join(t.TempDir(), "golden.json")
	if err := recorder.Transcript().Save(path); err != nil {
		t.Fatal(err)
	}
	return path, mustJSON(t, run) + string(res.Status)
}

func replayWith(t *testing.T, path string, mutate func(*harness)) (*workflowtest.ReplayLLM, string) {
	t.Helper()
	transcript, err := workflowtest.LoadTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, flowYAML)
	replay := workflowtest.NewReplayLLM(transcript)
	h.eng.LLM = replay
	if mutate != nil {
		mutate(h)
	}
	run, res := h.start(workflow.Input{Message: "hello", Language: "xaa"})
	return replay, mustJSON(t, run) + string(res.Status)
}

var goldenScript = []string{
	toolReply(`{"note":"x"}`, "verbs"),
	askReply(`{}`, `{"q":"Ready?"}`),
}

func TestGoldenReplayReproducesTheRun(t *testing.T) {
	path, recorded := recordGolden(t, goldenScript...)
	replay, replayed := replayWith(t, path, nil)
	if err := replay.Err(); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replayed != recorded {
		t.Errorf("replayed run differs:\n%s\n%s", recorded, replayed)
	}
}

func TestGoldenFileIsReadable(t *testing.T) {
	path, _ := recordGolden(t, goldenScript...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "\n  \"steps\": [") {
		t.Errorf("transcript is not indented for review:\n%s", text)
	}
	if !strings.Contains(text, `"input": {`) {
		t.Errorf("the step input must be stored as structured JSON, not an escaped string:\n%s", text)
	}
}

func TestGoldenReplayCatchesAChangedToolSchema(t *testing.T) {
	path, _ := recordGolden(t, goldenScript...)
	replay, _ := replayWith(t, path, func(h *harness) {
		h.search.Schema = map[string]any{"type": "object"}
	})
	err := replay.Err()
	if err == nil || !strings.Contains(err.Error(), "step input differs") || !strings.Contains(err.Error(), "call 1") {
		t.Fatalf("a changed tool schema changes the request and must be caught: %v", err)
	}
}

func TestGoldenReplayCatchesAChangedPrompt(t *testing.T) {
	path, _ := recordGolden(t, goldenScript...)
	replay, _ := replayWith(t, path, func(h *harness) {
		h.wf.Description = "A different description."
	})
	err := replay.Err()
	if err == nil || !strings.Contains(err.Error(), "differs from the transcript") {
		t.Fatalf("a changed workflow description changes the request and must be caught: %v", err)
	}
}

func TestGoldenReplayCatchesAChangedSystemPrompt(t *testing.T) {
	path, _ := recordGolden(t, goldenScript...)
	replay, _ := replayWith(t, path, func(h *harness) {
		prompts, err := workflow.LoadPrompts(fstestFS(map[string]string{"step.system.txt": "A different system prompt."}))
		if err != nil {
			t.Fatal(err)
		}
		h.eng.Prompts = prompts
	})
	err := replay.Err()
	if err == nil || !strings.Contains(err.Error(), "system prompt differs") {
		t.Fatalf("err = %v", err)
	}
}

func TestGoldenReplayCatchesAChangedStepOrder(t *testing.T) {
	path, _ := recordGolden(t, goldenScript...)
	replay, _ := replayWith(t, path, func(h *harness) {
		h.wf.States["start"] = workflow.State{
			Instructions: "Begin.", Tools: []string{"search"}, Next: []string{"done", "work"}, OnError: "recover",
		}
	})
	if err := replay.Err(); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("reordering next states changes the request: %v", err)
	}
}

func TestGoldenReplayCatchesFewerAndMoreCalls(t *testing.T) {
	path, _ := recordGolden(t, goldenScript...)
	transcript, err := workflowtest.LoadTranscript(path)
	if err != nil {
		t.Fatal(err)
	}

	short := workflowtest.NewReplayLLM(workflowtest.Transcript{Steps: transcript.Steps[:1]})
	h := newHarness(t, flowYAML)
	h.eng.LLM = short
	h.start(workflow.Input{Message: "hello", Language: "xaa"})
	if err := short.Err(); err == nil || !strings.Contains(err.Error(), "has only 1 steps") {
		t.Errorf("more calls than recorded: %v", err)
	}

	long := workflowtest.NewReplayLLM(workflowtest.Transcript{Steps: append(transcript.Steps, transcript.Steps[0])})
	h = newHarness(t, flowYAML)
	h.eng.LLM = long
	h.start(workflow.Input{Message: "hello", Language: "xaa"})
	if err := long.Err(); err == nil || !strings.Contains(err.Error(), "3 steps but only 2 were requested") {
		t.Errorf("fewer calls than recorded: %v", err)
	}
}

func TestReplayIgnoresInsignificantWhitespaceInTheInput(t *testing.T) {
	transcript := workflowtest.Transcript{Steps: []workflowtest.TranscriptStep{{
		System: "s", Input: []byte("{ \"a\" :  1 }"), Reply: "r", InputTokens: 1, OutputTokens: 1,
	}}}
	replay := workflowtest.NewReplayLLM(transcript)
	resp, err := replay.Complete(context.Background(), workflow.Request{System: "s", Input: `{"a":1}`})
	if err != nil || resp.Text != "r" {
		t.Fatalf("%v %+v", err, resp)
	}
	if err := replay.Err(); err != nil {
		t.Error(err)
	}
}

func TestTranscriptLoadErrors(t *testing.T) {
	if _, err := workflowtest.LoadTranscript(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Errorf("a missing file must be an error")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := workflowtest.LoadTranscript(bad); err == nil || !strings.Contains(err.Error(), "parse transcript") {
		t.Errorf("err = %v", err)
	}
}

func TestRecordingLLMDoesNotRecordFailedCalls(t *testing.T) {
	inner := workflowtest.NewScriptedLLM(workflowtest.Step{Err: context.DeadlineExceeded}, workflowtest.Reply("ok"))
	rec := &workflowtest.RecordingLLM{Inner: inner}
	_, _ = rec.Complete(context.Background(), workflow.Request{System: "s", Input: "{}"})
	_, _ = rec.Complete(context.Background(), workflow.Request{System: "s", Input: "{}"})
	if n := len(rec.Transcript().Steps); n != 1 {
		t.Errorf("recorded %d steps, want 1", n)
	}
}

func TestTraceOfARealRunMatchesTheRecords(t *testing.T) {
	h := newHarness(t, flowYAML, replies("bad", toolReply(`{"note":"x"}`, "q"), gotoReply(`{}`, "done"))...)
	h.start(workflow.Input{})
	out := workflow.FormatTrace(h.rec.Records())
	for _, want := range []string{"run run-1", "step 1 attempt 1 in start: retry", "step 1 attempt 2 in start: ok", "tool:   search ->", "action: goto done", "total: 3 model calls, 1 retries"} {
		if !strings.Contains(out, want) {
			t.Errorf("trace lacks %q:\n%s", want, out)
		}
	}
}
