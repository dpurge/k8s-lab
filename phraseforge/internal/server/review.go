// Vocabulary review endpoints (see specs/features/
// phraseforge-vocabulary-review-job.md): queue a review of a list or of one
// item, read the stored proposals, and apply or dismiss them. Everything needs
// the same CanEdit(list's language) authorization as the other item actions.
// Findings are addressed by their 0-based position in the review's findings
// array (not by a finding's own "index", which is an example number).
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"phraseforge/internal/ai"
	"phraseforge/internal/i18n"
	"phraseforge/internal/jobs"
	"phraseforge/internal/vocabulary"
)

// reviewTarget is a list the caller may edit and, for the item routes, one of
// its items.
type reviewTarget struct {
	listID   int64
	language string
	itemID   int64
	position int
}

// reviewList authorizes the caller for the list in the URL; false means the
// response has been written.
func (s *Server) reviewList(w http.ResponseWriter, r *http.Request) (reviewTarget, bool) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_id", err.Error())
		return reviewTarget{}, false
	}
	l, err := s.vocab.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "not_found", "vocabulary list not found")
			return reviewTarget{}, false
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return reviewTarget{}, false
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, l.Language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return reviewTarget{}, false
	}
	if !canEdit {
		writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, "vocabulary.err_no_edit_language"))
		return reviewTarget{}, false
	}
	return reviewTarget{listID: id, language: l.Language}, true
}

// reviewItem is reviewList plus the item at the URL's position.
func (s *Server) reviewItem(w http.ResponseWriter, r *http.Request) (reviewTarget, bool) {
	t, ok := s.reviewList(w, r)
	if !ok {
		return reviewTarget{}, false
	}
	position, err := strconv.Atoi(chi.URLParam(r, "position"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_position", err.Error())
		return reviewTarget{}, false
	}
	items, err := s.vocab.Items(r.Context(), t.listID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return reviewTarget{}, false
	}
	for _, it := range items {
		if it.Position == position {
			t.itemID, t.position = it.ID, position
			return t, true
		}
	}
	writeErr(w, http.StatusNotFound, "not_found", "vocabulary item not found")
	return reviewTarget{}, false
}

// enqueueReviews queues one review job per item id that has none waiting, and
// reports how many it queued and how many already had one.
func (s *Server) enqueueReviews(r *http.Request, itemIDs []int64) (enqueued, alreadyQueued int, err error) {
	active, err := s.jobs.ActiveItemIDs(r.Context(), ai.KindReviewVocabularyItem)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range itemIDs {
		if active[id] {
			alreadyQueued++
			continue
		}
		job, err := ai.NewReviewJob(id)
		if err != nil {
			return enqueued, alreadyQueued, err
		}
		if _, err := s.jobs.Enqueue(r.Context(), job.Kind, jobs.PriorityBackground, job.Payload); err != nil {
			return enqueued, alreadyQueued, err
		}
		enqueued++
	}
	return enqueued, alreadyQueued, nil
}

// apiReviewVocabList handles POST /api/v1/vocabulary/{id}/review: one review
// job per item of the list.
func (s *Server) apiReviewVocabList(w http.ResponseWriter, r *http.Request) {
	t, ok := s.reviewList(w, r)
	if !ok {
		return
	}
	items, err := s.vocab.Items(r.Context(), t.listID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	enqueued, already, err := s.enqueueReviews(r, ids)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"enqueued": enqueued, "alreadyQueued": already})
}

// apiReviewVocabItem handles POST /api/v1/vocabulary/{id}/items/{position}/review.
func (s *Server) apiReviewVocabItem(w http.ResponseWriter, r *http.Request) {
	t, ok := s.reviewItem(w, r)
	if !ok {
		return
	}
	enqueued, already, err := s.enqueueReviews(r, []int64{t.itemID})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"enqueued": enqueued, "alreadyQueued": already})
}

