package models

import (
	"context"
	"errors"
	"testing"
)

func TestItemSourceReturnsTheCurrentPhraseAndListLanguage(t *testing.T) {
	s, _, uid := newTestStore(t)
	a := newList(t, s, uid, "A", "arb", "arab")
	mustAdd(t, s, a, "كيف حالك", "")
	id := itemIDs(t, s, a)[0]

	got, err := s.ItemSource(context.Background(), id)
	if err != nil || got.Phrase != "كيف حالك" || got.Language != "arb" {
		t.Fatalf("ItemSource = (%+v, %v), want phrase كيف حالك in arb", got, err)
	}
	if _, err := s.ItemSource(context.Background(), id+1000); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("ItemSource of a missing item: err = %v, want ErrItemNotFound", err)
	}
}

func TestFillTranslationFollowsTheItemThroughAReorderingSync(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	mustAdd(t, s, a, "one", "")
	mustAdd(t, s, a, "two", "")
	twoID := itemIDs(t, s, a)[1]

	syncItems(t, s, a, true, SyncItem{Phrase: "zero"}, SyncItem{Phrase: "two"}, SyncItem{Phrase: "one"})

	applied, err := s.FillTranslation(ctx, twoID, "two", "en", "2")
	if err != nil || !applied {
		t.Fatalf("FillTranslation = (%v, %v), want applied", applied, err)
	}
	en, _ := s.Translations(ctx, a, "en")
	if en[1].Translation != "2" || len(en) != 1 {
		t.Fatalf("translations = %+v, want only position 1 (\"two\") translated", en)
	}
}

func TestFillWritesAreRefusedWhenTheItemIsGoneOrItsPhraseChanged(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	mustAdd(t, s, a, "one", "")
	mustAdd(t, s, a, "two", "")
	ids := itemIDs(t, s, a)

	if err := s.UpdateItem(ctx, a, 0, "uno", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteItem(ctx, a, 1); err != nil {
		t.Fatal(err)
	}

	fills := map[string]func(id int64) error{
		"translation":   func(id int64) error { _, err := s.FillTranslation(ctx, id, "one", "en", "1"); return err },
		"transcription": func(id int64) error { _, err := s.FillTranscription(ctx, id, "one", "wahid"); return err },
	}
	for name, fill := range fills {
		if err := fill(ids[0]); !errors.Is(err, ErrItemChanged) {
			t.Errorf("%s with a changed phrase: err = %v, want ErrItemChanged", name, err)
		}
		if err := fill(ids[1]); !errors.Is(err, ErrItemNotFound) {
			t.Errorf("%s of a deleted item: err = %v, want ErrItemNotFound", name, err)
		}
	}
	if en, _ := s.Translations(ctx, a, "en"); len(en) != 0 {
		t.Fatalf("translations = %+v, want nothing written", en)
	}
}

func TestFillTranscriptionOnlyFillsABlank(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	mustAdd(t, s, a, "blank", "")
	mustAdd(t, s, a, "filled", "old")
	ids := itemIDs(t, s, a)

	if applied, err := s.FillTranscription(ctx, ids[0], "blank", "new"); err != nil || !applied {
		t.Fatalf("FillTranscription of a blank = (%v, %v), want applied", applied, err)
	}
	if applied, err := s.FillTranscription(ctx, ids[1], "filled", "new"); err != nil || applied {
		t.Fatalf("FillTranscription of a filled one = (%v, %v), want not applied", applied, err)
	}
	items, _ := s.Items(ctx, a)
	if items[0].Transcription != "new" || items[1].Transcription != "old" {
		t.Fatalf("items = %+v, want blank filled and the other untouched", items)
	}
}

func TestFillTranscriptionAndTranslationWriteThroughTheSharedPhrase(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	mustAdd(t, s, a, "كيف", "")
	posB := mustAdd(t, s, b, "كيف", "")
	idA, idB := itemIDs(t, s, a)[0], itemIDs(t, s, b)[0]

	if applied, err := s.FillTranscription(ctx, idA, "كيف", "kayfa"); err != nil || !applied {
		t.Fatalf("first transcription write applied=%v err=%v", applied, err)
	}
	if applied, err := s.FillTranscription(ctx, idA, "كيف", "other"); err != nil || applied {
		t.Fatalf("second transcription write applied=%v err=%v, want not applied", applied, err)
	}
	if _, err := s.FillTranscription(ctx, idA, "stale", "x"); !errors.Is(err, ErrItemChanged) {
		t.Fatalf("stale phrase err = %v, want ErrItemChanged", err)
	}
	if items, _ := s.Items(ctx, b); items[posB].Transcription != "kayfa" {
		t.Fatalf("list B item = %+v, want the shared phrase's transcription", items[posB])
	}

	if applied, err := s.FillTranslation(ctx, idA, "كيف", "en", "how"); err != nil || !applied {
		t.Fatalf("first translation write applied=%v err=%v", applied, err)
	}
	if applied, err := s.FillTranslation(ctx, idB, "كيف", "en", "tome"); err != nil || applied {
		t.Fatalf("second translation write applied=%v err=%v, want not applied (shared)", applied, err)
	}
	if _, err := s.FillTranslation(ctx, idA, "stale", "pl", "x"); !errors.Is(err, ErrItemChanged) {
		t.Fatalf("stale phrase err = %v, want ErrItemChanged", err)
	}
}
