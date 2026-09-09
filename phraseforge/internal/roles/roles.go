package roles

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	Admin   = "admin"
	Teacher = "teacher"
	Student = "student"
)

// Grant is one row of user_role: a role, scoped to a language (empty for admin).
type Grant struct {
	ID           int64
	UserID       int64
	Username     string
	Role         string
	Language     string // "" for admin
	LanguageName string // "" for admin
}

type Service struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Service {
	return &Service{db: db}
}

// IsAdmin reports whether userID holds the site-wide admin role.
func (s *Service) IsAdmin(ctx context.Context, userID int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx,
		`SELECT exists(SELECT 1 FROM user_role WHERE user_id = $1 AND role = $2)`, userID, Admin).Scan(&ok)
	return ok, err
}

// CanEdit reports whether userID may create/edit texts in the given language:
// true for admin, or a teacher of that language.
func (s *Service) CanEdit(ctx context.Context, userID int64, language string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `
		SELECT exists(
			SELECT 1 FROM user_role
			WHERE user_id = $1 AND (role = $2 OR (role = $3 AND language = $4))
		)`, userID, Admin, Teacher, language).Scan(&ok)
	return ok, err
}

// CanView reports whether userID may read/browse texts in the given language:
// true for admin, teacher, or student of that language.
func (s *Service) CanView(ctx context.Context, userID int64, language string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `
		SELECT exists(
			SELECT 1 FROM user_role
			WHERE user_id = $1 AND (role = $2 OR (role IN ($3, $4) AND language = $5))
		)`, userID, Admin, Teacher, Student, language).Scan(&ok)
	return ok, err
}

// ViewableLanguages returns the language codes userID may read/browse, or nil
// with all=true if userID is admin (every language, without listing them all).
func (s *Service) ViewableLanguages(ctx context.Context, userID int64) (langs []string, all bool, err error) {
	if all, err = s.IsAdmin(ctx, userID); err != nil || all {
		return nil, all, err
	}
	rows, err := s.db.Query(ctx,
		`SELECT DISTINCT language FROM user_role WHERE user_id = $1 AND language IS NOT NULL`, userID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, false, err
		}
		langs = append(langs, l)
	}
	return langs, false, rows.Err()
}

// EditableLanguages returns the language codes userID may create/edit texts
// in, or nil with all=true if userID is admin (every language).
func (s *Service) EditableLanguages(ctx context.Context, userID int64) (langs []string, all bool, err error) {
	if all, err = s.IsAdmin(ctx, userID); err != nil || all {
		return nil, all, err
	}
	rows, err := s.db.Query(ctx,
		`SELECT DISTINCT language FROM user_role WHERE user_id = $1 AND role = $2`, userID, Teacher)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, false, err
		}
		langs = append(langs, l)
	}
	return langs, false, rows.Err()
}

const grantCols = `ur.id, ur.user_id, u.username, ur.role, coalesce(ur.language, ''), coalesce(l.name, '')`
const grantJoins = `FROM user_role ur JOIN users u ON u.id = ur.user_id LEFT JOIN language l ON l.code = ur.language`

func scanGrant(row interface{ Scan(...any) error }, g *Grant) error {
	return row.Scan(&g.ID, &g.UserID, &g.Username, &g.Role, &g.Language, &g.LanguageName)
}

// Grants lists every role grant in the system, joined with username and
// language name, for the admin "manage users" page.
func (s *Service) Grants(ctx context.Context) ([]Grant, error) {
	rows, err := s.db.Query(ctx, `SELECT `+grantCols+` `+grantJoins+` ORDER BY u.username, ur.role, ur.language NULLS FIRST`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Grant
	for rows.Next() {
		var g Grant
		if err := scanGrant(rows, &g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GrantsForUser lists one user's own role grants, for the profile page.
func (s *Service) GrantsForUser(ctx context.Context, userID int64) ([]Grant, error) {
	rows, err := s.db.Query(ctx, `SELECT `+grantCols+` `+grantJoins+` WHERE ur.user_id = $1 ORDER BY ur.role, ur.language NULLS FIRST`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Grant
	for rows.Next() {
		var g Grant
		if err := scanGrant(rows, &g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Grant assigns role to userID, scoped to language ("" for admin).
func (s *Service) Grant(ctx context.Context, userID int64, role, language string) error {
	var lang any
	if language != "" {
		lang = language
	}
	_, err := s.db.Exec(ctx,
		`INSERT INTO user_role (user_id, role, language) VALUES ($1, $2, $3)
		 ON CONFLICT DO NOTHING`, userID, role, lang)
	return err
}

// Revoke removes one role grant by its id.
func (s *Service) Revoke(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM user_role WHERE id = $1`, id)
	return err
}
