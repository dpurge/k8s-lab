package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode/utf8"

	"k8s-lab/shared/llm"

	"phraseforge/internal/config"
)

// The text and dialog reviews are two purposes, each with its own model,
// prompt and reply schema: one reviews the transcription, one the translations.
// Both default their provider and model to the review-vocabulary ones (see
// reviewTextDefault), so one stronger model set for reviews serves all three.
const (
	KindReviewTranscription = "review_transcription"
	KindReviewTranslation   = "review_translation"
)

// KindReviewTextJob is the job kind of a text or dialog review; the payload's
// part says which one.
const KindReviewTextJob = "review_text"

// The parts of a text or dialog that are reviewed separately, each with its own
// job, call and stored proposals.
const (
	ReviewPartTranscription = "transcription"
	ReviewPartTranslation   = "translation"
)

// IsReviewPart reports whether part is one of the reviewable parts.
func IsReviewPart(part string) bool {
	return part == ReviewPartTranscription || part == ReviewPartTranslation
}

// reviewKindOf is the purpose that reviews part.
func reviewKindOf(part string) string {
	if part == ReviewPartTranscription {
		return KindReviewTranscription
	}
	return KindReviewTranslation
}

// maxTextReviewChars bounds what one review call is given (source and the
// reviewed part together, in characters): a longer resource fails the job with a
// clear message instead of overflowing the model.
const maxTextReviewChars = 12000

// ReviewTranscriptionSchema is the reply schema of the transcription review: one
// {ok, suggestion, reason} check; a suggestion is the whole corrected
// transcription.
var ReviewTranscriptionSchema = json.RawMessage(`{"type":"object","required":["ok","suggestion","reason"],"properties":{"ok":{"type":"boolean"},"suggestion":{"type":"string"},"reason":{"type":"string"}}}`)

// ReviewTranslationSchema is the reply schema of the translation review: one
// check per locale; a suggestion is the whole corrected translation.
var ReviewTranslationSchema = json.RawMessage(`{"type":"object","required":["translations"],"properties":{"translations":{"type":"array","items":{"type":"object","required":["locale","ok","suggestion","reason"],"properties":{"locale":{"type":"string"},"ok":{"type":"boolean"},"suggestion":{"type":"string"},"reason":{"type":"string"}}}}}}`)

// reviewTextDefault is purpose kind's default with a blank provider or model
// taken from reviewVocabulary (which itself falls back to vocabularyItem).
func (s *Service) reviewTextDefault(kind string) config.PurposeConfig {
	def := s.cfg.ReviewTranslation
	if kind == KindReviewTranscription {
		def = s.cfg.ReviewTranscription
	}
	vocabulary := s.reviewVocabularyDefault()
	if strings.TrimSpace(def.Provider) == "" {
		def.Provider, def.Model = vocabulary.Provider, vocabulary.Model
	} else if strings.TrimSpace(def.Model) == "" {
		def.Model = vocabulary.Model
	}
	return def
}

// TextReviewVocabulary is one phrase of the vocabulary attached to a text, with
// its non-blank translations by locale.
type TextReviewVocabulary struct {
	Phrase       string
	Translations map[string]string
}

// TextReviewInput is what one text review call is given: a text or dialog as
// stored, with the vocabulary list generated from it. Version identifies the
// source text the review is about (a hash the store computes); a proposal is
// only applied while the resource still has it.
type TextReviewInput struct {
	Language      string
	Body          string
	Transcription string
	Translations  []ReviewTranslation // Translation holds the whole translated text
	Vocabulary    []TextReviewVocabulary
	Version       string
}

// textReviewPromptVars fills the text review prompt template.
type textReviewPromptVars struct {
	SourceLanguage, Text, Transcription, Translations, Vocabulary, TranscriptionPrompt string
}

func renderTextReviewPrompt(template string, v textReviewPromptVars) string {
	return strings.NewReplacer(
		"{{sourceLanguage}}", v.SourceLanguage,
		"{{text}}", v.Text,
		"{{transcription}}", v.Transcription,
		"{{translations}}", v.Translations,
		"{{vocabulary}}", v.Vocabulary,
		"{{transcriptionPrompt}}", v.TranscriptionPrompt,
	).Replace(template)
}

