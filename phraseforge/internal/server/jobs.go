package server

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"phraseforge/internal/i18n"
)

// jobsAppI18nKeys mirrors adminAppI18nKeys/modelsAppI18nKeys for the Jobs SPA
// shell. Delete reuses the shared texts.delete key rather than declaring its
// own, matching admin-app.js's own row-action buttons (see admin.go's
// adminAppI18nKeys), which reuse texts.edit/texts.delete instead of
// declaring admin-specific equivalents.
var jobsAppI18nKeys = []string{
	"jobs.title", "jobs.refresh", "jobs.clear", "jobs.clear_confirm", "jobs.clear_done",
	"jobs.col_kind", "jobs.col_priority", "jobs.col_status", "jobs.col_created", "jobs.col_error",
	"jobs.view", "jobs.retry", "texts.delete",
	"jobs.view_close", "jobs.view_id", "jobs.view_updated", "jobs.view_step",
	"jobs.view_payload", "jobs.view_result",
	"jobs.cancel", "jobs.cancel_confirm",
	"jobs.retry_confirm", "jobs.delete_confirm", "jobs.empty",
}

func jobsAppI18n(loc string) map[string]string {
	out := make(map[string]string, len(jobsAppI18nKeys))
	for _, k := range jobsAppI18nKeys {
		out[k] = i18n.T(loc, k)
	}
	return out
}

// apiJobSummary is the list endpoint's per-row shape — matches the Kind,
// Priority, Status, Created, Error columns jobs-app.js's table renders.
// CreatedAt is pre-formatted server-side, same convention as
// apiTextSummary/apiDialogSummary/etc.
type apiJobSummary struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Priority  string `json:"priority"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// apiListAdminJobs lists every job, most recent first (jobs.Service.List's
// existing 500-row cap) — writes the array directly, not wrapped in an
// envelope, since the client only ever needs the list itself.
func (s *Server) apiListAdminJobs(w http.ResponseWriter, r *http.Request) {
	list, err := s.jobs.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]apiJobSummary, len(list))
	for i, j := range list {
		out[i] = apiJobSummary{
			ID: j.ID, Kind: j.Kind, Priority: string(j.Priority), Status: string(j.Status),
			Error: j.Error, CreatedAt: j.CreatedAt.Format("Jan 2, 2006 · 15:04"),
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// apiGetAdminJob returns one job's full detail — payload/result/step/error
// included, unlike the list's summary shape — for the Jobs page's View
// dialog. jobs.Service.Get surfaces pgx.ErrNoRows on a missing id, which maps
// to 404 here rather than the generic 500 every other error gets.
func (s *Server) apiGetAdminJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	j, err := s.jobs.Get(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "not_found", "job not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, j)
}

// apiRetryAdminJob is jobs.Service.Retry's exact logic, JSON-encoded — its
// own error (job doesn't exist, or isn't failed) is a client mistake (the
// Jobs page only ever offers Retry on rows it already knows are failed), so
// it maps to 400 rather than 500.
func (s *Server) apiRetryAdminJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	newID, err := s.jobs.Retry(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": newID})
}

// apiDeleteAdminJob is jobs.Service.Delete's exact logic — same
// error-is-a-client-mistake reasoning as apiRetryAdminJob above (the Jobs
// page only offers Delete on rows it already knows are terminal).
func (s *Server) apiDeleteAdminJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.jobs.Delete(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiClearAdminJobs bulk-deletes every done job — see
// jobs.Service.DeleteAllDone's own doc comment on why only 'done' (not
// pending/running/failed/cancelled) is safe to clear without per-row review.
func (s *Server) apiClearAdminJobs(w http.ResponseWriter, r *http.Request) {
	count, err := s.jobs.DeleteAllDone(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": count})
}

// apiCancelAdminJob is jobs.Service.Cancel's exact logic — same
// error-is-a-client-mistake reasoning as apiRetryAdminJob/apiDeleteAdminJob
// above (the Jobs page only offers Cancel on rows it already knows are
// pending/running).
func (s *Server) apiCancelAdminJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.jobs.Cancel(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
