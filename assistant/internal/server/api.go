package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"assistant/internal/llm"
	"assistant/internal/llmcalls"
	"assistant/internal/tasks"
	"assistant/internal/tasksteps"
	"assistant/internal/users"
	"assistant/internal/workflows"
)

// UserStore is what the API needs from the users store.
type UserStore interface {
	Create(ctx context.Context, displayName string, telegramID *int64) (users.User, error)
	List(ctx context.Context) ([]users.User, error)
}

// TaskStore is what the API needs from the tasks store.
type TaskStore interface {
	Create(ctx context.Context, n tasks.New) (tasks.Task, error)
	Get(ctx context.Context, id string) (tasks.Task, error)
	List(ctx context.Context, o tasks.ListOptions) (tasks.Page, error)
	Resume(ctx context.Context, id string, message json.RawMessage) (tasks.Task, error)
	Cancel(ctx context.Context, id string) (tasks.Task, error)
	Remove(ctx context.Context, id string) error
	RemoveFinished(ctx context.Context, status string) (int, error)
	Retry(ctx context.Context, id string) (tasks.Task, error)
}

// WorkflowCatalog lists the workflows a task can name. *workflows.Runner
// implements it.
type WorkflowCatalog interface {
	Catalog() []workflows.Info
}

// Stopper stops the task the worker is running, if it is the one named. The
// worker implements it.
type Stopper interface {
	Cancel(id string) bool
}

// StepStore is what the API needs from the workflow trace.
type StepStore interface {
	ForTask(ctx context.Context, taskID string) ([]tasksteps.Step, error)
}

// CallStore is what the API needs from the model call records.
type CallStore interface {
	ForTask(ctx context.Context, taskID string) ([]llmcalls.Call, error)
	UsageByUserAndModel(ctx context.Context, since *time.Time) ([]llmcalls.Usage, error)
	Attempts(ctx context.Context, since *time.Time, limit, offset int) ([]llmcalls.Call, int, error)
	UsageForTasks(ctx context.Context, taskIDs []string) (map[string]llmcalls.TaskUsage, error)
}

// ModelStatuses reports the configured models and whether each can take
// calls now. *llm.Router implements it.
type ModelStatuses interface {
	Status() []llm.ModelStatus
}

// maxBodyBytes bounds a request body; payloads are small structured input.
const maxBodyBytes = 1 << 20

// Error codes of the API.
const (
	codeInvalidRequest = "invalid_request"
	codeNotFound       = "not_found"
	codeConflict       = "conflict"
	codeInternal       = "internal"
)

type apiError struct {
	Error apiErrorBody `json:"error"`
}

type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiError{Error: apiErrorBody{Code: code, Message: message}})
}

// internalError logs the cause and tells the client nothing about it.
func (d Deps) internalError(w http.ResponseWriter, r *http.Request, what string, err error) {
	d.Log.Error(what, "method", r.Method, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
}

// decodeBody reads one JSON object from the request into dst, refusing an
// empty body, unknown fields and trailing content.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("request body is empty; expected a JSON object")
		}
		return errors.New("request body is not valid: " + cleanJSONError(err))
	}
	if dec.More() {
		return errors.New("request body must hold one JSON object only")
	}
	return nil
}

func cleanJSONError(err error) string {
	msg := err.Error()
	// "json: unknown field \"x\"" and "json: cannot unmarshal ..." read fine
	// without the package prefix.
	return strings.TrimPrefix(msg, "json: ")
}

func (d Deps) mountAPI(r chi.Router) {
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/users", d.listUsers)
		r.Post("/users", d.createUser)
		r.Get("/tasks", d.listTasks)
		r.Post("/tasks", d.createTask)
		r.Get("/tasks/{id}", d.getTask)
		r.Delete("/tasks", d.removeTasks)
		r.Delete("/tasks/{id}", d.removeTask)
		r.Post("/tasks/{id}/cancel", d.cancelTask)
		r.Post("/tasks/{id}/retry", d.retryTask)
		r.Post("/tasks/{id}/messages", d.answerTask)
		r.Get("/tasks/{id}/steps", d.listTaskSteps)
		r.Get("/tasks/{id}/llm-calls", d.listTaskCalls)
		r.Get("/usage", d.usage)
		r.Get("/llm-calls", d.listAttempts)
		r.Get("/models", d.listModels)
		r.Get("/workflows", d.listWorkflows)
		r.Get("/conversations", d.listConversations)
		r.Post("/conversations", d.createConversation)
		r.Get("/conversations/{id}", d.getConversation)
		r.Delete("/conversations/{id}", d.removeConversation)
		r.Get("/conversations/{id}/messages", d.listMessages)
		r.Post("/conversations/{id}/messages", d.sendMessage)
		r.Post("/conversations/{id}/ui-events", d.postUIEvent)
	})
}

// ── users ────────────────────────────────────────────────────────────────────

type newUserRequest struct {
	DisplayName *string `json:"display_name"`
	TelegramID  *int64  `json:"telegram_id"`
}

