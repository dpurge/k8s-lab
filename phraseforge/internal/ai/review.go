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
	"phraseforge/internal/textnorm"
)

// KindReviewVocabulary is the purpose of the call that reviews one vocabulary
// item (its phrase, grammar, transcription, translations, notes and examples)
// and proposes corrections. It is meant for a more capable model than the one
// that generated the item, so its provider and model default to the
// vocabularyItem ones only until the admin sets its own (see purposeDefault).
const KindReviewVocabulary = "review_vocabulary"

// reviewVocabularyDefault is the reviewVocabulary purpose's default with a blank
// provider or model taken from vocabularyItem, so a review works out of the
// box with the model that generates items and a stronger one is a settings
// change (config.yaml or an admin LLM row) that touches no other purpose.
func (s *Service) reviewVocabularyDefault() config.PurposeConfig {
	def := s.cfg.ReviewVocabulary
	if strings.TrimSpace(def.Provider) == "" {
		def.Provider = s.cfg.VocabularyItem.Provider
		def.Model = s.cfg.VocabularyItem.Model
	} else if strings.TrimSpace(def.Model) == "" {
		def.Model = s.cfg.VocabularyItem.Model
	}
	return def
}

// ReviewVocabularySchema is the reply schema of the review call, sent as the
// call's format. Every check is {ok, suggestion, reason}: ok is false when
// something is wrong, suggestion is the fix ("" when there is none), reason is
// one short sentence. translations and notes have one entry per locale,
// examples one per numbered example.
var ReviewVocabularySchema = json.RawMessage(`{"type":"object","required":["phrase","grammar","transcription","translations","notes","examples"],"properties":{"phrase":{"type":"object","required":["ok","suggestion","reason"],"properties":{"ok":{"type":"boolean"},"suggestion":{"type":"string"},"reason":{"type":"string"}}},"grammar":{"type":"object","required":["ok","suggestion","reason"],"properties":{"ok":{"type":"boolean"},"suggestion":{"type":"string"},"reason":{"type":"string"}}},"transcription":{"type":"object","required":["ok","suggestion","reason"],"properties":{"ok":{"type":"boolean"},"suggestion":{"type":"string"},"reason":{"type":"string"}}},"translations":{"type":"array","items":{"type":"object","required":["locale","ok","suggestion","reason"],"properties":{"locale":{"type":"string"},"ok":{"type":"boolean"},"suggestion":{"type":"string"},"reason":{"type":"string"}}}},"notes":{"type":"array","items":{"type":"object","required":["locale","ok","suggestion","reason"],"properties":{"locale":{"type":"string"},"ok":{"type":"boolean"},"suggestion":{"type":"string"},"reason":{"type":"string"}}}},"examples":{"type":"array","items":{"type":"object","required":["index","action","suggestion","reason"],"properties":{"index":{"type":"integer"},"action":{"type":"string","enum":["keep","remove","shorten"]},"suggestion":{"type":"string"},"reason":{"type":"string"}}}}}}`)

// Review finding fields and actions, as stored and shown by the review dialog.
const (
	ReviewFieldPhrase        = "phrase"
	ReviewFieldGrammar       = "grammar"
	ReviewFieldTranscription = "transcription"
	ReviewFieldTranslation   = "translation"
	ReviewFieldNotes         = "notes"
	ReviewFieldExample       = "example"

	// ReviewReplace swaps the current value for Suggestion; ReviewRemove
	// deletes it (notes, an example); ReviewComment is a remark with no fix.
	ReviewReplace = "replace"
	ReviewRemove  = "remove"
	ReviewComment = "comment"

	// ReviewOpen is the status of a stored finding nobody has acted on.
	ReviewOpen = "open"
)

// maxReviewReasonChars bounds a stored reason: it is shown in a dialog and
// the reply is untrusted text.
const maxReviewReasonChars = 300

// ReviewTranslation is one locale's translation and notes of the reviewed item.
type ReviewTranslation struct {
	Locale      string
	Translation string
	Notes       string
}

// ReviewInput is what one review call is given: an item as it is stored.
type ReviewInput struct {
	Language      string // source language code of the item's list
	Phrase        string
	Grammar       string
	Transcription string
	Examples      []string
	Translations  []ReviewTranslation // the locales that have a translation or notes
}

