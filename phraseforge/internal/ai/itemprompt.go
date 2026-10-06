package ai

import "strings"

// ItemPromptVars fills the vocabulary_item/models_item prompt templates.
// The placeholder names are the prompt-eval setup's camelCase vars, so a
// template tuned there works here unchanged.
type ItemPromptVars struct {
	SourceLanguage      string // e.g. "German", from the language table
	TargetLanguage      string // e.g. "Polish", from i18n.Locale.PromptName
	Phrase              string
	GrammarPrompt       string // blank when the language has no section
	TranscriptionPrompt string // blank when the language has no section
}

// renderItemPrompt substitutes the five placeholders in one pass, so text
// inside a value (a phrase containing "{{grammarPrompt}}", say) is never
// itself expanded; any other {{...}} is left as-is.
func renderItemPrompt(template string, v ItemPromptVars) string {
	return strings.NewReplacer(
		"{{sourceLanguage}}", v.SourceLanguage,
		"{{targetLanguage}}", v.TargetLanguage,
		"{{phrase}}", v.Phrase,
		"{{grammarPrompt}}", v.GrammarPrompt,
		"{{transcriptionPrompt}}", v.TranscriptionPrompt,
	).Replace(template)
}

// snippetPlaceholders are the two placeholders a system prompt of any kind may
// carry, filled from the source language's sections.
var snippetPlaceholders = []string{"{{grammarPrompt}}", "{{transcriptionPrompt}}"}

// usesSnippets reports whether template has a snippet placeholder, so a
// prompt without one costs no sections lookup.
func usesSnippets(template string) bool {
	for _, placeholder := range snippetPlaceholders {
		if strings.Contains(template, placeholder) {
			return true
		}
	}
	return false
}

// renderSnippets substitutes only the two snippet placeholders, in one pass,
// leaving every other {{...}} as-is: Generate's system prompts carry no item
// vars, so renderItemPrompt would blank {{phrase}} and the like.
func renderSnippets(template string, sections LanguageSections) string {
	return strings.NewReplacer(
		"{{grammarPrompt}}", sections.GrammarPrompt,
		"{{transcriptionPrompt}}", sections.TranscriptionPrompt,
	).Replace(template)
}
