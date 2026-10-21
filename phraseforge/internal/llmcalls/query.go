package llmcalls

import (
	"context"
	"fmt"
	"time"
)

// Grouping of Aggregates.
const (
	GroupByModel     = "model"
	GroupByKindModel = "kind_model"
)

// Outcomes lists every value of llm_calls.outcome.
var Outcomes = []string{"ok", "error", "timeout", "rate_limited", "canceled"}

// Aggregate is the attempts of one model (and one kind, when grouped by kind
// and model) in a window. The durations and the speed count successful
// attempts only, as a failed attempt's duration says nothing about the model's
// speed; they are nil when there is no such attempt. Tokens and cost count
// every attempt.
type Aggregate struct {
	ModelID               string   `json:"model_id"`
	ProviderID            string   `json:"provider_id"`
	Kind                  *string  `json:"kind,omitempty"`
	Calls                 int64    `json:"calls"`
	OKCalls               int64    `json:"ok_calls"`
	ErrorCalls            int64    `json:"error_calls"`
	TimeoutCalls          int64    `json:"timeout_calls"`
	RateLimitedCalls      int64    `json:"rate_limited_calls"`
	CanceledCalls         int64    `json:"canceled_calls"`
	SuccessRate           float64  `json:"success_rate"`
	InputTokens           int64    `json:"input_tokens"`
	OutputTokens          int64    `json:"output_tokens"`
	MeanDurationMS        *float64 `json:"mean_duration_ms"`
	P50DurationMS         *float64 `json:"p50_duration_ms"`
	P95DurationMS         *float64 `json:"p95_duration_ms"`
	OutputTokensPerSecond *float64 `json:"output_tokens_per_second"`
	CostUSD               float64  `json:"cost_usd"`
}

// Aggregates summarizes the attempts that started at or after since, per model
// or per kind and model (groupBy is GroupByModel or GroupByKindModel), sorted
// by model id and kind. A model without an attempt in the window is absent.
func (s *Store) Aggregates(ctx context.Context, since time.Time, groupBy string) ([]Aggregate, error) {
	byKind := groupBy == GroupByKindModel
	if !byKind && groupBy != GroupByModel {
		return nil, fmt.Errorf("llmcalls: unknown grouping %q", groupBy)
	}
	// Only the select list and the GROUP BY differ between the two groupings;
	// every value is still a parameter.
	kindExpr := "NULL::text"
	if byKind {
		kindExpr = "kind"
	}
	query := `SELECT model_id, provider_id, ` + kindExpr + ` AS kind,
		count(*),
		count(*) FILTER (WHERE outcome = 'ok'),
		count(*) FILTER (WHERE outcome = 'error'),
		count(*) FILTER (WHERE outcome = 'timeout'),
		count(*) FILTER (WHERE outcome = 'rate_limited'),
		count(*) FILTER (WHERE outcome = 'canceled'),
		coalesce(sum(input_tokens), 0)::bigint,
		coalesce(sum(output_tokens), 0)::bigint,
		(avg(duration_ms) FILTER (WHERE outcome = 'ok'))::float8,
		(percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE outcome = 'ok'))::float8,
		(percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE outcome = 'ok'))::float8,
		(sum(output_tokens) FILTER (WHERE outcome = 'ok' AND output_tokens > 0))::float8
			/ nullif((sum(duration_ms) FILTER (WHERE outcome = 'ok' AND output_tokens > 0))::float8 / 1000.0, 0),
		coalesce(sum(cost_usd), 0)::float8
		FROM llm_calls
		WHERE started_at >= $1
		GROUP BY model_id, provider_id` + groupKind(byKind) + `
		ORDER BY model_id, provider_id` + groupKind(byKind)
	rows, err := s.db.Query(ctx, query, since)
	if err != nil {
		return nil, fmt.Errorf("llmcalls: aggregate: %w", err)
	}
	defer rows.Close()
	out := []Aggregate{}
	for rows.Next() {
		var a Aggregate
		if err := rows.Scan(&a.ModelID, &a.ProviderID, &a.Kind, &a.Calls, &a.OKCalls, &a.ErrorCalls, &a.TimeoutCalls,
			&a.RateLimitedCalls, &a.CanceledCalls, &a.InputTokens, &a.OutputTokens, &a.MeanDurationMS, &a.P50DurationMS,
			&a.P95DurationMS, &a.OutputTokensPerSecond, &a.CostUSD); err != nil {
			return nil, fmt.Errorf("llmcalls: aggregate: %w", err)
		}
		a.SuccessRate = float64(a.OKCalls) / float64(a.Calls)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("llmcalls: aggregate: %w", err)
	}
	return out, nil
}

func groupKind(byKind bool) string {
	if byKind {
		return ", kind"
	}
	return ""
}

