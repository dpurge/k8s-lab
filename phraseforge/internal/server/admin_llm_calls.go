package server

import (
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"phraseforge/internal/llmcalls"
)

const (
	defaultAggregateWindow = 24 * time.Hour
	defaultAttemptsLimit   = 50
	maxAttemptsLimit       = 200
	// maxPurgeDays keeps the cutoff arithmetic far from overflowing.
	maxPurgeDays = 36500
)

// apiAdminLLMAggregates summarizes the model attempts of a window, per model
// or per kind and model. A configured model without attempts in the window is
// not listed: the models list has it.
func (s *Server) apiAdminLLMAggregates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := time.Now().Add(-defaultAggregateWindow)
	if v := q.Get("since"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "validation_error", "since must be an RFC 3339 time, e.g. 2026-10-20T12:00:00Z")
			return
		}
		since = parsed
	}
	groupBy := llmcalls.GroupByModel
	if v := q.Get("group_by"); v != "" {
		if v != llmcalls.GroupByModel && v != llmcalls.GroupByKindModel {
			writeErr(w, http.StatusBadRequest, "validation_error", "group_by must be model or kind_model")
			return
		}
		groupBy = v
	}
	items, err := s.llmCalls.Aggregates(r.Context(), since, groupBy)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"since": since.UTC().Format(time.RFC3339), "group_by": groupBy, "items": items})
}

// apiAdminLLMCalls lists the latest model attempts, newest first. It returns
// the table's columns and nothing else: the table holds no prompt or reply.
func (s *Server) apiAdminLLMCalls(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := defaultAttemptsLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxAttemptsLimit {
			writeErr(w, http.StatusBadRequest, "validation_error", "limit must be an integer from 1 to "+strconv.Itoa(maxAttemptsLimit))
			return
		}
		limit = n
	}
	filter := llmcalls.Filter{ModelID: q.Get("model_id"), Outcome: q.Get("outcome"), Kind: q.Get("kind")}
	if v := q.Get("since"); v != "" {
		since, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "validation_error", "since must be an RFC 3339 time, e.g. 2026-10-20T12:00:00Z")
			return
		}
		filter.Since = since
	}
	if filter.Outcome != "" && !slices.Contains(llmcalls.Outcomes, filter.Outcome) {
		writeErr(w, http.StatusBadRequest, "validation_error", "outcome must be one of ok, error, timeout, rate_limited, canceled")
		return
	}
	items, err := s.llmCalls.List(r.Context(), filter, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// apiAdminPurgeLLMCalls deletes the attempt records that started more than
// older_than_days days ago (0 deletes all of them). The parameter is required so
// that a bare DELETE never empties the table.
func (s *Server) apiAdminPurgeLLMCalls(w http.ResponseWriter, r *http.Request) {
	days, err := strconv.Atoi(r.URL.Query().Get("older_than_days"))
	if err != nil || days < 0 || days > maxPurgeDays {
		writeErr(w, http.StatusBadRequest, "validation_error", "older_than_days is required: an integer from 0 to "+strconv.Itoa(maxPurgeDays))
		return
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	deleted, err := s.llmCalls.DeleteOlderThan(r.Context(), cutoff)
	slog.Info("llm calls purged on request", "deleted", deleted, "cutoff", cutoff.UTC().Format(time.RFC3339))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "older_than_days": days})
}
