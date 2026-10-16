package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validHeader = `id: demo
version: 1
description: A demo.
state_schema: {type: object}
start: a
limits: {max_steps: 5, max_tokens: 1000, max_retries: 1, max_visits: 3}
`

func mustLoadError(t *testing.T, yaml string) *LoadError {
	t.Helper()
	_, err := LoadBytes("test.yaml", []byte(yaml))
	if err == nil {
		t.Fatalf("expected a load error, got nil")
	}
	var lerr *LoadError
	if !errors.As(err, &lerr) {
		t.Fatalf("expected *LoadError, got %T: %v", err, err)
	}
	return lerr
}

func findFinding(t *testing.T, lerr *LoadError, path string) Finding {
	t.Helper()
	for _, f := range lerr.Findings {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no finding for path %q in:\n%s", path, lerr.Error())
	return Finding{}
}

func TestLoadValidFile(t *testing.T) {
	wf, err := Load(filepath.Join("testdata", "valid.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if wf.ID != "learn-topic" || wf.Version != 1 || wf.Start != "intake" {
		t.Errorf("unexpected header: %+v", wf)
	}
	if wf.Limits != (Limits{MaxSteps: 40, MaxTokens: 60000, MaxRetries: 2, MaxVisits: 10}) {
		t.Errorf("unexpected limits: %+v", wf.Limits)
	}
	teach := wf.States["teach"]
	if teach.Output != "lesson" || teach.OnError != "done" {
		t.Errorf("teach state not decoded: %+v", teach)
	}
	if got := strings.Join(teach.Next, ","); got != "teach,done" {
		t.Errorf("teach.next = %q", got)
	}
	if !wf.States["done"].End {
		t.Errorf("done.end should be true")
	}
	if wf.StateSchema["type"] != "object" {
		t.Errorf("state_schema not decoded: %v", wf.StateSchema)
	}
}

func TestLoadOptionalFieldsDefaultToZero(t *testing.T) {
	wf, err := LoadBytes("t", []byte(validHeader+"states:\n  a:\n    instructions: Do it.\n"))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	a := wf.States["a"]
	if a.Tools != nil || a.Next != nil || a.End || a.OnError != "" || a.Output != "" {
		t.Errorf("optional fields should be zero: %+v", a)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join("testdata", "nope.yaml"))
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected a not-exist error, got %v", err)
	}
}

func TestLoadEmptyFile(t *testing.T) {
	for _, in := range []string{"", "   \n", "# only a comment\n"} {
		lerr := mustLoadError(t, in)
		if !strings.Contains(lerr.Findings[0].Message, "empty") {
			t.Errorf("input %q: message = %q", in, lerr.Findings[0].Message)
		}
	}
}

func TestLoadYAMLSyntaxErrorHasLine(t *testing.T) {
	lerr := mustLoadError(t, "id: demo\n  bad: indent: here\n")
	f := lerr.Findings[0]
	if f.Path != "(file)" || f.Line != 2 {
		t.Errorf("expected a (file) finding at line 2, got %+v", f)
	}
}

func TestLoadNonStringKeyIsReported(t *testing.T) {
	lerr := mustLoadError(t, "1: one\n2: two\n")
	if len(lerr.Findings) == 0 {
		t.Fatal("expected findings for a file without the required keys")
	}
}

func TestLoadMissingRequiredKeys(t *testing.T) {
	lerr := mustLoadError(t, "id: demo\n")
	msgs := lerr.Error()
	for _, key := range []string{"version", "description", "state_schema", "start", "limits", "states"} {
		if !strings.Contains(msgs, `missing required key "`+key+`"`) {
			t.Errorf("missing finding for %s in:\n%s", key, msgs)
		}
	}
}

func TestLoadUnknownKeyReportsKeyAndLine(t *testing.T) {
	yaml := validHeader + "states:\n  a:\n    instructions: Do it.\n    colour: red\n"
	lerr := mustLoadError(t, yaml)
	f := findFinding(t, lerr, "states.a.colour")
	if f.Line != 10 || !strings.Contains(f.Message, `unknown key "colour"`) {
		t.Errorf("finding = %+v", f)
	}
	if !strings.HasPrefix(lerr.Error(), "test.yaml:10: states.a.colour:") {
		t.Errorf("Error() = %q", lerr.Error())
	}
}

func TestLoadUnknownTopLevelKey(t *testing.T) {
	lerr := mustLoadError(t, validHeader+"states:\n  a: {instructions: x}\nextra: 1\n")
	f := findFinding(t, lerr, "extra")
	if f.Line != 9 {
		t.Errorf("line = %d, want 9", f.Line)
	}
}

func TestLoadWrongTypes(t *testing.T) {
	tests := []struct {
		name, yaml, path string
		line             int
	}{
		{"version is text", strings.Replace(validHeader, "version: 1", "version: one", 1) + "states:\n  a: {instructions: x}\n", "version", 2},
		{"version below one", strings.Replace(validHeader, "version: 1", "version: 0", 1) + "states:\n  a: {instructions: x}\n", "version", 2},
		{"limit is text", strings.Replace(validHeader, "max_steps: 5", "max_steps: many", 1) + "states:\n  a: {instructions: x}\n", "limits.max_steps", 6},
		{"limit is zero", strings.Replace(validHeader, "max_visits: 3", "max_visits: 0", 1) + "states:\n  a: {instructions: x}\n", "limits.max_visits", 6},
		{"end is text", validHeader + "states:\n  a:\n    instructions: x\n    end: sometimes\n", "states.a.end", 10},
		{"tools not a list", validHeader + "states:\n  a:\n    instructions: x\n    tools: rag\n", "states.a.tools", 10},
		{"duplicate next", validHeader + "states:\n  a:\n    instructions: x\n    next: [a, a]\n", "states.a.next", 10},
		{"state_schema not an object", strings.Replace(validHeader, "state_schema: {type: object}", "state_schema: nope", 1) + "states:\n  a: {instructions: x}\n", "state_schema", 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lerr := mustLoadError(t, tc.yaml)
			f := findFinding(t, lerr, tc.path)
			if f.Line != tc.line {
				t.Errorf("line = %d, want %d (%s)", f.Line, tc.line, lerr.Error())
			}
		})
	}
}

