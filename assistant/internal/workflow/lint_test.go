package workflow

import (
	"strings"
	"testing"
)

func load(t *testing.T, body string) *Workflow {
	t.Helper()
	wf, err := LoadBytes("lint.yaml", []byte(validHeader+body))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	return wf
}

func messages(findings []Finding) string {
	parts := make([]string, len(findings))
	for i, f := range findings {
		parts[i] = f.Path + ": " + f.Message
	}
	return strings.Join(parts, "\n")
}

func wantFinding(t *testing.T, findings []Finding, path, contains string) Finding {
	t.Helper()
	for _, f := range findings {
		if f.Path == path && strings.Contains(f.Message, contains) {
			return f
		}
	}
	t.Fatalf("no finding at %q containing %q; findings:\n%s", path, contains, messages(findings))
	return Finding{}
}

func TestLintValidWorkflowHasNoFindings(t *testing.T) {
	wf, err := Load("testdata/valid.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tools := NameSet{"memory_search": true, "rag_search": true, "web_search": true}
	if findings := Lint(wf, tools); len(findings) != 0 {
		t.Fatalf("unexpected findings:\n%s", messages(findings))
	}
	if findings := Lint(wf, nil); len(findings) != 0 {
		t.Fatalf("unexpected findings with tool check skipped:\n%s", messages(findings))
	}
}

func TestLintUnknownStart(t *testing.T) {
	wf := load(t, "states:\n  b:\n    instructions: x\n    end: true\n")
	f := wantFinding(t, Lint(wf, nil), "start", `"a" does not exist`)
	if f.Line != 5 {
		t.Errorf("line = %d, want 5", f.Line)
	}
}

func TestLintUnknownNextTarget(t *testing.T) {
	wf := load(t, "states:\n  a:\n    instructions: x\n    next: [b, ghost]\n  b:\n    instructions: y\n    end: true\n")
	f := wantFinding(t, Lint(wf, nil), "states.a.next", `"ghost"`)
	if f.Line != 10 {
		t.Errorf("line = %d, want 10", f.Line)
	}
}

func TestLintUnknownOnErrorTarget(t *testing.T) {
	wf := load(t, "states:\n  a:\n    instructions: x\n    next: [b]\n    on_error: ghost\n  b:\n    instructions: y\n    end: true\n")
	wantFinding(t, Lint(wf, nil), "states.a.on_error", `"ghost"`)
}

func TestLintEndStateWithNext(t *testing.T) {
	wf := load(t, "states:\n  a:\n    instructions: x\n    end: true\n    next: [a]\n")
	wantFinding(t, Lint(wf, nil), "states.a.next", "must not have next states")
}

func TestLintNoEndState(t *testing.T) {
	wf := load(t, "states:\n  a:\n    instructions: x\n    next: [a]\n")
	wantFinding(t, Lint(wf, nil), "states", "no state has end: true")
}

func TestLintStateThatCannotReachEnd(t *testing.T) {
	wf := load(t, "states:\n  a:\n    instructions: x\n    next: [stuck, done]\n  stuck:\n    instructions: y\n  done:\n    instructions: z\n    end: true\n")
	findings := Lint(wf, nil)
	wantFinding(t, findings, "states.stuck", "cannot reach an end state")
	for _, f := range findings {
		if f.Path == "states.a" || f.Path == "states.done" {
			t.Errorf("unexpected finding for a healthy state: %+v", f)
		}
	}
}

func TestLintLoopWithoutExit(t *testing.T) {
	wf := load(t, "states:\n  a:\n    instructions: x\n    next: [b, done]\n  b:\n    instructions: y\n    next: [c]\n  c:\n    instructions: y\n    next: [b]\n  done:\n    instructions: z\n    end: true\n")
	findings := Lint(wf, nil)
	wantFinding(t, findings, "states.b", "cannot reach an end state")
	wantFinding(t, findings, "states.c", "cannot reach an end state")
}

func TestLintUnreachableState(t *testing.T) {
	wf := load(t, "states:\n  a:\n    instructions: x\n    next: [done]\n  orphan:\n    instructions: y\n    next: [done]\n  done:\n    instructions: z\n    end: true\n")
	findings := Lint(wf, nil)
	wantFinding(t, findings, "states.orphan", "cannot be reached")
	for _, f := range findings {
		if f.Path == "states.orphan" && strings.Contains(f.Message, "end state") {
			t.Errorf("an unreachable state should be reported once, got: %s", messages(findings))
		}
	}
}

func TestLintOnErrorEdgeCountsForReachability(t *testing.T) {
	wf := load(t, "states:\n  a:\n    instructions: x\n    next: [done]\n    on_error: recover\n  recover:\n    instructions: y\n    next: [done]\n  done:\n    instructions: z\n    end: true\n")
	if findings := Lint(wf, nil); len(findings) != 0 {
		t.Fatalf("on_error target should count as reachable:\n%s", messages(findings))
	}
}

func TestLintUnknownTool(t *testing.T) {
	wf := load(t, "states:\n  a:\n    instructions: x\n    tools: [rag_search, nope]\n    next: [done]\n  done:\n    instructions: z\n    end: true\n")
	findings := Lint(wf, NameSet{"rag_search": true})
	wantFinding(t, findings, "states.a.tools", `"nope"`)
	if len(findings) != 1 {
		t.Errorf("expected exactly one finding, got:\n%s", messages(findings))
	}
	if findings := Lint(wf, nil); len(findings) != 0 {
		t.Errorf("tool check should be skipped with a nil ToolSet, got:\n%s", messages(findings))
	}
}

func TestLintToolsFromRegistry(t *testing.T) {
	reg, err := NewRegistry(fakeTool{name: "rag_search"})
	if err != nil {
		t.Fatal(err)
	}
	wf := load(t, "states:\n  a:\n    instructions: x\n    tools: [rag_search]\n    next: [done]\n  done:\n    instructions: z\n    end: true\n")
	if findings := Lint(wf, reg); len(findings) != 0 {
		t.Fatalf("unexpected findings:\n%s", messages(findings))
	}
}

func TestLintInvalidStateSchema(t *testing.T) {
	body := "states:\n  a:\n    instructions: x\n    end: true\n"
	wf, err := LoadBytes("lint.yaml", []byte(strings.Replace(validHeader, "state_schema: {type: object}", "state_schema: {type: banana}", 1)+body))
	if err != nil {
		t.Fatal(err)
	}
	f := wantFinding(t, Lint(wf, nil), "state_schema", "not a valid JSON Schema")
	if f.Line != 4 {
		t.Errorf("line = %d, want 4", f.Line)
	}
}

func TestLintLimitsOfAWorkflowBuiltInCode(t *testing.T) {
	wf := &Workflow{
		ID: "x", Version: 1, Description: "d", StateSchema: map[string]any{"type": "object"},
		Start:  "a",
		States: map[string]State{"a": {Instructions: "x", End: true}},
	}
	findings := Lint(wf, nil)
	wantFinding(t, findings, "limits.max_steps", "at least 1")
	wantFinding(t, findings, "limits.max_tokens", "at least 1")
	wantFinding(t, findings, "limits.max_visits", "at least 1")
	for _, f := range findings {
		if f.Path == "limits.max_retries" {
			t.Errorf("max_retries of 0 is allowed, got %+v", f)
		}
		if f.Line != 0 {
			t.Errorf("a workflow built in code has no lines, got %+v", f)
		}
	}
}

func TestLintMissingStateSchemaInCode(t *testing.T) {
	wf := &Workflow{Start: "a", States: map[string]State{"a": {End: true}}, Limits: Limits{1, 1, 0, 1}}
	wantFinding(t, Lint(wf, nil), "state_schema", "missing")
}

func TestLintFindingsAreOrderedByLine(t *testing.T) {
	wf := load(t, "states:\n  a:\n    instructions: x\n    next: [ghost]\n    on_error: ghost2\n  b:\n    instructions: y\n")
	findings := Lint(wf, nil)
	if len(findings) < 3 {
		t.Fatalf("expected several findings:\n%s", messages(findings))
	}
	for i := 1; i < len(findings); i++ {
		if findings[i].Line < findings[i-1].Line {
			t.Errorf("not ordered by line:\n%s", messages(findings))
		}
	}
}

func TestLintModelTags(t *testing.T) {
	wf := load(t, `model_tags: [chat, " chat"]
states:
  a:
    instructions: Go.
    model_tags: ["  ", json, json2, " json2"]
    next: [b]
  b:
    instructions: Done.
    end: true
`)
	findings := Lint(wf, nil)
	wantFinding(t, findings, "model_tags", `"chat" is listed twice`)
	wantFinding(t, findings, "states.a.model_tags", "is blank")
	wantFinding(t, findings, "states.a.model_tags", `"json2" is listed twice`)
	if len(findings) != 3 {
		t.Errorf("want exactly 3 findings:\n%s", messages(findings))
	}
}

func TestLintGoodModelTagsAreQuiet(t *testing.T) {
	wf := load(t, `model_tags: [chat]
states:
  a:
    instructions: Go.
    model_tags: [json, strong]
    next: [b]
  b:
    instructions: Done.
    model_tags: []
    end: true
`)
	if findings := Lint(wf, nil); len(findings) != 0 {
		t.Errorf("unexpected findings:\n%s", messages(findings))
	}
}
