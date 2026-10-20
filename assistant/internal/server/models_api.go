package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"assistant/internal/llmcalls"
	"assistant/internal/tasks"
)

type callList struct {
	Items []llmcalls.Call `json:"items"`
}

// listTaskCalls returns every attempt a task made to call a model, oldest
// first, including the failed ones a fallback went past.
func (d Deps) listTaskCalls(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := d.Tasks.Get(r.Context(), id); err != nil {
		if errors.Is(err, tasks.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "task not found")
			return
		}
		d.internalError(w, r, "get task", err)
		return
	}
	calls, err := d.Calls.ForTask(r.Context(), id)
	if err != nil {
		d.internalError(w, r, "list llm calls", err)
		return
	}
	writeJSON(w, http.StatusOK, callList{Items: calls})
}

type usageList struct {
	Items []llmcalls.Usage `json:"items"`
}

// usage sums successful model calls per user and model, optionally from the
// time given as since (RFC 3339).
func (d Deps) usage(w http.ResponseWriter, r *http.Request) {
	var since *time.Time
	if v := r.URL.Query().Get("since"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, "since must be a timestamp such as 2026-10-01T00:00:00Z")
			return
		}
		since = &parsed
	}
	items, err := d.Calls.UsageByUserAndModel(r.Context(), since)
	if err != nil {
		d.internalError(w, r, "usage", err)
		return
	}
	writeJSON(w, http.StatusOK, usageList{Items: items})
}

// modelResponse is a configured model as the API shows it: never the URL of its
// provider, which can be an address inside the network.
type modelResponse struct {
	ID               string     `json:"id"`
	Provider         string     `json:"provider"`
	Tags             []string   `json:"tags"`
	Priority         int        `json:"priority"`
	Available        bool       `json:"available"`
	UnavailableUntil *time.Time `json:"unavailable_until"`
	// UnavailableReason is "cooldown" or "missing_credentials"; left out while
	// the model is available. It never says where a credential comes from.
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

type modelList struct {
	Items []modelResponse `json:"items"`
}

func (d Deps) listModels(w http.ResponseWriter, r *http.Request) {
	status := d.Models.Status()
	out := modelList{Items: make([]modelResponse, 0, len(status))}
	for _, s := range status {
		m := modelResponse{ID: s.ID, Provider: s.Provider, Tags: s.Tags, Priority: s.Priority, Available: s.Available}
		if !s.Available {
			m.UnavailableReason = s.Reason
			if !s.Until.IsZero() {
				until := s.Until
				m.UnavailableUntil = &until
			}
		}
		out.Items = append(out.Items, m)
	}
	writeJSON(w, http.StatusOK, out)
}
