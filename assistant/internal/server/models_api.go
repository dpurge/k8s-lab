package server

import (
	"errors"
	"net/http"
	"strconv"
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

// parseSince reads the optional since parameter (RFC 3339). It writes the
// error response itself and reports false when the value is not a timestamp.
func parseSince(w http.ResponseWriter, r *http.Request) (*time.Time, bool) {
	v := r.URL.Query().Get("since")
	if v == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339, v)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "since must be a timestamp such as 2026-10-01T00:00:00Z")
		return nil, false
	}
	return &parsed, true
}

// usage sums successful model calls per user, provider and model, optionally
// from the time given as since (RFC 3339).
func (d Deps) usage(w http.ResponseWriter, r *http.Request) {
	since, ok := parseSince(w, r)
	if !ok {
		return
	}
	items, err := d.Calls.UsageByUserAndModel(r.Context(), since)
	if err != nil {
		d.internalError(w, r, "usage", err)
		return
	}
	writeJSON(w, http.StatusOK, usageList{Items: items})
}

// The attempts list is paged like the task list: limit and offset, and the
// total so a pager knows how many pages there are.
const (
	defaultAttemptsLimit = 25
	maxAttemptsLimit     = 200
)

type attemptList struct {
	Items  []llmcalls.Call `json:"items"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

// listAttempts returns every attempt to call a model, newest first, failed ones
// included, optionally from the time given as since (RFC 3339).
func (d Deps) listAttempts(w http.ResponseWriter, r *http.Request) {
	since, ok := parseSince(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, offset := defaultAttemptsLimit, 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxAttemptsLimit {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, "limit must be a number from 1 to "+strconv.Itoa(maxAttemptsLimit))
			return
		}
		limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, "offset must be a number, 0 or more")
			return
		}
		offset = n
	}
	items, total, err := d.Calls.Attempts(r.Context(), since, limit, offset)
	if err != nil {
		d.internalError(w, r, "list attempts", err)
		return
	}
	writeJSON(w, http.StatusOK, attemptList{Items: items, Total: total, Limit: limit, Offset: offset})
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
