package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"

	"github.com/go-chi/chi/v5"

	"phraseforge/internal/catalog"
	"phraseforge/internal/i18n"
	"phraseforge/internal/tags"
	"phraseforge/internal/vocabulary"
)

// vocabularyAppI18nKeys mirrors textsAppI18nKeys/dialogsAppI18nKeys for the
// Vocabulary SPA shell.
var vocabularyAppI18nKeys = []string{
	"vocabulary.title", "texts.new", "vocabulary.empty", "texts.no_access",
	"texts.tag_filter", "texts.tag_filter_clear",
	"vocabulary.new_title", "texts.field_title", "texts.field_language", "texts.field_script",
	"texts.field_tags", "texts.field_tags_hint", "vocabulary.new_hint",
	"texts.save", "texts.save_changes", "llm.transcribe", "llm.translate",
	"vocabulary.edit_title", "texts.back", "texts.edit", "texts.delete",
	"vocabulary.delete_confirm", "vocabulary.delete_item_confirm",
	"vocabulary.add_item", "vocabulary.edit_item", "vocabulary.save_item", "vocabulary.cancel_edit",
	"vocabulary.col_phrase", "vocabulary.col_grammar", "vocabulary.col_transcription",
	"vocabulary.col_translation", "vocabulary.col_notes", "vocabulary.col_actions",
	"texts.export", "texts.import",
	"texts.import_result_imported", "texts.import_result_deleted", "texts.import_result_unchanged",
	"texts.import_result_errors", "texts.import_errors_close", "texts.err_import_file_read",
}

func vocabularyAppI18n(loc string) map[string]string {
	out := make(map[string]string, len(vocabularyAppI18nKeys))
	for _, k := range vocabularyAppI18nKeys {
		out[k] = i18n.T(loc, k)
	}
	return out
}

// --- JSON API (phraseforge-spa-vocabulary) ---

type apiVocabSummary struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Language  string   `json:"language"`
	Script    string   `json:"script"`
	Tags      []string `json:"tags"`
	ItemCount int      `json:"itemCount"`
	CreatedAt string   `json:"createdAt"`
}

type apiVocabItem struct {
	Position      int    `json:"position"`
	Phrase        string `json:"phrase"`
	Grammar       string `json:"grammar"`
	Transcription string `json:"transcription"`
	Translation   string `json:"translation"`
	Notes         string `json:"notes"`
}

// apiVocabDetail serves both the client's View and Manage states — Manage
// needs every field View does, plus canEdit to decide whether to show the
// edit form/table controls at all.
type apiVocabDetail struct {
	ID              int64          `json:"id"`
	Title           string         `json:"title"`
	Language        string         `json:"language"`
	Script          string         `json:"script"`
	Tags            []string       `json:"tags"`
	CanEdit         bool           `json:"canEdit"`
	ScriptDirection string         `json:"scriptDirection"`
	ScriptEnlarged  bool           `json:"scriptEnlarged"`
	Markdown        string         `json:"markdown"`
	Items           []apiVocabItem `json:"items"`
}

type apiVocabListRequest struct {
	Title    string `json:"title"`
	Language string `json:"language"`
	Script   string `json:"script"`
	Tags     string `json:"tags"`
}

type apiVocabItemRequest struct {
	Phrase        string `json:"phrase"`
	Grammar       string `json:"grammar"`
	Transcription string `json:"transcription"`
	Translation   string `json:"translation"`
	Notes         string `json:"notes"`
}

