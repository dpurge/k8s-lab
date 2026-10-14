package models

import (
	"context"
	"errors"
	"testing"
)

func TestReviewSourceReadsTheItemAsItIsNow(t *testing.T) {
	s, _, userID := newTestStore(t)
	ctx := context.Background()
	listID := newList(t, s, userID, "L", "deu", "latn")
	pos := mustAdd(t, s, listID, "Ich warte auf ...", "tr")
	mustTranslate(t, s, listID, "pl", pos, "Czekam na ...")
	mustTranslate(t, s, listID, "en", pos, "I am waiting for ...")
	items, err := s.Items(ctx, listID)
	if err != nil {
		t.Fatal(err)
	}

	src, err := s.ReviewSource(ctx, items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if src.Phrase != "Ich warte auf ..." || src.Transcription != "tr" || src.Language != "deu" {
		t.Errorf("source = %+v", src)
	}
	if len(src.Translations) != 2 || src.Translations[0] != (ReviewTranslation{"en", "I am waiting for ..."}) || src.Translations[1].Locale != "pl" {
		t.Errorf("translations = %+v, want en then pl", src.Translations)
	}
	if _, err := s.ReviewSource(ctx, items[0].ID+1000); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("unknown item: err = %v, want ErrItemNotFound", err)
	}
}

func TestSaveReviewReplacesAndOnlyWhileTheItemHasThePhrase(t *testing.T) {
	s, pool, userID := newTestStore(t)
	ctx := context.Background()
	listID := newList(t, s, userID, "L", "deu", "latn")
	mustAdd(t, s, listID, "Ich warte auf ...", "")
	items, _ := s.Items(ctx, listID)
	id := items[0].ID

	if _, err := s.GetReview(ctx, id); !errors.Is(err, ErrReviewNotFound) {
		t.Fatalf("before any review: err = %v, want ErrReviewNotFound", err)
	}
	if err := s.SaveReview(ctx, id, "Ich warte auf ...", []byte(`[{"field":"phrase"}]`), []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveReview(ctx, id, "Ich warte auf ...", []byte(`[]`), []byte(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetReview(ctx, id)
	if err != nil || string(r.Findings) != `[]` || r.Phrase != "Ich warte auf ..." || count(t, pool, "models_reviews") != 1 {
		t.Errorf("review = %+v, %v; want the second one, replacing the first", r, err)
	}
	if err := s.SaveReview(ctx, id, "another phrase", []byte(`[]`), []byte(`{}`)); !errors.Is(err, ErrItemChanged) {
		t.Errorf("a changed phrase: err = %v, want ErrItemChanged", err)
	}
	if err := s.SaveReview(ctx, id+1000, "x", []byte(`[]`), []byte(`{}`)); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("a gone item: err = %v, want ErrItemNotFound", err)
	}

	if err := s.DeleteItem(ctx, listID, 0); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, "models_reviews"); n != 0 {
		t.Errorf("%d reviews left after the item was deleted, want 0", n)
	}
}
