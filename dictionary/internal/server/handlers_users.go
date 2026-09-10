package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"dictionary/internal/auth"
	"dictionary/internal/roles"
)

type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type userResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type userListResponse struct {
	Users []userResponse `json:"users"`
}

// handleCreateUser implements POST /users (admin only).
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Username == "" || len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "username is required and password must be at least 8 characters")
		return
	}
	u, err := s.auth.CreateUser(r.Context(), req.Username, req.Password)
	switch {
	case errors.Is(err, auth.ErrUsernameTaken):
		writeError(w, http.StatusConflict, codeConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, userResponse{ID: u.ID, Username: u.Username})
}

// handleListUsers implements GET /users (admin only).
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.auth.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	out := make([]userResponse, len(users))
	for i, u := range users {
		out[i] = userResponse{ID: u.ID, Username: u.Username}
	}
	writeJSON(w, http.StatusOK, userListResponse{Users: out})
}

type grantRequest struct {
	Role     string  `json:"role"`
	Language *string `json:"language"`
}

type grantResponse struct {
	ID       int64   `json:"id"`
	UserID   int64   `json:"user_id"`
	Role     string  `json:"role"`
	Language *string `json:"language,omitempty"`
}

// handleGrantRole implements POST /users/{id}/roles (admin only).
func (s *Server) handleGrantRole(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "invalid user id")
		return
	}
	var req grantRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	role := roles.Role(req.Role)
	g, err := s.roles.Grant(r.Context(), userID, role, req.Language)
	switch {
	case errors.Is(err, roles.ErrInvalidGrant):
		writeError(w, http.StatusBadRequest, codeValidationFailed, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, grantResponse{ID: g.ID, UserID: g.UserID, Role: string(g.Role), Language: g.LanguageCode})
}

// handleListGrants implements GET /users/{id}/roles (admin only).
func (s *Server) handleListGrants(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "invalid user id")
		return
	}
	grants, err := s.roles.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	out := make([]grantResponse, len(grants))
	for i, g := range grants {
		out[i] = grantResponse{ID: g.ID, UserID: g.UserID, Role: string(g.Role), Language: g.LanguageCode}
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": out})
}

// handleRevokeGrant implements DELETE /users/{id}/roles/{grantID} (admin only).
func (s *Server) handleRevokeGrant(w http.ResponseWriter, r *http.Request) {
	grantID, err := strconv.ParseInt(chi.URLParam(r, "grantID"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "invalid grant id")
		return
	}
	if err := s.roles.Revoke(r.Context(), grantID); err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
