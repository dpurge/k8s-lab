package vocabulary

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests need a real Postgres: set PHRASEFORGE_TEST_DSN to a database
// URL the tests may create schemas in. Each test applies schema.sql into its
// own throwaway schema and drops it afterwards, so nothing else in the
// database is touched. Without the variable they are skipped.
const testDSNEnv = "PHRASEFORGE_TEST_DSN"

var schemaCounter atomic.Int64

func newTestStore(t *testing.T) (*Store, *pgxpool.Pool, int64) {
	t.Helper()
	dsn := os.Getenv(testDSNEnv)
	if dsn == "" {
		t.Skipf("%s not set", testDSNEnv)
	}
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	schema := fmt.Sprintf("pf_test_%d_%d", os.Getpid(), schemaCounter.Add(1))
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to schema: %v", err)
	}
	t.Cleanup(pool.Close)

	schemaSQL, err := os.ReadFile("../db/schema.sql")
	if err != nil {
		t.Fatalf("read schema.sql: %v", err)
	}
	if _, err := pool.Exec(ctx, string(schemaSQL)); err != nil {
		t.Fatalf("apply schema.sql: %v", err)
	}

	var userID int64
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ('tester', 'x') RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return New(pool), pool, userID
}

func newList(t *testing.T, s *Store, userID int64, title, language, script string) int64 {
	t.Helper()
	id, err := s.Create(context.Background(), userID, title, language, script)
	if err != nil {
		t.Fatalf("create list %q: %v", title, err)
	}
	return id
}

func mustAdd(t *testing.T, s *Store, listID int64, phrase, grammar, transcription string) int {
	t.Helper()
	pos, err := s.AddItem(context.Background(), listID, phrase, grammar, transcription)
	if err != nil {
		t.Fatalf("add %q: %v", phrase, err)
	}
	return pos
}

func count(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func mustTranslate(t *testing.T, s *Store, listID int64, locale string, pos int, translation, notes string) {
	t.Helper()
	err := s.SetTranslations(context.Background(), listID, locale, []ItemTranslation{{Position: pos, Translation: translation, Notes: notes}}, pos+1)
	if err != nil {
		t.Fatalf("set translation: %v", err)
	}
}

func TestSamePhraseInTwoListsIsStoredOnce(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")

	posA := mustAdd(t, s, a, "كتاب", "n", "kitab")
	posB := mustAdd(t, s, b, "كتاب", "n", "kitab")

	if n := count(t, pool, "phrases"); n != 1 {
		t.Fatalf("phrases = %d, want 1", n)
	}
	if n := count(t, pool, "vocabulary_items"); n != 2 {
		t.Fatalf("vocabulary_items = %d, want 2", n)
	}

	// A translation made through list A is already there for list B, so no
	// translation job is needed for B.
	mustTranslate(t, s, a, "en", posA, "book", "noun")
	got, err := s.Translations(ctx, b, "en")
	if err != nil {
		t.Fatal(err)
	}
	if got[posB].Translation != "book" || got[posB].Notes != "noun" {
		t.Fatalf("list B translation = %+v, want book/noun", got[posB])
	}
}

func TestDifferentFieldsOrLanguageAreDifferentPhrases(t *testing.T) {
	s, pool, uid := newTestStore(t)
	a := newList(t, s, uid, "A", "arb", "arab")
	zh := newList(t, s, uid, "ZH", "cmn", "hans")

	mustAdd(t, s, a, "كتاب", "n", "kitab")
	mustAdd(t, s, a, "كتاب", "", "kitab")   // different grammar
	mustAdd(t, s, a, "كتاب", "n", "")       // different transcription
	mustAdd(t, s, zh, "كتاب", "n", "kitab") // different language and script

	if n := count(t, pool, "phrases"); n != 4 {
		t.Fatalf("phrases = %d, want 4", n)
	}
}

func TestPhraseIsDeletedWithItsLastLink(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	posA := mustAdd(t, s, a, "كتاب", "", "")
	posB := mustAdd(t, s, b, "كتاب", "", "")
	mustTranslate(t, s, a, "en", posA, "book", "")

	if err := s.DeleteItem(ctx, a, posA); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, "phrases"); n != 1 {
		t.Fatalf("phrases after first unlink = %d, want 1 (still used by B)", n)
	}
	if got, _ := s.Translations(ctx, b, "en"); got[posB].Translation != "book" {
		t.Fatalf("translation lost while B still links the phrase: %+v", got[posB])
	}

	if err := s.DeleteItem(ctx, b, posB); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, "phrases"); n != 0 {
		t.Fatalf("phrases after last unlink = %d, want 0", n)
	}
	if n := count(t, pool, "phrase_translation"); n != 0 {
		t.Fatalf("phrase_translation after last unlink = %d, want 0", n)
	}
}

