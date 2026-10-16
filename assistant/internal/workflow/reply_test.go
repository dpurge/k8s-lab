package workflow

import (
	"errors"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func parse(t *testing.T, state, text string) (*Reply, error) {
	t.Helper()
	_, wf, reg := requestFixtures(t)
	return ParseReply(wf, state, reg, text)
}

func mustRefuse(t *testing.T, state, text, wantInMessage string) {
	t.Helper()
	_, err := parse(t, state, text)
	var rerr *ReplyError
	if !errors.As(err, &rerr) {
		t.Fatalf("reply %s: want *ReplyError, got %T %v", text, err, err)
	}
	if !strings.Contains(rerr.Message, wantInMessage) {
		t.Errorf("reply %s:\nmessage %q\nwant it to contain %q", text, rerr.Message, wantInMessage)
	}
}

func TestParseReplyToolAction(t *testing.T) {
	r, err := parse(t, "teach", `{"state_patch":{"lessons_done":1},"action":{"type":"tool","tool":"rag_search","args":{"query":"verbs"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Action.Type != ActionTool || r.Action.Tool != "rag_search" || r.Action.Args["query"] != "verbs" {
		t.Errorf("action = %+v", r.Action)
	}
	if asJSON(t, r.StatePatch) != `{"lessons_done":1}` {
		t.Errorf("patch = %s", asJSON(t, r.StatePatch))
	}
}

func TestParseReplyGotoAction(t *testing.T) {
	r, err := parse(t, "teach", `{"state_patch":{},"action":{"type":"goto","state":"done"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Action.Type != ActionGoto || r.Action.State != "done" || len(r.StatePatch) != 0 {
		t.Errorf("reply = %+v", r)
	}
}

func TestParseReplyAskUserAction(t *testing.T) {
	r, err := parse(t, "quiet", `{"state_patch":{"x":null},"action":{"type":"ask_user","payload":{"text":"Ready?"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Action.Type != ActionAskUser || r.Action.Payload["text"] != "Ready?" {
		t.Errorf("action = %+v", r.Action)
	}
	if v, present := r.StatePatch["x"]; !present || v != nil {
		t.Errorf("a null in the patch must survive parsing so merge can delete the key: %v", r.StatePatch)
	}
}

func TestParseReplyAcceptsCodeFence(t *testing.T) {
	body := `{"state_patch":{},"action":{"type":"goto","state":"done"}}`
	for _, text := range []string{
		"```json\n" + body + "\n```",
		"```\n" + body + "\n```",
		"  ```json\n" + body + "\n```  \n",
		"```" + body + "```",
	} {
		if _, err := parse(t, "teach", text); err != nil {
			t.Errorf("fenced reply %q refused: %v", text, err)
		}
	}
}

func TestParseReplyRefusals(t *testing.T) {
	tests := []struct {
		name, state, reply, want string
	}{
		{"not JSON", "teach", `I think we should go to done`, "not a valid JSON object"},
		{"prose around JSON", "teach", `Sure! {"state_patch":{},"action":{"type":"goto","state":"done"}}`, "not a valid JSON object"},
		{"empty reply", "teach", ``, "not a valid JSON object"},
		{"array", "teach", `[]`, "JSON object"},
		{"no state_patch", "teach", `{"action":{"type":"goto","state":"done"}}`, `missing the key "state_patch"`},
		{"no action", "teach", `{"state_patch":{}}`, `missing the key "action"`},
		{"extra key", "teach", `{"state_patch":{},"action":{"type":"goto","state":"done"},"why":"x"}`, `unknown keys: "why"`},
		{"patch is an array", "teach", `{"state_patch":[],"action":{"type":"goto","state":"done"}}`, "state_patch must be a JSON object, got an array"},
		{"patch is null", "teach", `{"state_patch":null,"action":{"type":"goto","state":"done"}}`, "state_patch must be a JSON object, got null"},
		{"action is a string", "teach", `{"state_patch":{},"action":"goto done"}`, "action must be a JSON object, got a string"},
		{"no action type", "teach", `{"state_patch":{},"action":{"state":"done"}}`, `needs a "type"`},
		{"action type not a string", "teach", `{"state_patch":{},"action":{"type":3}}`, `needs a "type"`},
		{"unknown action type", "teach", `{"state_patch":{},"action":{"type":"dance"}}`, `unknown action type "dance"`},
		{"tool not on the allowlist", "teach", `{"state_patch":{},"action":{"type":"tool","tool":"web_search","args":{}}}`, `tool "web_search" is not available in state "teach"; available tools: rag_search`},
		{"tool in a state with no tools", "quiet", `{"state_patch":{},"action":{"type":"tool","tool":"rag_search","args":{}}}`, `state "quiet" has no tools`},
		{"tool name not a string", "teach", `{"state_patch":{},"action":{"type":"tool","tool":5,"args":{}}}`, "tool must be a string"},
		{"tool without args", "teach", `{"state_patch":{},"action":{"type":"tool","tool":"rag_search"}}`, `missing the key "args"`},
		{"args not an object", "teach", `{"state_patch":{},"action":{"type":"tool","tool":"rag_search","args":"verbs"}}`, "must be a JSON object, got a string"},
		{"args break the schema", "teach", `{"state_patch":{},"action":{"type":"tool","tool":"rag_search","args":{"query":5}}}`, `arguments for tool "rag_search" are invalid: args.query`},
		{"required arg missing", "teach", `{"state_patch":{},"action":{"type":"tool","tool":"rag_search","args":{}}}`, `args is missing required "query"`},
		{"unknown arg", "teach", `{"state_patch":{},"action":{"type":"tool","tool":"rag_search","args":{"query":"x","k":3}}}`, `args has unknown "k"`},
		{"extra key in a tool action", "teach", `{"state_patch":{},"action":{"type":"tool","tool":"rag_search","args":{"query":"x"},"why":"y"}}`, `unknown keys: "why"`},
		{"goto not allowed", "teach", `{"state_patch":{},"action":{"type":"goto","state":"quiet"}}`, `cannot go to "quiet" from state "teach"; allowed next states: teach, done`},
		{"goto from a state with no next", "quiet", `{"state_patch":{},"action":{"type":"goto","state":"done"}}`, `state "quiet" has no next states`},
		{"goto without a state", "teach", `{"state_patch":{},"action":{"type":"goto"}}`, `missing the key "state"`},
		{"goto state not a string", "teach", `{"state_patch":{},"action":{"type":"goto","state":["done"]}}`, "goto state must be a string"},
		{"ask_user without payload", "teach", `{"state_patch":{},"action":{"type":"ask_user"}}`, `missing the key "payload"`},
		{"ask_user payload not an object", "teach", `{"state_patch":{},"action":{"type":"ask_user","payload":"hi"}}`, "payload must be a JSON object, got a string"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { mustRefuse(t, tc.state, tc.reply, tc.want) })
	}
}

func TestParseReplyUnknownStateIsNotAReplyError(t *testing.T) {
	_, err := parse(t, "ghost", `{}`)
	var rerr *ReplyError
	if err == nil || errors.As(err, &rerr) {
		t.Errorf("an unknown state is the engine's bug, not the model's: %v", err)
	}
}

func TestParseReplyUnregisteredToolIsNotAReplyError(t *testing.T) {
	_, wf, _ := requestFixtures(t)
	empty, _ := NewRegistry()
	_, err := ParseReply(wf, "teach", empty, `{"state_patch":{},"action":{"type":"tool","tool":"rag_search","args":{"query":"x"}}}`)
	var rerr *ReplyError
	if err == nil || errors.As(err, &rerr) {
		t.Errorf("a missing registration is the engine's problem, not the model's: %v", err)
	}
}

// The engine's own checks and the schema handed to the model must agree, or
// constrained decoding would allow replies the engine then refuses (or the
// other way round).
func TestParseReplyAgreesWithReplySchema(t *testing.T) {
	_, wf, reg := requestFixtures(t)
	replies := []string{
		`{"state_patch":{},"action":{"type":"tool","tool":"rag_search","args":{"query":"verbs"}}}`,
		`{"state_patch":{},"action":{"type":"tool","tool":"rag_search","args":{"query":""}}}`,
		`{"state_patch":{},"action":{"type":"tool","tool":"rag_search","args":{"query":5}}}`,
		`{"state_patch":{},"action":{"type":"tool","tool":"rag_search","args":{}}}`,
		`{"state_patch":{},"action":{"type":"tool","tool":"web_search","args":{}}}`,
		`{"state_patch":{},"action":{"type":"goto","state":"done"}}`,
		`{"state_patch":{},"action":{"type":"goto","state":"teach"}}`,
		`{"state_patch":{},"action":{"type":"goto","state":"quiet"}}`,
		`{"state_patch":{},"action":{"type":"ask_user","payload":{}}}`,
		`{"state_patch":{},"action":{"type":"ask_user","payload":[]}}`,
		`{"state_patch":{},"action":{"type":"dance"}}`,
		`{"state_patch":[],"action":{"type":"goto","state":"done"}}`,
		`{"state_patch":{},"action":{"type":"goto","state":"done"},"extra":1}`,
		`{"state_patch":{}}`,
	}
	for _, state := range []string{"teach", "quiet"} {
		schema, err := ReplySchema(wf, state, reg)
		if err != nil {
			t.Fatal(err)
		}
		compiled := compileReplySchema(t, schema)
		for _, reply := range replies {
			_, parseErr := ParseReply(wf, state, reg, reply)
			var schemaErr error
			if v, err := jsonschema.UnmarshalJSON(strings.NewReader(reply)); err == nil {
				schemaErr = compiled.Validate(v)
			}
			if (parseErr == nil) != (schemaErr == nil) {
				t.Errorf("state %s, reply %s: ParseReply err=%v, schema err=%v", state, reply, parseErr, schemaErr)
			}
		}
	}
}

func TestStripCodeFence(t *testing.T) {
	tests := map[string]string{
		`{"a":1}`:                   `{"a":1}`,
		"```json\n{\"a\":1}\n```":   `{"a":1}`,
		"```\n{\"a\":1}\n```":       `{"a":1}`,
		"```{\"a\":1}```":           `{"a":1}`,
		"```":                       "```",
		"``````":                    "",
		"text ```json\n{}\n```":     "text ```json\n{}\n```",
		"```json\n{}\n``` trailing": "```json\n{}\n``` trailing",
	}
	for in, want := range tests {
		if got := stripCodeFence(in); got != want {
			t.Errorf("stripCodeFence(%q) = %q, want %q", in, got, want)
		}
	}
}
