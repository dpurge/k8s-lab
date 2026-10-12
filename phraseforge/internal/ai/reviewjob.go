package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// KindReviewVocabularyItem is the job kind that reviews one vocabulary item
// and stores the model's proposals for the user to accept or dismiss.
const KindReviewVocabularyItem = "review_vocabulary_item"

// ReviewJobPayload identifies the item to review by its id, which survives the
// list being reordered or regenerated while the job waits in the queue.
type ReviewJobPayload struct {
	ItemID int64 `json:"item_id"`
}

// ReviewStore is the consumer-side interface of what HandleReviewItem reads and
// writes. Both methods return ErrItemGone or ErrItemChanged (which skip the
// job) when the item is gone, or SaveReview when it no longer has phrase, the
// phrase the review started from.
type ReviewStore interface {
	ReviewSource(ctx context.Context, itemID int64) (ReviewInput, error)
	SaveReview(ctx context.Context, itemID int64, phrase string, findings, response json.RawMessage) error
}

// SetReviewStore sets the store the review job uses; a Service without one
// fails review jobs with a clear error. It is a setter, not a New argument,
// because only the review job needs it.
func (s *Service) SetReviewStore(r ReviewStore) { s.reviews = r }

// NewReviewJob builds the job reviewing item itemID.
func NewReviewJob(itemID int64) (ItemJob, error) {
	if itemID == 0 {
		return ItemJob{}, fmt.Errorf("review job: item_id is required")
	}
	payload, err := json.Marshal(ReviewJobPayload{ItemID: itemID})
	if err != nil {
		return ItemJob{}, err
	}
	return ItemJob{Kind: KindReviewVocabularyItem, Payload: payload}, nil
}

// reviewJobResult is the job's result for the Jobs page.
type reviewJobResult struct {
	Response json.RawMessage `json:"response"`
	Findings int             `json:"findings"`
}

// HandleReviewItem is the jobs.HandlerFunc of KindReviewVocabularyItem: it
// reads the item, makes one review call and stores the findings as the item's
// review (a review with no findings is stored too: it says the item was
// checked). An item deleted or changed since the job was queued, or while the
// model worked, ends the job as skipped (see jobs.ErrSkipped).
func (s *Service) HandleReviewItem(ctx context.Context, id string, payload json.RawMessage) (json.RawMessage, error) {
	var p ReviewJobPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	if p.ItemID == 0 {
		return nil, fmt.Errorf("review job: item_id is required")
	}
	if s.reviews == nil {
		return nil, fmt.Errorf("review job: no review store configured")
	}
	in, err := s.reviews.ReviewSource(ctx, p.ItemID)
	if err != nil {
		return nil, err
	}
	result, err := s.ReviewItem(ctx, in)
	if err != nil {
		return nil, err
	}
	findings := result.Findings
	if findings == nil {
		findings = []ReviewFinding{}
	}
	for i := range findings {
		findings[i].Status = ReviewOpen
	}
	stored, err := json.Marshal(findings)
	if err != nil {
		return nil, err
	}
	if err := s.reviews.SaveReview(ctx, p.ItemID, in.Phrase, stored, result.Response); err != nil {
		return nil, err
	}
	return json.Marshal(reviewJobResult{Response: json.RawMessage(strings.TrimSpace(string(result.Response))), Findings: len(findings)})
}