func TestDeletingAListRemovesPhrasesOnlyItUsed(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	mustAdd(t, s, a, "shared", "", "")
	mustAdd(t, s, a, "only-a", "", "")
	mustAdd(t, s, b, "shared", "", "")

	if err := s.Delete(ctx, a); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, "phrases"); n != 1 {
		t.Fatalf("phrases after deleting list A = %d, want 1", n)
	}
}

func TestDeleteItemRenumbersAndKeepsTranslationsWithTheirItem(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	mustAdd(t, s, a, "one", "", "")
	mustAdd(t, s, a, "two", "", "")
	p3 := mustAdd(t, s, a, "three", "", "")
	mustTranslate(t, s, a, "en", p3, "3", "")

	if err := s.DeleteItem(ctx, a, 0); err != nil {
		t.Fatal(err)
	}
	items, err := s.Items(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Phrase != "two" || items[1].Phrase != "three" {
		t.Fatalf("items after delete = %+v", items)
	}
	got, _ := s.Translations(ctx, a, "en")
	if got[1].Translation != "3" || len(got) != 1 {
		t.Fatalf("translations after delete = %+v, want only position 1 = 3", got)
	}
	if err := s.DeleteItem(ctx, a, 5); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("delete missing position err = %v, want ErrItemNotFound", err)
	}
}

func TestUpdateItemChangesEveryListThatLinksThePhrase(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	posA := mustAdd(t, s, a, "كتب", "", "")
	posB := mustAdd(t, s, b, "كتب", "", "")

	if err := s.UpdateItem(ctx, a, posA, "كتاب", "n", "kitab"); err != nil {
		t.Fatal(err)
	}
	items, _ := s.Items(ctx, b)
	if items[posB].Phrase != "كتاب" || items[posB].Grammar != "n" || items[posB].Transcription != "kitab" {
		t.Fatalf("list B item = %+v, want it changed along with A", items[posB])
	}
	if n := count(t, pool, "phrases"); n != 1 {
		t.Fatalf("phrases = %d, want 1", n)
	}
	if err := s.UpdateItem(ctx, a, 9, "x", "", ""); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("update missing position err = %v, want ErrItemNotFound", err)
	}
}

func TestUpdateItemMergesIntoAnExistingIdenticalPhrase(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	typo := mustAdd(t, s, a, "كتب", "", "")
	good := mustAdd(t, s, b, "كتاب", "", "")
	mustTranslate(t, s, a, "en", typo, "write", "")     // only the typo has en
	mustTranslate(t, s, a, "pl", typo, "pisac", "")     // both have pl, survivor wins
	mustTranslate(t, s, b, "pl", good, "ksiazka", "n.") // survivor's pl

	if err := s.UpdateItem(ctx, a, typo, "كتاب", "", ""); err != nil {
		t.Fatal(err)
	}

	if n := count(t, pool, "phrases"); n != 1 {
		t.Fatalf("phrases = %d, want 1 after merge", n)
	}
	for _, listID := range []int64{a, b} {
		items, _ := s.Items(ctx, listID)
		if len(items) != 1 || items[0].Phrase != "كتاب" {
			t.Fatalf("list %d items = %+v", listID, items)
		}
	}
	en, _ := s.Translations(ctx, a, "en")
	if en[typo].Translation != "write" {
		t.Fatalf("en translation not carried over by the merge: %+v", en)
	}
	pl, _ := s.Translations(ctx, a, "pl")
	if pl[typo].Translation != "ksiazka" || pl[typo].Notes != "n." {
		t.Fatalf("pl = %+v, want the surviving phrase's own translation", pl[typo])
	}
}

