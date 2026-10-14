// Models review endpoints: the counterpart of review.go for a models list. Queue
// a review of a list or of one item, read the stored proposals, apply or dismiss
// them; everything needs CanEdit on the list's language. Findings are addressed
// by their 0-based position in the review's findings array.
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
	"phraseforge/internal/models"
)

// modelsReviewList authorizes the caller for the models list in the URL; false
// means the response has been written.
func (s *Server) modelsReviewList(w http.ResponseWriter, r *http.Request) (reviewTarget, bool) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_id", err.Error())
		return reviewTarget{}, false
	}
	l, err := s.models.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "not_found", "models list not found")
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
		writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, "models.err_no_edit_language"))
		return reviewTarget{}, false
	}
	return reviewTarget{listID: id, language: l.Language}, true
}

// modelsReviewItem is modelsReviewList plus the item at the URL's position.
func (s *Server) modelsReviewItem(w http.ResponseWriter, r *http.Request) (reviewTarget, bool) {
	t, ok := s.modelsReviewList(w, r)
	if !ok {
		return reviewTarget{}, false
	}
	position, err := strconv.Atoi(chi.URLParam(r, "position"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_position", err.Error())
		return reviewTarget{}, false
	}
	items, err := s.models.Items(r.Context(), t.listID)
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
	writeErr(w, http.StatusNotFound, "not_found", "models item not found")
	return reviewTarget{}, false
}

// enqueueModelsReviews queues one review job per item id that has none waiting,
// and reports how many it queued and how many already had one.
func (s *Server) enqueueModelsReviews(r *http.Request, itemIDs []int64) (enqueued, alreadyQueued int, err error) {
	active, err := s.jobs.ActiveItemIDs(r.Context(), ai.KindReviewModelsItem)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range itemIDs {
		if active[id] {
			alreadyQueued++
			continue
		}
		job, err := ai.NewReviewModelsJob(id)
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

// apiReviewModelsList handles POST /api/v1/models/{id}/review: one review job
// per item of the list.
func (s *Server) apiReviewModelsList(w http.ResponseWriter, r *http.Request) {
	t, ok := s.modelsReviewList(w, r)
	if !ok {
		return
	}
	items, err := s.models.Items(r.Context(), t.listID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	enqueued, already, err := s.enqueueModelsReviews(r, ids)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"enqueued": enqueued, "alreadyQueued": already})
}

// apiReviewModelsItem handles POST /api/v1/models/{id}/items/{position}/review.
func (s *Server) apiReviewModelsItem(w http.ResponseWriter, r *http.Request) {
	t, ok := s.modelsReviewItem(w, r)
	if !ok {
		return
	}
	enqueued, already, err := s.enqueueModelsReviews(r, []int64{t.itemID})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"enqueued": enqueued, "alreadyQueued": already})
}

// apiListModelsReviews handles GET /api/v1/models/{id}/reviews: the stored
// reviews of the list's items and the positions with a review job waiting.
func (s *Server) apiListModelsReviews(w http.ResponseWriter, r *http.Request) {
	t, ok := s.modelsReviewList(w, r)
	if !ok {
		return
	}
	reviews, err := s.models.ListReviews(r.Context(), t.listID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	active, err := s.jobs.ActiveItemIDs(r.Context(), ai.KindReviewModelsItem)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	items, err := s.models.Items(r.Context(), t.listID)
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

// modelsReviewView is an item's stored review as the dialog shows it.
type modelsReviewView struct {
	ItemID    int64                  `json:"itemId"`
	Position  int                    `json:"position"`
	Phrase    string                 `json:"phrase"` // the phrase that was reviewed
	CreatedAt time.Time              `json:"createdAt"`
	Stale     bool                   `json:"stale"`
	Findings  []models.ReviewFinding `json:"findings"`
}

func (s *Server) loadModelsReviewView(r *http.Request, t reviewTarget) (modelsReviewView, error) {
	review, err := s.models.GetReview(r.Context(), t.itemID)
	if err != nil {
		return modelsReviewView{}, err
	}
	findings, err := review.FindingList()
	if err != nil {
		return modelsReviewView{}, err
	}
	stale, err := s.models.IsReviewStale(r.Context(), t.itemID)
	if err != nil {
		return modelsReviewView{}, err
	}
	return modelsReviewView{ItemID: t.itemID, Position: t.position, Phrase: review.Phrase, CreatedAt: review.CreatedAt, Stale: stale, Findings: findings}, nil
}

// writeModelsReviewErr maps a models review store error to its response.
func writeModelsReviewErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, models.ErrReviewNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "this item has no review")
	case errors.Is(err, models.ErrItemNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "models item not found")
	case errors.Is(err, models.ErrItemChanged):
		writeErr(w, http.StatusConflict, "stale_review", "the phrase changed since it was reviewed; review it again")
	case errors.Is(err, models.ErrInvalidReviewIndex):
		writeErr(w, http.StatusBadRequest, "invalid_index", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

// apiGetModelsItemReview handles GET /api/v1/models/{id}/items/{position}/review.
func (s *Server) apiGetModelsItemReview(w http.ResponseWriter, r *http.Request) {
	t, ok := s.modelsReviewItem(w, r)
	if !ok {
		return
	}
	view, err := s.loadModelsReviewView(r, t)
	if err != nil {
		writeModelsReviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// apiApplyModelsItemReview handles POST
// /api/v1/models/{id}/items/{position}/review/apply. The response has the outcome
// of each finding and the review as it is afterwards.
func (s *Server) apiApplyModelsItemReview(w http.ResponseWriter, r *http.Request) {
	s.actOnModelsReview(w, r, func(t reviewTarget, indexes []int) (any, error) {
		results, err := s.models.ApplyReview(r.Context(), t.itemID, indexes)
		return results, err
	})
}

// apiDismissModelsItemReview handles POST
// /api/v1/models/{id}/items/{position}/review/dismiss.
func (s *Server) apiDismissModelsItemReview(w http.ResponseWriter, r *http.Request) {
	s.actOnModelsReview(w, r, func(t reviewTarget, indexes []int) (any, error) {
		return nil, s.models.DismissReview(r.Context(), t.itemID, indexes)
	})
}

func (s *Server) actOnModelsReview(w http.ResponseWriter, r *http.Request, act func(reviewTarget, []int) (any, error)) {
	t, ok := s.modelsReviewItem(w, r)
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
		writeModelsReviewErr(w, err)
		return
	}
	view, err := s.loadModelsReviewView(r, t)
	if err != nil {
		writeModelsReviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "review": view})
}
