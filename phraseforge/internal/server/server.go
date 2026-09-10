package server

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phraseforge/internal/auth"
	"phraseforge/internal/catalog"
	"phraseforge/internal/dialogs"
	"phraseforge/internal/i18n"
	"phraseforge/internal/ime"
	"phraseforge/internal/models"
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
		"login.html", "signup.html", "list.html", "new.html", "view.html",
		"edit.html", "profile.html", "admin.html",
		"dialogs-list.html", "dialogs-new.html", "dialogs-view.html", "dialogs-edit.html",
		"vocab-list.html", "vocab-new.html", "vocab-view.html", "vocab-edit.html",
		"models-list.html", "models-new.html", "models-view.html", "models-edit.html",
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
}

func New(db *pgxpool.Pool, authSvc *auth.Service, textStore *texts.Store, dialogStore *dialogs.Store, vocabStore *vocabulary.Store, modelsStore *models.Store, rolesSvc *roles.Service, tagsSvc *tags.Store, translationsSvc *translations.Store) *Server {
	return &Server{db: db, auth: authSvc, texts: textStore, dialogs: dialogStore, vocab: vocabStore, models: modelsStore, roles: rolesSvc, tags: tagsSvc, translations: translationsSvc}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

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
		r.Get("/", s.handleList)
		r.Get("/texts/new", s.handleNewForm)
		r.Post("/texts", s.handleCreate)
		r.Get("/texts/{id}", s.handleView)
		r.Get("/texts/{id}/edit", s.handleEditForm)
		r.Post("/texts/{id}/edit", s.handleUpdate)
		r.Post("/texts/{id}/delete", s.handleDelete)

		r.Get("/dialogs", s.handleDialogList)
		r.Get("/dialogs/new", s.handleDialogNewForm)
		r.Post("/dialogs", s.handleDialogCreate)
		r.Get("/dialogs/{id}", s.handleDialogView)
		r.Get("/dialogs/{id}/edit", s.handleDialogEditForm)
		r.Post("/dialogs/{id}/edit", s.handleDialogUpdate)
		r.Post("/dialogs/{id}/delete", s.handleDialogDelete)

		r.Get("/vocabulary", s.handleVocabList)
		r.Get("/vocabulary/new", s.handleVocabNewForm)
		r.Post("/vocabulary", s.handleVocabCreate)
		r.Get("/vocabulary/{id}", s.handleVocabView)
		r.Get("/vocabulary/{id}/edit", s.handleVocabEditForm)
		r.Post("/vocabulary/{id}/edit", s.handleVocabUpdate)
		r.Post("/vocabulary/{id}/delete", s.handleVocabDelete)
		r.Post("/vocabulary/{id}/items", s.handleVocabItemCreate)
		r.Post("/vocabulary/{id}/items/{position}", s.handleVocabItemUpdate)
		r.Post("/vocabulary/{id}/items/{position}/delete", s.handleVocabItemDelete)

		r.Get("/models", s.handleModelsList)
		r.Get("/models/new", s.handleModelsNewForm)
		r.Post("/models", s.handleModelsCreate)
		r.Get("/models/{id}", s.handleModelsView)
		r.Get("/models/{id}/edit", s.handleModelsEditForm)
		r.Post("/models/{id}/edit", s.handleModelsUpdate)
		r.Post("/models/{id}/delete", s.handleModelsDelete)
		r.Post("/models/{id}/items", s.handleModelsItemCreate)
		r.Post("/models/{id}/items/{position}", s.handleModelsItemUpdate)
		r.Post("/models/{id}/items/{position}/delete", s.handleModelsItemDelete)

		r.Get("/ime-config", s.handleIMEConfig)
		r.Get("/profile", s.handleProfileForm)
		r.Post("/profile/password", s.handleChangePassword)
		r.Post("/profile/locale", s.handleSetLocale)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAdmin)
			r.Get("/admin", s.handleAdmin)
			r.Post("/admin/grants", s.handleAdminGrant)
			r.Post("/admin/grants/{id}/revoke", s.handleAdminRevoke)
			r.Post("/admin/ime", s.handleAdminSetIME)
			r.Post("/admin/ime/delete", s.handleAdminDeleteIME)
		})
	})

	return r
}

type ctxKey int

