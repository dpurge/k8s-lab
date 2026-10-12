package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"phraseforge/internal/ai"
	"phraseforge/internal/translations"
	"phraseforge/internal/vocabulary"
)

// seedReviewedText creates an Arabic text with a transcription and an English
// translation and stores a review of it with findings.
func (f *vocabAPI) seedReviewedText(t *testing.T, resourceType, part string, findings ...ai.ReviewFinding) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	var err error
	if resourceType == "text" {
		id, err = f.srv.texts.Create(ctx, f.adminID, "T", "كتب الطالب", "kataba", "arb", "arab", "")
	} else {
		id, err = f.srv.dialogs.Create(ctx, f.adminID, "D", "A: مرحبا", "kataba", "arb", "arab", "")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := translations.New(f.srv.db).Set(ctx, resourceType, id, "en", "The student wrote"); err != nil {
		t.Fatal(err)
	}
	in, err := f.srv.resReviews.TextReviewSource(ctx, resourceType, id)
	if err != nil {
		t.Fatal(err)
	}
	for i := range findings {
		findings[i].Status = ai.ReviewOpen
	}
	raw, _ := json.Marshal(findings)
	if err := f.srv.resReviews.SaveTextReview(ctx, resourceType, id, part, in.Version, raw, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	return id
}

func resourcePath(resourceType string, id int64, part, suffix string) string {
	plural := map[string]string{"text": "texts", "dialog": "dialogs"}[resourceType]
	return fmt.Sprintf("/api/v1/%s/%d/review/%s%s", plural, id, part, suffix)
}

var textFindings = []ai.ReviewFinding{
	{Field: ai.ReviewFieldTranslation, Action: ai.ReviewReplace, Locale: "en", Current: "The student wrote", Suggestion: "The student wrote it", Reason: "Fuller."},
	{Field: ai.ReviewFieldTranslation, Action: ai.ReviewComment, Locale: "en", Current: "The student wrote", Reason: "Check the tense."},
}

func TestResourceReviewGetApplyAndDismissForTextsAndDialogs(t *testing.T) {
	for _, resourceType := range []string{"text", "dialog"} {
		t.Run(resourceType, func(t *testing.T) {
			f := newVocabAPI(t)
			id := f.seedReviewedText(t, resourceType, "translation", textFindings...)

			rec := f.do(t, f.adminID, http.MethodGet, resourcePath(resourceType, id, "translation", ""), nil)
			got := decodeJSON[struct {
				Pending bool                `json:"pending"`
				Review  *resourceReviewView `json:"review"`
			}](t, rec.Body.Bytes())
			if rec.Code != http.StatusOK || got.Pending || got.Review == nil || len(got.Review.Findings) != 2 || got.Review.Stale {
				t.Fatalf("GET = %d %s", rec.Code, rec.Body)
			}

			rec = f.do(t, f.adminID, http.MethodPost, resourcePath(resourceType, id, "translation", "/apply"), map[string]any{"indexes": []int{0}})
			act := decodeJSON[struct {
				Results []vocabulary.ReviewResult `json:"results"`
				Review  resourceReviewView        `json:"review"`
			}](t, rec.Body.Bytes())
			if rec.Code != http.StatusOK || len(act.Results) != 1 || act.Results[0].Outcome != "applied" || act.Review.Findings[0].Status != "applied" || act.Review.Findings[1].Status != "open" {
				t.Fatalf("apply = %d %s", rec.Code, rec.Body)
			}

			rec = f.do(t, f.adminID, http.MethodPost, resourcePath(resourceType, id, "translation", "/dismiss"), nil)
			act = decodeJSON[struct {
				Results []vocabulary.ReviewResult `json:"results"`
				Review  resourceReviewView        `json:"review"`
			}](t, rec.Body.Bytes())
			if rec.Code != http.StatusOK || act.Review.Findings[1].Status != "dismissed" {
				t.Fatalf("dismiss = %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestResourceReviewAnEditedSourceIsStaleAndRefused(t *testing.T) {
	f := newVocabAPI(t)
	id := f.seedReviewedText(t, "text", "translation", textFindings...)
	if err := f.srv.texts.Update(context.Background(), id, "T", "كتب الطالب كتابا", "kataba", "arb", "arab"); err != nil {
		t.Fatal(err)
	}
	rec := f.do(t, f.adminID, http.MethodGet, resourcePath("text", id, "translation", ""), nil)
	if got := decodeJSON[struct{ Review resourceReviewView }](t, rec.Body.Bytes()); !got.Review.Stale {
		t.Errorf("the review of an edited source must be stale: %s", rec.Body)
	}
	if rec := f.do(t, f.adminID, http.MethodPost, resourcePath("text", id, "translation", "/apply"), nil); rec.Code != http.StatusConflict {
		t.Errorf("apply = %d, want 409", rec.Code)
	}
}

func TestResourceReviewQueueingNeverMakesASecondJobAndTheViewShowsItPending(t *testing.T) {
	f := newVocabAPI(t)
	ctx := context.Background()
	id, err := f.srv.texts.Create(ctx, f.adminID, "T", "كتب", "", "arb", "arab", "")
	if err != nil {
		t.Fatal(err)
	}
	path := resourcePath("text", id, "translation", "")
	rec := f.do(t, f.adminID, http.MethodPost, path, nil)
	if got := decodeJSON[map[string]int](t, rec.Body.Bytes()); rec.Code != http.StatusAccepted || got["enqueued"] != 1 {
		t.Fatalf("first queue = %d %v", rec.Code, got)
	}
	rec = f.do(t, f.adminID, http.MethodPost, path, nil)
	if got := decodeJSON[map[string]int](t, rec.Body.Bytes()); got["enqueued"] != 0 || got["alreadyQueued"] != 1 {
		t.Errorf("second queue = %v, want nothing new", got)
	}
	// A dialog with the same id is a different resource.
	active, _ := f.srv.jobs.ActiveResourceIDs(ctx, ai.KindReviewTextJob, "dialog", "translation")
	if len(active) != 0 {
		t.Errorf("dialog jobs = %v, want none", active)
	}
	rec = f.do(t, f.adminID, http.MethodGet, path, nil)
	got := decodeJSON[struct {
		Pending bool            `json:"pending"`
		Review  json.RawMessage `json:"review"`
	}](t, rec.Body.Bytes())
	if !got.Pending || string(got.Review) != "null" {
		t.Errorf("view = %s, want pending and no review yet", rec.Body)
	}
}

func TestResourceReviewRoutesRefuseUsersWhoCannotEditAndUnknownIDs(t *testing.T) {
	f := newVocabAPI(t)
	id := f.seedReviewedText(t, "text", "translation", textFindings...)
	for _, c := range []struct{ method, suffix string }{{http.MethodGet, ""}, {http.MethodPost, ""}, {http.MethodPost, "/apply"}, {http.MethodPost, "/dismiss"}} {
		if rec := f.do(t, f.studentID, c.method, resourcePath("text", id, "translation", c.suffix), nil); rec.Code != http.StatusForbidden {
			t.Errorf("student %s %s = %d, want 403", c.method, c.suffix, rec.Code)
		}
	}
	if rec := f.do(t, f.adminID, http.MethodGet, resourcePath("text", id+99, "translation", ""), nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown text = %d, want 404", rec.Code)
	}
	if rec := f.do(t, f.adminID, http.MethodPost, resourcePath("text", id, "translation", "/apply"), map[string]any{"indexes": []int{5}}); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad index = %d, want 400", rec.Code)
	}
	if rec := f.do(t, f.adminID, http.MethodPost, resourcePath("dialog", id, "translation", "/apply"), nil); rec.Code != http.StatusNotFound {
		t.Errorf("a text id used as a dialog = %d, want 404", rec.Code)
	}
}

func TestResourceReviewPartsAreSeparateAndAnUnknownPartIsRefused(t *testing.T) {
	f := newVocabAPI(t)
	id := f.seedReviewedText(t, "text", "translation", textFindings...)
	// Only the translation has a review; the transcription of the same text has none.
	rec := f.do(t, f.adminID, http.MethodGet, resourcePath("text", id, "transcription", ""), nil)
	if got := decodeJSON[struct{ Review json.RawMessage }](t, rec.Body.Bytes()); rec.Code != http.StatusOK || string(got.Review) != "null" {
		t.Errorf("transcription view = %d %s, want no review", rec.Code, rec.Body)
	}
	// Queueing one part does not mark the other pending.
	if rec := f.do(t, f.adminID, http.MethodPost, resourcePath("text", id, "transcription", ""), nil); rec.Code != http.StatusAccepted {
		t.Fatalf("queue = %d", rec.Code)
	}
	for part, wantPending := range map[string]bool{"transcription": true, "translation": false} {
		rec := f.do(t, f.adminID, http.MethodGet, resourcePath("text", id, part, ""), nil)
		if got := decodeJSON[struct{ Pending bool }](t, rec.Body.Bytes()); got.Pending != wantPending {
			t.Errorf("%s pending = %v, want %v", part, got.Pending, wantPending)
		}
	}
	if rec := f.do(t, f.adminID, http.MethodGet, resourcePath("text", id, "title", ""), nil); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown part = %d, want 400", rec.Code)
	}
}
