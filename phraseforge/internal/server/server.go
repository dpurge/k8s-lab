package server

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"slices"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phraseforge/internal/ai"
	"phraseforge/internal/auth"
	"phraseforge/internal/catalog"
	"phraseforge/internal/dialogs"
	"phraseforge/internal/i18n"
	"phraseforge/internal/ime"
	"phraseforge/internal/jobs"
	"phraseforge/internal/models"
	"phraseforge/internal/pagination"
	"phraseforge/internal/roles"
	"phraseforge/internal/tags"
	"phraseforge/internal/texts"
	"phraseforge/internal/translations"
	"phraseforge/internal/vocabulary"
)

// resourceTypeText, resourceTypeDialog, resourceTypeVocab, resourceTypeModels
// identify texts/dialogs/vocabulary lists/model lists to the
// resource-agnostic tags package (a list is tagged as a whole; its items
// aren't tagged separately). A future resource type (media) gets its own
// constant here when it's built — the tags package itself never changes.
const (
	resourceTypeText   = "text"
	resourceTypeDialog = "dialog"
	resourceTypeVocab  = "vocabulary_list"
	resourceTypeModels = "models_list"
)

//go:embed templates/*.html
var templateFS embed.FS

// static serves the ported deno-app IME assets (ime/*.json presets,
// js/ime.js key-handling engine, css/*.css per-script fonts) plus
// static/js/editor.js, this app's own glue code — see internal/server/static.
//
//go:embed static
var staticFS embed.FS

// pages maps each content template to its own *Template paired with layout.html.
// html/template associates {{define}} blocks by name across one parsed set, so
// parsing all pages together would let the last-parsed "content" block silently
// win for every page — hence one Template per page instead of a single shared set.
var pages = map[string]*template.Template{}

func init() {
	names := []string{
		"login.html", "signup.html", "app.html",
	}
	for _, name := range names {
		pages[name] = template.Must(template.ParseFS(templateFS, "templates/layout.html", "templates/"+name))
	}
}

type Server struct {
	db           *pgxpool.Pool
	auth         *auth.Service
	texts        *texts.Store
	dialogs      *dialogs.Store
	vocab        *vocabulary.Store
	models       *models.Store
	roles        *roles.Service
	tags         *tags.Store
	translations *translations.Store
	ai           *ai.Service
	jobs         *jobs.Service
}

