// Package resreview stores the model's review of a text or dialog (see
// specs/features/phraseforge-text-dialog-review-job.md) and applies or
// dismisses its proposals. It reads and writes the texts, dialogs and
// resource_translation tables directly: a review only ever overwrites a
// transcription or one translation, each guarded by the value it was about.
// It satisfies ai.TextReviewStore.
package resreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phraseforge/internal/ai"
	"phraseforge/internal/textnorm"
	"phraseforge/internal/vocabulary"
)

// ErrNotFound is returned for a resource that has no review.
var ErrNotFound = errors.New("review not found")

// maxVocabulary bounds the attached vocabulary given to a review as context.
const maxVocabulary = 100

type Store struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

// table maps a resource type to its table; the names are constants, never
// taken from a request.
func table(resourceType string) (string, error) {
	switch resourceType {
	case "text":
		return "texts", nil
	case "dialog":
		return "dialogs", nil
	}
	return "", fmt.Errorf("resreview: unknown resource_type %q", resourceType)
}

// versionSQL is the hash of a resource's source text, computed by the database
// so the review and the guard always agree on it.
const versionSQL = `encode(sha256(convert_to(body, 'UTF8')), 'hex')`

// TextReviewSource reads a text or dialog for a review: its language, source,
// transcription and translations, the version of its source, and the
// vocabulary list generated from it (up to maxVocabulary phrases).
func (s *Store) TextReviewSource(ctx context.Context, resourceType string, id int64) (ai.TextReviewInput, error) {
	tbl, err := table(resourceType)
	if err != nil {
		return ai.TextReviewInput{}, err
	}
	var in ai.TextReviewInput
	err = s.db.QueryRow(ctx, `SELECT language, body, coalesce(transcription, ''), `+versionSQL+` FROM `+tbl+` WHERE id = $1`, id).
		Scan(&in.Language, &in.Body, &in.Transcription, &in.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return ai.TextReviewInput{}, ai.ErrItemGone
	}
	if err != nil {
		return ai.TextReviewInput{}, err
	}
	rows, err := s.db.Query(ctx, `SELECT locale, body FROM resource_translation WHERE resource_type = $1 AND resource_id = $2 AND btrim(body) <> '' ORDER BY locale`, resourceType, id)
	if err != nil {
		return ai.TextReviewInput{}, err
	}
	for rows.Next() {
		var t ai.ReviewTranslation
		if err := rows.Scan(&t.Locale, &t.Translation); err != nil {
			rows.Close()
			return ai.TextReviewInput{}, err
		}
		in.Translations = append(in.Translations, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ai.TextReviewInput{}, err
	}

	column := "source_text_id"
	if resourceType == "dialog" {
		column = "source_dialog_id"
	}
	vrows, err := s.db.Query(ctx, `
		SELECT p.phrase, coalesce(pt.locale, ''), coalesce(pt.translation, '')
		FROM vocabulary_lists l
		JOIN vocabulary_items i ON i.list_id = l.id
		JOIN phrases p ON p.id = i.phrase_id
		LEFT JOIN phrase_translation pt ON pt.phrase_id = p.id AND btrim(coalesce(pt.translation, '')) <> ''
		WHERE l.`+column+` = $1
		ORDER BY i.position, pt.locale`, id)
	if err != nil {
		return ai.TextReviewInput{}, err
	}
	defer vrows.Close()
	for vrows.Next() {
		var phrase, locale, translation string
		if err := vrows.Scan(&phrase, &locale, &translation); err != nil {
			return ai.TextReviewInput{}, err
		}
		last := len(in.Vocabulary) - 1
		if last < 0 || in.Vocabulary[last].Phrase != phrase {
			if len(in.Vocabulary) == maxVocabulary {
				break
			}
			in.Vocabulary = append(in.Vocabulary, ai.TextReviewVocabulary{Phrase: phrase})
			last++
		}
		if locale != "" {
			if in.Vocabulary[last].Translations == nil {
				in.Vocabulary[last].Translations = map[string]string{}
			}
			in.Vocabulary[last].Translations[locale] = translation
		}
	}
	return in, vrows.Err()
}

// SaveTextReview stores findings and the model's response as the resource's
// review, replacing the previous one, only while its source text still has
// version (ai.ErrItemChanged otherwise, ai.ErrItemGone if it is deleted).
func (s *Store) SaveTextReview(ctx context.Context, resourceType string, id int64, part, version string, findings, response json.RawMessage) error {
	tbl, err := table(resourceType)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds
	if err := lockVersion(ctx, tx, tbl, id, version); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO resource_reviews (resource_type, resource_id, part, version, findings, response) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (resource_type, resource_id, part) DO UPDATE
		SET version = excluded.version, findings = excluded.findings, response = excluded.response, created_at = now()`,
		resourceType, id, part, version, findings, response); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// lockVersion locks the resource row and checks its source still has version.
func lockVersion(ctx context.Context, tx pgx.Tx, tbl string, id int64, version string) error {
	var current string
	err := tx.QueryRow(ctx, `SELECT `+versionSQL+` FROM `+tbl+` WHERE id = $1 FOR UPDATE`, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ai.ErrItemGone
	}
	if err != nil {
		return err
	}
	if current != version {
		return ai.ErrItemChanged
	}
	return nil
}

// Review is a resource's stored review.
type Review struct {
	Version   string
	Findings  []vocabulary.ReviewFinding
	CreatedAt time.Time
	// Stale is true when the source text was edited after the review.
	Stale bool
}

// Get returns the resource's review of part, or ErrNotFound.
func (s *Store) Get(ctx context.Context, resourceType string, id int64, part string) (Review, error) {
	tbl, err := table(resourceType)
	if err != nil {
		return Review{}, err
	}
	var r Review
	var raw json.RawMessage
	err = s.db.QueryRow(ctx, `SELECT version, findings, created_at FROM resource_reviews WHERE resource_type = $1 AND resource_id = $2 AND part = $3`, resourceType, id, part).
		Scan(&r.Version, &raw, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Review{}, ErrNotFound
	}
	if err != nil {
		return Review{}, err
	}
	if err := json.Unmarshal(raw, &r.Findings); err != nil {
		return Review{}, fmt.Errorf("stored review of %s %d: %w", resourceType, id, err)
	}
	var current string
	if err := s.db.QueryRow(ctx, `SELECT `+versionSQL+` FROM `+tbl+` WHERE id = $1`, id).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Review{}, ErrNotFound
		}
		return Review{}, err
	}
	r.Stale = current != r.Version
	return r, nil
}

// selectFindings turns requested indexes into the findings to act on: none
// requested means every open one; a number outside the review is
// vocabulary.ErrInvalidReviewIndex.
func selectFindings(findings []vocabulary.ReviewFinding, indexes []int) ([]int, error) {
	if len(indexes) == 0 {
		var all []int
		for i, f := range findings {
			if f.Status == vocabulary.ReviewStatusOpen {
				all = append(all, i)
			}
		}
		return all, nil
	}
	seen := map[int]bool{}
	var out []int
	for _, i := range indexes {
		if i < 0 || i >= len(findings) {
			return nil, fmt.Errorf("%w: %d", vocabulary.ErrInvalidReviewIndex, i)
		}
		if !seen[i] {
			seen[i] = true
			out = append(out, i)
		}
	}
	slices.Sort(out)
	return out, nil
}

func lockReview(ctx context.Context, tx pgx.Tx, resourceType string, id int64, part string) (version string, findings []vocabulary.ReviewFinding, err error) {
	var raw json.RawMessage
	err = tx.QueryRow(ctx, `SELECT version, findings FROM resource_reviews WHERE resource_type = $1 AND resource_id = $2 AND part = $3 FOR UPDATE`, resourceType, id, part).Scan(&version, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	if err := json.Unmarshal(raw, &findings); err != nil {
		return "", nil, fmt.Errorf("stored review of %s %d: %w", resourceType, id, err)
	}
	return version, findings, nil
}

func saveFindings(ctx context.Context, tx pgx.Tx, resourceType string, id int64, part string, findings []vocabulary.ReviewFinding) error {
	raw, err := json.Marshal(findings)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE resource_reviews SET findings = $4 WHERE resource_type = $1 AND resource_id = $2 AND part = $3`, resourceType, id, part, raw)
	return err
}

