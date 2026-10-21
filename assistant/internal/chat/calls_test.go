package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"assistant/internal/a2ui"
	"assistant/internal/chat"
	"assistant/internal/tasks"
)

// drawn is a conversation in a waiting run that has shown an interface (surface s1).
func drawn(t *testing.T, r *rig) (chat.Conversation, string) {
	t.Helper()
	withUI(t, r)
	c, id := inRun(t, r)
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = askWith(t, map[string]any{"question": "Q", "ui": form("s1")})
	r.svc.TaskUpdated(context.Background(), tk)
	r.tasks.setStatus(id, tasks.StatusWaiting)
	return c, id
}

func agentCall(id, surface, catalog, fn string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"version":"v1.0","callAgentFunction":{"surfaceId":%q,"functionCallId":%q,"callFunction":{"@call":%q,"catalogId":%q,"args":{}}}}`, surface, id, fn, catalog))
}

// lastAnswer is the agentFunctionResponse of the most recent assistant interface message.
func lastAnswer(t *testing.T, r *rig, c chat.Conversation) map[string]any {
	t.Helper()
	msgs := messages(t, r, c)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Kind != chat.KindUI {
			continue
		}
		var list []map[string]any
		if err := json.Unmarshal(msgs[i].UI, &list); err != nil {
			t.Fatal(err)
		}
		if resp, ok := list[0]["agentFunctionResponse"].(map[string]any); ok {
			return resp
		}
	}
	t.Fatalf("no answer in %+v", msgs)
	return nil
}

func TestThePageCanAskTheAssistantWhichWorkflowsThereAreAndGetsTheAnswerInTheConversation(t *testing.T) {
	r := newRig(t)
	c, _ := drawn(t, r)
	ctx := context.Background()
	m, err := r.svc.Event(ctx, c.ID, agentCall("p1", "s1", a2ui.AssistantCatalogID, "listWorkflows"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != chat.KindUIAction || m.Role != chat.RoleUser || m.Text != "(call listWorkflows)" {
		t.Errorf("the call is stored as %+v", m)
	}
	answer := lastAnswer(t, r, c)
	value, _ := answer["value"].(string)
	if answer["functionCallId"] != "p1" || !strings.Contains(value, "hello: Asks for a name.") || !strings.Contains(value, "ask: One call to a model.") || strings.Contains(value, "noop") {
		t.Errorf("answer = %v", answer)
	}
	if len(r.tasks.resumed) != 0 {
		t.Errorf("a call to the assistant must not move the run: %v", r.tasks.resumed)
	}
}

func TestACallToAnUnknownFunctionOrCatalogIsAnsweredWithAnError(t *testing.T) {
	r := newRig(t)
	c, _ := drawn(t, r)
	ctx := context.Background()
	for id, call := range map[string]json.RawMessage{
		"u1": agentCall("u1", "s1", a2ui.AssistantCatalogID, "viewport"),
		"u2": agentCall("u2", "s1", a2ui.CatalogID, "viewport"),
	} {
		if _, err := r.svc.Event(ctx, c.ID, call); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		answer := lastAnswer(t, r, c)
		errBody, _ := answer["error"].(map[string]any)
		if answer["functionCallId"] != id || errBody == nil || errBody["code"] == "" || answer["value"] != nil {
			t.Errorf("%s: answer = %v", id, answer)
		}
	}
}

func TestACallFromNowhereOrWithAUsedIdIsRefused(t *testing.T) {
	r := newRig(t)
	c, _ := drawn(t, r)
	ctx := context.Background()
	var ve *a2ui.ValidationError
	if _, err := r.svc.Event(ctx, c.ID, agentCall("n1", "other", a2ui.AssistantCatalogID, "listWorkflows")); !errors.As(err, &ve) {
		t.Errorf("an unknown surface: %v", err)
	}
	if _, err := r.svc.Event(ctx, c.ID, agentCall("d1", "s1", a2ui.AssistantCatalogID, "listWorkflows")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Event(ctx, c.ID, agentCall("d1", "s1", a2ui.AssistantCatalogID, "listWorkflows")); !errors.As(err, &ve) || !strings.Contains(err.Error(), "already used") {
		t.Errorf("a used id: %v", err)
	}
}

func TestThePageMayCallTheAssistantOnlyALimitedNumberOfTimesPerTurn(t *testing.T) {
	r := newRig(t)
	c, _ := drawn(t, r)
	ctx := context.Background()
	for i := 0; i < chat.MaxAgentCallsPerTurn; i++ {
		if _, err := r.svc.Event(ctx, c.ID, agentCall(fmt.Sprintf("l%d", i), "s1", a2ui.AssistantCatalogID, "listWorkflows")); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	_, err := r.svc.Event(ctx, c.ID, agentCall("l-over", "s1", a2ui.AssistantCatalogID, "listWorkflows"))
	var ve *a2ui.ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("the call over the limit: %v", err)
	}
	// The user doing something starts a new turn.
	r.tasks.setStatus(*mustView(t, r, c).TaskID, tasks.StatusWaiting)
	if _, err := r.svc.Send(ctx, c.ID, "next"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Event(ctx, c.ID, agentCall("l-new", "s1", a2ui.AssistantCatalogID, "listWorkflows")); err != nil {
		t.Errorf("a new turn: %v", err)
	}
}

// callsFor asks, as a run would, for the page to run a function.
func askPageToRun(t *testing.T, r *rig, id string, calls ...string) {
	t.Helper()
	var ui []any
	for _, fn := range calls {
		ui = append(ui, map[string]any{"version": "v1.0", "callRendererFunction": map[string]any{"functionCallId": "placeholder", "callFunction": map[string]any{"catalogId": a2ui.AssistantCatalogID, "@call": fn, "args": map[string]any{}}}})
	}
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = askWith(t, map[string]any{"ui": ui})
	r.svc.TaskUpdated(context.Background(), tk)
}

// pendingIDs are the ids of the calls the run made to the page.
func pendingIDs(t *testing.T, r *rig, c chat.Conversation) []string {
	t.Helper()
	var ids []string
	for _, m := range messages(t, r, c) {
		if m.Kind != chat.KindUI {
			continue
		}
		var list []map[string]any
		json.Unmarshal(m.UI, &list)
		for _, item := range list {
			if call, ok := item["callRendererFunction"].(map[string]any); ok {
				ids = append(ids, call["functionCallId"].(string))
			}
		}
	}
	return ids
}

func TestARunAsksThePageToRunAFunctionAndGetsTheAnswerAsItsNextMessage(t *testing.T) {
	r := newRig(t)
	withUI(t, r)
	ctx := context.Background()
	c, id := inRun(t, r)
	askPageToRun(t, r, id, "viewport")
	r.tasks.setStatus(id, tasks.StatusWaiting)
	ids := pendingIDs(t, r, c)
	if len(ids) != 1 || ids[0] == "placeholder" || !strings.HasPrefix(ids[0], "call-") {
		t.Fatalf("the engine assigns the call id: %v", ids)
	}
	for _, m := range messages(t, r, c) {
		if m.Kind == chat.KindQuestion {
			t.Errorf("a call alone is not a question: %+v", m)
		}
	}
	answer := fmt.Sprintf(`{"version":"v1.0","rendererFunctionResponse":{"functionCallId":%q,"value":{"width":1280,"height":900,"pixelRatio":2,"colorScheme":"dark"}}}`, ids[0])
	m, err := r.svc.Event(ctx, c.ID, json.RawMessage(answer))
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != chat.KindUIAction {
		t.Errorf("stored %+v", m)
	}
	if len(r.tasks.resumed) != 1 || !strings.Contains(r.tasks.resumed[0], `"ui_function_response":{`) || !strings.Contains(r.tasks.resumed[0], `"width":1280`) || !strings.Contains(r.tasks.resumed[0], ids[0]) {
		t.Fatalf("resumed = %v", r.tasks.resumed)
	}
	// An answer is taken once.
	var ve *a2ui.ValidationError
	if _, err := r.svc.Event(ctx, c.ID, json.RawMessage(answer)); !errors.As(err, &ve) || !strings.Contains(err.Error(), "already answered") {
		t.Errorf("a second answer: %v", err)
	}
	// And only for a call the engine made.
	ghost := `{"version":"v1.0","rendererFunctionResponse":{"functionCallId":"call-ghost","value":1}}`
	if _, err := r.svc.Event(ctx, c.ID, json.RawMessage(ghost)); !errors.As(err, &ve) || !strings.Contains(err.Error(), "no call") {
		t.Errorf("an answer to nothing: %v", err)
	}
}

func TestAnErrorAnswerReachesTheRunAsAnError(t *testing.T) {
	r := newRig(t)
	withUI(t, r)
	c, id := inRun(t, r)
	askPageToRun(t, r, id, "viewport")
	r.tasks.setStatus(id, tasks.StatusWaiting)
	call := pendingIDs(t, r, c)[0]
	answer := fmt.Sprintf(`{"version":"v1.0","rendererFunctionResponse":{"functionCallId":%q,"error":{"code":"INVALID_FUNCTION_CALL","message":"no such function"}}}`, call)
	if _, err := r.svc.Event(context.Background(), c.ID, json.RawMessage(answer)); err != nil {
		t.Fatal(err)
	}
	if len(r.tasks.resumed) != 1 || !strings.Contains(r.tasks.resumed[0], `"error":{"code":"INVALID_FUNCTION_CALL"`) {
		t.Errorf("resumed = %v", r.tasks.resumed)
	}
}

func TestAnAnswerThatDoesNotComeInTimeReachesTheRunAsATimeoutAndALateOneIsRefused(t *testing.T) {
	r := newRig(t)
	withUI(t, r)
	r.svc.CallTimeout = 40 * time.Millisecond
	ctx := context.Background()
	c, id := inRun(t, r)
	askPageToRun(t, r, id, "viewport")
	r.tasks.setStatus(id, tasks.StatusWaiting)
	call := pendingIDs(t, r, c)[0]
	deadline := time.Now().Add(3 * time.Second)
	for len(r.tasks.snapshotResumed()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := r.tasks.snapshotResumed()
	if len(got) != 1 || !strings.Contains(got[0], `"code":"TIMEOUT"`) || !strings.Contains(got[0], call) {
		t.Fatalf("resumed = %v", got)
	}
	late := fmt.Sprintf(`{"version":"v1.0","rendererFunctionResponse":{"functionCallId":%q,"value":1}}`, call)
	var ve *a2ui.ValidationError
	if _, err := r.svc.Event(ctx, c.ID, json.RawMessage(late)); !errors.As(err, &ve) || !strings.Contains(err.Error(), "already answered") {
		t.Errorf("a late answer: %v", err)
	}
}

func TestAnAnsweredCallIsNotTimedOut(t *testing.T) {
	r := newRig(t)
	withUI(t, r)
	r.svc.CallTimeout = 60 * time.Millisecond
	c, id := inRun(t, r)
	askPageToRun(t, r, id, "viewport")
	r.tasks.setStatus(id, tasks.StatusWaiting)
	call := pendingIDs(t, r, c)[0]
	answer := fmt.Sprintf(`{"version":"v1.0","rendererFunctionResponse":{"functionCallId":%q,"value":1}}`, call)
	if _, err := r.svc.Event(context.Background(), c.ID, json.RawMessage(answer)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if got := r.tasks.snapshotResumed(); len(got) != 1 || strings.Contains(got[0], "TIMEOUT") {
		t.Errorf("resumed = %v", got)
	}
}

func TestARunMayAskThePageForOnlyAFewFunctionsAtOnceAndNotForOnesItMayNotCall(t *testing.T) {
	r := newRig(t)
	withUI(t, r)
	c, id := inRun(t, r)
	askPageToRun(t, r, id, "viewport", "viewport", "viewport", "viewport", "viewport", "viewport")
	askPageToRun(t, r, id, "listWorkflows")
	askPageToRun(t, r, id, "viewport", "viewport")
	var refusals, interfaces int
	for _, m := range messages(t, r, c) {
		switch {
		case m.Kind == chat.KindError && strings.Contains(m.Text, "interface was refused"):
			refusals++
		case m.Kind == chat.KindUI:
			interfaces++
		}
	}
	if refusals != 2 || interfaces != 1 {
		t.Errorf("refusals %d interfaces %d: six calls and a call to a renderer-only function are refused, two are fine", refusals, interfaces)
	}
}