func TestSetItemTranscriptionAndGrammarIfBlank(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	posA := mustAdd(t, s, a, "كتاب", "", "")
	posB := mustAdd(t, s, b, "كتاب", "", "")

	applied, err := s.SetItemTranscriptionIfBlank(ctx, a, posA, "كتاب", "kitab")
	if err != nil || !applied {
		t.Fatalf("first transcription write applied=%v err=%v", applied, err)
	}
	if applied, err = s.SetItemTranscriptionIfBlank(ctx, a, posA, "كتاب", "other"); err != nil || applied {
		t.Fatalf("second transcription write applied=%v err=%v, want not applied", applied, err)
	}
	if _, err = s.SetItemTranscriptionIfBlank(ctx, a, posA, "stale", "x"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("stale phrase err = %v, want ErrItemNotFound", err)
	}
	if applied, err = s.SetItemGrammarIfBlank(ctx, b, posB, "كتاب", "n"); err != nil || !applied {
		t.Fatalf("grammar write applied=%v err=%v", applied, err)
	}

	items, _ := s.Items(ctx, a)
	if items[posA].Transcription != "kitab" || items[posA].Grammar != "n" {
		t.Fatalf("list A item = %+v, want fields written through B too (shared phrase)", items[posA])
	}
}

func TestSetItemTranslationIfAbsent(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	posA := mustAdd(t, s, a, "كتاب", "", "")
	posB := mustAdd(t, s, b, "كتاب", "", "")

	applied, err := s.SetItemTranslationIfAbsent(ctx, a, posA, "كتاب", "en", "book", "")
	if err != nil || !applied {
		t.Fatalf("first write applied=%v err=%v", applied, err)
	}
	// B shares the phrase, so its translation already exists: no overwrite.
	if applied, err = s.SetItemTranslationIfAbsent(ctx, b, posB, "كتاب", "en", "tome", ""); err != nil || applied {
		t.Fatalf("second write applied=%v err=%v, want not applied", applied, err)
	}
	if _, err = s.SetItemTranslationIfAbsent(ctx, a, posA, "stale", "pl", "x", ""); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("stale phrase err = %v, want ErrItemNotFound", err)
	}
	if got, _ := s.Translations(ctx, b, "en"); got[posB].Translation != "book" {
		t.Fatalf("translation = %+v, want book", got[posB])
	}
}

func TestTranslatedLocales(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	posA := mustAdd(t, s, a, "كتاب", "", "")
	posB := mustAdd(t, s, b, "كتاب", "", "")
	mustTranslate(t, s, a, "en", posA, "book", "")
	mustTranslate(t, s, a, "pl", posA, "", "only notes") // blank translation doesn't count

	got, err := s.TranslatedLocales(ctx, b, posB)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got["en"] {
		t.Fatalf("TranslatedLocales = %v, want only en", got)
	}
}

func syncItems(t *testing.T, s *Store, listID int64, allowEdit bool, items ...SyncItem) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds
	if err := s.SyncItemsTx(ctx, tx, listID, items, allowEdit); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func phraseIDs(t *testing.T, s *Store, listID int64) []int64 {
	t.Helper()
	items, err := s.Items(context.Background(), listID)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.PhraseID
	}
	return ids
}

func TestSyncEditsASharedPhraseByIDEverywhereAndOverwritesTranslations(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	posA := mustAdd(t, s, a, "كتب", "", "")
	posB := mustAdd(t, s, b, "كتب", "", "")
	mustTranslate(t, s, a, "en", posA, "write", "")
	id := phraseIDs(t, s, a)[0]

	syncItems(t, s, a, true, SyncItem{PhraseID: id, Phrase: "كتاب", Grammar: "n", Transcription: "kitab",
		Translations: map[string]SyncTranslation{"en": {Translation: "book", Notes: "fixed"}}})

	items, _ := s.Items(ctx, b)
	if items[posB].Phrase != "كتاب" || items[posB].Grammar != "n" || items[posB].Transcription != "kitab" || items[posB].PhraseID != id {
		t.Fatalf("list B item = %+v, want the same phrase id with every field fixed", items[posB])
	}
	en, _ := s.Translations(ctx, b, "en")
	if en[posB].Translation != "book" || en[posB].Notes != "fixed" {
		t.Fatalf("en in B = %+v, want overwritten", en[posB])
	}
	if n := count(t, pool, "phrases"); n != 1 {
		t.Fatalf("phrases = %d, want 1 (edited in place, not recreated)", n)
	}
}

