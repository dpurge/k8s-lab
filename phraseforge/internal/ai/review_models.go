package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"k8s-lab/shared/llm"
)

// KindReviewModels is the purpose of the call that reviews one models item (its
// phrase, transcription and translations) and proposes corrections. It has its
// own prompt and model tags.
const KindReviewModels = "review_models"

// KindReviewModelsItem is the job kind that reviews one models item and stores
// the model's proposals for the user to accept or dismiss.
const KindReviewModelsItem = "review_models_item"

// ReviewModelsSchema is the reply schema of the models review: a {ok,
// suggestion, reason} check for the phrase and for the transcription, and one
// per locale for the translations.
var ReviewModelsSchema = json.RawMessage(`{"type":"object","required":["phrase","transcription","translations"],"properties":{"phrase":{"type":"object","required":["ok","suggestion","reason"],"properties":{"ok":{"type":"boolean"},"suggestion":{"type":"string"},"reason":{"type":"string"}}},"transcription":{"type":"object","required":["ok","suggestion","reason"],"properties":{"ok":{"type":"boolean"},"suggestion":{"type":"string"},"reason":{"type":"string"}}},"translations":{"type":"array","items":{"type":"object","required":["locale","ok","suggestion","reason"],"properties":{"locale":{"type":"string"},"ok":{"type":"boolean"},"suggestion":{"type":"string"},"reason":{"type":"string"}}}}}}`)

// renderReviewModelsPrompt substitutes the placeholders in one pass, so text
// inside a value is never itself expanded.
func renderReviewModelsPrompt(template string, in ReviewInput, language, transcriptionPrompt string) string {
	return strings.NewReplacer(
		"{{sourceLanguage}}", language,
		"{{phrase}}", in.Phrase,
		"{{transcription}}", orNone(in.Transcription),
		"{{translations}}", formatReviewTranslations(in.Translations),
		"{{transcriptionPrompt}}", transcriptionPrompt,
	).Replace(template)
}

// parseReviewModelsReply reads the reply of the models review into findings, in
// the order phrase, transcription, translations. The reply must be a JSON
// object with a "phrase" entry, and a phrase suggestion must come with a
// transcription suggestion when the item has a transcription the language keeps
// (the two change as one unit): anything else is an error and the call is
// corrected. The rest is lenient: a malformed entry, an unknown locale, a check
// without a fix or a reason, and a transcription entry for a language without a
// transcription snippet are ignored.
func parseReviewModelsReply(raw string, in ReviewInput, sections LanguageSections) ([]ReviewFinding, error) {
	var reply struct {
		Phrase        json.RawMessage   `json:"phrase"`
		Transcription json.RawMessage   `json:"transcription"`
		Translations  []json.RawMessage `json:"translations"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &reply); err != nil {
		return nil, fmt.Errorf("the reply is not a JSON object: %w", err)
	}
	if len(reply.Phrase) == 0 || string(reply.Phrase) == "null" {
		return nil, fmt.Errorf(`the reply has no "phrase" entry`)
	}
	phrase, _ := decodeReviewEntry(reply.Phrase)
	transcription, _ := decodeReviewEntry(reply.Transcription)
	transcriptionKept := hasSnippet(sections.TranscriptionPrompt)

	var findings []ReviewFinding
	rewritten := false
	if f, ok := reviewField(ReviewFieldPhrase, phrase, in.Phrase, false); ok {
		if f.Action == ReviewReplace {
			rewritten = true
			f.Transcription = transcription.suggestion()
			if transcriptionKept && strings.TrimSpace(in.Transcription) != "" && f.Transcription == "" {
				return nil, fmt.Errorf(`the "phrase" suggestion needs a "transcription" suggestion for the new phrase (repeat the current transcription if it stays valid)`)
			}
		}
		findings = append(findings, f)
	}
	if !rewritten && transcriptionKept {
		if f, ok := reviewField(ReviewFieldTranscription, transcription, in.Transcription, false); ok {
			findings = append(findings, f)
		}
	}
	findings = append(findings, localeFindings(reply.Translations, ReviewFieldTranslation, in.Translations, func(t ReviewTranslation) string { return t.Translation }, false)...)
	return findings, nil
}

// ReviewModelsItem asks the model, in one structured call, to review in (a
// models item: Grammar and Examples are unused) and returns its findings. A
// reply that is not the expected JSON is sent back for correction within the
// purpose's attempt budget; any error means no findings.
func (s *Service) ReviewModelsItem(ctx context.Context, in ReviewInput) (ReviewResult, error) {
	name, err := s.languageName(ctx, in.Language)
	if err != nil {
		return ReviewResult{}, err
	}
	sections, err := s.EffectiveSections(ctx, in.Language)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("load language sections for %s: %w", in.Language, err)
	}
	def := s.purposeDefault(KindReviewModels)
	prompt := s.prompt(ctx, KindReviewModels, in.Language, KindReviewModels)
	msgs := []llm.Message{{Role: "user", Content: renderReviewModelsPrompt(prompt.Prompt, in, name, sections.TranscriptionPrompt)}}
	maxAttempts := effectiveMaxAttempts(def.MaxAttempts)
	attempt := 0
	raw, err := runWithCorrection(ctx, maxAttempts, s.sleeper(),
		func(ctx context.Context, m []llm.Message) (string, error) {
			attempt++
			return s.callLLM(ctx, KindReviewModels, prompt, def, m, ReviewModelsSchema, attempt, maxAttempts)
		},
		func(raw string) error {
			_, err := parseReviewModelsReply(raw, in, sections)
			if err != nil {
				// Telemetry only: the error text may embed the model's reply.
				slog.Info("llm reply rejected", "kind", KindReviewModels, "outcome", "invalid_reply", "attempt", attempt, "max_attempts", maxAttempts)
			}
			return err
		},
		msgs)
	if err != nil {
		return ReviewResult{}, err
	}
	findings, err := parseReviewModelsReply(raw, in, sections)
	if err != nil {
		return ReviewResult{}, err
	}
	return ReviewResult{Findings: findings, Response: json.RawMessage(strings.TrimSpace(raw))}, nil
}
