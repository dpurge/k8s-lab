// Texts' half of export_import.go — see that file's package doc comment for
// why Texts and Dialogs each get their own types/handlers here instead of a
// shared abstraction.
package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"phraseforge/internal/auth"
	"phraseforge/internal/texts"
)

// apiTextExportItem is one exported text row — full round-trippable
// content, both site-locale translations included regardless of the
// viewer's own locale (unlike apiTextDetail's single Translation field).
type apiTextExportItem struct {
	ID            int64             `json:"id" yaml:"id"`
	Title         string            `json:"title" yaml:"title"`
	Body          string            `json:"body" yaml:"body"`
	Transcription string            `json:"transcription,omitempty" yaml:"transcription,omitempty"`
	Language      string            `json:"language" yaml:"language"`
	Script        string            `json:"script" yaml:"script"`
	IngestSource  string            `json:"ingest_source,omitempty" yaml:"ingest_source,omitempty"`
	Tags          []string          `json:"tags" yaml:"tags"`
	Translations  map[string]string `json:"translations,omitempty" yaml:"translations,omitempty"`
	CreatedAt     time.Time         `json:"created_at" yaml:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at" yaml:"updated_at"`
}
type apiTextExportResponse struct {
	Items []apiTextExportItem `json:"items" yaml:"items"`
}

// apiTextImportItem is one text import row. Only Body/Language/Script are
// required, and only for a brand-new (id-less) row — see apiImportTexts'
// doc comment for the full per-item rule. Translations is a partial map: an
// import only ever writes the locales present here, so omitting a locale
// leaves that locale's stored translation untouched (never cleared) on an
// update, and leaves it genuinely missing (eligible for backfill) on a
// create.
type apiTextImportItem struct {
	ID            int64             `json:"id,omitempty" yaml:"id,omitempty"`
	Title         string            `json:"title,omitempty" yaml:"title,omitempty"`
	Body          string            `json:"body,omitempty" yaml:"body,omitempty"`
	Transcription string            `json:"transcription,omitempty" yaml:"transcription,omitempty"`
	Language      string            `json:"language,omitempty" yaml:"language,omitempty"`
	Script        string            `json:"script,omitempty" yaml:"script,omitempty"`
	Tags          []string          `json:"tags,omitempty" yaml:"tags,omitempty"`
	Translations  map[string]string `json:"translations,omitempty" yaml:"translations,omitempty"`
	Delete        bool              `json:"delete,omitempty" yaml:"delete,omitempty"`
}
type apiTextImportRequest struct {
	Items []apiTextImportItem `json:"items" yaml:"items"`
}

