// Package chat is the assistant's conversations: what a user and the assistant
// said to each other, and the workflow runs that make the assistant's side. The
// channel that carries the words (a mock now, Telegram later) is behind an
// interface.
package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"assistant/internal/users"
)

// Message roles and kinds.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"

	KindText     = "text"
	KindQuestion = "question"
	KindResult   = "result"
	KindError    = "error"
	KindInfo     = "info"
)

// Errors a caller can tell apart.
var (
	ErrNotFound        = errors.New("conversation not found")
	ErrUnknownUser     = errors.New("user not found")
	ErrUnknownWorkflow = errors.New("workflow not found")
	ErrBlank           = errors.New("message is blank")
)

// Conversation is a chat with the assistant.
type Conversation struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	WorkflowID string    `json:"workflow_id"`
	Title      string    `json:"title"`
	TaskID     *string   `json:"task_id"`
	CreatedAt  time.Time `json:"created_at"`
}

// View is a conversation with what a list or a header needs: the state of the
// run it is in and the last thing said.
type View struct {
	Conversation
	// TaskStatus and TaskError describe the run the conversation is in.
	TaskStatus *string `json:"task_status"`
	TaskError  *string `json:"task_error"`
	// LastText is the last message, cut short; LastAt when the conversation
	// last had activity.
	LastText *string   `json:"last_text"`
	LastAt   time.Time `json:"last_at"`
	Messages int       `json:"messages"`
}

// Message is one thing said.
type Message struct {
	ID             int64     `json:"id"`
	ConversationID string    `json:"conversation_id"`
	Role           string    `json:"role"`
	Kind           string    `json:"kind"`
	Text           string    `json:"text"`
	TaskID         *string   `json:"task_id"`
	CreatedAt      time.Time `json:"created_at"`
}

// Store reads and writes conversations and messages.
type Store struct{ pool *pgxpool.Pool }

// Pool is the database the store uses, for the code that sets tests up.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// NewStore returns a store on pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const conversationColumns = `c.id::text, c.user_id::text, c.workflow_id, c.title, c.task_id::text, c.created_at`

const viewColumns = conversationColumns + `,
	t.status, t.error,
	(SELECT left(m.text, 140) FROM messages m WHERE m.conversation_id = c.id ORDER BY m.id DESC LIMIT 1),
	coalesce((SELECT max(m.created_at) FROM messages m WHERE m.conversation_id = c.id), c.created_at) AS last_at,
	(SELECT count(*) FROM messages m WHERE m.conversation_id = c.id)::int`

func scanView(row pgx.Row) (View, error) {
	var v View
	err := row.Scan(&v.ID, &v.UserID, &v.WorkflowID, &v.Title, &v.TaskID, &v.CreatedAt, &v.TaskStatus, &v.TaskError, &v.LastText, &v.LastAt, &v.Messages)
	return v, err
}

// Create starts an empty conversation. The workflow is not checked here.
func (s *Store) Create(ctx context.Context, userID, workflowID, title string) (Conversation, error) {
	if !users.ValidID(userID) {
		return Conversation{}, ErrUnknownUser
	}
	var c Conversation
	err := s.pool.QueryRow(ctx, `
		INSERT INTO conversations (user_id, workflow_id, title) VALUES ($1::uuid, $2, $3)
		RETURNING `+strings.ReplaceAll(conversationColumns, "c.", ""), userID, workflowID, strings.TrimSpace(title)).
		Scan(&c.ID, &c.UserID, &c.WorkflowID, &c.Title, &c.TaskID, &c.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return Conversation{}, ErrUnknownUser
		}
		return Conversation{}, fmt.Errorf("create conversation: %w", err)
	}
	return c, nil
}

// Get returns a conversation with its run's state.
func (s *Store) Get(ctx context.Context, id string) (View, error) {
	if !users.ValidID(id) {
		return View{}, ErrNotFound
	}
	v, err := scanView(s.pool.QueryRow(ctx, `SELECT `+viewColumns+` FROM conversations c LEFT JOIN tasks t ON t.id = c.task_id WHERE c.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return View{}, ErrNotFound
	}
	if err != nil {
		return View{}, fmt.Errorf("get conversation: %w", err)
	}
	return v, nil
}

// List returns conversations, the most recently active first; userID, if not
// empty, keeps one user's.
func (s *Store) List(ctx context.Context, userID string) ([]View, error) {
	if userID != "" && !users.ValidID(userID) {
		return []View{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+viewColumns+` FROM conversations c LEFT JOIN tasks t ON t.id = c.task_id
		WHERE ($1 = '' OR c.user_id = $1::uuid)
		ORDER BY last_at DESC, c.id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()
	out := []View{}
	for rows.Next() {
		v, err := scanView(rows)
		if err != nil {
			return nil, fmt.Errorf("list conversations: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Remove deletes a conversation and its messages. Its task is not touched.
func (s *Store) Remove(ctx context.Context, id string) error {
	if !users.ValidID(id) {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM conversations WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("remove conversation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTask records the run the conversation is in.
func (s *Store) SetTask(ctx context.Context, id, taskID string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE conversations SET task_id = $2::uuid WHERE id = $1::uuid`, id, taskID)
	if err != nil {
		return fmt.Errorf("set the conversation's task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ForTask returns the conversation that is in the run, if there is one.
func (s *Store) ForTask(ctx context.Context, taskID string) (Conversation, bool, error) {
	if !users.ValidID(taskID) {
		return Conversation{}, false, nil
	}
	var c Conversation
	err := s.pool.QueryRow(ctx, `SELECT `+strings.ReplaceAll(conversationColumns, "c.", "")+` FROM conversations WHERE task_id = $1::uuid`, taskID).
		Scan(&c.ID, &c.UserID, &c.WorkflowID, &c.Title, &c.TaskID, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, false, nil
	}
	if err != nil {
		return Conversation{}, false, fmt.Errorf("find the conversation of a task: %w", err)
	}
	return c, true, nil
}

// AddMessage appends a message.
func (s *Store) AddMessage(ctx context.Context, conversationID, role, kind, text string, taskID *string) (Message, error) {
	var m Message
	err := s.pool.QueryRow(ctx, `
		INSERT INTO messages (conversation_id, role, kind, text, task_id) VALUES ($1::uuid, $2, $3, $4, $5::uuid)
		RETURNING id, conversation_id::text, role, kind, text, task_id::text, created_at`,
		conversationID, role, kind, text, taskID).
		Scan(&m.ID, &m.ConversationID, &m.Role, &m.Kind, &m.Text, &m.TaskID, &m.CreatedAt)
	if err != nil {
		return Message{}, fmt.Errorf("add message: %w", err)
	}
	return m, nil
}

// Messages returns a conversation's messages in order; after, if above 0, keeps
// only those with a greater id, which is how a page asks for what is new.
func (s *Store) Messages(ctx context.Context, conversationID string, after int64) ([]Message, error) {
	if !users.ValidID(conversationID) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, conversation_id::text, role, kind, text, task_id::text, created_at
		FROM messages WHERE conversation_id = $1::uuid AND id > $2 ORDER BY id`, conversationID, after)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Kind, &m.Text, &m.TaskID, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("list messages: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
