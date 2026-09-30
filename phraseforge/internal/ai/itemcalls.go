package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"phraseforge/internal/i18n"
	"phraseforge/internal/ime"
)

// DefaultTranscriptionSection stands in for {{transcriptionPrompt}} when a
// language needs transcription (its IME config says so) but has no
// transcription section yet — so its items keep getting a transcription,
// as they did before sections existed. An admin section replaces it.
const DefaultTranscriptionSection = "Transcribe using the standard romanization for this language."

// EffectiveSections is language's stored sections, with
// DefaultTranscriptionSection filled in as described on that constant.
func (s *Service) EffectiveSections(ctx context.Context, language string) (LanguageSections, error) {
	sections, err := s.GetLanguageSections(ctx, language)
	if err != nil {
		return LanguageSections{}, err
	}
	if strings.TrimSpace(sections.TranscriptionPrompt) != "" {
		return sections, nil
	}
	needs, err := ime.NeedsTranscriptionForLanguage(ctx, s.db, language)
	if err != nil {
		return LanguageSections{}, fmt.Errorf("check needs_transcription for language %s: %w", language, err)
	}
	if needs {
		sections.TranscriptionPrompt = DefaultTranscriptionSection
	}
	return sections, nil
}

// ItemState is what an item already has, for ItemCallLocales.
type ItemState struct {
	ResourceType  string          // "vocabulary_item" or "models_item"
	Translated    map[string]bool // site locales with a non-blank translation
	Grammar       string          // vocabulary only
	Transcription string
}

// ItemCallLocales is which site locales an item needs one structured call
// for: every locale still missing a translation; or, when none is missing
// but a field the language has a section for is blank (grammar for
// vocabulary, transcription for both), the first site locale — any call
// fills those, since they don't depend on the target; otherwise none.
func ItemCallLocales(item ItemState, sections LanguageSections, siteLocales []i18n.Locale) []string {
	var out []string
	for _, l := range siteLocales {
		if !item.Translated[l.Code] {
			out = append(out, l.Code)
		}
	}
	if len(out) > 0 || len(siteLocales) == 0 {
		return out
	}
	grammarMissing := item.ResourceType == resourceVocabularyItem &&
		strings.TrimSpace(sections.GrammarPrompt) != "" && strings.TrimSpace(item.Grammar) == ""
	transcriptionMissing := strings.TrimSpace(sections.TranscriptionPrompt) != "" && strings.TrimSpace(item.Transcription) == ""
	if grammarMissing || transcriptionMissing {
		return []string{siteLocales[0].Code}
	}
	return nil
}

// ItemJob is one job to enqueue: Kind plus its marshalled ItemJobPayload.
type ItemJob struct {
	Kind    string
	Payload json.RawMessage
}

// NewItemJob builds the job translating one item into locale.
func NewItemJob(resourceType string, listID int64, position int, language, locale, phrase string) (ItemJob, error) {
	kind := KindGenerateVocabularyItem
	if resourceType == resourceModelsItem {
		kind = KindGenerateModelsItem
	}
	p := ItemJobPayload{ResourceType: resourceType, ListID: listID, Position: position, Language: language, Locale: locale, Phrase: phrase}
	if err := p.validate(); err != nil {
		return ItemJob{}, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return ItemJob{}, err
	}
	return ItemJob{Kind: kind, Payload: raw}, nil
}

// ItemJobsFor is ItemCallLocales plus NewItemJob for one item, with the
// language's effective sections looked up here — the single entry point
// every bulk caller (import, generate-from-text, Generate missing
// translations) uses. sections may be passed in by a caller looping over
// one list's items to avoid a lookup per item; nil means look it up.
func (s *Service) ItemJobsFor(ctx context.Context, item ItemState, listID int64, position int, language, phrase string, sections *LanguageSections, siteLocales []i18n.Locale) ([]ItemJob, error) {
	if sections == nil {
		eff, err := s.EffectiveSections(ctx, language)
		if err != nil {
			return nil, err
		}
		sections = &eff
	}
	var jobs []ItemJob
	for _, locale := range ItemCallLocales(item, *sections, siteLocales) {
		j, err := NewItemJob(item.ResourceType, listID, position, language, locale, phrase)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}
