// Vocabulary's half of export_import.go — see that file's package doc
// comment for why Texts/Dialogs and Vocabulary/Models each get their own
// types/handlers instead of a shared abstraction across all four. Vocabulary
// and Models are structurally similar to each other (a list plus an ordered
// items array), so they share listMetaFields/listMetaUnchanged/
// decideItemBackfill/enqueueItemBackfill from export_import.go, but each
// still gets its own request/response types and handler functions here and
// in export_import_models.go, mirroring apiVocabDetail/apiModelsDetail's
// existing duplication in vocabulary.go/models.go.
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
	"phraseforge/internal/vocabulary"
)

// apiVocabExportItemTranslation is one vocabulary item's per-locale
// translation — both translation and notes, since vocabulary items (unlike
// models items) carry a free-form notes annotation.
type apiVocabExportItemTranslation struct {
	Translation string `json:"translation" yaml:"translation"`
	Notes       string `json:"notes,omitempty" yaml:"notes,omitempty"`
}

// apiVocabExportItem is one exported vocabulary item — full
// round-trippable content, both site-locale translations included
// regardless of the viewer's own locale.
type apiVocabExportItem struct {
	Phrase        string                                   `json:"phrase" yaml:"phrase"`
	Grammar       string                                   `json:"grammar,omitempty" yaml:"grammar,omitempty"`
	Transcription string                                   `json:"transcription,omitempty" yaml:"transcription,omitempty"`
	Translations  map[string]apiVocabExportItemTranslation `json:"translations,omitempty" yaml:"translations,omitempty"`
}

// apiVocabExportList is one exported vocabulary list — its own metadata plus
// its ordered items.
type apiVocabExportList struct {
	ID       int64                `json:"id" yaml:"id"`
	Title    string               `json:"title" yaml:"title"`
	Language string               `json:"language" yaml:"language"`
	Script   string               `json:"script" yaml:"script"`
	Tags     []string             `json:"tags" yaml:"tags"`
	Items    []apiVocabExportItem `json:"items" yaml:"items"`
}
type apiVocabExportResponse struct {
	Lists []apiVocabExportList `json:"lists" yaml:"lists"`
}

// apiVocabImportItemTranslation mirrors apiVocabExportItemTranslation.
type apiVocabImportItemTranslation struct {
	Translation string `json:"translation,omitempty" yaml:"translation,omitempty"`
	Notes       string `json:"notes,omitempty" yaml:"notes,omitempty"`
}

// apiVocabImportItem is one vocabulary import item. Only Phrase is required.
// Translations is a partial map, same convention as apiTextImportItem's own
// field: only the locales present here are ever written.
type apiVocabImportItem struct {
	Phrase        string                                   `json:"phrase,omitempty" yaml:"phrase,omitempty"`
	Grammar       string                                   `json:"grammar,omitempty" yaml:"grammar,omitempty"`
	Transcription string                                   `json:"transcription,omitempty" yaml:"transcription,omitempty"`
	Translations  map[string]apiVocabImportItemTranslation `json:"translations,omitempty" yaml:"translations,omitempty"`
}

// apiVocabImportList is one vocabulary import list. Only Phrase-per-item is
// required; Title/Language/Script are required for a brand-new (id-less)
// list but may each be omitted on an update-by-id to leave that field of the
// list's own metadata unchanged (see listMetaFields's doc comment).
//
// Items is a pointer so an update-by-id can distinguish "the items key was
// omitted entirely" (nil — leave every existing item untouched; e.g. an
// import that only renames the list) from "items: []" (a non-nil pointer to
// an empty slice — wholesale-clear every item). Before this was a pointer,
// both cases decoded to the same empty/nil slice, so renaming a list without
// repeating its items silently deleted them all (see B1 in
// specs/features/phraseforge-export-import.md). On a brand-new (id-less)
// list, a nil Items is simply "create with no items" — there is nothing
// to "leave unchanged" for a list that doesn't exist yet.
type apiVocabImportList struct {
	ID       int64                 `json:"id,omitempty" yaml:"id,omitempty"`
	Title    string                `json:"title,omitempty" yaml:"title,omitempty"`
	Language string                `json:"language,omitempty" yaml:"language,omitempty"`
	Script   string                `json:"script,omitempty" yaml:"script,omitempty"`
	Tags     []string              `json:"tags,omitempty" yaml:"tags,omitempty"`
	Items    *[]apiVocabImportItem `json:"items,omitempty" yaml:"items,omitempty"`
	Delete   bool                  `json:"delete,omitempty" yaml:"delete,omitempty"`
}
type apiVocabImportRequest struct {
	Lists []apiVocabImportList `json:"lists" yaml:"lists"`
}

