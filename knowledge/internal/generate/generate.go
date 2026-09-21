package generate

import (
	"context"
	"errors"
	"strings"

	"k8s-lab/shared/llm"
	"knowledge/internal/config"
)

var ErrEmptyBody = errors.New("body is required")

type Service struct {
	llm *llm.Client
}

func New(cfg config.Config) *Service {
	return &Service{llm: llm.New(llm.Config{Provider: cfg.GenerateProvider, BaseURL: cfg.GenerateBaseURL, APIKey: cfg.GenerateAPIKey, Model: cfg.GenerateModel, NumCtx: cfg.GenerateNumCtx})}
}

const titlePrompt = `You write a short, specific title for the given Markdown document. Respond with only the title text on a single line — no quotes, no punctuation at the end, no preamble like "Title:".`

const summaryPrompt = `You write a one-paragraph summary of the given Markdown document, for use as a search-result preview. Respond with only the summary text — no preamble like "Summary:", no quotes.`

func (s *Service) Title(ctx context.Context, body string) (string, error) {
	if strings.TrimSpace(body) == "" {
		return "", ErrEmptyBody
	}
	out, err := s.llm.Complete(ctx, []llm.Message{{Role: "system", Content: titlePrompt}, {Role: "user", Content: body}})
	return strings.TrimSpace(out), err
}

func (s *Service) Summary(ctx context.Context, body string) (string, error) {
	if strings.TrimSpace(body) == "" {
		return "", ErrEmptyBody
	}
	out, err := s.llm.Complete(ctx, []llm.Message{{Role: "system", Content: summaryPrompt}, {Role: "user", Content: body}})
	return strings.TrimSpace(out), err
}
