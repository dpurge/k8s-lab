package llmcalls

import (
	"context"
	"log/slog"
	"time"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"

	"knowledge/internal/modelrouter"
	"knowledge/internal/queue"
)

// CallStore writes calls. *Store implements it.
type CallStore interface {
	Insert(ctx context.Context, c Call) error
}

// Metrics is where a recorded call is counted. *metrics.Metrics implements it.
type Metrics interface {
	ObserveLLM(provider, model, outcome string, inputTokens, outputTokens int, costUSD float64, elapsed time.Duration)
	RecordError()
}

// insertTimeout bounds writing a record after the call's own context ended, so
// that a canceled attempt is still recorded without ever holding the caller up
// for long.
const insertTimeout = 5 * time.Second

// Recorder is the router's observer: it stores every attempt, with its cost,
// against the queue operation and the kind of call it was made for, and counts
// it in the metrics. Recording never fails a call: a write that fails is logged
// and counted.
type Recorder struct {
	store   CallStore
	metrics Metrics
	prices  map[string]modelcfg.Price
	log     *slog.Logger
}

var _ router.Observer = (*Recorder)(nil)

// NewRecorder prices calls from cfg. metrics may be nil.
func NewRecorder(cfg *modelcfg.Config, store CallStore, metrics Metrics, log *slog.Logger) *Recorder {
	prices := make(map[string]modelcfg.Price, len(cfg.Models))
	for _, m := range cfg.Models {
		prices[m.ID] = m.Price
	}
	return &Recorder{store: store, metrics: metrics, prices: prices, log: log}
}

// Observe implements router.Observer.
func (r *Recorder) Observe(ctx context.Context, a router.Attempt) {
	cost := r.prices[a.ModelID].Cost(a.InputTokens, a.OutputTokens)
	call := Call{
		Kind:    modelrouter.Kind(ctx),
		ModelID: a.ModelID, ProviderID: a.ProviderID, ModelName: a.ModelName, Tags: a.Tags,
		InputTokens: a.InputTokens, OutputTokens: a.OutputTokens, CostUSD: cost,
		StartedAt: a.StartedAt, DurationMS: int(a.Duration.Milliseconds()), Outcome: a.Outcome,
	}
	if call.Kind == "" && a.Kind == router.AttemptEmbed {
		call.Kind = modelrouter.KindEmbed
	}
	if id, ok := queue.OperationID(ctx); ok {
		call.OperationID = &id
	}
	if a.Error != "" {
		call.Error = &a.Error
	}
	if a.HTTPStatus != 0 {
		call.HTTPStatus = &a.HTTPStatus
	}
	if a.RequestID != "" {
		call.RequestID = &a.RequestID
	}

	insertCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), insertTimeout)
	defer cancel()
	if err := r.store.Insert(insertCtx, call); err != nil {
		// Only the model and the error are logged, never a prompt, a reply or a text.
		r.log.Error("could not record a model call", "model", a.ModelID, "outcome", a.Outcome, "error", err)
		if r.metrics != nil {
			r.metrics.RecordError()
		}
	}
	if r.metrics != nil {
		r.metrics.ObserveLLM(a.ProviderID, a.ModelID, a.Outcome, a.InputTokens, a.OutputTokens, cost, a.Duration)
	}
}
