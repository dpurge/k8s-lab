package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"phraseforge/internal/jobs"
)

// fakeReviewStore is an in-memory ReviewStore.
type fakeReviewStore struct {
	source    ReviewInput
	sourceErr error
	saveErr   error

	saves                  int
	savedItem              int64
	savedPhrase            string
	savedFindings, savedIn json.RawMessage
}

func (f *fakeReviewStore) ReviewSource(ctx context.Context, itemID int64) (ReviewInput, error) {
	return f.source, f.sourceErr
}

func (f *fakeReviewStore) SaveReview(ctx context.Context, itemID int64, phrase string, findings, response json.RawMessage) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saves++
	f.savedItem, f.savedPhrase, f.savedFindings, f.savedIn = itemID, phrase, findings, response
	return nil
}

const phraseRewriteReply = `{"phrase":{"ok":false,"suggestion":"der Hund","reason":"Accusative."},"grammar":{"ok":true,"suggestion":"N m","reason":""},"transcription":` + okEntry + `,"translations":[{"locale":"en","ok":false,"suggestion":"dog","reason":"No explanations."}],"notes":[],"examples":[]}`

func runReviewJob(t *testing.T, store *fakeReviewStore, payload string, replies ...string) (*fakeOllama, json.RawMessage, error) {
	t.Helper()
	s, ollama := reviewService(t, replies...)
	if store != nil {
		s.SetReviewStore(store)
	}
	result, err := s.HandleReviewItem(context.Background(), "job-1", json.RawMessage(payload))
	return ollama, result, err
}

func TestHandleReviewItemStoresTheFindingsAsOpenProposals(t *testing.T) {
	store := &fakeReviewStore{source: reviewItem}
	_, result, err := runReviewJob(t, store, `{"item_id":42}`, phraseRewriteReply)
	if err != nil {
		t.Fatal(err)
	}
	if store.saves != 1 || store.savedItem != 42 || store.savedPhrase != "den Hund" {
		t.Fatalf("saved %d times as item %d phrase %q, want once as item 42 phrase \"den Hund\"", store.saves, store.savedItem, store.savedPhrase)
	}
	var findings []ReviewFinding
	if err := json.Unmarshal(store.savedFindings, &findings); err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 || findings[0].Field != ReviewFieldPhrase || findings[0].Grammar != "N m" || findings[1].Field != ReviewFieldTranslation {
		t.Fatalf("findings = %+v, want the phrase rewrite and the English translation", findings)
	}
	for _, f := range findings {
		if f.Status != ReviewOpen {
			t.Errorf("%s finding status = %q, want %q", f.Field, f.Status, ReviewOpen)
		}
	}
	if string(store.savedIn) != phraseRewriteReply {
		t.Errorf("stored response = %s, want the model's reply", store.savedIn)
	}
	var res struct {
		Findings int
		Response json.RawMessage
	}
	if err := json.Unmarshal(result, &res); err != nil || res.Findings != 2 || len(res.Response) == 0 {
		t.Errorf("job result = %s (%v), want 2 findings and the response", result, err)
	}
}

func TestHandleReviewItemStoresAnEmptyReviewWhenNothingIsWrong(t *testing.T) {
	store := &fakeReviewStore{source: reviewItem}
	if _, _, err := runReviewJob(t, store, `{"item_id":42}`, fullOK); err != nil {
		t.Fatal(err)
	}
	if store.saves != 1 || string(store.savedFindings) != `[]` {
		t.Errorf("saved %d times with findings %s, want once with []", store.saves, store.savedFindings)
	}
}

func TestHandleReviewItemSkipsAnItemThatIsGoneOrChanged(t *testing.T) {
	for name, store := range map[string]*fakeReviewStore{
		"gone before the call":    {sourceErr: ErrItemGone},
		"changed during the call": {source: reviewItem, saveErr: ErrItemChanged},
		"deleted during the call": {source: reviewItem, saveErr: ErrItemGone},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := runReviewJob(t, store, `{"item_id":42}`, fullOK)
			if !errors.Is(err, jobs.ErrSkipped) {
				t.Errorf("err = %v, want a skip (jobs.ErrSkipped)", err)
			}
			if store.saves != 0 {
				t.Errorf("saved %d times, want nothing stored", store.saves)
			}
		})
	}
}

func TestHandleReviewItemFailsWithoutStoringWhenTheModelStaysInvalid(t *testing.T) {
	store := &fakeReviewStore{source: reviewItem}
	ollama, _, err := runReviewJob(t, store, `{"item_id":42}`, `nope`, `nope`, `nope`)
	if err == nil || errors.Is(err, jobs.ErrSkipped) {
		t.Fatalf("err = %v, want a failure", err)
	}
	if store.saves != 0 || len(ollama.msgCount) != 3 {
		t.Errorf("saved %d times after %d model calls, want nothing stored after the 3 attempts", store.saves, len(ollama.msgCount))
	}
}

func TestHandleReviewItemRejectsABadPayloadOrMissingStore(t *testing.T) {
	store := &fakeReviewStore{source: reviewItem}
	for name, payload := range map[string]string{"not json": `x`, "no item": `{}`} {
		t.Run(name, func(t *testing.T) {
			ollama, _, err := runReviewJob(t, store, payload, fullOK)
			if err == nil || len(ollama.msgCount) != 0 {
				t.Errorf("err = %v after %d model calls, want an error and no call", err, len(ollama.msgCount))
			}
		})
	}
	_, _, err := runReviewJob(t, nil, `{"item_id":42}`, fullOK)
	if err == nil || !strings.Contains(err.Error(), "no review store") {
		t.Errorf("err = %v, want one about the missing store", err)
	}
}

func TestNewReviewJob(t *testing.T) {
	job, err := NewReviewJob(7)
	if err != nil || job.Kind != KindReviewVocabularyItem || string(job.Payload) != `{"item_id":7}` {
		t.Fatalf("NewReviewJob(7) = %+v, %v", job, err)
	}
	if _, err := NewReviewJob(0); err == nil {
		t.Error("NewReviewJob(0) succeeded, want an error")
	}
}