// apiListVocabReviews handles GET /api/v1/vocabulary/{id}/reviews: the stored
// reviews of the list's items and the positions with a review job waiting.
func (s *Server) apiListVocabReviews(w http.ResponseWriter, r *http.Request) {
	t, ok := s.reviewList(w, r)
	if !ok {
		return
	}
	reviews, err := s.vocab.ListReviews(r.Context(), t.listID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	active, err := s.jobs.ActiveItemIDs(r.Context(), ai.KindReviewVocabularyItem)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	items, err := s.vocab.Items(r.Context(), t.listID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	pending := []int{}
	for _, it := range items {
		if active[it.ID] {
			pending = append(pending, it.Position)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"reviews": reviews, "pending": pending})
}

// reviewView is an item's stored review as the dialog shows it.
type reviewView struct {
	ItemID    int64                      `json:"itemId"`
	Position  int                        `json:"position"`
	Phrase    string                     `json:"phrase"` // the phrase that was reviewed
	CreatedAt time.Time                  `json:"createdAt"`
	Stale     bool                       `json:"stale"`
	Findings  []vocabulary.ReviewFinding `json:"findings"`
}

func (s *Server) loadReviewView(r *http.Request, t reviewTarget) (reviewView, error) {
	review, err := s.vocab.GetReview(r.Context(), t.itemID)
	if err != nil {
		return reviewView{}, err
	}
	findings, err := review.FindingList()
	if err != nil {
		return reviewView{}, err
	}
	stale, err := s.vocab.IsReviewStale(r.Context(), t.itemID)
	if err != nil {
		return reviewView{}, err
	}
	return reviewView{ItemID: t.itemID, Position: t.position, Phrase: review.Phrase, CreatedAt: review.CreatedAt, Stale: stale, Findings: findings}, nil
}

// writeReviewErr maps a review store error to its response.
func writeReviewErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vocabulary.ErrReviewNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "this item has no review")
	case errors.Is(err, vocabulary.ErrItemNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "vocabulary item not found")
	case errors.Is(err, vocabulary.ErrItemChanged):
		writeErr(w, http.StatusConflict, "stale_review", "the phrase changed since it was reviewed; review it again")
	case errors.Is(err, vocabulary.ErrInvalidReviewIndex):
		writeErr(w, http.StatusBadRequest, "invalid_index", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

// apiGetVocabItemReview handles GET /api/v1/vocabulary/{id}/items/{position}/review.
func (s *Server) apiGetVocabItemReview(w http.ResponseWriter, r *http.Request) {
	t, ok := s.reviewItem(w, r)
	if !ok {
		return
	}
	view, err := s.loadReviewView(r, t)
	if err != nil {
		writeReviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// reviewActRequest names the findings to act on; none means every open one.
type reviewActRequest struct {
	Indexes []int `json:"indexes"`
}

// apiApplyVocabItemReview handles POST
// /api/v1/vocabulary/{id}/items/{position}/review/apply. The response has the
// outcome of each finding and the review as it is afterwards.
func (s *Server) apiApplyVocabItemReview(w http.ResponseWriter, r *http.Request) {
	s.actOnReview(w, r, func(t reviewTarget, indexes []int) (any, error) {
		results, err := s.vocab.ApplyReview(r.Context(), t.itemID, indexes)
		return results, err
	})
}

// apiDismissVocabItemReview handles POST
// /api/v1/vocabulary/{id}/items/{position}/review/dismiss.
func (s *Server) apiDismissVocabItemReview(w http.ResponseWriter, r *http.Request) {
	s.actOnReview(w, r, func(t reviewTarget, indexes []int) (any, error) {
		return nil, s.vocab.DismissReview(r.Context(), t.itemID, indexes)
	})
}

func (s *Server) actOnReview(w http.ResponseWriter, r *http.Request, act func(reviewTarget, []int) (any, error)) {
	t, ok := s.reviewItem(w, r)
	if !ok {
		return
	}
	var req reviewActRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
	}
	results, err := act(t, req.Indexes)
	if err != nil {
		writeReviewErr(w, err)
		return
	}
	view, err := s.loadReviewView(r, t)
	if err != nil {
		writeReviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "review": view})
}
