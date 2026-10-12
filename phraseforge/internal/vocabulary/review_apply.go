package vocabulary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"phraseforge/internal/textnorm"
)

// Review finding fields, actions and statuses as the review job stores them
// (ai.ReviewFinding is the writer; the JSON keys below are the contract, kept
// equal by a test in the server package).
const (
	ReviewFieldPhrase        = "phrase"
	ReviewFieldGrammar       = "grammar"
	ReviewFieldTranscription = "transcription"
	ReviewFieldTranslation   = "translation"
	ReviewFieldNotes         = "notes"
	ReviewFieldExample       = "example"

	ReviewActionReplace = "replace"
	ReviewActionRemove  = "remove"
	ReviewActionComment = "comment"

	ReviewStatusOpen      = "open"
	ReviewStatusApplied   = "applied"
	ReviewStatusDismissed = "dismissed"
)

// ErrInvalidReviewIndex is returned when a request names a finding that the
// review does not have.
var ErrInvalidReviewIndex = errors.New("no such review finding")

// ReviewFinding is one proposal of a stored review.
type ReviewFinding struct {
	Field         string `json:"field"`
	Action        string `json:"action"`
	Locale        string `json:"locale,omitempty"`
	Index         int    `json:"index,omitempty"`
	Current       string `json:"current,omitempty"`
	Suggestion    string `json:"suggestion,omitempty"`
	Grammar       string `json:"grammar,omitempty"`
	Transcription string `json:"transcription,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Status        string `json:"status,omitempty"`
}

// Outcomes of applying one finding.
const (
	// ReviewApplied: the stored value was changed.
	ReviewApplied = "applied"
	// ReviewConflict: the stored value is no longer the one that was reviewed
	// (it was edited since), so nothing was changed and the finding stays open.
	ReviewConflict = "conflict"
	// ReviewNotApplicable: a remark without a fix, or a finding that is not open.
	ReviewNotApplicable = "not_applicable"
)

// ReviewResult is the outcome for the finding at Index (0-based in the review).
type ReviewResult struct {
	Index   int    `json:"index"`
	Outcome string `json:"outcome"`
}

// ReviewSummary is what a list shows per reviewed item.
type ReviewSummary struct {
	ItemID    int64     `json:"itemId"`
	Position  int       `json:"position"`
	CreatedAt time.Time `json:"createdAt"`
	Open      int       `json:"open"`
	Total     int       `json:"total"`
	// Stale is true when the item no longer holds the reviewed phrase; its
	// proposals can not be applied any more.
	Stale bool `json:"stale"`
}

// ListReviews returns the reviews of the items of list listID, by position.
func (s *Store) ListReviews(ctx context.Context, listID int64) ([]ReviewSummary, error) {
	rows, err := s.db.Query(ctx, `
		SELECT i.id, i.position, r.created_at, r.phrase <> p.phrase,
		       jsonb_array_length(r.findings),
		       (SELECT count(*) FROM jsonb_array_elements(r.findings) e WHERE e->>'status' = 'open')
		FROM vocabulary_items i
		JOIN vocabulary_reviews r ON r.item_id = i.id
		JOIN phrases p ON p.id = i.phrase_id
		WHERE i.list_id = $1
		ORDER BY i.position`, listID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReviewSummary{}
	for rows.Next() {
		var r ReviewSummary
		if err := rows.Scan(&r.ItemID, &r.Position, &r.CreatedAt, &r.Stale, &r.Total, &r.Open); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FindingList decodes the review's findings.
func (r Review) FindingList() ([]ReviewFinding, error) {
	var findings []ReviewFinding
	if err := json.Unmarshal(r.Findings, &findings); err != nil {
		return nil, fmt.Errorf("stored review of item %d: %w", r.ItemID, err)
	}
	return findings, nil
}

// IsReviewStale reports whether item itemID no longer holds the phrase its review is
// about (ErrReviewNotFound when it has no review).
func (s *Store) IsReviewStale(ctx context.Context, itemID int64) (bool, error) {
	var stale bool
	err := s.db.QueryRow(ctx, `
		SELECT r.phrase <> p.phrase FROM vocabulary_items i
		JOIN phrases p ON p.id = i.phrase_id
		JOIN vocabulary_reviews r ON r.item_id = i.id
		WHERE i.id = $1`, itemID).Scan(&stale)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrReviewNotFound
	}
	return stale, err
}

// selectFindings turns the requested indexes into the set of findings to act
// on: none requested means every open finding. A number outside the review is
// ErrInvalidReviewIndex.
func selectFindings(findings []ReviewFinding, indexes []int) ([]int, error) {
	if len(indexes) == 0 {
		var all []int
		for i, f := range findings {
			if f.Status == ReviewStatusOpen {
				all = append(all, i)
			}
		}
		return all, nil
	}
	seen := map[int]bool{}
	var out []int
	for _, i := range indexes {
		if i < 0 || i >= len(findings) {
			return nil, fmt.Errorf("%w: %d", ErrInvalidReviewIndex, i)
		}
		if !seen[i] {
			seen[i] = true
			out = append(out, i)
		}
	}
	slices.Sort(out)
	return out, nil
}

// lockReview locks item itemID's review row and decodes it.
func lockReview(ctx context.Context, tx pgx.Tx, itemID int64) (phrase string, findings []ReviewFinding, err error) {
	var raw json.RawMessage
	err = tx.QueryRow(ctx, `SELECT phrase, findings FROM vocabulary_reviews WHERE item_id = $1 FOR UPDATE`, itemID).Scan(&phrase, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrReviewNotFound
	}
	if err != nil {
		return "", nil, err
	}
	if err := json.Unmarshal(raw, &findings); err != nil {
		return "", nil, fmt.Errorf("stored review of item %d: %w", itemID, err)
	}
	return phrase, findings, nil
}

func saveFindings(ctx context.Context, tx pgx.Tx, itemID int64, phrase string, findings []ReviewFinding) error {
	raw, err := json.Marshal(findings)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE vocabulary_reviews SET phrase = $2, findings = $3 WHERE item_id = $1`, itemID, phrase, raw)
	return err
}

