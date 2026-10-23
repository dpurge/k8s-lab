package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"assistant/internal/a2ui"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// composeReplySchema is the reply schema of the compose state of ui-compose,
// with the interface typed.
func composeReplySchema(t *testing.T) map[string]any {
	t.Helper()
	wf, err := Load("../../workflows/ui-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	v, err := a2ui.New()
	if err != nil {
		t.Fatal(err)
	}
	schema, err := ReplySchemaWithUI(wf, "compose", reg, v.InterfaceSchema())
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func validates(t *testing.T, compiled *jsonschema.Schema, reply string) error {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(reply))
	if err != nil {
		t.Fatalf("test reply is not JSON: %v", err)
	}
	return compiled.Validate(v)
}

func TestTypedInterfaceSchemaAcceptsTheGuideExampleAndPlainReplies(t *testing.T) {
	compiled := compileReplySchema(t, composeReplySchema(t))
	for name, reply := range map[string]string{
		"guide example":     a2ui.GuideExample,
		"plain question":    `{"state_patch":{},"action":{"type":"ask_user","payload":{"question":"Q"}}}`,
		"other payload key": `{"state_patch":{},"action":{"type":"ask_user","payload":{"question":"Q","hint":[1]}}}`,
		"goto":              `{"state_patch":{},"action":{"type":"goto","state":"summarise"}}`,
	} {
		if err := validates(t, compiled, reply); err != nil {
			t.Errorf("%s: should accept: %v", name, err)
		}
	}
}

func TestTypedInterfaceSchemaRejectsABrokenInterface(t *testing.T) {
	compiled := compileReplySchema(t, composeReplySchema(t))
	surface := func(createSurface string) string {
		return `{"state_patch":{},"action":{"type":"ask_user","payload":{"question":"Q","ui":[{"version":"v1.0","createSurface":` + createSurface + `}]}}}`
	}
	const root = `{"id":"root","component":"Text","text":"x"}`
	for name, reply := range map[string]string{
		"dataModel beside createSurface": prodCreateSurfaceClosedEarly,
		"missing components":             surface(`{"surfaceId":"s","catalogId":"` + a2ui.CatalogID + `"}`),
		"empty components":               surface(`{"surfaceId":"s","catalogId":"` + a2ui.CatalogID + `","components":[]}`),
		"other catalog":                  surface(`{"surfaceId":"s","catalogId":"https://example.com/catalog.json","components":[` + root + `]}`),
		"unknown component":              surface(`{"surfaceId":"s","catalogId":"` + a2ui.CatalogID + `","components":[{"id":"root","component":"Hologram"}]}`),
		"component without id":           surface(`{"surfaceId":"s","catalogId":"` + a2ui.CatalogID + `","components":[{"component":"Text"}]}`),
		"empty surfaceId":                surface(`{"surfaceId":"","catalogId":"` + a2ui.CatalogID + `","components":[` + root + `]}`),
		"unknown createSurface key":      surface(`{"surfaceId":"s","catalogId":"` + a2ui.CatalogID + `","components":[` + root + `],"theme":{}}`),
		"ui not an array":                `{"state_patch":{},"action":{"type":"ask_user","payload":{"ui":{}}}}`,
		"no message":                     `{"state_patch":{},"action":{"type":"ask_user","payload":{"ui":[]}}}`,
	} {
		if err := validates(t, compiled, reply); err == nil {
			t.Errorf("%s: should reject %s", name, reply)
		}
	}
}

func TestInterfaceSchemaNamesTheCatalogComponents(t *testing.T) {
	v, err := a2ui.New()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(v.InterfaceSchema())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range v.ComponentTypes() {
		if !strings.Contains(string(encoded), `"`+name+`"`) {
			t.Errorf("the interface schema lacks component %q", name)
		}
	}
}

// A workflow without ui, or a caller without a ui schema, gets the schema the
// engine always sent.
func TestReplySchemaIsUnchangedWithoutUI(t *testing.T) {
	v, err := a2ui.New()
	if err != nil {
		t.Fatal(err)
	}
	_, wf, reg := requestFixtures(t)
	plain, err := ReplySchema(wf, "teach", reg)
	if err != nil {
		t.Fatal(err)
	}
	withUI, err := ReplySchemaWithUI(wf, "teach", reg, v.InterfaceSchema())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(plain)
	b, _ := json.Marshal(withUI)
	if string(a) != string(b) {
		t.Errorf("a workflow without ui got a different schema:\n%s\n%s", a, b)
	}
	if strings.Contains(string(a), "createSurface") {
		t.Error("the plain schema mentions createSurface")
	}

	uiWorkflow, err := Load("../../workflows/ui-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	empty, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	untyped, _ := ReplySchema(uiWorkflow, "compose", empty)
	nilSchema, _ := ReplySchemaWithUI(uiWorkflow, "compose", empty, nil)
	a, _ = json.Marshal(untyped)
	b, _ = json.Marshal(nilSchema)
	if string(a) != string(b) {
		t.Error("a nil ui schema must give the plain schema")
	}
}

func TestEveryRequestOfAUIWorkflowCarriesTheTypedInterface(t *testing.T) {
	p, _, _ := requestFixtures(t)
	v, err := a2ui.New()
	if err != nil {
		t.Fatal(err)
	}
	p.UISchema = v.InterfaceSchema()
	wf, err := Load("../../workflows/ui-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := NewRegistry()
	req, err := BuildRequest(p, wf, &Run{StateName: "compose"}, reg, "ask me", nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(req.Schema)
	if !strings.Contains(string(encoded), "createSurface") {
		t.Error("the request schema does not type the interface")
	}
}
