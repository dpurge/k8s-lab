package resreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"phraseforge/internal/ai"
	"phraseforge/internal/dialogs"
	"phraseforge/internal/texts"
	"phraseforge/internal/translations"
	"phraseforge/internal/vocabulary"
)

var schemaCounter atomic.Int64

// newDB is a pool on a throwaway schema with schema.sql applied
// (PHRASEFORGE_TEST_DSN, skipped without it), and a user to own the rows.
func newDB(t *testing.T) (*pgxpool.Pool, int64) {
	t.Helper()
	dsn := os.Getenv("PHRASEFORGE_TEST_DSN")
	if dsn == "" {
		t.Skip("PHRASEFORGE_TEST_DSN not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("pf_test_%d_%d", os.Getpid(), schemaCounter.Add(1))
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); admin.Close() })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	sql, err := os.ReadFile("../db/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("apply schema.sql: %v", err)
	}
	var uid int64
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ('u', 'x') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	return pool, uid
}

type fixture struct {
	s      *Store
	pool   *pgxpool.Pool
	textID int64
	dlgID  int64
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool, uid := newDB(t)
	ctx := context.Background()
	f := fixture{s: New(pool), pool: pool}
	var err error
	if f.textID, err = texts.New(pool).Create(ctx, uid, "T", "Der Hund schläft.", "tr", "deu", "latn", ""); err != nil {
		t.Fatal(err)
	}
	if f.dlgID, err = dialogs.New(pool).Create(ctx, uid, "D", "A: Hallo.", "tr", "deu", "latn", ""); err != nil {
		t.Fatal(err)
	}
	tr := translations.New(pool)
	for _, loc := range [][2]string{{"en", "The dog sleeps."}, {"pl", "Pies śpi."}} {
		if err := tr.Set(ctx, "text", f.textID, loc[0], loc[1]); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f fixture) save(t *testing.T, resourceType string, id int64, part string, findings ...vocabulary.ReviewFinding) string {
	t.Helper()
	in, err := f.s.TextReviewSource(context.Background(), resourceType, id)
	if err != nil {
		t.Fatal(err)
	}
	for i := range findings {
		findings[i].Status = vocabulary.ReviewStatusOpen
	}
	raw, _ := json.Marshal(findings)
	if err := f.s.SaveTextReview(context.Background(), resourceType, id, part, in.Version, raw, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	return in.Version
}

func TestTextReviewSourceReadsTheResourceItsTranslationsAndTheAttachedVocabulary(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	vs := vocabulary.New(f.pool)
	list, err := vs.CreateFromText(ctx, 1, "V", "deu", "latn", f.textID)
	if err != nil {
		t.Fatal(err)
	}
	pos, _ := vs.AddItem(ctx, list, "der Hund", "N m", "")
	_, _ = vs.AddItem(ctx, list, "schlafen", "V", "")
	if err := vs.SetTranslations(ctx, list, "en", []vocabulary.ItemTranslation{{Position: pos, Translation: "dog"}}, pos+1); err != nil {
		t.Fatal(err)
	}

	in, err := f.s.TextReviewSource(ctx, "text", f.textID)
	if err != nil {
		t.Fatal(err)
	}
	if in.Language != "deu" || in.Body != "Der Hund schläft." || in.Transcription != "tr" || len(in.Version) != 64 {
		t.Errorf("source = %+v", in)
	}
	wantTr := []ai.ReviewTranslation{{Locale: "en", Translation: "The dog sleeps."}, {Locale: "pl", Translation: "Pies śpi."}}
	if !reflect.DeepEqual(in.Translations, wantTr) {
		t.Errorf("translations = %+v, want %+v", in.Translations, wantTr)
	}
	wantVocab := []ai.TextReviewVocabulary{{Phrase: "der Hund", Translations: map[string]string{"en": "dog"}}, {Phrase: "schlafen"}}
	if !reflect.DeepEqual(in.Vocabulary, wantVocab) {
		t.Errorf("vocabulary = %+v, want %+v", in.Vocabulary, wantVocab)
	}
	if d, err := f.s.TextReviewSource(ctx, "dialog", f.dlgID); err != nil || d.Body != "A: Hallo." || len(d.Vocabulary) != 0 {
		t.Errorf("dialog source = %+v, %v", d, err)
	}
	if _, err := f.s.TextReviewSource(ctx, "text", f.textID+99); !errors.Is(err, ai.ErrItemGone) {
		t.Errorf("a missing text: err = %v, want ErrItemGone", err)
	}
}

func TestSaveRefusesAChangedSourceAndGetMarksAnEditedOneStale(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.s.SaveTextReview(ctx, "text", f.textID, "translation", "not the version", json.RawMessage(`[]`), json.RawMessage(`{}`)); !errors.Is(err, ai.ErrItemChanged) {
		t.Fatalf("a wrong version: err = %v, want ErrItemChanged", err)
	}
	if err := f.s.SaveTextReview(ctx, "text", f.textID+99, "translation", "x", json.RawMessage(`[]`), json.RawMessage(`{}`)); !errors.Is(err, ai.ErrItemGone) {
		t.Fatalf("a missing text: err = %v, want ErrItemGone", err)
	}
	if _, err := f.s.Get(ctx, "text", f.textID, "translation"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("before a review: err = %v, want ErrNotFound", err)
	}
	f.save(t, "text", f.textID, "translation", vocabulary.ReviewFinding{Field: "translation", Action: "replace", Locale: "en", Current: "The dog sleeps.", Suggestion: "The dog sleeps well."})
	if r, err := f.s.Get(ctx, "text", f.textID, "translation"); err != nil || r.Stale || len(r.Findings) != 1 {
		t.Fatalf("review = %+v, %v", r, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE texts SET body = 'Der Hund wacht.' WHERE id = $1`, f.textID); err != nil {
		t.Fatal(err)
	}
	if r, _ := f.s.Get(ctx, "text", f.textID, "translation"); !r.Stale {
		t.Error("an edited source must make the review stale")
	}
	if _, err := f.s.Apply(ctx, "text", f.textID, "translation", nil); !errors.Is(err, ai.ErrItemChanged) {
		t.Errorf("apply on a stale review: err = %v, want ErrItemChanged", err)
	}
}

func TestApplyOverwritesTheTranscriptionAndATranslationWhereTheyAreUnchanged(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.save(t, "text", f.textID, "transcription",
		vocabulary.ReviewFinding{Field: "transcription", Action: "replace", Current: "tr", Suggestion: "tr2", Reason: "r"})
	f.save(t, "text", f.textID, "translation",
		vocabulary.ReviewFinding{Field: "translation", Action: "replace", Locale: "en", Current: "The dog sleeps.", Suggestion: "The dog is sleeping."},
		vocabulary.ReviewFinding{Field: "translation", Action: "replace", Locale: "pl", Current: "Pies śpi.", Suggestion: "Pies drzemie."},
		vocabulary.ReviewFinding{Field: "translation", Action: "comment", Locale: "pl", Current: "Pies śpi.", Reason: "fine"})
	// The Polish translation is edited after the review.
	if err := translations.New(f.pool).Set(ctx, "text", f.textID, "pl", "Pies sobie śpi."); err != nil {
		t.Fatal(err)
	}

	results, err := f.s.Apply(ctx, "text", f.textID, "translation", nil)
	if err != nil {
		t.Fatal(err)
	}
	var outcomes []string
	for _, r := range results {
		outcomes = append(outcomes, r.Outcome)
	}
	if want := []string{"applied", "conflict", "not_applicable"}; !reflect.DeepEqual(outcomes, want) {
		t.Fatalf("outcomes = %v, want %v", outcomes, want)
	}
	// The transcription is its own review: applying the translations left it alone.
	if tx, _ := texts.New(f.pool).Get(ctx, f.textID); tx.Transcription != "tr" {
		t.Fatalf("transcription = %q, applying the translation review must not touch it", tx.Transcription)
	}
	if res, err := f.s.Apply(ctx, "text", f.textID, "transcription", nil); err != nil || len(res) != 1 || res[0].Outcome != "applied" {
		t.Fatalf("apply transcription = %+v, %v", res, err)
	}
	tx, _ := texts.New(f.pool).Get(ctx, f.textID)
	en, _, _ := translations.New(f.pool).Get(ctx, "text", f.textID, "en")
	pl, _, _ := translations.New(f.pool).Get(ctx, "text", f.textID, "pl")
	if tx.Transcription != "tr2" || en != "The dog is sleeping." || pl != "Pies sobie śpi." {
		t.Errorf("transcription %q en %q pl %q, want tr2, the new English and the user's Polish kept", tx.Transcription, en, pl)
	}
	r, _ := f.s.Get(ctx, "text", f.textID, "translation")
	var statuses []string
	for _, fd := range r.Findings {
		statuses = append(statuses, fd.Status)
	}
	if want := []string{"applied", "open", "open"}; !reflect.DeepEqual(statuses, want) {
		t.Errorf("statuses = %v, want %v", statuses, want)
	}

	if err := f.s.Dismiss(ctx, "text", f.textID, "translation", []int{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Apply(ctx, "text", f.textID, "translation", []int{9}); !errors.Is(err, vocabulary.ErrInvalidReviewIndex) {
		t.Errorf("a bad index: err = %v", err)
	}
}

func TestApplyWorksOnADialogToo(t *testing.T) {
	f := newFixture(t)
	f.save(t, "dialog", f.dlgID, "transcription", vocabulary.ReviewFinding{Field: "transcription", Action: "replace", Current: "tr", Suggestion: "tr9"})
	if res, err := f.s.Apply(context.Background(), "dialog", f.dlgID, "transcription", nil); err != nil || len(res) != 1 || res[0].Outcome != "applied" {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	if d, _ := dialogs.New(f.pool).Get(context.Background(), f.dlgID); d.Transcription != "tr9" {
		t.Errorf("dialog transcription = %q", d.Transcription)
	}
}
