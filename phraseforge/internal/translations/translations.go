package translations

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

// Get returns resourceID's translation body for locale, and whether one exists.
func (s *Store) Get(ctx context.Context, resourceType string, resourceID int64, locale string) (string, bool, error) {
	var body string
	err := s.db.QueryRow(ctx,
		`SELECT body FROM resource_translation WHERE resource_type = $1 AND resource_id = $2 AND locale = $3`,
		resourceType, resourceID, locale).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return body, true, nil
}

// Set writes resourceID's translation for locale. An empty body deletes it
// instead — that's how an editor clears a translation they no longer want.
func (s *Store) Set(ctx context.Context, resourceType string, resourceID int64, locale, body string) error {
	if body == "" {
		_, err := s.db.Exec(ctx,
			`DELETE FROM resource_translation WHERE resource_type = $1 AND resource_id = $2 AND locale = $3`,
			resourceType, resourceID, locale)
		return err
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO resource_translation (resource_type, resource_id, locale, body) VALUES ($1, $2, $3, $4)
		ON CONFLICT (resource_type, resource_id, locale) DO UPDATE SET body = EXCLUDED.body, updated_at = now()`,
		resourceType, resourceID, locale, body)
	return err
}
