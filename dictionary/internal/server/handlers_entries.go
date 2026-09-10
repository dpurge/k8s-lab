package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"dictionary/internal/entries"
	"dictionary/internal/languages"
)

// entryResponse nests an entry's translations directly — a natural read
// shape, since a caller almost always wants both together.
type entryResponse struct {
	entries.Entry
	Translations []entries.Translation `json:"translations"`
}

func (s *Server) entryResponse(r *http.Request, e entries.Entry) (entryResponse, error) {
	tr, err := s.entries.TranslationsForEntry(r.Context(), e.ID)
	if err != nil {
		return entryResponse{}, err
	}
	return entryResponse{Entry: e, Translations: tr}, nil
}

type entryRequest struct {
	Phrase string   `json:"phrase"`
	Tags   []string `json:"tags"`
}

// handleCreateEntry implements POST /languages/{code}/entries (writer/admin).
func (s *Server) handleCreateEntry(w http.ResponseWriter, r *http.Request) {
	l, _ := languageFromContext(r.Context())
	var req entryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	e, err := s.entries.Create(r.Context(), l.Code, req.Phrase, req.Tags)
	if !s.writeEntryError(w, err) {
		return
	}
	resp, err := s.entryResponse(r, e)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

// handleListEntries implements GET /languages/{code}/entries (read-gated).
// An optional ?phrase= filter returns just that phrase's entries (there may
// be several — one per distinct tag set).
func (s *Server) handleListEntries(w http.ResponseWriter, r *http.Request) {
	l, _ := languageFromContext(r.Context())
	var phrase *string
	if p := r.URL.Query().Get("phrase"); p != "" {
		phrase = &p
	}
	list, err := s.entries.List(r.Context(), l.Code, phrase)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": list})
}

// handleGetEntry implements GET /languages/{code}/entries/{id} (read-gated).
func (s *Server) handleGetEntry(w http.ResponseWriter, r *http.Request) {
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
	resp, err := s.entryResponse(r, e)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleUpdateEntry implements PUT /languages/{code}/entries/{id} (writer/admin).
func (s *Server) handleUpdateEntry(w http.ResponseWriter, r *http.Request) {
	l, _ := languageFromContext(r.Context())
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "invalid entry id")
		return
	}
	var req entryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	e, err := s.entries.Update(r.Context(), l.Code, id, req.Phrase, req.Tags)
	if !s.writeEntryError(w, err) {
		return
	}
	resp, err := s.entryResponse(r, e)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleDeleteEntry implements DELETE /languages/{code}/entries/{id} (writer/admin).
func (s *Server) handleDeleteEntry(w http.ResponseWriter, r *http.Request) {
	l, _ := languageFromContext(r.Context())
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "invalid entry id")
		return
	}
	err = s.entries.Delete(r.Context(), l.Code, id)
	if errors.Is(err, entries.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeEntryError maps an entries-package error to an HTTP response and
// reports whether the caller should continue (false = already handled).
func (s *Server) writeEntryError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, entries.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, err.Error())
	case errors.Is(err, entries.ErrAlreadyExists), errors.Is(err, entries.ErrTranslationExists):
		writeError(w, http.StatusConflict, codeConflict, err.Error())
	case errors.Is(err, entries.ErrValidation), errors.Is(err, languages.ErrTagsInvalid), errors.Is(err, entries.ErrUnknownLanguage):
		writeError(w, http.StatusBadRequest, codeValidationFailed, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
	}
	return false
}

// ── Translations ─────────────────────────────────────────────────────────────

type translationRequest struct {
	LanguageCode string  `json:"language_code"`
	Text         string  `json:"text"`
	Notes        *string `json:"notes,omitempty"`
}

// handleAddTranslation implements POST /languages/{code}/entries/{id}/translations (writer/admin).
func (s *Server) handleAddTranslation(w http.ResponseWriter, r *http.Request) {
	entryID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "invalid entry id")
		return
	}
	var req translationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := s.entries.AddTranslation(r.Context(), entryID, req.LanguageCode, req.Text, req.Notes)
	if !s.writeEntryError(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// handleUpdateTranslation implements
// PUT /languages/{code}/entries/{id}/translations/{translationID} (writer/admin).
func (s *Server) handleUpdateTranslation(w http.ResponseWriter, r *http.Request) {
	entryID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "invalid entry id")
		return
	}
	translationID, err := strconv.ParseInt(chi.URLParam(r, "translationID"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "invalid translation id")
		return
	}
	var req struct {
		Text  string  `json:"text"`
		Notes *string `json:"notes,omitempty"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := s.entries.UpdateTranslation(r.Context(), entryID, translationID, req.Text, req.Notes)
	if !s.writeEntryError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleDeleteTranslation implements
// DELETE /languages/{code}/entries/{id}/translations/{translationID} (writer/admin).
func (s *Server) handleDeleteTranslation(w http.ResponseWriter, r *http.Request) {
	entryID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "invalid entry id")
		return
	}
	translationID, err := strconv.ParseInt(chi.URLParam(r, "translationID"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "invalid translation id")
		return
	}
	err = s.entries.DeleteTranslation(r.Context(), entryID, translationID)
	if errors.Is(err, entries.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
