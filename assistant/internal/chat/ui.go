package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"assistant/internal/a2ui"
)

// Errors of the page's events.
var (
	// ErrNoInterfaces is returned when the service has no A2UI validator.
	ErrNoInterfaces = errors.New("this assistant does not show interfaces")
	// ErrUnsupportedEvent is returned for an event kind that is not handled yet.
	ErrUnsupportedEvent = errors.New("this kind of event is not handled yet")
)

// surfaces rebuilds what a conversation has drawn, by applying its stored
// interface messages in order.
func (s *Service) surfaces(ctx context.Context, conversationID string) (*a2ui.Surfaces, error) {
	surfaces := a2ui.NewSurfaces(s.UI)
	msgs, err := s.Store.Messages(ctx, conversationID, 0)
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		if m.Kind != KindUI {
			continue
		}
		var list []json.RawMessage
		if json.Unmarshal(m.UI, &list) != nil {
			continue
		}
		for _, raw := range list {
			if _, err := surfaces.Apply(raw); err != nil {
				// A stored message passed when it was stored; if the rules grew
				// stricter since, the history is still shown as it was.
				s.Log.Warn("a stored interface message no longer passes", "conversation_id", conversationID, "message_id", m.ID, "error", err)
			}
		}
		_ = surfaces.Finish()
	}
	return surfaces, nil
}

// postInterface checks interface messages against the conversation's surfaces
// and, when they pass, stores them as one interface message and delivers it.
// When they do not, the user is told in the conversation, with the reason, and
// nothing of the interface is stored.
func (s *Service) postInterface(ctx context.Context, c Conversation, taskID *string, list []json.RawMessage) {
	if s.UI == nil {
		s.post(ctx, c, KindError, "⚠ The workflow showed an interface, but this assistant cannot draw interfaces.", taskID)
		return
	}
	refuse := func(err error) {
		if s.Rejections != nil {
			s.Rejections.A2UIRejected("interface")
		}
		s.Log.Warn("an interface was refused", "conversation_id", c.ID, "error", err)
		s.post(ctx, c, KindError, "⚠ The interface was refused: "+err.Error(), taskID)
	}
	surfaces, err := s.surfaces(ctx, c.ID)
	if err != nil {
		refuse(err)
		return
	}
	list, calls, err := freshCallIDs(list)
	if err != nil {
		refuse(err)
		return
	}
	if list, err = uniqueSurfaces(list, surfaces); err != nil {
		refuse(err)
		return
	}
	for _, raw := range list {
		if _, err := surfaces.Apply(raw); err != nil {
			refuse(err)
			return
		}
	}
	if err := surfaces.Finish(); err != nil {
		refuse(err)
		return
	}
	payload, err := json.Marshal(list)
	if err != nil {
		refuse(err)
		return
	}
	msg, err := s.Store.AddUIMessage(ctx, c.ID, RoleAssistant, KindUI, "", payload, taskID)
	if err != nil {
		s.Log.Error("could not store the interface", "conversation_id", c.ID, "error", err)
		if s.Errors != nil {
			s.Errors.ChatNotifyError()
		}
		return
	}
	if s.Channel != nil {
		if err := s.Channel.Deliver(ctx, c, msg); err != nil {
			s.Log.Error("could not deliver the interface", "conversation_id", c.ID, "error", err)
			if s.Errors != nil {
				s.Errors.ChatNotifyError()
			}
		}
	}
	for _, id := range calls {
		s.expireLater(c.ID, id)
	}
}

