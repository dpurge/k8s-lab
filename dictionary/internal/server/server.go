// Package server holds dictionary's HTTP router.
package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"dictionary/internal/auth"
	"dictionary/internal/entries"
	"dictionary/internal/inflection"
	"dictionary/internal/languages"
	"dictionary/internal/roles"
)

type Server struct {
	db         *pgxpool.Pool
	auth       *auth.Service
	roles      *roles.Service
	languages  *languages.Service
	entries    *entries.Service
	inflection *inflection.Service
}

func New(db *pgxpool.Pool, authSvc *auth.Service, rolesSvc *roles.Service, languagesSvc *languages.Service, entriesSvc *entries.Service, inflectionSvc *inflection.Service) *Server {
	return &Server{db: db, auth: authSvc, roles: rolesSvc, languages: languagesSvc, entries: entriesSvc, inflection: inflectionSvc}
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

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/auth/token", s.handleMintToken)
		r.Get("/languages", s.handleListLanguages)

		r.Group(func(r chi.Router) {
			r.Use(s.requireReadAccess)
			r.Get("/languages/{code}", s.handleGetLanguage)
			r.Get("/languages/{code}/schema", s.handleGetSchema)
			r.Get("/languages/{code}/entries", s.handleListEntries)
			r.Get("/languages/{code}/entries/{id}", s.handleGetEntry)
			r.Get("/languages/{code}/entries/{id}/inflection", s.handleEntryInflection)
			r.Get("/languages/{code}/inflection-templates", s.handleListInflectionTemplates)
			r.Get("/languages/{code}/inflection-forms", s.handleListInflectionForms)
		})

		r.Group(func(r chi.Router) {
			r.Use(s.requireWriteAccess)
			r.Post("/languages/{code}/entries", s.handleCreateEntry)
			r.Put("/languages/{code}/entries/{id}", s.handleUpdateEntry)
			r.Delete("/languages/{code}/entries/{id}", s.handleDeleteEntry)
			r.Post("/languages/{code}/entries/{id}/translations", s.handleAddTranslation)
			r.Put("/languages/{code}/entries/{id}/translations/{translationID}", s.handleUpdateTranslation)
			r.Delete("/languages/{code}/entries/{id}/translations/{translationID}", s.handleDeleteTranslation)

			r.Post("/languages/{code}/inflection-templates", s.handleCreateInflectionTemplate)
			r.Put("/languages/{code}/inflection-templates/{id}", s.handleUpdateInflectionTemplate)
			r.Delete("/languages/{code}/inflection-templates/{id}", s.handleDeleteInflectionTemplate)
			r.Post("/languages/{code}/inflection-forms", s.handleUpsertInflectionForms)
			r.Delete("/languages/{code}/inflection-forms/{id}", s.handleDeleteInflectionForm)
		})

		r.Group(func(r chi.Router) {
			r.Use(s.auth.RequireAdmin)
			r.Post("/users", s.handleCreateUser)
			r.Get("/users", s.handleListUsers)
			r.Post("/users/{id}/roles", s.handleGrantRole)
			r.Get("/users/{id}/roles", s.handleListGrants)
			r.Delete("/users/{id}/roles/{grantID}", s.handleRevokeGrant)

			r.Post("/languages", s.handleCreateLanguage)
			r.Put("/languages/{code}", s.handleUpdateLanguage)
			r.Put("/languages/{code}/schema", s.handlePutSchema)
		})
	})

	return r
}
