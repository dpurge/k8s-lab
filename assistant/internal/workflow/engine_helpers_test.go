package workflow_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"assistant/internal/workflow"
	"assistant/internal/workflow/workflowtest"
)

const flowYAML = `id: flow
version: 1
description: A test flow.
state_schema:
  type: object
  properties:
    n: {type: integer}
    note: {type: string}
  required: [note]
  additionalProperties: false
start: start
limits: {max_steps: 10, max_tokens: 100000, max_retries: 1, max_visits: 3}
states:
  start:
    instructions: Begin.
    tools: [search]
    next: [work, done]
    on_error: recover
  work:
    instructions: Work.
    tools: [search]
    next: [start, done]
  recover:
    instructions: Recover.
    next: [done]
  done:
    instructions: Finished.
    end: true
`

var fixedStart = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

const clockStep = 10 * time.Millisecond

type harness struct {
	t      *testing.T
	wf     *workflow.Workflow
	llm    *workflowtest.ScriptedLLM
	rec    *workflowtest.MemoryRecorder
	search *workflowtest.Tool
	eng    *workflow.Engine
}

func searchSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"query"},
		"properties":           map[string]any{"query": map[string]any{"type": "string"}},
	}
}

func newHarness(t *testing.T, yaml string, steps ...workflowtest.Step) *harness {
	t.Helper()
	wf, err := workflow.LoadBytes("flow.yaml", []byte(yaml))
	if err != nil {
		t.Fatalf("load workflow: %v", err)
	}
	search := &workflowtest.Tool{ToolName: "search", Schema: searchSchema()}
	reg, err := workflow.NewRegistry(search)
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := workflow.DefaultPrompts()
	if err != nil {
		t.Fatal(err)
	}
	llm := workflowtest.NewScriptedLLM(steps...)
	rec := &workflowtest.MemoryRecorder{}
	return &harness{
		t: t, wf: wf, llm: llm, rec: rec, search: search,
		eng: &workflow.Engine{
			LLM: llm, Tools: reg, Prompts: prompts, Recorder: rec,
			Clock: workflowtest.NewFakeClock(fixedStart, clockStep),
		},
	}
}

func (h *harness) start(in workflow.Input) (*workflow.Run, workflow.Result) {
	h.t.Helper()
	run, res, err := h.eng.Start(context.Background(), h.wf, "run-1", in)
	if err != nil {
		h.t.Fatalf("Start: %v", err)
	}
	return run, res
}

func (h *harness) wantNoScriptProblems() {
	h.t.Helper()
	if err := h.llm.Err(); err != nil {
		h.t.Errorf("script: %v", err)
	}
}

func replyJSON(patch, action string) string {
	return fmt.Sprintf(`{"state_patch":%s,"action":%s}`, patch, action)
}

func toolReply(patch, query string) string {
	return replyJSON(patch, fmt.Sprintf(`{"type":"tool","tool":"search","args":{"query":%q}}`, query))
}

func queryReply(tool, patch, query string) string {
	return replyJSON(patch, fmt.Sprintf(`{"type":"tool","tool":%q,"args":{"query":%q}}`, tool, query))
}

func gotoReply(patch, state string) string {
	return replyJSON(patch, fmt.Sprintf(`{"type":"goto","state":%q}`, state))
}

func askReply(patch, payload string) string {
	return replyJSON(patch, fmt.Sprintf(`{"type":"ask_user","payload":%s}`, payload))
}

func replies(texts ...string) []workflowtest.Step {
	steps := make([]workflowtest.Step, len(texts))
	for i, t := range texts {
		steps[i] = workflowtest.Reply(t)
	}
	return steps
}

func stateJSON(t *testing.T, run *workflow.Run) string {
	t.Helper()
	return mustJSON(t, run.State)
}

func requestInput(t *testing.T, h *harness, i int) string {
	t.Helper()
	reqs := h.llm.Requests()
	if i >= len(reqs) {
		t.Fatalf("request %d does not exist, only %d were made", i, len(reqs))
	}
	return reqs[i].Input
}

func contains(t *testing.T, haystack, needle, what string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("%s: %q not found in:\n%s", what, needle, haystack)
	}
}

func notContains(t *testing.T, haystack, needle, what string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("%s: %q should not appear in:\n%s", what, needle, haystack)
	}
}

func outcomes(h *harness) []workflow.Outcome {
	var out []workflow.Outcome
	for _, r := range h.rec.Records() {
		out = append(out, r.Outcome)
	}
	return out
}

func wantOutcomes(t *testing.T, h *harness, want ...workflow.Outcome) {
	t.Helper()
	got := outcomes(h)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("outcomes = %v, want %v", got, want)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fstestFS(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
