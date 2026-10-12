package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"k8s-lab/shared/llm"

	"phraseforge/internal/catalog"
	"phraseforge/internal/config"
	"phraseforge/internal/i18n"
	"phraseforge/internal/textnorm"
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

// ItemJobPayload identifies the item to translate by its id, which survives
// the list being reordered or regenerated while the job waits in the queue.
// Everything else (the item's phrase, its list's language) is read when the
// job starts.
type ItemJobPayload struct {
	ResourceType string `json:"resource_type"` // "vocabulary_item" or "models_item"
	ItemID       int64  `json:"item_id"`
	Locale       string `json:"locale"` // the target site locale
	// Usage is a short phrase from the generated text that shows how the word
	// was used there, a hint for the job; vocabulary items only, and absent
	// for every job not made by generating a list.
	Usage string `json:"usage,omitempty"`
	// Sense lets the job append a new sense to the item's existing translation
	// in Locale (vocabulary items only; set with a usage, for the locales of a
	// phrase the check marked needsSense). Absent otherwise.
	Sense bool `json:"sense,omitempty"`
}

// loadedItem is a job's item as it was when the job started: the payload plus
// what ItemSource returned. The phrase doubles as the stale-target guard of
// every write, so a phrase edited meanwhile is never overwritten.
type loadedItem struct {
	ItemJobPayload
	ItemSource
}

// itemWrites is what a validated reply may write. A blank field is never
// written; grammar and transcription are only written when the language
// has that section, since without one the prompt asks for nothing and any
// value the model volunteers is a guess.
type itemWrites struct {
	Translation, Notes, Transcription, Grammar string
	// Example is the usable example of the reply (see usableExample), or "".
	Example string
	// Sense is the reply's new sense when this job may append one (its payload
	// has Sense), else "".
	Sense string
	// Rewrite is the reply's phrase rewrite when this job may apply one (see
	// decideItemWrites), else nil. Its grammar and transcription are only kept
	// when the language has that section, like the fields above.
	Rewrite *PhraseRewrite
}

// itemJobResult is the job's result for the Jobs page: what the model
// returned and which fields were actually stored (false = the field
// already had a value, or there was nothing to write).
type itemJobResult struct {
	Response json.RawMessage `json:"response"`
	Applied  map[string]bool `json:"applied"`
}

// HandleItemTranslation is the jobs.HandlerFunc for both item kinds. An item
// deleted or changed since the job was queued ends it as skipped (see
// jobs.ErrSkipped), not failed.
func (s *Service) HandleItemTranslation(ctx context.Context, id string, payload json.RawMessage) (json.RawMessage, error) {
	var p ItemJobPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	if s.items == nil {
		return nil, fmt.Errorf("item job: no item store configured")
	}
	src, err := s.items.ItemSource(ctx, p.ResourceType, p.ItemID)
	if err != nil {
		return nil, err
	}
	it := loadedItem{ItemJobPayload: p, ItemSource: src}
	sections, err := s.EffectiveSections(ctx, it.Language)
	if err != nil {
		return nil, err
	}
	vars, err := s.itemPromptVars(ctx, it, sections)
	if err != nil {
		return nil, err
	}
	def := s.purposeDefault(it.ResourceType)
	prompt := s.prompt(ctx, it.ResourceType, it.Language, it.Locale)
	schema := VocabularyItemSchema
	if it.ResourceType == resourceModelsItem {
		schema = ModelsItemSchema
	}
	// One user message, no system message — how promptfoo sends a text
	// prompt file, so a template tuned in prompt-eval behaves the same here.
	msgs := []llm.Message{{Role: "user", Content: renderItemPrompt(prompt.Prompt, vars)}}
	raw, err := s.callItemWithCorrection(ctx, it, prompt, def, schema, msgs, sections)
	if err != nil {
		return nil, err
	}
	writes, err := decideItemWrites(it, raw, sections)
	if err != nil {
		return nil, err
	}
	applied, err := s.applyItemWrites(ctx, it, writes)
	if err != nil {
		return nil, err
	}
	return json.Marshal(itemJobResult{Response: json.RawMessage(strings.TrimSpace(raw)), Applied: applied})
}

