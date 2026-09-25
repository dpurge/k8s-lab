// Models' half of export_import.go — see that file's package doc comment,
// and export_import_vocabulary.go's own doc comment, for why Vocabulary and
// Models each get their own types/handlers here despite sharing
// listMetaFields/listMetaUnchanged/decideItemBackfill/enqueueItemBackfill.
// This file mirrors export_import_vocabulary.go closely; the only structural
// difference is that a models item has no grammar or notes field.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"phraseforge/internal/auth"
	"phraseforge/internal/i18n"
	"phraseforge/internal/models"
)

// apiModelsExportItemTranslation is one models item's per-locale
// translation — just the translation text, since models items (unlike
// vocabulary items) have no notes field.
type apiModelsExportItemTranslation struct {
	Translation string `json:"translation" yaml:"translation"`
}

// apiModelsExportItem is one exported models item — full round-trippable
// content, both site-locale translations included regardless of the
// viewer's own locale.
type apiModelsExportItem struct {
	Phrase        string                                    `json:"phrase" yaml:"phrase"`
	Transcription string                                    `json:"transcription,omitempty" yaml:"transcription,omitempty"`
	Translations  map[string]apiModelsExportItemTranslation `json:"translations,omitempty" yaml:"translations,omitempty"`
}

// apiModelsExportList is one exported models list — its own metadata plus
// its ordered items.
type apiModelsExportList struct {
	ID       int64                 `json:"id" yaml:"id"`
	Title    string                `json:"title" yaml:"title"`
	Language string                `json:"language" yaml:"language"`
	Script   string                `json:"script" yaml:"script"`
	Tags     []string              `json:"tags" yaml:"tags"`
	Items    []apiModelsExportItem `json:"items" yaml:"items"`
}
type apiModelsExportResponse struct {
	Lists []apiModelsExportList `json:"lists" yaml:"lists"`
}

// apiModelsImportItemTranslation mirrors apiModelsExportItemTranslation.
type apiModelsImportItemTranslation struct {
	Translation string `json:"translation,omitempty" yaml:"translation,omitempty"`
}

// apiModelsImportItem is one models import item. Only Phrase is required.
// Translations is a partial map, same convention as apiVocabImportItem's own
// field: only the locales present here are ever written.
type apiModelsImportItem struct {
	Phrase        string                                    `json:"phrase,omitempty" yaml:"phrase,omitempty"`
	Transcription string                                    `json:"transcription,omitempty" yaml:"transcription,omitempty"`
	Translations  map[string]apiModelsImportItemTranslation `json:"translations,omitempty" yaml:"translations,omitempty"`
}

// apiModelsImportList mirrors apiVocabImportList exactly — see that type's
// doc comment, including Items' pointer-for-omitted-vs-empty distinction
// (B1 fix).
type apiModelsImportList struct {
	ID       int64                  `json:"id,omitempty" yaml:"id,omitempty"`
	Title    string                 `json:"title,omitempty" yaml:"title,omitempty"`
	Language string                 `json:"language,omitempty" yaml:"language,omitempty"`
	Script   string                 `json:"script,omitempty" yaml:"script,omitempty"`
	Tags     []string               `json:"tags,omitempty" yaml:"tags,omitempty"`
	Items    *[]apiModelsImportItem `json:"items,omitempty" yaml:"items,omitempty"`
	Delete   bool                   `json:"delete,omitempty" yaml:"delete,omitempty"`
}
type apiModelsImportRequest struct {
	Lists []apiModelsImportList `json:"lists" yaml:"lists"`
}

// modelsItemFields is one models item's fields, as compared by
// modelsItemsUnchanged — mirrors vocabItemFields, minus Grammar/Notes.
type modelsItemFields struct {
	Phrase        string
	Transcription string
	// Translations is restricted to whichever locales the incoming item
	// actually provides — same rule as vocabItemFields.Translations.
	Translations map[string]string
}

// modelsItemsUnchanged mirrors vocabItemsUnchanged — see that function's
// doc comment.
func modelsItemsUnchanged(existing, incoming []modelsItemFields) bool {
	if len(existing) != len(incoming) {
		return false
	}
	for i := range incoming {
		e, in := existing[i], incoming[i]
		if e.Phrase != in.Phrase || e.Transcription != in.Transcription {
			return false
		}
		for locale, t := range in.Translations {
			if e.Translations[locale] != t {
				return false
			}
		}
	}
	return true
}

