// Text and dialog review endpoints (see specs/features/
// phraseforge-text-dialog-review-job.md): queue a review, read the stored
// proposals, apply or dismiss them. The same CanEdit(resource's language)
// authorization as the generate routes. Each part (transcription, translation)
// is reviewed separately: /review/{part}. Findings are addressed by their
// 0-based position in the review's findings array.
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
	"phraseforge/internal/resreview"
	"phraseforge/internal/vocabulary"
)

// resourceReviewView is a resource's stored review as the dialog shows it.
type resourceReviewView struct {
	CreatedAt time.Time                  `json:"createdAt"`
	Stale     bool                       `json:"stale"`
	Findings  []vocabulary.ReviewFinding `json:"findings"`
}

// reviewResourceID authorizes the caller for the text or dialog in the URL and
// returns its id; false means the response has been written.
func (s *Server) reviewResourceID(w http.ResponseWriter, r *http.Request, resourceType string) (int64, bool) {
	u := currentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_id", err.Error())
		return 0, false
	}
	var language string
	if resourceType == "text" {
		got, getErr := s.texts.Get(r.Context(), id)
		language, err = got.Language, getErr
	} else {
		got, getErr := s.dialogs.Get(r.Context(), id)
		language, err = got.Language, getErr
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "not_found", resourceType+" not found")
			return 0, false
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return 0, false
	}
	canEdit, err := s.roles.CanEdit(r.Context(), u.ID, language)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return 0, false
	}
	if !canEdit {
		writeErr(w, http.StatusForbidden, "forbidden", i18n.T(u.Locale, "texts.err_no_edit_language"))
		return 0, false
	}
	return id, true
}

func writeResourceReviewErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, resreview.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "this resource has no review")
	case errors.Is(err, ai.ErrItemGone):
		writeErr(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, ai.ErrItemChanged):
		writeErr(w, http.StatusConflict, "stale_review", "the source text changed since it was reviewed; review it again")
	case errors.Is(err, vocabulary.ErrInvalidReviewIndex):
		writeErr(w, http.StatusBadRequest, "invalid_index", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

// reviewResourceRoutes returns the four handlers of resourceType's review.
func (s *Server) reviewResourceHandler(resourceType, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		part := chi.URLParam(r, "part")
		if !ai.IsReviewPart(part) {
			writeErr(w, http.StatusBadRequest, "invalid_part", "part must be transcription or translation")
			return
		}
		id, ok := s.reviewResourceID(w, r, resourceType)
		if !ok {
			return
		}
		switch action {
		case "queue":
			s.queueResourceReview(w, r, resourceType, id, part)
		case "get":
			s.getResourceReview(w, r, resourceType, id, part)
		default:
			s.actOnResourceReview(w, r, resourceType, id, part, action)
		}
	}
}

func (s *Server) queueResourceReview(w http.ResponseWriter, r *http.Request, resourceType string, id int64, part string) {
	active, err := s.jobs.ActiveResourceIDs(r.Context(), ai.KindReviewTextJob, resourceType, part)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if active[id] {
		writeJSON(w, http.StatusAccepted, map[string]any{"enqueued": 0, "alreadyQueued": 1})
		return
	}
	job, err := ai.NewTextReviewJob(resourceType, id, part)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if _, err := s.jobs.Enqueue(r.Context(), job.Kind, jobs.PriorityBackground, job.Payload); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"enqueued": 1, "alreadyQueued": 0})
}

// getResourceReview answers {pending, review}; review is null before the first
// review of the resource.
func (s *Server) getResourceReview(w http.ResponseWriter, r *http.Request, resourceType string, id int64, part string) {
	active, err := s.jobs.ActiveResourceIDs(r.Context(), ai.KindReviewTextJob, resourceType, part)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	var view *resourceReviewView
	review, err := s.resReviews.Get(r.Context(), resourceType, id, part)
	switch {
	case err == nil:
		view = &resourceReviewView{CreatedAt: review.CreatedAt, Stale: review.Stale, Findings: review.Findings}
	case errors.Is(err, resreview.ErrNotFound):
	default:
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": active[id], "review": view})
}

func (s *Server) actOnResourceReview(w http.ResponseWriter, r *http.Request, resourceType string, id int64, part, action string) {
	var req reviewActRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
	}
	var results any
	var err error
	if action == "apply" {
		results, err = s.resReviews.Apply(r.Context(), resourceType, id, part, req.Indexes)
	} else {
		err = s.resReviews.Dismiss(r.Context(), resourceType, id, part, req.Indexes)
	}
	if err != nil {
		writeResourceReviewErr(w, err)
		return
	}
	review, err := s.resReviews.Get(r.Context(), resourceType, id, part)
	if err != nil {
		writeResourceReviewErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
		"review":  resourceReviewView{CreatedAt: review.CreatedAt, Stale: review.Stale, Findings: review.Findings},
	})
}
