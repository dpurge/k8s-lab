package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"assistant/internal/a2ui"
	"assistant/internal/tasks"
)

// Function calls between the page and the assistant, in both directions.
//
// The page calls the assistant (callAgentFunction): the call is stored, checked
// against the assistant's registry (listWorkflows) and answered in the
// conversation with an agentFunctionResponse as an interface message.
//
// A run calls the page: an interface it shows may hold a callRendererFunction
// (viewport, say). The service gives it a fresh id, the page runs it and answers
// with a rendererFunctionResponse, and the answer reaches the run as its next
// message, {"ui_function_response": ...}. An answer that does not come in time
// reaches the run as an error response with code TIMEOUT.

const (
	// MaxAgentCallsPerTurn bounds the calls the page may make to the assistant
	// between two messages of the user.
	MaxAgentCallsPerTurn = 10
	defaultCallTimeout   = 15 * time.Second
)

// isFunctionTraffic says whether a stored ui_action is a function call or an
// answer, not something the user did.
func isFunctionTraffic(m Message) (kind, id string, ok bool) {
	if m.Kind != KindUIAction {
		return "", "", false
	}
	var body map[string]any
	if json.Unmarshal(m.UI, &body) != nil {
		return "", "", false
	}
	for _, k := range []string{"callAgentFunction", "rendererFunctionResponse"} {
		if b, found := body[k].(map[string]any); found {
			id, _ := b["functionCallId"].(string)
			return k, id, true
		}
	}
	return "", "", false
}

// agentFunctionCall handles the page calling a function of the assistant.
func (s *Service) agentFunctionCall(ctx context.Context, v View, e *a2ui.Event, raw json.RawMessage) (Message, error) {
	surfaces, err := s.surfaces(ctx, v.ID)
	if err != nil {
		return Message{}, err
	}
	if !surfaces.Has(e.SurfaceID) {
		return Message{}, &a2ui.ValidationError{Problems: []a2ui.Problem{{Path: "/callAgentFunction/surfaceId", Message: fmt.Sprintf("there is no surface %q in this conversation", e.SurfaceID)}}}
	}
	id, _ := e.Body["functionCallId"].(string)
	msgs, err := s.Store.Messages(ctx, v.ID, 0)
	if err != nil {
		return Message{}, err
	}
	// A call id is used once for the life of the conversation; the calls of the
	// current turn (since the user last did something) are counted against the
	// limit.
	turn, counting := 0, true
	for i := len(msgs) - 1; i >= 0; i-- {
		kind, callID, traffic := isFunctionTraffic(msgs[i])
		if traffic && kind == "callAgentFunction" {
			if callID == id {
				return Message{}, &a2ui.ValidationError{Problems: []a2ui.Problem{{Path: "/callAgentFunction/functionCallId", Message: fmt.Sprintf("the call id %q was already used; every call needs a new one", id)}}}
			}
			if counting {
				turn++
			}
			continue
		}
		if msgs[i].Role == RoleUser && !traffic {
			counting = false
		}
	}
	if turn >= MaxAgentCallsPerTurn {
		return Message{}, &a2ui.ValidationError{Problems: []a2ui.Problem{{Path: "/callAgentFunction", Message: fmt.Sprintf("at most %d calls to the assistant between two messages of yours", MaxAgentCallsPerTurn)}}}
	}

	call, _ := e.Body["callFunction"].(map[string]any)
	stored, err := s.Store.AddUIMessage(ctx, v.ID, RoleUser, KindUIAction, "(call "+fmt.Sprint(call["@call"])+")", raw, nil)
	if err != nil {
		return Message{}, err
	}
	response := map[string]any{"functionCallId": id}
	if value, ferr := s.runAgentFunction(ctx, call); ferr != nil {
		response["error"] = map[string]any{"code": ferr.code, "message": ferr.message}
	} else {
		response["value"] = value
	}
	one, err := json.Marshal(map[string]any{"version": "v1.0", "agentFunctionResponse": response})
	if err != nil {
		return stored, err
	}
	if _, err := s.UI.Check(one); err != nil {
		return stored, fmt.Errorf("the assistant's own answer is not valid A2UI: %w", err)
	}
	if _, err := s.Store.AddUIMessage(ctx, v.ID, RoleAssistant, KindUI, "", append(append([]byte("["), one...), ']'), nil); err != nil {
		return stored, err
	}
	return stored, nil
}

type functionError struct{ code, message string }

// runAgentFunction runs one of the assistant's functions for the page. What
// the page may call is this registry; the catalog says the same in
// schemas/a2ui/assistant_catalog.json.
func (s *Service) runAgentFunction(_ context.Context, call map[string]any) (any, *functionError) {
	name, _ := call["@call"].(string)
	catalog, _ := call["catalogId"].(string)
	if catalog != a2ui.AssistantCatalogID {
		return nil, &functionError{"INVALID_FUNCTION_CALL", fmt.Sprintf("the assistant has no catalog %q; its functions are in %s", catalog, a2ui.AssistantCatalogID)}
	}
	switch name {
	case "listWorkflows":
		return s.workflowList(), nil
	}
	return nil, &functionError{"FUNCTION_NOT_FOUND", fmt.Sprintf("the assistant has no function %q the page may call", name)}
}