// apiExportModels handles GET /api/v1/models/export — mirrors
// apiExportVocabulary exactly, see that function's doc comment.
func (s *Server) apiExportModels(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	editLangs, editAll, err := s.roles.EditableLanguages(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	langs, all := exportScopeLanguages(editLangs, editAll, r.URL.Query().Get("language"))
	list, err := s.models.ListAll(r.Context(), langs, all)
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
	out := make([]apiModelsExportList, len(list))
	for i, l := range list {
		items, err := s.models.Items(r.Context(), l.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		transByLocale, err := s.modelsTranslationsByLocale(r.Context(), l.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		itemsOut := make([]apiModelsExportItem, len(items))
		for j, it := range items {
			translations := map[string]apiModelsExportItemTranslation{}
			for _, loc := range i18n.Locales {
				if t, ok := transByLocale[loc.Code][it.Position]; ok {
					translations[loc.Code] = apiModelsExportItemTranslation{Translation: t.Translation}
				}
			}
			itemsOut[j] = apiModelsExportItem{
				Phrase: it.Phrase, Transcription: it.Transcription,
				Translations: translations,
			}
		}
		out[i] = apiModelsExportList{
			ID: l.ID, Title: l.Title, Language: l.Language, Script: l.Script,
			Tags: tagsByID[l.ID], Items: itemsOut,
		}
	}
	w.Header().Set("Content-Disposition", `attachment; filename="phraseforge-models-export.yaml"`)
	writeYAML(w, http.StatusOK, apiModelsExportResponse{Lists: out})
}

// modelsTranslationsByLocale mirrors vocabTranslationsByLocale — see that
// function's doc comment.
func (s *Server) modelsTranslationsByLocale(ctx context.Context, listID int64) (map[string]map[int]models.ItemTranslation, error) {
	out := make(map[string]map[int]models.ItemTranslation, len(i18n.Locales))
	for _, loc := range i18n.Locales {
		m, err := s.models.Translations(ctx, listID, loc.Code)
		if err != nil {
			return nil, err
		}
		out[loc.Code] = m
	}
	return out, nil
}

// apiImportModels handles POST /api/v1/models/import — mirrors
// apiImportVocabulary exactly, see that function's doc comment.
func (s *Server) apiImportModels(w http.ResponseWriter, r *http.Request) {
	body, ok := readImportBody(w, r)
	if !ok {
		return
	}
	var req apiModelsImportRequest
	if err := yaml.Unmarshal(body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_yaml", err.Error())
		return
	}
	u := currentUser(r)
	res := importResult{Errors: []importError{}}
	for i, list := range req.Lists {
		s.importModelsList(r.Context(), u, i, list, &res)
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) importModelsList(ctx context.Context, u auth.User, index int, list apiModelsImportList, res *importResult) {
	switch {
	case list.Delete:
		s.importDeleteModelsList(ctx, u, index, list, res)
	case list.ID != 0:
		s.importUpdateModelsList(ctx, u, index, list, res)
	default:
		s.importCreateModelsList(ctx, u, index, list, res)
	}
}

func (s *Server) importDeleteModelsList(ctx context.Context, u auth.User, index int, list apiModelsImportList, res *importResult) {
	if list.ID == 0 {
		res.Errors = append(res.Errors, importError{Index: index, Message: "delete requires id"})
		return
	}
	existing, err := s.models.Get(ctx, list.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			res.Deleted++ // deleting an id that doesn't exist is a no-op, not an error
			return
		}
		res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
		return
	}
	canEdit, err := s.roles.CanEdit(ctx, u.ID, existing.Language)
	if err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
		return
	}
	if !canEdit {
		res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: "not permitted to edit this models list's language"})
		return
	}
	if err := s.models.Delete(ctx, list.ID); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
		return
	}
	if err := s.tags.DeleteFor(ctx, resourceTypeModels, list.ID); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
		return
	}
	res.Deleted++
}

// resolveModelsImportItems mirrors resolveVocabImportItems — see that
// function's doc comment.
func resolveModelsImportItems(list apiModelsImportList) (items []apiModelsImportItem, provided bool) {
	if list.Items == nil {
		return nil, false
	}
	return *list.Items, true
}

// validateModelsImportItems mirrors validateVocabImportItems — see that
// function's doc comment.
func validateModelsImportItems(items []apiModelsImportItem) error {
	for i, it := range items {
		if strings.TrimSpace(it.Phrase) == "" {
			return fmt.Errorf("item %d: phrase is required", i)
		}
	}
	return nil
}

