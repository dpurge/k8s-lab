package vocabulary

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrReviewNotFound is returned by GetReview for an item that has no review.
var ErrReviewNotFound = errors.New("vocabulary review not found")

// ReviewTranslation is one locale's translation and notes of a reviewed item.
type ReviewTranslation struct {
	Locale      string
	Translation string
	Notes       string
}

// ReviewSource is what a review job reads from an item when it starts: the
// phrase as it is now with its grammar and transcription, the list's language,
// the phrase's examples in order and the locales that have a non-blank
// translation or notes (in locale order).
type ReviewSource struct {
	Phrase        string
	Grammar       string
	Transcription string
	Language      string
	Examples      []string
	Translations  []ReviewTranslation
}

// ReviewSource returns item itemID's current state for a review, or
// ErrItemNotFound.
func (s *Store) ReviewSource(ctx context.Context, itemID int64) (ReviewSource, error) {
	var src ReviewSource
	var phraseID int64
	err := s.db.QueryRow(ctx, `
		SELECT p.id, p.phrase, p.grammar, p.transcription, l.language
		FROM vocabulary_items i
		JOIN phrases p ON p.id = i.phrase_id
		JOIN vocabulary_lists l ON l.id = i.list_id
		WHERE i.id = $1`, itemID).Scan(&phraseID, &src.Phrase, &src.Grammar, &src.Transcription, &src.Language)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewSource{}, ErrItemNotFound
	}
	if err != nil {
		return ReviewSource{}, err
	}
	examples, err := s.examplesByPhrase(ctx, []int64{phraseID})
	if err != nil {
		return ReviewSource{}, err
	}
	src.Examples = examples[phraseID]
	rows, err := s.db.Query(ctx, `
		SELECT locale, coalesce(translation, ''), coalesce(notes, '') FROM phrase_translation
		WHERE phrase_id = $1 AND (btrim(coalesce(translation, '')) <> '' OR btrim(coalesce(notes, '')) <> '')
		ORDER BY locale`, phraseID)
	if err != nil {
		return ReviewSource{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var t ReviewTranslation
		if err := rows.Scan(&t.Locale, &t.Translation, &t.Notes); err != nil {
			return ReviewSource{}, err
		}
		src.Translations = append(src.Translations, t)
	}
	return src, rows.Err()
}

// Review is the stored latest review of an item.
type Review struct {
	ItemID    int64
	Phrase    string // the phrase that was reviewed
	Findings  json.RawMessage
	Response  json.RawMessage
	CreatedAt time.Time
}

// SaveReview stores findings and the model's response as item itemID's review,
// replacing the previous one, but only while the item still has phrase: the
// item may be gone (ErrItemNotFound) or hold another phrase (ErrItemChanged),
// like every write of a background job.
func (s *Store) SaveReview(ctx context.Context, itemID int64, phrase string, findings, response json.RawMessage) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	if _, err := lockItemIfStill(ctx, tx, itemID, phrase); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vocabulary_reviews (item_id, phrase, findings, response) VALUES ($1, $2, $3, $4)
		ON CONFLICT (item_id) DO UPDATE
		SET phrase = excluded.phrase, findings = excluded.findings, response = excluded.response, created_at = now()`,
		itemID, phrase, findings, response); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetReview returns item itemID's stored review, or ErrReviewNotFound.
func (s *Store) GetReview(ctx context.Context, itemID int64) (Review, error) {
	r := Review{ItemID: itemID}
	err := s.db.QueryRow(ctx, `SELECT phrase, findings, response, created_at FROM vocabulary_reviews WHERE item_id = $1`, itemID).
		Scan(&r.Phrase, &r.Findings, &r.Response, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Review{}, ErrReviewNotFound
	}
	return r, err
}
