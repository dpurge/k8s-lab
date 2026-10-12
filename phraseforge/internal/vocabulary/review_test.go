package vocabulary

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestReviewSourceReturnsTheItemAsStored(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "cmn", "hans")
	pos := mustAdd(t, s, a, "吃了", "V", "chī le")
	mustTranslate(t, s, a, "pl", pos, "jeść", "czasownik")
	mustTranslate(t, s, a, "en", pos, "to eat", "")
	if err := s.SetItemExamples(ctx, a, pos, []string{"吃饭", "吃苹果"}); err != nil {
		t.Fatal(err)
	}
	id := itemIDs(t, s, a)[0]

	got, err := s.ReviewSource(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := ReviewSource{
		Phrase: "吃了", Grammar: "V", Transcription: "chī le", Language: "cmn",
		Examples: []string{"吃饭", "吃苹果"},
		// In locale order, notes included.
		Translations: []ReviewTranslation{{Locale: "en", Translation: "to eat"}, {Locale: "pl", Translation: "jeść", Notes: "czasownik"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReviewSource = %+v, want %+v", got, want)
	}
	if _, err := s.ReviewSource(ctx, id+1000); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("a missing item: err = %v, want ErrItemNotFound", err)
	}
}

func TestReviewSourceSkipsLocalesWithNothingAndKeepsNotesOnlyOnes(t *testing.T) {
	s, _, uid := newTestStore(t)
	a := newList(t, s, uid, "A", "deu", "latn")
	pos := mustAdd(t, s, a, "der Hund", "", "")
	mustTranslate(t, s, a, "en", pos, "  ", "")
	mustTranslate(t, s, a, "pl", pos, "", "tylko notatka")

	got, err := s.ReviewSource(context.Background(), itemIDs(t, s, a)[0])
	if err != nil {
		t.Fatal(err)
	}
	if want := []ReviewTranslation{{Locale: "pl", Notes: "tylko notatka"}}; !reflect.DeepEqual(got.Translations, want) {
		t.Errorf("translations = %+v, want %+v", got.Translations, want)
	}
	if got.Examples != nil {
		t.Errorf("examples = %v, want none", got.Examples)
	}
}

func TestSaveReviewStoresAndReplacesTheItemsReview(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "deu", "latn")
	mustAdd(t, s, a, "den Hund", "", "")
	id := itemIDs(t, s, a)[0]

	if _, err := s.GetReview(ctx, id); !errors.Is(err, ErrReviewNotFound) {
		t.Fatalf("before any review: err = %v, want ErrReviewNotFound", err)
	}
	first := json.RawMessage(`[{"field":"phrase","status":"open"}]`)
	if err := s.SaveReview(ctx, id, "den Hund", first, json.RawMessage(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveReview(ctx, id, "den Hund", json.RawMessage(`[]`), json.RawMessage(`{"n":2}`)); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetReview(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phrase != "den Hund" || string(got.Findings) != `[]` || string(got.Response) != `{"n": 2}` || got.CreatedAt.IsZero() {
		t.Errorf("review = %+v, want the second one only", got)
	}
}

func TestSaveReviewRefusesAChangedOrDeletedItemAndKeepsTheOldReview(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "deu", "latn")
	pos := mustAdd(t, s, a, "den Hund", "", "")
	id := itemIDs(t, s, a)[0]
	old := json.RawMessage(`["old"]`)
	if err := s.SaveReview(ctx, id, "den Hund", old, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}

	// The job started from another phrase than the item holds now.
	if err := s.SaveReview(ctx, id, "der Hund", json.RawMessage(`["new"]`), json.RawMessage(`{}`)); !errors.Is(err, ErrItemChanged) {
		t.Fatalf("a changed phrase: err = %v, want ErrItemChanged", err)
	}
	if got, _ := s.GetReview(ctx, id); string(got.Findings) != `["old"]` {
		t.Errorf("findings = %s, want the old review kept", got.Findings)
	}

	if err := s.SaveReview(ctx, id+1000, "x", json.RawMessage(`[]`), json.RawMessage(`{}`)); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("a missing item: err = %v, want ErrItemNotFound", err)
	}

	// The review goes with its item.
	if err := s.DeleteItem(ctx, a, pos); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetReview(ctx, id); !errors.Is(err, ErrReviewNotFound) {
		t.Errorf("after the item is deleted: err = %v, want ErrReviewNotFound", err)
	}
}
