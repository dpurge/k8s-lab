package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrValidation   = errors.New("email and a password of at least 8 characters are required")
	ErrEmailTaken   = errors.New("an account with this email already exists")
	ErrInvalidLogin = errors.New("invalid email or password")
	ErrNoSession    = errors.New("no session")
)

// SessionTTL is how long a session cookie/row stays valid after login.
const SessionTTL = 30 * 24 * time.Hour

type Service struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Service { return &Service{db: db} }

type User struct {
	ID    string
	Email string
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func uuid() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// Signup creates a new account. It does not start a session — call Login next.
func (s *Service) Signup(ctx context.Context, email, password string) (User, error) {
	email = normalizeEmail(email)
	if email == "" || !strings.Contains(email, "@") || len(password) < 8 {
		return User{}, ErrValidation
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	id, err := uuid()
	if err != nil {
		return User{}, err
	}
	_, err = s.db.Exec(ctx, "INSERT INTO users(id,email,password_hash) VALUES($1,$2,$3)", id, email, string(hash))
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return User{}, ErrEmailTaken
		}
		return User{}, err
	}
	return User{ID: id, Email: email}, nil
}

// Login verifies credentials and creates a new session row, returning its token.
func (s *Service) Login(ctx context.Context, email, password string) (string, User, error) {
	email = normalizeEmail(email)
	var u User
	var hash string
	err := s.db.QueryRow(ctx, "SELECT id,email,password_hash FROM users WHERE email=$1", email).Scan(&u.ID, &u.Email, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", User{}, ErrInvalidLogin
	}
	if err != nil {
		return "", User{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return "", User{}, ErrInvalidLogin
	}
	token, err := randomToken()
	if err != nil {
		return "", User{}, err
	}
	_, err = s.db.Exec(ctx, "INSERT INTO sessions(token,user_id,expires_at) VALUES($1,$2,$3)", token, u.ID, time.Now().Add(SessionTTL))
	if err != nil {
		return "", User{}, err
	}
	return token, u, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.db.Exec(ctx, "DELETE FROM sessions WHERE token=$1", token)
	return err
}

// UserForToken resolves a session cookie value to its owning user, rejecting
// missing/unknown/expired sessions uniformly via ErrNoSession.
func (s *Service) UserForToken(ctx context.Context, token string) (User, error) {
	if token == "" {
		return User{}, ErrNoSession
	}
	var u User
	err := s.db.QueryRow(ctx, `
		SELECT users.id, users.email FROM sessions
		JOIN users ON users.id = sessions.user_id
		WHERE sessions.token = $1 AND sessions.expires_at > now()`, token).Scan(&u.ID, &u.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNoSession
	}
	if err != nil {
		return User{}, err
	}
	return u, nil
}
