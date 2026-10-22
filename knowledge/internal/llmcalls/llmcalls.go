// Package llmcalls records every attempt the model router makes: one row per
// attempt in the llm_calls table, with its cost, and the same attempt counted in
// the metrics. A row never holds a prompt, a reply, a text or a vector.
package llmcalls

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"k8s-lab/shared/retention"

	"knowledge/internal/metrics"
)

// Call is one row of llm_calls. OperationID is nil for a call made outside the
// queue (the embedding of a search query), and Error, HTTPStatus and RequestID
// are nil when unknown.
type Call struct {
	OperationID  *string
	Kind         string
	ModelID      string
	ProviderID   string
	ModelName    string
	Tags         []string
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	StartedAt    time.Time
	DurationMS   int
	Outcome      string
	Error        *string
	HTTPStatus   *int
	RequestID    *string
}

// Default pace of DeleteOlderThan; the same as the shared helper's.
const (
	defaultBatchSize  = 5000
	defaultBatchPause = 50 * time.Millisecond
)

// Store writes calls to the database.
type Store struct {
	db         *pgxpool.Pool
	batchSize  int
	batchPause time.Duration
}

// StoreOption configures a Store.
type StoreOption func(*Store)

// WithBatchSize sets how many rows one delete of DeleteOlderThan removes.
func WithBatchSize(n int) StoreOption { return func(s *Store) { s.batchSize = n } }

// WithBatchPause sets the pause between the batches of DeleteOlderThan.
func WithBatchPause(d time.Duration) StoreOption { return func(s *Store) { s.batchPause = d } }

// NewStore returns a store on db.
func NewStore(db *pgxpool.Pool, opts ...StoreOption) *Store {
	s := &Store{db: db, batchSize: defaultBatchSize, batchPause: defaultBatchPause}
	for _, o := range opts {
		o(s)
	}
	if s.batchSize < 1 {
		s.batchSize = defaultBatchSize
	}
	return s
}

// RetentionRule is the rule that keeps calls for keep (0 keeps them forever).
func RetentionRule(keep time.Duration) retention.Rule {
	return retention.Rule{Name: metrics.RuleLLMCalls, Table: "llm_calls", TimeColumn: "started_at", Retention: keep}
}

// DeleteOlderThan deletes the calls that started before cutoff, a batch at a
// time, and returns how many it deleted; on an error or a canceled ctx that is
// the count so far. A cutoff of now deletes everything.
func (s *Store) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	return retention.DeleteOlderThan(ctx, s.db, RetentionRule(0), cutoff,
		retention.WithBatchSize(s.batchSize), retention.WithBatchPause(s.batchPause))
}

// Insert writes one call.
func (s *Store) Insert(ctx context.Context, c Call) error {
	tags := c.Tags
	if tags == nil {
		tags = []string{} // a NULL would violate the column's NOT NULL
	}
	_, err := s.db.Exec(ctx, `INSERT INTO llm_calls
		(operation_id, kind, model_id, provider_id, model_name, tags,
		 input_tokens, output_tokens, cost_usd, started_at, duration_ms, outcome, error, http_status, request_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		c.OperationID, c.Kind, c.ModelID, c.ProviderID, c.ModelName, tags,
		c.InputTokens, c.OutputTokens, c.CostUSD, c.StartedAt, c.DurationMS, c.Outcome, c.Error, c.HTTPStatus, c.RequestID)
	return err
}