const userCtxKey ctxKey = 0

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

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
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

	// Sidebar language-filter select (persisted client-side in localStorage,
	// same mechanism as the theme — see layout.html). Ignored if it names a
	// language the viewer can't actually see, rather than erroring.
	languageFilter := r.URL.Query().Get("language")
	if languageFilter != "" && (all || slices.Contains(langs, languageFilter)) {
		langs, all = []string{languageFilter}, false
	}

	list, err := s.texts.List(r.Context(), langs, all)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tagFilter := r.URL.Query().Get("tag")
	if tagFilter != "" {
		ids, err := s.tags.ResourceIDsWithTag(r.Context(), resourceTypeText, tagFilter)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		list = filterByID(list, ids, func(t texts.Text) int64 { return t.ID })
	}

	ids := make([]int64, len(list))
	for i, t := range list {
		ids[i] = t.ID
	}
	tagsByID, err := s.tags.ForMany(r.Context(), resourceTypeText, ids)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	render(w, u.Locale, "list.html", map[string]any{
		"User": u, "Nav": "texts", "NavFlags": nv, "Texts": list,
		"TagsByID": tagsByID, "TagFilter": tagFilter, "LanguageFilter": languageFilter,
	})
}

// filterByID keeps only the items whose id (extracted via idOf) is in keep —
// shared by the texts and dialogs list handlers' tag-filter step.
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
	cfg, _, err := ime.GetConfig(r.Context(), s.db, r.URL.Query().Get("language"), script)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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

