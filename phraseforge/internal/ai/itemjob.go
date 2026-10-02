package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"k8s-lab/shared/llm"

	"phraseforge/internal/catalog"
	"phraseforge/internal/config"
	"phraseforge/internal/i18n"
)

// Job kinds for one structured translation of one vocabulary/models item
// into one site locale (phraseforge-structured-item-translation). Both run
// HandleItemTranslation; the payload's resource_type picks the schema.
const (
	KindGenerateVocabularyItem = "generate_vocabulary_item"
	KindGenerateModelsItem     = "generate_models_item"
)

const (
	resourceVocabularyItem = "vocabulary_item"
	resourceModelsItem     = "models_item"
)

// ItemJobPayload identifies the item to translate. Phrase is the item's
// phrase when the job was queued — the stale-target guard for every write.
type ItemJobPayload struct {
	ResourceType string `json:"resource_type"` // "vocabulary_item" or "models_item"
	ListID       int64  `json:"list_id"`
	Position     int    `json:"position"`
	Language     string `json:"language"` // the list's source language code
	Locale       string `json:"locale"`   // the target site locale
	Phrase       string `json:"phrase"`
}

// itemWrites is what a validated reply may write. A blank field is never
// written; grammar and transcription are only written when the language
// has that section, since without one the prompt asks for nothing and any
// value the model volunteers is a guess.
type itemWrites struct {
	Translation, Notes, Transcription, Grammar string
}

// itemJobResult is the job's result for the Jobs page: what the model
// returned and which fields were actually stored (false = the field
// already had a value, or there was nothing to write).
type itemJobResult struct {
	Response json.RawMessage `json:"response"`
	Applied  map[string]bool `json:"applied"`
}

// HandleItemTranslation is the jobs.HandlerFunc for both item kinds.
func (s *Service) HandleItemTranslation(ctx context.Context, id string, payload json.RawMessage) (json.RawMessage, error) {
	var p ItemJobPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	sections, err := s.EffectiveSections(ctx, p.Language)
	if err != nil {
		return nil, err
	}
	vars, err := s.itemPromptVars(ctx, p, sections)
	if err != nil {
		return nil, err
	}
	def := s.purposeDefault(p.ResourceType)
	prompt := s.prompt(ctx, p.ResourceType, p.Language, p.Locale)
	schema := VocabularyItemSchema
	if p.ResourceType == resourceModelsItem {
		schema = ModelsItemSchema
	}
	// One user message, no system message — how promptfoo sends a text
	// prompt file, so a template tuned in prompt-eval behaves the same here.
	msgs := []llm.Message{{Role: "user", Content: renderItemPrompt(prompt.Prompt, vars)}}
	raw, err := s.callItemWithCorrection(ctx, p, prompt, def, schema, msgs, sections)
	if err != nil {
		return nil, err
	}
	writes, err := decideItemWrites(p, raw, sections)
	if err != nil {
		return nil, err
	}
	applied, err := s.applyItemWrites(ctx, p, writes)
	if err != nil {
		return nil, err
	}
	return json.Marshal(itemJobResult{Response: json.RawMessage(strings.TrimSpace(raw)), Applied: applied})
}

// callItemWithCorrection calls the model and validates its reply, sending a
// rejected reply back with the exact error for correction (see
// runWithCorrection) instead of failing the job outright. The attempt budget
// is the purpose's MaxAttempts.
func (s *Service) callItemWithCorrection(ctx context.Context, p ItemJobPayload, prompt Prompt, def config.PurposeConfig, schema json.RawMessage, msgs []llm.Message, sections LanguageSections) (string, error) {
	maxAttempts := effectiveMaxAttempts(def.MaxAttempts)
	attempt := 0
	return runWithCorrection(ctx, maxAttempts, s.sleeper(),
		func(ctx context.Context, m []llm.Message) (string, error) {
			attempt++
			return s.callLLM(ctx, p.ResourceType, prompt, def, m, schema, attempt, maxAttempts)
		},
		func(raw string) error {
			_, err := decideItemWrites(p, raw, sections)
			if err != nil {
				// Telemetry only — the error text embeds the model's reply.
				slog.Info("llm reply rejected", "kind", p.ResourceType, "outcome", "invalid_reply", "attempt", attempt, "max_attempts", maxAttempts)
			}
			return err
		},
		msgs)
}

