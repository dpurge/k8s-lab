package texts

import (
	"context"
	"html/template"
	"time"

	"github.com/dpurge/cli-tools/pkg/tool/markdown"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Text is one PhraseForge markdown text.
type Text struct {
	ID            int64
	UserID        int64
	Title         string
	Body          string
	Transcription string // "" if none — cli-tools' "pinned Latin/LTR romanization"
	Language      string
	Script        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Store persists texts in Postgres.
type Store struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

const selectCols = `id, user_id, title, body, coalesce(transcription, ''), language, script, created_at, updated_at`

func scanText(row interface{ Scan(...any) error }, t *Text) error {
	return row.Scan(&t.ID, &t.UserID, &t.Title, &t.Body, &t.Transcription, &t.Language, &t.Script, &t.CreatedAt, &t.UpdatedAt)
}

// List returns texts in the given languages, most recent first. If all is
// true (the caller is admin), languages is ignored and every text is returned.
func (s *Store) List(ctx context.Context, languages []string, all bool) ([]Text, error) {
	var rows pgxRows
	var err error
	if all {
		rows, err = s.db.Query(ctx, `SELECT `+selectCols+` FROM texts ORDER BY created_at DESC`)
	} else {
		if len(languages) == 0 {
			return nil, nil // no language access granted yet — nothing to show
		}
		rows, err = s.db.Query(ctx, `SELECT `+selectCols+` FROM texts WHERE language = ANY($1) ORDER BY created_at DESC`, languages)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Text
	for rows.Next() {
		var t Text
		if err := scanText(rows, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Get fetches one text by id, regardless of language — callers check
// CanView(text.Language) themselves before showing it.
func (s *Store) Get(ctx context.Context, id int64) (Text, error) {
	var t Text
	err := scanText(s.db.QueryRow(ctx, `SELECT `+selectCols+` FROM texts WHERE id = $1`, id), &t)
	return t, err
}

// Create inserts a new text, recording userID as its creator.
func (s *Store) Create(ctx context.Context, userID int64, title, body, transcription, language, script string) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx,
		`INSERT INTO texts (user_id, title, body, transcription, language, script) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		userID, title, body, nullIfEmpty(transcription), language, script).Scan(&id)
	return id, err
}

// Update overwrites a text. Callers check CanEdit(language) — both the
// existing and, if changed, the new language — before calling this.
func (s *Store) Update(ctx context.Context, id int64, title, body, transcription, language, script string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE texts SET title = $1, body = $2, transcription = $3, language = $4, script = $5, updated_at = now() WHERE id = $6`,
		title, body, nullIfEmpty(transcription), language, script, id)
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Delete removes a text by id.
func (s *Store) Delete(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM texts WHERE id = $1`, id)
	return err
}

// RenderHTML converts a text's markdown body to HTML using cli-tools' PhraseForge
// Goldmark extension (vocabulary/dialog/section blocks, CommonMark/GFM otherwise).
func RenderHTML(body string) (template.HTML, error) {
	out, err := markdown.ToHTML([]byte(body))
	if err != nil {
		return "", err
	}
	return template.HTML(out), nil // #nosec G203 -- goldmark output, not raw user HTML
}

// pgxRows is the subset of pgx.Rows this package needs — lets scanText share
// code between Query's row iterator and QueryRow's single-row result.
type pgxRows interface {
	Next() bool
	Scan(...any) error
	Close()
	Err() error
}
