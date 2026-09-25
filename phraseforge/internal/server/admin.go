package server

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"phraseforge/internal/ai"
	"phraseforge/internal/i18n"
	"phraseforge/internal/ime"
	"phraseforge/internal/roles"
)

// adminAppI18nKeys mirrors modelsAppI18nKeys/vocabularyAppI18nKeys for the
// Admin SPA shell.
var adminAppI18nKeys = []string{
	"admin.title",
	"admin.config_heading", "admin.config_import_hint", "admin.config_export",
	"admin.config_import_file", "admin.config_import", "admin.config_import_confirm",
	"admin.grant_heading", "admin.user", "admin.role", "admin.language_scoped", "admin.grant",
	"profile.role_admin", "role.admin", "role.teacher", "role.student",
	"admin.grants_heading", "texts.field_language", "admin.site_wide", "admin.revoke_confirm",
	"admin.no_grants",
	"admin.ime_heading", "texts.field_script", "admin.ime_source", "admin.ime_transcription",
	"admin.ime_none", "admin.ime_needs_transcription", "admin.ime_save",
	"admin.ime_configs_heading", "admin.ime_remove_confirm", "admin.ime_no_configs",
	"admin.llm_heading", "admin.llm_kind", "admin.llm_kind_translation", "admin.llm_kind_transcription",
	"admin.llm_kind_title", "admin.llm_kind_process_text", "admin.llm_kind_process_dialog",
	"admin.llm_source_language", "admin.llm_target_language", "admin.llm_target_not_applicable",
	"admin.llm_provider", "admin.llm_model", "admin.llm_model_placeholder", "admin.llm_model_default",
	"admin.llm_think", "admin.llm_timeout_seconds", "admin.llm_timeout_default", "admin.llm_timeout_placeholder",
	"admin.llm_prompt", "admin.llm_prompt_placeholder", "admin.llm_save",
	"admin.llm_configs_heading", "admin.llm_delete_confirm", "admin.llm_no_configs",
	"admin.tab_grants", "admin.tab_ime", "admin.tab_llm", "admin.tab_config",
	"texts.edit", "texts.delete", "vocabulary.cancel_edit",
}

func adminAppI18n(loc string) map[string]string {
	out := make(map[string]string, len(adminAppI18nKeys))
	for _, k := range adminAppI18nKeys {
		out[k] = i18n.T(loc, k)
	}
	return out
}

// --- JSON API (phraseforge-spa-admin) ---

type apiAdminUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type apiAdminGrant struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"userId"`
	Username     string `json:"username"`
	Role         string `json:"role"`
	Language     string `json:"language"`
	LanguageName string `json:"languageName"`
}

// apiAdminBootstrap is the one aggregate response every mutation below
// causes the client to re-fetch — see the spec's rationale for why this
// page re-fetches everything rather than tracking which tab changed.
type apiAdminBootstrap struct {
	Users      []apiAdminUser  `json:"users"`
	Grants     []apiAdminGrant `json:"grants"`
	IMEConfigs []ime.Config    `json:"imeConfigs"`
	LLMPrompts []ai.Prompt     `json:"llmPrompts"`
	// Providers lists the configured LLM provider registry's keys (e.g.
	// "ollama", "openrouter"), sorted — the admin UI's LLM tab uses this to
	// populate the per-prompt provider dropdown.
	Providers []string `json:"providers"`
	// LLMKinds lists every kind the LLM-prompt editor may target, straight
	// from ai.ValidKinds — the single authoritative source shared with this
	// file's own validators below, so the UI's kind <select> can never drift
	// out of sync with what the API (and the schema's CHECK constraint)
	// actually accept.
	LLMKinds []string `json:"llmKinds"`
}

// apiGetAdminBootstrap is handleAdmin's four DB reads (users, grants, IME
// configs, LLM prompts), JSON-encoded.
func (s *Server) apiGetAdminBootstrap(w http.ResponseWriter, r *http.Request) {
	users, err := s.auth.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	grants, err := s.roles.Grants(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	imeConfigs, err := ime.ListConfigs(r.Context(), s.db)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	llmPrompts, err := s.ai.ListPrompts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	apiUsers := make([]apiAdminUser, len(users))
	for i, u := range users {
		apiUsers[i] = apiAdminUser{ID: u.ID, Username: u.Username}
	}
	apiGrants := make([]apiAdminGrant, len(grants))
	for i, g := range grants {
		apiGrants[i] = apiAdminGrant{
			ID: g.ID, UserID: g.UserID, Username: g.Username, Role: g.Role,
			Language: g.Language, LanguageName: g.LanguageName,
		}
	}
	writeJSON(w, http.StatusOK, apiAdminBootstrap{
		Users: apiUsers, Grants: apiGrants, IMEConfigs: imeConfigs, LLMPrompts: llmPrompts,
		Providers: s.ai.Providers(),
		LLMKinds:  ai.ValidKinds,
	})
}

type apiAdminGrantRequest struct {
	UserID   int64  `json:"userId"`
	Role     string `json:"role"`
	Language string `json:"language"`
}

// apiCreateAdminGrant is handleAdminGrant's exact logic.
func (s *Server) apiCreateAdminGrant(w http.ResponseWriter, r *http.Request) {
	var req apiAdminGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	language := req.Language
	if req.Role == roles.Admin {
		language = ""
	} else if language == "" {
		writeErr(w, http.StatusBadRequest, "validation_error", "teacher/student grants require a language")
		return
	}
	if err := s.roles.Grant(r.Context(), req.UserID, req.Role, language); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

// apiRevokeAdminGrant is handleAdminRevoke's exact logic.
func (s *Server) apiRevokeAdminGrant(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "grant not found")
		return
	}
	if err := s.roles.Revoke(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type apiAdminIMERequest struct {
	Language           string `json:"language"`
	Script             string `json:"script"`
	SourceIME          string `json:"sourceIme"`
	TranscriptionIME   string `json:"transcriptionIme"`
	NeedsTranscription bool   `json:"needsTranscription"`
}

// apiSetAdminIME is handleAdminSetIME's exact logic.
func (s *Server) apiSetAdminIME(w http.ResponseWriter, r *http.Request) {
	var req apiAdminIMERequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	cfg := ime.Config{
		Language: req.Language, Script: req.Script,
		SourceIME: req.SourceIME, TranscriptionIME: req.TranscriptionIME,
		NeedsTranscription: req.NeedsTranscription,
	}
	if cfg.Language == "" || cfg.Script == "" {
		writeErr(w, http.StatusBadRequest, "validation_error", "language and script are required")
		return
	}
	if err := ime.SetConfig(r.Context(), s.db, cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// apiDeleteAdminIME is handleAdminDeleteIME's exact logic. IME configs have
// no surrogate key — (language, script) is the composite key, so it's a
// two-segment path param instead of the {id}/{position} shape used
// elsewhere.
func (s *Server) apiDeleteAdminIME(w http.ResponseWriter, r *http.Request) {
	language := chi.URLParam(r, "language")
	script := chi.URLParam(r, "script")
	if err := ime.DeleteConfig(r.Context(), s.db, language, script); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type apiAdminLLMPromptRequest struct {
	Kind           string `json:"kind"`
	SourceLanguage string `json:"sourceLanguage"`
	TargetLanguage string `json:"targetLanguage"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	Think          bool   `json:"think"`
	Prompt         string `json:"prompt"`
	// TimeoutSeconds nil means "inherit the purpose default" — see
	// ai.Prompt.TimeoutSeconds's own doc comment.
	TimeoutSeconds *int `json:"timeoutSeconds,omitempty"`
}

// validTimeoutSeconds mirrors the llm_prompts_timeout_seconds_check DB
// constraint exactly (schema.sql) — validated here too so a bad value is
// rejected at the API boundary with a clear message, not an opaque DB
// error, and so apiImportAdminConfig (which never reaches SetPrompt for an
// invalid row without this) can't silently accept one either.
func validTimeoutSeconds(t *int) bool {
	return t == nil || (*t > 0 && *t <= 3600)
}