// vocabTranslationFields is one vocabulary item's translation/notes pair, as
// compared by vocabItemsUnchanged.
type vocabTranslationFields struct {
	Translation string
	Notes       string
}

// vocabItemFields is one vocabulary item's fields, as compared by
// vocabItemsUnchanged — pulled out as a plain data value (like importFields)
// so the comparison is unit-testable without a database.
type vocabItemFields struct {
	Phrase        string
	Grammar       string
	Transcription string
	// Translations is restricted to whichever locales the incoming item
	// actually provides — an import never writes a locale it doesn't
	// mention, so a locale existing has but incoming omits must never make
	// vocabItemsUnchanged see the item as changed (same rule as
	// importUnchanged's own Translations field).
	Translations map[string]vocabTranslationFields
}

// vocabItemsUnchanged reports whether existing already matches every
// incoming item exactly, position-by-position (a wholesale item replacement
// makes an incoming item's array index its new position, so index-aligned
// comparison is exactly what "would this replacement actually change
// anything" means). A different item count is always a change.
func vocabItemsUnchanged(existing, incoming []vocabItemFields) bool {
	if len(existing) != len(incoming) {
		return false
	}
	for i := range incoming {
		e, in := existing[i], incoming[i]
		if e.Phrase != in.Phrase || e.Grammar != in.Grammar || e.Transcription != in.Transcription {
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

// apiExportVocabulary handles GET /api/v1/vocabulary/export — mirrors
// apiExportTexts' ?language= scoping exactly (see that function's doc
// comment), applied to vocabulary lists plus their ordered items and
// per-item, per-site-locale translations.
func (s *Server) apiExportVocabulary(w http.ResponseWriter, r *http.Request) {
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
	list, err := s.vocab.ListAll(r.Context(), langs, all)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	list = filterByScript(list, filter.Script, func(l vocabulary.List) string { return l.Script })
	if len(filter.Tags) > 0 {
		matching, err := s.tags.ResourceIDsWithAllTags(r.Context(), resourceTypeVocab, filter.Tags)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		list = filterByID(list, matching, func(l vocabulary.List) int64 { return l.ID })
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
	out := make([]apiVocabExportList, len(list))
	for i, l := range list {
		items, err := s.vocab.Items(r.Context(), l.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		transByLocale, err := s.vocabTranslationsByLocale(r.Context(), l.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		itemsOut := make([]apiVocabExportItem, len(items))
		for j, it := range items {
			translations := map[string]apiVocabExportItemTranslation{}
			for _, loc := range i18n.Locales {
				if t, ok := transByLocale[loc.Code][it.Position]; ok {
					translations[loc.Code] = apiVocabExportItemTranslation{Translation: t.Translation, Notes: t.Notes}
				}
			}
			itemsOut[j] = apiVocabExportItem{
				Phrase: it.Phrase, Grammar: it.Grammar, Transcription: it.Transcription,
				Translations: translations,
			}
		}
		out[i] = apiVocabExportList{
			ID: l.ID, Title: l.Title, Language: l.Language, Script: l.Script,
			Tags: tagsByID[l.ID], Items: itemsOut,
		}
	}
	w.Header().Set("Content-Disposition", `attachment; filename="phraseforge-vocabulary-export.yaml"`)
	writeYAML(w, http.StatusOK, apiVocabExportResponse{Lists: out})
}

// vocabTranslationsByLocale fetches listID's item translations for every
// site locale in one call per locale (i18n.Locales has just two today) —
// shared by export (every locale, every item) and import (every locale,
// restricted to whichever positions/locales the comparison and writeback
// actually need) so both call sites build the same shape from the same
// underlying vocabulary.Store.Translations calls.
func (s *Server) vocabTranslationsByLocale(ctx context.Context, listID int64) (map[string]map[int]vocabulary.ItemTranslation, error) {
	out := make(map[string]map[int]vocabulary.ItemTranslation, len(i18n.Locales))
	for _, loc := range i18n.Locales {
		m, err := s.vocab.Translations(ctx, listID, loc.Code)
		if err != nil {
			return nil, err
		}
		out[loc.Code] = m
	}
	return out, nil
}

// apiImportVocabulary handles POST /api/v1/vocabulary/import — per-list
// upsert with error collection (never fail-fast, never delete-and-replace-
// all — see specs/features/phraseforge-export-import.md's Acceptance
// Criteria). Each list, in order:
//  1. delete:true + id: deletes that list if the caller CanEdit its language
//     (a per-list error if not); a missing id is a no-op, not an error.
//  2. id present, not delete: found + CanEdit required (otherwise a
//     per-list error); identical-in-every-respect (metadata, tags, and every
//     item including translations) to what's stored is "unchanged" (no
//     write, no item replacement, no backfill); otherwise list metadata is
//     updated for whichever of title/language/script were actually given,
//     tags are replaced if given, and every item is wholesale-replaced:
//     existing items are deleted, then every incoming item is added fresh in
//     order, then backfilled.
//  3. id absent: title/language/script required, CanEdit(language)
//     required; always created, its items added fresh, then backfilled.
//
// Any item within a list that's fundamentally invalid (missing phrase)
// aborts that one list as a single list-level error, before any write for
// that list happens — a vocabulary list's items aren't independently
// addressable resources the way top-level rows are, so there's no
// meaningful per-item-within-a-list slot in the response envelope to report
// a narrower failure into.
func (s *Server) apiImportVocabulary(w http.ResponseWriter, r *http.Request) {
	body, ok := readImportBody(w, r)
	if !ok {
		return
	}
	var req apiVocabImportRequest
	if err := yaml.Unmarshal(body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_yaml", err.Error())
		return
	}
	u := currentUser(r)
	res := importResult{Errors: []importError{}}
	for i, list := range req.Lists {
		s.importVocabList(r.Context(), u, i, list, &res)
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) importVocabList(ctx context.Context, u auth.User, index int, list apiVocabImportList, res *importResult) {
	switch {
	case list.Delete:
		s.importDeleteVocabList(ctx, u, index, list, res)
	case list.ID != 0:
		s.importUpdateVocabList(ctx, u, index, list, res)
	default:
		s.importCreateVocabList(ctx, u, index, list, res)
	}
}

func (s *Server) importDeleteVocabList(ctx context.Context, u auth.User, index int, list apiVocabImportList, res *importResult) {
	if list.ID == 0 {
		res.Errors = append(res.Errors, importError{Index: index, Message: "delete requires id"})
		return
	}
	existing, err := s.vocab.Get(ctx, list.ID)
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
		res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: "not permitted to edit this vocabulary list's language"})
		return
	}
	if err := s.vocab.Delete(ctx, list.ID); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
		return
	}
	if err := s.tags.DeleteFor(ctx, resourceTypeVocab, list.ID); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
		return
	}
	res.Deleted++
}

// resolveVocabImportItems returns list.Items' underlying slice plus whether
// the items key was actually present in the import (see apiVocabImportList's
// Items doc comment, B1 fix) — pulled out as a pure function so this
// omitted-vs-empty distinction is unit-testable without a database.
func resolveVocabImportItems(list apiVocabImportList) (items []apiVocabImportItem, provided bool) {
	if list.Items == nil {
		return nil, false
	}
	return *list.Items, true
}

// validateVocabImportItems reports the first fundamentally-invalid item in
// items (a blank phrase) — pulled out as a pure function so both
// importUpdateVocabList and importCreateVocabList apply the exact same rule
// without duplicating the loop.
func validateVocabImportItems(items []apiVocabImportItem) error {
	for i, it := range items {
		if strings.TrimSpace(it.Phrase) == "" {
			return fmt.Errorf("item %d: phrase is required", i)
		}
	}
	return nil
}

func (s *Server) importUpdateVocabList(ctx context.Context, u auth.User, index int, list apiVocabImportList, res *importResult) {
	existing, err := s.vocab.Get(ctx, list.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: "vocabulary list not found"})
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
		res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: "not permitted to edit this vocabulary list's language"})
		return
	}

	// itemsProvided distinguishes an omitted items: key (leave every
	// existing item untouched below) from an explicit items: [] (wholesale-
	// clear them) — see apiVocabImportList's Items doc comment (B1 fix).
	items, itemsProvided := resolveVocabImportItems(list)
	if err := validateVocabImportItems(items); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
		return
	}

	existingTags, err := s.tags.For(ctx, resourceTypeVocab, list.ID)
	if err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
		return
	}

	incomingTagsProvided := len(list.Tags) > 0
	incomingTags := normalizeTagList(list.Tags)
	existingMeta := listMetaFields{Title: existing.Title, Language: existing.Language, Script: existing.Script, Tags: existingTags}
	incomingMeta := listMetaFields{Title: list.Title, Language: list.Language, Script: list.Script, Tags: incomingTags}

	// itemsChanged (and existingItemCount, needed below to know how many
	// positions the wholesale replace must delete) are only computed when
	// itemsProvided — an omitted items: key can never make the list look
	// "changed" on the items side, and fetching existingItems/transByLocale
	// at all would just be wasted work when there's nothing to compare.
	var existingItemCount int
	itemsChanged := false
	if itemsProvided {
		existingItems, err := s.vocab.Items(ctx, list.ID)
		if err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
			return
		}
		transByLocale, err := s.vocabTranslationsByLocale(ctx, list.ID)
		if err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
			return
		}
		existingItemCount = len(existingItems)

		incomingItemFields := make([]vocabItemFields, len(items))
		for i, it := range items {
			incomingItemFields[i] = vocabItemFields{
				Phrase: it.Phrase, Grammar: it.Grammar, Transcription: it.Transcription,
				Translations: toVocabTranslationFields(it.Translations),
			}
		}
		existingItemFields := make([]vocabItemFields, len(existingItems))
		for i, it := range existingItems {
			var provided map[string]apiVocabImportItemTranslation
			if i < len(items) {
				provided = items[i].Translations
			}
			existingItemFields[i] = vocabItemFields{
				Phrase: it.Phrase, Grammar: it.Grammar, Transcription: it.Transcription,
				Translations: existingVocabItemTranslations(it.Position, provided, transByLocale),
			}
		}
		itemsChanged = !vocabItemsUnchanged(existingItemFields, incomingItemFields)
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
		// A blank/whitespace-only language must never reach CanEdit/Update —
		// roles.CanEdit returns true for an admin regardless of the language
		// string given (including blank), so without this explicit check a
		// blank language would sail past the re-check below and hit the
		// database as a raw constraint violation instead of a clean
		// per-item error (S2 fix).
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
			res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: "not permitted to move this vocabulary list to that language"})
			return
		}
	}
	if title != existing.Title || language != existing.Language || script != existing.Script {
		if err := s.vocab.UpdateMeta(ctx, list.ID, title, language, script); err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
			return
		}
	}
	if incomingTagsProvided {
		if err := s.tags.SetFor(ctx, resourceTypeVocab, list.ID, incomingTags); err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
			return
		}
	}

	// itemsProvided gates the wholesale-replace step itself, not just the
	// unchanged check above — an omitted items: key must leave every
	// existing item untouched even when other list metadata did change (B1
	// fix). NOTE (S3, doc-only): a hand-authored partial YAML that DOES
	// provide items but omits translations: for some of them will still
	// destroy those items' hand-written translations/notes on this wholesale
	// replace and regenerate them via LLM background jobs — hand-authoring
	// partial YAML should always include a full translations: block per item
	// to avoid this.
	if itemsProvided {
		specs, err := s.replaceVocabItemsTx(ctx, list.ID, existingItemCount, items)
		if err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
			return
		}
		for _, spec := range specs {
			if err := s.enqueueItemBackfill(ctx, "vocabulary_item", list.ID, spec.position, language, spec.phrase, spec.transcription, spec.providedTranslations); err != nil {
				res.Errors = append(res.Errors, importError{Index: index, ID: list.ID, Message: err.Error()})
				return
			}
		}
	}
	res.Imported++
}

