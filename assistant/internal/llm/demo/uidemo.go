package demo

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The scripted ui-demo workflow: a tour of every component of the chat's
// interface toolkit, then a small form whose answers end the run. No model is
// involved; the point is to try the interface, and to test it, without one.

const basicCatalog = "https://a2ui.org/specification/v1_0/catalogs/basic/catalog.json"

// The assistant's own catalog of functions (schemas/a2ui/assistant_catalog.json).
const assistantCatalog = "https://assistant.k8s-lab.local/a2ui/catalog.json"

type obj = map[string]any

// surfaceID is unique for the life of the conversation (a surface id is used
// once), so a second run in the same conversation draws again.
func surfaceID(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

func create(id string, model obj) obj {
	body := obj{"surfaceId": id, "catalogId": basicCatalog}
	if model != nil {
		body["dataModel"] = model
	}
	return obj{"version": "v1.0", "createSurface": body}
}

func components(id string, list ...obj) obj {
	out := make([]any, len(list))
	for i, c := range list {
		out[i] = c
	}
	return obj{"version": "v1.0", "updateComponents": obj{"surfaceId": id, "components": out}}
}

func text(id, s string) obj { return obj{"id": id, "component": "Text", "text": s} }

func ids(list ...string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

// pixel is a 2x2 PNG, so the Image component has something to show that needs
// no host: a data: URL.
const pixel = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAFklEQVR4nGP8z8Dwn4EIwAjF/xkYAAgyAgM3KBzOAAAAAElFTkSuQmCC"

// silence is half a second of 8-bit mono WAV silence as a data: URL, for the
// AudioPlayer.
func silence() string {
	const rate, n = 8000, 4000
	header := make([]byte, 44)
	copy(header[0:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], 36+n)
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 1)
	binary.LittleEndian.PutUint32(header[24:], rate)
	binary.LittleEndian.PutUint32(header[28:], rate)
	binary.LittleEndian.PutUint16(header[32:], 1)
	binary.LittleEndian.PutUint16(header[34:], 8)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], n)
	data := make([]byte, n)
	for i := range data {
		data[i] = 128
	}
	return "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(append(header, data...))
}

