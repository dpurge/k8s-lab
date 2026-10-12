package vocabulary

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestItemSourceReturnsTheCurrentPhraseAndListLanguage(t *testing.T) {
	s, _, uid := newTestStore(t)
	a := newList(t, s, uid, "A", "arb", "arab")
	mustAdd(t, s, a, "كتاب", "", "")
	id := itemIDs(t, s, a)[0]

	got, err := s.ItemSource(context.Background(), id)
	if err != nil || got.Phrase != "كتاب" || got.Language != "arb" {
		t.Fatalf("ItemSource = (%+v, %v), want phrase كتاب in arb", got, err)
	}
	if _, err := s.ItemSource(context.Background(), id+1000); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("ItemSource of a missing item: err = %v, want ErrItemNotFound", err)
	}
}

func TestFillTranslationFollowsTheItemThroughAReorderingSync(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	mustAdd(t, s, a, "one", "", "")
	mustAdd(t, s, a, "two", "", "")
	twoID := itemIDs(t, s, a)[1]

	// Regeneration puts "two" at another position and adds an item before it.
	syncItems(t, s, a, true, SyncItem{Phrase: "zero"}, SyncItem{Phrase: "two"}, SyncItem{Phrase: "one"})

	applied, err := s.FillTranslation(ctx, twoID, "two", "en", "2", "a number")
	if err != nil || !applied {
		t.Fatalf("FillTranslation = (%v, %v), want applied", applied, err)
	}
	en, _ := s.Translations(ctx, a, "en")
	if en[1].Translation != "2" || en[1].Notes != "a number" || len(en) != 1 {
		t.Fatalf("translations = %+v, want only position 1 (\"two\") translated", en)
	}
}

func TestFillTranslationLeavesAnExistingTranslationAlone(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	pos := mustAdd(t, s, a, "one", "", "")
	mustTranslate(t, s, a, "en", pos, "1", "kept")
	id := itemIDs(t, s, a)[0]

	applied, err := s.FillTranslation(ctx, id, "one", "en", "uno", "")
	if err != nil || applied {
		t.Fatalf("FillTranslation = (%v, %v), want not applied and no error", applied, err)
	}
	if en, _ := s.Translations(ctx, a, "en"); en[pos].Translation != "1" || en[pos].Notes != "kept" {
		t.Fatalf("translation = %+v, want the original kept", en[pos])
	}
}