// formatTextTranslations lays the translations out as "[locale]" lines each
// followed by the text, English first (the reference), then locale-code order.
func formatTextTranslations(translations []ReviewTranslation) string {
	sorted := slices.Clone(translations)
	slices.SortStableFunc(sorted, func(a, b ReviewTranslation) int {
		order := func(l string) string {
			if l == "en" {
				return ""
			}
			return l
		}
		return strings.Compare(order(a.Locale), order(b.Locale))
	})
	blocks := make([]string, len(sorted))
	for i, t := range sorted {
		blocks[i] = "[" + t.Locale + "]\n" + t.Translation
	}
	return orNone(strings.Join(blocks, "\n\n"))
}

// formatTextVocabulary lists the attached vocabulary, one phrase per line with
// its translations ("phrase = en: x | pl: y"), none when the list is empty.
func formatTextVocabulary(entries []TextReviewVocabulary) string {
	lines := make([]string, len(entries))
	for i, e := range entries {
		locales := make([]string, 0, len(e.Translations))
		for l := range e.Translations {
			locales = append(locales, l)
		}
		slices.Sort(locales)
		parts := make([]string, len(locales))
		for j, l := range locales {
			parts[j] = l + ": " + e.Translations[l]
		}
		lines[i] = "- " + e.Phrase
		if len(parts) > 0 {
			lines[i] += " = " + strings.Join(parts, " | ")
		}
	}
	return orNone(strings.Join(lines, "\n"))
}

