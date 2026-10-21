package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"assistant/internal/a2ui"
	"assistant/internal/chat"
)

// ChatService is what the API needs from the conversations; *chat.Service
// implements it.
type ChatService interface {
	Start(ctx context.Context, userID, workflowID, title string) (chat.Conversation, error)
	Send(ctx context.Context, conversationID, text string) (chat.Message, error)
	Event(ctx context.Context, conversationID string, raw json.RawMessage) (chat.Message, error)
	Get(ctx context.Context, id string) (chat.View, error)
	List(ctx context.Context, userID string) ([]chat.View, error)
	Messages(ctx context.Context, conversationID string, after int64) ([]chat.Message, error)
	Remove(ctx context.Context, id string) error
}

type newConversationRequest struct {
	UserID     *string `json:"user_id"`
	WorkflowID *string `json:"workflow_id"`
	Title      string  `json:"title"`
}

func (d Deps) createConversation(w http.ResponseWriter, r *http.Request) {
	var req newConversationRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	switch {
	case req.UserID == nil || strings.TrimSpace(*req.UserID) == "":
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "user_id is required")
		return
	case req.WorkflowID == nil || strings.TrimSpace(*req.WorkflowID) == "":
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "workflow_id is required")
		return
	}
	c, err := d.Chat.Start(r.Context(), *req.UserID, strings.TrimSpace(*req.WorkflowID), req.Title)
	switch {
	case errors.Is(err, chat.ErrUnknownUser):
		writeError(w, http.StatusNotFound, codeNotFound, "user not found")
	case errors.Is(err, chat.ErrUnknownWorkflow):
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "there is no such workflow: see GET /api/v1/workflows")
	case errors.Is(err, chat.ErrNotConversational):
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "a conversation can run a YAML workflow or ask, not this one")
	case err != nil:
		d.internalError(w, r, "create conversation", err)
	default:
		v, getErr := d.Chat.Get(r.Context(), c.ID)
		if getErr != nil {
			d.internalError(w, r, "get new conversation", getErr)
			return
		}
		writeJSON(w, http.StatusCreated, v)
	}
}

type conversationList struct {
	Items []chat.View `json:"items"`
}

func (d Deps) listConversations(w http.ResponseWriter, r *http.Request) {
	items, err := d.Chat.List(r.Context(), r.URL.Query().Get("user_id"))
	if err != nil {
		d.internalError(w, r, "list conversations", err)
		return
	}
	writeJSON(w, http.StatusOK, conversationList{Items: items})
}

func (d Deps) getConversation(w http.ResponseWriter, r *http.Request) {
	v, err := d.Chat.Get(r.Context(), chi.URLParam(r, "id"))
	switch {
	case errors.Is(err, chat.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "conversation not found")
	case err != nil:
		d.internalError(w, r, "get conversation", err)
	default:
		writeJSON(w, http.StatusOK, v)
	}
}

func (d Deps) removeConversation(w http.ResponseWriter, r *http.Request) {
	err := d.Chat.Remove(r.Context(), chi.URLParam(r, "id"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, chat.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "conversation not found")
	default:
		d.internalError(w, r, "remove conversation", err)
	}
}

type messageList struct {
	Items []chat.Message `json:"items"`
}

func (d Deps) listMessages(w http.ResponseWriter, r *http.Request) {
	var after int64
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, "after must be a message id, 0 or more")
			return
		}
		after = n
	}
	items, err := d.Chat.Messages(r.Context(), chi.URLParam(r, "id"), after)
	switch {
	case errors.Is(err, chat.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "conversation not found")
	case err != nil:
		d.internalError(w, r, "list messages", err)
	default:
		writeJSON(w, http.StatusOK, messageList{Items: items})
	}
}

type newChatMessage struct {
	Text *string `json:"text"`
}

// sendMessage is the user saying something in a conversation. The reply comes
// later, from the run, as the assistant's messages.
func (d Deps) sendMessage(w http.ResponseWriter, r *http.Request) {
	var req newChatMessage
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	if req.Text == nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "text is required")
		return
	}
	m, err := d.Chat.Send(r.Context(), chi.URLParam(r, "id"), *req.Text)
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, m)
	case errors.Is(err, chat.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "conversation not found")
	case errors.Is(err, chat.ErrBlank):
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "text must not be blank")
	case errors.Is(err, chat.ErrBusy):
		writeError(w, http.StatusConflict, codeConflict, err.Error())
	case m.ID != 0:
		// The message was taken but the run could not be started or answered; the
		// conversation already says why, and so does this response.
		writeError(w, http.StatusBadGateway, codeInternal, err.Error())
	default:
		d.internalError(w, r, "send message", err)
	}
}

// maxEventBytes bounds a request body before it is read; the validator then
// applies its own, tighter limit to the message.
const maxEventBytes = 1 << 20

// postUIEvent is the page telling the assistant what happened in an interface
// it drew: the user's action, an error, a function call or its answer. The body
// is one renderer-to-agent A2UI message.
func (d Deps) postUIEvent(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEventBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "the body could not be read or is over 1 MiB")
		return
	}
	m, err := d.Chat.Event(r.Context(), chi.URLParam(r, "id"), raw)
	var invalid *a2ui.ValidationError
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, m)
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, codeInvalidRequest, invalid.Error())
	case errors.Is(err, chat.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "conversation not found")
	case errors.Is(err, chat.ErrBusy):
		writeError(w, http.StatusConflict, codeConflict, err.Error())
	case errors.Is(err, chat.ErrNoInterfaces), errors.Is(err, chat.ErrUnsupportedEvent):
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
	case m.ID != 0:
		writeError(w, http.StatusBadGateway, codeInternal, err.Error())
	default:
		d.internalError(w, r, "post interface event", err)
	}
}
