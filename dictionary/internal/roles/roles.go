// Package roles manages per-user role grants: "admin" is global (no
// language), "writer" and "reader" always name a language.
package roles

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Role string

const (
	Admin  Role = "admin"
	Writer Role = "writer"
	Reader Role = "reader"
)

func (r Role) Valid() bool {
	return r == Admin || r == Writer || r == Reader
}

var ErrInvalidGrant = errors.New("admin grants must not name a language; writer/reader grants must")

// LanguageCode is nil only for Admin.
type Grant struct {
	ID           int64
	UserID       int64
	Role         Role
	LanguageCode *string
}

type Service struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Service {
	return &Service{db: db}
}

// Granting the same (user, role, language) twice is a no-op.
func (s *Service) Grant(ctx context.Context, userID int64, role Role, languageCode *string) (Grant, error) {
	if !role.Valid() {
		return Grant{}, ErrInvalidGrant
	}
	if (role == Admin) != (languageCode == nil) {
		return Grant{}, ErrInvalidGrant
	}
	var g Grant
	err := s.db.QueryRow(ctx, `
		INSERT INTO role_grants (user_id, role, language_code)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, role, language_code) DO UPDATE SET role = EXCLUDED.role
		RETURNING id, user_id, role, language_code
	`, userID, string(role), languageCode).Scan(&g.ID, &g.UserID, &g.Role, &g.LanguageCode)
	return g, err
}

func (s *Service) Revoke(ctx context.Context, grantID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM role_grants WHERE id = $1`, grantID)
	return err
}

func (s *Service) ForUser(ctx context.Context, userID int64) ([]Grant, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, user_id, role, language_code FROM role_grants
		WHERE user_id = $1
		ORDER BY id
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGrants(rows)
}

func (s *Service) All(ctx context.Context) ([]Grant, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, user_id, role, language_code FROM role_grants
		ORDER BY user_id, id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGrants(rows)
}

func (s *Service) IsAdmin(ctx context.Context, userID int64) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM role_grants WHERE user_id = $1 AND role = 'admin')
	`, userID).Scan(&exists)
	return exists, err
}

// BestForLanguage returns the highest role userID holds that applies to
// languageCode — Admin (global) if held, else the grant naming languageCode
// directly, else ok=false.
func (s *Service) BestForLanguage(ctx context.Context, userID int64, languageCode string) (Role, bool, error) {
	if admin, err := s.IsAdmin(ctx, userID); err != nil {
		return "", false, err
	} else if admin {
		return Admin, true, nil
	}
	var role string
	err := s.db.QueryRow(ctx, `
		SELECT role FROM role_grants
		WHERE user_id = $1 AND language_code = $2
		ORDER BY CASE role WHEN 'writer' THEN 0 ELSE 1 END
		LIMIT 1
	`, userID, languageCode).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return Role(role), true, nil
}

func scanGrants(rows pgx.Rows) ([]Grant, error) {
	grants := make([]Grant, 0)
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.ID, &g.UserID, &g.Role, &g.LanguageCode); err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}
