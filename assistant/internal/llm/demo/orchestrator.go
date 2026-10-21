package demo

import (
	"fmt"
	"strings"
)

// The scripted orchestrator: it routes by keywords instead of by understanding,
// which is enough to try routing, delegation and the routing line without a
// model. "hello", "greet" or "name" go to the hello workflow; "form",
// "interface", "component" or "toolkit" to ui-demo; an empty message or "help"
// gets a question; anything else is answered by the orchestrator itself.

var keywordRoutes = []struct {
	workflow string
	words    []string
}{
	{"hello", []string{"hello", "greet", "name"}},
	{"ui-demo", []string{"form", "interface", "component", "toolkit"}},
}

func orchestratorReply(state string, observation any, offered []string) (string, error) {
	if state != "route" {
		return "", fmt.Errorf("the demo model has no answer for state %q of orchestrator", state)
	}
	if m, ok := observation.(map[string]any); ok {
		if r, ok := m["delegate_result"].(map[string]any); ok {
			return gotoState(obj{"reply": fmt.Sprintf("The %v workflow finished. (from the demo model)", r["workflow"])}, "done"), nil
		}
		if e, ok := m["delegate_error"].(map[string]any); ok {
			return gotoState(obj{"reply": fmt.Sprintf("Sorry, the %v workflow did not work out: %v (from the demo model)", e["workflow"], e["message"])}, "done"), nil
		}
	}
	text, _ := observation.(string)
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" || lower == "help" || lower == "?" {
		return mustJSON(obj{"state_patch": obj{}, "action": obj{"type": "ask_user", "payload": obj{
			"question": "What would you like to do? I can hand you to: " + strings.Join(offered, ", ") + ". (from the demo model)",
		}}}), nil
	}
	for _, route := range keywordRoutes {
		for _, word := range route.words {
			if strings.Contains(lower, word) && contains(offered, route.workflow) {
				return mustJSON(obj{"state_patch": obj{}, "action": obj{
					"type": "delegate", "workflow": route.workflow, "message": text, "reason": "you mentioned \"" + word + "\"",
				}}), nil
			}
		}
	}
	return gotoState(obj{"reply": "I have no workflow for that. I can hand you to: " + strings.Join(offered, ", ") + ". (from the demo model)"}, "done"), nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
