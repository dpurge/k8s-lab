package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestGenerateTranslationInvalidLocaleReturns400 covers background-generate-
// title-transcription-translation's Acceptance Criteria: an unvalidated
// locale would create untraceable rows and waste worker time, so it's
// rejected at the boundary with 400 before touching the database — each of
// these four handlers validates the locale before calling its
// apiGenerateFieldFrom*/apiGenerate*ItemField body (which is the only place
// that touches s.texts/s.vocab/s.models/s.roles/s.jobs), so a zero-value
// *Server exercises the invalid-locale branch safely, with no DB needed.
func TestGenerateTranslationInvalidLocaleReturns400(t *testing.T) {
	s := &Server{}
	cases := []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request)
		urlVars map[string]string
	}{
		{"text", s.apiGenerateTranslationFromText, map[string]string{"id": "1"}},
		{"dialog", s.apiGenerateTranslationFromDialog, map[string]string{"id": "1"}},
		{"vocabulary item", s.apiGenerateVocabItemTranslation, map[string]string{"id": "1", "position": "0"}},
		{"models item", s.apiGenerateModelsItemTranslation, map[string]string{"id": "1", "position": "0"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader(`{"locale":"not-a-real-locale"}`))
			rctx := chi.NewRouteContext()
			for k, v := range tt.urlVars {
				rctx.URLParams.Add(k, v)
			}
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
			w := httptest.NewRecorder()

			tt.handler(w, r)

			if w.Code != 400 {
				t.Errorf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
			}
		})
	}
}