func TestSyncReorderKeepsPhrasesAndTheirTranslations(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	mustAdd(t, s, a, "one", "", "")
	p2 := mustAdd(t, s, a, "two", "", "")
	mustAdd(t, s, a, "three", "", "")
	mustTranslate(t, s, a, "en", p2, "2", "")
	ids := phraseIDs(t, s, a)

	syncItems(t, s, a, true,
		SyncItem{PhraseID: ids[2], Phrase: "three"},
		SyncItem{PhraseID: ids[0], Phrase: "one"},
		SyncItem{PhraseID: ids[1], Phrase: "two"})

	items, _ := s.Items(ctx, a)
	if items[0].Phrase != "three" || items[1].Phrase != "one" || items[2].Phrase != "two" {
		t.Fatalf("order = %+v", items)
	}
	if got := phraseIDs(t, s, a); got[0] != ids[2] || got[1] != ids[0] || got[2] != ids[1] {
		t.Fatalf("phrase ids = %v, want the same phrases reordered (was %v)", got, ids)
	}
	if en, _ := s.Translations(ctx, a, "en"); en[2].Translation != "2" || len(en) != 1 {
		t.Fatalf("translations = %+v, want 2 still on 'two' (now position 2)", en)
	}
	if n := count(t, pool, "phrases"); n != 3 {
		t.Fatalf("phrases = %d, want 3", n)
	}
}

func TestSyncNewLineLinksAnExistingTranslatedPhraseWithoutRetranslating(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	posA := mustAdd(t, s, a, "كتاب", "n", "kitab")
	mustTranslate(t, s, a, "en", posA, "book", "")

	syncItems(t, s, b, true, SyncItem{Phrase: "كتاب", Grammar: "n", Transcription: "kitab"}) // no id, no translations

	if n := count(t, pool, "phrases"); n != 1 {
		t.Fatalf("phrases = %d, want 1", n)
	}
	if got, _ := s.TranslatedLocales(ctx, b, 0); !got["en"] {
		t.Fatalf("TranslatedLocales = %v, want en", got)
	}
}

func TestSyncRemovesUnmentionedLinksButKeepsPhrasesOthersUse(t *testing.T) {
	s, pool, uid := newTestStore(t)
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	mustAdd(t, s, a, "shared", "", "")
	mustAdd(t, s, a, "only-a", "", "")
	mustAdd(t, s, a, "keep", "", "")
	mustAdd(t, s, b, "shared", "", "")
	ids := phraseIDs(t, s, a)

	syncItems(t, s, a, true, SyncItem{PhraseID: ids[2], Phrase: "keep"})

	if got := phraseIDs(t, s, a); len(got) != 1 || got[0] != ids[2] {
		t.Fatalf("list A = %v, want only 'keep'", got)
	}
	if n := count(t, pool, "phrases"); n != 2 { // keep + shared (still in B); only-a is gone
		t.Fatalf("phrases = %d, want 2", n)
	}
}

func TestSyncIgnoresAnIDTheListDoesNotLinkAndNeverEditsAnotherListsPhrase(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	mustAdd(t, s, a, "mine", "", "")
	posB := mustAdd(t, s, b, "theirs", "", "")
	foreign := phraseIDs(t, s, b)[0]

	syncItems(t, s, a, true, SyncItem{PhraseID: foreign, Phrase: "hijack"})

	items, _ := s.Items(ctx, b)
	if items[posB].Phrase != "theirs" {
		t.Fatalf("list B item = %+v, want untouched", items[posB])
	}
	if got, _ := s.Items(ctx, a); len(got) != 1 || got[0].Phrase != "hijack" || got[0].PhraseID == foreign {
		t.Fatalf("list A = %+v, want a new phrase 'hijack'", got)
	}
}