func New(db *pgxpool.Pool, authSvc *auth.Service, textStore *texts.Store, dialogStore *dialogs.Store, vocabStore *vocabulary.Store, modelsStore *models.Store, rolesSvc *roles.Service, tagsSvc *tags.Store, translationsSvc *translations.Store, aiSvc *ai.Service, jobsSvc *jobs.Service) *Server {
	return &Server{db: db, auth: authSvc, texts: textStore, dialogs: dialogStore, vocab: vocabStore, models: modelsStore, roles: rolesSvc, tags: tagsSvc, translations: translationsSvc, ai: aiSvc, jobs: jobsSvc}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(recoverPanic)

	// Static 200 — DB down should not restart the pod, only fail readiness.
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Get("/ready", func(w http.ResponseWriter, r *http.Request) {
		if err := s.db.Ping(r.Context()); err != nil {
			http.Error(w, "database unreachable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embedded at build time — a missing "static" dir is a programmer error, not a runtime one
	}
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServerFS(staticSub)))

	r.Get("/login", s.handleLoginForm)
	r.Post("/login", s.handleLogin)
	r.Get("/logout", s.handleLogout)
	r.Get("/signup", s.handleSignupForm)
	r.Post("/signup", s.handleSignup)

	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		// The whole app is one unified SPA shell now — see specs/features/
		// phraseforge-spa-unified-shell.md. GET / serves it (handleApp);
		// the five old per-resource shell URLs collapse to this single one
		// (redirected below, not removed outright, so an old bookmark still
		// lands somewhere sensible instead of a bare 404).
		r.Get("/", s.handleApp)
		redirectToApp := func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/", http.StatusFound) }
		r.Get("/dialogs", redirectToApp)
		r.Get("/vocabulary", redirectToApp)
		r.Get("/models", redirectToApp)

		r.Get("/ime-config", s.handleIMEConfig)

		// phraseforge's first JSON API — see phraseforge-spa-shell-texts.
		// Texts' own old HTML routes/templates have already been removed
		// (this is the completed cutover, not a coexistence period); future
		// resource types repeat this same pattern, each removed only once
		// its own SPA replacement is fully validated.
		r.Route("/api/v1", func(r chi.Router) {
			r.Get("/texts", s.apiListTexts)
			r.Post("/texts", s.apiCreateText)
			// phraseforge-ingest-texts-dialogs: same requireAuth/CanEdit
			// authorization as the "New" form's own create endpoint above —
			// ingest is not admin-gated.
			r.Post("/texts/ingest", s.apiIngestText)
			// phraseforge-export-import: same requireAuth/CanEdit
			// authorization as create/update above — export/import is not
			// admin-gated either (see export_import.go).
			r.Get("/texts/export", s.apiExportTexts)
			r.Post("/texts/import", s.apiImportTexts)
			r.Get("/texts/{id}", s.apiGetText)
			r.Put("/texts/{id}", s.apiUpdateText)
			r.Delete("/texts/{id}", s.apiDeleteText)
			// phraseforge-generate-vocab-models-from-text: same requireAuth/
			// CanEdit authorization as create/update above.
			r.Post("/texts/{id}/generate-vocabulary", s.apiGenerateVocabularyFromText)
			r.Post("/texts/{id}/generate-models", s.apiGenerateModelsFromText)
			// background-generate-title-transcription-translation: same
			// requireAuth/CanEdit authorization as create/update above.
			r.Post("/texts/{id}/generate-title", s.apiGenerateTitleFromText)
			r.Post("/texts/{id}/generate-transcription", s.apiGenerateTranscriptionFromText)
			r.Post("/texts/{id}/generate-translation", s.apiGenerateTranslationFromText)

			// Coexists with the old /dialogs/* HTML routes above temporarily
			// (phraseforge-spa-dialogs) — removed once the SPA replacement
			// is validated, per the established per-resource cutover pattern.
			r.Get("/dialogs", s.apiListDialogs)
			r.Post("/dialogs", s.apiCreateDialog)
			r.Post("/dialogs/ingest", s.apiIngestDialog)
			r.Get("/dialogs/export", s.apiExportDialogs)
			r.Post("/dialogs/import", s.apiImportDialogs)
			r.Get("/dialogs/{id}", s.apiGetDialog)
			r.Put("/dialogs/{id}", s.apiUpdateDialog)
			r.Delete("/dialogs/{id}", s.apiDeleteDialog)
			// dialog-vocabulary-models-generation: same requireAuth/CanEdit
			// authorization as create/update above.
			r.Post("/dialogs/{id}/generate-vocabulary", s.apiGenerateVocabularyFromDialog)
			r.Post("/dialogs/{id}/generate-models", s.apiGenerateModelsFromDialog)
			// background-generate-title-transcription-translation: same
			// requireAuth/CanEdit authorization as create/update above.
			r.Post("/dialogs/{id}/generate-title", s.apiGenerateTitleFromDialog)
			r.Post("/dialogs/{id}/generate-transcription", s.apiGenerateTranscriptionFromDialog)
			r.Post("/dialogs/{id}/generate-translation", s.apiGenerateTranslationFromDialog)

			// Coexists with the old /vocabulary/* HTML routes above
			// temporarily (phraseforge-spa-vocabulary).
			r.Get("/vocabulary", s.apiListVocabLists)
			r.Post("/vocabulary", s.apiCreateVocabList)
			// phraseforge-export-import: same requireAuth/CanEdit
			// authorization as create/update above — export/import is not
			// admin-gated either (see export_import.go).
			r.Get("/vocabulary/export", s.apiExportVocabulary)
			r.Post("/vocabulary/import", s.apiImportVocabulary)
			r.Get("/vocabulary/{id}", s.apiGetVocabList)
			r.Put("/vocabulary/{id}", s.apiUpdateVocabList)
			r.Delete("/vocabulary/{id}", s.apiDeleteVocabList)
			r.Post("/vocabulary/{id}/items", s.apiAddVocabItem)
			r.Put("/vocabulary/{id}/items/{position}", s.apiUpdateVocabItem)
			r.Delete("/vocabulary/{id}/items/{position}", s.apiDeleteVocabItem)
			// background-generate-title-transcription-translation: same
			// requireAuth/CanEdit authorization as add/update above.
			r.Post("/vocabulary/{id}/items/{position}/generate-transcription", s.apiGenerateVocabItemTranscription)
			r.Post("/vocabulary/{id}/items/{position}/generate-translation", s.apiGenerateVocabItemTranslation)
			r.Post("/vocabulary/{id}/generate-missing-translations", s.apiGenerateMissingVocabTranslations)

			r.Get("/models", s.apiListModelsLists)
			r.Post("/models", s.apiCreateModelsList)
			r.Get("/models/export", s.apiExportModels)
			r.Post("/models/import", s.apiImportModels)
			r.Get("/models/{id}", s.apiGetModelsList)
			r.Put("/models/{id}", s.apiUpdateModelsList)
			r.Delete("/models/{id}", s.apiDeleteModelsList)
			r.Post("/models/{id}/items", s.apiAddModelsItem)
			r.Put("/models/{id}/items/{position}", s.apiUpdateModelsItem)
			r.Delete("/models/{id}/items/{position}", s.apiDeleteModelsItem)
			// background-generate-title-transcription-translation: same
			// requireAuth/CanEdit authorization as add/update above.
			r.Post("/models/{id}/items/{position}/generate-transcription", s.apiGenerateModelsItemTranscription)
			r.Post("/models/{id}/items/{position}/generate-translation", s.apiGenerateModelsItemTranslation)

			r.Get("/profile", s.apiGetProfile)
			r.Post("/profile/locale", s.apiSetProfileLocale)
			r.Post("/profile/password", s.apiChangeProfilePassword)

			r.Get("/app-bootstrap", s.apiGetAppBootstrap)
		})
		r.Get("/profile", redirectToApp)

		// Admin still gets its own requireAdmin-gated group — GET /admin
		// redirects like the other four old shell URLs (a non-admin still
		// gets 404, unchanged); GET /admin/config/export stays a real link
		// (file download) — everything else goes through /api/v1/admin.
		r.Group(func(r chi.Router) {
			r.Use(s.requireAdmin)
			r.Get("/admin", redirectToApp)
			r.Get("/admin/config/export", s.handleAdminExportConfig)

			r.Route("/api/v1/admin", func(r chi.Router) {
				r.Get("/", s.apiGetAdminBootstrap)
				r.Post("/grants", s.apiCreateAdminGrant)
				r.Delete("/grants/{id}", s.apiRevokeAdminGrant)
				r.Post("/ime", s.apiSetAdminIME)
				r.Delete("/ime/{language}/{script}", s.apiDeleteAdminIME)
				r.Post("/llm-prompts", s.apiSetAdminLLMPrompt)
				r.Delete("/llm-prompts/{kind}/{sourceLanguage}/{targetLanguage}", s.apiDeleteAdminLLMPrompt)
				r.Post("/config/import", s.apiImportAdminConfig)
				r.Get("/jobs", s.apiListAdminJobs)
				r.Post("/jobs/clear", s.apiClearAdminJobs)
				r.Get("/jobs/{id}", s.apiGetAdminJob)
				r.Post("/jobs/{id}/retry", s.apiRetryAdminJob)
				r.Post("/jobs/{id}/cancel", s.apiCancelAdminJob)
				r.Delete("/jobs/{id}", s.apiDeleteAdminJob)
			})
		})
	})

	return r
}