// tour is every component of the basic catalog on one surface.
func tour() []any {
	id := surfaceID("tour")
	return []any{
		create(id, obj{"tour": obj{"name": "", "agree": false, "picks": []any{"b"}, "chips": []any{}, "volume": 30, "when": ""}}),
		components(id,
			obj{"id": "root", "component": "Column", "children": ids("title", "intro", "icons", "d1", "text_card", "tabs", "d2", "inputs", "d3", "media", "d4", "workflows_title", "workflows", "d5", "bottom")},
			text("title", "# The chat's interface toolkit"),
			text("intro", "Every component of the A2UI basic catalog, drawn by the page. **Bold**, *italic*, `code` and a [link](https://example.com) are Markdown in Text."),
			obj{"id": "icons", "component": "Row", "children": ids("i1", "i2", "i3", "i4", "i5")},
			obj{"id": "i1", "component": "Icon", "name": "home"},
			obj{"id": "i2", "component": "Icon", "name": "star"},
			obj{"id": "i3", "component": "Icon", "name": "settings"},
			obj{"id": "i4", "component": "Icon", "name": "mail"},
			obj{"id": "i5", "component": "Icon", "name": obj{"svgPath": "M12 2 2 22h20L12 2z"}},
			obj{"id": "d1", "component": "Divider"},
			obj{"id": "text_card", "component": "Card", "child": "list"},
			obj{"id": "list", "component": "List", "children": ids("l1", "l2", "l3")},
			text("l1", "A Card holding a List"),
			obj{"id": "l2", "component": "Text", "text": "…whose items are Text", "variant": "caption"},
			text("l3", "A Row (above) lays out the icons; a Column stacks all of this."),
			obj{"id": "tabs", "component": "Tabs", "tabs": []any{obj{"title": "One", "child": "tab1"}, obj{"title": "Two", "child": "tab2"}}},
			text("tab1", "The first tab."),
			text("tab2", "The second tab."),
			obj{"id": "d2", "component": "Divider"},
			obj{"id": "inputs", "component": "Column", "children": ids("name", "agree", "picks", "chips", "volume", "when")},
			obj{"id": "name", "component": "TextField", "label": "Your name (try typing in any script)", "value": obj{"@path": "/tour/name"}},
			obj{"id": "agree", "component": "CheckBox", "label": "I like it", "value": obj{"@path": "/tour/agree"}},
			obj{"id": "picks", "component": "ChoicePicker", "label": "Pick one", "variant": "mutuallyExclusive", "value": obj{"@path": "/tour/picks"},
				"options": []any{obj{"label": "Alpha", "value": "a"}, obj{"label": "Beta", "value": "b"}, obj{"label": "Gamma", "value": "c"}}},
			obj{"id": "chips", "component": "ChoicePicker", "label": "Pick several", "variant": "multipleSelection", "displayStyle": "chips", "filterable": true, "value": obj{"@path": "/tour/chips"},
				"options": []any{obj{"label": "One", "value": "1"}, obj{"label": "Two", "value": "2"}, obj{"label": "Three", "value": "3"}}},
			obj{"id": "volume", "component": "Slider", "label": "Volume", "min": 0, "max": 100, "value": obj{"@path": "/tour/volume"}},
			obj{"id": "when", "component": "DateTimeInput", "label": "When", "enableDate": true, "enableTime": true, "value": obj{"@path": "/tour/when"}},
			obj{"id": "d3", "component": "Divider"},
			obj{"id": "media", "component": "Column", "children": ids("image", "video", "audio")},
			obj{"id": "image", "component": "Image", "url": pixel, "description": "A tiny image from a data: URL", "variant": "smallFeature", "fit": "contain"},
			obj{"id": "video", "component": "Video", "url": "data:video/mp4;base64,AAAA"},
			obj{"id": "audio", "component": "AudioPlayer", "url": silence(), "description": "Half a second of silence"},
			obj{"id": "d4", "component": "Divider"},
			obj{"id": "workflows_title", "component": "Text", "text": "Workflows you can start, asked of the assistant by a function call:", "variant": "caption"},
			obj{"id": "workflows", "component": "Text", "text": obj{"@call": "listWorkflows", "catalogId": assistantCatalog, "args": obj{}}},
			obj{"id": "d5", "component": "Divider"},
			obj{"id": "bottom", "component": "Row", "children": ids("modal", "next")},
			obj{"id": "modal", "component": "Modal", "trigger": "open", "content": "modal_body"},
			obj{"id": "open", "component": "Button", "child": "open_label", "action": obj{"functionCall": obj{"@call": "formatNumber", "args": obj{"value": 1}}}},
			text("open_label", "Open a Modal"),
			text("modal_body", "This is the content of a Modal."),
			obj{"id": "next", "component": "Button", "child": "next_label", "variant": "primary", "action": obj{"event": obj{"name": "continue", "userMessage": "Continue to the form", "context": obj{}}}},
			text("next_label", "Continue to the form"),
		),
	}
}

// form is a small form; its Button is disabled until the name is filled in.
func form(viewport map[string]any) []any {
	id := surfaceID("form")
	need := func(path, message string) obj {
		return obj{"condition": obj{"@call": "required", "args": obj{"value": obj{"@path": path}}}, "message": message}
	}
	return []any{
		create(id, obj{"form": obj{"name": "", "topics": []any{}, "level": 3, "agree": false}}),
		components(id,
			obj{"id": "root", "component": "Card", "child": "col"},
			obj{"id": "col", "component": "Column", "children": ids("heading", "name", "topics", "level", "agree", "submit")},
			text("heading", "## Tell me about you"+seenFrom(viewport)),
			obj{"id": "name", "component": "TextField", "label": "Name", "value": obj{"@path": "/form/name"}, "checks": []any{need("/form/name", "Name is required")}},
			obj{"id": "topics", "component": "ChoicePicker", "label": "Topics", "variant": "multipleSelection", "displayStyle": "chips", "value": obj{"@path": "/form/topics"},
				"options": []any{obj{"label": "Languages", "value": "languages"}, obj{"label": "Cooking", "value": "cooking"}, obj{"label": "Hiking", "value": "hiking"}}},
			obj{"id": "level", "component": "Slider", "label": "How experienced are you (1 to 5)?", "min": 1, "max": 5, "steps": 4, "value": obj{"@path": "/form/level"}},
			obj{"id": "agree", "component": "CheckBox", "label": "You may keep my answers for this demo", "value": obj{"@path": "/form/agree"}},
			obj{"id": "submit", "component": "Button", "child": "submit_label", "variant": "primary",
				"checks": []any{need("/form/name", "Fill in your name first")},
				"action": obj{"event": obj{"name": "submit_form", "userMessage": "Submitted the form", "context": obj{
					"name": obj{"@path": "/form/name"}, "topics": obj{"@path": "/form/topics"}, "level": obj{"@path": "/form/level"}, "agree": obj{"@path": "/form/agree"},
				}}}},
			text("submit_label", "Submit"),
		),
	}
}

