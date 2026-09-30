package ai

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// LanguageSections holds one source language's optional prompt sections,
// rendered into the per-item templates as {{grammarPrompt}} and
// {{transcriptionPrompt}}. A blank field renders as "", the same as an
// absent section file in the prompt-eval setup.
type LanguageSections struct {
	Language            string `json:"language"`
	GrammarPrompt       string `json:"grammar_prompt"`
	TranscriptionPrompt string `json:"transcription_prompt"`
}

func (s *Service) ListLanguageSections(ctx context.Context) ([]LanguageSections, error) {
	rows, err := s.db.Query(ctx, `SELECT language, grammar_prompt, transcription_prompt FROM language_llm_sections ORDER BY language`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LanguageSections{}
	for rows.Next() {
		var ls LanguageSections
		if err := rows.Scan(&ls.Language, &ls.GrammarPrompt, &ls.TranscriptionPrompt); err != nil {
			return nil, err
		}
		out = append(out, ls)
	}
	return out, rows.Err()
}

// GetLanguageSections returns language's sections, or blank sections (not
// an error) when none are configured.
func (s *Service) GetLanguageSections(ctx context.Context, language string) (LanguageSections, error) {
	ls := LanguageSections{Language: language}
	err := s.db.QueryRow(ctx, `SELECT grammar_prompt, transcription_prompt FROM language_llm_sections WHERE language = $1`, language).
		Scan(&ls.GrammarPrompt, &ls.TranscriptionPrompt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ls, nil
	}
	return ls, err
}

func (s *Service) SetLanguageSections(ctx context.Context, ls LanguageSections) error {
	_, err := s.db.Exec(ctx, `INSERT INTO language_llm_sections(language, grammar_prompt, transcription_prompt) VALUES($1,$2,$3) ON CONFLICT(language) DO UPDATE SET grammar_prompt=excluded.grammar_prompt, transcription_prompt=excluded.transcription_prompt, updated_at=now()`, ls.Language, ls.GrammarPrompt, ls.TranscriptionPrompt)
	return err
}

func (s *Service) DeleteLanguageSections(ctx context.Context, language string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM language_llm_sections WHERE language = $1`, language)
	return err
}
