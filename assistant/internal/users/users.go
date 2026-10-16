// Package users stores the people a task can be run for.
package users

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors a caller can tell apart.
var (
	ErrNotFound        = errors.New("user not found")
	ErrTelegramIDTaken = errors.New("another user already has this telegram id")
	ErrBlankName       = errors.New("display name is blank")
)

// User is one person.
type User struct {
	ID          string    `json:"id"`
	TelegramID  *int64    `json:"telegram_id"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
}

// Store reads and writes users.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a store on pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const columns = `id::text, telegram_id, display_name, created_at`

func scan(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.TelegramID, &u.DisplayName, &u.CreatedAt)
	return u, err
}

// Create adds a user. telegramID may be nil.
func (s *Store) Create(ctx context.Context, displayName string, telegramID *int64) (User, error) {
	name := strings.TrimSpace(displayName)
	if name == "" {
		return User{}, ErrBlankName
	}
	u, err := scan(s.pool.QueryRow(ctx,
		`INSERT INTO users (display_name, telegram_id) VALUES ($1, $2) RETURNING `+columns, name, telegramID))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return User{}, ErrTelegramIDTaken
		}
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}

// Get returns the user with id, or ErrNotFound. An id that is not a UUID
// cannot exist and is also ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (User, error) {
	if !ValidID(id) {
		return User{}, ErrNotFound
	}
	u, err := scan(s.pool.QueryRow(ctx, `SELECT `+columns+` FROM users WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

// List returns every user, oldest first.
func (s *Store) List(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+columns+` FROM users ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		u, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ValidID reports whether s has the shape of a UUID.
func ValidID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
				return false
			}
		}
	}
	return true
}
