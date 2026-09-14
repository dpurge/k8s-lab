package server

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"phraseforge/internal/catalog"
	"phraseforge/internal/i18n"
	"phraseforge/internal/tags"
	"phraseforge/internal/vocabulary"
)

func (s *Server) handleVocabList(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	nv, err := s.loadNav(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	langs, all, err := s.roles.ViewableLanguages(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	languageFilter := r.URL.Query().Get("language")
	if languageFilter != "" && (all || slices.Contains(langs, languageFilter)) {
		langs, all = []string{languageFilter}, false
	}

	list, err := s.vocab.ListAll(r.Context(), langs, all)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tagFilter := r.URL.Query().Get("tag")
	if tagFilter != "" {
		ids, err := s.tags.ResourceIDsWithTag(r.Context(), resourceTypeVocab, tagFilter)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Item count per list, so a tile can show "12 items" without a translation
	// existing yet — a list with zero translated items is still a real tile.
	itemCounts := make(map[int64]int, len(list))
	for _, l := range list {
		items, err := s.vocab.Items(r.Context(), l.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		itemCounts[l.ID] = len(items)
	}

	render(w, u.Locale, "vocab-list.html", map[string]any{
		"User": u, "Nav": "vocabulary", "NavFlags": nv, "Lists": list,
		"TagsByID": tagsByID, "TagFilter": tagFilter, "LanguageFilter": languageFilter,
		"ItemCounts": itemCounts,
	})
}

func (s *Server) handleVocabNewForm(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	nv, err := s.loadNav(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !nv.CanCreateAny {
		http.Error(w, i18n.T(u.Locale, "vocabulary.err_no_create_access"), http.StatusForbidden)
		return
	}
	langs, scripts, err := s.formOptions(r.Context(), nv)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	allTags, err := s.tags.AllNames(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	render(w, u.Locale, "vocab-new.html", map[string]any{
		"User": u, "Nav": "vocabulary", "NavFlags": nv, "Languages": langs, "Scripts": scripts, "AllTags": allTags,
	})
}

func (s *Server) handleVocabCreate(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	language := r.FormValue("language")
	script := r.FormValue("script")
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canEdit {
		http.Error(w, i18n.T(u.Locale, "vocabulary.err_no_edit_language"), http.StatusForbidden)
		return
	}
	id, err := s.vocab.Create(r.Context(), u.ID, r.FormValue("title"), language, script)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.tags.SetFor(r.Context(), resourceTypeVocab, id, tags.Parse(r.FormValue("tags"))); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A new list starts empty — land straight on its edit page to start
	// adding items one at a time, rather than the view page with nothing to show.
	http.Redirect(w, r, "/vocabulary/"+strconv.FormatInt(id, 10)+"/edit", http.StatusFound)
}

func (s *Server) handleVocabView(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	l, err := s.vocab.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	canView, err := s.roles.CanView(r.Context(), u.ID, l.Language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canView {
		http.NotFound(w, r)
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	nv, err := s.loadNav(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	scriptMeta, err := catalog.GetScript(r.Context(), s.db, l.Script)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	items, err := s.vocab.Items(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	trans, err := s.vocab.Translations(r.Context(), id, u.Locale)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	listTags, err := s.tags.For(r.Context(), resourceTypeVocab, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Pair each item with its translation (zero value — blank — if this
	// locale hasn't translated that position yet) for the view template's
	// one-row-per-item table.
	rows := make([]vocabViewRow, len(items))
	for i, it := range items {
		t := trans[it.Position]
		rows[i] = vocabViewRow{Item: it, Translation: t.Translation, Notes: t.Notes}
	}

	render(w, u.Locale, "vocab-view.html", map[string]any{
		"User": u, "NavFlags": nv, "List": l, "Rows": rows, "Script": scriptMeta,
		"CanEdit": canEdit, "Tags": listTags, "Markdown": vocabularyMarkdown(l.Language, l.Script, rows),
	})
}

func (s *Server) handleVocabEditForm(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	l, err := s.vocab.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canEdit {
		http.NotFound(w, r)
		return
	}
	nv, err := s.loadNav(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	langs, scripts, err := s.formOptions(r.Context(), nv)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	scriptMeta, err := catalog.GetScript(r.Context(), s.db, l.Script)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	items, err := s.vocab.Items(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	trans, err := s.vocab.Translations(r.Context(), id, u.Locale)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	listTags, err := s.tags.For(r.Context(), resourceTypeVocab, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	allTags, err := s.tags.AllNames(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Pair each item with its translation (blank if this locale hasn't
	// translated that position yet) for the item-list table below the entry
	// form — same shape as the view page's table.
	rows := make([]vocabViewRow, len(items))
	for i, it := range items {
		t := trans[it.Position]
		rows[i] = vocabViewRow{Item: it, Translation: t.Translation, Notes: t.Notes}
	}

	// The entry form either adds a new item (default) or edits an existing
	// one, named by ?edit=<position> — prefill its fields from that item.
	editPosition := -1
	var entryPhrase, entryGrammar, entryTranscription, entryTranslation, entryNotes string
	if q := r.URL.Query().Get("edit"); q != "" {
		if p, err := strconv.Atoi(q); err == nil && p >= 0 && p < len(items) {
			editPosition = p
			entryPhrase = items[p].Phrase
			entryGrammar = items[p].Grammar
			entryTranscription = items[p].Transcription
			if t, ok := trans[p]; ok {
				entryTranslation = t.Translation
				entryNotes = t.Notes
			}
		}
	}

	render(w, u.Locale, "vocab-edit.html", map[string]any{
		"User": u, "NavFlags": nv, "List": l, "Languages": langs, "Scripts": scripts, "Script": scriptMeta,
		"Tags": strings.Join(listTags, ", "), "AllTags": allTags, "Rows": rows,
		"EditPosition":       editPosition,
		"EntryPhrase":        entryPhrase,
		"EntryGrammar":       entryGrammar,
		"EntryTranscription": entryTranscription,
		"EntryTranslation":   entryTranslation,
		"EntryNotes":         entryNotes,
	})
}

func (s *Server) handleVocabUpdate(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	l, err := s.vocab.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canEdit {
		http.NotFound(w, r)
		return
	}
	newLanguage := r.FormValue("language")
	if newLanguage != l.Language {
		canEditNew, err := s.roles.CanEdit(r.Context(), u.ID, newLanguage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !canEditNew {
			http.Error(w, i18n.T(u.Locale, "vocabulary.err_no_move_language"), http.StatusForbidden)
			return
		}
	}
	if err := s.vocab.UpdateMeta(r.Context(), id, r.FormValue("title"), newLanguage, r.FormValue("script")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.tags.SetFor(r.Context(), resourceTypeVocab, id, tags.Parse(r.FormValue("tags"))); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/vocabulary/"+strconv.FormatInt(id, 10)+"/edit", http.StatusFound)
}

// handleVocabItemCreate appends one new item to the list, plus its
// translation/notes in the editing user's own site locale.
func (s *Server) handleVocabItemCreate(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	l, err := s.vocab.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canEdit {
		http.NotFound(w, r)
		return
	}
	position, err := s.vocab.AddItem(r.Context(), id, r.FormValue("phrase"), r.FormValue("grammar"), r.FormValue("transcription"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	translation, notes := r.FormValue("translation"), r.FormValue("notes")
	if translation != "" || notes != "" {
		if err := s.vocab.SetTranslations(r.Context(), id, u.Locale,
			[]vocabulary.ItemTranslation{{Position: position, Translation: translation, Notes: notes}}, position+1); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/vocabulary/"+strconv.FormatInt(id, 10)+"/edit", http.StatusFound)
}

// handleVocabItemUpdate overwrites one existing item's common fields and its
// translation/notes in the editing user's own site locale.
func (s *Server) handleVocabItemUpdate(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	position, err := strconv.Atoi(chi.URLParam(r, "position"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	l, err := s.vocab.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canEdit {
		http.NotFound(w, r)
		return
	}
	if err := s.vocab.UpdateItem(r.Context(), id, position, r.FormValue("phrase"), r.FormValue("grammar"), r.FormValue("transcription")); err != nil {
		if err == vocabulary.ErrItemNotFound {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.vocab.SetTranslations(r.Context(), id, u.Locale,
		[]vocabulary.ItemTranslation{{Position: position, Translation: r.FormValue("translation"), Notes: r.FormValue("notes")}}, position+1); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/vocabulary/"+strconv.FormatInt(id, 10)+"/edit", http.StatusFound)
}

// handleVocabItemDelete removes one item, shifting later items (and their
// translations, in every locale) down a position — see vocabulary.DeleteItem.
func (s *Server) handleVocabItemDelete(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	position, err := strconv.Atoi(chi.URLParam(r, "position"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	l, err := s.vocab.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canEdit {
		http.NotFound(w, r)
		return
	}
	if err := s.vocab.DeleteItem(r.Context(), id, position); err != nil {
		if err == vocabulary.ErrItemNotFound {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/vocabulary/"+strconv.FormatInt(id, 10)+"/edit", http.StatusFound)
}

func (s *Server) handleVocabDelete(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	l, err := s.vocab.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canEdit {
		http.NotFound(w, r)
		return
	}
	if err := s.vocab.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.tags.DeleteFor(r.Context(), resourceTypeVocab, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/vocabulary", http.StatusFound)
}
