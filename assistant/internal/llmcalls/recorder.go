package llmcalls

import (
	"context"
	"log/slog"
	"time"

	"assistant/internal/callctx"
	"assistant/internal/llm"
	"k8s-lab/shared/modelcfg"
)

// CallStore writes calls. *Store implements it.
type CallStore interface {
	Insert(ctx context.Context, c Call) error
}

// Metrics is where a recorded call is counted. *metrics.Metrics implements it.
type Metrics interface {
	ObserveLLM(user, provider, model, outcome string, inputTokens, outputTokens int, costUSD float64, elapsed time.Duration)
	RecordError()
}

// insertTimeout bounds writing a record after the call's own context ended, so
// that a canceled attempt is still recorded.
const insertTimeout = 5 * time.Second

// Recorder is the router's observer: it stores every attempt, with its cost,
// against the task and user the call was made for, and counts it in the
// metrics. Recording never fails a call: a write that fails is logged and
// counted.
type Recorder struct {
	store   CallStore
	metrics Metrics
	prices  map[string]modelcfg.Price
	log     *slog.Logger
}

var _ llm.Observer = (*Recorder)(nil)

// NewRecorder prices calls from cfg. metrics may be nil.
func NewRecorder(cfg *modelcfg.Config, store CallStore, metrics Metrics, log *slog.Logger) *Recorder {
	prices := make(map[string]modelcfg.Price, len(cfg.Models))
	for _, m := range cfg.Models {
		prices[m.ID] = m.Price
	}
	return &Recorder{store: store, metrics: metrics, prices: prices, log: log}
}

// Observe implements llm.Observer.
func (r *Recorder) Observe(ctx context.Context, a llm.Attempt) {
	info, ok := callctx.From(ctx)
	if !ok {
		// The router refuses such a call when attribution is required; this
		// is only reachable if it was not, and there is nobody to charge.
		r.log.Error("model call without a user was not recorded", "model", a.ModelID, "outcome", a.Outcome)
		if r.metrics != nil {
			r.metrics.RecordError()
		}
		return
	}

	cost := r.prices[a.ModelID].Cost(a.InputTokens, a.OutputTokens)
	call := Call{
		UserID: info.UserID, ModelID: a.ModelID, ProviderID: a.ProviderID, ModelName: a.ModelName,
		Tags: a.Tags, InputTokens: a.InputTokens, OutputTokens: a.OutputTokens, CostUSD: cost,
		StartedAt: a.StartedAt, DurationMS: int(a.Duration.Milliseconds()), Outcome: a.Outcome,
	}
	if info.TaskID != "" {
		call.TaskID = &info.TaskID
	}
	if a.Error != "" {
		call.Error = &a.Error
	}

	insertCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), insertTimeout)
	defer cancel()
	if err := r.store.Insert(insertCtx, call); err != nil {
		// Only the model and the error are logged, never a prompt or a reply.
		r.log.Error("could not record a model call", "model", a.ModelID, "outcome", a.Outcome, "error", err)
		if r.metrics != nil {
			r.metrics.RecordError()
		}
	}
	if r.metrics != nil {
		r.metrics.ObserveLLM(info.UserID, a.ProviderID, a.ModelID, a.Outcome, a.InputTokens, a.OutputTokens, cost, a.Duration)
	}
}