func TestSyncWithEditsNotAllowedLeavesExistingPhrasesAlone(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	mustAdd(t, s, a, "old", "", "")
	posB := mustAdd(t, s, b, "old", "", "")
	id := phraseIDs(t, s, a)[0]

	syncItems(t, s, a, false, SyncItem{PhraseID: id, Phrase: "new"})

	if items, _ := s.Items(ctx, b); items[posB].Phrase != "old" {
		t.Fatalf("list B = %+v, want 'old' untouched", items[posB])
	}
	if items, _ := s.Items(ctx, a); items[0].Phrase != "new" || items[0].PhraseID == id {
		t.Fatalf("list A = %+v, want a new phrase 'new'", items[0])
	}
}

func TestSyncTranslationsPresentBlankClearsAbsentIsLeftAlone(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	pos := mustAdd(t, s, a, "كتاب", "", "")
	mustTranslate(t, s, a, "en", pos, "book", "")
	mustTranslate(t, s, a, "pl", pos, "ksiazka", "")
	id := phraseIDs(t, s, a)[0]

	syncItems(t, s, a, true, SyncItem{PhraseID: id, Phrase: "كتاب", Translations: map[string]SyncTranslation{"en": {}}})

	if en, _ := s.Translations(ctx, a, "en"); len(en) != 0 {
		t.Fatalf("en = %+v, want cleared", en)
	}
	if pl, _ := s.Translations(ctx, a, "pl"); pl[0].Translation != "ksiazka" {
		t.Fatalf("pl = %+v, want left alone", pl)
	}
}

func TestSyncSameIDTwiceWithDifferentFieldsMakesTheSecondLineNew(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	mustAdd(t, s, a, "كتاب", "", "")
	id := phraseIDs(t, s, a)[0]

	syncItems(t, s, a, true,
		SyncItem{PhraseID: id, Phrase: "first"},
		SyncItem{PhraseID: id, Phrase: "second"})

	items, _ := s.Items(ctx, a)
	if len(items) != 2 || items[0].Phrase != "first" || items[1].Phrase != "second" || items[0].PhraseID == items[1].PhraseID {
		t.Fatalf("items = %+v, want two different phrases", items)
	}
}

func TestSyncEditCollidingWithAnExistingPhraseMerges(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	posA := mustAdd(t, s, a, "كتب", "", "")
	mustAdd(t, s, b, "كتاب", "", "")
	mustTranslate(t, s, a, "en", posA, "write", "")
	idTypo := phraseIDs(t, s, a)[0]

	syncItems(t, s, a, true, SyncItem{PhraseID: idTypo, Phrase: "كتاب"})

	if n := count(t, pool, "phrases"); n != 1 {
		t.Fatalf("phrases = %d, want 1 after merge", n)
	}
	if ga, gb := phraseIDs(t, s, a), phraseIDs(t, s, b); ga[0] != gb[0] {
		t.Fatalf("A links %v, B links %v, want the same surviving phrase", ga, gb)
	}
	if en, _ := s.Translations(ctx, a, "en"); en[0].Translation != "write" {
		t.Fatalf("en = %+v, want carried over by the merge", en)
	}
}

// TestRegeneratingAListKeepsTheTranslationsOfPhrasesGeneratedAgain is what
// generate-from-text relies on: its items carry no phrase id, and a phrase
// that comes out of the generation again must keep its translations — so it
// is never submitted for translation twice.
func TestRegeneratingAListKeepsTheTranslationsOfPhrasesGeneratedAgain(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	pos := mustAdd(t, s, a, "كتاب", "n", "kitab")
	mustAdd(t, s, a, "gone", "", "")
	mustTranslate(t, s, a, "en", pos, "book", "")

	syncItems(t, s, a, false,
		SyncItem{Phrase: "كتاب", Grammar: "n", Transcription: "kitab"},
		SyncItem{Phrase: "new", Grammar: "", Transcription: ""})

	if got, _ := s.TranslatedLocales(ctx, a, 0); !got["en"] {
		t.Fatalf("TranslatedLocales = %v, want en kept for the phrase generated again", got)
	}
	if got, _ := s.TranslatedLocales(ctx, a, 1); len(got) != 0 {
		t.Fatalf("TranslatedLocales for the new phrase = %v, want none", got)
	}
	if n := count(t, pool, "phrases"); n != 2 { // 'gone' went with its link
		t.Fatalf("phrases = %d, want 2", n)
	}
}

