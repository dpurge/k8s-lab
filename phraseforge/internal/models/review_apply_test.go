package models

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// reviewedItem is an item "Ich warte auf ..." / "tr" with en and pl translations
// and a stored review holding findings.
func reviewedItem(t *testing.T, findings []ReviewFinding) (s *Store, listID, itemID int64) {
	t.Helper()
	s, _, userID := newTestStore(t)
	ctx := context.Background()
	listID = newList(t, s, userID, "L", "deu", "latn")
	pos := mustAdd(t, s, listID, "Ich warte auf ...", "tr")
	mustTranslate(t, s, listID, "en", pos, "I am waiting for ...; (accusative)")
	mustTranslate(t, s, listID, "pl", pos, "Czekam na ...")
	items, err := s.Items(ctx, listID)
	if err != nil {
		t.Fatal(err)
	}
	itemID = items[0].ID
	for i := range findings {
		findings[i].Status = ReviewStatusOpen
	}
	raw, _ := json.Marshal(findings)
	if err := s.SaveReview(ctx, itemID, "Ich warte auf ...", raw, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	return s, listID, itemID
}

func stored(t *testing.T, s *Store, itemID int64) (Review, []ReviewFinding) {
	t.Helper()
	r, err := s.GetReview(context.Background(), itemID)
	if err != nil {
		t.Fatal(err)
	}
	f, err := r.FindingList()
	if err != nil {
		t.Fatal(err)
	}
	return r, f
}

func TestApplyReviewWritesTranslationTranscriptionAndPhrase(t *testing.T) {
	s, listID, itemID := reviewedItem(t, []ReviewFinding{
		{Field: ReviewFieldPhrase, Action: ReviewActionReplace, Current: "Ich warte auf ...", Suggestion: "Ich warte auf dich", Transcription: "tr2"},
		{Field: ReviewFieldTranslation, Action: ReviewActionReplace, Locale: "en", Current: "I am waiting for ...; (accusative)", Suggestion: "I am waiting for ..."},
		{Field: ReviewFieldTranscription, Action: ReviewActionReplace, Current: "tr", Suggestion: "tr1"},
		{Field: ReviewFieldTranslation, Action: ReviewActionComment, Locale: "pl", Reason: "fine"},
	})
	ctx := context.Background()

	// Every open finding: the phrase rewrite goes last, so the transcription
	// finding (about "tr") is applied first and the rewrite sets "tr2" after it.
	results, err := s.ApplyReview(ctx, itemID, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []ReviewResult{{0, ReviewApplied}, {1, ReviewApplied}, {2, ReviewApplied}, {3, ReviewNotApplicable}}
	for i, r := range results {
		if r != want[i] {
			t.Errorf("result %d = %+v, want %+v", i, r, want[i])
		}
	}
	items, _ := s.Items(ctx, listID)
	if items[0].Phrase != "Ich warte auf dich" || items[0].Transcription != "tr2" {
		t.Errorf("item = %+v, want the rewritten phrase with its new transcription", items[0])
	}
	if tr, _ := s.Translations(ctx, listID, "en"); tr[0].Translation != "I am waiting for ..." {
		t.Errorf("en translation = %q", tr[0].Translation)
	}
	review, findings := stored(t, s, itemID)
	if review.Phrase != "Ich warte auf dich" || findings[0].Status != ReviewStatusApplied || findings[3].Status != ReviewStatusOpen {
		t.Errorf("review follows the new phrase %q, statuses %q ... %q", review.Phrase, findings[0].Status, findings[3].Status)
	}
	if stale, err := s.IsReviewStale(ctx, itemID); err != nil || stale {
		t.Errorf("stale = %v, %v; want a review that follows its item to stay fresh", stale, err)
	}
}

func TestApplyReviewLeavesAnEditedValueAlone(t *testing.T) {
	s, listID, itemID := reviewedItem(t, []ReviewFinding{
		{Field: ReviewFieldTranslation, Action: ReviewActionReplace, Locale: "en", Current: "I am waiting for ...; (accusative)", Suggestion: "I am waiting for ..."},
	})
	ctx := context.Background()
	mustTranslate(t, s, listID, "en", 0, "edited by a person") // edited after the review

	results, err := s.ApplyReview(ctx, itemID, []int{0})
	if err != nil || len(results) != 1 || results[0].Outcome != ReviewConflict {
		t.Fatalf("results %+v, %v; want one conflict", results, err)
	}
	if tr, _ := s.Translations(ctx, listID, "en"); tr[0].Translation != "edited by a person" {
		t.Errorf("translation = %q, a conflict must write nothing", tr[0].Translation)
	}
	if _, f := stored(t, s, itemID); f[0].Status != ReviewStatusOpen {
		t.Errorf("status = %q, want the finding left open", f[0].Status)
	}
}

func TestApplyReviewRefusesAChangedPhraseAndBadIndexes(t *testing.T) {
	s, listID, itemID := reviewedItem(t, []ReviewFinding{
		{Field: ReviewFieldTranslation, Action: ReviewActionReplace, Locale: "pl", Current: "Czekam na ...", Suggestion: "x"},
	})
	ctx := context.Background()
	if _, err := s.ApplyReview(ctx, itemID, []int{5}); !errors.Is(err, ErrInvalidReviewIndex) {
		t.Errorf("index 5: err = %v, want ErrInvalidReviewIndex", err)
	}
	if err := s.UpdateItem(ctx, listID, 0, "Ich warte auf mich", "tr"); err != nil {
		t.Fatal(err)
	}
	if stale, _ := s.IsReviewStale(ctx, itemID); !stale {
		t.Error("the review must be stale once the phrase changed")
	}
	if _, err := s.ApplyReview(ctx, itemID, nil); !errors.Is(err, ErrItemChanged) {
		t.Errorf("changed phrase: err = %v, want ErrItemChanged", err)
	}
	if _, err := s.ApplyReview(ctx, itemID+1000, nil); !errors.Is(err, ErrReviewNotFound) {
		t.Errorf("no review: err = %v, want ErrReviewNotFound", err)
	}
}

func TestApplyPhraseRewriteMergesIntoAnExistingPhrase(t *testing.T) {
	s, listID, itemID := reviewedItem(t, []ReviewFinding{
		{Field: ReviewFieldPhrase, Action: ReviewActionReplace, Current: "Ich warte auf ...", Suggestion: "Ich warte auf dich", Transcription: "tr"},
		{Field: ReviewFieldTranslation, Action: ReviewActionReplace, Locale: "pl", Current: "Czekam na ...", Suggestion: "Czekam na ciebie"},
	})
	ctx := context.Background()
	other := mustAdd(t, s, listID, "Ich warte auf dich", "tr") // the rewritten form already exists
	_ = other

	results, err := s.ApplyReview(ctx, itemID, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Outcome != ReviewApplied {
			t.Errorf("result %+v, want both applied (translation first, then the merge)", r)
		}
	}
	review, findings := stored(t, s, itemID)
	if review.Phrase != "Ich warte auf dich" || findings[0].Status != ReviewStatusApplied {
		t.Errorf("review %q / %q after the merge", review.Phrase, findings[0].Status)
	}
}

func TestDismissReviewAndListReviews(t *testing.T) {
	s, listID, itemID := reviewedItem(t, []ReviewFinding{
		{Field: ReviewFieldTranslation, Action: ReviewActionReplace, Locale: "pl", Current: "Czekam na ...", Suggestion: "x"},
		{Field: ReviewFieldTranslation, Action: ReviewActionComment, Locale: "en", Reason: "r"},
	})
	ctx := context.Background()
	list, err := s.ListReviews(ctx, listID)
	if err != nil || len(list) != 1 || list[0].Open != 2 || list[0].Total != 2 || list[0].Stale || list[0].Position != 0 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := s.DismissReview(ctx, itemID, []int{0}); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.ListReviews(ctx, listID); list[0].Open != 1 {
		t.Errorf("open = %d after one dismissal, want 1", list[0].Open)
	}
	if err := s.DismissReview(ctx, itemID, nil); err != nil {
		t.Fatal(err)
	}
	if _, f := stored(t, s, itemID); f[0].Status != ReviewStatusDismissed || f[1].Status != ReviewStatusDismissed {
		t.Errorf("statuses %q %q, want both dismissed", f[0].Status, f[1].Status)
	}
	if err := s.DismissReview(ctx, itemID, []int{9}); !errors.Is(err, ErrInvalidReviewIndex) {
		t.Errorf("index 9: err = %v", err)
	}
}
