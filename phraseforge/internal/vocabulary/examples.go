package vocabulary

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"phraseforge/internal/textnorm"
)

const (
	// MaxExamples is how many usage examples a phrase may have.
	MaxExamples = 5
	// MaxExampleChars is the longest an example may be, in characters (code
	// points, after NFC): a short noun phrase or verb with its object, not a
	// sentence.
	MaxExampleChars = 80
)

// ErrInvalidExamples is returned (wrapped, with the reason) when an examples
// list breaks the limits; nothing is written then.
var ErrInvalidExamples = errors.New("invalid examples")

// NormalizeExamples returns examples as they are stored: each one trimmed and
// NFC-normalised (textnorm.Phrase), blank ones and exact duplicates dropped
// (the first is kept), in the given order. It rejects the list if more than
// MaxExamples remain or one is longer than MaxExampleChars.
func NormalizeExamples(examples []string) ([]string, error) {
	out := make([]string, 0, len(examples))
	seen := make(map[string]bool, len(examples))
	for _, e := range examples {
		e = textnorm.Phrase(e)
		if e == "" || seen[e] {
			continue
		}
		if n := utf8.RuneCountInString(e); n > MaxExampleChars {
			return nil, fmt.Errorf("%w: example %q has %d characters, the limit is %d", ErrInvalidExamples, e, n, MaxExampleChars)
		}
		seen[e] = true
		out = append(out, e)
	}
	if len(out) > MaxExamples {
		return nil, fmt.Errorf("%w: %d examples, the limit is %d", ErrInvalidExamples, len(out), MaxExamples)
	}
	return out, nil
}

// CheckExamples reports whether examples would be accepted by SetItemExamples
// (an ErrInvalidExamples with the reason if not), without writing anything —
// so a caller that does other writes first can refuse a bad list up front.
func CheckExamples(examples []string) error {
	_, err := NormalizeExamples(examples)
	return err
}

// SetItemExamples replaces the examples of the phrase the item at (listID,
// position) links to — so the new list shows in every list that links the
// phrase — with examples, normalised and checked as in NormalizeExamples. An
// empty list clears them. Returns ErrItemNotFound if position doesn't exist.
func (s *Store) SetItemExamples(ctx context.Context, listID int64, position int, examples []string) error {
	stored, err := NormalizeExamples(examples)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	var phraseID int64
	err = tx.QueryRow(ctx, `
		SELECT p.id FROM vocabulary_items i JOIN phrases p ON p.id = i.phrase_id
		WHERE i.list_id = $1 AND i.position = $2
		FOR UPDATE OF p`, listID, position).Scan(&phraseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrItemNotFound
	}
	if err != nil {
		return err
	}
	if err := replaceExamples(ctx, tx, phraseID, stored); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// replaceExamples makes phraseID's examples exactly stored (already
// normalised and within the limits), in order.
func replaceExamples(ctx context.Context, tx pgx.Tx, phraseID int64, stored []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM phrase_examples WHERE phrase_id = $1`, phraseID); err != nil {
		return err
	}
	if len(stored) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO phrase_examples (phrase_id, position, example)
		SELECT $1, (t.ord - 1)::int, t.example FROM unnest($2::text[]) WITH ORDINALITY AS t(example, ord)`,
		phraseID, stored)
	return err
}

// examplesByPhrase returns the examples of each of phraseIDs that has any, in
// position order.
func (s *Store) examplesByPhrase(ctx context.Context, phraseIDs []int64) (map[int64][]string, error) {
	out := map[int64][]string{}
	if len(phraseIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT phrase_id, example FROM phrase_examples
		WHERE phrase_id = ANY($1) ORDER BY phrase_id, position`, phraseIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var example string
		if err := rows.Scan(&id, &example); err != nil {
			return nil, err
		}
		out[id] = append(out[id], example)
	}
	return out, rows.Err()
}

// mergeExamples adds the examples of the phrase being merged away (fromID) to
// those of the phrase that survives (toID): the survivor keeps its own, in
// order, and fromID's that it doesn't already have follow, up to MaxExamples.
func mergeExamples(ctx context.Context, tx pgx.Tx, fromID, toID int64) error {
	_, err := tx.Exec(ctx, `
		WITH have AS (
			SELECT count(*)::int AS n FROM phrase_examples WHERE phrase_id = $2
		), extra AS (
			SELECT e.example, row_number() OVER (ORDER BY e.position) AS rn
			FROM phrase_examples e
			WHERE e.phrase_id = $1
			  AND NOT EXISTS (SELECT 1 FROM phrase_examples s WHERE s.phrase_id = $2 AND s.example = e.example)
		)
		INSERT INTO phrase_examples (phrase_id, position, example)
		SELECT $2, (have.n + extra.rn - 1)::int, extra.example
		FROM extra, have
		WHERE have.n + extra.rn - 1 < $3`, fromID, toID, MaxExamples)
	return err
}
