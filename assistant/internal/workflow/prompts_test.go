package workflow

import (
	"strings"
	"testing"
	"testing/fstest"
)

func promptFS(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

const testTemplate = "Start.\nLanguage: {{language}}\n{{languageRules}}\n\n{{outputRules}}\nEnd."

func testPrompts(t *testing.T) *Prompts {
	t.Helper()
	p, err := LoadPrompts(promptFS(map[string]string{
		"step.system.txt":         testTemplate,
		"lang/xaa/rules.txt":      "Rules for {{language}}: write A.",
		"lang/xbb/rules.txt":      "Rules for B.",
		"output/lesson/rules.txt": "Output a lesson.",
		"output/quiz/rules.txt":   "Output a quiz.",
	}))
	if err != nil {
		t.Fatalf("LoadPrompts: %v", err)
	}
	return p
}

func TestDefaultPromptsLoadAndRender(t *testing.T) {
	p, err := DefaultPrompts()
	if err != nil {
		t.Fatal(err)
	}
	out := p.System("", "")
	for _, want := range []string{`"state_patch"`, `"type": "tool"`, `"type": "goto"`, `"type": "ask_user"`} {
		if !strings.Contains(out, want) {
			t.Errorf("default prompt lacks %s", want)
		}
	}
	if strings.Contains(out, "{{") {
		t.Errorf("unfilled placeholder in default prompt:\n%s", out)
	}
	if strings.Contains(out, "\n\n\n") {
		t.Errorf("default prompt has runs of blank lines")
	}
}

func TestSystemFillsLanguageSnippet(t *testing.T) {
	out := testPrompts(t).System("xaa", "")
	if !strings.Contains(out, "Language: xaa") || !strings.Contains(out, "Rules for xaa: write A.") {
		t.Errorf("language not filled:\n%s", out)
	}
	if strings.Contains(out, "Output a") {
		t.Errorf("no output kind was asked for:\n%s", out)
	}
}

func TestSystemFillsOutputSnippet(t *testing.T) {
	out := testPrompts(t).System("", "quiz")
	if !strings.Contains(out, "Output a quiz.") || strings.Contains(out, "Output a lesson.") {
		t.Errorf("output not filled:\n%s", out)
	}
}

func TestSystemFillsBothAxesIndependently(t *testing.T) {
	p := testPrompts(t)
	out := p.System("xbb", "lesson")
	for _, want := range []string{"Rules for B.", "Output a lesson.", "Language: xbb"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	other := p.System("xaa", "lesson")
	if !strings.Contains(other, "Output a lesson.") || !strings.Contains(other, "write A.") {
		t.Errorf("changing the language must not change the output rules:\n%s", other)
	}
}

func TestSystemMissingSnippetsGiveEmptyText(t *testing.T) {
	p := testPrompts(t)
	out := p.System("zzz", "nokind")
	if !strings.Contains(out, "Language: zzz") || !strings.HasSuffix(out, "End.") || !strings.HasPrefix(out, "Start.") {
		t.Errorf("unexpected output:\n%s", out)
	}
	if strings.Contains(out, "Rules for") || strings.Contains(out, "Output a") {
		t.Errorf("missing snippets must render empty:\n%s", out)
	}
	if strings.Contains(out, "\n\n\n") {
		t.Errorf("empty snippets left runs of blank lines:\n%q", out)
	}
}

func TestSystemSnippetsDoNotLeakBetweenKinds(t *testing.T) {
	p := testPrompts(t)
	if out := p.System("xaa", "lesson"); strings.Contains(out, "Rules for B.") || strings.Contains(out, "quiz") {
		t.Errorf("another kind's snippet leaked:\n%s", out)
	}
	if out := p.System("xbb", "quiz"); strings.Contains(out, "write A.") || strings.Contains(out, "lesson") {
		t.Errorf("another kind's snippet leaked:\n%s", out)
	}
}

func TestSystemIsDeterministic(t *testing.T) {
	p := testPrompts(t)
	if p.System("xaa", "lesson") != p.System("xaa", "lesson") {
		t.Errorf("rendering is not deterministic")
	}
}

func TestLoadPromptsRejectsUnknownPlaceholders(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"template", map[string]string{"step.system.txt": "Hi {{nope}} {{language}}"}, "step.system.txt: unknown placeholder {{nope}}"},
		{"template with typo", map[string]string{"step.system.txt": "{{languageRule}}"}, "{{languageRule}}"},
		{"language snippet", map[string]string{"step.system.txt": testTemplate, "lang/xaa/rules.txt": "x {{nope}}"}, "lang/xaa/rules.txt: unknown placeholder {{nope}}"},
		{"output snippet", map[string]string{"step.system.txt": testTemplate, "output/quiz/rules.txt": "{{outputRules}}"}, "output/quiz/rules.txt: unknown placeholder {{outputRules}}"},
		{"snippet cannot pull in another snippet", map[string]string{"step.system.txt": testTemplate, "lang/xaa/rules.txt": "{{languageRules}}"}, "unknown placeholder {{languageRules}}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadPrompts(promptFS(tc.files))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestLoadPromptsStructureErrors(t *testing.T) {
	if _, err := LoadPrompts(promptFS(map[string]string{"lang/xaa/rules.txt": "x"})); err == nil || !strings.Contains(err.Error(), "step.system.txt") {
		t.Errorf("missing template: %v", err)
	}
	_, err := LoadPrompts(promptFS(map[string]string{"step.system.txt": testTemplate, "lang/Bad Name/rules.txt": "x"}))
	if err == nil || !strings.Contains(err.Error(), "lang/Bad Name") {
		t.Errorf("bad directory name: %v", err)
	}
	p, err := LoadPrompts(promptFS(map[string]string{"step.system.txt": testTemplate, "lang/xaa/notes.md": "no rules here"}))
	if err != nil || len(p.Languages()) != 0 {
		t.Errorf("a directory without rules.txt should be ignored: %v %v", err, p)
	}
}

func TestLoadPromptsWithoutSnippetDirs(t *testing.T) {
	p, err := LoadPrompts(promptFS(map[string]string{"step.system.txt": testTemplate}))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Languages()) != 0 || len(p.Outputs()) != 0 {
		t.Errorf("expected no snippets")
	}
}

func TestLanguagesAndOutputsAreSorted(t *testing.T) {
	p := testPrompts(t)
	if got := strings.Join(p.Languages(), ","); got != "xaa,xbb" {
		t.Errorf("Languages = %q", got)
	}
	if got := strings.Join(p.Outputs(), ","); got != "lesson,quiz" {
		t.Errorf("Outputs = %q", got)
	}
}

func TestSnippetsAreTrimmed(t *testing.T) {
	p, err := LoadPrompts(promptFS(map[string]string{
		"step.system.txt":    "A\n{{languageRules}}\nB",
		"lang/xaa/rules.txt": "\n\n  rules  \n\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.System("xaa", ""); got != "A\nrules\nB" {
		t.Errorf("got %q", got)
	}
}
