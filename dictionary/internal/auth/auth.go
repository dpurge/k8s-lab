// Package auth handles user accounts, password credentials, and the
// short-lived bearer tokens minted by POST /auth/token.
//
// Tokens are opaque: the raw value is 32 random bytes, hex-encoded, returned
// to the caller exactly once. Only its sha256 hash is stored, so a database
// read alone never discloses a usable token.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"dictionary/internal/roles"
)

// Scope is the kind of token requested — determines both the TTL and the
// minimum role required to obtain one.
type Scope string

const (
	ScopeWrite Scope = "write"
	ScopeRead  Scope = "read"

	writeTTL = 15 * time.Minute
	readTTL  = 24 * time.Hour
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrUsernameTaken      = errors.New("username already taken")
	ErrInsufficientRole   = errors.New("no grant covers the requested scope/language")
	ErrInvalidScope       = errors.New("scope must be \"write\" or \"read\"")
	ErrTokenInvalid       = errors.New("token is missing, expired, revoked, or unrecognized")
)

// User is a row of the users table (password_hash never leaves this package).
type User struct {
	ID       int64
	Username string
}

// Claims describes what a validated token authorizes.
type Claims struct {
	UserID       int64
	Username     string
	Role         roles.Role
	LanguageCode *string // nil for an admin token
	Scope        Scope
	ExpiresAt    time.Time
}

type Service struct {
	db    *pgxpool.Pool
	roles *roles.Service
}

func New(db *pgxpool.Pool, rolesSvc *roles.Service) *Service {
	return &Service{db: db, roles: rolesSvc}
}

const bcryptCost = bcrypt.DefaultCost

func (s *Service) CreateUser(ctx context.Context, username, password string) (User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return User{}, err
	}
	var u User
	err = s.db.QueryRow(ctx, `
		INSERT INTO users (username, password_hash) VALUES ($1, $2)
		RETURNING id, username
	`, username, string(hash)).Scan(&u.ID, &u.Username)
	if isUniqueViolation(err) {
		return User{}, ErrUsernameTaken
	}
	return u, err
}

// EnsureBootstrapAdmin creates the given admin account only if the users
// table is empty — dev/first-run convenience so there's always a way in.
func (s *Service) EnsureBootstrapAdmin(ctx context.Context, username, password string) error {
	var count int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	u, err := s.CreateUser(ctx, username, password)
	if err != nil {
		return err
	}
	_, err = s.roles.Grant(ctx, u.ID, roles.Admin, nil)
	return err
}

// ListUsers returns every user (admin listing) — password_hash excluded.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.Query(ctx, `SELECT id, username FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]User, 0)
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Service) authenticate(ctx context.Context, username, password string) (User, error) {
	var u User
	var hash string
	err := s.db.QueryRow(ctx, `
		SELECT id, username, password_hash FROM users WHERE username = $1
	`, username).Scan(&u.ID, &u.Username, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, ErrInvalidCredentials
	}
	return u, nil
}

// MintToken returns the raw bearer token — the only time it's available in
// cleartext — plus the claims it carries.
func (s *Service) MintToken(ctx context.Context, username, password string, scope Scope, language *string) (string, Claims, error) {
	if scope != ScopeWrite && scope != ScopeRead {
		return "", Claims{}, ErrInvalidScope
	}
	u, err := s.authenticate(ctx, username, password)
	if err != nil {
		return "", Claims{}, err
	}

	isAdmin, err := s.roles.IsAdmin(ctx, u.ID)
	if err != nil {
		return "", Claims{}, err
	}

	var role roles.Role
	var languageCode *string
	switch {
	case isAdmin:
		role, languageCode = roles.Admin, nil
	case language == nil || *language == "":
		return "", Claims{}, ErrInsufficientRole
	default:
		best, ok, err := s.roles.BestForLanguage(ctx, u.ID, *language)
		if err != nil {
			return "", Claims{}, err
		}
		if !ok {
			return "", Claims{}, ErrInsufficientRole
		}
		// A "write" scope needs the writer grant; "read" accepts writer or reader.
		if scope == ScopeWrite && best != roles.Writer {
			return "", Claims{}, ErrInsufficientRole
		}
		role, languageCode = best, language
	}

	ttl := readTTL
	if scope == ScopeWrite {
		ttl = writeTTL
	}
	expiresAt := time.Now().Add(ttl)

	raw, hash, err := generateToken()
	if err != nil {
		return "", Claims{}, err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO tokens (token_hash, user_id, role, language_code, scope, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, hash, u.ID, string(role), languageCode, string(scope), expiresAt)
	if err != nil {
		return "", Claims{}, err
	}

	return raw, Claims{
		UserID: u.ID, Username: u.Username, Role: role,
		LanguageCode: languageCode, Scope: scope, ExpiresAt: expiresAt,
	}, nil
}

func (s *Service) ValidateToken(ctx context.Context, raw string) (Claims, error) {
	if raw == "" {
		return Claims{}, ErrTokenInvalid
	}
	hash := hashToken(raw)
	var c Claims
	var roleStr, scopeStr string
	err := s.db.QueryRow(ctx, `
		SELECT u.id, u.username, t.role, t.language_code, t.scope, t.expires_at
		FROM tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = $1 AND NOT t.revoked AND t.expires_at > now()
	`, hash).Scan(&c.UserID, &c.Username, &roleStr, &c.LanguageCode, &scopeStr, &c.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Claims{}, ErrTokenInvalid
	}
	if err != nil {
		return Claims{}, err
	}
	c.Role, c.Scope = roles.Role(roleStr), Scope(scopeStr)
	return c, nil
}

func generateToken() (raw, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(buf)
	return raw, hashToken(raw), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