type ctxKey int

const userCtxKey ctxKey = 0

func recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("panic serving %s %s: %v", r.Method, r.URL.Path, v)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.auth.CurrentUser(r)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAdmin runs after requireAuth and 404s (not 403 — don't reveal the
// route exists) any non-admin.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		isAdmin, err := s.roles.IsAdmin(r.Context(), u.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !isAdmin {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func currentUser(r *http.Request) auth.User {
	u, _ := r.Context().Value(userCtxKey).(auth.User)
	return u
}

// locale resolves the site UI language for r: the signed-in user's saved
// preference, or i18n.Default before login (there's no account yet to hold
// a preference for).
func locale(r *http.Request) string {
	u := currentUser(r)
	if u.Locale == "" {
		return i18n.Default
	}
	return u.Locale
}

// nav bundles the sidebar-visibility flags every authenticated page needs
// (which languages the user may create texts in, and whether they're admin),
// plus the language-filter select's own options (every language the user may
// view at all).
type nav struct {
	IsAdmin           bool
	CanCreateAny      bool
	EditableLangs     []string
	EditableAllLng    bool
	ViewableLanguages []catalog.Language // options for the sidebar language-filter select
}

func (s *Server) loadNav(ctx context.Context, userID int64) (nav, error) {
	editLangs, editAll, err := s.roles.EditableLanguages(ctx, userID)
	if err != nil {
		return nav{}, err
	}
	viewLangs, viewAll, err := s.roles.ViewableLanguages(ctx, userID)
	if err != nil {
		return nav{}, err
	}
	isAdmin, err := s.roles.IsAdmin(ctx, userID)
	if err != nil {
		return nav{}, err
	}
	allCatalogLangs, err := catalog.ListLanguages(ctx, s.db)
	if err != nil {
		return nav{}, err
	}
	return nav{
		IsAdmin: isAdmin, CanCreateAny: editAll || len(editLangs) > 0,
		EditableLangs: editLangs, EditableAllLng: editAll,
		ViewableLanguages: filterLanguages(allCatalogLangs, viewLangs, viewAll),
	}, nil
}