// parseTextReviewReply reads the reply of the review of part into findings. The
// transcription reply is one check (a finding only when the resource has a
// transcription and its language has a transcription snippet); the translation
// reply has a "translations" array with one check per locale. A reply that is
// not the expected JSON object is an error (the call is corrected); a malformed
// entry, an unknown locale, and a check without a fix or a reason are ignored.
func parseTextReviewReply(raw, part string, in TextReviewInput, sections LanguageSections) ([]ReviewFinding, error) {
	body := []byte(strings.TrimSpace(raw))
	if part == ReviewPartTranscription {
		var reply struct {
			OK *bool `json:"ok"`
		}
		if err := json.Unmarshal(body, &reply); err != nil {
			return nil, fmt.Errorf("the reply is not a JSON object: %w", err)
		}
		if reply.OK == nil {
			return nil, fmt.Errorf(`the reply has no boolean "ok"`)
		}
		var findings []ReviewFinding
		if hasSnippet(sections.TranscriptionPrompt) && strings.TrimSpace(in.Transcription) != "" {
			entry, _ := decodeReviewEntry(body)
			if f, ok := reviewField(ReviewFieldTranscription, entry, in.Transcription, false); ok {
				findings = append(findings, f)
			}
		}
		return findings, nil
	}
	var reply struct {
		Translations *[]json.RawMessage `json:"translations"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return nil, fmt.Errorf("the reply is not a JSON object: %w", err)
	}
	if reply.Translations == nil {
		return nil, fmt.Errorf(`the reply has no "translations" array`)
	}
	return localeFindings(*reply.Translations, ReviewFieldTranslation, in.Translations,
		func(t ReviewTranslation) string { return t.Translation }, false), nil
}

// ReviewResource asks the model, in one structured call, to review one part of
// in (the transcription or the translations) and returns its findings; the
// other part is not sent. The call is corrected within the purpose's attempt
// budget. A resource whose source and reviewed part together exceed
// maxTextReviewChars is refused.
func (s *Service) ReviewResource(ctx context.Context, in TextReviewInput, part string) (ReviewResult, error) {
	if !IsReviewPart(part) {
		return ReviewResult{}, fmt.Errorf("review: unknown part %q", part)
	}
	if part == ReviewPartTranscription {
		in.Translations = nil
	} else {
		in.Transcription = ""
	}
	size := utf8.RuneCountInString(in.Body) + utf8.RuneCountInString(in.Transcription)
	for _, t := range in.Translations {
		size += utf8.RuneCountInString(t.Translation)
	}
	if size > maxTextReviewChars {
		return ReviewResult{}, fmt.Errorf("too long to review in one call: %d characters, the limit is %d", size, maxTextReviewChars)
	}
	name, err := s.languageName(ctx, in.Language)
	if err != nil {
		return ReviewResult{}, err
	}
	sections, err := s.EffectiveSections(ctx, in.Language)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("load language sections for %s: %w", in.Language, err)
	}
	kind := reviewKindOf(part)
	schema := ReviewTranslationSchema
	if part == ReviewPartTranscription {
		schema = ReviewTranscriptionSchema
	}
	def := s.purposeDefault(kind)
	prompt := s.prompt(ctx, kind, in.Language, kind)
	text := renderTextReviewPrompt(prompt.Prompt, textReviewPromptVars{
		SourceLanguage: name, Text: in.Body, Transcription: orNone(in.Transcription),
		Translations: formatTextTranslations(in.Translations), Vocabulary: formatTextVocabulary(in.Vocabulary),
		TranscriptionPrompt: sections.TranscriptionPrompt,
	})
	msgs := []llm.Message{{Role: "user", Content: text}}
	maxAttempts := effectiveMaxAttempts(def.MaxAttempts)
	attempt := 0
	raw, err := runWithCorrection(ctx, maxAttempts, s.sleeper(),
		func(ctx context.Context, m []llm.Message) (string, error) {
			attempt++
			return s.callLLM(ctx, kind, prompt, def, m, schema, attempt, maxAttempts)
		},
		func(raw string) error {
			_, err := parseTextReviewReply(raw, part, in, sections)
			if err != nil {
				slog.Info("llm reply rejected", "kind", kind, "outcome", "invalid_reply", "attempt", attempt, "max_attempts", maxAttempts)
			}
			return err
		},
		msgs)
	if err != nil {
		return ReviewResult{}, err
	}
	findings, err := parseTextReviewReply(raw, part, in, sections)
	if err != nil {
		return ReviewResult{}, err
	}
	return ReviewResult{Findings: findings, Response: json.RawMessage(strings.TrimSpace(raw))}, nil
}

// TextReviewJobPayload identifies the text or dialog to review.
type TextReviewJobPayload struct {
	ResourceType string `json:"resource_type"` // "text" or "dialog"
	ResourceID   int64  `json:"resource_id"`
	Part         string `json:"part"` // ReviewPartTranscription or ReviewPartTranslation
}

// TextReviewStore is the consumer-side interface of what HandleReviewResource
// reads and writes; both methods return ErrItemGone when the resource is gone
// and SaveTextReview ErrItemChanged when its source text is no longer the one
// that was reviewed (version).
type TextReviewStore interface {
	TextReviewSource(ctx context.Context, resourceType string, id int64) (TextReviewInput, error)
	SaveTextReview(ctx context.Context, resourceType string, id int64, part, version string, findings, response json.RawMessage) error
}

// SetTextReviewStore sets the store the text review job uses.
func (s *Service) SetTextReviewStore(r TextReviewStore) { s.textReviews = r }

// NewTextReviewJob builds the job reviewing one part of the text or dialog id.
func NewTextReviewJob(resourceType string, id int64, part string) (ItemJob, error) {
	if !IsReviewPart(part) {
		return ItemJob{}, fmt.Errorf("review job: unknown part %q", part)
	}
	if resourceType != "text" && resourceType != "dialog" {
		return ItemJob{}, fmt.Errorf("review job: unknown resource_type %q", resourceType)
	}
	if id == 0 {
		return ItemJob{}, fmt.Errorf("review job: resource_id is required")
	}
	payload, err := json.Marshal(TextReviewJobPayload{ResourceType: resourceType, ResourceID: id, Part: part})
	if err != nil {
		return ItemJob{}, err
	}
	return ItemJob{Kind: KindReviewTextJob, Payload: payload}, nil
}

// HandleReviewResource is the jobs.HandlerFunc of KindReviewTextJob: it reads
// the resource, makes one review call and stores the findings as its review
// (an empty one too). A resource deleted or edited since the job was queued, or
// while the model worked, ends the job as skipped.
func (s *Service) HandleReviewResource(ctx context.Context, id string, payload json.RawMessage) (json.RawMessage, error) {
	var p TextReviewJobPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	if _, err := NewTextReviewJob(p.ResourceType, p.ResourceID, p.Part); err != nil {
		return nil, err
	}
	if s.textReviews == nil {
		return nil, fmt.Errorf("review job: no text review store configured")
	}
	in, err := s.textReviews.TextReviewSource(ctx, p.ResourceType, p.ResourceID)
	if err != nil {
		return nil, err
	}
	result, err := s.ReviewResource(ctx, in, p.Part)
	if err != nil {
		return nil, err
	}
	findings := result.Findings
	if findings == nil {
		findings = []ReviewFinding{}
	}
	for i := range findings {
		findings[i].Status = ReviewOpen
	}
	stored, err := json.Marshal(findings)
	if err != nil {
		return nil, err
	}
	if err := s.textReviews.SaveTextReview(ctx, p.ResourceType, p.ResourceID, p.Part, in.Version, stored, result.Response); err != nil {
		return nil, err
	}
	return json.Marshal(reviewJobResult{Response: json.RawMessage(strings.TrimSpace(string(result.Response))), Findings: len(findings)})
}
