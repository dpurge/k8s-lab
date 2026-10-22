package server

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"time"

	"knowledge/internal/llmcalls"
	"knowledge/internal/modelrouter"
)

const (
	defaultAggregateWindow = 24 * time.Hour
	defaultAttemptsLimit   = 25
	maxAttemptsLimit       = 200
)

// CallQueries is what the model-call pages read: the attempts and their
// summaries. *llmcalls.Store implements it; tests give a fake.
type CallQueries interface {
	List(ctx context.Context, f llmcalls.Filter, limit, offset int) ([]llmcalls.Attempt, int, error)
	Aggregates(ctx context.Context, since time.Time, groupBy string) ([]llmcalls.Aggregate, error)
}

// SetLLMAdmin gives the server what the model pages show: the call records and
// the configured models with their availability. Call it before Router. The
// pages are open to any signed-in user until knowledge has roles.
func (s *Server) SetLLMAdmin(calls CallQueries, models func() []modelrouter.ModelInfo) {
	s.llmCalls, s.llmModels = calls, models
}

// adminModels lists the configured models. It never carries a base URL, a key,
// the name of a key variable or the provider's own name for a model.
func (s *Server) adminModels(w http.ResponseWriter, r *http.Request) {
	models := []modelrouter.ModelInfo{}
	if s.llmModels != nil {
		if got := s.llmModels(); got != nil {
			models = got
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// adminLLMAggregates summarizes the model attempts of a window, per model or per
// kind and model. A configured model without attempts in the window is not
// listed: the models list has it.
func (s *Server) adminLLMAggregates(w http.ResponseWriter, r *http.Request) {
	if s.llmCalls == nil {
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "model call records are not available")
		return
	}
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
		writeErr(w, http.StatusInternalServerError, "internal_error", "could not read the model call records")
		return
	}
	if items == nil {
		items = []llmcalls.Aggregate{} // an array, never null
	}
	writeJSON(w, http.StatusOK, map[string]any{"since": since.UTC().Format(time.RFC3339), "group_by": groupBy, "items": items})
}

// adminLLMCalls lists the model attempts, newest first, a page at a time. It
// returns the table's columns that are configured ids, numbers and the outcome,
// and nothing else: no prompt, reply, text or vector, no address.
func (s *Server) adminLLMCalls(w http.ResponseWriter, r *http.Request) {
	if s.llmCalls == nil {
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "model call records are not available")
		return
	}
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
	offset := 0
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "validation_error", "offset must be an integer, 0 or more")
			return
		}
		offset = n
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
	items, total, err := s.llmCalls.List(r.Context(), filter, limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "could not read the model call records")
		return
	}
	if items == nil {
		items = []llmcalls.Attempt{} // an array, never null
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}
