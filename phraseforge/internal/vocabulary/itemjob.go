package vocabulary

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"phraseforge/internal/textnorm"
)

// ErrItemChanged is returned by the Fill* methods when the item still exists
// but its phrase is no longer the one the caller started from.
var ErrItemChanged = errors.New("vocabulary item changed")

// ItemSource is what a background job needs from an item when it starts: the
// phrase as it is now, the source language of the list it is in, and the
// phrase's usage examples (in order, nil when it has none) and its non-blank
// translations by site locale code (nil when it has none).
type ItemSource struct {
	Phrase       string
	Language     string
	Examples     []string
	Translations map[string]string
}

// ItemSource returns item itemID's current phrase, its list's language and the
// phrase's examples, or ErrItemNotFound.
func (s *Store) ItemSource(ctx context.Context, itemID int64) (ItemSource, error) {
	var src ItemSource
	var phraseID int64
	err := s.db.QueryRow(ctx, `
		SELECT p.id, p.phrase, l.language
		FROM vocabulary_items i
		JOIN phrases p ON p.id = i.phrase_id
		JOIN vocabulary_lists l ON l.id = i.list_id
		WHERE i.id = $1`, itemID).Scan(&phraseID, &src.Phrase, &src.Language)
	if errors.Is(err, pgx.ErrNoRows) {
		return ItemSource{}, ErrItemNotFound
	}
	if err != nil {
		return ItemSource{}, err
	}
	examples, err := s.examplesByPhrase(ctx, []int64{phraseID})
	if err != nil {
		return ItemSource{}, err
	}
	src.Examples = examples[phraseID]
	if src.Translations, err = s.translationsByPhrase(ctx, phraseID); err != nil {
		return ItemSource{}, err
	}
	return src, nil
}

// translationsByPhrase returns phraseID's non-blank translations by locale, nil
// when it has none.
func (s *Store) translationsByPhrase(ctx context.Context, phraseID int64) (map[string]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT locale, translation FROM phrase_translation
		WHERE phrase_id = $1 AND btrim(coalesce(translation, '')) <> ''`, phraseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out map[string]string
	for rows.Next() {
		var locale, translation string
		if err := rows.Scan(&locale, &translation); err != nil {
			return nil, err
		}
		if out == nil {
			out = map[string]string{}
		}
		out[locale] = translation
	}
	return out, rows.Err()
}

// lockItemIfStill locks item itemID and the phrase it links to, and guards the
// write that follows: a background job is made for an item and the phrase it
// had when the job started, and by the time it writes, the item may be gone
// (ErrItemNotFound) or hold another phrase (ErrItemChanged). Writing the
// result to a different phrase would corrupt it, so the caller must fail or
// skip the job instead. Following the item by id, not by position, is what
// lets it survive the list being reordered or regenerated meanwhile.
func lockItemIfStill(ctx context.Context, tx pgx.Tx, itemID int64, phrase string) (itemPhrase, error) {
	var cur itemPhrase
	err := tx.QueryRow(ctx, `
		SELECT p.id, p.phrase, p.grammar, p.transcription
		FROM vocabulary_items i JOIN phrases p ON p.id = i.phrase_id
		WHERE i.id = $1
		FOR UPDATE OF i, p`, itemID).Scan(&cur.id, &cur.phrase, &cur.grammar, &cur.transcription)
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

// FillTranscription writes transcription to item itemID's phrase, but only if
// the item still has phrase and its transcription is still blank (applied is
// false, not an error, when it was already filled). The check and the write
// are one transaction, so a concurrent edit can't slip in between.
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
	if _, err := retargetPhrase(ctx, tx, cur.id, cur.phrase, cur.grammar, transcription); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// FillGrammar is FillTranscription for the item's grammar tags.
func (s *Store) FillGrammar(ctx context.Context, itemID int64, phrase, grammar string) (applied bool, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	cur, err := lockItemIfStill(ctx, tx, itemID, phrase)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(cur.grammar) != "" {
		return false, nil
	}
	if _, err := retargetPhrase(ctx, tx, cur.id, cur.phrase, grammar, cur.transcription); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// FillTranslation stores translation and notes for locale on item itemID's
// phrase, but only if the item still has phrase and the phrase has no
// translation for that locale yet (applied is false, not an error, when it
// has). Empty translation and notes clear the locale's row instead.
func (s *Store) FillTranslation(ctx context.Context, itemID int64, phrase, locale, translation, notes string) (applied bool, err error) {
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
	err = tx.QueryRow(ctx, `SELECT coalesce(translation, '') FROM phrase_translation WHERE phrase_id = $1 AND locale = $2 FOR UPDATE`, cur.id, locale).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if strings.TrimSpace(existing) != "" {
		return false, nil
	}

	if translation == "" && notes == "" {
		if _, err := tx.Exec(ctx, `DELETE FROM phrase_translation WHERE phrase_id = $1 AND locale = $2`, cur.id, locale); err != nil {
			return false, err
		}
	} else if _, err := tx.Exec(ctx, `
		INSERT INTO phrase_translation (phrase_id, locale, translation, notes)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (phrase_id, locale) DO UPDATE SET
			translation = EXCLUDED.translation, notes = EXCLUDED.notes, updated_at = now()`,
		cur.id, locale, nullIfEmpty(translation), nullIfEmpty(notes)); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// AppendSense adds sense, a new meaning of the phrase of item itemID in locale,
// to the end of the locale's existing translation: "old; sense", the old text
// kept as the prefix and the senses joined with "; ". A sense holding several
// meanings separated by ";" adds only those the translation lacks. It applies
// only if the item still has phrase, the locale's translation is not blank and
// something is new (see withSense); applied is false, not an error, otherwise.
// Notes are left alone. A different item or a vanished one is ErrItemChanged or
// ErrItemNotFound.
func (s *Store) AppendSense(ctx context.Context, itemID int64, phrase, locale, sense string) (applied bool, err error) {
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
	err = tx.QueryRow(ctx, `SELECT coalesce(translation, '') FROM phrase_translation WHERE phrase_id = $1 AND locale = $2 FOR UPDATE`, cur.id, locale).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	joined, ok := withSense(existing, sense)
	if !ok {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE phrase_translation SET translation = $3, updated_at = now() WHERE phrase_id = $1 AND locale = $2`, cur.id, locale, joined); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// withSense returns existing with the new senses of sense appended after "; ",