func (s *Server) handleNewForm(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	nv, err := s.loadNav(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !nv.CanCreateAny {
		http.Error(w, i18n.T(u.Locale, "texts.err_no_create_access"), http.StatusForbidden)
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
	render(w, u.Locale, "new.html", map[string]any{
		"User": u, "Nav": "new", "NavFlags": nv, "Languages": langs, "Scripts": scripts, "AllTags": allTags,
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

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	language := r.FormValue("language")
	script := r.FormValue("script")
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canEdit {
		http.Error(w, i18n.T(u.Locale, "texts.err_no_edit_language"), http.StatusForbidden)
		return
	}
	id, err := s.texts.Create(r.Context(), u.ID, r.FormValue("title"), r.FormValue("body"), r.FormValue("transcription"), language, script)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.tags.SetFor(r.Context(), resourceTypeText, id, tags.Parse(r.FormValue("tags"))); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Translation is stored per the editing user's own site locale — see
	// internal/translations.
	if err := s.translations.Set(r.Context(), resourceTypeText, id, u.Locale, r.FormValue("translation")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/texts/"+strconv.FormatInt(id, 10), http.StatusFound)
}

func (s *Server) handleView(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	t, err := s.texts.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	canView, err := s.roles.CanView(r.Context(), u.ID, t.Language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canView {
		http.NotFound(w, r)
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, t.Language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	nv, err := s.loadNav(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	scriptMeta, err := catalog.GetScript(r.Context(), s.db, t.Script)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rendered, err := texts.RenderHTML(t.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	textTags, err := s.tags.For(r.Context(), resourceTypeText, t.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Show whichever translation matches the viewer's own site locale, if any.
	translationBody, hasTranslation, err := s.translations.Get(r.Context(), resourceTypeText, t.ID, u.Locale)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var renderedTranscription, renderedTranslation template.HTML
	if t.Transcription != "" {
		if renderedTranscription, err = texts.RenderHTML(t.Transcription); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if hasTranslation {
		if renderedTranslation, err = texts.RenderHTML(translationBody); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	render(w, u.Locale, "view.html", map[string]any{
		"User": u, "NavFlags": nv, "Text": t, "RenderedBody": rendered,
		"CanEdit": canEdit, "Script": scriptMeta, "Tags": textTags,
		"RenderedTranscription": renderedTranscription,
		"HasTranslation":        hasTranslation,
		"RenderedTranslation":   renderedTranslation,
	})
}

func (s *Server) handleEditForm(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	t, err := s.texts.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, t.Language)
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
	textTags, err := s.tags.For(r.Context(), resourceTypeText, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	allTags, err := s.tags.AllNames(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Prefills the editing user's OWN locale's translation slot — a
	// different editor with a different profile language would see/edit a
	// different slot for the same text (see internal/translations).
	translationBody, _, err := s.translations.Get(r.Context(), resourceTypeText, id, u.Locale)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	render(w, u.Locale, "edit.html", map[string]any{
		"User": u, "NavFlags": nv, "Text": t, "Languages": langs, "Scripts": scripts,
		"Tags": strings.Join(textTags, ", "), "AllTags": allTags, "Translation": translationBody,
	})
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	t, err := s.texts.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, t.Language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canEdit {
		http.NotFound(w, r)
		return
	}
	newLanguage := r.FormValue("language")
	if newLanguage != t.Language {
		// Moving a text to a different language requires edit rights there too.
		canEditNew, err := s.roles.CanEdit(r.Context(), u.ID, newLanguage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !canEditNew {
			http.Error(w, i18n.T(u.Locale, "texts.err_no_move_language"), http.StatusForbidden)
			return
		}
	}
	if err := s.texts.Update(r.Context(), id, r.FormValue("title"), r.FormValue("body"), r.FormValue("transcription"), newLanguage, r.FormValue("script")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.tags.SetFor(r.Context(), resourceTypeText, id, tags.Parse(r.FormValue("tags"))); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.translations.Set(r.Context(), resourceTypeText, id, u.Locale, r.FormValue("translation")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/texts/"+strconv.FormatInt(id, 10), http.StatusFound)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	t, err := s.texts.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, t.Language)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canEdit {
		http.NotFound(w, r)
		return
	}
	if err := s.texts.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.tags.DeleteFor(r.Context(), resourceTypeText, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleProfileForm(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	nv, err := s.loadNav(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	grants, err := s.roles.GrantsForUser(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	render(w, u.Locale, "profile.html", map[string]any{
		"User": u, "NavFlags": nv, "Grants": grants, "Locales": i18n.Locales,
	})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	nv, _ := s.loadNav(r.Context(), u.ID)
	grants, _ := s.roles.GrantsForUser(r.Context(), u.ID)
	current := r.FormValue("current_password")
	next := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	base := map[string]any{"User": u, "NavFlags": nv, "Grants": grants, "Locales": i18n.Locales}
	withError := func(key string) map[string]any {
		base["Message"] = i18n.T(u.Locale, key)
		base["MessageType"] = "error"
		return base
	}

	if next != confirm {
		render(w, u.Locale, "profile.html", withError("profile.err_password_mismatch"))
		return
	}
	if len(next) < 8 {
		render(w, u.Locale, "profile.html", withError("profile.err_password_length"))
		return
	}
	if err := s.auth.ChangePassword(r.Context(), u.ID, current, next); err != nil {
		key := "profile.err_generic"
		if err == auth.ErrInvalidCredentials {
			key = "profile.err_current_password"
		}
		render(w, u.Locale, "profile.html", withError(key))
		return
	}
	base["Message"] = i18n.T(u.Locale, "profile.password_changed")
	base["MessageType"] = "success"
	render(w, u.Locale, "profile.html", base)
}

func (s *Server) handleSetLocale(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	newLocale := r.FormValue("locale")
	if !i18n.IsValid(newLocale) {
		http.Error(w, "invalid locale", http.StatusBadRequest)
		return
	}
	if err := s.auth.SetLocale(r.Context(), u.ID, newLocale); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	u.Locale = newLocale // avoid a re-query just to reflect what we just wrote

	nv, err := s.loadNav(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	grants, err := s.roles.GrantsForUser(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	render(w, u.Locale, "profile.html", map[string]any{
		"User": u, "NavFlags": nv, "Grants": grants, "Locales": i18n.Locales,
		"Message": i18n.T(u.Locale, "profile.language_saved"), "MessageType": "success",
	})
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	nv, err := s.loadNav(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	users, err := s.auth.ListUsers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	grants, err := s.roles.Grants(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	langs, err := catalog.ListLanguages(r.Context(), s.db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	scripts, err := catalog.ListScripts(r.Context(), s.db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	imePresets, err := ime.ListPresets(r.Context(), s.db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	imeConfigs, err := ime.ListConfigs(r.Context(), s.db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	render(w, u.Locale, "admin.html", map[string]any{
		"User": u, "Nav": "admin", "NavFlags": nv,
		"Users": users, "Grants": grants, "Languages": langs, "Scripts": scripts,
		"ImePresets": imePresets, "ImeConfigs": imeConfigs,
	})
}

func (s *Server) handleAdminSetIME(w http.ResponseWriter, r *http.Request) {
	cfg := ime.Config{
		Language:           r.FormValue("language"),
		Script:             r.FormValue("script"),
		SourceIME:          r.FormValue("source_ime"),
		TranscriptionIME:   r.FormValue("transcription_ime"),
		NeedsTranscription: r.FormValue("needs_transcription") != "", // unchecked checkboxes send no field at all
	}
	if cfg.Language == "" || cfg.Script == "" {
		http.Error(w, "language and script are required", http.StatusBadRequest)
		return
	}
	if err := ime.SetConfig(r.Context(), s.db, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusFound)
}

func (s *Server) handleAdminDeleteIME(w http.ResponseWriter, r *http.Request) {
	language := r.FormValue("language")
	script := r.FormValue("script")
	if err := ime.DeleteConfig(r.Context(), s.db, language, script); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusFound)
}

func (s *Server) handleAdminGrant(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid user_id", http.StatusBadRequest)
		return
	}
	role := r.FormValue("role")
	language := r.FormValue("language")
	if role == roles.Admin {
		language = ""
	} else if language == "" {
		http.Error(w, "teacher/student grants require a language", http.StatusBadRequest)
		return
	}
	if err := s.roles.Grant(r.Context(), userID, role, language); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusFound)
}

func (s *Server) handleAdminRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.roles.Revoke(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusFound)
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
