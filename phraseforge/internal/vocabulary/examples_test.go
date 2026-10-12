package vocabulary

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func examplesOf(t *testing.T, s *Store, listID int64, position int) []string {
	t.Helper()
	items, err := s.Items(context.Background(), listID)
	if err != nil {
		t.Fatal(err)
	}
	return items[position].Examples
}

func TestSetItemExamplesRoundTripKeepsDiacritizedArabicAndOrder(t *testing.T) {
	s, _, uid := newTestStore(t)
	a := newList(t, s, uid, "A", "arb", "arab")
	pos := mustAdd(t, s, a, "وَحِيدًا", "", "")
	want := []string{"وَحِيدًا فِي الْبَيْتِ", "رَجُلٌ وَحِيدٌ", "ذَهَبَ وَحِيدًا"}

	if err := s.SetItemExamples(context.Background(), a, pos, want); err != nil {
		t.Fatal(err)
	}
	if got := examplesOf(t, s, a, pos); !reflect.DeepEqual(got, want) {
		t.Fatalf("examples = %q, want %q (same bytes, same order)", got, want)
	}
}

func TestItemWithoutExamplesHasNone(t *testing.T) {
	s, _, uid := newTestStore(t)
	a := newList(t, s, uid, "A", "arb", "arab")
	pos := mustAdd(t, s, a, "كتاب", "", "")
	if got := examplesOf(t, s, a, pos); len(got) != 0 {
		t.Fatalf("examples = %q, want none", got)
	}
}

func TestSetItemExamplesTrimsNormalisesAndDropsBlankAndDuplicates(t *testing.T) {
	s, _, uid := newTestStore(t)
	a := newList(t, s, uid, "A", "fra", "latn")
	pos := mustAdd(t, s, a, "café", "", "")

	// "e" + combining acute is the decomposed form of "é".
	in := []string{"  un café noir  ", "", "   ", "un café noir", "une tasse", "une tasse"}
	if err := s.SetItemExamples(context.Background(), a, pos, in); err != nil {
		t.Fatal(err)
	}
	want := []string{"un café noir", "une tasse"}
	if got := examplesOf(t, s, a, pos); !reflect.DeepEqual(got, want) {
		t.Fatalf("examples = %q, want %q", got, want)
	}
}

func TestSetItemExamplesRejectsTooManyOrTooLongAndWritesNothing(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "fra", "latn")
	pos := mustAdd(t, s, a, "mot", "", "")
	if err := s.SetItemExamples(ctx, a, pos, []string{"gardé"}); err != nil {
		t.Fatal(err)
	}

	tooMany := []string{"a", "b", "c", "d", "e", "f"}
	tooLong := []string{"ok", strings.Repeat("x", MaxExampleChars+1)}
	for name, in := range map[string][]string{"too many": tooMany, "too long": tooLong} {
		if err := s.SetItemExamples(ctx, a, pos, in); !errors.Is(err, ErrInvalidExamples) {
			t.Errorf("%s: err = %v, want ErrInvalidExamples", name, err)
		}
	}
	if got := examplesOf(t, s, a, pos); !reflect.DeepEqual(got, []string{"gardé"}) {
		t.Fatalf("examples = %q, want the earlier list untouched after the rejected writes", got)
	}
}

func TestSetItemExamplesAcceptsTheExactLimits(t *testing.T) {
	s, _, uid := newTestStore(t)
	a := newList(t, s, uid, "A", "arb", "arab")
	pos := mustAdd(t, s, a, "كتاب", "", "")

	// 80 Arabic letters are 80 characters although 160 bytes: the limit counts characters.
	eighty := strings.Repeat("ك", MaxExampleChars)
	in := []string{eighty, "b", "c", "d", "e"}
	if err := s.SetItemExamples(context.Background(), a, pos, in); err != nil {
		t.Fatalf("5 examples of up to 80 characters must be accepted: %v", err)
	}
	if got := examplesOf(t, s, a, pos); !reflect.DeepEqual(got, in) {
		t.Fatalf("examples = %q, want %q", got, in)
	}
}