// workflowList is what a user can start in a conversation, one entry per
// workflow, as the catalog says (a string).
func (s *Service) workflowList() string {
	infos := s.Catalog.Catalog()
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	var parts []string
	for _, info := range infos {
		if info.Kind == "yaml" || info.ID == "ask" {
			parts = append(parts, info.ID+": "+strings.TrimSpace(info.Description))
		}
	}
	return strings.Join(parts, "\n")
}

// pendingCall finds a call the run made to the page that is not answered yet.
func pendingCall(msgs []Message, id string) (found, answered bool) {
	for _, m := range msgs {
		switch m.Kind {
		case KindUI:
			var list []map[string]any
			if json.Unmarshal(m.UI, &list) != nil {
				continue
			}
			for _, item := range list {
				if call, ok := item["callRendererFunction"].(map[string]any); ok && call["functionCallId"] == id {
					found = true
				}
			}
		case KindUIAction:
			if kind, callID, ok := isFunctionTraffic(m); ok && kind == "rendererFunctionResponse" && callID == id {
				answered = true
			}
		}
	}
	return found, answered
}

// rendererFunctionAnswer handles the page's answer to a function a run asked
// it to run: it must match a call that is still waiting, and it becomes the
// run's next message.
func (s *Service) rendererFunctionAnswer(ctx context.Context, v View, e *a2ui.Event, raw json.RawMessage) (Message, error) {
	id, _ := e.Body["functionCallId"].(string)
	s.callMu.Lock()
	defer s.callMu.Unlock()
	msgs, err := s.Store.Messages(ctx, v.ID, 0)
	if err != nil {
		return Message{}, err
	}
	found, answered := pendingCall(msgs, id)
	if !found || answered {
		why := "no call of the assistant is waiting for this answer"
		if answered {
			why = "this call was already answered (or timed out)"
		}
		return Message{}, &a2ui.ValidationError{Problems: []a2ui.Problem{{Path: "/rendererFunctionResponse/functionCallId", Message: fmt.Sprintf("%s: %q", why, id)}}}
	}
	return s.recordAnswer(ctx, v, id, raw, e.Body)
}

// recordAnswer stores an answer to a call and gives it to the run if the run
// is waiting for it. callMu is held.
func (s *Service) recordAnswer(ctx context.Context, v View, id string, raw json.RawMessage, body map[string]any) (Message, error) {
	stored, err := s.Store.AddUIMessage(ctx, v.ID, RoleUser, KindUIAction, "(answer to "+id+")", raw, nil)
	if err != nil {
		return Message{}, err
	}
	view, err := s.Store.Get(ctx, v.ID)
	if err != nil {
		return stored, err
	}
	current, err := s.currentRun(ctx, view)
	if err != nil || current == nil || current.Status != tasks.StatusWaiting {
		return stored, nil
	}
	return stored, s.advance(ctx, view, current, map[string]any{"ui_function_response": body})
}

func (s *Service) callTimeout() time.Duration {
	if s.CallTimeout > 0 {
		return s.CallTimeout
	}
	return defaultCallTimeout
}

// expireLater answers a call with a timeout if the page has not answered it
// by then. The timer lives in this process: after a restart a call that was
// waiting is not timed out, and the run waits until the user writes.
func (s *Service) expireLater(conversationID, id string) {
	time.AfterFunc(s.callTimeout(), func() { s.expire(context.Background(), conversationID, id) })
}

func (s *Service) expire(ctx context.Context, conversationID, id string) {
	s.callMu.Lock()
	defer s.callMu.Unlock()
	msgs, err := s.Store.Messages(ctx, conversationID, 0)
	if err != nil {
		s.Log.Error("could not check a function call for a timeout", "conversation_id", conversationID, "error", err)
		return
	}
	found, answered := pendingCall(msgs, id)
	if !found || answered {
		return
	}
	v, err := s.Store.Get(ctx, conversationID)
	if err != nil {
		return // the conversation was deleted
	}
	body := map[string]any{"functionCallId": id, "error": map[string]any{"code": "TIMEOUT", "message": fmt.Sprintf("the page did not answer within %s", s.callTimeout())}}
	raw, _ := json.Marshal(map[string]any{"version": "v1.0", "rendererFunctionResponse": body})
	if _, err := s.recordAnswer(ctx, v, id, raw, body); err != nil {
		s.Log.Error("could not record a timed-out function call", "conversation_id", conversationID, "error", err)
	}
}
