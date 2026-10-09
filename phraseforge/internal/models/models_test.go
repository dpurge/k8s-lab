package models

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
// database is touched. Without the variable they are skipped. They mirror
// vocabulary's tests (see internal/vocabulary/vocabulary_test.go).
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
	schema := fmt.Sprintf("pf_test_models_%d_%d", os.Getpid(), schemaCounter.Add(1))
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

func mustAdd(t *testing.T, s *Store, listID int64, phrase, transcription string) int {
	t.Helper()
	pos, err := s.AddItem(context.Background(), listID, phrase, transcription)
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

func mustTranslate(t *testing.T, s *Store, listID int64, locale string, pos int, translation string) {
	t.Helper()
	if err := s.SetTranslation(context.Background(), listID, pos, locale, translation, pos+1); err != nil {
		t.Fatalf("set translation: %v", err)
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

func TestSamePhraseInTwoListsIsStoredOnce(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	posA := mustAdd(t, s, a, "كيف حالك", "kayfa haluk")
	posB := mustAdd(t, s, b, "كيف حالك", "kayfa haluk")

	if n := count(t, pool, "models_phrases"); n != 1 {
		t.Fatalf("models_phrases = %d, want 1", n)
	}
	mustTranslate(t, s, a, "en", posA, "how are you")
	if got, _ := s.Translations(ctx, b, "en"); got[posB].Translation != "how are you" {
		t.Fatalf("list B translation = %+v, want it shared", got[posB])
	}
	if got, _ := s.TranslatedLocales(ctx, b, posB); len(got) != 1 || !got["en"] {
		t.Fatalf("TranslatedLocales = %v, want only en", got)
	}
}

func TestDifferentTranscriptionOrLanguageAreDifferentPhrases(t *testing.T) {
	s, pool, uid := newTestStore(t)
	a := newList(t, s, uid, "A", "arb", "arab")
	zh := newList(t, s, uid, "ZH", "cmn", "hans")
	mustAdd(t, s, a, "كيف", "kayfa")
	mustAdd(t, s, a, "كيف", "")
	mustAdd(t, s, zh, "كيف", "kayfa")
	if n := count(t, pool, "models_phrases"); n != 3 {
		t.Fatalf("models_phrases = %d, want 3", n)
	}
}

func TestPhraseIsDeletedWithItsLastLinkOrListAndItemsRenumber(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	mustAdd(t, s, a, "one", "")
	p2 := mustAdd(t, s, a, "two", "")
	mustAdd(t, s, a, "three", "")
	mustAdd(t, s, b, "two", "")
	mustTranslate(t, s, a, "en", p2, "2")

	if err := s.DeleteItem(ctx, a, 0); err != nil {
		t.Fatal(err)
	}
	items, _ := s.Items(ctx, a)
	if len(items) != 2 || items[0].Phrase != "two" || items[1].Phrase != "three" {
		t.Fatalf("items after delete = %+v", items)
	}
	if got, _ := s.Translations(ctx, a, "en"); got[0].Translation != "2" || len(got) != 1 {
		t.Fatalf("translations = %+v, want 2 on 'two' (now position 0)", got)
	}
	if n := count(t, pool, "models_phrases"); n != 2 { // 'one' went with its link
		t.Fatalf("models_phrases = %d, want 2", n)
	}
	if err := s.DeleteItem(ctx, a, 9); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("delete missing position err = %v, want ErrItemNotFound", err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM models_lists WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, "models_phrases"); n != 1 { // only 'two', still linked by B
		t.Fatalf("models_phrases after deleting list A = %d, want 1", n)
	}
}

func TestUpdateItemIsGlobalAndMergesOnCollision(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	typo := mustAdd(t, s, a, "كيف", "")
	posB := mustAdd(t, s, b, "كيف", "")
	good := mustAdd(t, s, b, "كيفا", "kayfa")
	mustTranslate(t, s, a, "en", typo, "how")
	mustTranslate(t, s, b, "pl", good, "jak")

	// The typo phrase is shared by A's item and B's first item, so the global
	// edit moves both onto the phrase it collides with (B's second item).
	if err := s.UpdateItem(ctx, a, typo, "كيفا", "kayfa"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, "models_phrases"); n != 1 {
		t.Fatalf("models_phrases = %d, want 1 after the merge", n)
	}
	if items, _ := s.Items(ctx, b); items[posB].Phrase != "كيفا" || items[posB].Transcription != "kayfa" {
		t.Fatalf("list B's first item = %+v, want it merged along with A's", items[posB])
	}
	en, _ := s.Translations(ctx, a, "en")
	pl, _ := s.Translations(ctx, a, "pl")
	if en[0].Translation != "how" || pl[0].Translation != "jak" {
		t.Fatalf("translations after merge en=%+v pl=%+v, want both kept", en, pl)
	}
	if err := s.UpdateItem(ctx, a, 9, "x", ""); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("update missing position err = %v, want ErrItemNotFound", err)
	}
}

func TestSetItemTranscriptionAndTranslationIfBlank(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	posA := mustAdd(t, s, a, "كيف", "")
	posB := mustAdd(t, s, b, "كيف", "")

	if applied, err := s.SetItemTranscriptionIfBlank(ctx, a, posA, "كيف", "kayfa"); err != nil || !applied {
		t.Fatalf("first transcription write applied=%v err=%v", applied, err)
	}
	if applied, err := s.SetItemTranscriptionIfBlank(ctx, a, posA, "كيف", "other"); err != nil || applied {
		t.Fatalf("second transcription write applied=%v err=%v, want not applied", applied, err)
	}
	if _, err := s.SetItemTranscriptionIfBlank(ctx, a, posA, "stale", "x"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("stale phrase err = %v, want ErrItemNotFound", err)
	}
	if items, _ := s.Items(ctx, b); items[posB].Transcription != "kayfa" {
		t.Fatalf("list B item = %+v, want the shared phrase's transcription", items[posB])
	}

	if applied, err := s.SetItemTranslationIfAbsent(ctx, a, posA, "كيف", "en", "how"); err != nil || !applied {
		t.Fatalf("first translation write applied=%v err=%v", applied, err)
	}
	if applied, err := s.SetItemTranslationIfAbsent(ctx, b, posB, "كيف", "en", "tome"); err != nil || applied {
		t.Fatalf("second translation write applied=%v err=%v, want not applied (shared)", applied, err)
	}
	if _, err := s.SetItemTranslationIfAbsent(ctx, a, posA, "stale", "pl", "x"); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("stale phrase err = %v, want ErrItemNotFound", err)
	}
}

func TestSyncEditsASharedPhraseByIDEverywhereAndOverwritesTranslations(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	posA := mustAdd(t, s, a, "كيف", "")
	posB := mustAdd(t, s, b, "كيف", "")
	mustTranslate(t, s, a, "en", posA, "old")
	id := phraseIDs(t, s, a)[0]

	syncItems(t, s, a, true, SyncItem{PhraseID: id, Phrase: "كيف حالك", Transcription: "kayfa haluk",
		Translations: map[string]string{"en": "how are you"}})

	items, _ := s.Items(ctx, b)
	if items[posB].Phrase != "كيف حالك" || items[posB].Transcription != "kayfa haluk" || items[posB].PhraseID != id {
		t.Fatalf("list B item = %+v, want the same phrase id with every field fixed", items[posB])
	}
	if en, _ := s.Translations(ctx, b, "en"); en[posB].Translation != "how are you" {
		t.Fatalf("en in B = %+v, want overwritten", en[posB])
	}
	if n := count(t, pool, "models_phrases"); n != 1 {
		t.Fatalf("models_phrases = %d, want 1 (edited in place)", n)
	}
}

func TestSyncReorderRemoveAndNewLines(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	mustAdd(t, s, a, "one", "")
	p2 := mustAdd(t, s, a, "two", "")
	mustAdd(t, s, a, "three", "")
	mustAdd(t, s, a, "only-a", "")
	mustAdd(t, s, b, "three", "")
	mustTranslate(t, s, a, "en", p2, "2")
	ids := phraseIDs(t, s, a)

	// reorder two, drop 'one' and 'only-a', and add a new line that already exists elsewhere
	syncItems(t, s, a, true,
		SyncItem{PhraseID: ids[2], Phrase: "three"},
		SyncItem{PhraseID: ids[1], Phrase: "two"},
		SyncItem{Phrase: "three"}) // no id: links the existing 'three' again, a second link

	items, _ := s.Items(ctx, a)
	if len(items) != 3 || items[0].Phrase != "three" || items[1].Phrase != "two" || items[2].Phrase != "three" {
		t.Fatalf("items = %+v", items)
	}
	if got := phraseIDs(t, s, a); got[0] != ids[2] || got[1] != ids[1] || got[2] != ids[2] {
		t.Fatalf("phrase ids = %v, want 'three' and 'two' kept (was %v)", got, ids)
	}
	if en, _ := s.Translations(ctx, a, "en"); en[1].Translation != "2" || len(en) != 1 {
		t.Fatalf("translations = %+v, want 2 still on 'two'", en)
	}
	if n := count(t, pool, "models_phrases"); n != 2 { // 'one' and 'only-a' went with their links
		t.Fatalf("models_phrases = %d, want 2", n)
	}
}

func TestSyncGuards(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	mustAdd(t, s, a, "mine", "")
	posB := mustAdd(t, s, b, "theirs", "")
	foreign := phraseIDs(t, s, b)[0]

	// an id the list doesn't link makes a new line, never an edit of B's phrase
	syncItems(t, s, a, true, SyncItem{PhraseID: foreign, Phrase: "hijack"})
	if items, _ := s.Items(ctx, b); items[posB].Phrase != "theirs" {
		t.Fatalf("list B item = %+v, want untouched", items[posB])
	}
	if items, _ := s.Items(ctx, a); len(items) != 1 || items[0].Phrase != "hijack" || items[0].PhraseID == foreign {
		t.Fatalf("list A = %+v, want a new phrase 'hijack'", items)
	}

	// edits not allowed: the existing phrase is left alone, the line is new
	id := phraseIDs(t, s, a)[0]
	syncItems(t, s, a, false, SyncItem{PhraseID: id, Phrase: "renamed"})
	if items, _ := s.Items(ctx, a); items[0].Phrase != "renamed" || items[0].PhraseID == id {
		t.Fatalf("list A = %+v, want a new phrase when edits aren't allowed", items)
	}

	// the same id twice with different fields: the second line is new
	id = phraseIDs(t, s, a)[0]
	syncItems(t, s, a, true, SyncItem{PhraseID: id, Phrase: "first"}, SyncItem{PhraseID: id, Phrase: "second"})
	items, _ := s.Items(ctx, a)
	if len(items) != 2 || items[0].Phrase != "first" || items[1].Phrase != "second" || items[0].PhraseID == items[1].PhraseID {
		t.Fatalf("items = %+v, want two different phrases", items)
	}
}

func TestSyncTranslationsPresentBlankClearsAbsentIsLeftAlone(t *testing.T) {
	s, _, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	pos := mustAdd(t, s, a, "كيف", "")
	mustTranslate(t, s, a, "en", pos, "how")
	mustTranslate(t, s, a, "pl", pos, "jak")
	id := phraseIDs(t, s, a)[0]

	syncItems(t, s, a, true, SyncItem{PhraseID: id, Phrase: "كيف", Translations: map[string]string{"en": ""}})

	if en, _ := s.Translations(ctx, a, "en"); len(en) != 0 {
		t.Fatalf("en = %+v, want cleared", en)
	}
	if pl, _ := s.Translations(ctx, a, "pl"); pl[0].Translation != "jak" {
		t.Fatalf("pl = %+v, want left alone", pl)
	}
}

// TestRegeneratingAListKeepsTheTranslationsOfPhrasesGeneratedAgain mirrors
// vocabulary's test of the same name.
func TestRegeneratingAListKeepsTheTranslationsOfPhrasesGeneratedAgain(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	pos := mustAdd(t, s, a, "كيف", "kayfa")
	mustAdd(t, s, a, "gone", "")
	mustTranslate(t, s, a, "en", pos, "how")

	syncItems(t, s, a, false, SyncItem{Phrase: "كيف", Transcription: "kayfa"}, SyncItem{Phrase: "new"})

	if got, _ := s.TranslatedLocales(ctx, a, 0); !got["en"] {
		t.Fatalf("TranslatedLocales = %v, want en kept for the phrase generated again", got)
	}
	if n := count(t, pool, "models_phrases"); n != 2 {
		t.Fatalf("models_phrases = %d, want 2", n)
	}
}

// The cross-line cases — see vocabulary's tests of the same names.
func TestSyncCrossLineCasesNeverRewriteOrDeleteAnEarlierLinesPhrase(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()

	a := newList(t, s, uid, "A", "arb", "arab")
	mustAdd(t, s, a, "b", "")
	idB := phraseIDs(t, s, a)[0]
	syncItems(t, s, a, true, SyncItem{Phrase: "b"}, SyncItem{PhraseID: idB, Phrase: "c"})
	items, _ := s.Items(ctx, a)
	if len(items) != 2 || items[0].Phrase != "b" || items[1].Phrase != "c" || items[0].PhraseID == items[1].PhraseID {
		t.Fatalf("later edit: items = %+v, want b then c as two different phrases", items)
	}

	sw := newList(t, s, uid, "SW", "arb", "arab")
	mustAdd(t, s, sw, "x", "")
	mustAdd(t, s, sw, "y", "")
	ids := phraseIDs(t, s, sw)
	syncItems(t, s, sw, true, SyncItem{PhraseID: ids[0], Phrase: "y"}, SyncItem{PhraseID: ids[1], Phrase: "x"})
	items, _ = s.Items(ctx, sw)
	if len(items) != 2 || items[0].Phrase != "y" || items[1].Phrase != "x" {
		t.Fatalf("swap: items = %+v, want y then x", items)
	}

	m := newList(t, s, uid, "M", "cmn", "hans")
	mustAdd(t, s, m, "p", "")
	mustAdd(t, s, m, "q", "")
	idP := phraseIDs(t, s, m)[0]
	syncItems(t, s, m, true, SyncItem{Phrase: "p"}, SyncItem{PhraseID: idP, Phrase: "q"})
	items, _ = s.Items(ctx, m)
	if len(items) != 2 || items[0].Phrase != "p" || items[1].Phrase != "q" {
		t.Fatalf("merge: items = %+v, want p then q", items)
	}
	if n := count(t, pool, "models_phrases"); n != 6 { // b c | y x | p q
		t.Fatalf("models_phrases = %d, want 6", n)
	}
}

func TestChangingAListsLanguageRelinksItsItemsToThePhrasesOfTheNewLanguage(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")
	px := mustAdd(t, s, a, "x", "ex")
	mustAdd(t, s, a, "y", "")
	posBx := mustAdd(t, s, b, "x", "ex")
	mustTranslate(t, s, a, "en", px, "ecks")

	if err := s.UpdateMeta(ctx, a, "A moved", "cmn", "hans"); err != nil {
		t.Fatal(err)
	}

	var wrong int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM models_items i JOIN models_phrases p ON p.id = i.phrase_id
		WHERE i.list_id = $1 AND (p.language <> 'cmn' OR p.script <> 'hans')`, a).Scan(&wrong); err != nil {
		t.Fatal(err)
	}
	if wrong != 0 {
		t.Fatalf("list A links %d phrases that are not cmn/hans", wrong)
	}
	if got, _ := s.Translations(ctx, a, "en"); got[px].Translation != "ecks" {
		t.Fatalf("translation after the move = %+v, want it copied", got[px])
	}
	if got, _ := s.Translations(ctx, b, "en"); got[posBx].Translation != "ecks" {
		t.Fatalf("list B translation = %+v, want it kept", got[posBx])
	}
	if n := count(t, pool, "models_phrases"); n != 3 { // arb x (B), cmn x, cmn y
		t.Fatalf("models_phrases = %d, want 3", n)
	}
	mustAdd(t, s, a, "x", "ex")
	if n := count(t, pool, "models_phrases"); n != 3 {
		t.Fatalf("models_phrases after adding the same text = %d, want 3 (no duplicate)", n)
	}
	if err := s.UpdateMeta(ctx, 999999, "x", "arb", "arab"); err != nil {
		t.Fatalf("UpdateMeta of a missing list err = %v, want nil as before", err)
	}
}

func TestLookalikeTextIsTheSamePhraseAndBlankIsRejected(t *testing.T) {
	s, pool, uid := newTestStore(t)
	ctx := context.Background()
	a := newList(t, s, uid, "A", "arb", "arab")
	b := newList(t, s, uid, "B", "arb", "arab")

	mustAdd(t, s, a, "سَّ", " sa ")
	mustAdd(t, s, b, "  سَّ\t", "sa")
	if n := count(t, pool, "models_phrases"); n != 1 {
		t.Fatalf("models_phrases after two lookalike adds = %d, want 1", n)
	}
	syncItems(t, s, b, true, SyncItem{Phrase: "سَّ ", Transcription: "sa"})
	if n := count(t, pool, "models_phrases"); n != 1 {
		t.Fatalf("models_phrases after a lookalike sync line = %d, want 1", n)
	}

	if _, err := s.AddItem(ctx, a, " ", ""); !errors.Is(err, ErrBlankPhrase) {
		t.Fatalf("AddItem blank err = %v, want ErrBlankPhrase", err)
	}
	if err := s.UpdateItem(ctx, a, 0, "  ", ""); !errors.Is(err, ErrBlankPhrase) {
		t.Fatalf("UpdateItem blank err = %v, want ErrBlankPhrase", err)
	}
}

func TestSchemaNormalisesModelsPhrasesStoredBeforeTheRuleAndNeverFailsOnACollision(t *testing.T) {
	_, pool, _ := newTestStore(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO models_phrases (language, script, phrase, transcription) VALUES
		('arb','arab', 'x ', ' t'),
		('arb','arab', 'c', ''),
		('arb','arab', 'c ', '')`); err != nil {
		t.Fatal(err)
	}
	schemaSQL, err := os.ReadFile("../db/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(ctx, string(schemaSQL)); err != nil {
			t.Fatalf("apply schema.sql (run %d): %v", i+1, err)
		}
	}
	var first, second string
	if err := pool.QueryRow(ctx, `SELECT phrase || '|' || transcription FROM models_phrases ORDER BY id LIMIT 1`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT phrase FROM models_phrases ORDER BY id OFFSET 2 LIMIT 1`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first != "x|t" || second != "c " {
		t.Fatalf("first = %q, third phrase = %q, want x|t and the colliding 'c ' left as it is", first, second)
	}
}
