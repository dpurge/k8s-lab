// Dialogs' half of export_import.go — see that file's package doc comment
// for why Texts and Dialogs each get their own types/handlers here instead
// of a shared abstraction.
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
	"phraseforge/internal/dialogs"
)

// apiDialogExportItem mirrors apiTextExportItem exactly — see that type's
// doc comment.
type apiDialogExportItem struct {
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
type apiDialogExportResponse struct {
	Items []apiDialogExportItem `json:"items" yaml:"items"`
}

// apiDialogImportItem mirrors apiTextImportItem exactly — see that type's
// doc comment. A dialog's body is stored raw here too (wrapDialogBody only
// applies at render time, see dialogs.go), matching apiCreateDialog/
// apiUpdateDialog's own behavior.
type apiDialogImportItem struct {
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
type apiDialogImportRequest struct {
	Items []apiDialogImportItem `json:"items" yaml:"items"`
}

// apiExportDialogs handles GET /api/v1/dialogs/export — mirrors
// apiExportTexts exactly, see that function's doc comment.
func (s *Server) apiExportDialogs(w http.ResponseWriter, r *http.Request) {
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
	list, err := s.dialogs.List(r.Context(), langs, all)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	list = filterByScript(list, filter.Script, func(d dialogs.Dialog) string { return d.Script })
	if len(filter.Tags) > 0 {
		matching, err := s.tags.ResourceIDsWithAllTags(r.Context(), resourceTypeDialog, filter.Tags)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		list = filterByID(list, matching, func(d dialogs.Dialog) int64 { return d.ID })
	}
	ids := make([]int64, len(list))
	for i, d := range list {
		ids[i] = d.ID
	}
	tagsByID, err := s.tags.ForMany(r.Context(), resourceTypeDialog, ids)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	items := make([]apiDialogExportItem, len(list))
	for i, d := range list {
		translations, err := s.exportTranslations(r.Context(), resourceTypeDialog, d.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		items[i] = apiDialogExportItem{
			ID: d.ID, Title: d.Title, Body: d.Body, Transcription: d.Transcription,
			Language: d.Language, Script: d.Script, IngestSource: d.IngestSource,
			Tags: tagsByID[d.ID], Translations: translations,
			CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		}
	}
	w.Header().Set("Content-Disposition", `attachment; filename="phraseforge-dialogs-export.yaml"`)
	writeYAML(w, http.StatusOK, apiDialogExportResponse{Items: items})
}

// apiImportDialogs handles POST /api/v1/dialogs/import — mirrors
// apiImportTexts exactly, see that function's doc comment.
func (s *Server) apiImportDialogs(w http.ResponseWriter, r *http.Request) {
	body, ok := readImportBody(w, r)
	if !ok {
		return
	}
	var req apiDialogImportRequest
	if err := yaml.Unmarshal(body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_yaml", err.Error())
		return
	}
	u := currentUser(r)
	res := importResult{Errors: []importError{}}
	for i, item := range req.Items {
		s.importDialogItem(r.Context(), u, i, item, &res)
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) importDialogItem(ctx context.Context, u auth.User, index int, item apiDialogImportItem, res *importResult) {
	switch {
	case item.Delete:
		s.importDeleteDialog(ctx, u, index, item, res)
	case item.ID != 0:
		s.importUpdateDialog(ctx, u, index, item, res)
	default:
		s.importCreateDialog(ctx, u, index, item, res)
	}
}

func (s *Server) importDeleteDialog(ctx context.Context, u auth.User, index int, item apiDialogImportItem, res *importResult) {
	if item.ID == 0 {
		res.Errors = append(res.Errors, importError{Index: index, Message: "delete requires id"})
		return
	}
	existing, err := s.dialogs.Get(ctx, item.ID)
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
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: "not permitted to edit this dialog's language"})
		return
	}
	if err := s.dialogs.Delete(ctx, item.ID); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	if err := s.tags.DeleteFor(ctx, resourceTypeDialog, item.ID); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	res.Deleted++
}

func (s *Server) importUpdateDialog(ctx context.Context, u auth.User, index int, item apiDialogImportItem, res *importResult) {
	existing, err := s.dialogs.Get(ctx, item.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: "dialog not found"})
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
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: "not permitted to edit this dialog's language"})
		return
	}
	// S1 fix: mirrors importUpdateText's own check — see that function's
	// doc comment.
	if strings.TrimSpace(item.Body) == "" {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: "body is required"})
		return
	}

	incomingTags := normalizeTagList(item.Tags)
	existingTags, err := s.tags.For(ctx, resourceTypeDialog, item.ID)
	if err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	existingTranslations, err := s.existingTranslationsFor(ctx, resourceTypeDialog, item.ID, item.Translations)
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
		// S2 fix: mirrors importUpdateText's own check — see that
		// function's doc comment.
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
			res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: "not permitted to move this dialog to that language"})
			return
		}
	}

	if err := s.dialogs.Update(ctx, item.ID, item.Title, item.Body, item.Transcription, item.Language, item.Script); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	if err := s.tags.SetFor(ctx, resourceTypeDialog, item.ID, incomingTags); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	for locale, text := range item.Translations {
		if err := s.translations.Set(ctx, resourceTypeDialog, item.ID, locale, text); err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
			return
		}
	}
	if err := s.enqueueBackfill(ctx, resourceTypeDialog, item.ID, item.Language, item.Title, item.Body, item.Transcription, item.Translations); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: item.ID, Message: err.Error()})
		return
	}
	res.Imported++
}

func (s *Server) importCreateDialog(ctx context.Context, u auth.User, index int, item apiDialogImportItem, res *importResult) {
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
		res.Errors = append(res.Errors, importError{Index: index, Message: "not permitted to edit this dialog's language"})
		return
	}
	id, err := s.dialogs.Create(ctx, u.ID, item.Title, item.Body, item.Transcription, item.Language, item.Script, "")
	if err != nil {
		res.Errors = append(res.Errors, importError{Index: index, Message: err.Error()})
		return
	}
	if err := s.tags.SetFor(ctx, resourceTypeDialog, id, normalizeTagList(item.Tags)); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: id, Message: err.Error()})
		return
	}
	for locale, text := range item.Translations {
		if err := s.translations.Set(ctx, resourceTypeDialog, id, locale, text); err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: id, Message: err.Error()})
			return
		}
	}
	if err := s.enqueueBackfill(ctx, resourceTypeDialog, id, item.Language, item.Title, item.Body, item.Transcription, item.Translations); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: id, Message: err.Error()})
		return
	}
	res.Imported++
}