// apiExportTexts handles GET /api/v1/texts/export: language and script are
// both required (requireExportFilter), narrowed further by an optional
// ALL-match tags= filter; language scoping still respects the caller's
// EditableLanguages the same way it always has — export's purpose is
// round-tripping content for hand-editing and re-import, the same
// authorization boundary create/update already enforce.
func (s *Server) apiExportTexts(w http.ResponseWriter, r *http.Request) {
	filter, ok := requireExportFilter(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	editLangs, editAll, err := s.roles.EditableLanguages(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	langs, all := exportScopeLanguages(editLangs, editAll, filter.Language)
	list, err := s.texts.List(r.Context(), langs, all)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	list = filterByScript(list, filter.Script, func(t texts.Text) string { return t.Script })
	if len(filter.Tags) > 0 {
		matching, err := s.tags.ResourceIDsWithAllTags(r.Context(), resourceTypeText, filter.Tags)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		list = filterByID(list, matching, func(t texts.Text) int64 { return t.ID })
	}
	ids := make([]int64, len(list))
	for i, t := range list {
		ids[i] = t.ID
	}
	tagsByID, err := s.tags.ForMany(r.Context(), resourceTypeText, ids)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	items := make([]apiTextExportItem, len(list))
	for i, t := range list {
		translations, err := s.exportTranslations(r.Context(), resourceTypeText, t.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		items[i] = apiTextExportItem{
			ID: t.ID, Title: t.Title, Body: t.Body, Transcription: t.Transcription,
			Language: t.Language, Script: t.Script, IngestSource: t.IngestSource,
			Tags: tagsByID[t.ID], Translations: translations,
			CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		}
	}
	w.Header().Set("Content-Disposition", `attachment; filename="phraseforge-texts-export.yaml"`)
	writeYAML(w, http.StatusOK, apiTextExportResponse{Items: items})
}

// apiImportTexts handles POST /api/v1/texts/import: per-item upsert with
// error collection (never fail-fast, never delete-and-replace-all — see
// specs/features/phraseforge-export-import.md's Acceptance Criteria). Each
// item, in order:
//  1. delete:true + id: deletes that row if the caller CanEdit its language
//     (a per-item error if not); a missing id is a no-op, not an error.
//  2. id present, not delete: found + CanEdit required (otherwise a
//     per-item error); identical-in-every-respect to what's stored is
//     "unchanged" (no write, no backfill); otherwise updated, then
//     backfilled.
//  3. id absent: body/language/script required, CanEdit(language) required;
//     always created, then backfilled.
//
// The body itself is stored exactly as given — import never re-runs
// ingest's process_text cleanup pipeline (see this feature's Problem/
// Motivation section for why).
func (s *Server) apiImportTexts(w http.ResponseWriter, r *http.Request) {
	body, ok := readImportBody(w, r)
	if !ok {
		return
	}
	var req apiTextImportRequest
	if err := yaml.Unmarshal(body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_yaml", err.Error())
		return
	}
	u := currentUser(r)
	res := importResult{Errors: []importError{}}
	for i, item := range req.Items {
		s.importTextItem(r.Context(), u, i, item, &res)
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) importTextItem(ctx context.Context, u auth.User, index int, item apiTextImportItem, res *importResult) {
	switch {
	case item.Delete:
		s.importDeleteText(ctx, u, index, item, res)
	case item.ID != 0:
		s.importUpdateText(ctx, u, index, item, res)
	default:
		s.importCreateText(ctx, u, index, item, res)
	}
}

func (s *Server) importDeleteText(ctx context.Context, u auth.User, index int, item apiTextImportItem, res *importResult) {
	if item.ID == 0 {
		res.Errors = append(res.Errors, importError{Index: index, Message: "delete requires id"})
		return
	}
	existing, err := s.texts.Get(ctx, item.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			res.Deleted++ // deleting an id that doesn't exist is a no-op, not an error
			return
		}
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	canEdit, err := s.roles.CanEdit(ctx, u.ID, existing.Language)
	if err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	if !canEdit {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: "not permitted to edit this text's language"})
		return
	}
	if err := s.texts.Delete(ctx, item.ID); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	if err := s.tags.DeleteFor(ctx, resourceTypeText, item.ID); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	res.Deleted++
}

func (s *Server) importUpdateText(ctx context.Context, u auth.User, index int, item apiTextImportItem, res *importResult) {
	existing, err := s.texts.Get(ctx, item.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: "text not found"})
			return
		}
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	canEdit, err := s.roles.CanEdit(ctx, u.ID, existing.Language)
	if err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	if !canEdit {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: "not permitted to edit this text's language"})
		return
	}
	// S1 fix: the create path already requires a non-blank body; the update
	// path must too — without this, an update that omits body silently
	// blanks a previously-good field instead of being rejected.
	if strings.TrimSpace(item.Body) == "" {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: "body is required"})
		return
	}

	incomingTags := normalizeTagList(item.Tags)
	existingTags, err := s.tags.For(ctx, resourceTypeText, item.ID)
	if err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	existingTranslations, err := s.existingTranslationsFor(ctx, resourceTypeText, item.ID, item.Translations)
	if err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	existingFields := importFields{
		Title: existing.Title, Body: existing.Body, Transcription: existing.Transcription,
		Language: existing.Language, Script: existing.Script,
		Tags: existingTags, Translations: existingTranslations,
	}
	incomingFields := importFields{
		Title: item.Title, Body: item.Body, Transcription: item.Transcription,
		Language: item.Language, Script: item.Script,
		Tags: incomingTags, Translations: item.Translations,
	}
	if importUnchanged(existingFields, incomingFields) {
		res.Unchanged++
		return
	}

	if item.Language != existing.Language {
		// S2 fix: roles.CanEdit returns true for an admin regardless of the
		// language string given (including blank), so without this explicit
		// check a blank/omitted language would sail past the re-check below
		// and hit the database as a raw constraint violation instead of a
		// clean per-item error.
		if strings.TrimSpace(item.Language) == "" {
			res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: "language must not be blank"})
			return
		}
		canEditNew, err := s.roles.CanEdit(ctx, u.ID, item.Language)
		if err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
			return
		}
		if !canEditNew {
			res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: "not permitted to move this text to that language"})
			return
		}
	}

	if err := s.texts.Update(ctx, item.ID, item.Title, item.Body, item.Transcription, item.Language, item.Script); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	if err := s.tags.SetFor(ctx, resourceTypeText, item.ID, incomingTags); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	for locale, text := range item.Translations {
		if err := s.translations.Set(ctx, resourceTypeText, item.ID, locale, text); err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
			return
		}
	}
	if err := s.enqueueBackfill(ctx, resourceTypeText, item.ID, item.Language, item.Title, item.Body, item.Transcription, item.Translations); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	res.Imported++
}

func (s *Server) importCreateText(ctx context.Context, u auth.User, index int, item apiTextImportItem, res *importResult) {
	if strings.TrimSpace(item.Body) == "" || strings.TrimSpace(item.Language) == "" || strings.TrimSpace(item.Script) == "" {
		res.Errors = append(res.Errors, importError{Index: index, Message: "body, language, and script are required"})
		return
	}
	canEdit, err := s.roles.CanEdit(ctx, u.ID, item.Language)
	if err != nil {
		res.Errors = append(res.Errors, importError{Index: index, Message: err.Error()})
		return
	}
	if !canEdit {
		res.Errors = append(res.Errors, importError{Index: index, Message: "not permitted to edit this text's language"})
		return
	}
	id, err := s.texts.Create(ctx, u.ID, item.Title, item.Body, item.Transcription, item.Language, item.Script, "")
	if err != nil {
		res.Errors = append(res.Errors, importError{Index: index, Message: err.Error()})
		return
	}
	if err := s.tags.SetFor(ctx, resourceTypeText, id, normalizeTagList(item.Tags)); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: id, Message: err.Error()})
		return
	}
	for locale, text := range item.Translations {
		if err := s.translations.Set(ctx, resourceTypeText, id, locale, text); err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: id, Message: err.Error()})
			return
		}
	}
	if err := s.enqueueBackfill(ctx, resourceTypeText, id, item.Language, item.Title, item.Body, item.Transcription, item.Translations); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: id, Message: err.Error()})
		return
	}
	res.Imported++
}