// DismissReview marks the selected open findings of item itemID's review as
// dismissed (every open one when indexes is empty). It needs no item guard:
// nothing about the item changes.
func (s *Store) DismissReview(ctx context.Context, itemID int64, indexes []int) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	phrase, findings, err := lockReview(ctx, tx, itemID)
	if err != nil {
		return err
	}
	selected, err := selectFindings(findings, indexes)
	if err != nil {
		return err
	}
	for _, i := range selected {
		if findings[i].Status == ReviewStatusOpen {
			findings[i].Status = ReviewStatusDismissed
		}
	}
	if err := saveFindings(ctx, tx, itemID, phrase, findings); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ApplyReview applies the selected open findings of item itemID's review (every
// open one when indexes is empty) in one transaction and marks the applied ones
// applied. The item must still hold the reviewed phrase (ErrItemChanged
// otherwise, ErrItemNotFound if it is gone); each finding is further guarded by
// the value it was about: if that value was edited since, the finding is a
// conflict and nothing is written for it. Findings that change the phrase's
// data go first and the phrase rewrite goes last, since it may merge the phrase
// into an existing one; afterwards the review follows the item's new phrase so
// its remaining findings stay usable. Returns one result per selected finding.
func (s *Store) ApplyReview(ctx context.Context, itemID int64, indexes []int) ([]ReviewResult, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	reviewed, findings, err := lockReview(ctx, tx, itemID)
	if err != nil {
		return nil, err
	}
	if _, err := lockItemIfStill(ctx, tx, itemID, reviewed); err != nil {
		return nil, err
	}
	selected, err := selectFindings(findings, indexes)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(selected, func(a, b int) int {
		return rank(findings[a]) - rank(findings[b])
	})

	results := make([]ReviewResult, 0, len(selected))
	for _, i := range selected {
		f := findings[i]
		outcome := ReviewNotApplicable
		if f.Status == ReviewStatusOpen && f.Action != ReviewActionComment {
			applied, err := applyFinding(ctx, tx, itemID, f)
			if err != nil {
				return nil, err
			}
			outcome = ReviewConflict
			if applied {
				outcome = ReviewApplied
				findings[i].Status = ReviewStatusApplied
			}
		}
		results = append(results, ReviewResult{Index: i, Outcome: outcome})
	}
	slices.SortFunc(results, func(a, b ReviewResult) int { return a.Index - b.Index })

	cur, err := lockItemCurrent(ctx, tx, itemID)
	if err != nil {
		return nil, err
	}
	if err := saveFindings(ctx, tx, itemID, cur.phrase, findings); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return results, nil
}

// rank orders the findings of one apply: the phrase rewrite last.
func rank(f ReviewFinding) int {
	if f.Field == ReviewFieldPhrase {
		return 1
	}
	return 0
}

// lockItemCurrent locks item itemID and its phrase row as they are now.
func lockItemCurrent(ctx context.Context, tx pgx.Tx, itemID int64) (itemPhrase, error) {
	var cur itemPhrase
	err := tx.QueryRow(ctx, `
		SELECT p.id, p.phrase, p.grammar, p.transcription
		FROM vocabulary_items i JOIN phrases p ON p.id = i.phrase_id
		WHERE i.id = $1
		FOR UPDATE OF i, p`, itemID).Scan(&cur.id, &cur.phrase, &cur.grammar, &cur.transcription)
	if errors.Is(err, pgx.ErrNoRows) {
		return itemPhrase{}, ErrItemNotFound
	}
	return cur, err
}