// Attempt is one row of llm_calls as the admin lists it: never a prompt or a
// reply, which the table does not hold.
type Attempt struct {
	ID             string    `json:"id"`
	StartedAt      time.Time `json:"started_at"`
	UserID         *int64    `json:"user_id"`
	Username       *string   `json:"username"`
	Kind           string    `json:"kind"`
	SourceLanguage string    `json:"source_language"`
	TargetLanguage string    `json:"target_language"`
	JobID          *string   `json:"job_id"`
	ModelID        string    `json:"model_id"`
	ProviderID     string    `json:"provider_id"`
	ModelName      string    `json:"model_name"`
	Tags           []string  `json:"tags"`
	InputTokens    int       `json:"input_tokens"`
	OutputTokens   int       `json:"output_tokens"`
	CostUSD        float64   `json:"cost_usd"`
	DurationMS     int       `json:"duration_ms"`
	Outcome        string    `json:"outcome"`
	Error          *string   `json:"error"`
	HTTPStatus     *int      `json:"http_status"`
	RequestID      *string   `json:"request_id"`
}

// Filter narrows List; a zero field does not filter.
type Filter struct {
	Since   time.Time
	ModelID string
	Outcome string
	Kind    string
}

// List returns one page of the attempts matching f, newest first: up to limit
// of them after skipping offset, and how many attempts match in all.
func (s *Store) List(ctx context.Context, f Filter, limit, offset int) ([]Attempt, int, error) {
	var since *time.Time
	if !f.Since.IsZero() {
		since = &f.Since
	}
	const where = `WHERE ($1::timestamptz IS NULL OR c.started_at >= $1)
		  AND ($2 = '' OR c.model_id = $2)
		  AND ($3 = '' OR c.outcome = $3)
		  AND ($4 = '' OR c.kind = $4)`
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*)::int FROM llm_calls c `+where, since, f.ModelID, f.Outcome, f.Kind).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("llmcalls: count: %w", err)
	}
	rows, err := s.db.Query(ctx, `SELECT c.id::text, c.started_at, c.user_id, u.username, c.kind, c.source_language, c.target_language,
		c.job_id::text, c.model_id, c.provider_id, c.model_name, c.tags, c.input_tokens, c.output_tokens, c.cost_usd::float8,
		c.duration_ms, c.outcome, c.error, c.http_status, c.request_id
		FROM llm_calls c LEFT JOIN users u ON u.id = c.user_id
		`+where+`
		ORDER BY c.started_at DESC, c.id DESC
		LIMIT $5 OFFSET $6`, since, f.ModelID, f.Outcome, f.Kind, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("llmcalls: list: %w", err)
	}
	defer rows.Close()
	out := []Attempt{}
	for rows.Next() {
		var a Attempt
		if err := rows.Scan(&a.ID, &a.StartedAt, &a.UserID, &a.Username, &a.Kind, &a.SourceLanguage, &a.TargetLanguage, &a.JobID,
			&a.ModelID, &a.ProviderID, &a.ModelName, &a.Tags, &a.InputTokens, &a.OutputTokens, &a.CostUSD, &a.DurationMS,
			&a.Outcome, &a.Error, &a.HTTPStatus, &a.RequestID); err != nil {
			return nil, 0, fmt.Errorf("llmcalls: list: %w", err)
		}
		if a.Tags == nil {
			a.Tags = []string{}
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("llmcalls: list: %w", err)
	}
	return out, total, nil
}

// Usage is what one user used of one model of one provider. Only successful
// attempts count: a failed attempt has no tokens worth billing. UserID and
// Username are nil for calls with no user behind them (and for calls from
// before the user was recorded).
type Usage struct {
	UserID       *int64  `json:"user_id"`
	Username     *string `json:"username"`
	ProviderID   string  `json:"provider_id"`
	ModelID      string  `json:"model_id"`
	Calls        int64   `json:"calls"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// UsageByUserAndModel sums the successful attempts that started at or after
// since (all of them if since is nil) per user, provider and model, highest
// cost first. It is the finest grain; a page that groups by one of the three
// sums these rows.
func (s *Store) UsageByUserAndModel(ctx context.Context, since *time.Time) ([]Usage, error) {
	rows, err := s.db.Query(ctx, `SELECT c.user_id, u.username, c.provider_id, c.model_id, count(*),
		coalesce(sum(c.input_tokens), 0)::bigint, coalesce(sum(c.output_tokens), 0)::bigint, coalesce(sum(c.cost_usd), 0)::float8
		FROM llm_calls c LEFT JOIN users u ON u.id = c.user_id
		WHERE c.outcome = 'ok' AND ($1::timestamptz IS NULL OR c.started_at >= $1)
		GROUP BY c.user_id, u.username, c.provider_id, c.model_id
		ORDER BY 8 DESC, u.username NULLS LAST, c.provider_id, c.model_id`, since)
	if err != nil {
		return nil, fmt.Errorf("llmcalls: usage: %w", err)
	}
	defer rows.Close()
	out := []Usage{}
	for rows.Next() {
		var u Usage
		if err := rows.Scan(&u.UserID, &u.Username, &u.ProviderID, &u.ModelID, &u.Calls, &u.InputTokens, &u.OutputTokens, &u.CostUSD); err != nil {
			return nil, fmt.Errorf("llmcalls: usage: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