// replaceVocabItemsTx wholesale-replaces listID's items — deleting every
// position from existingCount-1 down to 0 (so no lower position ever needs
// to shift mid-replace, see vocabulary.Store.DeleteItem's own doc comment on
// why shifting exists at all), then adding every incoming item fresh in
// order along with whichever translations it provides — as a single
// transaction (B2 fix): a failure or client disconnect partway through must
// never leave existingCount items deleted with nothing re-added. Backfill is
// enqueued by the caller only after this returns successfully (i.e. after
// commit), since a job enqueued for an item that got rolled back would
// reference nothing.
func (s *Server) replaceVocabItemsTx(ctx context.Context, listID int64, existingCount int, items []apiVocabImportItem) ([]itemBackfillSpec, error) {
	txCtx, cancel := backgroundTxContext(ctx)
	defer cancel()
	tx, err := s.vocab.Begin(txCtx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(txCtx) //nolint:errcheck // no-op once Commit succeeds

	for pos := existingCount - 1; pos >= 0; pos-- {
		if err := s.vocab.DeleteItemTx(txCtx, tx, listID, pos); err != nil {
			return nil, err
		}
	}
	specs := make([]itemBackfillSpec, 0, len(items))
	for _, it := range items {
		position, err := s.vocab.AddItemTx(txCtx, tx, listID, it.Phrase, it.Grammar, it.Transcription)
		if err != nil {
			return nil, err
		}
		providedTranslations := make(map[string]string, len(it.Translations))
		for locale, t := range it.Translations {
			providedTranslations[locale] = t.Translation
			if err := s.vocab.SetTranslationsTx(txCtx, tx, listID, locale,
				[]vocabulary.ItemTranslation{{Position: position, Translation: t.Translation, Notes: t.Notes}}, position+1); err != nil {
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

func (s *Server) importCreateVocabList(ctx context.Context, u auth.User, index int, list apiVocabImportList, res *importResult) {
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
		res.Errors = append(res.Errors, importError{Index: index, Message: "not permitted to edit this vocabulary list's language"})
		return
	}
	// A brand-new list has no items to "leave unchanged", so an omitted
	// items: key here simply means "create with no items" (see
	// apiVocabImportList's Items doc comment).
	items, _ := resolveVocabImportItems(list)
	if err := validateVocabImportItems(items); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, Message: err.Error()})
		return
	}
	id, err := s.vocab.Create(ctx, u.ID, list.Title, list.Language, list.Script)
	if err != nil {
		res.Errors = append(res.Errors, importError{Index: index, Message: err.Error()})
		return
	}
	if err := s.tags.SetFor(ctx, resourceTypeVocab, id, normalizeTagList(list.Tags)); err != nil {
		res.Errors = append(res.Errors, importError{Index: index, ID: id, Message: err.Error()})
		return
	}
	if err := s.addVocabItemsAndBackfill(ctx, id, list.Language, items, res, index); err != nil {
		return
	}
	res.Imported++
}

// addVocabItemsAndBackfill adds every item in items to listID fresh (in
// order), sets whichever translations each provides, and enqueues backfill
// for whichever fields are genuinely missing — shared by the create path and
// the update path's wholesale item replacement. Any error is already
// recorded onto res (with list-level id/index) before returning it, so the
// caller only needs to check for non-nil to stop.
func (s *Server) addVocabItemsAndBackfill(ctx context.Context, listID int64, language string, items []apiVocabImportItem, res *importResult, index int) error {
	for _, it := range items {
		position, err := s.vocab.AddItem(ctx, listID, it.Phrase, it.Grammar, it.Transcription)
		if err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: listID, Message: err.Error()})
			return err
		}
		providedTranslations := make(map[string]string, len(it.Translations))
		for locale, t := range it.Translations {
			providedTranslations[locale] = t.Translation
			if err := s.vocab.SetTranslations(ctx, listID, locale,
				[]vocabulary.ItemTranslation{{Position: position, Translation: t.Translation, Notes: t.Notes}}, position+1); err != nil {
				res.Errors = append(res.Errors, importError{Index: index, ID: listID, Message: err.Error()})
				return err
			}
		}
		if err := s.enqueueItemBackfill(ctx, "vocabulary_item", listID, position, language, it.Phrase, it.Transcription, providedTranslations); err != nil {
			res.Errors = append(res.Errors, importError{Index: index, ID: listID, Message: err.Error()})
			return err
		}
	}
	return nil
}

// toVocabTranslationFields converts an import item's raw translations map
// into vocabItemFields' comparable shape.
func toVocabTranslationFields(in map[string]apiVocabImportItemTranslation) map[string]vocabTranslationFields {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]vocabTranslationFields, len(in))
	for locale, t := range in {
		out[locale] = vocabTranslationFields{Translation: t.Translation, Notes: t.Notes}
	}
	return out
}

// existingVocabItemTranslations returns existingPosition's currently-stored
// translation/notes for every locale that provided (the incoming item at the
// same array index) actually mentions — restricted to those locales because
// they are the only ones a wholesale item replacement would ever change, the
// same restriction existingTranslationsFor applies for texts/dialogs.
func existingVocabItemTranslations(existingPosition int, provided map[string]apiVocabImportItemTranslation, transByLocale map[string]map[int]vocabulary.ItemTranslation) map[string]vocabTranslationFields {
	if len(provided) == 0 {
		return nil
	}
	out := make(map[string]vocabTranslationFields, len(provided))
	for locale := range provided {
		t := transByLocale[locale][existingPosition]
		out[locale] = vocabTranslationFields{Translation: t.Translation, Notes: t.Notes}
	}
	return out
}
