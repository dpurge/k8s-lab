package ai

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"k8s-lab/shared/llm"
)

type Prompt struct {
	Kind           string `json:"kind"`
	SourceLanguage string `json:"source_language"`
	TargetLanguage string `json:"target_language"`
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
}

type Service struct {
	db  *pgxpool.Pool
	cfg llm.Config
}

func New(db *pgxpool.Pool, cfg llm.Config) *Service { return &Service{db: db, cfg: cfg} }

func (s *Service) ListPrompts(ctx context.Context) ([]Prompt, error) {
	rows, err := s.db.Query(ctx, `SELECT kind, source_language, target_language, coalesce(model, ''), prompt FROM llm_prompts ORDER BY kind, source_language, target_language`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Prompt
	for rows.Next() {
		var p Prompt
		if err := rows.Scan(&p.Kind, &p.SourceLanguage, &p.TargetLanguage, &p.Model, &p.Prompt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Service) SetPrompt(ctx context.Context, p Prompt) error {
	_, err := s.db.Exec(ctx, `INSERT INTO llm_prompts(kind,source_language,target_language,model,prompt) VALUES($1,$2,$3,$4,$5) ON CONFLICT(kind,source_language,target_language) DO UPDATE SET model=excluded.model, prompt=excluded.prompt, updated_at=now()`, p.Kind, p.SourceLanguage, p.TargetLanguage, strings.TrimSpace(p.Model), p.Prompt)
	return err
}

func (s *Service) DeletePrompt(ctx context.Context, kind, sourceLanguage, targetLanguage string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM llm_prompts WHERE kind=$1 AND source_language=$2 AND target_language=$3`, kind, sourceLanguage, targetLanguage)
	return err
}

func (s *Service) prompt(ctx context.Context, kind, source, target string) Prompt {
	p := Prompt{Kind: kind, SourceLanguage: source, TargetLanguage: target, Model: s.cfg.Model}
	_ = s.db.QueryRow(ctx, `SELECT coalesce(model, ''), prompt FROM llm_prompts WHERE kind=$1 AND source_language=$2 AND target_language=$3`, kind, source, target).Scan(&p.Model, &p.Prompt)
	if strings.TrimSpace(p.Model) == "" {
		p.Model = s.cfg.Model
	}
	if strings.TrimSpace(p.Prompt) != "" {
		return p
	}
	if kind == "transcription" {
		p.Prompt = "Create a romanized transcription for the source language content. Return only the transcription, preserving line breaks and structure. Do not add explanations."
		return p
	}
	p.Prompt = "Translate the source language content to the target language. Return only the translation, preserving line breaks and structure. Do not add explanations."
	return p
}

func (s *Service) Generate(ctx context.Context, kind, sourceLanguage, targetLanguage, contentType, content string) (string, error) {
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("content is required")
	}
	prompt := s.prompt(ctx, kind, sourceLanguage, targetLanguage)
	user := fmt.Sprintf("Source language: %s\nTarget language: %s\nContent type: %s\n\n%s", sourceLanguage, targetLanguage, contentType, content)
	clientCfg := s.cfg
	clientCfg.Model = prompt.Model
	return llm.New(clientCfg).Complete(ctx, []llm.Message{{Role: "system", Content: prompt.Prompt}, {Role: "user", Content: user}})
}
