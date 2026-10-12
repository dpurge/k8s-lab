package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"k8s-lab/shared/llm"
)

// KindCheckVocabulary is the purpose of the step-2 call that decides which
// known phrases of a generated list need a new example.
const KindCheckVocabulary = "check_vocabulary"

// CheckVocabularySchema is the reply schema of the check call, sent as the
// call's format; parseCheckVerdicts enforces its outer shape in Go.
var CheckVocabularySchema = json.RawMessage(`{"type":"object","required":["results"],"properties":{"results":{"type":"array","items":{"type":"object","required":["n","needsExample","needsSense"],"properties":{"n":{"type":"integer"},"needsExample":{"type":"boolean"},"needsSense":{"type":"boolean"}}}}}}`)

// CheckVerdict is what the check call decided for one known phrase: its usage
// is worth one more example, and/or shows a meaning that none of its existing
// translations covers.
type CheckVerdict struct {
	NeedsExample, NeedsSense bool
}

// CheckItem is one known phrase the check call judges: its phrase, the usage
// it came with, and what the dictionary already has for it.
type CheckItem struct {
	Phrase       string
	Usage        string
	Examples     []string
	Translations map[string]string // non-blank translations by site locale code
}

// formatCheckItems lays items out for {{candidates}}: numbered from 1, each
// with its usage, examples and translations (by locale code, in code order),
// "none" where there are none. Values are joined with " | " since a
// translation uses "; " between its senses.
func formatCheckItems(items []CheckItem) string {
	var b strings.Builder
	for i, it := range items {
		examples := "none"
		if len(it.Examples) > 0 {
			examples = strings.Join(it.Examples, " | ")
		}
		translations := "none"
		if len(it.Translations) > 0 {
			locales := make([]string, 0, len(it.Translations))
			for locale := range it.Translations {
				locales = append(locales, locale)
			}
			slices.Sort(locales)
			parts := make([]string, len(locales))
			for j, locale := range locales {
				parts[j] = locale + ": " + it.Translations[locale]
			}
			translations = strings.Join(parts, " | ")
		}
		fmt.Fprintf(&b, "%d. %s\n   usage: %s\n   examples: %s\n   translations: %s\n", i+1, it.Phrase, it.Usage, examples, translations)
	}
	return b.String()
}

// renderCheckPrompt substitutes the two placeholders in one pass, so text
// inside a phrase or usage is never itself expanded.
func renderCheckPrompt(template, sourceLanguage, candidates string) string {
	return strings.NewReplacer("{{sourceLanguage}}", sourceLanguage, "{{candidates}}", candidates).Replace(template)
}

// parseCheckVerdicts reads the reply of a check call for count items and
// returns the verdicts, by 0-based index, of the items the model marked
// needsExample or needsSense. The reply must be a JSON object with a results
// array (anything else is an error, so the call is corrected); inside it an
// entry that is not an object with an integer n in 1..count and a boolean
// needsExample is ignored, which means no job for it; a needsSense that is
// absent or not a boolean counts as false.
func parseCheckVerdicts(raw string, count int) (map[int]CheckVerdict, error) {
	var reply struct {
		Results *[]json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &reply); err != nil {
		return nil, fmt.Errorf("the reply is not a JSON object: %w", err)
	}
	if reply.Results == nil {
		return nil, fmt.Errorf(`the reply has no "results" array`)
	}
	verdicts := map[int]CheckVerdict{}
	for _, entry := range *reply.Results {
		var e struct {
			N            *int  `json:"n"`
			NeedsExample *bool `json:"needsExample"`
			NeedsSense   any   `json:"needsSense"`
		}
		if json.Unmarshal(entry, &e) != nil || e.N == nil || e.NeedsExample == nil || *e.N < 1 || *e.N > count {
			continue
		}
		sense, _ := e.NeedsSense.(bool)
		if *e.NeedsExample || sense {
			verdicts[*e.N-1] = CheckVerdict{NeedsExample: *e.NeedsExample, NeedsSense: sense}
		}
	}
	return verdicts, nil
}

// CheckVocabulary asks the model, in one structured call, which of items need a
// new example or a new sense, and returns the verdicts of those that do by
// 0-based index. A reply that is not the expected JSON is sent back for
// correction within the purpose's attempt budget; any error means no verdicts
// at all.
func (s *Service) CheckVocabulary(ctx context.Context, language string, items []CheckItem) (map[int]CheckVerdict, error) {
	if len(items) == 0 {
		return nil, nil
	}
	name, err := s.languageName(ctx, language)
	if err != nil {
		return nil, err
	}
	def := s.purposeDefault(KindCheckVocabulary)
	// The target language is the kind's own name, like every purpose that has none.
	prompt := s.prompt(ctx, KindCheckVocabulary, language, KindCheckVocabulary)
	msgs := []llm.Message{{Role: "user", Content: renderCheckPrompt(prompt.Prompt, name, formatCheckItems(items))}}
	maxAttempts := effectiveMaxAttempts(def.MaxAttempts)
	attempt := 0
	raw, err := runWithCorrection(ctx, maxAttempts, s.sleeper(),
		func(ctx context.Context, m []llm.Message) (string, error) {
			attempt++
			return s.callLLM(ctx, KindCheckVocabulary, prompt, def, m, CheckVocabularySchema, attempt, maxAttempts)
		},
		func(raw string) error {
			_, err := parseCheckVerdicts(raw, len(items))
			if err != nil {
				// Telemetry only — the error text may embed the model's reply.
				slog.Info("llm reply rejected", "kind", KindCheckVocabulary, "outcome", "invalid_reply", "attempt", attempt, "max_attempts", maxAttempts)
			}
			return err
		},
		msgs)
	if err != nil {
		return nil, err
	}
	return parseCheckVerdicts(raw, len(items))
}