func TestSetItemExamplesReplacesTheWholeListAndEmptyClears(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "fra", "latn")
	pos := mustAdd(t, s, a, "mot", "", "")

	if err := s.SetItemExamples(ctx, a, pos, []string{"un", "deux", "trois"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemExamples(ctx, a, pos, []string{"seul"}); err != nil {
		t.Fatal(err)
	}
	if got := examplesOf(t, s, a, pos); !reflect.DeepEqual(got, []string{"seul"}) {
		t.Fatalf("examples = %q, want only the replacement", got)
	}
	if err := s.SetItemExamples(ctx, a, pos, nil); err != nil {
		t.Fatal(err)
	}
	if got := examplesOf(t, s, a, pos); len(got) != 0 {
		t.Fatalf("examples = %q, want cleared", got)
	}
	if n := count(t, pool, "phrase_examples"); n != 0 {
		t.Fatalf("phrase_examples rows = %d, want 0", n)
	}
}

func TestExamplesAreSharedByEveryListThatLinksThePhrase(t *testing.T) {
	s, _, uid := newTestStore(t)
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	posA := mustAdd(t, s, a, "كتاب", "", "")
	posB := mustAdd(t, s, b, "كتاب", "", "")

	if err := s.SetItemExamples(context.Background(), a, posA, []string{"كِتَابٌ جَدِيدٌ"}); err != nil {
		t.Fatal(err)
	}
	if got := examplesOf(t, s, b, posB); !reflect.DeepEqual(got, []string{"كِتَابٌ جَدِيدٌ"}) {
		t.Fatalf("list B examples = %q, want the ones set through list A (shared phrase)", got)
	}
}

func TestSetItemExamplesOfAMissingItem(t *testing.T) {
	s, _, uid := newTestStore(t)
	a := newList(t, s, uid, "A", "arb", "arab")
	if err := s.SetItemExamples(context.Background(), a, 7, []string{"x"}); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("err = %v, want ErrItemNotFound", err)
	}
}

func TestDeletingTheLastLinkDeletesThePhrasesExamples(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	pos := mustAdd(t, s, a, "كتاب", "", "")
	if err := s.SetItemExamples(ctx, a, pos, []string{"كِتَابٌ جَدِيدٌ"}); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteItem(ctx, a, pos); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, "phrase_examples"); n != 0 {
		t.Fatalf("phrase_examples rows = %d, want 0 after the phrase's last link is gone", n)
	}
}

