package translate

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"knowledge/internal/config"
	"knowledge/internal/modelrouter"
)

var ErrEmptyText = errors.New("text is required")

type Service struct {
	llm            modelrouter.Completer
	language       string
	promptTemplate string
}

// New builds the service over c, the model call for the translate purpose.
func New(cfg config.Config, c modelrouter.Completer) *Service {
	return &Service{
		llm:            c,
		language:       cfg.KnowledgeLanguage,
		promptTemplate: cfg.Translate.Prompt,
	}
}

// Translate returns text translated into the configured knowledge-base
// language, or unchanged if it's already in that language. One model call
// handles both cases — confirmed live rather than running a separate
// language-detection step first. It is always called directly from within
// ingest's own chunk pipeline (never through the queue package): an
// ingest_chunk operation already owns the queue's one worker slot, so this
// call, like generate.TitleDirect/SummaryDirect, must not itself enqueue
// further queue work.
func (s *Service) Translate(ctx context.Context, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", ErrEmptyText
	}
	sys := strings.ReplaceAll(s.promptTemplate, "{{language}}", s.language)
	start := time.Now()
	out, err := s.llm.Complete(ctx, []modelrouter.Message{{Role: "system", Content: sys}, {Role: "user", Content: text}})
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	slog.Info("translate llm call", "duration_ms", time.Since(start).Milliseconds(), "outcome", outcome)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