// apiSetAdminLLMPrompt is handleAdminLLMPrompt's exact logic.
func (s *Server) apiSetAdminLLMPrompt(w http.ResponseWriter, r *http.Request) {
	var req apiAdminLLMPromptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	p := ai.Prompt{
		Kind: req.Kind, SourceLanguage: req.SourceLanguage, TargetLanguage: req.TargetLanguage,
		Provider: req.Provider, Model: req.Model, Think: req.Think, Prompt: req.Prompt,
		TimeoutSeconds: req.TimeoutSeconds,
	}
	if p.Kind == "" || p.SourceLanguage == "" || p.TargetLanguage == "" || strings.TrimSpace(p.Prompt) == "" {
		writeErr(w, http.StatusBadRequest, "validation_error", "kind, source language, target language, and prompt are required")
		return
	}
	if !ai.IsValidKind(p.Kind) {
		writeErr(w, http.StatusBadRequest, "validation_error", "kind must be one of "+strings.Join(ai.ValidKinds, ", "))
		return
	}
	if !slices.Contains(s.ai.Providers(), p.Provider) {
		writeErr(w, http.StatusBadRequest, "validation_error", "provider must be one of the configured providers")
		return
	}
	if !validTimeoutSeconds(p.TimeoutSeconds) {
		writeErr(w, http.StatusBadRequest, "validation_error", "timeout seconds must be between 1 and 3600")
		return
	}
	if err := s.ai.SetPrompt(r.Context(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// apiDeleteAdminLLMPrompt is handleAdminDeleteLLMPrompt's exact logic. LLM
// prompts have no surrogate key — (kind, sourceLanguage, targetLanguage) is
// the composite key.
func (s *Server) apiDeleteAdminLLMPrompt(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	sourceLanguage := chi.URLParam(r, "sourceLanguage")
	targetLanguage := chi.URLParam(r, "targetLanguage")
	if err := s.ai.DeletePrompt(r.Context(), kind, sourceLanguage, targetLanguage); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type adminConfigExport struct {
	Version    int          `json:"version"`
	IMEConfigs []ime.Config `json:"ime_configs"`
	LLMPrompts []ai.Prompt  `json:"llm_prompts"`
}

// handleAdminExportConfig is unchanged from before this feature — it stays
// a real link/file download (Content-Disposition: attachment), not
// converted to fetch, since a fetch response can't trigger a browser
// save-file prompt.
func (s *Server) handleAdminExportConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	imeConfigs, err := ime.ListConfigs(ctx, s.db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	llmPrompts, err := s.ai.ListPrompts(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="phraseforge-admin-config.json"`)
	if err := json.NewEncoder(w).Encode(adminConfigExport{Version: 1, IMEConfigs: imeConfigs, LLMPrompts: llmPrompts}); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

// apiImportAdminConfig is handleAdminImportConfig's exact validation and
// same-transaction replace-all logic, accepting a multipart/form-data file
// upload (the client sends FormData, matching the file <input> in the UI)
// and returning JSON instead of redirecting. The original handler's
// config_json form-field fallback (no file present) is dropped — the SPA
// form only ever offers a file input, so that fallback path was never
// reachable from the UI.
func (s *Server) apiImportAdminConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_form", err.Error())
		return
	}
	file, _, err := r.FormFile("config_file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_form", "config_file is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_form", err.Error())
		return
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		writeErr(w, http.StatusBadRequest, "validation_error", "configuration JSON is required")
		return
	}
	var cfg adminConfigExport
	if err := json.Unmarshal(data, &cfg); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", "invalid configuration JSON: "+err.Error())
		return
	}
	if cfg.Version != 1 {
		writeErr(w, http.StatusBadRequest, "validation_error", "unsupported configuration version")
		return
	}
	for _, c := range cfg.IMEConfigs {
		if c.Language == "" || c.Script == "" {
			writeErr(w, http.StatusBadRequest, "validation_error", "each IME config requires language and script")
			return
		}
	}
	for _, p := range cfg.LLMPrompts {
		if !ai.IsValidKind(p.Kind) {
			writeErr(w, http.StatusBadRequest, "validation_error", "each LLM prompt kind must be one of "+strings.Join(ai.ValidKinds, ", "))
			return
		}
		if p.SourceLanguage == "" || p.TargetLanguage == "" || strings.TrimSpace(p.Prompt) == "" {
			writeErr(w, http.StatusBadRequest, "validation_error", "each LLM prompt requires source_language, target_language, and prompt")
			return
		}
		if !slices.Contains(s.ai.Providers(), p.Provider) {
			writeErr(w, http.StatusBadRequest, "validation_error", "each LLM prompt's provider must be one of the configured providers")
			return
		}
		if !validTimeoutSeconds(p.TimeoutSeconds) {
			writeErr(w, http.StatusBadRequest, "validation_error", "each LLM prompt's timeout seconds must be between 1 and 3600")
			return
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds
	if _, err := tx.Exec(ctx, `DELETE FROM ime_config`); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM llm_prompts`); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	toNull := func(s string) any {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return s
	}
	for _, c := range cfg.IMEConfigs {
		if _, err := tx.Exec(ctx, `INSERT INTO ime_config(language, script, source_ime, transcription_ime, needs_transcription) VALUES($1,$2,$3,$4,$5)`, c.Language, c.Script, toNull(c.SourceIME), toNull(c.TranscriptionIME), c.NeedsTranscription); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_config", err.Error())
			return
		}
	}
	for _, p := range cfg.LLMPrompts {
		if _, err := tx.Exec(ctx, `INSERT INTO llm_prompts(kind, source_language, target_language, provider, model, think, prompt, timeout_seconds) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, p.Kind, p.SourceLanguage, p.TargetLanguage, p.Provider, strings.TrimSpace(p.Model), p.Think, p.Prompt, p.TimeoutSeconds); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_config", err.Error())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
