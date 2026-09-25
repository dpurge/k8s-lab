// Generate Vocabulary/Generate Models endpoints (see
// specs/features/phraseforge-generate-vocab-models-from-text.md and
// dialog-vocabulary-models-generation.md): POST
// /api/v1/texts/{id}/generate-vocabulary, .../generate-models, and their
// /api/v1/dialogs/{id}/... mirrors validate the caller can edit the
// source's language, then enqueue the corresponding background job — the
// actual LLM call and list upsert happen asynchronously in
// phraseforge/internal/generate's job handlers, mirroring apiIngest's own
// "authorize, enqueue, 202" shape (see ingest.go).
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"phraseforge/internal/ai"
	"phraseforge/internal/generate"
	"phraseforge/internal/i18n"
	"phraseforge/internal/jobs"
)

// generateJobPayload mirrors generate's own (unexported) job payload shape
// field-for-field — a job payload is a wire contract between independently-
// versioned packages, not a shared Go type, matching ingestJobPayload's own
// precedent (ingest.go). resource_type/resource_id is the current shape;
// text_id is omitted here (left unset) since every request that reaches
// this handler is on the current API — only pre-existing job rows carry
// the old text_id-only shape, and generate.Service's own payload type
// handles decoding those for backward compatibility.
type generateJobPayload struct {
	ResourceType string `json:"resource_type"`
	ResourceID   int64  `json:"resource_id"`
	UserID       int64  `json:"user_id"`
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
// background job carrying the text's resource_type/resource_id (the job
// handler reads the text's own body/language/script itself — see
// generate.Service.HandleGenerateVocab/HandleGenerateModels) and the
// caller's user id (becomes a newly created list's owner).
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

	payload, err := json.Marshal(generateJobPayload{ResourceType: "text", ResourceID: t.ID, UserID: u.ID})
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

// apiGenerateVocabularyFromDialog handles POST
// /api/v1/dialogs/{id}/generate-vocabulary.
func (s *Server) apiGenerateVocabularyFromDialog(w http.ResponseWriter, r *http.Request) {
	s.apiGenerateFromDialog(w, r, generate.KindGenerateVocabFromDialog)
}

// apiGenerateModelsFromDialog handles POST /api/v1/dialogs/{id}/generate-models.
func (s *Server) apiGenerateModelsFromDialog(w http.ResponseWriter, r *http.Request) {
	s.apiGenerateFromDialog(w, r, generate.KindGenerateModelsFromDialog)
}

// apiGenerateFromDialog mirrors apiGenerateFromText exactly — see that
// method's doc comment — including the same CanEdit(dialog's language)
// authorization check.
func (s *Server) apiGenerateFromDialog(w http.ResponseWriter, r *http.Request, kind string) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	d, err := s.dialogs.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "not_found", "dialog not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, d.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, "dialogs.err_no_edit_language"))
		return
	}

	payload, err := json.Marshal(generateJobPayload{ResourceType: "dialog", ResourceID: d.ID, UserID: u.ID})
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

// --- background-generate-title-transcription-translation: single-click
// Generate Title/Transcribe/Translate on the Text/Dialog View page ---
//
// These reuse buildBackfillPayload/generatePayload directly (no new payload
// type, per this feature's Approach) — the same ai.KindLLMGenerate job kind
// import already enqueues for backfill/ingest, just for one decision at a
// time instead of a whole decideBackfill batch. The job's own writeback
// (ai.Service.writeback) is what actually enforces the blank-check —
// nothing here re-checks blankness before enqueueing, matching how the
// button itself is only shown when the field is blank (a stale button click
// racing a since-completed edit still safely no-ops at writeback time).

// apiGenerateTitleFromText handles POST /api/v1/texts/{id}/generate-title.
func (s *Server) apiGenerateTitleFromText(w http.ResponseWriter, r *http.Request) {
	s.apiGenerateFieldFromText(w, r, "title", "")
}

// apiGenerateTranscriptionFromText handles POST
// /api/v1/texts/{id}/generate-transcription.
func (s *Server) apiGenerateTranscriptionFromText(w http.ResponseWriter, r *http.Request) {
	s.apiGenerateFieldFromText(w, r, "transcription", "")
}

