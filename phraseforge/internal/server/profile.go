package server

import (
	"encoding/json"
	"net/http"

	"phraseforge/internal/auth"
	"phraseforge/internal/i18n"
)

// profileAppI18nKeys mirrors adminAppI18nKeys for the Profile SPA shell.
var profileAppI18nKeys = []string{
	"profile.title", "profile.signed_in_as",
	"profile.roles_heading", "profile.role_admin", "role.admin", "role.teacher", "role.student",
	"profile.role_none",
	"profile.language_heading", "profile.language_save",
	"profile.password_heading", "profile.current_password", "profile.new_password",
	"profile.confirm_password", "profile.change_password",
	"profile.err_password_mismatch", "profile.err_password_length", "profile.err_current_password",
	"profile.err_generic", "profile.password_changed",
}

func profileAppI18n(loc string) map[string]string {
	out := make(map[string]string, len(profileAppI18nKeys))
	for _, k := range profileAppI18nKeys {
		out[k] = i18n.T(loc, k)
	}
	return out
}

// --- JSON API (phraseforge-spa-profile) ---

type apiProfileGrant struct {
	Role         string `json:"role"`
	Language     string `json:"language"`
	LanguageName string `json:"languageName"`
}

// apiGetProfile is handleProfileForm's exact GrantsForUser read, JSON-encoded.
func (s *Server) apiGetProfile(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	grants, err := s.roles.GrantsForUser(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	apiGrants := make([]apiProfileGrant, len(grants))
	for i, g := range grants {
		apiGrants[i] = apiProfileGrant{Role: g.Role, Language: g.Language, LanguageName: g.LanguageName}
	}
	writeJSON(w, http.StatusOK, map[string]any{"locale": u.Locale, "grants": apiGrants})
}

type apiProfileLocaleRequest struct {
	Locale string `json:"locale"`
}

// apiSetProfileLocale is handleSetLocale's exact logic. Unlike every other
// mutation in this app, the client does a real page reload after this
// succeeds — layout.html's sidebar/header chrome is server-rendered per
// request and has no bootstrap of its own to patch in-page, so only a real
// navigation picks up the new locale there. See the spec's rationale.
func (s *Server) apiSetProfileLocale(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var req apiProfileLocaleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if !i18n.IsValid(req.Locale) {
		writeErr(w, http.StatusBadRequest, "validation_error", "invalid locale")
		return
	}
	if err := s.auth.SetLocale(r.Context(), u.ID, req.Locale); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"locale": req.Locale})
}

type apiProfilePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
	ConfirmPassword string `json:"confirmPassword"`
}

// apiChangeProfilePassword is handleChangePassword's exact validation order
// (mismatch, then length, then the store's own current-password check).
func (s *Server) apiChangeProfilePassword(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var req apiProfilePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.NewPassword != req.ConfirmPassword {
		writeErr(w, http.StatusBadRequest, "password_mismatch", i18n.T(u.Locale, "profile.err_password_mismatch"))
		return
	}
	if len(req.NewPassword) < 8 {
		writeErr(w, http.StatusBadRequest, "password_too_short", i18n.T(u.Locale, "profile.err_password_length"))
		return
	}
	if err := s.auth.ChangePassword(r.Context(), u.ID, req.CurrentPassword, req.NewPassword); err != nil {
		if err == auth.ErrInvalidCredentials {
			writeErr(w, http.StatusBadRequest, "invalid_current_password", i18n.T(u.Locale, "profile.err_current_password"))
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", i18n.T(u.Locale, "profile.err_generic"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
