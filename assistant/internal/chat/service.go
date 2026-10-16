package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"assistant/internal/tasks"
	"assistant/internal/workflows"
)

// ErrBusy is returned when a message arrives while the assistant is still
// working on the previous one.
var ErrBusy = errors.New("the assistant is still working on your last message")

// Tasks is what the service needs from the queue.
type Tasks interface {
	Create(ctx context.Context, n tasks.New) (tasks.Task, error)
	Get(ctx context.Context, id string) (tasks.Task, error)
	Resume(ctx context.Context, id string, message json.RawMessage) (tasks.Task, error)
}

// Catalog lists the workflows a conversation can run.
type Catalog interface {
	Catalog() []workflows.Info
}

// Channel carries the assistant's words to the user: Telegram, one day. The
// service has already stored a message when it is delivered, so a channel that
// fails loses nothing from the conversation; it is told and logged.
type Channel interface {
	Deliver(ctx context.Context, c Conversation, m Message) error
}

// ErrorCounter counts messages that could not be posted.
type ErrorCounter interface{ ChatNotifyError() }

// Service runs conversations: it turns the user's messages into workflow runs
// and the runs' questions, results and failures into the assistant's messages.
type Service struct {
	Store   *Store
	Tasks   Tasks
	Catalog Catalog
	Channel Channel
	Errors  ErrorCounter
	Log     *slog.Logger
}

// ErrNotConversational is returned for a workflow a conversation cannot run.
var ErrNotConversational = errors.New("a conversation can run a YAML workflow or ask, not this one")

// payloadFor builds the task payload that carries the user's text into a
// workflow, by the workflow's kind.
func (s *Service) payloadFor(workflowID, text string) (json.RawMessage, error) {
	for _, info := range s.Catalog.Catalog() {
		if info.ID != workflowID {
			continue
		}
		switch {
		case info.Kind == "yaml":
			return json.Marshal(map[string]any{"message": text})
		case info.ID == "ask":
			return json.Marshal(map[string]any{"prompt": text})
		}
		return nil, ErrNotConversational
	}
	return nil, ErrUnknownWorkflow
}

// Start opens a conversation. The workflow must be one a conversation can run.
func (s *Service) Start(ctx context.Context, userID, workflowID, title string) (Conversation, error) {
	if _, err := s.payloadFor(workflowID, ""); err != nil {
		return Conversation{}, err
	}
	return s.Store.Create(ctx, userID, workflowID, title)
}

// Send handles what the user typed. A command (/help) is answered at once.
// Otherwise the text goes to the run: as the answer to a question it is waiting
// for, or as the first message of a new run. It returns the stored message.
func (s *Service) Send(ctx context.Context, conversationID, text string) (Message, error) {
	if strings.TrimSpace(text) == "" {
		return Message{}, ErrBlank
	}
	v, err := s.Store.Get(ctx, conversationID)
	if err != nil {
		return Message{}, err
	}
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
		return s.command(ctx, v, text)
	}

	var current *tasks.Task
	if v.TaskID != nil {
		t, err := s.Tasks.Get(ctx, *v.TaskID)
		switch {
		case err == nil:
			current = &t
		case errors.Is(err, tasks.ErrNotFound):
			// The run was removed: the conversation just starts a new one.
		default:
			return Message{}, fmt.Errorf("look up the conversation's run: %w", err)
		}
	}
	if current != nil && (current.Status == tasks.StatusPending || current.Status == tasks.StatusRunning) {
		return Message{}, ErrBusy
	}

	msg, err := s.Store.AddMessage(ctx, v.ID, RoleUser, KindText, text, nil)
	if err != nil {
		return Message{}, err
	}
	if current != nil && current.Status == tasks.StatusWaiting {
		answer, _ := json.Marshal(text)
		if _, err := s.Tasks.Resume(ctx, current.ID, answer); err != nil {
			return msg, s.fail(ctx, v, fmt.Errorf("could not deliver your answer to the run: %w", err))
		}
		return msg, nil
	}
	payload, err := s.payloadFor(v.WorkflowID, text)
	if err != nil {
		return msg, s.fail(ctx, v, fmt.Errorf("could not start the workflow %q: %w", v.WorkflowID, err))
	}
	task, err := s.Tasks.Create(ctx, tasks.New{UserID: v.UserID, WorkflowID: v.WorkflowID, Source: tasks.SourceTelegram, Payload: payload})
	if err != nil {
		return msg, s.fail(ctx, v, fmt.Errorf("could not start the workflow %q: %w", v.WorkflowID, err))
	}
	if err := s.Store.SetTask(ctx, v.ID, task.ID); err != nil {
		return msg, err
	}
	return msg, nil
}

// fail tells the user in the conversation why their message did not take, and
// returns the error as well, so the caller sees it too.
func (s *Service) fail(ctx context.Context, v View, cause error) error {
	s.post(ctx, v.Conversation, KindError, "⚠ "+cause.Error(), nil)
	return cause
}

// command answers a slash command.
func (s *Service) command(ctx context.Context, v View, text string) (Message, error) {
	msg, err := s.Store.AddMessage(ctx, v.ID, RoleUser, KindText, text, nil)
	if err != nil {
		return Message{}, err
	}
	switch name := strings.Fields(strings.TrimSpace(text))[0]; name {
	case "/help":
		s.post(ctx, v.Conversation, KindInfo, s.help(v), nil)
	default:
		s.post(ctx, v.Conversation, KindInfo, fmt.Sprintf("I do not know the command %s. Try /help.", name), nil)
	}
	return msg, nil
}