func TestLoadBadNames(t *testing.T) {
	t.Run("workflow id", func(t *testing.T) {
		lerr := mustLoadError(t, strings.Replace(validHeader, "id: demo", "id: Demo Flow", 1)+"states:\n  a: {instructions: x}\n")
		findFinding(t, lerr, "id")
	})
	t.Run("state name", func(t *testing.T) {
		lerr := mustLoadError(t, validHeader+"states:\n  Bad-Name: {instructions: x}\n")
		if len(lerr.Findings) == 0 {
			t.Fatal("expected a finding for the state name")
		}
	})
	t.Run("output kind", func(t *testing.T) {
		lerr := mustLoadError(t, validHeader+"states:\n  a:\n    instructions: x\n    output: Lesson Plan\n")
		findFinding(t, lerr, "states.a.output")
	})
}

func TestLoadStatesMustNotBeEmpty(t *testing.T) {
	lerr := mustLoadError(t, validHeader+"states: {}\n")
	findFinding(t, lerr, "states")
}

func TestLoadStateNeedsInstructions(t *testing.T) {
	lerr := mustLoadError(t, validHeader+"states:\n  a:\n    tools: [x]\n")
	f := findFinding(t, lerr, "states.a")
	if !strings.Contains(f.Message, `missing required key "instructions"`) {
		t.Errorf("finding = %+v", f)
	}
}

func TestLoadReportsAllProblemsInLineOrder(t *testing.T) {
	yaml := strings.Replace(validHeader, "version: 1", "version: zero", 1) +
		"states:\n  a:\n    instructions: x\n    bogus: 1\n  b:\n    tools: []\n"
	lerr := mustLoadError(t, yaml)
	if len(lerr.Findings) < 3 {
		t.Fatalf("expected at least 3 findings, got:\n%s", lerr.Error())
	}
	for i := 1; i < len(lerr.Findings); i++ {
		if lerr.Findings[i].Line < lerr.Findings[i-1].Line {
			t.Errorf("findings not in line order:\n%s", lerr.Error())
		}
	}
}

func TestLoadModelTags(t *testing.T) {
	wf, err := LoadBytes("t.yaml", []byte(validHeader+`model_tags: [chat, json]
states:
  a:
    instructions: Go.
    model_tags: [strong]
    next: [b]
  b:
    instructions: Done.
    model_tags: []
    end: true
  c:
    instructions: Inherit.
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(wf.ModelTags, ","); got != "chat,json" {
		t.Errorf("workflow tags = %q", got)
	}
	if got := strings.Join(wf.States["a"].ModelTags, ","); got != "strong" {
		t.Errorf("a tags = %q", got)
	}
	if b := wf.States["b"].ModelTags; b == nil || len(b) != 0 {
		t.Errorf("an empty list must stay non-nil so it overrides: %#v", b)
	}
	if c := wf.States["c"].ModelTags; c != nil {
		t.Errorf("an absent list must stay nil so it inherits: %#v", c)
	}
}

func TestLoadModelTagsRejectsBadShapes(t *testing.T) {
	for name, body := range map[string]string{
		"not a list":  "model_tags: chat\nstates:\n  a: {instructions: x, end: true}\n",
		"empty tag":   "model_tags: [\"\"]\nstates:\n  a: {instructions: x, end: true}\n",
		"repeat":      "model_tags: [a, a]\nstates:\n  a: {instructions: x, end: true}\n",
		"state tags":  "states:\n  a: {instructions: x, end: true, model_tags: [1]}\n",
		"state empty": "states:\n  a: {instructions: x, end: true, model_tags: [\"\"]}\n",
	} {
		if _, err := LoadBytes("t.yaml", []byte(validHeader+body)); err == nil {
			t.Errorf("%s: want a load error", name)
		}
	}
}

func TestTagsForStateReplacesWorkflowTags(t *testing.T) {
	wf := &Workflow{ModelTags: []string{"chat"}, States: map[string]State{
		"inherit":  {},
		"replace":  {ModelTags: []string{"strong"}},
		"anything": {ModelTags: []string{}},
	}}
	cases := map[string]string{"inherit": "chat", "replace": "strong", "anything": "", "unknown": "chat"}
	for state, want := range cases {
		if got := strings.Join(wf.TagsFor(state), ","); got != want {
			t.Errorf("TagsFor(%s) = %q, want %q", state, got, want)
		}
	}
}
