package generate

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"phraseforge/internal/vocabulary"
)

func TestKnownCandidatesKeepsOnlyKnownPhrasesWithAUsage(t *testing.T) {
	stored := []vocabulary.Item{
		{ID: 1, Phrase: "known by example", Examples: []string{"ein Beispiel"}},
		{ID: 2, Phrase: "known by translation"},
		{ID: 3, Phrase: "new"},
		{ID: 4, Phrase: "known but no usage", Examples: []string{"x"}},
	}
	usages := []string{"u1", "u2", "u3", ""}
	translations := map[int64]map[string]string{2: {"en": "two"}, 4: {"en": "four"}}

	known, indexes := knownCandidates(stored, usages, translations)

	wantKnown := []KnownCandidate{
		{Phrase: "known by example", Usage: "u1", Examples: []string{"ein Beispiel"}},
		{Phrase: "known by translation", Usage: "u2", Translations: map[string]string{"en": "two"}},
	}
	if !reflect.DeepEqual(known, wantKnown) || !reflect.DeepEqual(indexes, []int{0, 1}) {
		t.Fatalf("known = %+v, indexes = %v, want %+v and [0 1]", known, indexes, wantKnown)
	}
}

// fakeChecker answers from yes (indexes into the batch it receives, by call
// number) and records every batch.
type fakeChecker struct {
	batches [][]KnownCandidate
	yes     func(call int, batch []KnownCandidate) (map[int]Verdict, error)
}

func (f *fakeChecker) Check(_ context.Context, _ string, known []KnownCandidate) (map[int]Verdict, error) {
	f.batches = append(f.batches, known)
	return f.yes(len(f.batches)-1, known)
}

func knownList(n int) ([]KnownCandidate, []int) {
	known := make([]KnownCandidate, n)
	indexes := make([]int, n)
	for i := range known {
		known[i] = KnownCandidate{Phrase: fmt.Sprintf("p%d", i)}
		indexes[i] = i * 10 // stored positions differ from known positions
	}
	return known, indexes
}

func TestCheckInBatchesSplitsLongListsAndMapsAnswersBackToStoredItems(t *testing.T) {
	known, indexes := knownList(2*maxCheckBatch + 5)
	checker := &fakeChecker{yes: func(call int, batch []KnownCandidate) (map[int]Verdict, error) {
		return map[int]Verdict{0: {NeedsExample: true}, len(batch) - 1: {NeedsSense: true}, 1: {}}, nil
	}}

	got := checkInBatches(context.Background(), checker, 7, "deu", known, indexes)

	if len(checker.batches) != 3 || len(checker.batches[0]) != maxCheckBatch || len(checker.batches[1]) != maxCheckBatch || len(checker.batches[2]) != 5 {
		t.Fatalf("batch sizes = %d batches, want %d, %d, 5", len(checker.batches), maxCheckBatch, maxCheckBatch)
	}
	// each batch marks its first candidate needsExample and its last needsSense
	want := map[int]Verdict{}
	for _, k := range []int{0, maxCheckBatch, 2 * maxCheckBatch} {
		want[k*10] = Verdict{NeedsExample: true}
	}
	for _, k := range []int{maxCheckBatch - 1, 2*maxCheckBatch - 1, 2*maxCheckBatch + 4} {
		want[k*10] = Verdict{NeedsSense: true}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("needs = %v, want %v", got, want)
	}
}

func TestCheckInBatchesIgnoresIndexesOutsideTheBatchAndNoes(t *testing.T) {
	known, indexes := knownList(3)
	checker := &fakeChecker{yes: func(int, []KnownCandidate) (map[int]Verdict, error) {
		return map[int]Verdict{-1: {NeedsExample: true}, 3: {NeedsExample: true}, 99: {NeedsSense: true}, 1: {}}, nil
	}}
	if got := checkInBatches(context.Background(), checker, 1, "deu", known, indexes); len(got) != 0 {
		t.Fatalf("needs = %v, want none", got)
	}
}

