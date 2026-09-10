package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"dictionary/internal/auth"
	"dictionary/internal/languages"
)

// ── Languages ──────────────────────────────────────────────────────────────

type languageRequest struct {
	Code             string `json:"code"`
	Name             string `json:"name"`
	IsPublic         bool   `json:"is_public"`
	HasTranscription bool   `json:"has_transcription"`
}

// handleCreateLanguage implements POST /languages (admin only).
func (s *Server) handleCreateLanguage(w http.ResponseWriter, r *http.Request) {
	var req languageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "name is required")
		return
	}
	l, err := s.languages.Create(r.Context(), languages.Language{
		Code: req.Code, Name: req.Name, IsPublic: req.IsPublic, HasTranscription: req.HasTranscription,
	})
	switch {
	case errors.Is(err, languages.ErrInvalidCode):
		writeError(w, http.StatusBadRequest, codeValidationFailed, err.Error())
		return
	case errors.Is(err, languages.ErrAlreadyExist):
		writeError(w, http.StatusConflict, codeConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

// handleUpdateLanguage implements PUT /languages/{code} (admin only).
func (s *Server) handleUpdateLanguage(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	var req languageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "name is required")
		return
	}
	l, err := s.languages.Update(r.Context(), languages.Language{
		Code: code, Name: req.Name, IsPublic: req.IsPublic, HasTranscription: req.HasTranscription,
	})
	switch {
	case errors.Is(err, languages.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// handleListLanguages implements GET /languages (public — the catalog of
// which languages exist isn't sensitive, only their content is access-gated).
func (s *Server) handleListLanguages(w http.ResponseWriter, r *http.Request) {
	list, err := s.languages.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"languages": list})
}

// handleGetLanguage implements GET /languages/{code}, behind requireReadAccess.
func (s *Server) handleGetLanguage(w http.ResponseWriter, r *http.Request) {
	l, _ := languageFromContext(r.Context())
	writeJSON(w, http.StatusOK, l)
}

// ── Grammar schema ───────────────────────────────────────────────────────────

// handleGetSchema implements GET /languages/{code}/schema, behind requireReadAccess.
func (s *Server) handleGetSchema(w http.ResponseWriter, r *http.Request) {
	l, _ := languageFromContext(r.Context())
	sch, err := s.languages.GetSchema(r.Context(), l.Code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sch)
}

// handlePutSchema implements PUT /languages/{code}/schema (admin only).
func (s *Server) handlePutSchema(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	if _, err := s.languages.Get(r.Context(), code); errors.Is(err, languages.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	var sch languages.Schema
	if !decodeJSON(w, r, &sch) {
		return
	}
	if err := s.languages.ReplaceSchema(r.Context(), code, sch); err != nil {
		if errors.Is(err, languages.ErrSchemaInvalid) {
			writeError(w, http.StatusBadRequest, codeValidationFailed, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sch)
}

// ── Read-access gate ─────────────────────────────────────────────────────────

type ctxKey int

const languageCtxKey ctxKey = 0

func languageFromContext(ctx context.Context) (languages.Language, bool) {
	l, ok := ctx.Value(languageCtxKey).(languages.Language)
	return l, ok
}

// requireReadAccess lets the request through if the {code} language is
// public, or if the caller presents a valid reader/writer/admin token for it.
func (s *Server) requireReadAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := chi.URLParam(r, "code")
		l, err := s.languages.Get(r.Context(), code)
		if errors.Is(err, languages.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
			return
		}

		ctx := context.WithValue(r.Context(), languageCtxKey, l)

		if l.IsPublic {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		claims, ok, err := s.auth.ClaimsFromRequest(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "invalid or expired token")
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "this language is not public; a reader, writer, or admin token is required")
			return
		}
		if claims.Role != "admin" && (claims.LanguageCode == nil || *claims.LanguageCode != l.Code) {
			writeError(w, http.StatusForbidden, codeForbidden, "token does not grant access to this language")
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, claimsCtxKeyForServer{}, claims)))
	})
}

// requireWriteAccess requires a writer or admin token scoped to {code} —
// unlike requireReadAccess, the language's public flag never applies here.
func (s *Server) requireWriteAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := chi.URLParam(r, "code")
		l, err := s.languages.Get(r.Context(), code)
		if errors.Is(err, languages.ErrNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
			return
		}

		claims, ok, err := s.auth.ClaimsFromRequest(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "invalid or expired token")
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "a writer or admin token is required")
			return
		}
		if claims.Role != "admin" && !(claims.Role == "writer" && claims.LanguageCode != nil && *claims.LanguageCode == l.Code) {
			writeError(w, http.StatusForbidden, codeForbidden, "token does not grant write access to this language")
			return
		}

		ctx := context.WithValue(r.Context(), languageCtxKey, l)
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, claimsCtxKeyForServer{}, claims)))
	})
}

// claimsCtxKeyForServer is a distinct key from auth's own internal one —
// server package handlers that need claims read them back with
// claimsFromContext, kept local since only requireReadAccess/requireWriteAccess
// (not auth.RequireAdmin) populate it this way.
type claimsCtxKeyForServer struct{}

func claimsFromContext(ctx context.Context) (auth.Claims, bool) {
	c, ok := ctx.Value(claimsCtxKeyForServer{}).(auth.Claims)
	return c, ok
}