// and whether any was: sense may itself hold several senses separated by ";",
// of which only those not already in existing (nor repeated within sense) are
// added; none is added when existing is blank or nothing is new. Senses are
// compared trimmed, NFC-normalised and ignoring case.
func withSense(existing, sense string) (string, bool) {
	existing = strings.TrimRight(strings.TrimSpace(existing), "; ")
	if existing == "" {
		return "", false
	}
	have := splitSenses(existing)
	joined := existing
	added := false
	for _, candidate := range splitSenses(sense) {
		if containsFold(have, candidate) {
			continue
		}
		have = append(have, candidate)
		joined += "; " + candidate
		added = true
	}
	if !added {
		return "", false
	}
	return joined, true
}

// splitSenses splits text on ";" into its trimmed, NFC-normalised, non-blank parts.
func splitSenses(text string) []string {
	var out []string
	for _, part := range strings.Split(text, ";") {
		if sense := textnorm.Phrase(part); sense != "" {
			out = append(out, sense)
		}
	}
	return out
}

func containsFold(list []string, sense string) bool {
	for _, have := range list {
		if strings.EqualFold(have, sense) {
			return true
		}
	}
	return false
}

// FillExample adds example to the phrase of item itemID, but only if the item
// still has phrase, the phrase has fewer than MaxExamples examples and does not
// have this one already (compared after the same trim and NFC normalisation as
// every stored example): applied is false, not an error, otherwise, and also
// for a blank example. An example over MaxExampleChars is ErrInvalidExamples.
// The new example goes last, so the order of the existing ones is kept.
func (s *Store) FillExample(ctx context.Context, itemID int64, phrase, example string) (applied bool, err error) {
	normalised, err := NormalizeExamples([]string{example})
	if err != nil {
		return false, err
	}
	if len(normalised) == 0 {
		return false, nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	// The phrase row is locked, so two jobs adding to the same phrase take turns.
	cur, err := lockItemIfStill(ctx, tx, itemID, phrase)
	if err != nil {
		return false, err
	}
	var existing int
	var duplicate bool
	if err := tx.QueryRow(ctx, `
		SELECT count(*), coalesce(bool_or(example = $2), false) FROM phrase_examples WHERE phrase_id = $1`,
		cur.id, normalised[0]).Scan(&existing, &duplicate); err != nil {
		return false, err
	}
	if duplicate || existing >= MaxExamples {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO phrase_examples (phrase_id, position, example) VALUES ($1, $2, $3)`,
		cur.id, existing, normalised[0]); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// RewritePhrase changes the phrase of item itemID from phrase to newPhrase,
// together with its grammar and transcription (an empty one keeps the stored
// value), as one change through retargetPhrase: the same path as a manual edit,
// so every list that links the phrase sees it, translations, notes and examples
// stay with it, and a collision with an existing phrase of exactly the new
// values merges into that one. The item keeps its id. The item must still hold
// phrase (ErrItemChanged otherwise, ErrItemNotFound if it is gone); a newPhrase
// blank after trim and NFC is ErrBlankPhrase, and one equal to phrase changes
// nothing (applied is false, not an error).
func (s *Store) RewritePhrase(ctx context.Context, itemID int64, phrase, newPhrase, grammar, transcription string) (applied bool, err error) {
	newPhrase = textnorm.Phrase(newPhrase)
	if newPhrase == "" {
		return false, ErrBlankPhrase
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	cur, err := lockItemIfStill(ctx, tx, itemID, phrase)
	if err != nil {
		return false, err
	}
	if newPhrase == cur.phrase {
		return false, nil
	}
	if strings.TrimSpace(grammar) == "" {
		grammar = cur.grammar
	}
	if strings.TrimSpace(transcription) == "" {
		transcription = cur.transcription
	}
	if _, err := retargetPhrase(ctx, tx, cur.id, newPhrase, grammar, transcription); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