func TestCheckInBatchesSkipsAFailedBatchAndKeepsTheOthers(t *testing.T) {
	known, indexes := knownList(maxCheckBatch + 1)
	checker := &fakeChecker{yes: func(call int, batch []KnownCandidate) (map[int]Verdict, error) {
		if call == 0 {
			return nil, errors.New("model unreachable")
		}
		return map[int]Verdict{0: {NeedsExample: true}}, nil
	}}
	got := checkInBatches(context.Background(), checker, 1, "deu", known, indexes)
	if want := map[int]Verdict{maxCheckBatch * 10: {NeedsExample: true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("needs = %v, want %v (first batch failed, second answered)", got, want)
	}
}

func TestCheckInBatchesWithNothingKnownMakesNoCall(t *testing.T) {
	checker := &fakeChecker{yes: func(int, []KnownCandidate) (map[int]Verdict, error) { return nil, errors.New("must not be called") }}
	if got := checkInBatches(context.Background(), checker, 1, "deu", nil, nil); got != nil || len(checker.batches) != 0 {
		t.Fatalf("needs = %v after %d calls, want none after none", got, len(checker.batches))
	}
}

// queuedItemJobs returns the locales of the generate_vocabulary_item jobs
// queued for each item of listID, by phrase; a job that may append a sense
// shows as "<locale>+sense".
func queuedItemJobs(t *testing.T, svc *Service, pool *pgxpool.Pool, listID int64) map[string][]string {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, `
		SELECT p.phrase, j.payload->>'locale' || CASE WHEN coalesce((j.payload->>'sense')::boolean, false) THEN '+sense' ELSE '' END
		FROM jobs j
		JOIN vocabulary_items i ON i.id = (j.payload->>'item_id')::bigint
		JOIN phrases p ON p.id = i.phrase_id
		WHERE j.kind = 'generate_vocabulary_item' AND i.list_id = $1 ORDER BY j.id`, listID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var phrase, locale string
		if err := rows.Scan(&phrase, &locale); err != nil {
			t.Fatal(err)
		}
		out[phrase] = append(out[phrase], locale)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, locales := range out {
		slices.Sort(locales) // jobs of one item are not queued in a fixed locale order
	}
	return out
}

// seedKnownPhrases generates a first list holding two complete, translated
// phrases with an example each, and clears the queue, so a second list that
// links the same phrases finds them known.
func seedKnownPhrases(t *testing.T, svc *Service, pool *pgxpool.Pool, uid int64) {
	t.Helper()
	ctx := context.Background()
	first, err := svc.vocab.Create(ctx, uid, "First", "deu", "latn")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.syncVocabItems(ctx, first, "deu", []Candidate{
		{Item: vocabulary.Item{Phrase: "der Koffer", Grammar: "N m"}},
		{Item: vocabulary.Item{Phrase: "der Tag", Grammar: "N m"}},
	}); err != nil {
		t.Fatal(err)
	}
	for position, translation := range []string{"walizka", "dzień"} {
		if err := svc.vocab.SetTranslations(ctx, first, "en", []vocabulary.ItemTranslation{{Position: position, Translation: "en " + translation}}, 2); err != nil {
			t.Fatal(err)
		}
		if err := svc.vocab.SetTranslations(ctx, first, "pl", []vocabulary.ItemTranslation{{Position: position, Translation: translation}}, 2); err != nil {
			t.Fatal(err)
		}
		if err := svc.vocab.SetItemExamples(ctx, first, position, []string{"ein Beispiel"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `DELETE FROM jobs`); err != nil {
		t.Fatal(err)
	}
}

func TestSyncVocabItemsQueuesJobsOnlyForKnownPhrasesTheCheckMarks(t *testing.T) {
	svc, pool, uid := newGenerateDB(t)
	ctx := context.Background()
	seedKnownPhrases(t, svc, pool, uid)
	checker := &fakeChecker{yes: func(_ int, batch []KnownCandidate) (map[int]Verdict, error) {
		yes := map[int]Verdict{}
		for i, k := range batch {
			yes[i] = Verdict{NeedsExample: k.Phrase == "der Koffer", NeedsSense: k.Phrase == "der Tag"}
		}
		return yes, nil
	}}
	svc.SetChecker(checker)
	second, err := svc.vocab.Create(ctx, uid, "Second", "deu", "latn")
	if err != nil {
		t.Fatal(err)
	}

	err = svc.syncVocabItems(ctx, second, "deu", []Candidate{
		{Item: vocabulary.Item{Phrase: "der Koffer", Grammar: "N m"}, Usage: "den Koffer packen"},
		{Item: vocabulary.Item{Phrase: "der Tag", Grammar: "N m"}, Usage: "jeden Tag"},
		{Item: vocabulary.Item{Phrase: "das Buch", Grammar: "N n"}, Usage: "ein neues Buch"},
		{Item: vocabulary.Item{Phrase: "die Zeit", Grammar: "N f"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(checker.batches) != 1 {
		t.Fatalf("checker called %d times, want once for the list", len(checker.batches))
	}
	var sent []string
	for _, k := range checker.batches[0] {
		sent = append(sent, k.Phrase)
	}
	if !reflect.DeepEqual(sent, []string{"der Koffer", "der Tag"}) {
		t.Fatalf("sent to the check = %q, want the two known phrases that have a usage", sent)
	}
	first := checker.batches[0][0]
	if first.Usage != "den Koffer packen" || !reflect.DeepEqual(first.Examples, []string{"ein Beispiel"}) || first.Translations["pl"] != "walizka" || first.Translations["en"] != "en walizka" {
		t.Fatalf("known candidate = %+v, want its usage, examples and translations by locale", first)
	}
	got := queuedItemJobs(t, svc, pool, second)
	want := map[string][]string{
		"der Koffer": {"en"},                   // needsExample: one job, first site locale, no sense
		"der Tag":    {"en+sense", "pl+sense"}, // needsSense: a job per locale that has a translation
		"das Buch":   {"en", "pl"},             // new phrase: as before, never checked
		"die Zeit":   {"en", "pl"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("jobs by phrase = %v, want %v (only der Koffer needs an example, only der Tag a sense)", got, want)
	}
}

func TestSyncVocabItemsWithAFailingCheckStillSucceedsAndQueuesTheUsualJobs(t *testing.T) {
	svc, pool, uid := newGenerateDB(t)
	ctx := context.Background()
	seedKnownPhrases(t, svc, pool, uid)
	svc.SetChecker(&fakeChecker{yes: func(int, []KnownCandidate) (map[int]Verdict, error) { return nil, errors.New("model unreachable") }})
	second, err := svc.vocab.Create(ctx, uid, "Second", "deu", "latn")
	if err != nil {
		t.Fatal(err)
	}

	err = svc.syncVocabItems(ctx, second, "deu", []Candidate{
		{Item: vocabulary.Item{Phrase: "der Koffer", Grammar: "N m"}, Usage: "den Koffer packen"},
		{Item: vocabulary.Item{Phrase: "das Buch", Grammar: "N n"}, Usage: "ein neues Buch"},
	})
	if err != nil {
		t.Fatalf("syncVocabItems returned %v, want success even though the check failed", err)
	}

	got := queuedItemJobs(t, svc, pool, second)
	want := map[string][]string{"das Buch": {"en", "pl"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("jobs by phrase = %v, want %v (no check-driven job for the known phrase)", got, want)
	}
}

func TestSyncVocabItemsWithoutACheckerQueuesNoExampleJobForAKnownPhrase(t *testing.T) {
	svc, pool, uid := newGenerateDB(t)
	ctx := context.Background()
	seedKnownPhrases(t, svc, pool, uid)
	second, err := svc.vocab.Create(ctx, uid, "Second", "deu", "latn")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.syncVocabItems(ctx, second, "deu", []Candidate{{Item: vocabulary.Item{Phrase: "der Koffer", Grammar: "N m"}, Usage: "den Koffer packen"}}); err != nil {
		t.Fatal(err)
	}
	if got := queuedItemJobs(t, svc, pool, second); len(got) != 0 {
		t.Fatalf("jobs by phrase = %v, want none: today's behaviour", got)
	}
}