// apiGenerateTranslationFromText handles POST
// /api/v1/texts/{id}/generate-translation, body {"locale": "en"}.
func (s *Server) apiGenerateTranslationFromText(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Locale string `json:"locale"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if !i18n.IsValid(req.Locale) {
		writeErr(w, http.StatusBadRequest, "invalid_locale", "locale is not a supported site locale")
		return
	}
	s.apiGenerateFieldFromText(w, r, "translation", req.Locale)
}

// apiGenerateFieldFromText is apiGenerateTitleFromText/
// apiGenerateTranscriptionFromText/apiGenerateTranslationFromText's shared
// body: same CanEdit(text's language)/404/403 shape as apiGenerateFromText.
// locale is "" for title/transcription (buildBackfillPayload ignores it for
// those kinds).
func (s *Server) apiGenerateFieldFromText(w http.ResponseWriter, r *http.Request, kind, locale string) {
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
	payload := buildBackfillPayload(backfillDecision{Kind: kind, Locale: locale}, "text", t.ID, t.Language, t.Body)
	raw, err := json.Marshal(payload)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	jobID, err := s.jobs.Enqueue(r.Context(), ai.JobKind(kind), jobs.PriorityBackground, raw)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": jobID})
}

// apiGenerateTitleFromDialog handles POST /api/v1/dialogs/{id}/generate-title.
func (s *Server) apiGenerateTitleFromDialog(w http.ResponseWriter, r *http.Request) {
	s.apiGenerateFieldFromDialog(w, r, "title", "")
}

// apiGenerateTranscriptionFromDialog handles POST
// /api/v1/dialogs/{id}/generate-transcription.
func (s *Server) apiGenerateTranscriptionFromDialog(w http.ResponseWriter, r *http.Request) {
	s.apiGenerateFieldFromDialog(w, r, "transcription", "")
}

// apiGenerateTranslationFromDialog handles POST
// /api/v1/dialogs/{id}/generate-translation, body {"locale": "en"}.
func (s *Server) apiGenerateTranslationFromDialog(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Locale string `json:"locale"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if !i18n.IsValid(req.Locale) {
		writeErr(w, http.StatusBadRequest, "invalid_locale", "locale is not a supported site locale")
		return
	}
	s.apiGenerateFieldFromDialog(w, r, "translation", req.Locale)
}

// apiGenerateFieldFromDialog mirrors apiGenerateFieldFromText — see that
// method's doc comment.
func (s *Server) apiGenerateFieldFromDialog(w http.ResponseWriter, r *http.Request, kind, locale string) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	d, err := s.dialogs.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "not_found", "dialog not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, d.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, "dialogs.err_no_edit_language"))
		return
	}
	payload := buildBackfillPayload(backfillDecision{Kind: kind, Locale: locale}, "dialog", d.ID, d.Language, d.Body)
	raw, err := json.Marshal(payload)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	jobID, err := s.jobs.Enqueue(r.Context(), ai.JobKind(kind), jobs.PriorityBackground, raw)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": jobID})
}

// findItemByPosition returns the item at position within items, and whether
// one was found — a linear scan rather than an index lookup, since a
// position isn't guaranteed to equal its slice index in every caller (it
// always does today, given DeleteItem's renumbering, but this doesn't rely
// on that invariant holding forever).
func findVocabItemByPosition(items []vocabItem, position int) (vocabItem, bool) {
	for _, it := range items {
		if it.Position == position {
			return it, true
		}
	}
	return vocabItem{}, false
}

// vocabItem is the minimal shape findVocabItemByPosition needs, satisfied by
// both vocabulary.Item and models.Item (structurally, via the two small
// adapters below) — kept private to this file, not a shared package type,
// since it exists only to make one generic-ish helper serve both without
// duplicating the scan loop.
type vocabItem struct {
	Position      int
	Phrase        string
	Transcription string
}

// apiGenerateVocabItemTranscription handles POST
// /api/v1/vocabulary/{id}/items/{position}/generate-transcription.
func (s *Server) apiGenerateVocabItemTranscription(w http.ResponseWriter, r *http.Request) {
	s.apiGenerateVocabItemField(w, r, "transcription", "")
}

