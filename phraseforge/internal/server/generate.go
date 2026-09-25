// Generate Vocabulary/Generate Models endpoints (see
// specs/features/phraseforge-generate-vocab-models-from-text.md): both POST
// /api/v1/texts/{id}/generate-vocabulary and .../generate-models validate
// the caller can edit the text's language, then enqueue the corresponding
// background job — the actual LLM call and list upsert happen asynchronously
// in phraseforge/internal/generate's job handlers, mirroring apiIngest's own
// "authorize, enqueue, 202" shape (see ingest.go).
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"phraseforge/internal/generate"
	"phraseforge/internal/i18n"
	"phraseforge/internal/jobs"
)

// generateJobPayload mirrors generate's own (unexported) job payload shape
// field-for-field — a job payload is a wire contract between independently-
// versioned packages, not a shared Go type, matching ingestJobPayload's own
// precedent (ingest.go).
type generateJobPayload struct {
	TextID int64 `json:"text_id"`
	UserID int64 `json:"user_id"`
}

// apiGenerateVocabularyFromText handles POST
// /api/v1/texts/{id}/generate-vocabulary.
func (s *Server) apiGenerateVocabularyFromText(w http.ResponseWriter, r *http.Request) {
	s.apiGenerateFromText(w, r, generate.KindGenerateVocabFromText)
}

// apiGenerateModelsFromText handles POST /api/v1/texts/{id}/generate-models.
func (s *Server) apiGenerateModelsFromText(w http.ResponseWriter, r *http.Request) {
	s.apiGenerateFromText(w, r, generate.KindGenerateModelsFromText)
}

// apiGenerateFromText is apiGenerateVocabularyFromText/
// apiGenerateModelsFromText's shared body: same CanEdit(text's language)
// authorization as every other text-scoped action, then enqueue kind as a
// background job carrying just the text's id (the job handler reads the
// text's own body/language/script itself — see generate.Service.
// HandleGenerateVocabFromText/HandleGenerateModelsFromText) and the caller's
// user id (becomes a newly created list's owner).
func (s *Server) apiGenerateFromText(w http.ResponseWriter, r *http.Request, kind string) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	t, err := s.texts.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "not_found", "text not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, t.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, "texts.err_no_edit_language"))
		return
	}

	payload, err := json.Marshal(generateJobPayload{TextID: t.ID, UserID: u.ID})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	jobID, err := s.jobs.Enqueue(r.Context(), kind, jobs.PriorityBackground, payload)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": jobID})
}
