package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"phraseforge/internal/vocabulary"
)

// apiHideVocabItem hides the item's phrase for the current user only.
func (s *Server) apiHideVocabItem(w http.ResponseWriter, r *http.Request) {
	s.setVocabItemHidden(w, r, s.vocab.Hide)
}

// apiUnhideVocabItem shows the item's phrase to the current user again.
func (s *Server) apiUnhideVocabItem(w http.ResponseWriter, r *http.Request) {
	s.setVocabItemHidden(w, r, s.vocab.Unhide)
}

// setVocabItemHidden runs hide or unhide for the item at the URL's position.
// Hiding is a personal preference, so being able to view the list is enough.
func (s *Server) setVocabItemHidden(w http.ResponseWriter, r *http.Request, change func(ctx context.Context, userID, listID int64, position int) error) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	position, err := strconv.Atoi(chi.URLParam(r, "position"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_position", err.Error())
		return
	}
	l, err := s.vocab.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	canView, err := s.roles.CanView(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !canView {
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
		return
	}
	if err := change(r.Context(), u.ID, id, position); err != nil {
		if errors.Is(err, vocabulary.ErrItemNotFound) {
			writeErr(w, http.StatusNotFound, "not_found", "vocabulary item not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