// apiGenerateVocabItemTranslation handles POST
// /api/v1/vocabulary/{id}/items/{position}/generate-translation, body
// {"locale": "en"}.
func (s *Server) apiGenerateVocabItemTranslation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Locale string `json:"locale"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if !i18n.IsValid(req.Locale) {
		writeErr(w, http.StatusBadRequest, "invalid_locale", "locale is not a supported site locale")
		return
	}
	s.apiGenerateVocabItemField(w, r, "translation", req.Locale)
}

// apiGenerateVocabItemField is apiGenerateVocabItemTranscription/
// apiGenerateVocabItemTranslation's shared body — same CanEdit(list's
// language)/404/403 shape as apiGenerateFromText, plus a position lookup for
// the item's current phrase (the generation source, and the stale-target
// guard the job's writeback checks against — see
// vocabulary.Store.SetItemTranscriptionIfBlank's doc comment).
func (s *Server) apiGenerateVocabItemField(w http.ResponseWriter, r *http.Request, kind, locale string) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	position, err := strconv.Atoi(chi.URLParam(r, "position"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_position", err.Error())
		return
	}
	l, err := s.vocab.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, "vocabulary.err_no_edit_language"))
		return
	}
	items, err := s.vocab.Items(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	converted := make([]vocabItem, len(items))
	for i, it := range items {
		converted[i] = vocabItem{Position: it.Position, Phrase: it.Phrase, Transcription: it.Transcription}
	}
	item, found := findVocabItemByPosition(converted, position)
	if !found {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary item not found")
		return
	}
	payload := buildBackfillPayload(backfillDecision{Kind: kind, Locale: locale}, "vocabulary_item", l.ID, l.Language, item.Phrase)
	payload.ItemPosition = position
	raw, err := json.Marshal(payload)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	jobID, err := s.jobs.Enqueue(r.Context(), ai.JobKind(kind), jobs.PriorityBackground, raw)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": jobID})
}

// apiGenerateModelsItemTranscription handles POST
// /api/v1/models/{id}/items/{position}/generate-transcription.
func (s *Server) apiGenerateModelsItemTranscription(w http.ResponseWriter, r *http.Request) {
	s.apiGenerateModelsItemField(w, r, "transcription", "")
}

// apiGenerateModelsItemTranslation handles POST
// /api/v1/models/{id}/items/{position}/generate-translation, body
// {"locale": "en"}.
func (s *Server) apiGenerateModelsItemTranslation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Locale string `json:"locale"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if !i18n.IsValid(req.Locale) {
		writeErr(w, http.StatusBadRequest, "invalid_locale", "locale is not a supported site locale")
		return
	}
	s.apiGenerateModelsItemField(w, r, "translation", req.Locale)
}

// apiGenerateModelsItemField mirrors apiGenerateVocabItemField — see that
// method's doc comment.
func (s *Server) apiGenerateModelsItemField(w http.ResponseWriter, r *http.Request, kind, locale string) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_id", err.Error())
		return
	}
	position, err := strconv.Atoi(chi.URLParam(r, "position"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_position", err.Error())
		return
	}
	l, err := s.models.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "not_found", "models list not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, "models.err_no_edit_language"))
		return
	}
	items, err := s.models.Items(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	converted := make([]vocabItem, len(items))
	for i, it := range items {
		converted[i] = vocabItem{Position: it.Position, Phrase: it.Phrase, Transcription: it.Transcription}
	}
	item, found := findVocabItemByPosition(converted, position)
	if !found {
		writeErr(w, http.StatusNotFound, "not_found", "models item not found")
		return
	}
	payload := buildBackfillPayload(backfillDecision{Kind: kind, Locale: locale}, "models_item", l.ID, l.Language, item.Phrase)
	payload.ItemPosition = position
	raw, err := json.Marshal(payload)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	jobID, err := s.jobs.Enqueue(r.Context(), ai.JobKind(kind), jobs.PriorityBackground, raw)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": jobID})
}