func TestMergingPhrasesAddsTheMergedPhrasesExamplesAfterTheSurvivors(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	typo := mustAdd(t, s, a, "كتب", "", "")
	good := mustAdd(t, s, b, "كتاب", "", "")
	if err := s.SetItemExamples(ctx, a, typo, []string{"كَتَبَ رِسَالَةً", "كِتَابٌ", "كَتَبَ دَرْسًا"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemExamples(ctx, b, good, []string{"كِتَابٌ"}); err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateItem(ctx, a, typo, "كتاب", "", ""); err != nil { // the typo becomes the good phrase: a merge
		t.Fatal(err)
	}

	want := []string{"كِتَابٌ", "كَتَبَ رِسَالَةً", "كَتَبَ دَرْسًا"} // survivor's first, then the new ones, no repeat
	for _, l := range []struct {
		id  int64
		pos int
	}{{a, typo}, {b, good}} {
		if got := examplesOf(t, s, l.id, l.pos); !reflect.DeepEqual(got, want) {
			t.Fatalf("list %d examples = %q, want %q", l.id, got, want)
		}
	}
}

func TestMergingPhrasesNeverExceedsTheExampleLimit(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "fra", "latn")
	b := newList(t, s, uid, "B", "fra", "latn")
	typo := mustAdd(t, s, a, "mott", "", "")
	good := mustAdd(t, s, b, "mot", "", "")
	if err := s.SetItemExamples(ctx, a, typo, []string{"x1", "x2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemExamples(ctx, b, good, []string{"s1", "s2", "s3", "s4"}); err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateItem(ctx, a, typo, "mot", "", ""); err != nil {
		t.Fatal(err)
	}

	want := []string{"s1", "s2", "s3", "s4", "x1"}
	if got := examplesOf(t, s, b, good); !reflect.DeepEqual(got, want) {
		t.Fatalf("examples = %q, want %q (capped at %d)", got, want, MaxExamples)
	}
	if n := count(t, pool, "phrase_examples"); n != MaxExamples {
		t.Fatalf("phrase_examples rows = %d, want %d (the merged phrase's rows are gone)", n, MaxExamples)
	}
}

func TestChangingAListsLanguageCopiesTheExamplesToThePhraseItNowLinks(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab") // keeps linking the Arabic phrase
	posA := mustAdd(t, s, a, "كتاب", "", "")
	posB := mustAdd(t, s, b, "كتاب", "", "")
	examples := []string{"كِتَابٌ جَدِيدٌ", "قَرَأَ كِتَابًا"}
	if err := s.SetItemExamples(ctx, a, posA, examples); err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateMeta(ctx, a, "A in Persian", "fas", "arab"); err != nil {
		t.Fatal(err)
	}

	if got := examplesOf(t, s, a, posA); !reflect.DeepEqual(got, examples) {
		t.Fatalf("list A examples = %q, want %q copied to its new (Persian) phrase", got, examples)
	}
	if got := examplesOf(t, s, b, posB); !reflect.DeepEqual(got, examples) {
		t.Fatalf("list B examples = %q, want its phrase's own examples untouched", got)
	}
}

func TestChangingAListsLanguageKeepsTheExamplesTheNewPhraseAlreadyHas(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	c := newList(t, s, uid, "C", "fas", "arab") // already has the same text as a Persian phrase
	posA := mustAdd(t, s, a, "كتاب", "", "")
	posC := mustAdd(t, s, c, "كتاب", "", "")
	if err := s.SetItemExamples(ctx, a, posA, []string{"عَرَبِيٌّ"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemExamples(ctx, c, posC, []string{"فارسی"}); err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateMeta(ctx, a, "A in Persian", "fas", "arab"); err != nil {
		t.Fatal(err)
	}

	if got := examplesOf(t, s, a, posA); !reflect.DeepEqual(got, []string{"فارسی"}) {
		t.Fatalf("list A examples = %q, want the Persian phrase's own examples, not a mix", got)
	}
}

func TestSyncWritesReplacesAndClearsExamplesButNilLeavesThemAlone(t *testing.T) {
	s, _, uid := newTestStore(t)
	a := newList(t, s, uid, "A", "arb", "arab")

	syncItems(t, s, a, true, SyncItem{Phrase: "كتاب", Examples: []string{"كِتَابٌ جَدِيدٌ", "قَرَأَ كِتَابًا"}})
	want := []string{"كِتَابٌ جَدِيدٌ", "قَرَأَ كِتَابًا"}
	if got := examplesOf(t, s, a, 0); !reflect.DeepEqual(got, want) {
		t.Fatalf("after a sync with examples: %q, want %q", got, want)
	}

	phraseID := phraseIDs(t, s, a)[0]
	syncItems(t, s, a, true, SyncItem{PhraseID: phraseID, Phrase: "كتاب", Grammar: "n"}) // examples nil: untouched
	if got := examplesOf(t, s, a, 0); !reflect.DeepEqual(got, want) {
		t.Fatalf("after a sync without examples: %q, want them untouched", got)
	}

	syncItems(t, s, a, true, SyncItem{PhraseID: phraseIDs(t, s, a)[0], Phrase: "كتاب", Grammar: "n", Examples: []string{"جديد"}})
	if got := examplesOf(t, s, a, 0); !reflect.DeepEqual(got, []string{"جديد"}) {
		t.Fatalf("after a sync with new examples: %q, want them replaced", got)
	}

	syncItems(t, s, a, true, SyncItem{PhraseID: phraseIDs(t, s, a)[0], Phrase: "كتاب", Grammar: "n", Examples: []string{}})
	if got := examplesOf(t, s, a, 0); len(got) != 0 {
		t.Fatalf("after a sync with an empty list: %q, want cleared", got)
	}
}

func TestSyncWithInvalidExamplesFailsAndChangesNothing(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	pos := mustAdd(t, s, a, "كتاب", "", "")
	if err := s.SetItemExamples(ctx, a, pos, []string{"قديم"}); err != nil {
		t.Fatal(err)
	}

	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // the sync failed, nothing to commit
	err = s.SyncItemsTx(ctx, tx, a, []SyncItem{
		{Phrase: "جديد"},
		{Phrase: "كتاب", Examples: []string{"a", "b", "c", "d", "e", "f"}},
	}, true)
	if !errors.Is(err, ErrInvalidExamples) {
		t.Fatalf("err = %v, want ErrInvalidExamples", err)
	}
	tx.Rollback(ctx) //nolint:errcheck // explicit: the caller's duty when the sync fails

	items, _ := s.Items(ctx, a)
	if len(items) != 1 || items[0].Phrase != "كتاب" || !reflect.DeepEqual(items[0].Examples, []string{"قديم"}) {
		t.Fatalf("items = %+v, want the list as it was before the failed sync", items)
	}
}
