// Package languages manages the language catalog and each language's own
// grammar schema (categories + word-class templates).
package languages

import (
	"context"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound     = errors.New("language not found")
	ErrAlreadyExist = errors.New("language already exists")
	ErrInvalidCode  = errors.New("language code must be exactly 3 lowercase letters")
)

// codePattern matches PhraseForge's 3-letter ISO code convention.
var codePattern = regexp.MustCompile(`^[a-z]{3}$`)

func ValidCode(code string) bool {
	return codePattern.MatchString(code)
}

type Language struct {
	Code             string `json:"code"`
	Name             string `json:"name"`
	IsPublic         bool   `json:"is_public"`
	HasTranscription bool   `json:"has_transcription"`
}

type Service struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Service {
	return &Service{db: db}
}

func (s *Service) Create(ctx context.Context, l Language) (Language, error) {
	if !ValidCode(l.Code) {
		return Language{}, ErrInvalidCode
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO languages (code, name, is_public, has_transcription)
		VALUES ($1, $2, $3, $4)
	`, l.Code, l.Name, l.IsPublic, l.HasTranscription)
	if isUniqueViolation(err) {
		return Language{}, ErrAlreadyExist
	}
	if err != nil {
		return Language{}, err
	}
	return l, nil
}

func (s *Service) Update(ctx context.Context, l Language) (Language, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE languages SET name = $2, is_public = $3, has_transcription = $4
		WHERE code = $1
	`, l.Code, l.Name, l.IsPublic, l.HasTranscription)
	if err != nil {
		return Language{}, err
	}
	if tag.RowsAffected() == 0 {
		return Language{}, ErrNotFound
	}
	return l, nil
}

func (s *Service) Get(ctx context.Context, code string) (Language, error) {
	var l Language
	err := s.db.QueryRow(ctx, `
		SELECT code, name, is_public, has_transcription FROM languages WHERE code = $1
	`, code).Scan(&l.Code, &l.Name, &l.IsPublic, &l.HasTranscription)
	if errors.Is(err, pgx.ErrNoRows) {
		return Language{}, ErrNotFound
	}
	return l, err
}

// List returns every language, regardless of its public flag — the catalog
// itself (which languages exist) isn't sensitive; only their content is
// access-gated.
func (s *Service) List(ctx context.Context) ([]Language, error) {
	rows, err := s.db.Query(ctx, `
		SELECT code, name, is_public, has_transcription FROM languages ORDER BY code
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Language, 0)
	for rows.Next() {
		var l Language
		if err := rows.Scan(&l.Code, &l.Name, &l.IsPublic, &l.HasTranscription); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