// uniqueSurfaces renames a surface the list creates when the conversation
// already used its id: a model writes "main" every time, and an id is used once
// for the life of a conversation, so the second "main" becomes "main-1a2b3c4d" in
// every message of the list that names it.
func uniqueSurfaces(list []json.RawMessage, surfaces *a2ui.Surfaces) ([]json.RawMessage, error) {
	kinds := []string{"createSurface", "updateComponents", "updateDataModel", "deleteSurface", "callRendererFunction"}
	renames := map[string]string{}
	parsed := make([]map[string]any, len(list))
	for i, raw := range list {
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			return list, nil // not our business: the validator reports it
		}
		parsed[i] = m
		if body, ok := m["createSurface"].(map[string]any); ok {
			if id, ok := body["surfaceId"].(string); ok && surfaces.Used(id) {
				renames[id] = id + "-" + newID()[:8]
			}
		}
	}
	if len(renames) == 0 {
		return list, nil
	}
	out := make([]json.RawMessage, len(list))
	for i, m := range parsed {
		for _, kind := range kinds {
			if body, ok := m[kind].(map[string]any); ok {
				if id, ok := body["surfaceId"].(string); ok && renames[id] != "" {
					body["surfaceId"] = renames[id]
				}
			}
		}
		b, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

// MaxRendererCallsPerInterface bounds how many functions one interface message
// may ask the page to run.
const MaxRendererCallsPerInterface = 5

// freshCallIDs gives every callRendererFunction of the list a new unique id: an
// id is the engine's to assign, so that an answer can only match a call the
// engine really made. It returns the list and the new ids.
func freshCallIDs(list []json.RawMessage) ([]json.RawMessage, []string, error) {
	var ids []string
	out := make([]json.RawMessage, len(list))
	for i, raw := range list {
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			out[i] = raw
			continue
		}
		call, ok := m["callRendererFunction"].(map[string]any)
		if !ok {
			out[i] = raw
			continue
		}
		id := "call-" + newID()
		call["functionCallId"] = id
		ids = append(ids, id)
		if len(ids) > MaxRendererCallsPerInterface {
			return nil, nil, fmt.Errorf("an interface may ask the page to run at most %d functions", MaxRendererCallsPerInterface)
		}
		changed, err := json.Marshal(m)
		if err != nil {
			return nil, nil, err
		}
		out[i] = changed
	}
	return out, ids, nil
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// askParts splits what a waiting run asked into the words and the interface
// messages it carries under "ui".
func askParts(ask json.RawMessage) (text string, ui []json.RawMessage) {
	var payload map[string]json.RawMessage
	if json.Unmarshal(ask, &payload) != nil {
		return questionText(ask), nil
	}
	// The engine's note on where the request went is not part of the question.
	if _, routed := payload["routing"]; routed {
		delete(payload, "routing")
		if len(payload) == 0 {
			return "", nil
		}
		rest, _ := json.Marshal(payload)
		return askParts(rest)
	}
	if payload["ui"] == nil {
		return questionText(ask), nil
	}
	if json.Unmarshal(payload["ui"], &ui) != nil {
		return questionText(ask), nil
	}
	rest := make(map[string]json.RawMessage, len(payload))
	for k, v := range payload {
		if k != "ui" {
			rest[k] = v
		}
	}
	// Words only if the run said some: an interface alone is not a question.
	if len(rest) == 0 {
		return "", ui
	}
	raw, _ := json.Marshal(rest)
	return questionText(raw), ui
}

// Event handles what the page sends back about an interface: the user's action
// becomes their turn, handed to the run; an error the page reports is recorded.
// The message is checked against the protocol, and an action must come from a
// component of a surface this conversation drew. It returns the stored message.
func (s *Service) Event(ctx context.Context, conversationID string, raw json.RawMessage) (Message, error) {
	msg, err := s.event(ctx, conversationID, raw)
	var invalid *a2ui.ValidationError
	if errors.As(err, &invalid) && s.Rejections != nil {
		s.Rejections.A2UIRejected("event")
	}
	return msg, err
}

func (s *Service) event(ctx context.Context, conversationID string, raw json.RawMessage) (Message, error) {
	if s.UI == nil {
		return Message{}, ErrNoInterfaces
	}
	v, err := s.Store.Get(ctx, conversationID)
	if err != nil {
		return Message{}, err
	}
	e, err := s.UI.CheckEvent(raw)
	if err != nil {
		return Message{}, err
	}
	switch e.Kind {
	case "action":
		surfaces, err := s.surfaces(ctx, v.ID)
		if err != nil {
			return Message{}, err
		}
		if err := surfaces.CheckAction(e); err != nil {
			return Message{}, err
		}
		current, err := s.currentRun(ctx, v)
		if err != nil {
			return Message{}, err
		}
		text := strings.TrimSpace(e.UserMessage)
		if text == "" {
			text = "(" + e.Name + ")"
		}
		msg, err := s.Store.AddUIMessage(ctx, v.ID, RoleUser, KindUIAction, text, raw, nil)
		if err != nil {
			return Message{}, err
		}
		return msg, s.advance(ctx, v, current, map[string]any{"ui_action": e.Body})
	case "error":
		code, _ := e.Body["code"].(string)
		message, _ := e.Body["message"].(string)
		s.Log.Warn("the page reported an interface error", "conversation_id", v.ID, "code", code, "message", message)
		return s.Store.AddUIMessage(ctx, v.ID, RoleUser, KindUIAction, fmt.Sprintf("⚠ The page could not draw part of the interface (%s): %s", code, message), raw, nil)
	case "callAgentFunction":
		return s.agentFunctionCall(ctx, v, e, raw)
	case "rendererFunctionResponse":
		return s.rendererFunctionAnswer(ctx, v, e, raw)
	}
	return Message{}, ErrUnsupportedEvent
}
