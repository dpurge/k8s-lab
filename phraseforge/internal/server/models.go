package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"

	"github.com/go-chi/chi/v5"

	"phraseforge/internal/catalog"
	"phraseforge/internal/i18n"
	"phraseforge/internal/models"
	"phraseforge/internal/pagination"
	"phraseforge/internal/tags"
)

// modelsAppI18nKeys mirrors vocabularyAppI18nKeys for the Models SPA shell.
var modelsAppI18nKeys = []string{
	"models.title", "texts.new", "models.empty", "texts.no_access",
	"texts.tag_filter", "texts.tag_filter_clear",
	"models.new_title", "texts.field_title", "texts.field_language", "texts.field_script",
	"texts.field_tags", "texts.field_tags_hint", "models.new_hint",
	"texts.save", "texts.save_changes", "llm.transcribe", "llm.translate",
	"models.edit_title", "texts.back", "texts.edit", "texts.delete",
	"models.delete_confirm", "models.delete_item_confirm",
	"models.add_item", "models.edit_item", "models.save_item", "models.cancel_edit",
	"models.col_phrase", "models.col_transcription", "models.col_translation", "models.col_actions",
	"texts.export", "texts.import",
	"texts.import_result_imported", "texts.import_result_deleted", "texts.import_result_unchanged",
	"texts.import_result_errors", "texts.import_errors_close", "texts.err_import_file_read",
	"texts.generate_transcription_started", "texts.generate_translation_started",
	"texts.pagination_previous", "texts.pagination_next",
}

func modelsAppI18n(loc string) map[string]string {
	out := make(map[string]string, len(modelsAppI18nKeys))
	for _, k := range modelsAppI18nKeys {
		out[k] = i18n.T(loc, k)
	}
	return out
}

// --- JSON API (phraseforge-spa-models) ---

type apiModelsSummary struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Language  string   `json:"language"`
	Script    string   `json:"script"`
	Tags      []string `json:"tags"`
	ItemCount int      `json:"itemCount"`
	CreatedAt string   `json:"createdAt"`
}

type apiModelsItem struct {
	Position      int    `json:"position"`
	Phrase        string `json:"phrase"`
	Transcription string `json:"transcription"`
	Translation   string `json:"translation"`
}

// apiModelsDetail serves both the client's View and Manage states — Manage
// needs every field View does, plus canEdit to decide whether to show the
// edit form/table controls at all.
type apiModelsDetail struct {
	ID              int64           `json:"id"`
	Title           string          `json:"title"`
	Language        string          `json:"language"`
	Script          string          `json:"script"`
	Tags            []string        `json:"tags"`
	CanEdit         bool            `json:"canEdit"`
	ScriptDirection string          `json:"scriptDirection"`
	ScriptEnlarged  bool            `json:"scriptEnlarged"`
	Markdown        string          `json:"markdown"`
	Items           []apiModelsItem `json:"items"`
}

type apiModelsListRequest struct {
	Title    string `json:"title"`
	Language string `json:"language"`
	Script   string `json:"script"`
	Tags     string `json:"tags"`
}

type apiModelsItemRequest struct {
	Phrase        string `json:"phrase"`
	Transcription string `json:"transcription"`
	Translation   string `json:"translation"`
}

// apiListModelsLists is handleModelsList's exact logic, JSON-encoded.
func (s *Server) apiListModelsLists(w http.ResponseWriter, r *http.Request) {
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
	var tagIDs []int64
	if tagFilter := r.URL.Query().Get("tag"); tagFilter != "" {
		tagIDs, err = s.tags.ResourceIDsWithTag(r.Context(), resourceTypeModels, tagFilter)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
	}
	list, hasMore, err := s.models.ListAllPage(r.Context(), langs, all, tagIDs, decodeCursorParam(r), pagination.DefaultLimit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	ids := make([]int64, len(list))
	for i, l := range list {
		ids[i] = l.ID
	}
	tagsByID, err := s.tags.ForMany(r.Context(), resourceTypeModels, ids)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]apiModelsSummary, len(list))
	for i, l := range list {
		items, err := s.models.Items(r.Context(), l.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		out[i] = apiModelsSummary{
			ID: l.ID, Title: l.Title, Language: l.Language, Script: l.Script,
			Tags: tagsByID[l.ID], ItemCount: len(items), CreatedAt: l.CreatedAt.Format("Jan 2, 2006 · 15:04"),
		}
	}
	resp := map[string]any{"items": out}
	if hasMore && len(list) > 0 {
		last := list[len(list)-1]
		resp["nextCursor"] = pagination.Encode(pagination.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	writeJSON(w, http.StatusOK, resp)
}

// apiCreateModelsList is handleModelsCreate's exact logic.
func (s *Server) apiCreateModelsList(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var req apiModelsListRequest
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
		writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, "models.err_no_edit_language"))
		return
	}
	id, err := s.models.Create(r.Context(), u.ID, req.Title, req.Language, req.Script)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.tags.SetFor(r.Context(), resourceTypeModels, id, tags.Parse(req.Tags)); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// apiGetModelsList is handleModelsView's/handleModelsEditForm's shared logic