func TestFillWritesAreRefusedWhenTheItemIsGoneOrItsPhraseChanged(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	mustAdd(t, s, a, "one", "", "")
	mustAdd(t, s, a, "two", "", "")
	ids := itemIDs(t, s, a)

	if err := s.UpdateItem(ctx, a, 0, "uno", "", ""); err != nil { // the phrase of ids[0] changes
		t.Fatal(err)
	}
	if err := s.DeleteItem(ctx, a, 1); err != nil { // ids[1] is deleted
		t.Fatal(err)
	}

	fills := map[string]func(id int64) error{
		"translation":   func(id int64) error { _, err := s.FillTranslation(ctx, id, "one", "en", "1", ""); return err },
		"transcription": func(id int64) error { _, err := s.FillTranscription(ctx, id, "one", "wahid"); return err },
		"grammar":       func(id int64) error { _, err := s.FillGrammar(ctx, id, "one", "n"); return err },
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

func TestFillTranscriptionAndGrammarOnlyFillBlanks(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	mustAdd(t, s, a, "blank", "", "")
	mustAdd(t, s, a, "filled", "v", "old")
	ids := itemIDs(t, s, a)

	if applied, err := s.FillTranscription(ctx, ids[0], "blank", "new"); err != nil || !applied {
		t.Fatalf("FillTranscription of a blank = (%v, %v), want applied", applied, err)
	}
	if applied, err := s.FillGrammar(ctx, ids[0], "blank", "n"); err != nil || !applied {
		t.Fatalf("FillGrammar of a blank = (%v, %v), want applied", applied, err)
	}
	if applied, err := s.FillTranscription(ctx, ids[1], "filled", "new"); err != nil || applied {
		t.Fatalf("FillTranscription of a filled one = (%v, %v), want not applied", applied, err)
	}
	if applied, err := s.FillGrammar(ctx, ids[1], "filled", "n"); err != nil || applied {
		t.Fatalf("FillGrammar of a filled one = (%v, %v), want not applied", applied, err)
	}
	items, _ := s.Items(ctx, a)
	if items[0].Transcription != "new" || items[0].Grammar != "n" || items[1].Transcription != "old" || items[1].Grammar != "v" {
		t.Fatalf("items = %+v, want blank filled and the other untouched", items)
	}
}

func TestFillTranscriptionAndGrammarWriteThroughTheSharedPhrase(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	posA := mustAdd(t, s, a, "كتاب", "", "")
	mustAdd(t, s, b, "كتاب", "", "")
	idA, idB := itemIDs(t, s, a)[0], itemIDs(t, s, b)[0]

	if applied, err := s.FillTranscription(ctx, idA, "كتاب", "kitab"); err != nil || !applied {
		t.Fatalf("first transcription write applied=%v err=%v", applied, err)
	}
	if applied, err := s.FillTranscription(ctx, idA, "كتاب", "other"); err != nil || applied {
		t.Fatalf("second transcription write applied=%v err=%v, want not applied", applied, err)
	}
	if _, err := s.FillTranscription(ctx, idA, "stale", "x"); !errors.Is(err, ErrItemChanged) {
		t.Fatalf("stale phrase err = %v, want ErrItemChanged", err)
	}
	if applied, err := s.FillGrammar(ctx, idB, "كتاب", "n"); err != nil || !applied {
		t.Fatalf("grammar write applied=%v err=%v", applied, err)
	}

	items, _ := s.Items(ctx, a)
	if items[posA].Transcription != "kitab" || items[posA].Grammar != "n" {
		t.Fatalf("list A item = %+v, want fields written through B too (shared phrase)", items[posA])
	}
}

func TestFillTranslationSeesATranslationMadeThroughAnotherList(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	mustAdd(t, s, a, "كتاب", "", "")
	posB := mustAdd(t, s, b, "كتاب", "", "")
	idA, idB := itemIDs(t, s, a)[0], itemIDs(t, s, b)[0]

	if applied, err := s.FillTranslation(ctx, idA, "كتاب", "en", "book", ""); err != nil || !applied {
		t.Fatalf("first write applied=%v err=%v", applied, err)
	}
	// B shares the phrase, so its translation already exists: no overwrite.
	if applied, err := s.FillTranslation(ctx, idB, "كتاب", "en", "tome", ""); err != nil || applied {
		t.Fatalf("second write applied=%v err=%v, want not applied", applied, err)
	}
	if _, err := s.FillTranslation(ctx, idA, "stale", "pl", "x", ""); !errors.Is(err, ErrItemChanged) {
		t.Fatalf("stale phrase err = %v, want ErrItemChanged", err)
	}
	if got, _ := s.Translations(ctx, b, "en"); got[posB].Translation != "book" {
		t.Fatalf("translation = %+v, want book", got[posB])
	}
}

func TestItemSourceCarriesThePhrasesExamplesInOrder(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	pos := mustAdd(t, s, a, "كتاب", "", "")
	id := itemIDs(t, s, a)[0]

	if src, err := s.ItemSource(ctx, id); err != nil || len(src.Examples) != 0 {
		t.Fatalf("ItemSource of a phrase without examples = (%+v, %v), want none", src, err)
	}
	want := []string{"كِتَابٌ جَدِيدٌ", "قَرَأَ كِتَابًا", "كِتَابُ الطَّالِبِ"}
	if err := s.SetItemExamples(ctx, a, pos, want); err != nil {
		t.Fatal(err)
	}
	src, err := s.ItemSource(ctx, id)
	if err != nil || !reflect.DeepEqual(src.Examples, want) || src.Phrase != "كتاب" || src.Language != "arb" {
		t.Fatalf("ItemSource = (%+v, %v), want phrase, language and the examples in order", src, err)
	}
}

func TestFillExampleAddsAfterTheExistingOnesAndKeepsTheirOrder(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	pos := mustAdd(t, s, a, "كتاب", "", "")
	id := itemIDs(t, s, a)[0]

	for i, ex := range []string{"كِتَابٌ جَدِيدٌ", "قَرَأَ كِتَابًا"} {
		if applied, err := s.FillExample(ctx, id, "كتاب", ex); err != nil || !applied {
			t.Fatalf("FillExample #%d = (%v, %v), want applied", i, applied, err)
		}
	}
	if got := examplesOf(t, s, a, pos); !reflect.DeepEqual(got, []string{"كِتَابٌ جَدِيدٌ", "قَرَأَ كِتَابًا"}) {
		t.Fatalf("examples = %q, want both in the order they were added", got)
	}
}

func TestFillExampleSkipsADuplicateEvenWhenWrittenDecomposed(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "fra", "latn")
	pos := mustAdd(t, s, a, "café", "", "")
	id := itemIDs(t, s, a)[0]
	if err := s.SetItemExamples(ctx, a, pos, []string{"un café noir"}); err != nil {
		t.Fatal(err)
	}

	// "e" + combining acute is the decomposed form of "é"; padding is trimmed.
	if applied, err := s.FillExample(ctx, id, "café", "  un cafe\u0301 noir "); err != nil || applied {
		t.Fatalf("FillExample of a duplicate = (%v, %v), want not applied and no error", applied, err)
	}
	if got := examplesOf(t, s, a, pos); !reflect.DeepEqual(got, []string{"un café noir"}) {
		t.Fatalf("examples = %q, want unchanged", got)
	}
}

func TestFillExampleStopsAtTheLimit(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "fra", "latn")
	pos := mustAdd(t, s, a, "mot", "", "")
	id := itemIDs(t, s, a)[0]
	full := []string{"un", "deux", "trois", "quatre", "cinq"}
	if err := s.SetItemExamples(ctx, a, pos, full); err != nil {
		t.Fatal(err)
	}

	if applied, err := s.FillExample(ctx, id, "mot", "six"); err != nil || applied {
		t.Fatalf("FillExample at the limit = (%v, %v), want not applied and no error", applied, err)
	}
	if got := examplesOf(t, s, a, pos); !reflect.DeepEqual(got, full) {
		t.Fatalf("examples = %q, want the five untouched", got)
	}
}

