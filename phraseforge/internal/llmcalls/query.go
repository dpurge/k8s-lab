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

// List returns up to limit attempts matching f, newest first.
func (s *Store) List(ctx context.Context, f Filter, limit int) ([]Attempt, error) {
	var since *time.Time
	if !f.Since.IsZero() {
		since = &f.Since
	}
	rows, err := s.db.Query(ctx, `SELECT id::text, started_at, kind, source_language, target_language, job_id::text,
		model_id, provider_id, model_name, tags, input_tokens, output_tokens, cost_usd::float8, duration_ms,
		outcome, error, http_status, request_id
		FROM llm_calls
		WHERE ($1::timestamptz IS NULL OR started_at >= $1)
		  AND ($2 = '' OR model_id = $2)
		  AND ($3 = '' OR outcome = $3)
		  AND ($4 = '' OR kind = $4)
		ORDER BY started_at DESC, id DESC
		LIMIT $5`, since, f.ModelID, f.Outcome, f.Kind, limit)
	if err != nil {
		return nil, fmt.Errorf("llmcalls: list: %w", err)
	}
	defer rows.Close()
	out := []Attempt{}
	for rows.Next() {
		var a Attempt
		if err := rows.Scan(&a.ID, &a.StartedAt, &a.Kind, &a.SourceLanguage, &a.TargetLanguage, &a.JobID, &a.ModelID,
			&a.ProviderID, &a.ModelName, &a.Tags, &a.InputTokens, &a.OutputTokens, &a.CostUSD, &a.DurationMS,
			&a.Outcome, &a.Error, &a.HTTPStatus, &a.RequestID); err != nil {
			return nil, fmt.Errorf("llmcalls: list: %w", err)
		}
		if a.Tags == nil {
			a.Tags = []string{}
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("llmcalls: list: %w", err)
	}
	return out, nil
}
