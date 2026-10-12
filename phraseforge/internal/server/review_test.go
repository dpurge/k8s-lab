package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"phraseforge/internal/ai"
	"phraseforge/internal/vocabulary"
)

// reviewedItem seeds one item (an Arabic list) with the data the findings
// below are about, and stores a review of it made of findings.
type reviewedItem struct {
	pos int
	id  int64
}

func (f *vocabAPI) seedReviewedItem(t *testing.T, findings ...ai.ReviewFinding) reviewedItem {
	t.Helper()
	ctx := context.Background()
	pos, err := f.srv.vocab.AddItem(ctx, f.listID, "كتابا", "N", "kitāban")
	if err != nil {
		t.Fatal(err)
	}
	for locale, tr := range map[string]vocabulary.ItemTranslation{
		"en": {Position: pos, Translation: "book (object)", Notes: "see also kitab"},
		"pl": {Position: pos, Translation: "książka", Notes: "uwaga"},
	} {
		if err := f.srv.vocab.SetTranslations(ctx, f.listID, locale, []vocabulary.ItemTranslation{tr}, pos+1); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.srv.vocab.SetItemExamples(ctx, f.listID, pos, []string{"ex1", "ex2", "ex3 is long"}); err != nil {
		t.Fatal(err)
	}
	items, err := f.srv.vocab.Items(ctx, f.listID)
	if err != nil {
		t.Fatal(err)
	}
	r := reviewedItem{pos: pos, id: items[pos].ID}
	for i := range findings {
		findings[i].Status = ai.ReviewOpen
	}
	raw, err := json.Marshal(findings)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.srv.vocab.SaveReview(ctx, r.id, "كتابا", raw, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *vocabAPI) reviewPath(pos int, suffix string) string {
	return fmt.Sprintf("/api/v1/vocabulary/%d/items/%d/review%s", f.listID, pos, suffix)
}

type actResponse struct {
	Results []vocabulary.ReviewResult `json:"results"`
	Review  reviewView                `json:"review"`
}

func decodeJSON[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return v
}

var allKindsOfFinding = []ai.ReviewFinding{
	{Field: ai.ReviewFieldPhrase, Action: ai.ReviewReplace, Current: "كتابا", Suggestion: "كتاب", Grammar: "N m", Transcription: "kitāb", Reason: "Accusative form."},
	{Field: ai.ReviewFieldTranslation, Action: ai.ReviewReplace, Locale: "en", Current: "book (object)", Suggestion: "book"},
	{Field: ai.ReviewFieldNotes, Action: ai.ReviewRemove, Locale: "pl", Current: "uwaga"},
	{Field: ai.ReviewFieldExample, Action: ai.ReviewRemove, Index: 1, Current: "ex1"},
	{Field: ai.ReviewFieldExample, Action: ai.ReviewReplace, Index: 3, Current: "ex3 is long", Suggestion: "ex3"},
	{Field: ai.ReviewFieldTranslation, Action: ai.ReviewComment, Locale: "pl", Current: "książka", Reason: "Fine, but check."},
}

func TestReviewApplyAllWritesEveryKindOfProposalAndTheReviewFollowsTheNewPhrase(t *testing.T) {
	f := newVocabAPI(t)
	item := f.seedReviewedItem(t, allKindsOfFinding...)

	rec := f.do(t, f.adminID, http.MethodPost, f.reviewPath(item.pos, "/apply"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply = %d %s", rec.Code, rec.Body)
	}
	got := decodeJSON[actResponse](t, rec.Body.Bytes())
	var outcomes []string
	for _, r := range got.Results {
		outcomes = append(outcomes, r.Outcome)
	}
	want := []string{"applied", "applied", "applied", "applied", "applied", "not_applicable"}
	if !reflect.DeepEqual(outcomes, want) {
		t.Fatalf("outcomes = %v, want %v", outcomes, want)
	}

	src, err := f.srv.vocab.ReviewSource(context.Background(), item.id)
	if err != nil {
		t.Fatal(err)
	}
	if src.Phrase != "كتاب" || src.Grammar != "N m" || src.Transcription != "kitāb" {
		t.Errorf("phrase unit = %q %q %q, want the rewrite with its grammar and transcription together", src.Phrase, src.Grammar, src.Transcription)
	}
	wantTranslations := []vocabulary.ReviewTranslation{
		{Locale: "en", Translation: "book", Notes: "see also kitab"}, // notes untouched
		{Locale: "pl", Translation: "książka"},                       // notes removed
	}
	if !reflect.DeepEqual(src.Translations, wantTranslations) {
		t.Errorf("translations = %+v, want %+v", src.Translations, wantTranslations)
	}
	if want := []string{"ex2", "ex3"}; !reflect.DeepEqual(src.Examples, want) {
		t.Errorf("examples = %v, want %v", src.Examples, want)
	}

	// The review follows the new phrase and keeps the remark open.
	if got.Review.Stale || got.Review.Phrase != "كتاب" {
		t.Errorf("review phrase %q stale %v, want it to follow the new phrase", got.Review.Phrase, got.Review.Stale)
	}
	statuses := make([]string, len(got.Review.Findings))
	for i, fd := range got.Review.Findings {
		statuses[i] = fd.Status
	}
	if want := []string{"applied", "applied", "applied", "applied", "applied", "open"}; !reflect.DeepEqual(statuses, want) {
		t.Errorf("statuses = %v, want %v", statuses, want)
	}
}

func TestReviewApplyChosenFindingsLeavesTheOthersOpen(t *testing.T) {
	f := newVocabAPI(t)
	item := f.seedReviewedItem(t, allKindsOfFinding...)

	rec := f.do(t, f.adminID, http.MethodPost, f.reviewPath(item.pos, "/apply"), map[string]any{"indexes": []int{1}})
	if rec.Code != http.StatusOK {
		t.Fatalf("apply = %d %s", rec.Code, rec.Body)
	}
	got := decodeJSON[actResponse](t, rec.Body.Bytes())
	if len(got.Results) != 1 || got.Results[0].Index != 1 || got.Results[0].Outcome != "applied" {
		t.Fatalf("results = %+v, want only finding 1 applied", got.Results)
	}
	src, _ := f.srv.vocab.ReviewSource(context.Background(), item.id)
	if src.Phrase != "كتابا" || src.Translations[0].Translation != "book" || len(src.Examples) != 3 {
		t.Errorf("item = %+v, want only the English translation changed", src)
	}
}

func TestReviewApplyIsAConflictWhereTheValueWasEditedSinceTheReview(t *testing.T) {
	f := newVocabAPI(t)
	item := f.seedReviewedItem(t, allKindsOfFinding[1], allKindsOfFinding[3])
	// Someone corrects the English translation after the review.
	if err := f.srv.vocab.SetTranslations(context.Background(), f.listID, "en", []vocabulary.ItemTranslation{{Position: item.pos, Translation: "a book", Notes: "see also kitab"}}, item.pos+1); err != nil {
		t.Fatal(err)
	}

	rec := f.do(t, f.adminID, http.MethodPost, f.reviewPath(item.pos, "/apply"), nil)
	got := decodeJSON[actResponse](t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || got.Results[0].Outcome != "conflict" || got.Results[1].Outcome != "applied" {
		t.Fatalf("apply = %d %+v, want a conflict for the edited translation and the example removal applied", rec.Code, got.Results)
	}
	src, _ := f.srv.vocab.ReviewSource(context.Background(), item.id)
	if src.Translations[0].Translation != "a book" {
		t.Errorf("translation = %q, want the user's edit kept", src.Translations[0].Translation)
	}
	if got.Review.Findings[0].Status != "open" || got.Review.Findings[1].Status != "applied" {
		t.Errorf("statuses = %q %q, want the conflict left open", got.Review.Findings[0].Status, got.Review.Findings[1].Status)
	}
}

func TestReviewOfAChangedPhraseIsStaleAndCannotBeApplied(t *testing.T) {
	f := newVocabAPI(t)
	item := f.seedReviewedItem(t, allKindsOfFinding[1])
	if err := f.srv.vocab.UpdateItem(context.Background(), f.listID, item.pos, "كتابان", "N", "kitābān"); err != nil {
		t.Fatal(err)
	}

	rec := f.do(t, f.adminID, http.MethodGet, f.reviewPath(item.pos, ""), nil)
	if view := decodeJSON[reviewView](t, rec.Body.Bytes()); rec.Code != http.StatusOK || !view.Stale || view.Phrase != "كتابا" {
		t.Fatalf("GET = %d %+v, want the review marked stale with the reviewed phrase", rec.Code, view)
	}
	rec = f.do(t, f.adminID, http.MethodPost, f.reviewPath(item.pos, "/apply"), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("apply = %d %s, want 409", rec.Code, rec.Body)
	}
	rec = f.do(t, f.adminID, http.MethodGet, fmt.Sprintf("/api/v1/vocabulary/%d/reviews", f.listID), nil)
	list := decodeJSON[struct {
		Reviews []vocabulary.ReviewSummary `json:"reviews"`
	}](t, rec.Body.Bytes())
	if len(list.Reviews) != 1 || !list.Reviews[0].Stale || list.Reviews[0].Open != 1 {
		t.Errorf("reviews = %+v, want one stale review with one open finding", list.Reviews)
	}
}

func TestReviewDismissMarksFindingsAndTheSummaryCountsTheOpenOnes(t *testing.T) {
	f := newVocabAPI(t)
	item := f.seedReviewedItem(t, allKindsOfFinding...)

	rec := f.do(t, f.adminID, http.MethodPost, f.reviewPath(item.pos, "/dismiss"), map[string]any{"indexes": []int{0, 5}})
	if rec.Code != http.StatusOK {
		t.Fatalf("dismiss = %d %s", rec.Code, rec.Body)
	}
	got := decodeJSON[actResponse](t, rec.Body.Bytes())
	if got.Review.Findings[0].Status != "dismissed" || got.Review.Findings[5].Status != "dismissed" || got.Review.Findings[1].Status != "open" {
		t.Errorf("statuses = %+v", got.Review.Findings)
	}
	if src, _ := f.srv.vocab.ReviewSource(context.Background(), item.id); src.Phrase != "كتابا" {
		t.Errorf("phrase = %q, dismissing must not change the item", src.Phrase)
	}
	rec = f.do(t, f.adminID, http.MethodGet, fmt.Sprintf("/api/v1/vocabulary/%d/reviews", f.listID), nil)
	list := decodeJSON[struct {
		Reviews []vocabulary.ReviewSummary `json:"reviews"`
	}](t, rec.Body.Bytes())
	if len(list.Reviews) != 1 || list.Reviews[0].Open != 4 || list.Reviews[0].Total != 6 {
		t.Errorf("reviews = %+v, want 4 of 6 open", list.Reviews)
	}
}

func TestReviewRoutesRefuseBadInputAndUsersWhoCannotEdit(t *testing.T) {
	f := newVocabAPI(t)
	item := f.seedReviewedItem(t, allKindsOfFinding[1])

	if rec := f.do(t, f.adminID, http.MethodPost, f.reviewPath(item.pos, "/apply"), map[string]any{"indexes": []int{7}}); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown finding: %d, want 400", rec.Code)
	}
	if rec := f.do(t, f.adminID, http.MethodGet, f.reviewPath(item.pos+5, ""), nil); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown position: %d, want 404", rec.Code)
	}
	if rec := f.do(t, f.adminID, http.MethodPost, f.reviewPath(item.pos, "/apply"), "not json"); rec.Code != http.StatusBadRequest {
		t.Errorf("a malformed body: %d, want 400", rec.Code)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, f.reviewPath(item.pos, "")},
		{http.MethodPost, f.reviewPath(item.pos, "")},
		{http.MethodPost, f.reviewPath(item.pos, "/apply")},
		{http.MethodPost, f.reviewPath(item.pos, "/dismiss")},
		{http.MethodPost, fmt.Sprintf("/api/v1/vocabulary/%d/review", f.listID)},
		{http.MethodGet, fmt.Sprintf("/api/v1/vocabulary/%d/reviews", f.listID)},
	} {
		if rec := f.do(t, f.studentID, c.method, c.path, nil); rec.Code != http.StatusForbidden {
			t.Errorf("student %s %s = %d, want 403", c.method, c.path, rec.Code)
		}
	}
	// No review yet for another item.
	pos2, _ := f.srv.vocab.AddItem(context.Background(), f.listID, "قلم", "", "")
	if rec := f.do(t, f.adminID, http.MethodGet, f.reviewPath(pos2, ""), nil); rec.Code != http.StatusNotFound {
		t.Errorf("an item without a review: %d, want 404", rec.Code)
	}
}

func TestReviewQueueingMakesOneJobPerItemAndNeverASecondWhileOneWaits(t *testing.T) {
	f := newVocabAPI(t)
	ctx := context.Background()
	a, _ := f.srv.vocab.AddItem(ctx, f.listID, "كتاب", "", "")
	b, _ := f.srv.vocab.AddItem(ctx, f.listID, "قلم", "", "")
	listPath := fmt.Sprintf("/api/v1/vocabulary/%d/review", f.listID)

	rec := f.do(t, f.adminID, http.MethodPost, listPath, nil)
	if got := decodeJSON[map[string]int](t, rec.Body.Bytes()); rec.Code != http.StatusAccepted || got["enqueued"] != 2 || got["alreadyQueued"] != 0 {
		t.Fatalf("first queue = %d %v, want 2 enqueued", rec.Code, got)
	}
	active, err := f.srv.jobs.ActiveItemIDs(ctx, ai.KindReviewVocabularyItem)
	if err != nil || len(active) != 2 {
		t.Fatalf("active = %v, %v, want both items", active, err)
	}

	rec = f.do(t, f.adminID, http.MethodPost, listPath, nil)
	if got := decodeJSON[map[string]int](t, rec.Body.Bytes()); got["enqueued"] != 0 || got["alreadyQueued"] != 2 {
		t.Errorf("second queue = %v, want nothing new", got)
	}
	rec = f.do(t, f.adminID, http.MethodPost, f.reviewPath(a, ""), nil)
	if got := decodeJSON[map[string]int](t, rec.Body.Bytes()); got["enqueued"] != 0 || got["alreadyQueued"] != 1 {
		t.Errorf("one item again = %v, want nothing new", got)
	}

	rec = f.do(t, f.adminID, http.MethodGet, fmt.Sprintf("/api/v1/vocabulary/%d/reviews", f.listID), nil)
	list := decodeJSON[struct {
		Pending []int `json:"pending"`
	}](t, rec.Body.Bytes())
	if !reflect.DeepEqual(list.Pending, []int{a, b}) {
		t.Errorf("pending positions = %v, want %v", list.Pending, []int{a, b})
	}
}

// The review job writes ai.ReviewFinding, the store reads vocabulary.ReviewFinding:
// the two must carry the same JSON keys.
func TestReviewFindingJSONIsTheSameInTheJobAndTheStore(t *testing.T) {
	written := ai.ReviewFinding{Field: "phrase", Action: "replace", Locale: "en", Index: 2, Current: "c", Suggestion: "s", Grammar: "g", Transcription: "t", Reason: "r", Status: "open"}
	raw, err := json.Marshal(written)
	if err != nil {
		t.Fatal(err)
	}
	var read vocabulary.ReviewFinding
	if err := json.Unmarshal(raw, &read); err != nil {
		t.Fatal(err)
	}
	back, _ := json.Marshal(read)
	if string(back) != string(raw) {
		t.Errorf("store read %s back as %s: a key differs between ai.ReviewFinding and vocabulary.ReviewFinding", raw, back)
	}
	for _, c := range [][2]string{{ai.ReviewFieldPhrase, vocabulary.ReviewFieldPhrase}, {ai.ReviewFieldGrammar, vocabulary.ReviewFieldGrammar}, {ai.ReviewFieldTranscription, vocabulary.ReviewFieldTranscription}, {ai.ReviewFieldTranslation, vocabulary.ReviewFieldTranslation}, {ai.ReviewFieldNotes, vocabulary.ReviewFieldNotes}, {ai.ReviewFieldExample, vocabulary.ReviewFieldExample}, {ai.ReviewReplace, vocabulary.ReviewActionReplace}, {ai.ReviewRemove, vocabulary.ReviewActionRemove}, {ai.ReviewComment, vocabulary.ReviewActionComment}, {ai.ReviewOpen, vocabulary.ReviewStatusOpen}} {
		if c[0] != c[1] {
			t.Errorf("constant %q in ai and %q in vocabulary differ", c[0], c[1])
		}
	}
}

// A phrase rewrite may merge the phrase into one that already exists. The
// other findings are applied first, on the phrase they were reviewed against,
// so they are not turned into conflicts by the merge; the merged phrase then
// keeps the survivor's own data where it has some.
func TestReviewPhraseRewriteIntoAnExistingPhraseMergesAfterTheOtherFindings(t *testing.T) {
	f := newVocabAPI(t)
	ctx := context.Background()
	survivorPos, err := f.srv.vocab.AddItem(ctx, f.listID, "كتاب", "N m", "kitāb")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.srv.vocab.SetTranslations(ctx, f.listID, "en", []vocabulary.ItemTranslation{{Position: survivorPos, Translation: "libro"}}, survivorPos+1); err != nil {
		t.Fatal(err)
	}
	item := f.seedReviewedItem(t, allKindsOfFinding[0], allKindsOfFinding[1])

	rec := f.do(t, f.adminID, http.MethodPost, f.reviewPath(item.pos, "/apply"), nil)
	got := decodeJSON[actResponse](t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || got.Results[0].Outcome != "applied" || got.Results[1].Outcome != "applied" {
		t.Fatalf("apply = %d %+v, want both applied", rec.Code, got.Results)
	}
	src, err := f.srv.vocab.ReviewSource(ctx, item.id)
	if err != nil {
		t.Fatal(err)
	}
	if src.Phrase != "كتاب" || src.Translations[0].Translation != "libro" {
		t.Errorf("item = %+v, want the merged phrase with the survivor's translation", src)
	}
}