// ReviewFinding is one proposal of a review: change or remove one stored value
// of the item, or a remark. A phrase finding is one unit with its grammar and
// transcription (Grammar and Transcription are the values the new phrase gets;
// a blank one keeps the stored value).
type ReviewFinding struct {
	Field         string `json:"field"`
	Action        string `json:"action"`
	Locale        string `json:"locale,omitempty"`  // translation and notes
	Index         int    `json:"index,omitempty"`   // example, 1-based
	Current       string `json:"current,omitempty"` // the value the finding is about
	Suggestion    string `json:"suggestion,omitempty"`
	Grammar       string `json:"grammar,omitempty"`
	Transcription string `json:"transcription,omitempty"`
	Reason        string `json:"reason,omitempty"`
	// Status is ReviewOpen when the review stores the finding; the review
	// dialog moves it on.
	Status string `json:"status,omitempty"`
}

// ReviewResult is a review call's findings and the model's reply as it came.
type ReviewResult struct {
	Findings []ReviewFinding
	Response json.RawMessage
}

// reviewPromptVars fills the review prompt template.
type reviewPromptVars struct {
	SourceLanguage, Phrase, Grammar, Transcription, Examples, Translations string
	DictionaryPrompt, GrammarPrompt, TranscriptionPrompt                   string
}

// renderReviewPrompt substitutes the placeholders in one pass, so text inside
// a value (a phrase or a note) is never itself expanded.
func renderReviewPrompt(template string, v reviewPromptVars) string {
	return strings.NewReplacer(
		"{{sourceLanguage}}", v.SourceLanguage,
		"{{phrase}}", v.Phrase,
		"{{grammar}}", v.Grammar,
		"{{transcription}}", v.Transcription,
		"{{examples}}", v.Examples,
		"{{translations}}", v.Translations,
		"{{dictionaryPrompt}}", v.DictionaryPrompt,
		"{{grammarPrompt}}", v.GrammarPrompt,
		"{{transcriptionPrompt}}", v.TranscriptionPrompt,
	).Replace(template)
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}

// formatReviewExamples numbers the examples from 1, the numbers the reply's
// examples entries refer to.
func formatReviewExamples(examples []string) string {
	lines := make([]string, len(examples))
	for i, e := range examples {
		lines[i] = fmt.Sprintf("%d. %s", i+1, e)
	}
	return orNone(strings.Join(lines, "\n"))
}

// formatReviewTranslations lays the locales out one per line as
// "- locale: translation", with a "notes:" line below when there are notes.
// English comes first, since it is the context for judging the others, then
// the rest in locale-code order.
func formatReviewTranslations(translations []ReviewTranslation) string {
	sorted := slices.Clone(translations)
	slices.SortStableFunc(sorted, func(a, b ReviewTranslation) int {
		switch {
		case a.Locale == b.Locale:
			return 0
		case a.Locale == "en":
			return -1
		case b.Locale == "en":
			return 1
		}
		return strings.Compare(a.Locale, b.Locale)
	})
	var b strings.Builder
	for _, t := range sorted {
		fmt.Fprintf(&b, "- %s: %s\n", t.Locale, orNone(t.Translation))
		if strings.TrimSpace(t.Notes) != "" {
			fmt.Fprintf(&b, "  notes: %s\n", t.Notes)
		}
	}
	return orNone(strings.TrimRight(b.String(), "\n"))
}

// reviewEntry is one {ok, suggestion, reason} check of the reply; every part
// is optional here, a malformed entry is dropped rather than corrected.
type reviewEntry struct {
	Locale     string  `json:"locale"`
	Index      *int    `json:"index"`
	Action     string  `json:"action"`
	OK         *bool   `json:"ok"`
	Suggestion *string `json:"suggestion"`
	Reason     *string `json:"reason"`
}

func decodeReviewEntry(raw json.RawMessage) (reviewEntry, bool) {
	var e reviewEntry
	if len(raw) == 0 || json.Unmarshal(raw, &e) != nil {
		return reviewEntry{}, false
	}
	return e, true
}

func (e reviewEntry) suggestion() string {
	if e.Suggestion == nil {
		return ""
	}
	return textnorm.Phrase(*e.Suggestion)
}

func (e reviewEntry) reason() string {
	if e.Reason == nil {
		return ""
	}
	reason := textnorm.Phrase(*e.Reason)
	if utf8.RuneCountInString(reason) > maxReviewReasonChars {
		reason = string([]rune(reason)[:maxReviewReasonChars])
	}
	return reason
}

// isNotOK is true when the model said, explicitly, that the value is wrong.
func (e reviewEntry) isNotOK() bool { return e.OK != nil && !*e.OK }

