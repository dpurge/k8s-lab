package generate

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"k8s-lab/shared/llm"
	"knowledge/internal/config"
	"knowledge/internal/queue"
)

var ErrEmptyBody = errors.New("body is required")

// KindTitle and KindSummary are the queue.Service operation kinds
// registered for generating a title/summary. Exported so main.go can call
// queue.Register(generate.KindTitle, generateSvc.HandleTitle), etc.
const (
	KindTitle   = "generate_title"
	KindSummary = "generate_summary"
)

// ItemWriteback adapts however this app writes a generated title/summary
// back into a real knowledge item. It carries only primitives, matching
// this codebase's existing import-graph-avoidance convention (see
// ingest.Promoter) — generate never imports qdrant; main.go supplies the
// adapter once both types are already in scope.
type ItemWriteback interface {
	SetTitle(ctx context.Context, itemID, title string) error
	SetSummary(ctx context.Context, itemID, summary string) error
}

type Service struct {
	llm           *llm.Client
	model         string
	titlePrompt   string
	summaryPrompt string
	queue         *queue.Service
	items         ItemWriteback
}

func New(cfg config.Config, q *queue.Service, items ItemWriteback) *Service {
	return &Service{
		llm:           llm.New(llm.Config{Provider: cfg.GenerateProvider, BaseURL: cfg.GenerateBaseURL, APIKey: cfg.GenerateAPIKey, Model: cfg.GenerateModel, NumCtx: cfg.GenerateNumCtx}),
		model:         cfg.GenerateModel,
		titlePrompt:   cfg.GenerateTitlePrompt,
		summaryPrompt: cfg.GenerateSummaryPrompt,
		queue:         q,
		items:         items,
	}
}

// genPayload is the queue.Operation payload shared by KindTitle and
// KindSummary. ItemID is empty for the manual "Generate" button (no
// write-back, the caller reads Result directly via EnqueueAndAwait) and
// set for an import-triggered background generate (write-back happens,
// nothing awaits Result).
type genPayload struct {
	Body   string `json:"body"`
	ItemID string `json:"item_id,omitempty"`
}

type genResult struct {
	Text string `json:"text"`
}

// Title keeps its synchronous, request-blocking contract for callers: it
// enqueues an interactive-priority operation and awaits it, so a manual
// "Generate title" click still gets its answer in the same HTTP response,
// while participating in the single-worker, priority-ordered queue like
// everything else.
func (s *Service) Title(ctx context.Context, body string) (string, error) {
	if strings.TrimSpace(body) == "" {
		return "", ErrEmptyBody
	}
	raw, err := s.queue.EnqueueAndAwait(ctx, KindTitle, queue.PriorityInteractive, genPayload{Body: body})
	if err != nil {
		return "", err
	}
	return decodeGenResult(raw)
}

// Summary is Title's counterpart for the summary prompt.
func (s *Service) Summary(ctx context.Context, body string) (string, error) {
	if strings.TrimSpace(body) == "" {
		return "", ErrEmptyBody
	}
	raw, err := s.queue.EnqueueAndAwait(ctx, KindSummary, queue.PriorityInteractive, genPayload{Body: body})
	if err != nil {
		return "", err
	}
	return decodeGenResult(raw)
}

// EnqueueBackgroundTitle and EnqueueBackgroundSummary are the
// import-triggered counterparts of Title/Summary: fire-and-forget,
// background-priority, and carrying itemID so HandleTitle/HandleSummary
// write the generated text back into that item once the queue gets to it.
// No caller awaits these — qdrant.Import returns to its own caller long
// before this LLM call happens.
func (s *Service) EnqueueBackgroundTitle(ctx context.Context, itemID, body string) error {
	_, err := s.queue.Enqueue(ctx, KindTitle, queue.PriorityBackground, genPayload{Body: body, ItemID: itemID})
	return err
}

func (s *Service) EnqueueBackgroundSummary(ctx context.Context, itemID, body string) error {
	_, err := s.queue.Enqueue(ctx, KindSummary, queue.PriorityBackground, genPayload{Body: body, ItemID: itemID})
	return err
}

func decodeGenResult(raw json.RawMessage) (string, error) {
	var r genResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", err
	}
	return r.Text, nil
}

// TitleDirect and SummaryDirect call the LLM immediately, bypassing the
// queue entirely. They exist solely for ingest's per-chunk pipeline: an
// ingest_chunk operation handler already runs inside the queue's one
// worker slot, so it must never enqueue-and-await a further operation on
// the same queue — with a single worker, that operation could never be
// claimed until the very handler waiting on it returns, deadlocking
// forever. Title/Summary (queued, awaited) are for every other caller —
// the manual "Generate" HTTP handlers — which run outside the worker.
func (s *Service) TitleDirect(ctx context.Context, body string) (string, error) {
	if strings.TrimSpace(body) == "" {
		return "", ErrEmptyBody
	}
	return s.llmComplete(ctx, s.titlePrompt, body)
}

func (s *Service) SummaryDirect(ctx context.Context, body string) (string, error) {
	if strings.TrimSpace(body) == "" {
		return "", ErrEmptyBody
	}
	return s.llmComplete(ctx, s.summaryPrompt, body)
}

// HandleTitle and HandleSummary are the queue.HandlerFunc implementations
// registered for KindTitle/KindSummary. Both call the LLM with the
// matching prompt and, when the payload carries an ItemID (the
// import-triggered background case), write the result back into that item
// — the same handler serves both the awaited manual case and the
// fire-and-forget import case.
func (s *Service) HandleTitle(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	return s.handleGenerate(ctx, raw, s.titlePrompt, s.items.SetTitle)
}

func (s *Service) HandleSummary(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	return s.handleGenerate(ctx, raw, s.summaryPrompt, s.items.SetSummary)
}

func (s *Service) handleGenerate(ctx context.Context, raw json.RawMessage, prompt string, writeback func(ctx context.Context, itemID, text string) error) (json.RawMessage, error) {
	var p genPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	text, err := s.llmComplete(ctx, prompt, p.Body)
	if err != nil {
		return nil, err
	}
	if p.ItemID != "" {
		if err := writeback(ctx, p.ItemID, text); err != nil {
			return nil, err
		}
	}
	return json.Marshal(genResult{Text: text})
}

// llmComplete is the one place this package actually calls the LLM,
// logging duration and outcome (never body content) for every call
// regardless of which path reached it.
func (s *Service) llmComplete(ctx context.Context, prompt, body string) (string, error) {
	start := time.Now()
	out, err := s.llm.Complete(ctx, []llm.Message{{Role: "system", Content: prompt}, {Role: "user", Content: body}})
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	slog.Info("generate llm call", "model", s.model, "duration_ms", time.Since(start).Milliseconds(), "outcome", outcome)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
