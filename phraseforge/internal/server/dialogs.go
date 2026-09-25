package server

import (
	"encoding/json"
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

// dialogsAppI18nKeys mirrors textsAppI18nKeys (server.go) for the Dialogs
// SPA shell — gathered by grepping dialogs-list/new/edit/view.html's
// {{call .T "..."}} calls before removing those templates.
var dialogsAppI18nKeys = []string{
	"dialogs.title", "texts.new", "dialogs.empty", "texts.no_access",
	"texts.tag_filter", "texts.tag_filter_clear",
	"dialogs.new_title", "texts.field_title", "texts.field_language", "texts.field_script",
	"texts.field_tags", "texts.field_tags_hint",
	"texts.tab_source", "texts.tab_transcription", "texts.tab_translation",
	"dialogs.field_body_hint", "texts.field_transcription_hint", "texts.field_translation_hint",
	"texts.save", "llm.transcribe", "llm.translate",
	"dialogs.edit_title", "texts.back", "texts.edit", "texts.delete", "dialogs.delete_confirm",
	"dialogs.render_error_prefix",
	"texts.ingest", "dialogs.ingest_title", "texts.ingest_source_label",
	"texts.ingest_source_text", "texts.ingest_source_file", "texts.ingest_source_url",
	"texts.ingest_field_text", "texts.ingest_field_file", "texts.ingest_field_url",
	"texts.ingest_submit", "texts.ingest_started",
	"texts.err_ingest_no_file", "texts.err_ingest_file_read",
	"texts.export", "texts.import",
	"texts.import_result_imported", "texts.import_result_deleted", "texts.import_result_unchanged",
	"texts.import_result_errors", "texts.import_errors_close", "texts.err_import_file_read",
}

func dialogsAppI18n(loc string) map[string]string {
	out := make(map[string]string, len(dialogsAppI18nKeys))
	for _, k := range dialogsAppI18nKeys {
		out[k] = i18n.T(loc, k)
	}
	return out
}

// --- JSON API (phraseforge-spa-dialogs) ---

type apiDialogSummary struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Language  string   `json:"language"`
	Script    string   `json:"script"`
	Tags      []string `json:"tags"`
	CreatedAt string   `json:"createdAt"`
}

type apiDialogDetail struct {
	ID                    int64    `json:"id"`
	Title                 string   `json:"title"`
	Language              string   `json:"language"`
	Script                string   `json:"script"`
	Body                  string   `json:"body"`
	Transcription         string   `json:"transcription"`
	Translation           string   `json:"translation"`
	Tags                  []string `json:"tags"`
	CanEdit               bool     `json:"canEdit"`
	ScriptDirection       string   `json:"scriptDirection"`
	ScriptEnlarged        bool     `json:"scriptEnlarged"`
	RenderedBody          string   `json:"renderedBody,omitempty"`
	BodyError             string   `json:"bodyError,omitempty"`
	RenderedTranscription string   `json:"renderedTranscription,omitempty"`
	TranscriptionError    string   `json:"transcriptionError,omitempty"`
	HasTranslation        bool     `json:"hasTranslation"`
	RenderedTranslation   string   `json:"renderedTranslation,omitempty"`
	TranslationError      string   `json:"translationError,omitempty"`
	SourceMarkdown        string   `json:"sourceMarkdown"`
	TranscriptionMarkdown string   `json:"transcriptionMarkdown,omitempty"`
	TranslationMarkdown   string   `json:"translationMarkdown,omitempty"`
}

// apiDialogRequest is the JSON body shape for both create and update — the
// raw author-typed body/transcription, stored as-is (wrapDialogBody is
// called only when rendering, at GET time, exactly matching
// handleDialogCreate/handleDialogUpdate's original behavior of never
// wrapping before storing).
type apiDialogRequest struct {
	Title         string `json:"title"`
	Language      string `json:"language"`
	Script        string `json:"script"`
	Body          string `json:"body"`
	Transcription string `json:"transcription"`
	Translation   string `json:"translation"`
	Tags          string `json:"tags"`
}

