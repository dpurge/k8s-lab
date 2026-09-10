// Package entries manages dictionary entries, their translations, and notes.
//
// An entry's identity is (language, phrase, grammar tags) — the same phrase
// with different tags is a different entry. One entry may have several
// translations; a translation's notes (markdown) belong to that specific
// (entry, translation) pair.
package entries

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"dictionary/internal/languages"
)

var (
	ErrNotFound          = errors.New("entry not found")
	ErrAlreadyExists     = errors.New("an entry with this phrase and these grammar tags already exists")
	ErrTranslationExists = errors.New("an identical translation already exists for this entry")
	ErrValidation        = errors.New("validation failed")
	ErrUnknownLanguage   = errors.New("unknown target language")
)

// Entry is a row of the entries table. Tags is stored (and returned) in
// canonical order — see languages.Schema.ValidateAndCanonicalizeTags.
type Entry struct {
	ID           int64    `json:"id"`
	LanguageCode string   `json:"language_code"`
	Phrase       string   `json:"phrase"`
	Tags         []string `json:"tags"`
}

type Translation struct {
	ID           int64   `json:"id"`
	EntryID      int64   `json:"entry_id"`
	LanguageCode string  `json:"language_code"`
	Text         string  `json:"text"`
	Notes        *string `json:"notes,omitempty"`
}

type Service struct {
	db        *pgxpool.Pool
	languages *languages.Service
}

func New(db *pgxpool.Pool, languagesSvc *languages.Service) *Service {
	return &Service{db: db, languages: languagesSvc}
}

func validateOneLine(field, s string) error {
	if s == "" || strings.ContainsAny(s, "\r\n") {
		return fmt.Errorf("%w: %s must be a non-empty single line", ErrValidation, field)
	}
	return nil
}

// Create validates phrase/tags against languageCode's grammar schema, then
// inserts a new entry. tags need not be pre-sorted or word-class-first — they
// are canonicalized before storage.
func (s *Service) Create(ctx context.Context, languageCode, phrase string, tags []string) (Entry, error) {
	if err := validateOneLine("phrase", phrase); err != nil {
		return Entry{}, err
	}
	canonical, err := s.canonicalizeTags(ctx, languageCode, tags)
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	err = s.db.QueryRow(ctx, `
		INSERT INTO entries (language_code, phrase, tags) VALUES ($1, $2, $3)
		RETURNING id, language_code, phrase, tags
	`, languageCode, phrase, canonical).Scan(&e.ID, &e.LanguageCode, &e.Phrase, &e.Tags)
	if isUniqueViolation(err) {
		return Entry{}, ErrAlreadyExists
	}
	return e, err
}

// Get fetches one entry, scoped to languageCode so an id can't be used to
// read an entry belonging to a different language.
func (s *Service) Get(ctx context.Context, languageCode string, id int64) (Entry, error) {
	var e Entry
	err := s.db.QueryRow(ctx, `
		SELECT id, language_code, phrase, tags FROM entries WHERE id = $1 AND language_code = $2
	`, id, languageCode).Scan(&e.ID, &e.LanguageCode, &e.Phrase, &e.Tags)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	return e, err
}

// List returns languageCode's entries, optionally filtered to one phrase
// (a phrase may have several entries, one per distinct tag set).
func (s *Service) List(ctx context.Context, languageCode string, phrase *string) ([]Entry, error) {
	var rows pgx.Rows
	var err error
	if phrase != nil {
		rows, err = s.db.Query(ctx, `
			SELECT id, language_code, phrase, tags FROM entries
			WHERE language_code = $1 AND phrase = $2 ORDER BY id
		`, languageCode, *phrase)
	} else {
		rows, err = s.db.Query(ctx, `
			SELECT id, language_code, phrase, tags FROM entries
			WHERE language_code = $1 ORDER BY id
		`, languageCode)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Entry, 0)
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.LanguageCode, &e.Phrase, &e.Tags); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Service) Update(ctx context.Context, languageCode string, id int64, phrase string, tags []string) (Entry, error) {
	if err := validateOneLine("phrase", phrase); err != nil {
		return Entry{}, err
	}
	canonical, err := s.canonicalizeTags(ctx, languageCode, tags)
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	err = s.db.QueryRow(ctx, `
		UPDATE entries SET phrase = $1, tags = $2 WHERE id = $3 AND language_code = $4
		RETURNING id, language_code, phrase, tags
	`, phrase, canonical, id, languageCode).Scan(&e.ID, &e.LanguageCode, &e.Phrase, &e.Tags)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Entry{}, ErrNotFound
	case isUniqueViolation(err):
		return Entry{}, ErrAlreadyExists
	}
	return e, err
}

// ON DELETE CASCADE also removes the entry's translations.
func (s *Service) Delete(ctx context.Context, languageCode string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM entries WHERE id = $1 AND language_code = $2`, id, languageCode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) canonicalizeTags(ctx context.Context, languageCode string, tags []string) ([]string, error) {
	sch, err := s.languages.GetSchema(ctx, languageCode)
	if err != nil {
		return nil, err
	}
	_, canonical, err := sch.ValidateAndCanonicalizeTags(tags)
	return canonical, err
}

// ── Translations ─────────────────────────────────────────────────────────────

func (s *Service) AddTranslation(ctx context.Context, entryID int64, targetLanguage, text string, notes *string) (Translation, error) {
	if err := validateOneLine("text", text); err != nil {
		return Translation{}, err
	}
	if _, err := s.languages.Get(ctx, targetLanguage); errors.Is(err, languages.ErrNotFound) {
		return Translation{}, ErrUnknownLanguage
	} else if err != nil {
		return Translation{}, err
	}
	var t Translation
	err := s.db.QueryRow(ctx, `
		INSERT INTO translations (entry_id, language_code, text, notes) VALUES ($1, $2, $3, $4)
		RETURNING id, entry_id, language_code, text, notes
	`, entryID, targetLanguage, text, notes).Scan(&t.ID, &t.EntryID, &t.LanguageCode, &t.Text, &t.Notes)
	if isUniqueViolation(err) {
		return Translation{}, ErrTranslationExists
	}
	return t, err
}

func (s *Service) TranslationsForEntry(ctx context.Context, entryID int64) ([]Translation, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, entry_id, language_code, text, notes FROM translations
		WHERE entry_id = $1 ORDER BY id
	`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Translation, 0)
	for rows.Next() {
		var t Translation
		if err := rows.Scan(&t.ID, &t.EntryID, &t.LanguageCode, &t.Text, &t.Notes); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateTranslation replaces a translation's text/notes, scoped to entryID so
// a translation belonging to a different entry can't be reached by guessing IDs.
func (s *Service) UpdateTranslation(ctx context.Context, entryID, translationID int64, text string, notes *string) (Translation, error) {
	if err := validateOneLine("text", text); err != nil {
		return Translation{}, err
	}
	var t Translation
	err := s.db.QueryRow(ctx, `
		UPDATE translations SET text = $1, notes = $2 WHERE id = $3 AND entry_id = $4
		RETURNING id, entry_id, language_code, text, notes
	`, text, notes, translationID, entryID).Scan(&t.ID, &t.EntryID, &t.LanguageCode, &t.Text, &t.Notes)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Translation{}, ErrNotFound
	case isUniqueViolation(err):
		return Translation{}, ErrTranslationExists
	}
	return t, err
}

func (s *Service) DeleteTranslation(ctx context.Context, entryID, translationID int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM translations WHERE id = $1 AND entry_id = $2`, translationID, entryID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