// callItemWithCorrection calls the model and validates its reply, sending a
// rejected reply back with the exact error for correction (see
// runWithCorrection) instead of failing the job outright. The attempt budget
// is the purpose's MaxAttempts.
func (s *Service) callItemWithCorrection(ctx context.Context, it loadedItem, prompt Prompt, def config.PurposeConfig, schema json.RawMessage, msgs []llm.Message, sections LanguageSections) (string, error) {
	maxAttempts := effectiveMaxAttempts(def.MaxAttempts)
	attempt := 0
	return runWithCorrection(ctx, maxAttempts, s.sleeper(),
		func(ctx context.Context, m []llm.Message) (string, error) {
			attempt++
			return s.callLLM(ctx, it.ResourceType, prompt, def, m, schema, attempt, maxAttempts)
		},
		func(raw string) error {
			_, err := decideItemWrites(it, raw, sections)
			if err != nil {
				// Telemetry only — the error text embeds the model's reply.
				slog.Info("llm reply rejected", "kind", it.ResourceType, "outcome", "invalid_reply", "attempt", attempt, "max_attempts", maxAttempts)
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
	if p.ItemID == 0 {
		return fmt.Errorf("item job: item_id is required")
	}
	return nil
}

// itemPromptVars resolves the eval-style names: the source language from
// the language table (falling back to its code), the target from the
// locale's PromptName.
func (s *Service) itemPromptVars(ctx context.Context, it loadedItem, sections LanguageSections) (ItemPromptVars, error) {
	sourceName, err := s.languageName(ctx, it.Language)
	if err != nil {
		return ItemPromptVars{}, err
	}
	return ItemPromptVars{
		SourceLanguage: sourceName, TargetLanguage: localePromptName(it.Locale), Phrase: it.Phrase,
		GrammarPrompt: sections.GrammarPrompt, TranscriptionPrompt: sections.TranscriptionPrompt, DictionaryPrompt: sections.DictionaryPrompt,
		Usage: cleanUsage(it.ResourceType, it.Usage), Examples: formatExamples(it.Examples),
		Translation: it.Translations[it.Locale],
	}, nil
}

// languageName is the name a prompt uses for language code: the language
// table's name, or the code itself when the table has none.
func (s *Service) languageName(ctx context.Context, code string) (string, error) {
	lang, ok, err := catalog.GetLanguageIfExists(ctx, s.db, code)
	if err != nil {
		return "", err
	}
	if ok && lang.Name != "" {
		return lang.Name, nil
	}
	return code, nil
}

func localePromptName(code string) string {
	for _, l := range i18n.Locales {
		if l.Code == code {
			return l.PromptName
		}
	}
	return code
}

// decideItemWrites validates raw against the item's schema and reduces it to
// the fields this job may write (see itemWrites).
func decideItemWrites(it loadedItem, raw string, sections LanguageSections) (itemWrites, error) {
	hasTranscription := strings.TrimSpace(sections.TranscriptionPrompt) != ""
	if it.ResourceType == resourceModelsItem {
		r, err := parseModelsItemResponse(raw, it.Phrase)
		if err != nil {
			return itemWrites{}, err
		}
		w := itemWrites{Translation: r.Translation}
		if hasTranscription {
			w.Transcription = r.Transcription
		}
		return w, nil
	}
	r, err := parseVocabularyItemResponse(raw, it.Phrase)
	if err != nil {
		return itemWrites{}, err
	}
	w := itemWrites{Translation: r.Translation, Notes: r.Notes, Example: usableExample(r.Example)}
	if hasTranscription {
		w.Transcription = r.Transcription
	}
	if strings.TrimSpace(sections.GrammarPrompt) != "" {
		w.Grammar = r.Grammar
	}
	if it.Sense {
		// The model often puts the extra meaning into translation instead; the
		// existing translation is never rewritten, so both are recovered here.
		w.Sense = r.NewSense
		if w.Sense == "" {
			w.Sense = senseFromTranslation(it.Translations[it.Locale], r.Translation)
		}
	}
	w.Rewrite = usableRewrite(it, r.Rewrite, sections)
	return w, nil
}

// usableRewrite returns rewrite as the job may apply it, or nil: only a job
// that carries a usage may rewrite (the model had the text context; a job from
// a button or an import never rewrites what a user typed), and only to a phrase
// that differs from the stored one after trim and NFC. Grammar and
// transcription are dropped, meaning "keep", where the language has no section.
func usableRewrite(it loadedItem, rewrite *PhraseRewrite, sections LanguageSections) *PhraseRewrite {
	if rewrite == nil || cleanUsage(it.ResourceType, it.Usage) == "" {
		return nil
	}
	if textnorm.Phrase(rewrite.Phrase) == textnorm.Phrase(it.Phrase) {
		return nil
	}
	out := *rewrite
	if strings.TrimSpace(sections.GrammarPrompt) == "" {
		out.Grammar = ""
	}
	if strings.TrimSpace(sections.TranscriptionPrompt) == "" {
		out.Transcription = ""
	}
	return &out
}

// applyItemWrites stores w's non-blank fields, each only where still blank,
// translation first. Any store error fails the job, except an item that is
// gone or changed (ai.ErrItemGone, ai.ErrItemChanged), which skips it.
func (s *Service) applyItemWrites(ctx context.Context, it loadedItem, w itemWrites) (map[string]bool, error) {
	applied := map[string]bool{"translation": false, "transcription": false}
	if it.ResourceType == resourceVocabularyItem {
		applied["grammar"] = false
		applied["example"] = false
	}
	ok, err := s.items.FillTranslation(ctx, it.ResourceType, it.ItemID, it.Phrase, it.Locale, w.Translation, w.Notes)
	if err != nil {
		return nil, err
	}
	applied["translation"] = ok
	if w.Transcription != "" {
		if applied["transcription"], err = s.items.FillTranscription(ctx, it.ResourceType, it.ItemID, it.Phrase, w.Transcription); err != nil {
			return nil, err
		}
	}
	if w.Grammar != "" {
		if applied["grammar"], err = s.items.FillGrammar(ctx, it.ItemID, it.Phrase, w.Grammar); err != nil {
			return nil, err
		}
	}
	if w.Example != "" {
		if applied["example"], err = s.items.FillExample(ctx, it.ItemID, it.Phrase, w.Example); err != nil {
			return nil, err
		}
	}
	if it.ResourceType == resourceVocabularyItem {
		applied["sense"] = s.applySense(ctx, it, w.Sense)
		applied["rewrite"] = s.applyRewrite(ctx, it, w.Rewrite)
	}
	return applied, nil
}

// applySense appends the new sense, if any, to the item's translation in the
// job's locale, after the example and before the rewrite. Best-effort like the
// rewrite: a sense that is skipped (the item changed or is gone) or fails to
// store is logged and the job still succeeds.
func (s *Service) applySense(ctx context.Context, it loadedItem, sense string) bool {
	if sense == "" {
		return false
	}
	applied, err := s.items.AppendSense(ctx, it.ItemID, it.Phrase, it.Locale, sense)
	if err != nil {
		// Telemetry only — never log the phrase or the error text, which may embed it.
		slog.Info("item sense skipped", "item_id", it.ItemID, "outcome", rewriteOutcome(err))
		return false
	}
	return applied
}

// applyRewrite applies the rewrite, if any, after every other write of the job
// (those target the phrase the job started with). It is best-effort: a rewrite
// that is skipped (the item changed or is gone) or fails is logged and the job
// still succeeds, since the translation it came with is already stored.
func (s *Service) applyRewrite(ctx context.Context, it loadedItem, rewrite *PhraseRewrite) bool {
	if rewrite == nil {
		return false
	}
	applied, err := s.items.RewritePhrase(ctx, it.ItemID, it.Phrase, rewrite.Phrase, rewrite.Grammar, rewrite.Transcription)
	if err != nil {
		// Telemetry only — never log the phrase or the error text, which may embed it.
		slog.Info("item rewrite skipped", "item_id", it.ItemID, "outcome", rewriteOutcome(err))
		return false
	}
	return applied
}

// rewriteOutcome names why a rewrite was skipped, without any content.
func rewriteOutcome(err error) string {
	switch {
	case errors.Is(err, ErrItemGone):
		return "item_gone"
	case errors.Is(err, ErrItemChanged):
		return "item_changed"
	default:
		return "error"
	}
}