// filterLanguages restricts all to just the given codes, unless includeAll
// (the caller is admin), in which case every language is returned unfiltered.
func filterLanguages(all []catalog.Language, codes []string, includeAll bool) []catalog.Language {
	if includeAll {
		return all
	}
	out := make([]catalog.Language, 0, len(all))
	for _, l := range all {
		if slices.Contains(codes, l.Code) {
			out = append(out, l)
		}
	}
	return out
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.auth.CurrentUser(r); ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	render(w, locale(r), "login.html", map[string]any{})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")
	u, err := s.auth.Authenticate(r.Context(), username, password)
	if err != nil {
		render(w, i18n.Default, "login.html", map[string]any{"Message": i18n.T(i18n.Default, "login.error"), "MessageType": "error"})
		return
	}
	s.auth.SetSession(w, u.ID)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleSignupForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.auth.CurrentUser(r); ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	render(w, locale(r), "signup.html", map[string]any{})
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm_password")
	loc := i18n.Default // no account yet to carry a preference

	if username == "" {
		render(w, loc, "signup.html", map[string]any{"Message": i18n.T(loc, "signup.err_username_required"), "MessageType": "error", "Username": username})
		return
	}
	if password != confirm {
		render(w, loc, "signup.html", map[string]any{"Message": i18n.T(loc, "signup.err_password_mismatch"), "MessageType": "error", "Username": username})
		return
	}
	if len(password) < 8 {
		render(w, loc, "signup.html", map[string]any{"Message": i18n.T(loc, "signup.err_password_length"), "MessageType": "error", "Username": username})
		return
	}
	u, err := s.auth.Register(r.Context(), username, password)
	if err != nil {
		msg := i18n.T(loc, "signup.err_generic")
		if err == auth.ErrUsernameTaken {
			msg = i18n.T(loc, "signup.err_username_taken")
		}
		render(w, loc, "signup.html", map[string]any{"Message": msg, "MessageType": "error", "Username": username})
		return
	}
	s.auth.SetSession(w, u.ID)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.ClearSession(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}

// textsAppI18nKeys lists every i18n key list.html/new.html/edit.html/
// view.html use (gathered by grepping their {{call .T "..."}} calls before
// removing those templates) — the exact set handleTextsApp resolves once,
// server-side, into a flat {key: translatedString} map for texts-app.js.
var textsAppI18nKeys = []string{
	"texts.title", "texts.new", "texts.empty", "texts.no_access",
	"texts.tag_filter", "texts.tag_filter_clear",
	"texts.new_title", "texts.field_title", "texts.field_language", "texts.field_script",
	"texts.field_tags", "texts.field_tags_hint",
	"texts.tab_source", "texts.tab_transcription", "texts.tab_translation",
	"texts.field_body", "texts.field_transcription_hint", "texts.field_translation_hint",
	"texts.save", "llm.transcribe", "llm.translate",
	"texts.edit_title", "texts.back", "texts.edit", "texts.delete", "texts.delete_confirm",
	"texts.ingest", "texts.ingest_title", "texts.ingest_source_label",
	"texts.ingest_source_text", "texts.ingest_source_file", "texts.ingest_source_url",
	"texts.ingest_field_text", "texts.ingest_field_file", "texts.ingest_field_url",
	"texts.ingest_submit", "texts.ingest_started",
	"texts.err_ingest_no_file", "texts.err_ingest_file_read",
	"texts.export", "texts.import",
	"texts.import_result_imported", "texts.import_result_deleted", "texts.import_result_unchanged",
	"texts.import_result_errors", "texts.import_errors_close", "texts.err_import_file_read",
	"texts.generate_vocabulary", "texts.generate_models",
	"texts.generate_vocabulary_started", "texts.generate_models_started",
	"texts.linked_vocabulary", "texts.linked_models",
	"texts.generate_title", "texts.generate_transcription", "texts.generate_translation",
	"texts.generate_title_started", "texts.generate_transcription_started", "texts.generate_translation_started",
	"texts.pagination_previous", "texts.pagination_next",
}

func textsAppI18n(loc string) map[string]string {
	out := make(map[string]string, len(textsAppI18nKeys))
	for _, k := range textsAppI18nKeys {
		out[k] = i18n.T(loc, k)
	}
	return out
}

// filterByID keeps only the items whose id (extracted via idOf) is in keep —
// shared by the texts and dialogs list handlers' tag-filter step, and (with
// tags.ResourceIDsWithAllTags) by all four export handlers' own ALL-match
// tags= filter.
func filterByID[T any](list []T, keep []int64, idOf func(T) int64) []T {
	keepSet := make(map[int64]bool, len(keep))
	for _, id := range keep {
		keepSet[id] = true
	}
	out := make([]T, 0, len(list))
	for _, item := range list {
		if keepSet[idOf(item)] {
			out = append(out, item)
		}
	}
	return out
}

// filterByScript keeps only the items whose script (extracted via
// scriptOf) equals script exactly — shared by all four export handlers'
// required script= filter.
func filterByScript[T any](list []T, script string, scriptOf func(T) string) []T {
	out := make([]T, 0, len(list))
	for _, item := range list {
		if scriptOf(item) == script {
			out = append(out, item)
		}
	}
	return out
}

// handleIMEConfig serves the source/transcription IME assignment AND the
// script's own direction/enlarged styling for a (language, script) pair, as
// JSON — resource-agnostic (texts and dialogs both use it) since it only
// depends on language+script, for the editor's JS to fetch when the
// language/script selects change (see static/js/editor.js).
//
// direction/enlarged come from the script catalog, not from ime_config: they
// must apply whenever that script is selected, whether or not an admin has
// configured an IME preset for it — RTL/Hebrew looking wrong just because
// nobody picked an IME yet would be a bug, not a missing feature.
func (s *Server) handleIMEConfig(w http.ResponseWriter, r *http.Request) {
	script := r.URL.Query().Get("script")
	language := r.URL.Query().Get("language")
	cfg, found, err := ime.GetConfig(r.Context(), s.db, language, script)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		needs, err := ime.NeedsTranscriptionForLanguage(r.Context(), s.db, language)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cfg.NeedsTranscription = needs
	}
	scriptMeta, found, err := catalog.GetScriptIfExists(r.Context(), s.db, script)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	direction, enlarged := "ltr", false
	if found {
		direction, enlarged = scriptMeta.Direction, scriptMeta.Enlarged
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // headers already sent; nothing to do if encoding fails
		"source_ime":          cfg.SourceIME,
		"transcription_ime":   cfg.TranscriptionIME,
		"needs_transcription": cfg.NeedsTranscription,
		"script":              script,
		"direction":           direction,
		"enlarged":            enlarged,
	})
}