// applyFinding writes one finding if the stored value is still the reviewed
// one; applied is false (a conflict) when it is not. The phrase row is looked
// up again for every finding, since an earlier one may have retargeted it.
func applyFinding(ctx context.Context, tx pgx.Tx, itemID int64, f ReviewFinding) (applied bool, err error) {
	cur, err := lockItemCurrent(ctx, tx, itemID)
	if err != nil {
		return false, err
	}
	suggestion := textnorm.Phrase(f.Suggestion)
	if f.Action == ReviewActionReplace && suggestion == "" {
		return false, nil
	}
	switch f.Field {
	case ReviewFieldPhrase:
		if f.Action != ReviewActionReplace || cur.phrase != f.Current {
			return false, nil
		}
		grammar, transcription := f.Grammar, f.Transcription
		if strings.TrimSpace(grammar) == "" {
			grammar = cur.grammar
		}
		if strings.TrimSpace(transcription) == "" {
			transcription = cur.transcription
		}
		_, err = retargetPhrase(ctx, tx, cur.id, suggestion, grammar, transcription)
		return err == nil, err
	case ReviewFieldGrammar:
		if f.Action != ReviewActionReplace || cur.grammar != f.Current {
			return false, nil
		}
		_, err = retargetPhrase(ctx, tx, cur.id, cur.phrase, suggestion, cur.transcription)
		return err == nil, err
	case ReviewFieldTranscription:
		if f.Action != ReviewActionReplace || cur.transcription != f.Current {
			return false, nil
		}
		_, err = retargetPhrase(ctx, tx, cur.id, cur.phrase, cur.grammar, suggestion)
		return err == nil, err
	case ReviewFieldTranslation:
		return applyTranslationFinding(ctx, tx, cur.id, f, suggestion)
	case ReviewFieldNotes:
		return applyNotesFinding(ctx, tx, cur.id, f, suggestion)
	case ReviewFieldExample:
		return applyExampleFinding(ctx, tx, cur.id, f, suggestion)
	}
	return false, nil
}

func applyTranslationFinding(ctx context.Context, tx pgx.Tx, phraseID int64, f ReviewFinding, suggestion string) (bool, error) {
	if f.Action != ReviewActionReplace {
		return false, nil
	}
	var translation string
	err := tx.QueryRow(ctx, `SELECT coalesce(translation, '') FROM phrase_translation WHERE phrase_id = $1 AND locale = $2 FOR UPDATE`, phraseID, f.Locale).Scan(&translation)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && translation != f.Current) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE phrase_translation SET translation = $3, updated_at = now() WHERE phrase_id = $1 AND locale = $2`, phraseID, f.Locale, suggestion)
	return err == nil, err
}

// applyNotesFinding replaces or removes the locale's notes; a locale left with
// neither translation nor notes loses its row, like every other blank write.
func applyNotesFinding(ctx context.Context, tx pgx.Tx, phraseID int64, f ReviewFinding, suggestion string) (bool, error) {
	var translation, notes string
	err := tx.QueryRow(ctx, `SELECT coalesce(translation, ''), coalesce(notes, '') FROM phrase_translation WHERE phrase_id = $1 AND locale = $2 FOR UPDATE`, phraseID, f.Locale).Scan(&translation, &notes)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && notes != f.Current) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	newNotes := ""
	switch f.Action {
	case ReviewActionReplace:
		newNotes = suggestion
	case ReviewActionRemove:
	default:
		return false, nil
	}
	if strings.TrimSpace(translation) == "" && newNotes == "" {
		_, err = tx.Exec(ctx, `DELETE FROM phrase_translation WHERE phrase_id = $1 AND locale = $2`, phraseID, f.Locale)
	} else {
		_, err = tx.Exec(ctx, `UPDATE phrase_translation SET notes = $3, updated_at = now() WHERE phrase_id = $1 AND locale = $2`, phraseID, f.Locale, nullIfEmpty(newNotes))
	}
	return err == nil, err
}

// applyExampleFinding replaces or removes the example that equals f.Current
// (found by its text, not its number, so findings applied one after another
// do not shift each other).
func applyExampleFinding(ctx context.Context, tx pgx.Tx, phraseID int64, f ReviewFinding, suggestion string) (bool, error) {
	rows, err := tx.Query(ctx, `SELECT example FROM phrase_examples WHERE phrase_id = $1 ORDER BY position`, phraseID)
	if err != nil {
		return false, err
	}
	examples, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, err
	}
	at := slices.Index(examples, f.Current)
	if at < 0 {
		return false, nil
	}
	switch f.Action {
	case ReviewActionRemove:
		examples = slices.Delete(examples, at, at+1)
	case ReviewActionReplace:
		examples[at] = suggestion
	default:
		return false, nil
	}
	stored, err := NormalizeExamples(examples)
	if err != nil {
		return false, nil // a suggestion over the limits: nothing is written for it
	}
	return true, replaceExamples(ctx, tx, phraseID, stored)
}
