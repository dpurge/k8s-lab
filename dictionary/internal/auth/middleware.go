package auth

import (
	"context"
	"net/http"
	"strings"
)

type ctxKey int

const claimsCtxKey ctxKey = 0

// BearerToken is exported so server's read-access gate can reuse it, treating
// a missing token as "anonymous" rather than an error.
func BearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	return strings.TrimPrefix(h, prefix), true
}

// ok is false only when no token was presented — not an error, since a
// public resource treats that as "anonymous".
func (s *Service) ClaimsFromRequest(r *http.Request) (claims Claims, ok bool, err error) {
	raw, present := BearerToken(r)
	if !present {
		return Claims{}, false, nil
	}
	claims, err = s.ValidateToken(r.Context(), raw)
	if err != nil {
		return Claims{}, false, err
	}
	return claims, true, nil
}

func (s *Service) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := BearerToken(r)
		if !ok {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		claims, err := s.ValidateToken(r.Context(), raw)
		if err != nil {
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}
		if claims.Role != "admin" {
			http.Error(w, "admin role required", http.StatusForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), claimsCtxKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func ClaimsFrom(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(claimsCtxKey).(Claims)
	return c, ok
}
