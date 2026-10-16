package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type schemaTool struct {
	name   string
	schema map[string]any
}

func (s schemaTool) Name() string               { return s.name }
func (s schemaTool) ArgsSchema() map[string]any { return s.schema }
func (s schemaTool) Call(_ context.Context, _ map[string]any) (any, error) {
	return nil, nil
}

func ragTool() schemaTool {
	return schemaTool{"rag_search", map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"query"},
		"properties":           map[string]any{"query": map[string]any{"type": "string", "minLength": 1}},
	}}
}

const requestWorkflow = `id: learn
version: 1
description: Teach a topic.
state_schema: {type: object}
start: teach
limits: {max_steps: 9, max_tokens: 9000, max_retries: 2, max_visits: 5}
states:
  teach:
    instructions: Give the next lesson.
    output: lesson
    tools: [rag_search]
    next: [teach, done]
  quiet:
    instructions: Only ask.
  done:
    instructions: Wrap up.
    end: true
`

func requestFixtures(t *testing.T) (*Prompts, *Workflow, *Registry) {
	t.Helper()
	wf, err := LoadBytes("req.yaml", []byte(requestWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewRegistry(ragTool())
	if err != nil {
		t.Fatal(err)
	}
	return testPrompts(t), wf, reg
}

func decodeInput(t *testing.T, req Request) map[string]any {
	t.Helper()
	return obj(t, req.Input)
}

func TestBuildRequestContainsEveryPart(t *testing.T) {
	p, wf, reg := requestFixtures(t)
	run := NewRun(wf, "r1", obj(t, `{"goal":"Arabic"}`))
	run.Language = "xaa"

	req, err := BuildRequest(p, wf, run, reg, map[string]any{"hits": []any{"doc1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.System, "Rules for xaa") || !strings.Contains(req.System, "Output a lesson.") {
		t.Errorf("system prompt lacks snippets:\n%s", req.System)
	}
	in := decodeInput(t, req)
	wfInfo := in["workflow"].(map[string]any)
	if wfInfo["id"] != "learn" || wfInfo["description"] != "Teach a topic." {
		t.Errorf("workflow info = %v", wfInfo)
	}
	st := in["state"].(map[string]any)
	if st["name"] != "teach" || st["instructions"] != "Give the next lesson." {
		t.Errorf("state info = %v", st)
	}
	tools := st["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "rag_search" {
		t.Errorf("tools = %v", tools)
	}
	if tools[0].(map[string]any)["args_schema"].(map[string]any)["required"].([]any)[0] != "query" {
		t.Errorf("tool args schema missing: %v", tools[0])
	}
	if next := st["next"].([]any); len(next) != 2 || next[0] != "teach" || next[1] != "done" {
		t.Errorf("next = %v", next)
	}
	if rs := in["run_state"].(map[string]any); rs["goal"] != "Arabic" {
		t.Errorf("run_state = %v", rs)
	}
	if schema, ok := in["run_state_schema"].(map[string]any); !ok || schema["type"] != "object" {
		t.Errorf("the run state's schema must be in the input, got %v", in["run_state_schema"])
	}
	if obs := in["observation"].(map[string]any); obs["hits"].([]any)[0] != "doc1" {
		t.Errorf("observation = %v", obs)
	}
	if req.Schema == nil {
		t.Errorf("reply schema not attached")
	}
}

func TestBuildRequestHasNoHistory(t *testing.T) {
	p, wf, reg := requestFixtures(t)
	run := NewRun(wf, "r1", nil)

	first, err := BuildRequest(p, wf, run, reg, "FIRST-OBSERVATION", nil)
	if err != nil {
		t.Fatal(err)
	}
	run.State = obj(t, `{"progress":"second"}`)
	second, err := BuildRequest(p, wf, run, reg, "SECOND-OBSERVATION", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(second.Input, "FIRST-OBSERVATION") || strings.Contains(second.System, "FIRST-OBSERVATION") {
		t.Errorf("an earlier observation leaked into the next request")
	}
	allowed := map[string]bool{"workflow": true, "state": true, "run_state_schema": true, "run_state": true, "observation": true, "retry": true}
	for key := range decodeInput(t, second) {
		if !allowed[key] {
			t.Errorf("unexpected key %q in the step input: history must not appear", key)
		}
	}
	_ = first
}

func TestBuildRequestOmitsEmptyObservationAndRetry(t *testing.T) {
	p, wf, reg := requestFixtures(t)
	req, err := BuildRequest(p, wf, NewRun(wf, "r", nil), reg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	in := decodeInput(t, req)
	if _, has := in["observation"]; has {
		t.Errorf("a nil observation should be omitted")
	}
	if _, has := in["retry"]; has {
		t.Errorf("a first attempt has no retry note")
	}
	if rs, ok := in["run_state"].(map[string]any); !ok || len(rs) != 0 {
		t.Errorf("run_state should be an empty object, got %v", in["run_state"])
	}
}

func TestBuildRequestEmptyListsAreArrays(t *testing.T) {
	p, wf, reg := requestFixtures(t)
	run := NewRun(wf, "r", nil)
	run.StateName = "quiet"
	req, err := BuildRequest(p, wf, run, reg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := decodeInput(t, req)["state"].(map[string]any)
	if tools, ok := st["tools"].([]any); !ok || len(tools) != 0 {
		t.Errorf("tools should be [], got %v", st["tools"])
	}
	if next, ok := st["next"].([]any); !ok || len(next) != 0 {
		t.Errorf("next should be [], got %v", st["next"])
	}
}

func TestBuildRequestRetryNote(t *testing.T) {
	p, wf, reg := requestFixtures(t)
	run := NewRun(wf, "r", nil)

	req, err := BuildRequest(p, wf, run, reg, nil, &Retry{Reply: "not json", Error: "reply is not valid JSON"})
	if err != nil {
		t.Fatal(err)
	}
	retry := decodeInput(t, req)["retry"].(map[string]any)
	if retry["reply"] != "not json" || retry["error"] != "reply is not valid JSON" {
		t.Errorf("retry = %v", retry)
	}

	long := strings.Repeat("x", maxRetryReplyBytes*2)
	req, err = BuildRequest(p, wf, run, reg, nil, &Retry{Reply: long, Error: "e"})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeInput(t, req)["retry"].(map[string]any)["reply"].(string)
	if !strings.Contains(got, "truncated") || len(got) > maxRetryReplyBytes+20 {
		t.Errorf("a long refused reply should be truncated, got %d bytes", len(got))
	}
}

func TestBuildRequestLanguageSources(t *testing.T) {
	p, wf, reg := requestFixtures(t)
	run := NewRun(wf, "r", nil)

	req, _ := BuildRequest(p, wf, run, reg, nil, nil)
	if strings.Contains(req.System, "Rules for") {
		t.Errorf("no language set, no language rules expected")
	}
	run.Language = "xaa"
	req, _ = BuildRequest(p, wf, run, reg, nil, nil)
	if !strings.Contains(req.System, "Rules for xaa") {
		t.Errorf("run language not used")
	}
	run.State = obj(t, `{"language":"xbb"}`)
	req, _ = BuildRequest(p, wf, run, reg, nil, nil)
	if !strings.Contains(req.System, "Rules for B.") || strings.Contains(req.System, "write A.") {
		t.Errorf("a language recorded in the run state should win:\n%s", req.System)
	}
	run.State = obj(t, `{"language":42}`)
	req, _ = BuildRequest(p, wf, run, reg, nil, nil)
	if !strings.Contains(req.System, "Rules for xaa") {
		t.Errorf("a non-string state language must be ignored")
	}
}

func TestBuildRequestOutputKindFollowsTheState(t *testing.T) {
	p, wf, reg := requestFixtures(t)
	run := NewRun(wf, "r", nil)

	req, _ := BuildRequest(p, wf, run, reg, nil, nil)
	if !strings.Contains(req.System, "Output a lesson.") {
		t.Errorf("teach state declares output lesson:\n%s", req.System)
	}
	run.StateName = "quiet"
	req, _ = BuildRequest(p, wf, run, reg, nil, nil)
	if strings.Contains(req.System, "Output a") {
		t.Errorf("quiet state declares no output kind:\n%s", req.System)
	}
	wf.States["quiet"] = State{Instructions: "x", Output: "quiz"}
	req, _ = BuildRequest(p, wf, run, reg, nil, nil)
	if !strings.Contains(req.System, "Output a quiz.") || strings.Contains(req.System, "lesson") {
		t.Errorf("moving to another output kind must change the rules:\n%s", req.System)
	}
}

func TestBuildRequestErrors(t *testing.T) {
	p, wf, reg := requestFixtures(t)
	run := NewRun(wf, "r", nil)

	run.StateName = "ghost"
	if _, err := BuildRequest(p, wf, run, reg, nil, nil); err == nil || !strings.Contains(err.Error(), `unknown state "ghost"`) {
		t.Errorf("unknown state: %v", err)
	}
	run.StateName = "teach"
	empty, _ := NewRegistry()
	if _, err := BuildRequest(p, wf, run, empty, nil, nil); err == nil || !strings.Contains(err.Error(), `tool "rag_search" which is not registered`) {
		t.Errorf("unregistered tool: %v", err)
	}
}

func compileReplySchema(t *testing.T, schema map[string]any) *jsonschema.Schema {
	t.Helper()
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("reply.json", doc); err != nil {
		t.Fatal(err)
	}
	compiled, err := c.Compile("reply.json")
	if err != nil {
		t.Fatalf("reply schema is not a valid JSON Schema: %v", err)
	}
	return compiled
}

func TestReplySchemaAcceptsAndRejects(t *testing.T) {
	_, wf, reg := requestFixtures(t)
	schema, err := ReplySchema(wf, "teach", reg)
	if err != nil {
		t.Fatal(err)
	}
	compiled := compileReplySchema(t, schema)

	good := []string{
		`{"state_patch":{},"action":{"type":"tool","tool":"rag_search","args":{"query":"verbs"}}}`,
		`{"state_patch":{"a":1},"action":{"type":"goto","state":"done"}}`,
		`{"state_patch":{},"action":{"type":"goto","state":"teach"}}`,
		`{"state_patch":{"a":null},"action":{"type":"ask_user","payload":{"text":"hi"}}}`,
	}
	bad := map[string]string{
		"not an object":          `[]`,
		"no patch":               `{"action":{"type":"goto","state":"done"}}`,
		"no action":              `{"state_patch":{}}`,
		"extra key":              `{"state_patch":{},"action":{"type":"goto","state":"done"},"why":"x"}`,
		"patch not an object":    `{"state_patch":[],"action":{"type":"goto","state":"done"}}`,
		"unknown action type":    `{"state_patch":{},"action":{"type":"dance"}}`,
		"tool not allowed":       `{"state_patch":{},"action":{"type":"tool","tool":"web_search","args":{}}}`,
		"tool args wrong":        `{"state_patch":{},"action":{"type":"tool","tool":"rag_search","args":{"query":5}}}`,
		"tool args missing":      `{"state_patch":{},"action":{"type":"tool","tool":"rag_search","args":{}}}`,
		"goto not allowed":       `{"state_patch":{},"action":{"type":"goto","state":"quiet"}}`,
		"goto without state":     `{"state_patch":{},"action":{"type":"goto"}}`,
		"ask_user no payload":    `{"state_patch":{},"action":{"type":"ask_user"}}`,
		"ask_user payload array": `{"state_patch":{},"action":{"type":"ask_user","payload":[]}}`,
	}
	for _, in := range good {
		if err := compiled.Validate(obj(t, in)); err != nil {
			t.Errorf("should accept %s: %v", in, err)
		}
	}
	for name, in := range bad {
		var v any
		if d, derr := jsonschema.UnmarshalJSON(strings.NewReader(in)); derr == nil {
			v = d
		}
		if err := compiled.Validate(v); err == nil {
			t.Errorf("%s: should reject %s", name, in)
		}
	}
}

func TestReplySchemaFollowsTheState(t *testing.T) {
	_, wf, reg := requestFixtures(t)
	schema, err := ReplySchema(wf, "quiet", reg)
	if err != nil {
		t.Fatal(err)
	}
	compiled := compileReplySchema(t, schema)
	if err := compiled.Validate(obj(t, `{"state_patch":{},"action":{"type":"ask_user","payload":{}}}`)); err != nil {
		t.Errorf("a state with no tools or next can still ask: %v", err)
	}
	for _, in := range []string{
		`{"state_patch":{},"action":{"type":"goto","state":"done"}}`,
		`{"state_patch":{},"action":{"type":"tool","tool":"rag_search","args":{"query":"x"}}}`,
	} {
		if err := compiled.Validate(obj(t, in)); err == nil {
			t.Errorf("quiet state should reject %s", in)
		}
	}
}

func TestReplySchemaErrors(t *testing.T) {
	_, wf, reg := requestFixtures(t)
	if _, err := ReplySchema(wf, "ghost", reg); err == nil || !strings.Contains(err.Error(), "unknown state") {
		t.Errorf("unknown state: %v", err)
	}
	if _, err := ReplySchema(wf, "done", reg); err == nil || !strings.Contains(err.Error(), "end state") {
		t.Errorf("end state: %v", err)
	}
	empty, _ := NewRegistry()
	if _, err := ReplySchema(wf, "teach", empty); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Errorf("unregistered tool: %v", err)
	}
}

func TestBuildRequestShowsTheRunStateSchemaWithKeysNotYetInTheState(t *testing.T) {
	p, wf, reg := requestFixtures(t)
	wf.StateSchema = obj(t, `{"type":"object","properties":{"pending":{"type":"object","properties":{"question":{"type":"string"},"expected":{"type":"string"}}}}}`)
	req, err := BuildRequest(p, wf, NewRun(wf, "r", nil), reg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	in := decodeInput(t, req)
	if rs := in["run_state"].(map[string]any); len(rs) != 0 {
		t.Fatalf("run_state should still be empty: %v", rs)
	}
	pending := in["run_state_schema"].(map[string]any)["properties"].(map[string]any)["pending"].(map[string]any)
	if _, ok := pending["properties"].(map[string]any)["expected"]; !ok {
		t.Errorf("the key names of a key that is not in the run state yet must be visible: %v", pending)
	}
}

func TestBuildRequestWithoutAStateSchemaHasAnEmptyOne(t *testing.T) {
	p, wf, reg := requestFixtures(t)
	wf.StateSchema = nil
	req, err := BuildRequest(p, wf, NewRun(wf, "r", nil), reg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if schema, ok := decodeInput(t, req)["run_state_schema"].(map[string]any); !ok || len(schema) != 0 {
		t.Errorf("run_state_schema = %v, want {}", decodeInput(t, req)["run_state_schema"])
	}
}

func TestBuildRequestCarriesTheStatesModelTags(t *testing.T) {
	p, _, reg := requestFixtures(t)
	wf, err := LoadBytes("tags.yaml", []byte(strings.Replace(requestWorkflow, "start: teach\n", "start: teach\nmodel_tags: [chat]\n", 1)+""))
	if err != nil {
		t.Fatal(err)
	}
	st := wf.States["teach"]
	st.ModelTags = []string{"strong", "json"}
	wf.States["teach"] = st
	st = wf.States["quiet"]
	st.ModelTags = []string{}
	wf.States["quiet"] = st

	build := func(state string) []string {
		req, err := BuildRequest(p, wf, &Run{StateName: state}, reg, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return req.Tags
	}
	if got := strings.Join(build("teach"), ","); got != "strong,json" {
		t.Errorf("teach = %q", got)
	}
	if got := build("quiet"); len(got) != 0 {
		t.Errorf("an empty state list means any model, got %v", got)
	}
	st = wf.States["quiet"]
	st.ModelTags = nil
	wf.States["quiet"] = st
	if got := strings.Join(build("quiet"), ","); got != "chat" {
		t.Errorf("a state without a list inherits the workflow's tags, got %q", got)
	}
}
