package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"phraseforge/internal/models"
)

// modelsReviewAPI is the vocabAPI harness with the models store and the models
// review routes added, and one models list "Models" (arb/arab) of two items.
type modelsReviewAPI struct {
	*vocabAPI
	listID int64
}

func newModelsReviewAPI(t *testing.T) *modelsReviewAPI {
	t.Helper()
	f := newVocabAPI(t)
	f.srv.models = models.New(f.srv.db)
	mux := f.router.(*chi.Mux)
	mux.Post("/api/v1/models/{id}/review", f.srv.apiReviewModelsList)
	mux.Get("/api/v1/models/{id}/reviews", f.srv.apiListModelsReviews)
	mux.Post("/api/v1/models/{id}/items/{position}/review", f.srv.apiReviewModelsItem)
	mux.Get("/api/v1/models/{id}/items/{position}/review", f.srv.apiGetModelsItemReview)
	mux.Post("/api/v1/models/{id}/items/{position}/review/apply", f.srv.apiApplyModelsItemReview)
	mux.Post("/api/v1/models/{id}/items/{position}/review/dismiss", f.srv.apiDismissModelsItemReview)
	ctx := context.Background()
	listID, err := f.srv.models.Create(ctx, f.adminID, "Models", "arb", "arab")
	if err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"كتاب", "قلم"} {
		if _, err := f.srv.models.AddItem(ctx, listID, phrase, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.srv.models.SetTranslation(ctx, listID, 0, "en", "book", 2); err != nil {
		t.Fatal(err)
	}
	return &modelsReviewAPI{vocabAPI: f, listID: listID}
}

func (f *modelsReviewAPI) path(suffix string) string {
	return fmt.Sprintf("/api/v1/models/%d%s", f.listID, suffix)
}

// storeReview saves findings as item position's review, as the job would.
func (f *modelsReviewAPI) storeReview(t *testing.T, position int, phrase string, findings []models.ReviewFinding) {
	t.Helper()
	ctx := context.Background()
	items, err := f.srv.models.Items(ctx, f.listID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range findings {
		findings[i].Status = models.ReviewStatusOpen
	}
	raw, _ := json.Marshal(findings)
	if err := f.srv.models.SaveReview(ctx, items[position].ID, phrase, raw, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
}

func TestModelsAPIReviewQueuesOncePerItemAndNeedsEditRight(t *testing.T) {
	f := newModelsReviewAPI(t)
	count := func(rec interface{ Bytes() []byte }) (int, int) {
		var b struct{ Enqueued, AlreadyQueued int }
		_ = json.Unmarshal(rec.Bytes(), &b)
		return b.Enqueued, b.AlreadyQueued
	}

	rec := f.do(t, f.adminID, http.MethodPost, f.path("/review"), nil)
	if e, a := count(rec.Body); rec.Code != http.StatusAccepted || e != 2 || a != 0 {
		t.Fatalf("list review: %d, %d queued, %d already; want 202, 2, 0", rec.Code, e, a)
	}
	rec = f.do(t, f.adminID, http.MethodPost, f.path("/review"), nil)
	if e, a := count(rec.Body); e != 0 || a != 2 {
		t.Errorf("second list review: %d queued, %d already; want 0 and 2 (one waiting job per item)", e, a)
	}
	rec = f.do(t, f.adminID, http.MethodPost, f.path("/items/1/review"), nil)
	if e, a := count(rec.Body); e != 0 || a != 1 {
		t.Errorf("item review of a waiting item: %d queued, %d already; want 0 and 1", e, a)
	}
	var pending struct{ Pending []int }
	rec = f.do(t, f.adminID, http.MethodGet, f.path("/reviews"), nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &pending); err != nil || len(pending.Pending) != 2 {
		t.Errorf("reviews = %s, want both positions pending", rec.Body)
	}

	for name, c := range map[string]struct {
		user         int64
		method, path string
		status       int
	}{
		"student queue":      {f.studentID, http.MethodPost, f.path("/review"), http.StatusForbidden},
		"student read":       {f.studentID, http.MethodGet, f.path("/reviews"), http.StatusForbidden},
		"student item apply": {f.studentID, http.MethodPost, f.path("/items/0/review/apply"), http.StatusForbidden},
		"unknown list":       {f.adminID, http.MethodPost, "/api/v1/models/99999/review", http.StatusNotFound},
		"unknown item":       {f.adminID, http.MethodPost, f.path("/items/9/review"), http.StatusNotFound},
		"bad position":       {f.adminID, http.MethodGet, f.path("/items/x/review"), http.StatusBadRequest},
		"no review yet":      {f.adminID, http.MethodGet, f.path("/items/0/review"), http.StatusNotFound},
	} {
		if rec := f.do(t, c.user, c.method, c.path, nil); rec.Code != c.status {
			t.Errorf("%s: status %d, want %d (%s)", name, rec.Code, c.status, rec.Body)
		}
	}
}

func TestModelsAPIReviewReadApplyAndDismiss(t *testing.T) {
	f := newModelsReviewAPI(t)
	f.storeReview(t, 0, "كتاب", []models.ReviewFinding{
		{Field: models.ReviewFieldTranslation, Action: models.ReviewActionReplace, Locale: "en", Current: "book", Suggestion: "a book"},
		{Field: models.ReviewFieldTranslation, Action: models.ReviewActionComment, Locale: "en", Reason: "ok"},
	})

	var view modelsReviewView
	rec := f.do(t, f.adminID, http.MethodGet, f.path("/items/0/review"), nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || rec.Code != http.StatusOK || len(view.Findings) != 2 || view.Stale || view.Position != 0 {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}

	var applied struct {
		Results []models.ReviewResult
		Review  modelsReviewView
	}
	rec = f.do(t, f.adminID, http.MethodPost, f.path("/items/0/review/apply"), `{"indexes":[0]}`)
	if err := json.Unmarshal(rec.Body.Bytes(), &applied); err != nil || rec.Code != http.StatusOK ||
		len(applied.Results) != 1 || applied.Results[0].Outcome != models.ReviewApplied || applied.Review.Findings[0].Status != models.ReviewStatusApplied {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	if tr, _ := f.srv.models.Translations(context.Background(), f.listID, "en"); tr[0].Translation != "a book" {
		t.Errorf("translation = %q, want the applied suggestion", tr[0].Translation)
	}

	rec = f.do(t, f.adminID, http.MethodPost, f.path("/items/0/review/dismiss"), nil) // no body: every open one
	if err := json.Unmarshal(rec.Body.Bytes(), &applied); err != nil || rec.Code != http.StatusOK || applied.Review.Findings[1].Status != models.ReviewStatusDismissed {
		t.Fatalf("dismiss: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(t, f.adminID, http.MethodPost, f.path("/items/0/review/apply"), `{"indexes":[7]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a finding that does not exist: status %d, want 400", rec.Code)
	}
	if rec := f.do(t, f.adminID, http.MethodPost, f.path("/items/0/review/apply"), `{`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON: status %d, want 400", rec.Code)
	}

	// A phrase edited after the review makes it stale: applying is a 409.
	if err := f.srv.models.UpdateItem(context.Background(), f.listID, 0, "كتابان", ""); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(t, f.adminID, http.MethodPost, f.path("/items/0/review/apply"), nil); rec.Code != http.StatusConflict {
		t.Errorf("stale review: status %d, want 409 (%s)", rec.Code, rec.Body)
	}
	rec = f.do(t, f.adminID, http.MethodGet, f.path("/reviews"), nil)
	var list struct{ Reviews []models.ReviewSummary }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Reviews) != 1 || !list.Reviews[0].Stale {
		t.Errorf("reviews = %s, want one stale review", rec.Body)
	}
}