// (list + items + per-locale translations + tags + markdown) — serves both
// the client's View and Manage states from one response.
func (s *Server) apiGetModelsList(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	l, err := s.models.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	canView, err := s.roles.CanView(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canView {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
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
	items, err := s.models.Items(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	trans, err := s.models.Translations(r.Context(), id, u.Locale)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	listTags, err := s.tags.For(r.Context(), resourceTypeModels, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	rows := make([]modelsViewRow, len(items))
	apiItems := make([]apiModelsItem, len(items))
	for i, it := range items {
		t := trans[it.Position]
		rows[i] = modelsViewRow{Item: it, Translation: t.Translation}
		apiItems[i] = apiModelsItem{
			Position: it.Position, Phrase: it.Phrase, Transcription: it.Transcription,
			Translation: t.Translation,
		}
	}
	writeJSON(w, http.StatusOK, apiModelsDetail{
		ID: l.ID, Title: l.Title, Language: l.Language, Script: l.Script,
		Tags: listTags, CanEdit: canEdit,
		ScriptDirection: scriptMeta.Direction, ScriptEnlarged: scriptMeta.Enlarged,
		Markdown: modelsMarkdown(l.Language, l.Script, rows),
		Items:    apiItems,
	})
}

// apiUpdateModelsList is handleModelsUpdate's exact logic (list metadata
// only — items are untouched by this endpoint).
func (s *Server) apiUpdateModelsList(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	l, err := s.models.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	var req apiModelsListRequest
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
			writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, "models.err_no_move_language"))
			return
		}
	}
	if err := s.models.UpdateMeta(r.Context(), id, req.Title, req.Language, req.Script); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.tags.SetFor(r.Context(), resourceTypeModels, id, tags.Parse(req.Tags)); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// apiDeleteModelsList is handleModelsDelete's exact logic.
func (s *Server) apiDeleteModelsList(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	l, err := s.models.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	if err := s.models.Delete(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.tags.DeleteFor(r.Context(), resourceTypeModels, id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiAddModelsItem is handleModelsItemCreate's exact logic.
func (s *Server) apiAddModelsItem(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	l, err := s.models.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	var req apiModelsItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	position, err := s.models.AddItem(r.Context(), id, req.Phrase, req.Transcription)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.models.SetTranslation(r.Context(), id, position, u.Locale, req.Translation, position+1); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"position": position})
}

// apiUpdateModelsItem is handleModelsItemUpdate's exact logic.
func (s *Server) apiUpdateModelsItem(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	position, err := strconv.Atoi(chi.URLParam(r, "position"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "models item not found")
		return
	}
	l, err := s.models.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	var req apiModelsItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if err := s.models.UpdateItem(r.Context(), id, position, req.Phrase, req.Transcription); err != nil {
		if err == models.ErrItemNotFound {
			writeErr(w, http.StatusNotFound, "not_found", "models item not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.models.SetTranslation(r.Context(), id, position, u.Locale, req.Translation, position+1); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"position": position})
}

// apiDeleteModelsItem is handleModelsItemDelete's exact logic.
func (s *Server) apiDeleteModelsItem(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	position, err := strconv.Atoi(chi.URLParam(r, "position"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "models item not found")
		return
	}
	l, err := s.models.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusNotFound, "not_found", "models list not found")
		return
	}
	if err := s.models.DeleteItem(r.Context(), id, position); err != nil {
		if err == models.ErrItemNotFound {
			writeErr(w, http.StatusNotFound, "not_found", "models item not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
