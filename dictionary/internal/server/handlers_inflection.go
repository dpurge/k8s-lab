package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"dictionary/internal/entries"
	"dictionary/internal/inflection"
	"dictionary/internal/languages"
)

// writeInflectionError maps an inflection-package error to an HTTP response,
// same contract as writeEntryError.
func (s *Server) writeInflectionError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, inflection.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, err.Error())
	case errors.Is(err, inflection.ErrAlreadyExists):
		writeError(w, http.StatusConflict, codeConflict, err.Error())
	case errors.Is(err, inflection.ErrValidation), errors.Is(err, languages.ErrTagsInvalid):
		writeError(w, http.StatusBadRequest, codeValidationFailed, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
	}
	return false
}

// ── Templates ────────────────────────────────────────────────────────────────

type inflectionTemplateRequest struct {
	IndexTags []string `json:"index_tags"`
	Body      string   `json:"body"`
}

// handleCreateInflectionTemplate implements POST /languages/{code}/inflection-templates (writer/admin).
func (s *Server) handleCreateInflectionTemplate(w http.ResponseWriter, r *http.Request) {
	l, _ := languageFromContext(r.Context())
	var req inflectionTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := s.inflection.CreateTemplate(r.Context(), l.Code, req.IndexTags, req.Body)
	if !s.writeInflectionError(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// handleListInflectionTemplates implements GET /languages/{code}/inflection-templates (read-gated).
func (s *Server) handleListInflectionTemplates(w http.ResponseWriter, r *http.Request) {
	l, _ := languageFromContext(r.Context())
	list, err := s.inflection.ListTemplates(r.Context(), l.Code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": list})
}

// handleUpdateInflectionTemplate implements PUT /languages/{code}/inflection-templates/{id} (writer/admin).
func (s *Server) handleUpdateInflectionTemplate(w http.ResponseWriter, r *http.Request) {
	l, _ := languageFromContext(r.Context())
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "invalid template id")
		return
	}
	var req inflectionTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := s.inflection.UpdateTemplate(r.Context(), l.Code, id, req.IndexTags, req.Body)
	if !s.writeInflectionError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleDeleteInflectionTemplate implements DELETE /languages/{code}/inflection-templates/{id} (writer/admin).
func (s *Server) handleDeleteInflectionTemplate(w http.ResponseWriter, r *http.Request) {
	l, _ := languageFromContext(r.Context())
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "invalid template id")
		return
	}
	if err := s.inflection.DeleteTemplate(r.Context(), l.Code, id); !s.writeInflectionError(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Forms ────────────────────────────────────────────────────────────────────

// handleUpsertInflectionForms implements POST /languages/{code}/inflection-forms (writer/admin).
// Body: {"forms": [{"text": "am", "tags": ["V","praes","sg","1"]}, ...]}
func (s *Server) handleUpsertInflectionForms(w http.ResponseWriter, r *http.Request) {
	l, _ := languageFromContext(r.Context())
	var req struct {
		Forms []inflection.FormInput `json:"forms"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Forms) == 0 {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "forms must not be empty")
		return
	}
	saved, err := s.inflection.UpsertForms(r.Context(), l.Code, req.Forms)
	if !s.writeInflectionError(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"forms": saved})
}

// handleListInflectionForms implements GET /languages/{code}/inflection-forms (read-gated).
func (s *Server) handleListInflectionForms(w http.ResponseWriter, r *http.Request) {
	l, _ := languageFromContext(r.Context())
	list, err := s.inflection.ListForms(r.Context(), l.Code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"forms": list})
}

// handleDeleteInflectionForm implements DELETE /languages/{code}/inflection-forms/{id} (writer/admin).
func (s *Server) handleDeleteInflectionForm(w http.ResponseWriter, r *http.Request) {
	l, _ := languageFromContext(r.Context())
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "invalid form id")
		return
	}
	if err := s.inflection.DeleteForm(r.Context(), l.Code, id); !s.writeInflectionError(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Rendering ────────────────────────────────────────────────────────────────

// handleEntryInflection implements GET /languages/{code}/entries/{id}/inflection (read-gated).
// Optional ?tags=a,b,c overrides which tag combination to look up; defaults
// to the entry's own stored tags.
func (s *Server) handleEntryInflection(w http.ResponseWriter, r *http.Request) {
	l, _ := languageFromContext(r.Context())
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "invalid entry id")
		return
	}
	e, err := s.entries.Get(r.Context(), l.Code, id)
	if errors.Is(err, entries.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}

	requestTags := e.Tags
	if q := r.URL.Query().Get("tags"); q != "" {
		requestTags = strings.Split(q, ",")
	}

	tables, err := s.inflection.RenderTables(r.Context(), l.Code, requestTags)
	if !s.writeInflectionError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"phrase": e.Phrase, "tags": requestTags, "tables": tables,
	})
}