func (p ItemJobPayload) validate() error {
	if p.ResourceType != resourceVocabularyItem && p.ResourceType != resourceModelsItem {
		return fmt.Errorf("item job: unknown resource_type %q", p.ResourceType)
	}
	if !i18n.IsValid(p.Locale) {
		return fmt.Errorf("item job: unknown locale %q", p.Locale)
	}
	if strings.TrimSpace(p.Phrase) == "" || p.ListID == 0 || strings.TrimSpace(p.Language) == "" {
		return fmt.Errorf("item job: list_id, language, and phrase are required")
	}
	return nil
}

// itemPromptVars resolves the eval-style names: the source language from
// the language table (falling back to its code), the target from the
// locale's PromptName.
func (s *Service) itemPromptVars(ctx context.Context, p ItemJobPayload, sections LanguageSections) (ItemPromptVars, error) {
	sourceName := p.Language
	lang, ok, err := catalog.GetLanguageIfExists(ctx, s.db, p.Language)
	if err != nil {
		return ItemPromptVars{}, err
	}
	if ok && lang.Name != "" {
		sourceName = lang.Name
	}
	return ItemPromptVars{
		SourceLanguage: sourceName, TargetLanguage: localePromptName(p.Locale), Phrase: p.Phrase,
		GrammarPrompt: sections.GrammarPrompt, TranscriptionPrompt: sections.TranscriptionPrompt,
	}, nil
}

func localePromptName(code string) string {
	for _, l := range i18n.Locales {
		if l.Code == code {
			return l.PromptName
		}
	}
	return code
}

// decideItemWrites validates raw against p's schema and reduces it to the
// fields this job may write (see itemWrites).
func decideItemWrites(p ItemJobPayload, raw string, sections LanguageSections) (itemWrites, error) {
	hasTranscription := strings.TrimSpace(sections.TranscriptionPrompt) != ""
	if p.ResourceType == resourceModelsItem {
		r, err := parseModelsItemResponse(raw, p.Phrase)
		if err != nil {
			return itemWrites{}, err
		}
		w := itemWrites{Translation: r.Translation}
		if hasTranscription {
			w.Transcription = r.Transcription
		}
		return w, nil
	}
	r, err := parseVocabularyItemResponse(raw, p.Phrase)
	if err != nil {
		return itemWrites{}, err
	}
	w := itemWrites{Translation: r.Translation, Notes: r.Notes}
	if hasTranscription {
		w.Transcription = r.Transcription
	}
	if strings.TrimSpace(sections.GrammarPrompt) != "" {
		w.Grammar = r.Grammar
	}
	return w, nil
}

// applyItemWrites stores w's non-blank fields, each only where still
// blank, translation first. Any store error (including a phrase that no
// longer matches — the item moved or changed) fails the job.
func (s *Service) applyItemWrites(ctx context.Context, p ItemJobPayload, w itemWrites) (map[string]bool, error) {
	applied := map[string]bool{"translation": false, "transcription": false}
	if p.ResourceType == resourceVocabularyItem {
		applied["grammar"] = false
	}
	if s.itemReplies == nil {
		return nil, fmt.Errorf("item job: no item reply writeback configured")
	}
	ok, err := s.itemReplies.SetItemTranslationWithNotes(ctx, p.ResourceType, p.ListID, p.Position, p.Phrase, p.Locale, w.Translation, w.Notes)
	if err != nil {
		return nil, err
	}
	applied["translation"] = ok
	if w.Transcription != "" {
		store := s.itemTranscriptionWriteback(p.ResourceType)
		if store == nil {
			return nil, fmt.Errorf("item job: no transcription writeback for %q", p.ResourceType)
		}
		if applied["transcription"], err = store.SetItemTranscriptionIfBlank(ctx, p.ListID, p.Position, p.Phrase, w.Transcription); err != nil {
			return nil, err
		}
	}
	if w.Grammar != "" {
		if applied["grammar"], err = s.itemReplies.SetItemGrammarIfBlank(ctx, p.ListID, p.Position, p.Phrase, w.Grammar); err != nil {
			return nil, err
		}
	}
	return applied, nil
}
