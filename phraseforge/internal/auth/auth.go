package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const cookieName = "phraseforge_session"

// User is an authenticated account.
type User struct {
	ID       int64
	Username string
	Locale   string // site UI language, e.g. "en"/"pl" — see internal/i18n
}

// Service checks credentials and issues/reads session cookies.
type Service struct {
	db  *pgxpool.Pool
	key []byte
}

func New(db *pgxpool.Pool, sessionKey string) *Service {
	return &Service{db: db, key: []byte(sessionKey)}
}

var ErrInvalidCredentials = errors.New("invalid username or password")

// Authenticate verifies username/password against the users table.
func (s *Service) Authenticate(ctx context.Context, username, password string) (User, error) {
	var u User
	var hash string
	err := s.db.QueryRow(ctx, `SELECT id, username, locale, password_hash FROM users WHERE username = $1`, username).
		Scan(&u.ID, &u.Username, &u.Locale, &hash)
	if err != nil {
		return User{}, ErrInvalidCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, ErrInvalidCredentials
	}
	return u, nil
}

var ErrUsernameTaken = errors.New("username already taken")

// Register creates a new user account with the given username/password.
// Returns ErrUsernameTaken if the username is already in use.
func (s *Service) Register(ctx context.Context, username, password string) (User, error) {
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT exists(SELECT 1 FROM users WHERE username = $1)`, username).Scan(&exists); err != nil {
		return User{}, err
	}
	if exists {
		return User{}, ErrUsernameTaken
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	var u User
	err = s.db.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ($1, $2) RETURNING id, username, locale`,
		username, string(hash)).Scan(&u.ID, &u.Username, &u.Locale)
	return u, err
}

// SetLocale changes userID's site UI language. Caller validates code against
// i18n.IsValid — this only guards against garbage reaching the DB via the
// CHECK constraint, which would surface as an opaque SQL error otherwise.
func (s *Service) SetLocale(ctx context.Context, userID int64, locale string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET locale = $1 WHERE id = $2`, locale, userID)
	return err
}

// ChangePassword verifies currentPassword against userID's stored hash, then
// replaces it with newPassword. Returns ErrInvalidCredentials if currentPassword
// doesn't match.
func (s *Service) ChangePassword(ctx context.Context, userID int64, currentPassword, newPassword string) error {
	var hash string
	if err := s.db.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, userID).Scan(&hash); err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(currentPassword)) != nil {
		return ErrInvalidCredentials
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE users SET password_hash = $1 WHERE id = $2`, string(newHash), userID)
	return err
}

// ListUsers returns every account, for the admin "manage users" page.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.Query(ctx, `SELECT id, username FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// EnsureUser creates the user if the table is empty (first-run bootstrap),
// mirroring Readeck's "create the first admin" convenience — and grants it the
// site-wide admin role, since without one nobody could ever grant any role.
func (s *Service) EnsureUser(ctx context.Context, username, password string) error {
	var count int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	var userID int64
	if err := s.db.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ($1, $2) RETURNING id`,
		username, string(hash)).Scan(&userID); err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO user_role (user_id, role, language) VALUES ($1, 'admin', NULL)`, userID)
	return err
}

func (s *Service) sign(userID int64) string {
	payload := strconv.FormatInt(userID, 10)
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

func (s *Service) verify(cookie string) (int64, bool) {
	parts := strings.SplitN(cookie, ".", 2)
	if len(parts) != 2 {
		return 0, false
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// SetSession writes the signed session cookie for userID.
func (s *Service) SetSession(w http.ResponseWriter, userID int64) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    s.sign(userID),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(30 * 24 * time.Hour),
	})
}

// ClearSession logs the current user out.
func (s *Service) ClearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
}

// CurrentUser resolves the request's session cookie to a User, if valid.
func (s *Service) CurrentUser(r *http.Request) (User, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return User{}, false
	}
	userID, ok := s.verify(c.Value)
	if !ok {
		return User{}, false
	}
	var u User
	err = s.db.QueryRow(r.Context(), `SELECT id, username, locale FROM users WHERE id = $1`, userID).Scan(&u.ID, &u.Username, &u.Locale)
	if err != nil {
		return User{}, false
	}
	return u, true
}
