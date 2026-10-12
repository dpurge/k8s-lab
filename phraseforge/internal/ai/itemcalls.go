package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"phraseforge/internal/i18n"
	"phraseforge/internal/ime"
	"phraseforge/internal/textnorm"
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

// renderSystemPrompt fills the snippet placeholders of a system prompt from
// language's effective sections, the same ones the item prompts get. A prompt
// without a placeholder is returned unchanged, without a database lookup.
func (s *Service) renderSystemPrompt(ctx context.Context, template, language string) (string, error) {
	if !usesSnippets(template) {
		return template, nil
	}
	sections, err := s.EffectiveSections(ctx, language)
	if err != nil {
		return "", fmt.Errorf("load language sections for %s: %w", language, err)
	}
	return renderSnippets(template, sections), nil
}

// ItemState is what an item already has, for ItemCallLocales.
type ItemState struct {
	ResourceType  string          // "vocabulary_item" or "models_item"
	Translated    map[string]bool // site locales with a non-blank translation
	Grammar       string          // vocabulary only
	Transcription string
	// NeedsExample asks for one call even when nothing else is missing: the
	// generate step found a usage that the phrase's examples do not cover yet.
	NeedsExample bool // vocabulary only
	// NeedsSense asks for a call in every locale the phrase already has a
	// translation in: the usage shows a meaning that translation may not cover.
	NeedsSense bool // vocabulary only
}

// ItemCallLocales is which site locales an item needs one structured call
// for: every locale still missing a translation, and with NeedsSense every
// locale that has one too; or, when none is missing
// but a field the language has a section for is blank (grammar for
// vocabulary, transcription for both) or the item NeedsExample, the first site
// locale — any call fills those, since they don't depend on the target;
// otherwise none.
func ItemCallLocales(item ItemState, sections LanguageSections, siteLocales []i18n.Locale) []string {
	var out []string
	for _, l := range siteLocales {
		if !item.Translated[l.Code] || item.NeedsSense {
			out = append(out, l.Code)
		}
	}
	if len(out) > 0 || len(siteLocales) == 0 {
		return out
	}
	grammarMissing := item.ResourceType == resourceVocabularyItem &&
		strings.TrimSpace(sections.GrammarPrompt) != "" && strings.TrimSpace(item.Grammar) == ""
	transcriptionMissing := strings.TrimSpace(sections.TranscriptionPrompt) != "" && strings.TrimSpace(item.Transcription) == ""
	if grammarMissing || transcriptionMissing || item.NeedsExample {
		return []string{siteLocales[0].Code}
	}
	return nil
}

// ItemJob is one job to enqueue: Kind plus its marshalled ItemJobPayload.
type ItemJob struct {
	Kind    string
	Payload json.RawMessage
}

// MaxUsageChars is the longest usage hint an item job carries, and the longest
// example it may return, in characters. It is the limit of a stored example
// (vocabulary.MaxExampleChars, which this package does not import; a test
// elsewhere keeps the two equal).
const MaxUsageChars = 80

// cleanUsage returns usage as a job carries it: only for a vocabulary item,
// trimmed and NFC-normalised, and dropped altogether when it is longer than
// MaxUsageChars (a sentence, not the short phrase asked for).
func cleanUsage(resourceType, usage string) string {
	if resourceType != resourceVocabularyItem {
		return ""
	}
	usage = textnorm.Phrase(usage)
	if utf8.RuneCountInString(usage) > MaxUsageChars {
		return ""
	}
	return usage
}

// usableExample returns example as the job would store it: trimmed and
// NFC-normalised, or "" when it is blank or longer than MaxUsageChars (a
// sentence, not the short phrase asked for) — which means no example, not an
// error.
func usableExample(example string) string {
	example = textnorm.Phrase(example)
	if utf8.RuneCountInString(example) > MaxUsageChars {
		return ""
	}
	return example
}

// NewItemJob builds the job translating item itemID into locale. usage is the
// optional hint described on ItemJobPayload.Usage.
func NewItemJob(resourceType string, itemID int64, locale, usage string) (ItemJob, error) {
	return newItemJob(resourceType, itemID, locale, usage, false)
}

// newItemJob is NewItemJob with the sense flag: the job may append a new sense
// to the item's existing translation in locale (see ItemJobPayload.Sense). The
// flag only means something with a usage, so a job without one never has it.
func newItemJob(resourceType string, itemID int64, locale, usage string, sense bool) (ItemJob, error) {
	kind := KindGenerateVocabularyItem
	if resourceType == resourceModelsItem {
		kind = KindGenerateModelsItem
	}
	p := ItemJobPayload{ResourceType: resourceType, ItemID: itemID, Locale: locale, Usage: cleanUsage(resourceType, usage)}
	p.Sense = sense && p.Usage != ""
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
// one list's items to avoid a lookup per item; nil means look it up. usage is
// put in every job of the item (see NewItemJob).
func (s *Service) ItemJobsFor(ctx context.Context, item ItemState, itemID int64, language, usage string, sections *LanguageSections, siteLocales []i18n.Locale) ([]ItemJob, error) {
	if sections == nil {
		eff, err := s.EffectiveSections(ctx, language)
		if err != nil {
			return nil, err
		}
		sections = &eff
	}
	var jobs []ItemJob
	for _, locale := range ItemCallLocales(item, *sections, siteLocales) {
		// Only a locale that already has a translation can get a new sense.
		j, err := newItemJob(item.ResourceType, itemID, locale, usage, item.NeedsSense && item.Translated[locale])
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}
