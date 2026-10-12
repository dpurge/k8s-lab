package models

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ErrItemChanged is returned by the Fill* methods when the item still exists
// but its phrase is no longer the one the caller started from.
var ErrItemChanged = errors.New("models item changed")

// ItemSource is what a background job needs from an item when it starts: the
// phrase as it is now, and the source language of the list it is in.
type ItemSource struct {
	Phrase   string
	Language string
}

// ItemSource returns item itemID's current phrase and its list's language, or
// ErrItemNotFound.
func (s *Store) ItemSource(ctx context.Context, itemID int64) (ItemSource, error) {
	var src ItemSource
	err := s.db.QueryRow(ctx, `
		SELECT p.phrase, l.language
		FROM models_items i
		JOIN models_phrases p ON p.id = i.phrase_id
		JOIN models_lists l ON l.id = i.list_id
		WHERE i.id = $1`, itemID).Scan(&src.Phrase, &src.Language)
	if errors.Is(err, pgx.ErrNoRows) {
		return ItemSource{}, ErrItemNotFound
	}
	return src, err
}

// lockItemIfStill mirrors vocabulary's lockItemIfStill — see its doc comment.
func lockItemIfStill(ctx context.Context, tx pgx.Tx, itemID int64, phrase string) (itemPhrase, error) {
	var cur itemPhrase
	err := tx.QueryRow(ctx, `
		SELECT p.id, p.phrase, p.transcription
		FROM models_items i JOIN models_phrases p ON p.id = i.phrase_id
		WHERE i.id = $1
		FOR UPDATE OF i, p`, itemID).Scan(&cur.id, &cur.phrase, &cur.transcription)
	if errors.Is(err, pgx.ErrNoRows) {
		return itemPhrase{}, ErrItemNotFound
	}
	if err != nil {
		return itemPhrase{}, err
	}
	if cur.phrase != phrase {
		return itemPhrase{}, ErrItemChanged
	}
	return cur, nil
}

// FillTranscription mirrors vocabulary.Store.FillTranscription. The
// transcription is part of a models phrase's identity, so this is a change of
// the shared phrase (merging into an identical one if there is one), see
// retargetPhrase.
func (s *Store) FillTranscription(ctx context.Context, itemID int64, phrase, transcription string) (applied bool, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	cur, err := lockItemIfStill(ctx, tx, itemID, phrase)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(cur.transcription) != "" {
		return false, nil
	}
	if _, err := retargetPhrase(ctx, tx, cur.id, cur.phrase, transcription); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// FillTranslation mirrors vocabulary.Store.FillTranslation (models phrases
// have no notes).
func (s *Store) FillTranslation(ctx context.Context, itemID int64, phrase, locale, translation string) (applied bool, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	cur, err := lockItemIfStill(ctx, tx, itemID, phrase)
	if err != nil {
		return false, err
	}

	var existing string
	err = tx.QueryRow(ctx, `SELECT coalesce(translation, '') FROM models_phrase_translation WHERE phrase_id = $1 AND locale = $2 FOR UPDATE`, cur.id, locale).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if strings.TrimSpace(existing) != "" {
		return false, nil
	}

	if translation == "" {
		if _, err := tx.Exec(ctx, `DELETE FROM models_phrase_translation WHERE phrase_id = $1 AND locale = $2`, cur.id, locale); err != nil {
			return false, err
		}
	} else if _, err := tx.Exec(ctx, `
		INSERT INTO models_phrase_translation (phrase_id, locale, translation)
		VALUES ($1, $2, $3)
		ON CONFLICT (phrase_id, locale) DO UPDATE SET
			translation = EXCLUDED.translation, updated_at = now()`,
		cur.id, locale, translation); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