func TestFillExampleIgnoresBlankAndRejectsOverLong(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "fra", "latn")
	pos := mustAdd(t, s, a, "mot", "", "")
	id := itemIDs(t, s, a)[0]

	if applied, err := s.FillExample(ctx, id, "mot", "   "); err != nil || applied {
		t.Fatalf("FillExample of a blank = (%v, %v), want not applied and no error", applied, err)
	}
	if _, err := s.FillExample(ctx, id, "mot", strings.Repeat("x", MaxExampleChars+1)); !errors.Is(err, ErrInvalidExamples) {
		t.Fatalf("FillExample over the limit: err = %v, want ErrInvalidExamples", err)
	}
	if applied, err := s.FillExample(ctx, id, "mot", strings.Repeat("x", MaxExampleChars)); err != nil || !applied {
		t.Fatalf("FillExample of exactly the limit = (%v, %v), want applied", applied, err)
	}
	if got := examplesOf(t, s, a, pos); len(got) != 1 {
		t.Fatalf("examples = %q, want only the one of exactly the limit", got)
	}
}

func TestFillExampleIsRefusedWhenTheItemIsGoneOrItsPhraseChanged(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	mustAdd(t, s, a, "one", "", "")
	mustAdd(t, s, a, "two", "", "")
	ids := itemIDs(t, s, a)
	if err := s.UpdateItem(ctx, a, 0, "uno", "", ""); err != nil { // ids[0] now has another phrase
		t.Fatal(err)
	}
	if err := s.DeleteItem(ctx, a, 1); err != nil { // ids[1] is gone
		t.Fatal(err)
	}

	if _, err := s.FillExample(ctx, ids[0], "one", "x"); !errors.Is(err, ErrItemChanged) {
		t.Errorf("changed phrase: err = %v, want ErrItemChanged", err)
	}
	if _, err := s.FillExample(ctx, ids[1], "two", "x"); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("deleted item: err = %v, want ErrItemNotFound", err)
	}
	if got := examplesOf(t, s, a, 0); len(got) != 0 {
		t.Fatalf("examples = %q, want nothing written", got)
	}
}

func TestFillExampleShowsInEveryListThatLinksThePhrase(t *testing.T) {
	s, _, uid := newTestStore(t)
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	mustAdd(t, s, a, "كتاب", "", "")
	posB := mustAdd(t, s, b, "كتاب", "", "")

	if applied, err := s.FillExample(context.Background(), itemIDs(t, s, a)[0], "كتاب", "كِتَابٌ جَدِيدٌ"); err != nil || !applied {
		t.Fatalf("FillExample = (%v, %v), want applied", applied, err)
	}
	if got := examplesOf(t, s, b, posB); !reflect.DeepEqual(got, []string{"كِتَابٌ جَدِيدٌ"}) {
		t.Fatalf("list B examples = %q, want the one added through list A (shared phrase)", got)
	}
}