// seenFrom says, in one line, what the page told us about its window.
func seenFrom(viewport map[string]any) string {
	w, h := viewport["width"], viewport["height"]
	if w == nil || h == nil {
		return ""
	}
	scheme, _ := viewport["colorScheme"].(string)
	return fmt.Sprintf("\n\n*Seen from a %v×%v window, %s theme.*", w, h, scheme)
}

// viewportCall asks the page for its size, as a run does: the id is a
// placeholder, because the assistant gives every call a fresh one.
func viewportCall() []any {
	return []any{obj{"version": "v1.0", "callRendererFunction": obj{
		"functionCallId": "placeholder",
		"callFunction":   obj{"catalogId": assistantCatalog, "@call": "viewport", "args": obj{}},
	}}}
}

// functionResponse reads the observation as the page's answer to a function
// the run asked it to run.
func functionResponse(observation any) (value map[string]any, ok bool) {
	m, isMap := observation.(map[string]any)
	if !isMap {
		return nil, false
	}
	r, isMap := m["ui_function_response"].(map[string]any)
	if !isMap {
		return nil, false
	}
	value, _ = r["value"].(map[string]any)
	return value, true
}

// action reads the observation of a step as a ui_action: the user pressed a
// button of the interface. ok is false for anything else (the first message,
// say), which the script treats as "show the interface".
func action(observation any) (name string, context map[string]any, ok bool) {
	m, isMap := observation.(map[string]any)
	if !isMap {
		return "", nil, false
	}
	a, isMap := m["ui_action"].(map[string]any)
	if !isMap {
		return "", nil, false
	}
	name, _ = a["name"].(string)
	context, _ = a["context"].(map[string]any)
	return name, context, name != ""
}

func askUser(patch obj, question string, ui []any) string {
	return mustJSON(obj{"state_patch": patch, "action": obj{"type": "ask_user", "payload": obj{"question": question, "ui": ui}}})
}

func gotoState(patch obj, state string) string {
	return mustJSON(obj{"state_patch": patch, "action": obj{"type": "goto", "state": state}})
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // the script's own values always encode
	}
	return string(b)
}

// uiDemoReply is the scripted answer for one step of ui-demo.
func uiDemoReply(state string, observation any, runState map[string]any) (string, error) {
	name, context, pressed := action(observation)
	switch state {
	case "tour":
		if pressed && name == "continue" {
			return gotoState(obj{"tour_done": true}, "device"), nil
		}
		return askUser(obj{"tour_shown": true}, "Here is every component of the toolkit. Try them, then press Continue.", tour()), nil
	case "device":
		// No words: the call is not a question for the user. The answer, or the
		// timeout the assistant makes of a missing one, is the next observation.
		if value, answered := functionResponse(observation); answered {
			return gotoState(obj{"viewport": value}, "form"), nil
		}
		return mustJSON(obj{"state_patch": obj{"device_asked": true}, "action": obj{"type": "ask_user", "payload": obj{"ui": viewportCall()}}}), nil
	case "form":
		if pressed && name == "submit_form" {
			return gotoState(obj{"answers": context, "summary": summarise(context)}, "done"), nil
		}
		viewport, _ := runState["viewport"].(map[string]any)
		return askUser(obj{"form_shown": true}, "Fill in the form and press Submit.", form(viewport)), nil
	}
	return "", fmt.Errorf("the demo model has no answer for state %q of ui-demo", state)
}

func summarise(answers map[string]any) string {
	who, _ := answers["name"].(string)
	var topics []string
	if list, ok := answers["topics"].([]any); ok {
		for _, t := range list {
			if s, ok := t.(string); ok {
				topics = append(topics, s)
			}
		}
	}
	sort.Strings(topics)
	interest := "no topics"
	if len(topics) > 0 {
		interest = strings.Join(topics, ", ")
	}
	return fmt.Sprintf("Thank you, %s. You chose %s, at level %v. (from the demo model)", who, interest, answers["level"])
}