// reviewField turns one check of a stored value into a finding. A suggestion
// that differs from current is a replacement; otherwise a check that is
// explicitly not ok is a removal (when allowRemove: notes) or a comment with
// its reason; otherwise there is nothing to report.
func reviewField(field string, e reviewEntry, current string, allowRemove bool) (ReviewFinding, bool) {
	f := ReviewFinding{Field: field, Current: current, Reason: e.reason()}
	switch suggestion := e.suggestion(); {
	case suggestion != "" && suggestion != textnorm.Phrase(current):
		f.Action, f.Suggestion = ReviewReplace, suggestion
	case e.isNotOK() && allowRemove && strings.TrimSpace(current) != "":
		f.Action = ReviewRemove
	case e.isNotOK() && f.Reason != "":
		f.Action = ReviewComment
	default:
		return ReviewFinding{}, false
	}
	return f, true
}

func hasSnippet(s string) bool { return strings.TrimSpace(s) != "" }

// parseReviewReply reads the reply of a review call for in and returns its
// findings, in the order phrase, grammar, transcription, translations, notes,
// examples. The reply must be a JSON object with a "phrase" entry, and a
// phrase suggestion must come with a grammar and a transcription suggestion
// for every one the item has and the language keeps (the three change as one
// unit, so a phrase proposal without them could not be applied): anything else
// is an error and the call is corrected. Everything else is lenient: an entry
// that is malformed, names a locale or an example the item does not have, or
// is about a field the language has no snippet for (phrase without a
// dictionary-form snippet, grammar without a grammar snippet, transcription
// without a transcription one) is ignored.
func parseReviewReply(raw string, in ReviewInput, sections LanguageSections) ([]ReviewFinding, error) {
	var reply struct {
		Phrase        json.RawMessage   `json:"phrase"`
		Grammar       json.RawMessage   `json:"grammar"`
		Transcription json.RawMessage   `json:"transcription"`
		Translations  []json.RawMessage `json:"translations"`
		Notes         []json.RawMessage `json:"notes"`
		Examples      []json.RawMessage `json:"examples"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &reply); err != nil {
		return nil, fmt.Errorf("the reply is not a JSON object: %w", err)
	}
	if len(reply.Phrase) == 0 || string(reply.Phrase) == "null" {
		return nil, fmt.Errorf(`the reply has no "phrase" entry`)
	}
	var findings []ReviewFinding

	phrase, _ := decodeReviewEntry(reply.Phrase)
	grammar, _ := decodeReviewEntry(reply.Grammar)
	transcription, _ := decodeReviewEntry(reply.Transcription)
	grammarKept, transcriptionKept := hasSnippet(sections.GrammarPrompt), hasSnippet(sections.TranscriptionPrompt)

	rewritten := false
	if hasSnippet(sections.DictionaryPrompt) {
		if f, ok := reviewField(ReviewFieldPhrase, phrase, in.Phrase, false); ok {
			if f.Action == ReviewReplace {
				rewritten = true
				f.Grammar, f.Transcription = grammar.suggestion(), transcription.suggestion()
				if grammarKept && strings.TrimSpace(in.Grammar) != "" && f.Grammar == "" {
					return nil, fmt.Errorf(`the "phrase" suggestion needs a "grammar" suggestion for the new phrase (repeat the current grammar if it stays valid)`)
				}
				if transcriptionKept && strings.TrimSpace(in.Transcription) != "" && f.Transcription == "" {
					return nil, fmt.Errorf(`the "phrase" suggestion needs a "transcription" suggestion for the new phrase (repeat the current transcription if it stays valid)`)
				}
			}
			findings = append(findings, f)
		}
	}
	if !rewritten {
		if grammarKept {
			if f, ok := reviewField(ReviewFieldGrammar, grammar, in.Grammar, false); ok {
				findings = append(findings, f)
			}
		}
		if transcriptionKept {
			if f, ok := reviewField(ReviewFieldTranscription, transcription, in.Transcription, false); ok {
				findings = append(findings, f)
			}
		}
	}

	findings = append(findings, localeFindings(reply.Translations, ReviewFieldTranslation, in.Translations, func(t ReviewTranslation) string { return t.Translation }, false)...)
	findings = append(findings, localeFindings(reply.Notes, ReviewFieldNotes, in.Translations, func(t ReviewTranslation) string { return t.Notes }, true)...)
	findings = append(findings, exampleFindings(reply.Examples, in.Examples)...)
	return findings, nil
}

// localeFindings reads the per-locale entries of field (translations or
// notes); value picks the stored text from the item's locale. The first entry
// of a locale counts; a locale the item has no text for is ignored.
func localeFindings(entries []json.RawMessage, field string, locales []ReviewTranslation, value func(ReviewTranslation) string, allowRemove bool) []ReviewFinding {
	var out []ReviewFinding
	seen := map[string]bool{}
	for _, raw := range entries {
		e, ok := decodeReviewEntry(raw)
		if !ok || seen[e.Locale] {
			continue
		}
		i := slices.IndexFunc(locales, func(t ReviewTranslation) bool { return t.Locale == e.Locale })
		if i < 0 || strings.TrimSpace(value(locales[i])) == "" {
			continue
		}
		seen[e.Locale] = true
		if f, ok := reviewField(field, e, value(locales[i]), allowRemove); ok {
			f.Locale = e.Locale
			out = append(out, f)
		}
	}
	slices.SortStableFunc(out, func(a, b ReviewFinding) int { return localeOrder(locales, a.Locale) - localeOrder(locales, b.Locale) })
	return out
}

func localeOrder(locales []ReviewTranslation, locale string) int {
	return slices.IndexFunc(locales, func(t ReviewTranslation) bool { return t.Locale == locale })
}

// exampleFindings reads the examples entries: "remove" drops the numbered
// example, "shorten" replaces it with the suggestion when that is a usable
// example (see usableExample) that differs from it; "keep", an unknown number
// and a repeated number are ignored.
func exampleFindings(entries []json.RawMessage, examples []string) []ReviewFinding {
	var out []ReviewFinding
	seen := map[int]bool{}
	for _, raw := range entries {
		e, ok := decodeReviewEntry(raw)
		if !ok || e.Index == nil || *e.Index < 1 || *e.Index > len(examples) || seen[*e.Index] {
			continue
		}
		current := examples[*e.Index-1]
		f := ReviewFinding{Field: ReviewFieldExample, Index: *e.Index, Current: current, Reason: e.reason()}
		switch e.Action {
		case "remove":
			f.Action = ReviewRemove
		case "shorten":
			suggestion := usableExample(e.suggestion())
			if suggestion == "" || suggestion == textnorm.Phrase(current) {
				continue
			}
			f.Action, f.Suggestion = ReviewReplace, suggestion
		default:
			continue
		}
		seen[*e.Index] = true
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b ReviewFinding) int { return a.Index - b.Index })
	return out
}

// ReviewItem asks the model, in one structured call, to review in and returns
// its findings. A reply that is not the expected JSON is sent back for
// correction within the purpose's attempt budget; any error means no findings.
func (s *Service) ReviewItem(ctx context.Context, in ReviewInput) (ReviewResult, error) {
	name, err := s.languageName(ctx, in.Language)
	if err != nil {
		return ReviewResult{}, err
	}
	sections, err := s.EffectiveSections(ctx, in.Language)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("load language sections for %s: %w", in.Language, err)
	}
	def := s.purposeDefault(KindReviewVocabulary)
	// The target language is the kind's own name, like every purpose that has none.
	prompt := s.prompt(ctx, KindReviewVocabulary, in.Language, KindReviewVocabulary)
	text := renderReviewPrompt(prompt.Prompt, reviewPromptVars{
		SourceLanguage: name, Phrase: in.Phrase,
		Grammar: orNone(in.Grammar), Transcription: orNone(in.Transcription),
		Examples: formatReviewExamples(in.Examples), Translations: formatReviewTranslations(in.Translations),
		DictionaryPrompt: sections.DictionaryPrompt, GrammarPrompt: sections.GrammarPrompt, TranscriptionPrompt: sections.TranscriptionPrompt,
	})
	msgs := []llm.Message{{Role: "user", Content: text}}
	maxAttempts := effectiveMaxAttempts(def.MaxAttempts)
	attempt := 0
	raw, err := runWithCorrection(ctx, maxAttempts, s.sleeper(),
		func(ctx context.Context, m []llm.Message) (string, error) {
			attempt++
			return s.callLLM(ctx, KindReviewVocabulary, prompt, def, m, ReviewVocabularySchema, attempt, maxAttempts)
		},
		func(raw string) error {
			_, err := parseReviewReply(raw, in, sections)
			if err != nil {
				// Telemetry only — the error text may embed the model's reply.
				slog.Info("llm reply rejected", "kind", KindReviewVocabulary, "outcome", "invalid_reply", "attempt", attempt, "max_attempts", maxAttempts)
			}
			return err
		},
		msgs)
	if err != nil {
		return ReviewResult{}, err
	}
	findings, err := parseReviewReply(raw, in, sections)
	if err != nil {
		return ReviewResult{}, err
	}
	return ReviewResult{Findings: findings, Response: json.RawMessage(strings.TrimSpace(raw))}, nil
}