// apiListDialogs is handleDialogList's exact logic, JSON-encoded.
func (s *Server) apiListDialogs(w http.ResponseWriter, r *http.Request) {
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
	list, err := s.dialogs.List(r.Context(), langs, all)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	tagFilter := r.URL.Query().Get("tag")
	if tagFilter != "" {
		ids, err := s.tags.ResourceIDsWithTag(r.Context(), resourceTypeDialog, tagFilter)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
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
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]apiDialogSummary, len(list))
	for i, d := range list {
		out[i] = apiDialogSummary{
			ID: d.ID, Title: d.Title, Language: d.Language, Script: d.Script,
			Tags: tagsByID[d.ID], CreatedAt: d.CreatedAt.Format("Jan 2, 2006 · 15:04"),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// apiCreateDialog is handleDialogCreate's exact logic — stores the raw
// body, never wraps it (wrapDialogBody is a render-time-only concern).
func (s *Server) apiCreateDialog(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var req apiDialogRequest
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
		writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, "dialogs.err_no_edit_language"))
		return
	}
	id, err := s.dialogs.Create(r.Context(), u.ID, req.Title, req.Body, req.Transcription, req.Language, req.Script, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.tags.SetFor(r.Context(), resourceTypeDialog, id, tags.Parse(req.Tags)); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.translations.Set(r.Context(), resourceTypeDialog, id, u.Locale, req.Translation); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// apiGetDialog is handleDialogView's exact logic, including wrapDialogBody
// at render time and the three independent render-error fields — a render
// failure is data (a 200 with an error string), never an HTTP error.
func (s *Server) apiGetDialog(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "dialog not found")
		return
	}
	d, err := s.dialogs.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "dialog not found")
		return
	}
	canView, err := s.roles.CanView(r.Context(), u.ID, d.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canView {
		writeErr(w, http.StatusNotFound, "not_found", "dialog not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, d.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	scriptMeta, err := catalog.GetScript(r.Context(), s.db, d.Script)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	dialogTags, err := s.tags.For(r.Context(), resourceTypeDialog, d.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	translationBody, hasTranslation, err := s.translations.Get(r.Context(), resourceTypeDialog, d.ID, u.Locale)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	rendered, bodyErr := texts.RenderHTML(wrapDialogBody(d.Body, d.Language, d.Script))
	var renderedTranscription, renderedTranslation template.HTML
	var transcriptionErr, translationErr error
	if d.Transcription != "" {
		renderedTranscription, transcriptionErr = texts.RenderHTML(wrapDialogBody(d.Transcription, d.Language, "latn"))
	}
	if hasTranslation {
		renderedTranslation, translationErr = texts.RenderHTML(wrapDialogBody(translationBody, d.Language, "latn"))
	}

	writeJSON(w, http.StatusOK, apiDialogDetail{
		ID: d.ID, Title: d.Title, Language: d.Language, Script: d.Script,
		Body: d.Body, Transcription: d.Transcription, Translation: translationBody,
		Tags: dialogTags, CanEdit: canEdit,
		ScriptDirection: scriptMeta.Direction, ScriptEnlarged: scriptMeta.Enlarged,
		RenderedBody: string(rendered), BodyError: renderErrString(bodyErr),
		RenderedTranscription: string(renderedTranscription), TranscriptionError: renderErrString(transcriptionErr),
		HasTranslation: hasTranslation, RenderedTranslation: string(renderedTranslation), TranslationError: renderErrString(translationErr),
		SourceMarkdown:        wrapDialogBody(d.Body, d.Language, d.Script),
		TranscriptionMarkdown: wrapDialogBody(d.Transcription, d.Language, "latn"),
		TranslationMarkdown:   wrapDialogBody(translationBody, u.Locale, "latn"),
	})
}

// apiUpdateDialog is handleDialogUpdate's exact logic, including the
// moving-to-a-different-language re-check.
func (s *Server) apiUpdateDialog(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "dialog not found")
		return
	}
	d, err := s.dialogs.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "dialog not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, d.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusNotFound, "not_found", "dialog not found")
		return
	}
	var req apiDialogRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Language != d.Language {
		canEditNew, err := s.roles.CanEdit(r.Context(), u.ID, req.Language)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		if !canEditNew {
			writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, "dialogs.err_no_move_language"))
			return
		}
	}
	if err := s.dialogs.Update(r.Context(), id, req.Title, req.Body, req.Transcription, req.Language, req.Script); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.tags.SetFor(r.Context(), resourceTypeDialog, id, tags.Parse(req.Tags)); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.translations.Set(r.Context(), resourceTypeDialog, id, u.Locale, req.Translation); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// apiDeleteDialog is handleDialogDelete's exact logic.
func (s *Server) apiDeleteDialog(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "dialog not found")
		return
	}
	d, err := s.dialogs.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "dialog not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, d.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusNotFound, "not_found", "dialog not found")
		return
	}
	if err := s.dialogs.Delete(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.tags.DeleteFor(r.Context(), resourceTypeDialog, id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
