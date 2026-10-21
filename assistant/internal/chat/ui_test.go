package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"assistant/internal/a2ui"
	"assistant/internal/chat"
	"assistant/internal/tasks"
)

const catalogID = "https://a2ui.org/specification/v1_0/catalogs/basic/catalog.json"

// form is a small interface: a Button whose action is "send".
func form(surface string) []any {
	return []any{
		map[string]any{"version": "v1.0", "createSurface": map[string]any{"surfaceId": surface, "catalogId": catalogID}},
		map[string]any{"version": "v1.0", "updateComponents": map[string]any{"surfaceId": surface, "components": []any{
			map[string]any{"id": "root", "component": "Button", "child": "label", "action": map[string]any{"event": map[string]any{"name": "send", "context": map[string]any{}}}},
			map[string]any{"id": "label", "component": "Text", "text": "Send"},
		}}},
	}
}

func askWith(t *testing.T, payload map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func withUI(t *testing.T, r *rig) {
	t.Helper()
	v, err := a2ui.New()
	if err != nil {
		t.Fatal(err)
	}
	r.svc.UI = v
}

func messages(t *testing.T, r *rig, c chat.Conversation) []chat.Message {
	t.Helper()
	m, err := r.svc.Store.Messages(context.Background(), c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAQuestionCanCarryAnInterfaceWhichIsStoredAfterItsWords(t *testing.T) {
	r := newRig(t)
	withUI(t, r)
	c, id := inRun(t, r)
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = askWith(t, map[string]any{"question": "Fill this in", "ui": form("s1")})
	r.svc.TaskUpdated(context.Background(), tk)
	got := messages(t, r, c)
	last := got[len(got)-2:]
	if last[0].Kind != chat.KindQuestion || last[0].Text != "Fill this in" || last[1].Kind != chat.KindUI || last[1].Role != chat.RoleAssistant {
		t.Fatalf("messages = %+v", last)
	}
	var list []map[string]any
	if err := json.Unmarshal(last[1].UI, &list); err != nil || len(list) != 2 {
		t.Errorf("ui = %s %v", last[1].UI, err)
	}
	if last[1].TaskID == nil || *last[1].TaskID != id {
		t.Errorf("the interface says which run it came from")
	}
}

func TestAnInterfaceAloneIsNotAQuestion(t *testing.T) {
	r := newRig(t)
	withUI(t, r)
	c, id := inRun(t, r)
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = askWith(t, map[string]any{"ui": form("s1")})
	r.svc.TaskUpdated(context.Background(), tk)
	got := messages(t, r, c)
	if got[len(got)-1].Kind != chat.KindUI || len(got) > 0 && got[len(got)-1].Text != "" {
		t.Fatalf("messages = %+v", got)
	}
	for _, m := range got {
		if m.Kind == chat.KindQuestion {
			t.Errorf("a question was posted for an interface alone: %+v", m)
		}
	}
}

func TestAnInvalidInterfaceIsRefusedWithItsReasonAndNothingOfItIsStored(t *testing.T) {
	r := newRig(t)
	withUI(t, r)
	c, id := inRun(t, r)
	bad := form("s1")
	bad[1].(map[string]any)["updateComponents"].(map[string]any)["components"] = []any{map[string]any{"id": "root", "component": "Hologram"}}
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = askWith(t, map[string]any{"question": "Q", "ui": bad})
	r.svc.TaskUpdated(context.Background(), tk)
	for _, m := range messages(t, r, c) {
		if m.Kind == chat.KindUI {
			t.Errorf("an invalid interface was stored: %+v", m)
		}
	}
	got := r.texts(t, c)
	if last := got[len(got)-1]; !strings.HasPrefix(last, "assistant/error: ⚠ The interface was refused:") || !strings.Contains(last, "Hologram") {
		t.Errorf("texts = %v", got)
	}
}

func TestAnIncompleteInterfaceIsRefused(t *testing.T) {
	r := newRig(t)
	withUI(t, r)
	c, id := inRun(t, r)
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = askWith(t, map[string]any{"ui": form("s1")[:1]}) // a surface with no components
	r.svc.TaskUpdated(context.Background(), tk)
	got := r.texts(t, c)
	if last := got[len(got)-1]; !strings.Contains(last, "interface was refused") {
		t.Errorf("texts = %v", got)
	}
}

func TestAnInterfaceNeedsAValidator(t *testing.T) {
	r := newRig(t)
	c, id := inRun(t, r)
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = askWith(t, map[string]any{"question": "Q", "ui": form("s1")})
	r.svc.TaskUpdated(context.Background(), tk)
	got := r.texts(t, c)
	if last := got[len(got)-1]; !strings.Contains(last, "cannot draw interfaces") {
		t.Errorf("texts = %v", got)
	}
}

func TestAnActionIsTheUsersTurnAndAnswersTheRunsQuestion(t *testing.T) {
	r := newRig(t)
	withUI(t, r)
	ctx := context.Background()
	c, id := inRun(t, r)
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = askWith(t, map[string]any{"question": "Q", "ui": form("s1")})
	r.svc.TaskUpdated(ctx, tk)
	r.tasks.setStatus(id, tasks.StatusWaiting)

	event := `{"version":"v1.0","action":{"name":"send","userMessage":"Sent it","surfaceId":"s1","sourceComponentId":"root","timestamp":"2026-10-21T12:00:00Z","context":{"n":1}}}`
	m, err := r.svc.Event(ctx, c.ID, json.RawMessage(event))
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != chat.KindUIAction || m.Role != chat.RoleUser || m.Text != "Sent it" || len(m.UI) == 0 {
		t.Errorf("stored = %+v", m)
	}
	if len(r.tasks.resumed) != 1 || !strings.HasPrefix(r.tasks.resumed[0], id+`={"ui_action":{`) || !strings.Contains(r.tasks.resumed[0], `"name":"send"`) || !strings.Contains(r.tasks.resumed[0], `"context":{"n":1}`) {
		t.Errorf("resumed = %v", r.tasks.resumed)
	}
}

func TestAnActionWithoutAMessageIsShownByItsName(t *testing.T) {
	r := newRig(t)
	withUI(t, r)
	ctx := context.Background()
	c, id := inRun(t, r)
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = askWith(t, map[string]any{"ui": form("s1")})
	r.svc.TaskUpdated(ctx, tk)
	r.tasks.setStatus(id, tasks.StatusWaiting)
	m, err := r.svc.Event(ctx, c.ID, json.RawMessage(`{"version":"v1.0","action":{"name":"send","surfaceId":"s1","sourceComponentId":"root","timestamp":"2026-10-21T12:00:00Z","context":{}}}`))
	if err != nil || m.Text != "(send)" {
		t.Errorf("%+v %v", m, err)
	}
}

func TestAnActionFromNowhereIsRefusedAndNothingIsStored(t *testing.T) {
	r := newRig(t)
	withUI(t, r)
	ctx := context.Background()
	c, id := inRun(t, r)
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = askWith(t, map[string]any{"ui": form("s1")})
	r.svc.TaskUpdated(ctx, tk)
	r.tasks.setStatus(id, tasks.StatusWaiting)
	before := len(messages(t, r, c))
	for name, event := range map[string]string{
		"unknown surface":   `{"version":"v1.0","action":{"name":"send","surfaceId":"other","sourceComponentId":"root","timestamp":"2026-10-21T12:00:00Z","context":{}}}`,
		"unknown component": `{"version":"v1.0","action":{"name":"send","surfaceId":"s1","sourceComponentId":"ghost","timestamp":"2026-10-21T12:00:00Z","context":{}}}`,
		"not a message":     `{"version":"v1.0"}`,
		"not json":          `nope`,
	} {
		_, err := r.svc.Event(ctx, c.ID, json.RawMessage(event))
		var ve *a2ui.ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
	if len(messages(t, r, c)) != before || len(r.tasks.resumed) != 0 {
		t.Errorf("a refused event left traces: %d messages, resumed %v", len(messages(t, r, c)), r.tasks.resumed)
	}
}

func TestAnActionWhileTheRunWorksIsRefused(t *testing.T) {
	r := newRig(t)
	withUI(t, r)
	ctx := context.Background()
	c, id := inRun(t, r)
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = askWith(t, map[string]any{"ui": form("s1")})
	r.svc.TaskUpdated(ctx, tk)
	r.tasks.setStatus(id, tasks.StatusRunning)
	_, err := r.svc.Event(ctx, c.ID, json.RawMessage(`{"version":"v1.0","action":{"name":"send","surfaceId":"s1","sourceComponentId":"root","timestamp":"2026-10-21T12:00:00Z","context":{}}}`))
	if !errors.Is(err, chat.ErrBusy) {
		t.Errorf("err = %v", err)
	}
}

func TestAnErrorFromThePageIsRecordedAndDoesNotReachTheRun(t *testing.T) {
	r := newRig(t)
	withUI(t, r)
	ctx := context.Background()
	c, id := inRun(t, r)
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = askWith(t, map[string]any{"ui": form("s1")})
	r.svc.TaskUpdated(ctx, tk)
	r.tasks.setStatus(id, tasks.StatusWaiting)
	m, err := r.svc.Event(ctx, c.ID, json.RawMessage(`{"version":"v1.0","error":{"code":"RENDERER_ERROR","surfaceId":"s1","message":"it broke"}}`))
	if err != nil || m.Kind != chat.KindUIAction || !strings.Contains(m.Text, "it broke") {
		t.Fatalf("%+v %v", m, err)
	}
	if len(r.tasks.resumed) != 0 {
		t.Errorf("an error resumed the run: %v", r.tasks.resumed)
	}
}

func TestEventsNeedAValidatorAndAConversation(t *testing.T) {
	r := newRig(t)
	c, _ := inRun(t, r)
	if _, err := r.svc.Event(context.Background(), c.ID, json.RawMessage(`{}`)); !errors.Is(err, chat.ErrNoInterfaces) {
		t.Errorf("err = %v", err)
	}
	withUI(t, r)
	if _, err := r.svc.Event(context.Background(), "00000000-0000-0000-0000-000000000000", json.RawMessage(`{}`)); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

type rejections struct{ interfaces, events int }

func (r *rejections) A2UIRejected(source string) {
	if source == "interface" {
		r.interfaces++
	} else {
		r.events++
	}
}

func TestRefusedInterfacesAndEventsAreCounted(t *testing.T) {
	r := newRig(t)
	withUI(t, r)
	counter := &rejections{}
	r.svc.Rejections = counter
	ctx := context.Background()
	c, id := inRun(t, r)
	bad := form("s1")
	bad[1].(map[string]any)["updateComponents"].(map[string]any)["components"] = []any{map[string]any{"id": "root", "component": "Hologram"}}
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = askWith(t, map[string]any{"ui": bad})
	r.svc.TaskUpdated(ctx, tk)
	r.svc.Event(ctx, c.ID, json.RawMessage(`{"version":"v1.0"}`))
	r.svc.Event(ctx, c.ID, json.RawMessage(`{"version":"v1.0","error":{"code":"X","surfaceId":"s","message":"m"}}`)) // accepted: not counted
	if counter.interfaces != 1 || counter.events != 1 {
		t.Errorf("counted %+v", counter)
	}
}

func TestASurfaceIdAModelReusesIsRenamedSoTheSecondInterfaceIsDrawn(t *testing.T) {
	r := newRig(t)
	withUI(t, r)
	ctx := context.Background()
	c, id := inRun(t, r)
	for i := 0; i < 2; i++ {
		tk := task(id, tasks.StatusWaiting)
		tk.Ask = askWith(t, map[string]any{"ui": form("main")}) // a model writes "main" every time
		r.svc.TaskUpdated(ctx, tk)
	}
	r.tasks.setStatus(id, tasks.StatusWaiting)
	var surfaces []string
	for _, m := range messages(t, r, c) {
		if m.Kind != chat.KindUI {
			continue
		}
		var list []map[string]any
		if err := json.Unmarshal(m.UI, &list); err != nil {
			t.Fatal(err)
		}
		for _, item := range list {
			for _, kind := range []string{"createSurface", "updateComponents"} {
				if body, ok := item[kind].(map[string]any); ok {
					surfaces = append(surfaces, kind[:6]+":"+body["surfaceId"].(string))
				}
			}
		}
	}
	if len(surfaces) != 4 || surfaces[0] != "create:main" || surfaces[1] != "update:main" {
		t.Fatalf("surfaces = %v: two interfaces were expected", surfaces)
	}
	renamed := strings.TrimPrefix(surfaces[2], "create:")
	if !strings.HasPrefix(renamed, "main-") || surfaces[3] != "update:"+renamed {
		t.Errorf("the second interface was not renamed consistently: %v", surfaces)
	}
	// The user acts on the renamed surface.
	event := `{"version":"v1.0","action":{"name":"send","surfaceId":"` + renamed + `","sourceComponentId":"root","timestamp":"2026-10-21T12:00:00Z","context":{}}}`
	if _, err := r.svc.Event(ctx, c.ID, json.RawMessage(event)); err != nil {
		t.Errorf("an action on the renamed surface: %v", err)
	}
}

func TestTheEnginesRoutingNoteIsAShortLineAndNotPartOfTheQuestion(t *testing.T) {
	r := newRig(t)
	c, id := inRun(t, r)
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = askWith(t, map[string]any{"question": "What is your name?", "routing": map[string]any{"workflow": "hello", "reason": "you asked for a greeting"}})
	r.svc.TaskUpdated(context.Background(), tk)
	got := r.texts(t, c)
	if len(got) < 3 || got[len(got)-2] != "assistant/info: → hello: you asked for a greeting" || got[len(got)-1] != "assistant/question: What is your name?" {
		t.Errorf("texts = %v", got)
	}
	tk.Ask = askWith(t, map[string]any{"question": "Q", "routing": map[string]any{"workflow": "hello"}})
	r.svc.TaskUpdated(context.Background(), tk)
	got = r.texts(t, c)
	if got[len(got)-2] != "assistant/info: → hello" {
		t.Errorf("a routing without a reason: %v", got)
	}
}

func TestARoutingNoteAloneSaysNothingElse(t *testing.T) {
	r := newRig(t)
	c, id := inRun(t, r)
	tk := task(id, tasks.StatusWaiting)
	tk.Ask = askWith(t, map[string]any{"routing": map[string]any{"workflow": "hello"}})
	r.svc.TaskUpdated(context.Background(), tk)
	got := r.texts(t, c)
	if got[len(got)-1] != "assistant/info: → hello" {
		t.Errorf("texts = %v", got)
	}
	for _, line := range got {
		if strings.HasPrefix(line, "assistant/question") {
			t.Errorf("a question was invented: %v", got)
		}
	}
}

func TestTheConversationSaysWhichWorkflowTakesTheTurnOnlyWhenARunHandedItOn(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	c, id := inRun(t, r)
	view := func() chat.View {
		v, err := r.svc.Get(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := view(); v.HandledBy != nil {
		t.Errorf("a run with no child: %v", *v.HandledBy)
	}
	run := func(js string) {
		if _, err := r.tasks.pool.Exec(ctx, `UPDATE tasks SET status = 'waiting', run = $2::jsonb WHERE id = $1::uuid`, id, js); err != nil {
			t.Fatal(err)
		}
	}
	run(`{"workflow_id":"orchestrator","child":{"workflow_id":"hello","depth":1}}`)
	if v := view(); v.HandledBy == nil || *v.HandledBy != "hello" {
		t.Errorf("handled by %v", v.HandledBy)
	}
	run(`{"workflow_id":"orchestrator","child":{"workflow_id":"deeper","child":{"workflow_id":"hello","depth":2}}}`)
	if v := view(); v.HandledBy == nil || *v.HandledBy != "hello" {
		t.Errorf("the deepest one takes the turn: %v", v.HandledBy)
	}
	run(`{"workflow_id":"orchestrator"}`)
	if v := view(); v.HandledBy != nil {
		t.Errorf("a run without a child: %v", *v.HandledBy)
	}
	if _, err := r.tasks.pool.Exec(ctx, `UPDATE tasks SET status = 'succeeded', run = $2::jsonb WHERE id = $1::uuid`, id, `{"workflow_id":"orchestrator","child":{"workflow_id":"hello"}}`); err != nil {
		t.Fatal(err)
	}
	if v := view(); v.HandledBy != nil {
		t.Errorf("a finished run is handled by no one: %v", *v.HandledBy)
	}
}
