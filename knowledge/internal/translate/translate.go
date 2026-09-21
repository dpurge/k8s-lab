package translate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"k8s-lab/shared/llm"
	"knowledge/internal/config"
)

var ErrEmptyText = errors.New("text is required")

type Service struct {
	llm      *llm.Client
	language string
}

func New(cfg config.Config) *Service {
	return &Service{
		llm:      llm.New(llm.Config{Provider: cfg.TranslateProvider, BaseURL: cfg.TranslateBaseURL, APIKey: cfg.TranslateAPIKey, Model: cfg.TranslateModel, NumCtx: cfg.TranslateNumCtx}),
		language: cfg.KnowledgeLanguage,
	}
}

// Translate returns text translated into the configured knowledge-base
// language, or unchanged if it's already in that language. One model call
// handles both cases — confirmed live against rinex20/translategemma3:12b
// rather than running a separate language-detection step first.
func (s *Service) Translate(ctx context.Context, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", ErrEmptyText
	}
	sys := fmt.Sprintf("If the following text is already in %s, return it unchanged. Otherwise, translate it into %s. Respond with only the resulting text — no preamble, no explanation.", s.language, s.language)
	out, err := s.llm.Complete(ctx, []llm.Message{{Role: "system", Content: sys}, {Role: "user", Content: text}})
	return strings.TrimSpace(out), err
}
