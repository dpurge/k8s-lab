package router

import (
	"reflect"
	"strings"
	"testing"
)

func TestConversationOfAnInputOnlyRequestIsOneUserTurn(t *testing.T) {
	system, turns := Request{System: "SYS", Input: "in"}.Conversation()
	if system != "SYS" || !reflect.DeepEqual(turns, []Message{{Role: RoleUser, Content: "in"}}) {
		t.Errorf("system %q, turns %v", system, turns)
	}
	// An empty input is still the single turn, as it always was.
	system, turns = Request{}.Conversation()
	if system != "" || !reflect.DeepEqual(turns, []Message{{Role: RoleUser}}) {
		t.Errorf("system %q, turns %v", system, turns)
	}
}

func TestMessagesReplaceInputAndSystemRoleMessagesJoinTheSystemPrompt(t *testing.T) {
	req := Request{
		System: "SYS",
		Input:  "ignored",
		Messages: []Message{
			{Role: RoleSystem, Content: "S2"},
			{Role: RoleUser, Content: "u1"},
			{Role: RoleSystem, Content: ""},
			{Role: RoleAssistant, Content: "a1"},
			{Role: RoleUser, Content: "u2"},
			{Role: RoleSystem, Content: "S3"},
		},
	}
	system, turns := req.Conversation()
	if system != "SYS\n\nS2\n\nS3" {
		t.Errorf("system = %q", system)
	}
	want := []Message{{RoleUser, "u1"}, {RoleAssistant, "a1"}, {RoleUser, "u2"}}
	if !reflect.DeepEqual(turns, want) {
		t.Errorf("turns = %v", turns)
	}
	if err := req.Validate(); err != nil {
		t.Errorf("valid history refused: %v", err)
	}
}

func TestSystemRoleMessagesAloneGiveNoSystemSeparator(t *testing.T) {
	system, _ := Request{Messages: []Message{{RoleSystem, "S"}, {RoleUser, "u"}}}.Conversation()
	if system != "S" {
		t.Errorf("system = %q", system)
	}
}

func TestValidate(t *testing.T) {
	u, a := Message{RoleUser, "SECRET-u"}, Message{RoleAssistant, "SECRET-a"}
	tests := []struct {
		name string
		req  Request
		ok   bool
	}{
		{"input only", Request{Input: "x"}, true},
		{"empty request", Request{}, true},
		{"single user", Request{Messages: []Message{u}}, true},
		{"user assistant user", Request{Messages: []Message{u, a, u}}, true},
		{"ends with assistant", Request{Messages: []Message{u, a}}, true},
		{"starts with assistant", Request{Messages: []Message{a, u}}, false},
		{"two users in a row", Request{Messages: []Message{u, u}}, false},
		{"two assistants in a row", Request{Messages: []Message{u, a, a}}, false},
		{"only system messages", Request{Messages: []Message{{RoleSystem, "SECRET-s"}}}, false},
		{"unknown role", Request{Messages: []Message{{"tool", "SECRET-t"}}}, false},
	}
	for _, tc := range tests {
		err := tc.req.Validate()
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
		if err != nil && strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: the error carries message text: %v", tc.name, err)
		}
	}
}