// The cross-line cases: once a line has resolved to a phrase, a later line
// must not rewrite it or merge it away.
func TestSyncLaterLineNeverRewritesAPhraseAnEarlierLineLinked(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	mustAdd(t, s, a, "b", "", "")
	idB := phraseIDs(t, s, a)[0]

	syncItems(t, s, a, true,
		SyncItem{Phrase: "b"},                // new line that links the existing phrase B
		SyncItem{PhraseID: idB, Phrase: "c"}) // edit of B — must not change line 0

	items, _ := s.Items(ctx, a)
	if len(items) != 2 || items[0].Phrase != "b" || items[1].Phrase != "c" || items[0].PhraseID == items[1].PhraseID {
		t.Fatalf("items = %+v, want b then c as two different phrases", items)
	}
}

func TestSyncSwappingTwoLinesTextsKeepsBothTexts(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	mustAdd(t, s, a, "a", "", "")
	mustAdd(t, s, a, "b", "", "")
	ids := phraseIDs(t, s, a)

	syncItems(t, s, a, true,
		SyncItem{PhraseID: ids[0], Phrase: "b"}, // A's line now says what B's did: merges into B
		SyncItem{PhraseID: ids[1], Phrase: "a"}) // B is claimed, so this is a new line, not a rename of B

	items, _ := s.Items(ctx, a)
	if len(items) != 2 || items[0].Phrase != "b" || items[1].Phrase != "a" {
		t.Fatalf("items = %+v, want b then a", items)
	}
}

func TestSyncMergeNeverDeletesAPhraseAnEarlierLineUses(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	mustAdd(t, s, a, "b", "", "")
	mustAdd(t, s, a, "c", "", "")
	ids := phraseIDs(t, s, a)

	// Line 0 links B. Line 1 would edit B into "c", which exists (C) — a merge
	// that would delete B out from under line 0.
	syncItems(t, s, a, true,
		SyncItem{Phrase: "b"},
		SyncItem{PhraseID: ids[0], Phrase: "c"})

	items, _ := s.Items(ctx, a)
	if len(items) != 2 || items[0].Phrase != "b" || items[1].Phrase != "c" {
		t.Fatalf("items = %+v, want b then c", items)
	}
	if n := count(t, pool, "phrases"); n != 2 {
		t.Fatalf("phrases = %d, want 2", n)
	}
}