// formOptions returns the language dropdown (restricted to nv's editable
// languages, unless admin) and the full script list (every script applies to
// every language) for the new/edit text forms.
func (s *Server) formOptions(ctx context.Context, nv nav) ([]catalog.Language, []catalog.Script, error) {
	allLangs, err := catalog.ListLanguages(ctx, s.db)
	if err != nil {
		return nil, nil, err
	}
	scripts, err := catalog.ListScripts(ctx, s.db)
	if err != nil {
		return nil, nil, err
	}
	return filterLanguages(allLangs, nv.EditableLangs, nv.EditableAllLng), scripts, nil
}

// --- JSON API (phraseforge-spa-shell-texts) ---
//
// writeJSON/writeErr mirror knowledge's own convention exactly
// (knowledge/internal/server/server.go) for cross-app consistency: a
// success body is whatever shape the caller passes; an error body is
// always {"error": {"code": "...", "message": "..."}}.

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck // headers already sent; nothing to do if encoding fails
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}}) //nolint:errcheck
}

type apiTextSummary struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Language  string   `json:"language"`
	Script    string   `json:"script"`
	Tags      []string `json:"tags"`
	CreatedAt string   `json:"createdAt"`
}

type apiTextDetail struct {
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
	RenderedBody          string   `json:"renderedBody"`
	RenderedTranscription string   `json:"renderedTranscription,omitempty"`
	HasTranslation        bool     `json:"hasTranslation"`
	RenderedTranslation   string   `json:"renderedTranslation,omitempty"`
	SourceMarkdown        string   `json:"sourceMarkdown"`
	TranscriptionMarkdown string   `json:"transcriptionMarkdown,omitempty"`
	TranslationMarkdown   string   `json:"translationMarkdown,omitempty"`
	VocabularyListID      *int64   `json:"vocabularyListId,omitempty"`
	ModelsListID          *int64   `json:"modelsListId,omitempty"`
	// NeedsTranscription mirrors ime.NeedsTranscriptionForLanguage's own
	// query (background-generate-title-transcription-translation) — the
	// View page has no live language/script selects to derive this
	// client-side the way editor.js's /ime-config fetch does for the
	// New/Edit form, so it's resolved server-side instead, gating the
	// Generate Transcription button's visibility.
	NeedsTranscription bool `json:"needsTranscription"`
}

