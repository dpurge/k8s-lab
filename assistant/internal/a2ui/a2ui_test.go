package a2ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newValidator(t *testing.T, limits ...Limits) *Validator {
	t.Helper()
	v, err := New(limits...)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func msg(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type obj = map[string]any

func create(id string) obj {
	return obj{"version": "v1.0", "createSurface": obj{"surfaceId": id, "catalogId": CatalogID}}
}

func update(id string, components ...obj) obj {
	list := make([]any, len(components))
	for i, c := range components {
		list[i] = c
	}
	return obj{"version": "v1.0", "updateComponents": obj{"surfaceId": id, "components": list}}
}

func text(id, s string) obj { return obj{"id": id, "component": "Text", "text": s} }

func problems(t *testing.T, err error) []Problem {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a *ValidationError", err)
	}
	return ve.Problems
}

func wantProblem(t *testing.T, err error, path, contains string) {
	t.Helper()
	for _, p := range problems(t, err) {
		if (path == "" || p.Path == path) && strings.Contains(p.Message, contains) {
			return
		}
	}
	t.Errorf("no problem at %q containing %q in: %v", path, contains, err)
}

// Every example of the reference's basic catalog is a valid conversation.
func TestEveryReferenceExampleIsAccepted(t *testing.T) {
	files, err := filepath.Glob("testdata/examples/*.json")
	if err != nil || len(files) < 40 {
		t.Fatalf("examples: %d files, %v", len(files), err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var example struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(raw, &example); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		s := NewSurfaces(newValidator(t))
		for i, m := range example.Messages {
			if _, err := s.Apply(m); err != nil {
				t.Errorf("%s message %d: %v", filepath.Base(file), i, err)
			}
		}
		if err := s.Finish(); err != nil {
			t.Errorf("%s: %v", filepath.Base(file), err)
		}
	}
}

func TestMalformedEnvelopesAreRefused(t *testing.T) {
	v := newValidator(t)
	for name, raw := range map[string]string{
		"not json":          `{`,
		"an array":          `[]`,
		"null":              `null`,
		"two values":        `{"version":"v1.0","deleteSurface":{"surfaceId":"s"}} {}`,
		"no kind":           `{"version":"v1.0"}`,
		"two kinds":         `{"version":"v1.0","deleteSurface":{"surfaceId":"s"},"updateDataModel":{"surfaceId":"s","value":1}}`,
		"unknown kind":      `{"version":"v1.0","explode":{}}`,
		"wrong version":     `{"version":"v0.9","deleteSurface":{"surfaceId":"s"}}`,
		"no version":        `{"deleteSurface":{"surfaceId":"s"}}`,
		"extra key in body": `{"version":"v1.0","deleteSurface":{"surfaceId":"s","force":true}}`,
		"empty components":  `{"version":"v1.0","updateComponents":{"surfaceId":"s","components":[]}}`,
		"missing surfaceId": `{"version":"v1.0","createSurface":{"catalogId":"x"}}`,
	} {
		if _, err := v.Check([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAComponentErrorNamesTheComponentAndTheProperty(t *testing.T) {
	v := newValidator(t)
	cases := []struct {
		name      string
		component obj
		path      string
		contains  string
	}{
		{"unknown type", obj{"id": "a", "component": "Hologram"}, "/updateComponents/components/0/component", `unknown component type "Hologram"`},
		{"missing property", obj{"id": "a", "component": "Text"}, "/updateComponents/components/0", "text"},
		{"extra property", obj{"id": "a", "component": "Text", "text": "x", "colour": "red"}, "/updateComponents/components/0/colour", "not allowed"},
		{"wrong type", obj{"id": "a", "component": "Text", "text": 5}, "/updateComponents/components/0/text", ""},
		{"bad enum", obj{"id": "a", "component": "Text", "text": "x", "variant": "huge"}, "/updateComponents/components/0/variant", ""},
		{"bad icon", obj{"id": "a", "component": "Icon", "name": "teleport"}, "/updateComponents/components/0/name", ""},
		{"no id", obj{"component": "Text", "text": "x"}, "/updateComponents/components/0", "id"},
		{"children not ids", obj{"id": "a", "component": "Column", "children": []any{1}}, "/updateComponents/components/0/children", ""},
	}
	for _, c := range cases {
		_, err := v.Check(msg(t, update("s", c.component)))
		if err == nil {
			t.Errorf("%s: accepted", c.name)
			continue
		}
		wantProblem(t, err, c.path, c.contains)
	}
}

func TestOneBadComponentIsReportedOnceAmongGoodOnes(t *testing.T) {
	v := newValidator(t)
	_, err := v.Check(msg(t, update("s", text("a", "ok"), obj{"id": "b", "component": "Image"}, text("c", "ok"))))
	ps := problems(t, err)
	for _, p := range ps {
		if !strings.HasPrefix(p.Path, "/updateComponents/components/1") {
			t.Errorf("a problem points outside the bad component: %v", p)
		}
	}
	if len(ps) == 0 || len(ps) > 3 {
		t.Errorf("problems = %v, want a few about component 1 only", ps)
	}
}

func TestDuplicateIdsInOneMessageAreRefused(t *testing.T) {
	_, err := newValidator(t).Check(msg(t, update("s", text("a", "1"), text("a", "2"))))
	wantProblem(t, err, "/updateComponents/components/1/id", `"a" is used twice`)
}

func TestSurfaceLifecycle(t *testing.T) {
	s := NewSurfaces(newValidator(t))
	apply := func(m obj) error { _, err := s.Apply(msg(t, m)); return err }
	if err := apply(update("s", text("root", "x"))); err == nil {
		t.Error("an update for a surface that does not exist was accepted")
	}
	if err := apply(create("s")); err != nil {
		t.Fatal(err)
	}
	if err := apply(create("s")); err == nil {
		t.Error("a surface was created twice")
	}
	if err := apply(update("s", text("root", "x"))); err != nil {
		t.Fatal(err)
	}
	if err := apply(obj{"version": "v1.0", "updateDataModel": obj{"surfaceId": "s", "value": obj{"a": 1}}}); err != nil {
		t.Error(err)
	}
	if err := apply(obj{"version": "v1.0", "updateDataModel": obj{"surfaceId": "nope", "value": 1}}); err == nil {
		t.Error("a data update for a missing surface was accepted")
	}
	if err := apply(obj{"version": "v1.0", "deleteSurface": obj{"surfaceId": "s"}}); err != nil {
		t.Fatal(err)
	}
	if s.Has("s") {
		t.Error("the surface is still there")
	}
	if err := apply(create("s")); err == nil {
		t.Error("a deleted surface's id was used again")
	}
	if err := apply(obj{"version": "v1.0", "deleteSurface": obj{"surfaceId": "s"}}); err == nil {
		t.Error("a surface was deleted twice")
	}
}

func TestOnlyTheBasicCatalogIsAvailable(t *testing.T) {
	s := NewSurfaces(newValidator(t))
	_, err := s.Apply(msg(t, obj{"version": "v1.0", "createSurface": obj{"surfaceId": "s", "catalogId": "https://example.com/mine.json"}}))
	wantProblem(t, err, "/createSurface/catalogId", "not available")
}

func TestCyclesAndDepthAreRefusedAndNothingIsKept(t *testing.T) {
	s := NewSurfaces(newValidator(t, Limits{MaxMessageBytes: 1 << 20, MaxComponentsPerMessage: 500, MaxComponentsPerSurface: 1000, MaxDepth: 5}))
	if _, err := s.Apply(msg(t, create("s"))); err != nil {
		t.Fatal(err)
	}
	col := func(id string, children ...string) obj {
		list := make([]any, len(children))
		for i, c := range children {
			list[i] = c
		}
		return obj{"id": id, "component": "Column", "children": list}
	}
	_, err := s.Apply(msg(t, update("s", col("root", "a"), col("a", "root"))))
	wantProblem(t, err, "", "cycle")
	_, err = s.Apply(msg(t, update("s", col("x", "x"))))
	wantProblem(t, err, "", "cycle")
	if len(s.Components("s")) != 0 {
		t.Errorf("a refused update left components: %v", s.Components("s"))
	}
	var chain []obj
	for i := 0; i < 8; i++ {
		id := "n" + string(rune('0'+i))
		if i == 0 {
			id = "root"
		}
		next := "n" + string(rune('0'+i+1))
		if i == 7 {
			chain = append(chain, col(id))
		} else {
			chain = append(chain, col(id, next))
		}
	}
	_, err = s.Apply(msg(t, update("s", chain...)))
	wantProblem(t, err, "", "levels deep; the limit is 5")
	if _, err := s.Apply(msg(t, update("s", chain[:4]...))); err == nil {
		// dangling references are allowed until Finish
	}
}

func TestFinishAsksForARootAndForEveryReferencedComponent(t *testing.T) {
	s := NewSurfaces(newValidator(t))
	if _, err := s.Apply(msg(t, create("s"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(msg(t, update("s", obj{"id": "root", "component": "Column", "children": []any{"later"}}))); err != nil {
		t.Fatalf("a reference to a component that comes in a later message must be allowed: %v", err)
	}
	wantProblem(t, s.Finish(), "", `names "later", which does not exist`)
	if _, err := s.Apply(msg(t, update("s", text("later", "here")))); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(); err != nil {
		t.Errorf("complete tree: %v", err)
	}

	s2 := NewSurfaces(newValidator(t))
	s2.Apply(msg(t, create("s")))
	s2.Apply(msg(t, update("s", text("notroot", "x"))))
	wantProblem(t, s2.Finish(), "", `no component with the id "root"`)
}

func TestReferencesInEveryKindOfProperty(t *testing.T) {
	s := NewSurfaces(newValidator(t))
	s.Apply(msg(t, create("s")))
	_, err := s.Apply(msg(t, update("s",
		obj{"id": "root", "component": "Column", "children": []any{"card", "tabs", "list", "modal", "btn"}},
		obj{"id": "card", "component": "Card", "child": "m1"},
		obj{"id": "tabs", "component": "Tabs", "tabs": []any{obj{"title": "A", "child": "m2"}}},
		obj{"id": "list", "component": "List", "children": obj{"componentId": "m3", "path": "/items"}},
		obj{"id": "modal", "component": "Modal", "trigger": "m4", "content": "m5"},
		obj{"id": "btn", "component": "Button", "child": "m6", "action": obj{"event": obj{"name": "x"}}},
	)))
	if err != nil {
		t.Fatal(err)
	}
	ps := problems(t, s.Finish())
	for _, want := range []string{"m1", "m2", "m3", "m4", "m5", "m6"} {
		found := false
		for _, p := range ps {
			found = found || strings.Contains(p.Message, `"`+want+`"`)
		}
		if !found {
			t.Errorf("a missing %s was not reported: %v", want, ps)
		}
	}
}

func TestSizeLimits(t *testing.T) {
	v := newValidator(t, Limits{MaxMessageBytes: 600, MaxComponentsPerMessage: 3, MaxComponentsPerSurface: 5, MaxDepth: 20})
	_, err := v.Check(msg(t, update("s", text("a", strings.Repeat("x", 700)))))
	wantProblem(t, err, "", "limit is 600")
	_, err = v.Check(msg(t, update("s", text("a", "1"), text("b", "2"), text("c", "3"), text("d", "4"))))
	wantProblem(t, err, "/updateComponents/components", "4 components; the limit is 3")

	s := NewSurfaces(v)
	s.Apply(msg(t, create("s")))
	if _, err := s.Apply(msg(t, update("s", text("a", "1"), text("b", "2"), text("c", "3")))); err != nil {
		t.Fatal(err)
	}
	_, err = s.Apply(msg(t, update("s", text("d", "4"), text("e", "5"), text("f", "6"))))
	wantProblem(t, err, "", "6 components; the limit is 5")
	if _, err := s.Apply(msg(t, update("s", text("a", "again"), text("b", "again")))); err != nil {
		t.Errorf("replacing components must not count them twice: %v", err)
	}
}

func TestFunctionCalls(t *testing.T) {
	s := NewSurfaces(newValidator(t))
	call := func(id, catalog, fn string) obj {
		return obj{"version": "v1.0", "callRendererFunction": obj{"functionCallId": id, "callFunction": obj{"catalogId": catalog, "@call": fn, "args": obj{}}}}
	}
	_, err := s.Apply(msg(t, call("c1", CatalogID, "formatNumber")))
	wantProblem(t, err, "/callRendererFunction/callFunction/@call", "only by the renderer")
	_, err = s.Apply(msg(t, call("c2", CatalogID, "teleport")))
	wantProblem(t, err, "", "no function")
	_, err = s.Apply(msg(t, call("c3", "https://example.com/x.json", "formatNumber")))
	wantProblem(t, err, "", "not available")
}

func TestComponentTypesAreTheEighteenOfTheBasicCatalog(t *testing.T) {
	want := "AudioPlayer Button Card CheckBox ChoicePicker Column DateTimeInput Divider Icon Image List Modal Row Slider Tabs Text TextField Video"
	if got := strings.Join(newValidator(t).ComponentTypes(), " "); got != want {
		t.Errorf("types = %s", got)
	}
}

func action(surface, component, name string, extra obj) obj {
	a := obj{"name": name, "surfaceId": surface, "sourceComponentId": component, "timestamp": "2026-10-21T12:00:00Z", "context": obj{}}
	for k, v := range extra {
		a[k] = v
	}
	return obj{"version": "v1.0", "action": a}
}

func TestEventsFromThePageAreCheckedAgainstTheProtocol(t *testing.T) {
	v := newValidator(t)
	e, err := v.CheckEvent(msg(t, action("s", "btn", "send", obj{"userMessage": "Sent it", "context": obj{"email": "a@b.c"}})))
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != "action" || e.SurfaceID != "s" || e.ComponentID != "btn" || e.Name != "send" || e.UserMessage != "Sent it" {
		t.Errorf("event = %+v", e)
	}
	e, err = v.CheckEvent(msg(t, obj{"version": "v1.0", "error": obj{"code": "VALIDATION_FAILED", "surfaceId": "s", "path": "/components/0", "message": "bad"}}))
	if err != nil || e.Kind != "error" || e.SurfaceID != "s" {
		t.Errorf("error event: %+v %v", e, err)
	}
	for name, bad := range map[string]string{
		"not json":         `{`,
		"no kind":          `{"version":"v1.0"}`,
		"unknown kind":     `{"version":"v1.0","click":{}}`,
		"missing name":     `{"version":"v1.0","action":{"surfaceId":"s","sourceComponentId":"b","timestamp":"2026-10-21T12:00:00Z","context":{}}}`,
		"missing context":  `{"version":"v1.0","action":{"name":"x","surfaceId":"s","sourceComponentId":"b","timestamp":"2026-10-21T12:00:00Z"}}`,
		"extra key":        `{"version":"v1.0","action":{"name":"x","surfaceId":"s","sourceComponentId":"b","timestamp":"2026-10-21T12:00:00Z","context":{}},"extra":1}`,
		"wrong version":    `{"version":"v0.9","action":{"name":"x","surfaceId":"s","sourceComponentId":"b","timestamp":"2026-10-21T12:00:00Z","context":{}}}`,
		"an agent message": `{"version":"v1.0","deleteSurface":{"surfaceId":"s"}}`,
	} {
		if _, err := v.CheckEvent([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	huge := action("s", "b", "x", obj{"context": obj{"blob": strings.Repeat("x", 300*1024)}})
	if _, err := v.CheckEvent(msg(t, huge)); err == nil {
		t.Error("an oversized event was accepted")
	}
}

func TestAnActionMustComeFromAComponentOfASurfaceTheConversationDrew(t *testing.T) {
	v := newValidator(t)
	s := NewSurfaces(v)
	s.Apply(msg(t, create("s")))
	s.Apply(msg(t, update("s", obj{"id": "root", "component": "Button", "child": "t", "action": obj{"event": obj{"name": "go"}}}, text("t", "Go"))))
	check := func(m obj) error {
		e, err := v.CheckEvent(msg(t, m))
		if err != nil {
			t.Fatal(err)
		}
		return s.CheckAction(e)
	}
	if err := check(action("s", "root", "go", nil)); err != nil {
		t.Errorf("a real action: %v", err)
	}
	wantProblem(t, check(action("nope", "root", "go", nil)), "/action/surfaceId", "no surface")
	wantProblem(t, check(action("s", "ghost", "go", nil)), "/action/sourceComponentId", "no component")
	e, _ := v.CheckEvent(msg(t, obj{"version": "v1.0", "error": obj{"code": "X", "message": "m", "surfaceId": "s"}}))
	if err := s.CheckAction(e); err != nil {
		t.Errorf("only actions are matched against components: %v", err)
	}
}

func TestASurfaceWithNoComponentsIsIncompleteWhenFinished(t *testing.T) {
	s := NewSurfaces(newValidator(t))
	if _, err := s.Apply(msg(t, create("s"))); err != nil {
		t.Fatal(err)
	}
	wantProblem(t, s.Finish(), "", `no component with the id "root"`)
}

func TestComponentIdsMustBeIdentifiersBecauseThePageRefusesOthers(t *testing.T) {
	v := newValidator(t)
	for _, id := range []string{"text-card", "two words", "1st", "a.b", ""} {
		_, err := v.Check(msg(t, update("s", text(id, "x"))))
		if err == nil {
			t.Errorf("%q was accepted", id)
			continue
		}
		wantProblem(t, err, "", "")
	}
	for _, id := range []string{"root", "text_card", "_x", "Card2", "تسمية", "名前"} {
		if _, err := v.Check(msg(t, update("s", text(id, "x")))); err != nil {
			t.Errorf("%q refused: %v", id, err)
		}
	}
}

func rendererCall(id, catalog, fn string, args obj) obj {
	return obj{"version": "v1.0", "callRendererFunction": obj{"functionCallId": id, "callFunction": obj{"catalogId": catalog, "@call": fn, "args": args}}}
}

func TestTheAssistantsViewportFunctionMayBeCalledByAnAgentAndNothingElseOfItsCatalogCan(t *testing.T) {
	s := NewSurfaces(newValidator(t))
	if _, err := s.Apply(msg(t, rendererCall("c1", AssistantCatalogID, "viewport", obj{}))); err != nil {
		t.Errorf("viewport: %v", err)
	}
	_, err := s.Apply(msg(t, rendererCall("c2", AssistantCatalogID, "listWorkflows", obj{})))
	wantProblem(t, err, "/callRendererFunction/callFunction/@call", "only by the renderer")
	_, err = s.Apply(msg(t, rendererCall("c3", CatalogID, "viewport", obj{})))
	wantProblem(t, err, "", "has no function")
	_, err = s.Apply(msg(t, rendererCall("c4", AssistantCatalogID, "formatNumber", obj{"value": 1})))
	wantProblem(t, err, "", "has no function")
	if _, err := s.Apply(msg(t, rendererCall("c5", AssistantCatalogID, "viewport", obj{"colour": "red"}))); err == nil {
		t.Error("viewport takes no arguments")
	}
	if _, err := s.Apply(msg(t, rendererCall("c1", AssistantCatalogID, "viewport", obj{}))); err == nil {
		t.Error("a call id was used twice")
	}
}

func TestEventsMayCallAndAnswerWithTheAssistantsFunctions(t *testing.T) {
	v := newValidator(t)
	call := `{"version":"v1.0","callAgentFunction":{"surfaceId":"s","functionCallId":"r1","callFunction":{"@call":"listWorkflows","catalogId":"` + AssistantCatalogID + `","args":{}}}}`
	e, err := v.CheckEvent([]byte(call))
	if err != nil || e.Kind != "callAgentFunction" || e.SurfaceID != "s" {
		t.Fatalf("%+v %v", e, err)
	}
	answer := `{"version":"v1.0","rendererFunctionResponse":{"functionCallId":"c1","value":{"width":1280,"height":900,"pixelRatio":1,"colorScheme":"light"}}}`
	if e, err := v.CheckEvent([]byte(answer)); err != nil || e.Kind != "rendererFunctionResponse" {
		t.Errorf("%+v %v", e, err)
	}
	if _, err := v.CheckEvent([]byte(`{"version":"v1.0","rendererFunctionResponse":{"functionCallId":"c1"}}`)); err == nil {
		t.Error("an answer with neither a value nor an error was accepted")
	}
}

func TestAnActionMayCarryTheValuesTheUserEnteredInAnExtension(t *testing.T) {
	v := newValidator(t)
	body := action("s", "btn", "send", obj{"metadata": obj{"extensions": obj{"assistant_data_model": obj{"s": obj{"name": "Ada"}}}}})
	if _, err := v.CheckEvent(msg(t, body)); err != nil {
		t.Errorf("the extension was refused: %v", err)
	}
	reserved := action("s", "btn", "send", obj{"metadata": obj{"extensions": obj{"has-hyphen": 1}}})
	if _, err := v.CheckEvent(msg(t, reserved)); err == nil {
		t.Error("an extension key that is not an identifier was accepted")
	}
}

func TestTheGuideListsEveryComponentAndItsExampleIsValid(t *testing.T) {
	v := newValidator(t)
	guide := v.Guide()
	for _, name := range v.ComponentTypes() {
		if !strings.Contains(guide, "\n- "+name+": ") {
			t.Errorf("the guide lacks %s", name)
		}
	}
	// The example reply must pass the same check a model's reply does.
	var reply struct {
		Action struct {
			Payload struct {
				UI []json.RawMessage `json:"ui"`
			} `json:"payload"`
		} `json:"action"`
	}
	if err := json.Unmarshal([]byte(GuideExample), &reply); err != nil {
		t.Fatal(err)
	}
	if err := v.CheckInterface(reply.Action.Payload.UI); err != nil {
		t.Errorf("the guide's own example is not valid: %v", err)
	}
	if len(guide) > 8000 {
		t.Errorf("the guide is %d bytes: a model with a small context cannot afford more", len(guide))
	}
}

func TestCheckInterfaceNamesTheMessageAndLimitsWhatItReports(t *testing.T) {
	v := newValidator(t)
	var many []any
	for i := 0; i < 20; i++ {
		many = append(many, obj{"id": fmt.Sprintf("c%d", i), "component": "Hologram"})
	}
	err := v.CheckInterface([]json.RawMessage{msg(t, create("s")), msg(t, update("s", many[0].(obj), many[1].(obj), many[2].(obj), many[3].(obj), many[4].(obj), many[5].(obj), many[6].(obj), many[7].(obj), many[8].(obj), many[9].(obj)))})
	ps := problems(t, err)
	if len(ps) != maxReportedProblems+1 || !strings.HasPrefix(ps[0].Path, "ui[1]/updateComponents/components/0") || !strings.Contains(ps[len(ps)-1].Message, "more problems") {
		t.Errorf("problems = %v", ps)
	}
	if err := v.CheckInterface([]json.RawMessage{msg(t, create("s"))}); err == nil || !strings.Contains(err.Error(), `no component with the id "root"`) {
		t.Errorf("an incomplete interface: %v", err)
	}
}
