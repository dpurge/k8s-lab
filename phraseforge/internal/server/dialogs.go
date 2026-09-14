package server

import (
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"phraseforge/internal/catalog"
	"phraseforge/internal/dialogs"
	"phraseforge/internal/i18n"
	"phraseforge/internal/tags"
	"phraseforge/internal/texts"
)

func (s *Server) handleDialogList(w http.ResponseWriter, r *http.Request) {
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

	list, err := s.dialogs.List(r.Context(), langs, all)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tagFilter := r.URL.Query().Get("tag")
	if tagFilter != "" {
		ids, err := s.tags.ResourceIDsWithTag(r.Context(), resourceTypeDialog, tagFilter)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		list = filterByID(list, ids, func(d dialogs.Dialog) int64 { return d.ID })
	}

	ids := make([]int64, len(list))
	for i, d := range list {
		ids[i] = d.ID
	}
	tagsByID, err := s.tags.ForMany(r.Context(), resourceTypeDialog, ids)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	render(w, u.Locale, "dialogs-list.html", map[string]any{
		"User": u, "Nav": "dialogs", "NavFlags": nv, "Dialogs": list,
		"TagsByID": tagsByID, "TagFilter": tagFilter, "LanguageFilter": languageFilter,
	})
}

func (s *Server) handleDialogNewForm(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	nv, err := s.loadNav(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !nv.CanCreateAny {
		http.Error(w, i18n.T(u.Locale, "dialogs.err_no_create_access"), http.StatusForbidden)
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
	render(w, u.Locale, "dialogs-new.html", map[string]any{
		"User": u, "Nav": "dialogs", "NavFlags": nv, "Languages": langs, "Scripts": scripts, "AllTags": allTags,
	})
}

func (s *Server) handleDialogCreate(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	language := r.FormValue("language")
	script := r.FormValue("script")
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canEdit {
		http.Error(w, i18n.T(u.Locale, "dialogs.err_no_edit_language"), http.StatusForbidden)
		return
	}
	id, err := s.dialogs.Create(r.Context(), u.ID, r.FormValue("title"), r.FormValue("body"), r.FormValue("transcription"), language, script)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.tags.SetFor(r.Context(), resourceTypeDialog, id, tags.Parse(r.FormValue("tags"))); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.translations.Set(r.Context(), resourceTypeDialog, id, u.Locale, r.FormValue("translation")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dialogs/"+strconv.FormatInt(id, 10), http.StatusFound)
}

func (s *Server) handleDialogView(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d, err := s.dialogs.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	canView, err := s.roles.CanView(r.Context(), u.ID, d.Language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canView {
		http.NotFound(w, r)
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, d.Language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	nv, err := s.loadNav(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	scriptMeta, err := catalog.GetScript(r.Context(), s.db, d.Script)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dialogTags, err := s.tags.For(r.Context(), resourceTypeDialog, d.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	translationBody, hasTranslation, err := s.translations.Get(r.Context(), resourceTypeDialog, d.ID, u.Locale)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Malformed dialog turn syntax (e.g. content that isn't indented under a
	// "--:" header) is a content problem, not a server error — cli-tools'
	// parser rejects the whole block rather than rendering what it can, so
	// show that as an inline notice instead of a hard 500. The edit form
	// still works either way, so the author can fix it.
	rendered, bodyErr := texts.RenderHTML(wrapDialogBody(d.Body, d.Language, d.Script))
	var renderedTranscription, renderedTranslation template.HTML
	var transcriptionErr, translationErr error
	if d.Transcription != "" {
		// Transcription is a pinned-Latin romanization, independent of the
		// dialog's own script — always wrapped as script=latn so it never
		// inherits e.g. RTL from a Hebrew/Arabic source dialog.
		renderedTranscription, transcriptionErr = texts.RenderHTML(wrapDialogBody(d.Transcription, d.Language, "latn"))
	}
	if hasTranslation {
		// Translation is written in the viewer's own site locale (en/pl —
		// both Latin script, LTR), never the dialog's own source script —
		// wrapping it with d.Script wrongly inherited e.g. RTL from a
		// Hebrew/Arabic source dialog even when the translation is plain
		// English. Pinned to latn, same treatment as transcription above.
		renderedTranslation, translationErr = texts.RenderHTML(wrapDialogBody(translationBody, d.Language, "latn"))
	}

	render(w, u.Locale, "dialogs-view.html", map[string]any{
		"User": u, "NavFlags": nv, "Dialog": d, "RenderedBody": rendered,
		"CanEdit": canEdit, "Script": scriptMeta, "Tags": dialogTags,
		"RenderedTranscription": renderedTranscription,
		"HasTranslation":        hasTranslation,
		"RenderedTranslation":   renderedTranslation,
		"BodyError":             renderErrString(bodyErr),
		"TranscriptionError":    renderErrString(transcriptionErr),
		"TranslationError":      renderErrString(translationErr),
		"SourceMarkdown":        wrapDialogBody(d.Body, d.Language, d.Script),
		"TranscriptionMarkdown": wrapDialogBody(d.Transcription, d.Language, "latn"),
		"TranslationMarkdown":   wrapDialogBody(translationBody, u.Locale, "latn"),
	})
}

func (s *Server) handleDialogEditForm(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d, err := s.dialogs.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, d.Language)
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
	dialogTags, err := s.tags.For(r.Context(), resourceTypeDialog, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	allTags, err := s.tags.AllNames(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	translationBody, _, err := s.translations.Get(r.Context(), resourceTypeDialog, id, u.Locale)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	render(w, u.Locale, "dialogs-edit.html", map[string]any{
		"User": u, "NavFlags": nv, "Dialog": d, "Languages": langs, "Scripts": scripts,
		"Tags": strings.Join(dialogTags, ", "), "AllTags": allTags, "Translation": translationBody,
	})
}

func (s *Server) handleDialogUpdate(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d, err := s.dialogs.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, d.Language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canEdit {
		http.NotFound(w, r)
		return
	}
	newLanguage := r.FormValue("language")
	if newLanguage != d.Language {
		canEditNew, err := s.roles.CanEdit(r.Context(), u.ID, newLanguage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !canEditNew {
			http.Error(w, i18n.T(u.Locale, "dialogs.err_no_move_language"), http.StatusForbidden)
			return
		}
	}
	if err := s.dialogs.Update(r.Context(), id, r.FormValue("title"), r.FormValue("body"), r.FormValue("transcription"), newLanguage, r.FormValue("script")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.tags.SetFor(r.Context(), resourceTypeDialog, id, tags.Parse(r.FormValue("tags"))); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.translations.Set(r.Context(), resourceTypeDialog, id, u.Locale, r.FormValue("translation")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dialogs/"+strconv.FormatInt(id, 10), http.StatusFound)
}

func (s *Server) handleDialogDelete(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d, err := s.dialogs.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, d.Language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canEdit {
		http.NotFound(w, r)
		return
	}
	if err := s.dialogs.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.tags.DeleteFor(r.Context(), resourceTypeDialog, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dialogs", http.StatusFound)
}

// renderErrString returns err's message, or "" if err is nil — a small
// helper so handleDialogView's template data can carry three independent
// render errors (body/transcription/translation) without a per-field if.
func renderErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// wrapDialogBody wraps a bare "--:" turn body with the {start-dialog}/
// {end-dialog} markers cli-tools' parser needs to recognize it as a dialog
// at all — without them the turns just fall through as plain paragraphs
// (cli-tools' typographic dash substitution even turns "--:" into "–:").
// A Dialog resource already knows its own language/script, so authors type
// turns directly; lang=/script= are filled in here instead of by hand. An
// author who wants explicit control (e.g. as=translation for a parallel
// dialog) can still write their own {start-dialog ...} — detected by prefix
// and left untouched (but still sanitized — see below), not double-wrapped.
func wrapDialogBody(body, language, script string) string {
	// Browser <textarea> submissions are CRLF ("\r\n"); cli-tools' own
	// ToHTML normalizes that to LF internally before parsing, but only
	// *after* this function's own scan below runs on the raw string — so
	// without normalizing here too, isDialogHeaderLine never matches a
	// "--:\r"-terminated line and the blank-line bug it guards against
	// comes right back for any real (non-curl-test) authored content.
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	body = stripBlankBeforeDialogHeader(body)
	if strings.HasPrefix(strings.TrimSpace(body), "{start-dialog") {
		return body
	}
	// No blank line between the marker and body: see stripBlankBeforeDialogHeader
	// below — a leading blank line hits the exact same bug it's guarding
	// against everywhere else in the body.
	return fmt.Sprintf("{start-dialog lang=%s script=%s}\n%s\n{end-dialog}", language, script, body)
}

// stripBlankBeforeDialogHeader removes any blank line that immediately
// precedes a heading ("# ..."), note ("(...)"), or turn header ("--:" or
// "@Name:"/"＠Name:") line — anywhere in a dialog body, not just at the top.
//
// cli-tools' dialog parser (parseDialogItems) flushes its pending-turn
// buffer whenever it hits one of those lines, and it flushes unconditionally
// once the buffer is non-empty — even if the only thing buffered is a single
// blank line. A perfectly natural "# heading\n\n--:\n  turn text" (blank
// line for readability between the heading and the first turn) therefore
// produces a spurious empty turn (an empty "—" row) right before the real
// one. There's no way to fix that from cli-tools' own parser without
// forking it, so it's neutralized here by dropping the blank line(s) before
// rendering — this only touches blank lines that would otherwise trigger
// the bug, never a blank line inside an indented turn's own content (which
// is followed by more indented text, not a header line).
func stripBlankBeforeDialogHeader(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t\r") != "" {
			out = append(out, lines[i])
			continue
		}
		// Blank line: look past the rest of this blank run to the next
		// non-blank line. Drop the whole run if that line is a header —
		// keep it otherwise (e.g. blank lines between two turns).
		j := i
		for j < len(lines) && strings.TrimRight(lines[j], " \t\r") == "" {
			j++
		}
		if j < len(lines) && isDialogHeaderLine(lines[j]) {
			i = j - 1 // skip the whole blank run; outer loop's i++ lands on j
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}

// isDialogHeaderLine mirrors cli-tools' own isBlockHeader/isBlockNote/
// isDialogItemHeader (pkg/tool/markdown/parser.go) closely enough to detect
// the lines that trigger its parser's flush() — not a full reimplementation,
// just the shape needed to know "a blank line before this one is dangerous".
func isDialogHeaderLine(line string) bool {
	s := strings.TrimRight(line, " *")
	if s == "--:" {
		return true
	}
	if (strings.HasPrefix(s, "@") || strings.HasPrefix(s, "＠")) &&
		(strings.HasSuffix(s, ":") || strings.HasSuffix(s, "︰") || strings.HasSuffix(s, "：")) {
		return true
	}
	if len(s) > 0 && s[0] == '#' {
		i := 0
		for i < len(s) && s[i] == '#' {
			i++
		}
		if i <= 6 && (i == len(s) || s[i] == ' ' || s[i] == '\t') {
			return true
		}
	}
	if len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' {
		return true
	}
	return false
}