// apiListVocabLists is handleVocabList's exact logic, JSON-encoded.
func (s *Server) apiListVocabLists(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	langs, all, err := s.roles.ViewableLanguages(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	languageFilter := r.URL.Query().Get("language")
	if languageFilter != "" && (all || slices.Contains(langs, languageFilter)) {
		langs, all = []string{languageFilter}, false
	}
	list, err := s.vocab.ListAll(r.Context(), langs, all)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	tagFilter := r.URL.Query().Get("tag")
	if tagFilter != "" {
		ids, err := s.tags.ResourceIDsWithTag(r.Context(), resourceTypeVocab, tagFilter)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		list = filterByID(list, ids, func(l vocabulary.List) int64 { return l.ID })
	}
	ids := make([]int64, len(list))
	for i, l := range list {
		ids[i] = l.ID
	}
	tagsByID, err := s.tags.ForMany(r.Context(), resourceTypeVocab, ids)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]apiVocabSummary, len(list))
	for i, l := range list {
		items, err := s.vocab.Items(r.Context(), l.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		out[i] = apiVocabSummary{
			ID: l.ID, Title: l.Title, Language: l.Language, Script: l.Script,
			Tags: tagsByID[l.ID], ItemCount: len(items), CreatedAt: l.CreatedAt.Format("Jan 2, 2006 · 15:04"),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// apiCreateVocabList is handleVocabCreate's exact logic.
func (s *Server) apiCreateVocabList(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var req apiVocabListRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, req.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, "vocabulary.err_no_edit_language"))
		return
	}
	id, err := s.vocab.Create(r.Context(), u.ID, req.Title, req.Language, req.Script)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.tags.SetFor(r.Context(), resourceTypeVocab, id, tags.Parse(req.Tags)); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// apiGetVocabList is handleVocabView's/handleVocabEditForm's shared logic
// (list + items + per-locale translations + tags + markdown) — serves both
// the client's View and Manage states from one response.
func (s *Server) apiGetVocabList(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	l, err := s.vocab.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	canView, err := s.roles.CanView(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canView {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	scriptMeta, err := catalog.GetScript(r.Context(), s.db, l.Script)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	items, err := s.vocab.Items(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	trans, err := s.vocab.Translations(r.Context(), id, u.Locale)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	listTags, err := s.tags.For(r.Context(), resourceTypeVocab, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	rows := make([]vocabViewRow, len(items))
	apiItems := make([]apiVocabItem, len(items))
	for i, it := range items {
		t := trans[it.Position]
		rows[i] = vocabViewRow{Item: it, Translation: t.Translation, Notes: t.Notes}
		apiItems[i] = apiVocabItem{
			Position: it.Position, Phrase: it.Phrase, Grammar: it.Grammar, Transcription: it.Transcription,
			Translation: t.Translation, Notes: t.Notes,
		}
	}
	writeJSON(w, http.StatusOK, apiVocabDetail{
		ID: l.ID, Title: l.Title, Language: l.Language, Script: l.Script,
		Tags: listTags, CanEdit: canEdit,
		ScriptDirection: scriptMeta.Direction, ScriptEnlarged: scriptMeta.Enlarged,
		Markdown: vocabularyMarkdown(l.Language, l.Script, rows),
		Items:    apiItems,
	})
}

// apiUpdateVocabList is handleVocabUpdate's exact logic (list metadata
// only — items are untouched by this endpoint).
func (s *Server) apiUpdateVocabList(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	l, err := s.vocab.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	var req apiVocabListRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Language != l.Language {
		canEditNew, err := s.roles.CanEdit(r.Context(), u.ID, req.Language)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		if !canEditNew {
			writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, "vocabulary.err_no_move_language"))
			return
		}
	}
	if err := s.vocab.UpdateMeta(r.Context(), id, req.Title, req.Language, req.Script); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.tags.SetFor(r.Context(), resourceTypeVocab, id, tags.Parse(req.Tags)); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// apiDeleteVocabList is handleVocabDelete's exact logic.
func (s *Server) apiDeleteVocabList(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	l, err := s.vocab.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	if err := s.vocab.Delete(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.tags.DeleteFor(r.Context(), resourceTypeVocab, id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiAddVocabItem is handleVocabItemCreate's exact logic.
func (s *Server) apiAddVocabItem(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	l, err := s.vocab.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	var req apiVocabItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	position, err := s.vocab.AddItem(r.Context(), id, req.Phrase, req.Grammar, req.Transcription)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if req.Translation != "" || req.Notes != "" {
		if err := s.vocab.SetTranslations(r.Context(), id, u.Locale,
			[]vocabulary.ItemTranslation{{Position: position, Translation: req.Translation, Notes: req.Notes}}, position+1); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"position": position})
}

// apiUpdateVocabItem is handleVocabItemUpdate's exact logic.
func (s *Server) apiUpdateVocabItem(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	position, err := strconv.Atoi(chi.URLParam(r, "position"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary item not found")
		return
	}
	l, err := s.vocab.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	var req apiVocabItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if err := s.vocab.UpdateItem(r.Context(), id, position, req.Phrase, req.Grammar, req.Transcription); err != nil {
		if err == vocabulary.ErrItemNotFound {
			writeErr(w, http.StatusNotFound, "not_found", "vocabulary item not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.vocab.SetTranslations(r.Context(), id, u.Locale,
		[]vocabulary.ItemTranslation{{Position: position, Translation: req.Translation, Notes: req.Notes}}, position+1); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"position": position})
}

// apiDeleteVocabItem is handleVocabItemDelete's exact logic.
func (s *Server) apiDeleteVocabItem(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	position, err := strconv.Atoi(chi.URLParam(r, "position"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary item not found")
		return
	}
	l, err := s.vocab.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	if err := s.vocab.DeleteItem(r.Context(), id, position); err != nil {
		if err == vocabulary.ErrItemNotFound {
			writeErr(w, http.StatusNotFound, "not_found", "vocabulary item not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
