package server

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"assistant/internal/tasks"
	"assistant/internal/workflows"
)

// cancelTask stops a task. A pending or waiting one is cancelled at once; a
// running one is told to stop and ends as cancelled within moments (202).
func (d Deps) cancelTask(w http.ResponseWriter, r *http.Request) {
	t, outcome, err := tasks.CancelOrStop(r.Context(), d.Tasks, d.Worker, chi.URLParam(r, "id"))
	switch {
	case err == nil && outcome == tasks.Stopping:
		writeJSON(w, http.StatusAccepted, d.respond(t))
	case err == nil:
		writeJSON(w, http.StatusOK, d.respond(t))
	case errors.Is(err, tasks.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "task not found")
	case errors.Is(err, tasks.ErrNotCancellable):
		writeError(w, http.StatusConflict, codeConflict, "only a pending, waiting or running task can be cancelled (this one may already be finished)")
	default:
		d.internalError(w, r, "cancel task", err)
	}
}

// removeTask deletes one task with its trace.
func (d Deps) removeTask(w http.ResponseWriter, r *http.Request) {
	err := d.Tasks.Remove(r.Context(), chi.URLParam(r, "id"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, tasks.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "task not found")
	case errors.Is(err, tasks.ErrRunning):
		writeError(w, http.StatusConflict, codeConflict, "the task is running: cancel it first")
	default:
		d.internalError(w, r, "remove task", err)
	}
}

type workflowList struct {
	Items []workflows.Info `json:"items"`
}

// listWorkflows lists what a task can name, so a form can offer exactly those.
func (d Deps) listWorkflows(w http.ResponseWriter, r *http.Request) {
	items := []workflows.Info{}
	if d.Workflows != nil {
		items = d.Workflows.Catalog()
	}
	writeJSON(w, http.StatusOK, workflowList{Items: items})
}

type removed struct {
	Removed int `json:"removed"`
}

// removeTasks deletes every task in one finished status.
func (d Deps) removeTasks(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	n, err := d.Tasks.RemoveFinished(r.Context(), status)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, removed{Removed: n})
	case errors.Is(err, tasks.ErrNotFinished):
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "status is required and must be one of succeeded, failed, cancelled: running and waiting tasks are never removed in bulk")
	default:
		d.internalError(w, r, "remove tasks", err)
	}
}

// retryTask queues a copy of a failed or cancelled task.
func (d Deps) retryTask(w http.ResponseWriter, r *http.Request) {
	t, err := d.Tasks.Retry(r.Context(), chi.URLParam(r, "id"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, d.respond(t))
	case errors.Is(err, tasks.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "task not found")
	case errors.Is(err, tasks.ErrNotRetryable):
		writeError(w, http.StatusConflict, codeConflict, "only a failed or cancelled task can be retried")
	default:
		d.internalError(w, r, "retry task", err)
	}
}
