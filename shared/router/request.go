package router

import (
	"fmt"
	"strings"
	"time"
)

// Message roles.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message is one turn of a conversation. Role is RoleSystem, RoleUser or
// RoleAssistant.
type Message struct {
	Role    string
	Content string
}

// Request is what a caller sends the model for one step.
type Request struct {
	// System is the rendered system prompt.
	System string
	// Input is the step input as JSON: the workflow, the current state, the
	// run state, the latest observation. It never holds earlier steps.
	Input string
	// Messages is a multi-turn conversation, for a caller that corrects the
	// model by replaying its earlier replies. When it is not empty it is the
	// whole conversation and Input is ignored; System, if set, is still
	// honoured. System-role messages are merged into the system prompt. The
	// router does not repair a malformed history: see Validate.
	Messages []Message
	// Schema is the JSON Schema of the reply the engine will accept in this
	// state. A client may use it for constrained decoding.
	Schema map[string]any
	// Tags are what the step needs from a model; a router sends the call to a
	// model that has all of them. Empty means any model.
	Tags []string

	// The four fields below override, for this call, the model's configured
	// options. The zero value of each means "use the model's configured
	// option", so a caller that sets none of them gets the configured behavior.

	// NumCtx is the context window to ask Ollama for in this call, in tokens. A
	// caller that sizes its input to a window sends that window here; the
	// model's options.num_ctx is then the most the model supports, not what is
	// sent. Zero or less leaves the model's num_ctx. Providers other than
	// ollama ignore it.
	NumCtx int

	// MaxTokens caps the reply, in tokens. It replaces the model's num_predict;
	// zero or less leaves the model's value.
	MaxTokens int
	// Think turns Ollama's hidden reasoning on or off for this call; nil leaves
	// the model's think option. Providers other than ollama ignore it.
	Think *bool
	// Timeout bounds the whole call. It replaces the model's
	// overall_timeout_seconds; zero or less leaves the model's value. It is an
	// absolute duration, not a count of the provider's timeout unit.
	Timeout time.Duration
}

// Response is the model's reply with the tokens it cost.
type Response struct {
	Text         string
	InputTokens  int
	OutputTokens int
	// ModelID is the configured id of the model that answered, set by a
	// router; empty from a client that has no such notion.
	ModelID string
}

// Conversation returns the system prompt and the turns to send. The system
// prompt is System followed by the content of every system-role message, empty
// parts skipped and the rest joined by a blank line. The turns are the
// non-system messages in order, or, when Messages is empty, the single user
// turn Input (even if it is empty).
func (r Request) Conversation() (system string, turns []Message) {
	if len(r.Messages) == 0 {
		return r.System, []Message{{Role: RoleUser, Content: r.Input}}
	}
	var parts []string
	if r.System != "" {
		parts = append(parts, r.System)
	}
	for _, m := range r.Messages {
		if m.Role == RoleSystem {
			if m.Content != "" {
				parts = append(parts, m.Content)
			}
			continue
		}
		turns = append(turns, m)
	}
	return strings.Join(parts, "\n\n"), turns
}

// Validate says whether the conversation can be sent: every role is known and
// the turns, system messages aside, are not empty and alternate user,
// assistant, user, ... starting with a user turn (Anthropic requires it). The
// router does not repair a history that breaks this; a provider refuses it as a
// client failure. The error never contains message text.
func (r Request) Validate() error {
	for i, m := range r.Messages {
		switch m.Role {
		case RoleSystem, RoleUser, RoleAssistant:
		default:
			return fmt.Errorf("message %d has role %q; it must be system, user or assistant", i, m.Role)
		}
	}
	_, turns := r.Conversation()
	if len(turns) == 0 {
		return fmt.Errorf("the conversation has no user or assistant turns")
	}
	for i, t := range turns {
		want := RoleUser
		if i%2 == 1 {
			want = RoleAssistant
		}
		if t.Role != want {
			return fmt.Errorf("turn %d has role %q; turns must alternate user, assistant starting with user", i, t.Role)
		}
	}
	return nil
}