// help describes the commands and the workflows.
func (s *Service) help(v View) string {
	var b strings.Builder
	b.WriteString("Commands:\n/help: this message\n\n")
	fmt.Fprintf(&b, "This conversation runs the workflow %q. ", v.WorkflowID)
	b.WriteString("Write a message to start it; when it asks you something, your next message is the answer. When a run has finished or failed, your next message starts a new one.\n\nWorkflows you can run:\n")
	infos := s.Catalog.Catalog()
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	for _, info := range infos {
		if info.Kind == "yaml" || info.ID == "ask" {
			fmt.Fprintf(&b, "• %s: %s\n", info.ID, info.Description)
		}
	}
	b.WriteString("\nStart a new conversation to talk to a different workflow.")
	return b.String()
}

// post stores an assistant message and hands it to the channel. A channel
// failure is logged and counted, never lost silently; the message stays in the
// conversation either way.
func (s *Service) post(ctx context.Context, c Conversation, kind, text string, taskID *string) {
	msg, err := s.Store.AddMessage(ctx, c.ID, RoleAssistant, kind, text, taskID)
	if err != nil {
		s.Log.Error("could not store the assistant's message", "conversation_id", c.ID, "error", err)
		if s.Errors != nil {
			s.Errors.ChatNotifyError()
		}
		return
	}
	if s.Channel != nil {
		if err := s.Channel.Deliver(ctx, c, msg); err != nil {
			s.Log.Error("could not deliver the assistant's message", "conversation_id", c.ID, "error", err)
			if s.Errors != nil {
				s.Errors.ChatNotifyError()
			}
		}
	}
}

// TaskUpdated is called by the worker when a run waits for the user, finishes,
// fails or is cancelled. If a conversation is in that run, the assistant says
// what happened.
func (s *Service) TaskUpdated(ctx context.Context, t tasks.Task) {
	c, ok, err := s.Store.ForTask(ctx, t.ID)
	if err != nil {
		s.Log.Error("could not find the conversation of a task", "task_id", t.ID, "error", err)
		if s.Errors != nil {
			s.Errors.ChatNotifyError()
		}
		return
	}
	if !ok {
		return
	}
	kind, text := describe(t)
	if text == "" {
		return
	}
	s.post(ctx, c, kind, text, &t.ID)
}

// describe turns a task's new state into the assistant's message.
func describe(t tasks.Task) (kind, text string) {
	switch t.Status {
	case tasks.StatusWaiting:
		return KindQuestion, questionText(t.Ask)
	case tasks.StatusSucceeded:
		return KindResult, resultText(t.Result)
	case tasks.StatusFailed:
		reason := "the run failed without a message"
		if t.Error != nil && *t.Error != "" {
			reason = *t.Error
		}
		return KindError, "⚠ The run failed: " + reason
	case tasks.StatusCancelled:
		return KindInfo, "The run was cancelled."
	}
	return "", ""
}

// questionText is what a waiting run asked, with any options listed.
func questionText(ask json.RawMessage) string {
	var payload map[string]any
	if json.Unmarshal(ask, &payload) != nil || payload == nil {
		return "The assistant is waiting for your answer."
	}
	var b strings.Builder
	for _, key := range []string{"question", "text", "prompt", "message"} {
		if q, ok := payload[key].(string); ok && strings.TrimSpace(q) != "" {
			b.WriteString(q)
			break
		}
	}
	if options, ok := payload["options"].([]any); ok {
		for i, o := range options {
			if text, ok := o.(string); ok {
				fmt.Fprintf(&b, "\n%d. %s", i+1, text)
			}
		}
	}
	if b.Len() == 0 {
		raw, _ := json.MarshalIndent(payload, "", "  ")
		return "The assistant asks:\n" + string(raw)
	}
	return b.String()
}

// resultText is the end of a run in words: the model's text for ask, a greeting
// if the state has one, otherwise the final state as JSON.
func resultText(result json.RawMessage) string {
	var r struct {
		Text  string         `json:"text"`
		State map[string]any `json:"state"`
	}
	if json.Unmarshal(result, &r) != nil {
		return "The run is finished."
	}
	if r.Text != "" {
		return r.Text
	}
	for _, key := range []string{"greeting", "summary", "reply", "answer"} {
		if text, ok := r.State[key].(string); ok && strings.TrimSpace(text) != "" {
			return text + "\n\n(The run is finished. Write a message to start a new one.)"
		}
	}
	raw, _ := json.MarshalIndent(r.State, "", "  ")
	return "The run is finished. Its final state:\n" + string(raw) + "\n\n(Write a message to start a new run.)"
}

// mockKeeps is how many delivered messages the mock channel remembers.
const mockKeeps = 200

// MockChannel is the channel of a Telegram that does not exist yet: it keeps
// the last messages it was asked to deliver, so the mock side can be looked at
// in a test. It is safe for concurrent use.
type MockChannel struct {
	mu        sync.Mutex
	delivered []Message
}

// Deliver records the message.
func (m *MockChannel) Deliver(_ context.Context, _ Conversation, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delivered = append(m.delivered, msg)
	if len(m.delivered) > mockKeeps {
		m.delivered = m.delivered[len(m.delivered)-mockKeeps:]
	}
	return nil
}

// Delivered returns what was delivered, oldest first.
func (m *MockChannel) Delivered() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Message(nil), m.delivered...)
}
