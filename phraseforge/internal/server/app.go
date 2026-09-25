package server

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"

	"phraseforge/internal/auth"
	"phraseforge/internal/catalog"
	"phraseforge/internal/i18n"
	"phraseforge/internal/ime"
)

// chromeI18nKeys lists every i18n key layout.html's sidebar/header used to
// render server-side — gathered before that markup moved into shell.js.
var chromeI18nKeys = []string{
	"nav.texts", "nav.dialogs", "nav.vocabulary", "nav.models", "nav.admin", "nav.jobs", "nav.logout",
	"nav.all_languages", "sidebar.toggle", "theme.toggle",
}

func chromeI18n(loc string) map[string]string {
	out := make(map[string]string, len(chromeI18nKeys))
	for _, k := range chromeI18nKeys {
		out[k] = i18n.T(loc, k)
	}
	return out
}

// buildAppBootstrap gathers every section's own bootstrap data, keyed by
// section name, for the unified shell — see specs/features/
// phraseforge-spa-unified-shell.md. Shared by handleApp (embeds it in the
// initial page) and apiGetAppBootstrap (re-fetched after a locale change,
// with no other state changing in between). texts/dialogs/vocabulary/models
// share one formOptions/tags.AllNames call each, instead of one per
// section as before unification — those two calls were always
// resource-agnostic, so the four sections calling them separately was pure
// duplication that unification incidentally removes.
func buildAppBootstrap(ctx context.Context, u auth.User, nv nav, s *Server) (map[string]any, error) {
	langs, scripts, err := s.formOptions(ctx, nv)
	if err != nil {
		return nil, err
	}
	allTags, err := s.tags.AllNames(ctx)
	if err != nil {
		return nil, err
	}

	navFlags := map[string]any{
		"isAdmin":           nv.IsAdmin,
		"canCreateAny":      nv.CanCreateAny,
		"viewableLanguages": nv.ViewableLanguages,
	}
	resourceBootstrap := func(i18nMap map[string]string) map[string]any {
		return map[string]any{
			"navFlags": navFlags, "languages": langs, "scripts": scripts, "allTags": allTags,
			"locale": u.Locale, "i18n": i18nMap,
		}
	}

	bootstrap := map[string]any{
		"chrome": map[string]any{
			"username": u.Username, "navFlags": navFlags, "i18n": chromeI18n(u.Locale),
		},
		"texts":      resourceBootstrap(textsAppI18n(u.Locale)),
		"dialogs":    resourceBootstrap(dialogsAppI18n(u.Locale)),
		"vocabulary": resourceBootstrap(vocabularyAppI18n(u.Locale)),
		"models":     resourceBootstrap(modelsAppI18n(u.Locale)),
		"profile": map[string]any{
			"navFlags": navFlags, "username": u.Username, "locales": i18n.Locales,
			"locale": u.Locale, "i18n": profileAppI18n(u.Locale),
		},
	}

	if nv.IsAdmin {
		adminLangs, err := catalog.ListLanguages(ctx, s.db)
		if err != nil {
			return nil, err
		}
		adminScripts, err := catalog.ListScripts(ctx, s.db)
		if err != nil {
			return nil, err
		}
		imePresets, err := ime.ListPresets(ctx, s.db)
		if err != nil {
			return nil, err
		}
		bootstrap["admin"] = map[string]any{
			"navFlags": navFlags, "languages": adminLangs, "scripts": adminScripts,
			"imePresets": imePresets, "siteLanguages": i18n.Locales,
			"locale": u.Locale, "i18n": adminAppI18n(u.Locale),
		}
		bootstrap["jobs"] = map[string]any{
			"navFlags": navFlags, "locale": u.Locale, "i18n": jobsAppI18n(u.Locale),
		}
	}

	return bootstrap, nil
}

// handleApp serves the unified SPA shell — see specs/features/
// phraseforge-spa-unified-shell.md. Replaces the six former
// handle{Texts,Dialogs,Vocabulary,Models,Admin,Profile}App handlers.
func (s *Server) handleApp(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	nv, err := s.loadNav(r.Context(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	bootstrap, err := buildAppBootstrap(r.Context(), u, nv, s)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b, err := json.Marshal(bootstrap)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	render(w, u.Locale, "app.html", map[string]any{
		"User": u, "NavFlags": nv, "BootstrapJSON": template.JS(b),
	})
}

// apiGetAppBootstrap is buildAppBootstrap re-run and returned as plain JSON
// — used after a locale change to refresh the whole shell (chrome and every
// section's own bootstrap/i18n) without a page reload.
func (s *Server) apiGetAppBootstrap(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	nv, err := s.loadNav(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	bootstrap, err := buildAppBootstrap(r.Context(), u, nv, s)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, bootstrap)
}
