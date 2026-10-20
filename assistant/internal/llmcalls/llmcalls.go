// Package llmcalls stores one row per attempt to call a model, and reads them
// back per task and as usage per user and model.
package llmcalls

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"k8s-lab/shared/retention"
)

// RuleName is the name of the retention rule of llm_calls in logs and metrics.
const RuleName = "llm_calls"

// RetentionRule is the rule that keeps calls for keep (0 keeps them forever).
// Only llm_calls is purged: it has no dependents, it references tasks and users
// and never the other way round.
func RetentionRule(keep time.Duration) retention.Rule {
	return retention.Rule{Name: RuleName, Table: "llm_calls", TimeColumn: "started_at", Retention: keep}
}

// Call is one attempt to call a model.
type Call struct {
	ID           string    `json:"id"`
	TaskID       *string   `json:"task_id"`
	UserID       string    `json:"user_id"`
	ModelID      string    `json:"model_id"`
	ProviderID   string    `json:"provider_id"`
	ModelName    string    `json:"model_name"`
	Tags         []string  `json:"tags"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd"`
	StartedAt    time.Time `json:"started_at"`
	DurationMS   int       `json:"duration_ms"`
	Outcome      string    `json:"outcome"`
	Error        *string   `json:"error"`
}

// Store reads and writes calls.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a store on pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const columns = `id::text, task_id::text, user_id::text, model_id, provider_id, model_name, tags,
	input_tokens, output_tokens, cost_usd::float8, started_at, duration_ms, outcome, error`

// Insert stores a call; its ID is ignored and assigned by the database.
func (s *Store) Insert(ctx context.Context, c Call) error {
	tags := c.Tags
	if tags == nil {
		tags = []string{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO llm_calls (task_id, user_id, model_id, provider_id, model_name, tags,
			input_tokens, output_tokens, cost_usd, started_at, duration_ms, outcome, error)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		c.TaskID, c.UserID, c.ModelID, c.ProviderID, c.ModelName, tags,
		c.InputTokens, c.OutputTokens, c.CostUSD, c.StartedAt, c.DurationMS, c.Outcome, c.Error)
	if err != nil {
		return fmt.Errorf("insert llm call: %w", err)
	}
	return nil
}

func scan(row pgx.Row) (Call, error) {
	var c Call
	err := row.Scan(&c.ID, &c.TaskID, &c.UserID, &c.ModelID, &c.ProviderID, &c.ModelName, &c.Tags,
		&c.InputTokens, &c.OutputTokens, &c.CostUSD, &c.StartedAt, &c.DurationMS, &c.Outcome, &c.Error)
	return c, err
}

// ForTask returns the task's attempts, oldest first.
func (s *Store) ForTask(ctx context.Context, taskID string) ([]Call, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+columns+` FROM llm_calls WHERE task_id = $1::uuid ORDER BY started_at, id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list llm calls: %w", err)
	}
	defer rows.Close()
	calls := []Call{}
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list llm calls: %w", err)
		}
		calls = append(calls, c)
	}
	return calls, rows.Err()
}

// Usage is what one user used of one model. Only successful calls count: a
// failed attempt has no tokens and no cost.
type Usage struct {
	UserID       string  `json:"user_id"`
	ModelID      string  `json:"model_id"`
	Calls        int     `json:"calls"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// UsageByUserAndModel sums successful calls per user and model, from since if
// given. Ordered by cost, highest first, then by user and model.
func (s *Store) UsageByUserAndModel(ctx context.Context, since *time.Time) ([]Usage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT user_id::text, model_id, count(*)::int, coalesce(sum(input_tokens), 0)::int,
		       coalesce(sum(output_tokens), 0)::int, coalesce(sum(cost_usd), 0)::float8
		FROM llm_calls
		WHERE outcome = 'ok' AND ($1::timestamptz IS NULL OR started_at >= $1)
		GROUP BY user_id, model_id
		ORDER BY 6 DESC, user_id, model_id`, since)
	if err != nil {
		return nil, fmt.Errorf("usage: %w", err)
	}
	defer rows.Close()
	out := []Usage{}
	for rows.Next() {
		var u Usage
		if err := rows.Scan(&u.UserID, &u.ModelID, &u.Calls, &u.InputTokens, &u.OutputTokens, &u.CostUSD); err != nil {
			return nil, fmt.Errorf("usage: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// TaskUsage is what one task used.
type TaskUsage struct {
	Calls        int      `json:"calls"`
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	CostUSD      float64  `json:"cost_usd"`
	Models       []string `json:"models"`
}

// UsageForTasks returns, for each of the given tasks that made calls, what it
// used. Tasks that made none are absent from the map. Models lists each model
// that answered, in the order it first did.
func (s *Store) UsageForTasks(ctx context.Context, taskIDs []string) (map[string]TaskUsage, error) {
	out := map[string]TaskUsage{}
	if len(taskIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT task_id::text, count(*)::int, coalesce(sum(input_tokens), 0)::int,
		       coalesce(sum(output_tokens), 0)::int, coalesce(sum(cost_usd), 0)::float8,
		       array_agg(model_id ORDER BY started_at, id)
		FROM llm_calls
		WHERE outcome = 'ok' AND task_id = ANY($1::uuid[])
		GROUP BY task_id`, taskIDs)
	if err != nil {
		return nil, fmt.Errorf("task usage: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var u TaskUsage
		var models []string
		if err := rows.Scan(&id, &u.Calls, &u.InputTokens, &u.OutputTokens, &u.CostUSD, &models); err != nil {
			return nil, fmt.Errorf("task usage: %w", err)
		}
		seen := map[string]bool{}
		u.Models = []string{}
		for _, m := range models {
			if !seen[m] {
				seen[m] = true
				u.Models = append(u.Models, m)
			}
		}
		out[id] = u
	}
	return out, rows.Err()
}
