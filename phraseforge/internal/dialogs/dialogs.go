package dialogs

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Dialog is one PhraseForge markdown dialog.
type Dialog struct {
	ID            int64
	UserID        int64
	Title         string
	Body          string
	Transcription string
	Language      string
	Script        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Store persists dialogs in Postgres.
type Store struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

const selectCols = `id, user_id, title, body, coalesce(transcription, ''), language, script, created_at, updated_at`

func scanDialog(row interface{ Scan(...any) error }, d *Dialog) error {
	return row.Scan(&d.ID, &d.UserID, &d.Title, &d.Body, &d.Transcription, &d.Language, &d.Script, &d.CreatedAt, &d.UpdatedAt)
}

// List returns dialogs in the given languages, most recent first. If all is
// true (the caller is admin), languages is ignored and every dialog is returned.
func (s *Store) List(ctx context.Context, languages []string, all bool) ([]Dialog, error) {
	var rows pgxRows
	var err error
	if all {
		rows, err = s.db.Query(ctx, `SELECT `+selectCols+` FROM dialogs ORDER BY created_at DESC`)
	} else {
		if len(languages) == 0 {
			return nil, nil // no language access granted yet — nothing to show
		}
		rows, err = s.db.Query(ctx, `SELECT `+selectCols+` FROM dialogs WHERE language = ANY($1) ORDER BY created_at DESC`, languages)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Dialog
	for rows.Next() {
		var d Dialog
		if err := scanDialog(rows, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Get fetches one dialog by id, regardless of language — callers check
// CanView(dialog.Language) themselves before showing it.
func (s *Store) Get(ctx context.Context, id int64) (Dialog, error) {
	var d Dialog
	err := scanDialog(s.db.QueryRow(ctx, `SELECT `+selectCols+` FROM dialogs WHERE id = $1`, id), &d)
	return d, err
}

// Create inserts a new dialog, recording userID as its creator.
func (s *Store) Create(ctx context.Context, userID int64, title, body, transcription, language, script string) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx,
		`INSERT INTO dialogs (user_id, title, body, transcription, language, script) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		userID, title, body, nullIfEmpty(transcription), language, script).Scan(&id)
	return id, err
}

// Update overwrites a dialog. Callers check CanEdit(language) — both the
// existing and, if changed, the new language — before calling this.
func (s *Store) Update(ctx context.Context, id int64, title, body, transcription, language, script string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE dialogs SET title = $1, body = $2, transcription = $3, language = $4, script = $5, updated_at = now() WHERE id = $6`,
		title, body, nullIfEmpty(transcription), language, script, id)
	return err
}

// Delete removes a dialog by id.
func (s *Store) Delete(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM dialogs WHERE id = $1`, id)
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// pgxRows is the subset of pgx.Rows this package needs — lets scanDialog
// share code between Query's row iterator and QueryRow's single-row result.
type pgxRows interface {
	Next() bool
	Scan(...any) error
	Close()
	Err() error
}