// Dismiss marks the selected open findings dismissed (every open one when
// indexes is empty).
func (s *Store) Dismiss(ctx context.Context, resourceType string, id int64, part string, indexes []int) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds
	_, findings, err := lockReview(ctx, tx, resourceType, id, part)
	if err != nil {
		return err
	}
	selected, err := selectFindings(findings, indexes)
	if err != nil {
		return err
	}
	for _, i := range selected {
		if findings[i].Status == vocabulary.ReviewStatusOpen {
			findings[i].Status = vocabulary.ReviewStatusDismissed
		}
	}
	if err := saveFindings(ctx, tx, resourceType, id, part, findings); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Apply applies the selected open findings (every open one when indexes is
// empty) in one transaction. The resource's source text must still be the
// reviewed one (ai.ErrItemChanged otherwise); each finding is further guarded
// by the value it was about: an edited one is a conflict, nothing is written
// and the finding stays open. Remarks are not applicable.
func (s *Store) Apply(ctx context.Context, resourceType string, id int64, part string, indexes []int) ([]vocabulary.ReviewResult, error) {
	tbl, err := table(resourceType)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds
	version, findings, err := lockReview(ctx, tx, resourceType, id, part)
	if err != nil {
		return nil, err
	}
	if err := lockVersion(ctx, tx, tbl, id, version); err != nil {
		return nil, err
	}
	selected, err := selectFindings(findings, indexes)
	if err != nil {
		return nil, err
	}
	results := make([]vocabulary.ReviewResult, 0, len(selected))
	for _, i := range selected {
		f := findings[i]
		outcome := vocabulary.ReviewNotApplicable
		if f.Status == vocabulary.ReviewStatusOpen && f.Action == vocabulary.ReviewActionReplace {
			applied, err := applyFinding(ctx, tx, tbl, resourceType, id, f)
			if err != nil {
				return nil, err
			}
			outcome = vocabulary.ReviewConflict
			if applied {
				outcome = vocabulary.ReviewApplied
				findings[i].Status = vocabulary.ReviewStatusApplied
			}
		}
		results = append(results, vocabulary.ReviewResult{Index: i, Outcome: outcome})
	}
	if err := saveFindings(ctx, tx, resourceType, id, part, findings); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return results, nil
}

// applyFinding overwrites the transcription or one translation if it still is
// the reviewed value.
func applyFinding(ctx context.Context, tx pgx.Tx, tbl, resourceType string, id int64, f vocabulary.ReviewFinding) (bool, error) {
	suggestion := textnorm.Phrase(f.Suggestion)
	if suggestion == "" {
		return false, nil
	}
	var tag interface{ RowsAffected() int64 }
	var err error
	switch f.Field {
	case vocabulary.ReviewFieldTranscription:
		tag, err = tx.Exec(ctx, `UPDATE `+tbl+` SET transcription = $2, updated_at = now() WHERE id = $1 AND coalesce(transcription, '') = $3`, id, suggestion, f.Current)
	case vocabulary.ReviewFieldTranslation:
		tag, err = tx.Exec(ctx, `UPDATE resource_translation SET body = $4, updated_at = now() WHERE resource_type = $1 AND resource_id = $2 AND locale = $3 AND body = $5`,
			resourceType, id, f.Locale, suggestion, f.Current)
	default:
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