// apiTextRequest is the JSON body shape for both create (POST) and update
// (PUT) — same fields as the old handleCreate/handleUpdate's r.FormValue
// reads, just decoded from JSON instead.
type apiTextRequest struct {
	Title         string `json:"title"`
	Language      string `json:"language"`
	Script        string `json:"script"`
	Body          string `json:"body"`
	Transcription string `json:"transcription"`
	Translation   string `json:"translation"`
	Tags          string `json:"tags"`
}

// apiListTexts is handleList's exact logic (language/tag filtering,
// per-viewer visibility), JSON-encoded instead of rendered.
// decodeCursorParam decodes ?cursor= for every paginated list handler — a
// missing/invalid value is "first page", never a 400 (matches this app's
// existing lenient-query-param conventions, e.g. this same handler's own
// ?language= handling below).
func decodeCursorParam(r *http.Request) *pagination.Cursor {
	token := r.URL.Query().Get("cursor")
	if token == "" {
		return nil
	}
	c, err := pagination.Decode(token)
	if err != nil {
		return nil
	}
	return &c
}

func (s *Server) apiListTexts(w http.ResponseWriter, r *http.Request) {
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
		tagIDs, err = s.tags.ResourceIDsWithTag(r.Context(), resourceTypeText, tagFilter)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
	}
	list, hasMore, err := s.texts.ListPage(r.Context(), langs, all, tagIDs, decodeCursorParam(r), pagination.DefaultLimit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
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
	out := make([]apiTextSummary, len(list))
	for i, t := range list {
		out[i] = apiTextSummary{
			ID: t.ID, Title: t.Title, Language: t.Language, Script: t.Script,
			Tags: tagsByID[t.ID], CreatedAt: t.CreatedAt.Format("Jan 2, 2006 · 15:04"),
		}
	}
	resp := map[string]any{"items": out}
	if hasMore && len(list) > 0 {
		last := list[len(list)-1]
		resp["nextCursor"] = pagination.Encode(pagination.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	writeJSON(w, http.StatusOK, resp)
}

// apiCreateText is handleCreate's exact logic — same authorization check,
// same Create/SetFor/Set calls, in the same order.
func (s *Server) apiCreateText(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var req apiTextRequest
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
		writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, "texts.err_no_edit_language"))
		return
	}
	id, err := s.texts.Create(r.Context(), u.ID, req.Title, req.Body, req.Transcription, req.Language, req.Script, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.tags.SetFor(r.Context(), resourceTypeText, id, tags.Parse(req.Tags)); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.translations.Set(r.Context(), resourceTypeText, id, u.Locale, req.Translation); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// apiGetText is handleView's exact logic (CanView 404s exactly like
// today — never reveal existence to a viewer who can't see it).
func (s *Server) apiGetText(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "text not found")
		return
	}
	t, err := s.texts.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "text not found")
		return
	}
	canView, err := s.roles.CanView(r.Context(), u.ID, t.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canView {
		writeErr(w, http.StatusNotFound, "not_found", "text not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, t.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	scriptMeta, err := catalog.GetScript(r.Context(), s.db, t.Script)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	needsTranscription, err := ime.NeedsTranscriptionForLanguage(r.Context(), s.db, t.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	rendered, err := texts.RenderHTML(t.Body)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	textTags, err := s.tags.For(r.Context(), resourceTypeText, t.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	translationBody, hasTranslation, err := s.translations.Get(r.Context(), resourceTypeText, t.ID, u.Locale)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	var renderedTranscription, renderedTranslation template.HTML
	if t.Transcription != "" {
		if renderedTranscription, err = texts.RenderHTML(t.Transcription); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
	}
	if hasTranslation {
		if renderedTranslation, err = texts.RenderHTML(translationBody); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
	}
	var vocabularyListID, modelsListID *int64
	if vid, found, err := s.vocab.GetBySourceTextID(r.Context(), t.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	} else if found {
		vocabularyListID = &vid
	}
	if mid, found, err := s.models.GetBySourceTextID(r.Context(), t.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	} else if found {
		modelsListID = &mid
	}
	writeJSON(w, http.StatusOK, apiTextDetail{
		ID: t.ID, Title: t.Title, Language: t.Language, Script: t.Script,
		Body: t.Body, Transcription: t.Transcription, Translation: translationBody,
		Tags: textTags, CanEdit: canEdit,
		ScriptDirection: scriptMeta.Direction, ScriptEnlarged: scriptMeta.Enlarged,
		RenderedBody: string(rendered), RenderedTranscription: string(renderedTranscription),
		HasTranslation: hasTranslation, RenderedTranslation: string(renderedTranslation),
		SourceMarkdown:        textBlockMarkdown(t.Body, "source", t.Language, t.Script),
		TranscriptionMarkdown: textBlockMarkdown(t.Transcription, "transcription", t.Language, "latn"),
		TranslationMarkdown:   textBlockMarkdown(translationBody, "translation", u.Locale, "latn"),
		VocabularyListID:      vocabularyListID,
		ModelsListID:          modelsListID,
		NeedsTranscription:    needsTranscription,
	})
}

// apiUpdateText is handleUpdate's exact logic, including the
// moving-to-a-different-language re-check.
func (s *Server) apiUpdateText(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "text not found")
		return
	}
	t, err := s.texts.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "text not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, t.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusNotFound, "not_found", "text not found")
		return
	}
	var req apiTextRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Language != t.Language {
		canEditNew, err := s.roles.CanEdit(r.Context(), u.ID, req.Language)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		if !canEditNew {
			writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, "texts.err_no_move_language"))
			return
		}
	}
	if err := s.texts.Update(r.Context(), id, req.Title, req.Body, req.Transcription, req.Language, req.Script); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.tags.SetFor(r.Context(), resourceTypeText, id, tags.Parse(req.Tags)); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.translations.Set(r.Context(), resourceTypeText, id, u.Locale, req.Translation); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// apiDeleteText is handleDelete's exact logic.
func (s *Server) apiDeleteText(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "text not found")
		return
	}
	t, err := s.texts.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "text not found")
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, t.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canEdit {
		writeErr(w, http.StatusNotFound, "not_found", "text not found")
		return
	}
	if err := s.texts.Delete(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := s.tags.DeleteFor(r.Context(), resourceTypeText, id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func render(w http.ResponseWriter, loc, name string, data map[string]any) {
	t, ok := pages[name]
	if !ok {
		http.Error(w, "unknown template: "+name, http.StatusInternalServerError)
		return
	}
	if loc == "" {
		loc = i18n.Default
	}
	data["T"] = func(key string) string { return i18n.T(loc, key) }
	data["Locale"] = loc
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