func (s *Server) importUpdateModelsList(ctx context.Context, u auth.User, index int, list apiModelsImportList, res *importResult) {
	existing, err := s.models.Get(ctx, list.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: "models list not found"})
			return
		}
		res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
		return
	}
	canEdit, err := s.roles.CanEdit(ctx, u.ID, existing.Language)
	if err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
		return
	}
	if !canEdit {
		res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: "not permitted to edit this models list's language"})
		return
	}

	// itemsProvided distinguishes an omitted items: key (leave every
	// existing item untouched below) from an explicit items: [] (wholesale-
	// clear them) — see apiModelsImportList's Items doc comment (B1 fix).
	items, itemsProvided := resolveModelsImportItems(list)
	if err := validateModelsImportItems(items); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
		return
	}

	existingTags, err := s.tags.For(ctx, resourceTypeModels, list.ID)
	if err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
		return
	}

	incomingTagsProvided := len(list.Tags) > 0
	incomingTags := normalizeTagList(list.Tags)
	existingMeta := listMetaFields{Title: existing.Title, Language: existing.Language, Script: existing.Script, Tags: existingTags}
	incomingMeta := listMetaFields{Title: list.Title, Language: list.Language, Script: list.Script, Tags: incomingTags}

	// itemsChanged/existingItemCount mirror importUpdateVocabList's own —
	// see that function's doc comment.
	var existingItemCount int
	itemsChanged := false
	if itemsProvided {
		existingItems, err := s.models.Items(ctx, list.ID)
		if err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
			return
		}
		transByLocale, err := s.modelsTranslationsByLocale(ctx, list.ID)
		if err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
			return
		}
		existingItemCount = len(existingItems)

		incomingItemFields := make([]modelsItemFields, len(items))
		for i, it := range items {
			incomingItemFields[i] = modelsItemFields{
				Phrase: it.Phrase, Transcription: it.Transcription,
				Translations: toModelsTranslationFields(it.Translations),
			}
		}
		existingItemFields := make([]modelsItemFields, len(existingItems))
		for i, it := range existingItems {
			var provided map[string]apiModelsImportItemTranslation
			if i < len(items) {
				provided = items[i].Translations
			}
			existingItemFields[i] = modelsItemFields{
				Phrase: it.Phrase, Transcription: it.Transcription,
				Translations: existingModelsItemTranslations(it.Position, provided, transByLocale),
			}
		}
		itemsChanged = !modelsItemsUnchanged(existingItemFields, incomingItemFields)
	}

	if listMetaUnchanged(existingMeta, incomingMeta) && !itemsChanged {
		res.Unchanged++
		return
	}

	title, language, script := existing.Title, existing.Language, existing.Script
	if list.Title != "" {
		title = list.Title
	}
	if list.Language != "" {
		language = list.Language
	}
	if list.Script != "" {
		script = list.Script
	}
	if language != existing.Language {
		// See importUpdateVocabList's own doc comment on this check (S2 fix):
		// roles.CanEdit returns true for an admin regardless of the language
		// string given, so a blank/whitespace-only language must be rejected
		// here, before it ever reaches CanEdit or the database.
		if strings.TrimSpace(language) == "" {
			res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: "language must not be blank"})
			return
		}
		canEditNew, err := s.roles.CanEdit(ctx, u.ID, language)
		if err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
			return
		}
		if !canEditNew {
			res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: "not permitted to move this models list to that language"})
			return
		}
	}
	if title != existing.Title || language != existing.Language || script != existing.Script {
		if err := s.models.UpdateMeta(ctx, list.ID, title, language, script); err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
			return
		}
	}
	if incomingTagsProvided {
		if err := s.tags.SetFor(ctx, resourceTypeModels, list.ID, incomingTags); err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
			return
		}
	}

	// itemsProvided gates the wholesale-replace step itself (B1 fix). NOTE
	// (S3, doc-only): a hand-authored partial YAML that DOES provide items
	// but omits translations: for some of them will still destroy those
	// items' hand-written translations on this wholesale replace and
	// regenerate them via LLM background jobs — hand-authoring partial YAML
	// should always include a full translations: block per item to avoid
	// this.
	if itemsProvided {
		specs, err := s.replaceModelsItemsTx(ctx, list.ID, existingItemCount, items)
		if err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
			return
		}
		for _, spec := range specs {
			if err := s.enqueueItemBackfill(ctx, "models_item", list.ID, spec.position, language, spec.phrase, spec.transcription, spec.providedTranslations); err != nil {
				res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
				return
			}
		}
	}
	res.Imported++
}