func (d Deps) createUser(w http.ResponseWriter, r *http.Request) {
	var req newUserRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	if req.DisplayName == nil || strings.TrimSpace(*req.DisplayName) == "" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "display_name is required")
		return
	}
	u, err := d.Users.Create(r.Context(), *req.DisplayName, req.TelegramID)
	switch {
	case errors.Is(err, users.ErrBlankName):
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "display_name is required")
	case errors.Is(err, users.ErrTelegramIDTaken):
		writeError(w, http.StatusConflict, codeConflict, "another user already has this telegram_id")
	case err != nil:
		d.internalError(w, r, "create user", err)
	default:
		writeJSON(w, http.StatusCreated, u)
	}
}

type userList struct {
	Items []users.User `json:"items"`
}

func (d Deps) listUsers(w http.ResponseWriter, r *http.Request) {
	list, err := d.Users.List(r.Context())
	if err != nil {
		d.internalError(w, r, "list users", err)
		return
	}
	writeJSON(w, http.StatusOK, userList{Items: list})
}

// ── tasks ────────────────────────────────────────────────────────────────────

// taskResponse is a task with the times derived from its timestamps. They are
// computed here, with the server's clock, so a browser's clock cannot skew them.
type taskResponse struct {
	tasks.Task
	QueueSeconds     float64            `json:"queue_seconds"`
	ExecutionSeconds *float64           `json:"execution_seconds"`
	Usage            llmcalls.TaskUsage `json:"usage"`
}

// noUsage is what a task that made no model calls has used.
func noUsage() llmcalls.TaskUsage { return llmcalls.TaskUsage{Models: []string{}} }

// respond describes a task that has no usage to show, such as one just queued.
func (d Deps) respond(t tasks.Task) taskResponse {
	now := d.now()
	resp := taskResponse{Task: t, QueueSeconds: t.QueueTime(now).Seconds(), Usage: noUsage()}
	if exec, ok := t.ExecutionTime(now); ok {
		s := exec.Seconds()
		resp.ExecutionSeconds = &s
	}
	return resp
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

type newTaskRequest struct {
	UserID     *string         `json:"user_id"`
	WorkflowID *string         `json:"workflow_id"`
	Payload    json.RawMessage `json:"payload"`
}

func (d Deps) createTask(w http.ResponseWriter, r *http.Request) {
	var req newTaskRequest
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
	case !users.ValidID(*req.UserID):
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "user_id is not a valid id")
		return
	}
	t, err := d.Tasks.Create(r.Context(), tasks.New{
		UserID: *req.UserID, WorkflowID: *req.WorkflowID, Source: tasks.SourceAPI, Payload: req.Payload,
	})
	switch {
	case errors.Is(err, tasks.ErrUnknownUser):
		writeError(w, http.StatusNotFound, codeNotFound, "user not found")
	case errors.Is(err, tasks.ErrPayloadNotJSON):
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "payload must be a JSON object")
	case errors.Is(err, tasks.ErrBlankWorkflow):
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "workflow_id is required")
	case err != nil:
		d.internalError(w, r, "create task", err)
	default:
		writeJSON(w, http.StatusCreated, d.respond(t))
	}
}

func (d Deps) getTask(w http.ResponseWriter, r *http.Request) {
	t, err := d.Tasks.Get(r.Context(), chi.URLParam(r, "id"))
	switch {
	case errors.Is(err, tasks.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "task not found")
	case err != nil:
		d.internalError(w, r, "get task", err)
	default:
		resp, err := d.respondWithUsage(r.Context(), []tasks.Task{t})
		if err != nil {
			d.internalError(w, r, "get task usage", err)
			return
		}
		writeJSON(w, http.StatusOK, resp[0])
	}
}

// respondWithUsage describes the tasks with what each used, in one query.
func (d Deps) respondWithUsage(ctx context.Context, ts []tasks.Task) ([]taskResponse, error) {
	ids := make([]string, len(ts))
	for i, t := range ts {
		ids[i] = t.ID
	}
	usage, err := d.Calls.UsageForTasks(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]taskResponse, len(ts))
	for i, t := range ts {
		out[i] = d.respond(t)
		if u, ok := usage[t.ID]; ok {
			out[i].Usage = u
		}
	}
	return out, nil
}

type taskList struct {
	Items  []taskResponse `json:"items"`
	Total  int            `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

func (d Deps) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts := tasks.ListOptions{Status: q.Get("status"), Limit: tasks.DefaultLimit}
	if opts.Status != "" && !validStatus(opts.Status) {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "status must be one of pending, running, waiting, succeeded, failed, cancelled")
		return
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > tasks.MaxLimit {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, "limit must be a number from 1 to "+strconv.Itoa(tasks.MaxLimit))
			return
		}
		opts.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, "offset must be a number, 0 or more")
			return
		}
		opts.Offset = n
	}
	page, err := d.Tasks.List(r.Context(), opts)
	if err != nil {
		d.internalError(w, r, "list tasks", err)
		return
	}
	items, err := d.respondWithUsage(r.Context(), page.Tasks)
	if err != nil {
		d.internalError(w, r, "list task usage", err)
		return
	}
	writeJSON(w, http.StatusOK, taskList{Items: items, Total: page.Total, Limit: opts.Limit, Offset: opts.Offset})
}

func validStatus(s string) bool {
	for _, v := range tasks.Statuses {
		if v == s {
			return true
		}
	}
	return false
}
