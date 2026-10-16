package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"assistant/internal/tasks"
	"assistant/internal/tasksteps"
)

type newMessageRequest struct {
	Message json.RawMessage `json:"message"`
}

// answerTask delivers the user's answer to a task whose run is waiting for it.
// The task goes back to the queue; the worker resumes the run, so this answers
// at once and the model's slow work happens in the background.
func (d Deps) answerTask(w http.ResponseWriter, r *http.Request) {
	var req newMessageRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	if len(req.Message) == 0 || strings.TrimSpace(string(req.Message)) == "null" {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "message is required and must not be null")
		return
	}
	t, err := d.Tasks.Resume(r.Context(), chi.URLParam(r, "id"), req.Message)
	switch {
	case errors.Is(err, tasks.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "task not found")
	case errors.Is(err, tasks.ErrNotWaiting):
		writeError(w, http.StatusConflict, codeConflict, "the task is not waiting for an answer (it may already have been answered)")
	case err != nil:
		d.internalError(w, r, "answer task", err)
	default:
		writeJSON(w, http.StatusOK, d.respond(t))
	}
}

type stepList struct {
	Items []tasksteps.Step `json:"items"`
}

// listTaskSteps returns the trace of a task's run, oldest first.
func (d Deps) listTaskSteps(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := d.Tasks.Get(r.Context(), id); err != nil {
		if errors.Is(err, tasks.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "task not found")
			return
		}
		d.internalError(w, r, "get task", err)
		return
	}
	steps, err := d.Steps.ForTask(r.Context(), id)
	if err != nil {
		d.internalError(w, r, "list task steps", err)
		return
	}
	writeJSON(w, http.StatusOK, stepList{Items: steps})
}
