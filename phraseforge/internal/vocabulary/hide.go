package vocabulary

import (
	"context"
	"fmt"
)

// Hide hides the phrase of the item at position in listID for userID only, in
// every list that contains the phrase. Hiding twice is not an error.
func (s *Store) Hide(ctx context.Context, userID, listID int64, position int) error {
	tag, err := s.db.Exec(ctx,
		`INSERT INTO vocabulary_hidden_phrases (user_id, phrase_id)
		 SELECT $1, phrase_id FROM vocabulary_items WHERE list_id = $2 AND position = $3
		 ON CONFLICT DO NOTHING`, userID, listID, position)
	if err != nil {
		return fmt.Errorf("hide item %d of list %d for user %d: %w", position, listID, userID, err)
	}
	if tag.RowsAffected() == 0 {
		return s.itemExistsOrNotFound(ctx, listID, position)
	}
	return nil
}

// Unhide shows the phrase of the item at position in listID to userID again.
// Unhiding a phrase that is not hidden is not an error.
func (s *Store) Unhide(ctx context.Context, userID, listID int64, position int) error {
	if _, err := s.db.Exec(ctx,
		`DELETE FROM vocabulary_hidden_phrases
		  WHERE user_id = $1
		    AND phrase_id IN (SELECT phrase_id FROM vocabulary_items WHERE list_id = $2 AND position = $3)`,
		userID, listID, position); err != nil {
		return fmt.Errorf("unhide item %d of list %d for user %d: %w", position, listID, userID, err)
	}
	return s.itemExistsOrNotFound(ctx, listID, position)
}

// HiddenPhraseIDs returns which of phraseIDs userID has hidden.
func (s *Store) HiddenPhraseIDs(ctx context.Context, userID int64, phraseIDs []int64) (map[int64]bool, error) {
	hidden := make(map[int64]bool)
	if len(phraseIDs) == 0 {
		return hidden, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT phrase_id FROM vocabulary_hidden_phrases WHERE user_id = $1 AND phrase_id = ANY($2)`, userID, phraseIDs)
	if err != nil {
		return nil, fmt.Errorf("hidden phrases of user %d: %w", userID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		hidden[id] = true
	}
	return hidden, rows.Err()
}

// itemExistsOrNotFound distinguishes "already in the requested state" from
// "no such item" when a statement changed no rows.
func (s *Store) itemExistsOrNotFound(ctx context.Context, listID int64, position int) error {
	var exists bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM vocabulary_items WHERE list_id = $1 AND position = $2)`, listID, position).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrItemNotFound
	}
	return nil
}