// Two jobs adding to the same phrase at the same moment, with room for only
// one more: exactly one is applied and the limit holds.
func TestFillExampleConcurrentJobsNeverExceedTheLimit(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "fra", "latn")
	pos := mustAdd(t, s, a, "mot", "", "")
	id := itemIDs(t, s, a)[0]
	if err := s.SetItemExamples(ctx, a, pos, []string{"un", "deux", "trois", "quatre"}); err != nil {
		t.Fatal(err)
	}

	results := make([]bool, 2)
	var wg sync.WaitGroup
	for i, ex := range []string{"cinq", "six"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			applied, err := s.FillExample(ctx, id, "mot", ex)
			if err != nil {
				t.Errorf("FillExample(%q): %v", ex, err)
			}
			results[i] = applied
		}()
	}
	wg.Wait()

	if results[0] == results[1] {
		t.Fatalf("applied = %v, want exactly one of the two to fit", results)
	}
	if got := examplesOf(t, s, a, pos); len(got) != MaxExamples {
		t.Fatalf("examples = %q, want exactly %d", got, MaxExamples)
	}
}

func TestRewritePhraseKeepsTheItemAndShowsTheNewPhraseInEveryListThatLinksIt(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	posA := mustAdd(t, s, a, "فِيهِ", "Prep", "fīhi")
	posB := mustAdd(t, s, b, "فِيهِ", "Prep", "fīhi")
	idA := itemIDs(t, s, a)[0]
	if err := s.SetTranslations(ctx, a, "en", []ItemTranslation{{Position: posA, Translation: "in it", Notes: "note"}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemExamples(ctx, a, posA, []string{"فِي الْبَيْتِ"}); err != nil {
		t.Fatal(err)
	}

	applied, err := s.RewritePhrase(ctx, idA, "فِيهِ", "فِي", "Prep", "fī")
	if err != nil || !applied {
		t.Fatalf("RewritePhrase = (%v, %v), want applied", applied, err)
	}

	for _, l := range []struct {
		list int64
		pos  int
	}{{a, posA}, {b, posB}} {
		items, err := s.Items(ctx, l.list)
		if err != nil {
			t.Fatal(err)
		}
		it := items[l.pos]
		if it.Phrase != "فِي" || it.Grammar != "Prep" || it.Transcription != "fī" {
			t.Errorf("list %d item = %+v, want the rewritten phrase, grammar and transcription", l.list, it)
		}
		if !reflect.DeepEqual(it.Examples, []string{"فِي الْبَيْتِ"}) {
			t.Errorf("list %d examples = %q, want them kept with the phrase", l.list, it.Examples)
		}
	}
	if items, _ := s.Items(ctx, a); items[posA].ID != idA {
		t.Errorf("item id = %d, want %d kept", items[posA].ID, idA)
	}
	translations, err := s.TranslationsByItem(ctx, a)
	if err != nil || translations[idA]["en"] != "in it" {
		t.Fatalf("translations = %v (err %v), want the translation kept with the phrase", translations, err)
	}
}

func TestRewritePhraseKeepsAnEmptyGrammarOrTranscriptionAndReplacesAGivenOne(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "deu", "latn")
	mustAdd(t, s, a, "Kinder", "N pl", "")
	mustAdd(t, s, a, "Tage", "N pl", "")
	ids := itemIDs(t, s, a)

	if applied, err := s.RewritePhrase(ctx, ids[0], "Kinder", "das Kind", "", ""); err != nil || !applied {
		t.Fatalf("rewrite with empty grammar = (%v, %v)", applied, err)
	}
	if applied, err := s.RewritePhrase(ctx, ids[1], "Tage", "der Tag", "N m", ""); err != nil || !applied {
		t.Fatalf("rewrite with grammar = (%v, %v)", applied, err)
	}

	items, _ := s.Items(ctx, a)
	if items[0].Grammar != "N pl" || items[1].Grammar != "N m" {
		t.Fatalf("grammar = %q and %q, want the stored one kept and the given one written", items[0].Grammar, items[1].Grammar)
	}
}

func TestRewritePhraseMergesIntoAnExistingPhraseWithTheNewValues(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "deu", "latn")
	b := newList(t, s, uid, "B", "deu", "latn")
	posA := mustAdd(t, s, a, "Kinder", "N pl", "")
	posB := mustAdd(t, s, b, "das Kind", "N n", "")
	idA := itemIDs(t, s, a)[0]
	if err := s.SetTranslations(ctx, a, "en", []ItemTranslation{{Position: posA, Translation: "children"}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTranslations(ctx, a, "pl", []ItemTranslation{{Position: posA, Translation: "dzieci"}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTranslations(ctx, b, "en", []ItemTranslation{{Position: posB, Translation: "child"}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemExamples(ctx, a, posA, []string{"die Kinder spielen"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemExamples(ctx, b, posB, []string{"ein Kind"}); err != nil {
		t.Fatal(err)
	}

	applied, err := s.RewritePhrase(ctx, idA, "Kinder", "das Kind", "N n", "")
	if err != nil || !applied {
		t.Fatalf("RewritePhrase = (%v, %v), want applied", applied, err)
	}

	items, _ := s.Items(ctx, a)
	if items[posA].ID != idA || items[posA].Phrase != "das Kind" || items[posA].PhraseID != itemAt(t, s, b, posB).PhraseID {
		t.Fatalf("list A item = %+v, want the same item id linked to the surviving phrase", items[posA])
	}
	if want := []string{"ein Kind", "die Kinder spielen"}; !reflect.DeepEqual(items[posA].Examples, want) {
		t.Errorf("examples = %q, want the survivor's first, then the merged ones %q", items[posA].Examples, want)
	}
	translations, _ := s.TranslationsByItem(ctx, a)
	if translations[idA]["en"] != "child" || translations[idA]["pl"] != "dzieci" {
		t.Errorf("translations = %v, want the survivor's en kept and the missing pl filled", translations[idA])
	}
}

func TestRewritePhraseChangesNothingWhenTheItemOrTheInputDoNotAllowIt(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "deu", "latn")
	mustAdd(t, s, a, "Kinder", "N pl", "")
	id := itemIDs(t, s, a)[0]

	if _, err := s.RewritePhrase(ctx, id, "stale", "das Kind", "", ""); !errors.Is(err, ErrItemChanged) {
		t.Errorf("stale phrase err = %v, want ErrItemChanged", err)
	}
	if _, err := s.RewritePhrase(ctx, id+1000, "Kinder", "das Kind", "", ""); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("unknown item err = %v, want ErrItemNotFound", err)
	}
	if _, err := s.RewritePhrase(ctx, id, "Kinder", "  ", "", ""); !errors.Is(err, ErrBlankPhrase) {
		t.Errorf("blank phrase err = %v, want ErrBlankPhrase", err)
	}
	if applied, err := s.RewritePhrase(ctx, id, "Kinder", "  Kinder ", "N", ""); err != nil || applied {
		t.Errorf("unchanged phrase = (%v, %v), want not applied and no error", applied, err)
	}
	if items, _ := s.Items(ctx, a); items[0].Phrase != "Kinder" || items[0].Grammar != "N pl" {
		t.Errorf("item = %+v, want it untouched", items[0])
	}
}

func itemAt(t *testing.T, s *Store, listID int64, position int) Item {
	t.Helper()
	items, err := s.Items(context.Background(), listID)
	if err != nil || position >= len(items) {
		t.Fatalf("items of list %d = %v (err %v), want position %d", listID, items, err, position)
	}
	return items[position]
}

func TestWithSense(t *testing.T) {
	cases := []struct {
		name, existing, sense, want string
		ok                          bool
	}{
		{"appends after a separator", "szkoła", "uczelnia", "szkoła; uczelnia", true},
		{"keeps several old senses as the prefix", "szkoła; nauka", "uczelnia", "szkoła; nauka; uczelnia", true},
		{"trims both", "  szkoła ", "  uczelnia  ", "szkoła; uczelnia", true},
		{"a trailing separator is not doubled", "szkoła;", "uczelnia", "szkoła; uczelnia", true},
		{"a sense that is already there is not added", "szkoła; nauka", "nauka", "", false},
		{"compared ignoring case", "Szkoła", "szkoła", "", false},
		{"compared after NFC", "café", "café", "", false},
		{"a longer sense containing an old one is new", "szkoła", "szkoła wyższa", "szkoła; szkoła wyższa", true},
		{"several senses: only the new ones are added", "szkoła; nauka", "nauka; uczelnia; szkoła; kurs", "szkoła; nauka; uczelnia; kurs", true},
		{"several senses, none of them new", "szkoła; nauka", "Nauka; SZKOŁA", "", false},
		{"a repeat within the new senses is added once", "szkoła", "uczelnia; Uczelnia", "szkoła; uczelnia", true},
		{"a blank sense is not added", "szkoła", "   ", "", false},
		{"nothing to append to", "  ", "uczelnia", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := withSense(c.existing, c.sense)
			if got != c.want || ok != c.ok {
				t.Errorf("withSense(%q, %q) = (%q, %v), want (%q, %v)", c.existing, c.sense, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestAppendSenseKeepsTheOldTextAsThePrefixAndLeavesNotesAndOtherLocalesAlone(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "deu", "latn")
	b := newList(t, s, uid, "B", "deu", "latn")
	mustAdd(t, s, a, "Schule", "N f", "")
	mustAdd(t, s, b, "Schule", "N f", "")
	idA := itemIDs(t, s, a)[0]
	if err := s.SetTranslations(ctx, a, "pl", []ItemTranslation{{Position: 0, Translation: "szkoła", Notes: "uwaga"}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTranslations(ctx, a, "en", []ItemTranslation{{Position: 0, Translation: "school"}}, 1); err != nil {
		t.Fatal(err)
	}

	applied, err := s.AppendSense(ctx, idA, "Schule", "pl", "uczelnia")
	if err != nil || !applied {
		t.Fatalf("AppendSense = (%v, %v), want applied", applied, err)
	}

	// list B links the same phrase, so it sees the new sense; notes and the other locale are untouched
	for _, list := range []int64{a, b} {
		pl, err := s.Translations(ctx, list, "pl")
		if err != nil {
			t.Fatal(err)
		}
		if pl[0].Translation != "szkoła; uczelnia" || pl[0].Notes != "uwaga" {
			t.Errorf("list %d pl = %+v, want the old text as prefix and the notes kept", list, pl[0])
		}
		en, err := s.Translations(ctx, list, "en")
		if err != nil {
			t.Fatal(err)
		}
		if en[0].Translation != "school" {
			t.Errorf("list %d en = %+v, want it untouched", list, en[0])
		}
	}
}

func TestAppendSenseChangesNothingWhenTheTranslationOrTheInputDoNotAllowIt(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "deu", "latn")
	mustAdd(t, s, a, "Schule", "N f", "")
	id := itemIDs(t, s, a)[0]
	if err := s.SetTranslations(ctx, a, "pl", []ItemTranslation{{Position: 0, Translation: "szkoła; nauka"}}, 1); err != nil {
		t.Fatal(err)
	}

	for name, sense := range map[string]string{"a sense already there": " NAUKA ", "a blank sense": "  "} {
		if applied, err := s.AppendSense(ctx, id, "Schule", "pl", sense); err != nil || applied {
			t.Errorf("%s: AppendSense = (%v, %v), want not applied and no error", name, applied, err)
		}
	}
	if applied, err := s.AppendSense(ctx, id, "Schule", "en", "school"); err != nil || applied {
		t.Errorf("a locale without a translation: AppendSense = (%v, %v), want not applied and no error", applied, err)
	}
	if _, err := s.AppendSense(ctx, id, "stale", "pl", "uczelnia"); !errors.Is(err, ErrItemChanged) {
		t.Errorf("stale phrase err = %v, want ErrItemChanged", err)
	}
	if _, err := s.AppendSense(ctx, id+1000, "Schule", "pl", "uczelnia"); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("unknown item err = %v, want ErrItemNotFound", err)
	}
	pl, _ := s.Translations(ctx, a, "pl")
	if pl[0].Translation != "szkoła; nauka" {
		t.Errorf("pl = %q, want it untouched", pl[0].Translation)
	}
	if en, _ := s.Translations(ctx, a, "en"); len(en) != 0 {
		t.Errorf("en = %+v, want no translation row created", en)
	}
}

func TestItemSourceCarriesThePhrasesNonBlankTranslationsByLocale(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "deu", "latn")
	mustAdd(t, s, a, "Schule", "N f", "")
	mustAdd(t, s, a, "Tag", "N m", "")
	ids := itemIDs(t, s, a)
	if err := s.SetTranslations(ctx, a, "pl", []ItemTranslation{{Position: 0, Translation: "szkoła; nauka"}}, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTranslations(ctx, a, "en", []ItemTranslation{{Position: 0, Translation: "school"}, {Position: 1, Translation: "  "}}, 2); err != nil {
		t.Fatal(err)
	}

	src, err := s.ItemSource(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"pl": "szkoła; nauka", "en": "school"}; !reflect.DeepEqual(src.Translations, want) {
		t.Errorf("Translations = %v, want %v", src.Translations, want)
	}
	none, err := s.ItemSource(ctx, ids[1])
	if err != nil || len(none.Translations) != 0 {
		t.Errorf("an item without translations: %v (err %v), want none (a blank one is not a translation)", none.Translations, err)
	}
}