func listPhraseLanguages(t *testing.T, pool *pgxpool.Pool, listID int64) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT p.language || '/' || p.script FROM vocabulary_items i JOIN phrases p ON p.id = i.phrase_id WHERE i.list_id = $1 ORDER BY i.position`, listID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatal(err)
		}
		out = append(out, l)
	}
	return out
}

func TestChangingAListsLanguageRelinksItsItemsToThePhrasesOfTheNewLanguage(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	px := mustAdd(t, s, a, "x", "n", "ex")
	mustAdd(t, s, a, "y", "", "")
	posBx := mustAdd(t, s, b, "x", "n", "ex")
	mustTranslate(t, s, a, "en", px, "ecks", "note")

	if err := s.UpdateMeta(ctx, a, "A moved", "cmn", "hans"); err != nil {
		t.Fatal(err)
	}

	for _, l := range listPhraseLanguages(t, pool, a) {
		if l != "cmn/hans" {
			t.Fatalf("list A links a %s phrase, want only cmn/hans", l)
		}
	}
	if got, _ := s.Translations(ctx, a, "en"); got[px].Translation != "ecks" || got[px].Notes != "note" {
		t.Fatalf("translation after the move = %+v, want it copied to the new phrase", got[px])
	}
	// B still has the arb phrase, with its translation; the arb 'y' nobody uses is gone.
	if got := listPhraseLanguages(t, pool, b); len(got) != 1 || got[0] != "arb/arab" {
		t.Fatalf("list B links %v, want its arb phrase untouched", got)
	}
	if got, _ := s.Translations(ctx, b, "en"); got[posBx].Translation != "ecks" {
		t.Fatalf("list B translation = %+v, want it kept", got[posBx])
	}
	if n := count(t, pool, "phrases"); n != 3 { // arb x (B), cmn x, cmn y
		t.Fatalf("phrases = %d, want 3", n)
	}

	// Adding the same text again now links the existing new-language phrase.
	mustAdd(t, s, a, "x", "n", "ex")
	if n := count(t, pool, "phrases"); n != 3 {
		t.Fatalf("phrases after adding the same text = %d, want 3 (no duplicate)", n)
	}

	// An update that leaves language and script alone does not touch the items, and a missing list is no error.
	if err := s.UpdateMeta(ctx, a, "renamed", "cmn", "hans"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMeta(ctx, 999999, "x", "arb", "arab"); err != nil {
		t.Fatalf("UpdateMeta of a missing list err = %v, want nil as before", err)
	}
}

func TestLookalikeTextIsTheSamePhraseOnEveryPath(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")

	// Fatha and shadda in either order, stray spaces around every field.
	mustAdd(t, s, a, "سَّ", " n ", " sa ")
	mustAdd(t, s, b, "  سَّ\t", "n", "sa")
	if n := count(t, pool, "phrases"); n != 1 {
		t.Fatalf("phrases after two lookalike adds = %d, want 1", n)
	}

	// An edit that makes a phrase a lookalike of another one merges them.
	mustAdd(t, s, a, "other", "", "")
	if err := s.UpdateItem(ctx, a, 1, " سَّ ", "n", "sa"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, "phrases"); n != 1 {
		t.Fatalf("phrases after editing into a lookalike = %d, want 1", n)
	}

	// A sync line that is a lookalike links the same phrase.
	syncItems(t, s, b, true, SyncItem{Phrase: "سَّ ", Grammar: "n", Transcription: "sa"})
	if n := count(t, pool, "phrases"); n != 1 {
		t.Fatalf("phrases after a lookalike sync line = %d, want 1", n)
	}
	if items, _ := s.Items(ctx, b); items[0].Phrase != "سَّ" {
		t.Fatalf("stored phrase = %+q, want the normalised form", items[0].Phrase)
	}
}

func TestABlankPhraseIsRejected(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	if _, err := s.AddItem(ctx, a, " \t ", "", ""); !errors.Is(err, ErrBlankPhrase) {
		t.Fatalf("AddItem blank err = %v, want ErrBlankPhrase", err)
	}
	pos := mustAdd(t, s, a, "ok", "", "")
	if err := s.UpdateItem(ctx, a, pos, "  ", "", ""); !errors.Is(err, ErrBlankPhrase) {
		t.Fatalf("UpdateItem blank err = %v, want ErrBlankPhrase", err)
	}
	tx, _ := s.Begin(ctx)
	defer tx.Rollback(ctx) //nolint:errcheck // test cleanup
	if err := s.SyncItemsTx(ctx, tx, a, []SyncItem{{Phrase: "  "}}, true); !errors.Is(err, ErrBlankPhrase) {
		t.Fatalf("SyncItemsTx blank err = %v, want ErrBlankPhrase", err)
	}
}

func TestSchemaNormalisesPhrasesStoredBeforeTheRuleAndNeverFailsOnACollision(t *testing.T) {
	_, pool, _ := newTestStore(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO phrases (language, script, phrase, grammar, transcription) VALUES
		('arb','arab', 'x ', ' n', ''),
		('arb','arab', E'é', '', ''),
		('arb','arab', 'c', '', ''),
		('arb','arab', 'c ', '', '')`); err != nil {
		t.Fatal(err)
	}
	schemaSQL, err := os.ReadFile("../db/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // twice: safe to rerun
		if _, err := pool.Exec(ctx, string(schemaSQL)); err != nil {
			t.Fatalf("apply schema.sql (run %d): %v", i+1, err)
		}
	}
	var got []string
	rows, err := pool.Query(ctx, `SELECT phrase || '|' || grammar FROM phrases ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	want := []string{"x|n", "é|", "c|", "c |"} // the colliding 'c ' is left as it is
	if len(got) != len(want) {
		t.Fatalf("rows = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows = %q, want %q", got, want)
		}
	}
}
