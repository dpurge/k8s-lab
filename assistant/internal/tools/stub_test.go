package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"assistant/internal/tools"
	"assistant/internal/workflow"
)

func TestTheStubsAreTheFourToolsLearnTopicUses(t *testing.T) {
	reg, err := workflow.NewRegistry(tools.Stubs()...)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(reg.Names(), ","); got != "memory_add,memory_search,rag_search,web_search" {
		t.Errorf("names = %s", got)
	}
	wf, err := workflow.Load("../../workflows/learn-topic.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if findings := workflow.Lint(wf, reg); len(findings) != 0 {
		t.Errorf("learn-topic does not lint against the stubs: %v", findings)
	}
}

func TestEveryStubSaysItIsAStubAndIsJSON(t *testing.T) {
	for _, tool := range tools.Stubs() {
		args := map[string]any{"query": "arabic verbs", "text": "likes tea"}
		got, err := tool.Call(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("%s: %v", tool.Name(), err)
		}
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m["stub"] != true || !strings.Contains(m["note"].(string), "STUB") {
			t.Errorf("%s does not say it is a stub: %s", tool.Name(), raw)
		}
	}
}

func TestStubSchemasAcceptTheirArgumentAndRefuseOthers(t *testing.T) {
	cases := map[string]string{"memory_search": "query", "memory_add": "text", "rag_search": "query", "web_search": "query"}
	for _, tool := range tools.Stubs() {
		raw, _ := json.Marshal(tool.ArgsSchema())
		c := jsonschema.NewCompiler()
		doc, _ := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
		if err := c.AddResource("s.json", doc); err != nil {
			t.Fatal(err)
		}
		schema, err := c.Compile("s.json")
		if err != nil {
			t.Fatalf("%s: %v", tool.Name(), err)
		}
		valid := func(v string) bool {
			inst, _ := jsonschema.UnmarshalJSON(strings.NewReader(v))
			return schema.Validate(inst) == nil
		}
		arg := cases[tool.Name()]
		if !valid(`{"` + arg + `":"x"}`) {
			t.Errorf("%s refuses its own argument", tool.Name())
		}
		for _, bad := range []string{`{}`, `{"` + arg + `":""}`, `{"` + arg + `":1}`, `{"` + arg + `":"x","extra":1}`} {
			if valid(bad) {
				t.Errorf("%s accepts %s", tool.Name(), bad)
			}
		}
	}
}

func TestTheOutlineEchoesTheQuery(t *testing.T) {
	for _, tool := range tools.Stubs() {
		if tool.Name() != "rag_search" {
			continue
		}
		got, _ := tool.Call(context.Background(), map[string]any{"query": "kubernetes"})
		raw, _ := json.Marshal(got)
		if !strings.Contains(string(raw), "kubernetes") {
			t.Errorf("%s", raw)
		}
	}
}
