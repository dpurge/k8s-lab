package workflow

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// renderVectors is the file prompt-eval keeps (vectors/template-render.json)
// and syncs here. The same hand-written cases are run against prompt-eval's
// JavaScript renderer, so the two implementations cannot drift apart.
type renderVectors struct {
	Axes []struct {
		Dir         string `json:"dir"`
		File        string `json:"file"`
		Placeholder string `json:"placeholder"`
		Selector    string `json:"selector"`
	} `json:"axes"`
	Vectors []struct {
		Name     string                       `json:"name"`
		Template string                       `json:"template"`
		Snippets map[string]map[string]string `json:"snippets"`
		Vars     map[string]string            `json:"vars"`
		Expected string                       `json:"expected"`
	} `json:"vectors"`
	Errors []struct {
		Name     string                       `json:"name"`
		Template string                       `json:"template"`
		Snippets map[string]map[string]string `json:"snippets"`
		Error    string                       `json:"error"`
	} `json:"errors"`
}

func loadRenderVectors(t *testing.T) renderVectors {
	t.Helper()
	data, err := os.ReadFile("testdata/prompt-eval/render-vectors.json")
	if err != nil {
		t.Fatalf("%v\n(sync it from prompt-eval: npm run sync-assistant)", err)
	}
	var v renderVectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func vectorFS(template string, snippets map[string]map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{"step.system.txt": {Data: []byte(template)}}
	for axisDir, byName := range snippets {
		for name, text := range byName {
			fsys[axisDir+"/"+name+"/rules.txt"] = &fstest.MapFile{Data: []byte(text)}
		}
	}
	return fsys
}

func TestRenderVectorsUseTheAxesTheEngineHas(t *testing.T) {
	v := loadRenderVectors(t)
	want := [][4]string{
		{"lang", "rules.txt", "languageRules", "language"},
		{"output", "rules.txt", "outputRules", "output"},
	}
	if len(v.Axes) != len(want) {
		t.Fatalf("vector file has %d axes, the engine has %d", len(v.Axes), len(want))
	}
	for i, axis := range v.Axes {
		if got := [4]string{axis.Dir, axis.File, axis.Placeholder, axis.Selector}; got != want[i] {
			t.Errorf("axis %d = %v, the engine uses %v", i, got, want[i])
		}
	}
	if len(v.Vectors) < 15 || len(v.Errors) < 6 {
		t.Errorf("too few vectors to mean anything: %d cases, %d errors", len(v.Vectors), len(v.Errors))
	}
}

func TestPromptsRenderEveryVector(t *testing.T) {
	for _, tc := range loadRenderVectors(t).Vectors {
		t.Run(tc.Name, func(t *testing.T) {
			p, err := LoadPrompts(vectorFS(tc.Template, tc.Snippets))
			if err != nil {
				t.Fatalf("LoadPrompts: %v", err)
			}
			if got := p.System(tc.Vars["language"], tc.Vars["output"]); got != tc.Expected {
				t.Errorf("rendered:\n%q\nwant:\n%q", got, tc.Expected)
			}
		})
	}
}

func TestPromptsRefuseEveryErrorVector(t *testing.T) {
	for _, tc := range loadRenderVectors(t).Errors {
		t.Run(tc.Name, func(t *testing.T) {
			_, err := LoadPrompts(vectorFS(tc.Template, tc.Snippets))
			if err == nil || !strings.Contains(err.Error(), tc.Error) {
				t.Errorf("err = %v, want it to contain %q", err, tc.Error)
			}
		})
	}
}