// replaceModelsItemsTx mirrors replaceVocabItemsTx — see that function's doc
// comment (B2 fix).
func (s *Server) replaceModelsItemsTx(ctx context.Context, listID int64, existingCount int, items []apiModelsImportItem) ([]itemBackfillSpec, error) {
	txCtx, cancel := backgroundTxContext(ctx)
	defer cancel()
	tx, err := s.models.Begin(txCtx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(txCtx) //nolint:errcheck // no-op once Commit succeeds

	for pos := existingCount - 1; pos >= 0; pos-- {
		if err := s.models.DeleteItemTx(txCtx, tx, listID, pos); err != nil {
			return nil, err
		}
	}
	specs := make([]itemBackfillSpec, 0, len(items))
	for _, it := range items {
		position, err := s.models.AddItemTx(txCtx, tx, listID, it.Phrase, it.Transcription)
		if err != nil {
			return nil, err
		}
		providedTranslations := make(map[string]string, len(it.Translations))
		for locale, t := range it.Translations {
			providedTranslations[locale] = t.Translation
			if err := s.models.SetTranslationTx(txCtx, tx, listID, position, locale, t.Translation, position+1); err != nil {
				return nil, err
			}
		}
		specs = append(specs, itemBackfillSpec{position: position, phrase: it.Phrase, transcription: it.Transcription, providedTranslations: providedTranslations})
	}
	if err := tx.Commit(txCtx); err != nil {
		return nil, err
	}
	return specs, nil
}

func (s *Server) importCreateModelsList(ctx context.Context, u auth.User, index int, list apiModelsImportList, res *importResult) {
	if strings.TrimSpace(list.Title) == "" || strings.TrimSpace(list.Language) == "" || strings.TrimSpace(list.Script) == "" {
		res.Errors = append(res.Errors, importError{Index: index, Message: "title, language, and script are required"})
		return
	}
	canEdit, err := s.roles.CanEdit(ctx, u.ID, list.Language)
	if err != nil {
		res.Errors = append(res.Errors, importError{Index: index, Message: err.Error()})
		return
	}
	if !canEdit {
		res.Errors = append(res.Errors, importError{Index: index, Message: "not permitted to edit this models list's language"})
		return
	}
	// A brand-new list has no items to "leave unchanged" (see
	// apiModelsImportList's Items doc comment).
	items, _ := resolveModelsImportItems(list)
	if err := validateModelsImportItems(items); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, Message: err.Error()})
		return
	}
	id, err := s.models.Create(ctx, u.ID, list.Title, list.Language, list.Script)
	if err != nil {
		res.Errors = append(res.Errors, importError{Index: index, Message: err.Error()})
		return
	}
	if err := s.tags.SetFor(ctx, resourceTypeModels, id, normalizeTagList(list.Tags)); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: id, Message: err.Error()})
		return
	}
	if err := s.addModelsItemsAndBackfill(ctx, id, list.Language, items, res, index); err != nil {
		return
	}
	res.Imported++
}

// addModelsItemsAndBackfill mirrors addVocabItemsAndBackfill — see that
// function's doc comment.
func (s *Server) addModelsItemsAndBackfill(ctx context.Context, listID int64, language string, items []apiModelsImportItem, res *importResult, index int) error {
	for _, it := range items {
		position, err := s.models.AddItem(ctx, listID, it.Phrase, it.Transcription)
		if err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: listID, Message: err.Error()})
			return err
		}
		providedTranslations := make(map[string]string, len(it.Translations))
		for locale, t := range it.Translations {
			providedTranslations[locale] = t.Translation
			if err := s.models.SetTranslation(ctx, listID, position, locale, t.Translation, position+1); err != nil {
				res.Errors = append(res.Errors, importError{Index: index, ID: listID, Message: err.Error()})
				return err
			}
		}
		if err := s.enqueueItemBackfill(ctx, "models_item", listID, position, language, it.Phrase, it.Transcription, providedTranslations); err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: listID, Message: err.Error()})
			return err
		}
	}
	return nil
}

// toModelsTranslationFields converts an import item's raw translations map
// into modelsItemFields' comparable shape.
func toModelsTranslationFields(in map[string]apiModelsImportItemTranslation) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for locale, t := range in {
		out[locale] = t.Translation
	}
	return out
}

// existingModelsItemTranslations mirrors existingVocabItemTranslations — see
// that function's doc comment.
func existingModelsItemTranslations(existingPosition int, provided map[string]apiModelsImportItemTranslation, transByLocale map[string]map[int]models.ItemTranslation) map[string]string {
	if len(provided) == 0 {
		return nil
	}
	out := make(map[string]string, len(provided))
	for locale := range provided {
		out[locale] = transByLocale[locale][existingPosition].Translation
	}
	return out
}
