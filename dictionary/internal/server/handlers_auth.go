package server

import (
	"errors"
	"net/http"
	"time"

	"dictionary/internal/auth"
)

type tokenRequest struct {
	Username string  `json:"username"`
	Password string  `json:"password"`
	Scope    string  `json:"scope"`    // "write" or "read"
	Language *string `json:"language"` // required unless the account is a global admin
}

type tokenResponse struct {
	Token     string    `json:"token"`
	Role      string    `json:"role"`
	Language  *string   `json:"language,omitempty"`
	Scope     string    `json:"scope"`
	ExpiresAt time.Time `json:"expires_at"`
}

// handleMintToken implements POST /auth/token.
func (s *Server) handleMintToken(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "username and password are required")
		return
	}

	raw, claims, err := s.auth.MintToken(r.Context(), req.Username, req.Password, auth.Scope(req.Scope), req.Language)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, codeInvalidCredentials, err.Error())
		return
	case errors.Is(err, auth.ErrInsufficientRole):
		writeError(w, http.StatusForbidden, codeInsufficientRole, err.Error())
		return
	case errors.Is(err, auth.ErrInvalidScope):
		writeError(w, http.StatusBadRequest, codeValidationFailed, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, tokenResponse{
		Token: raw, Role: string(claims.Role), Language: claims.LanguageCode,
		Scope: string(claims.Scope), ExpiresAt: claims.ExpiresAt,
	})
}
