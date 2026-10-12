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
	DictionaryPrompt    string // blank when the language has no section
	Usage               string // vocabulary items only: how the word was used in the generated text; blank when unknown
	Examples            string // vocabulary items only: the phrase's examples, one per line (see formatExamples); blank when none
	Translation         string // vocabulary items only: the phrase's existing translation in the target language; blank when none
}

// formatExamples lays examples out for {{examples}}: one per line, each
// prefixed "- ". No examples give an empty string.
func formatExamples(examples []string) string {
	lines := make([]string, len(examples))
	for i, e := range examples {
		lines[i] = "- " + e
	}
	return strings.Join(lines, "\n")
}

// renderItemPrompt substitutes the nine placeholders in one pass, so text
// inside a value (a phrase containing "{{grammarPrompt}}", say) is never
// itself expanded; any other {{...}} is left as-is.
func renderItemPrompt(template string, v ItemPromptVars) string {
	return strings.NewReplacer(
		"{{sourceLanguage}}", v.SourceLanguage,
		"{{targetLanguage}}", v.TargetLanguage,
		"{{phrase}}", v.Phrase,
		"{{grammarPrompt}}", v.GrammarPrompt,
		"{{transcriptionPrompt}}", v.TranscriptionPrompt,
		"{{dictionaryPrompt}}", v.DictionaryPrompt,
		"{{usage}}", v.Usage,
		"{{examples}}", v.Examples,
		"{{translation}}", v.Translation,
	).Replace(template)
}

// snippetPlaceholders are the three placeholders a system prompt of any kind may
// carry, filled from the source language's sections.
var snippetPlaceholders = []string{"{{grammarPrompt}}", "{{transcriptionPrompt}}", "{{dictionaryPrompt}}"}

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

// renderSnippets substitutes only the three snippet placeholders, in one pass,
// leaving every other {{...}} as-is: Generate's system prompts carry no item
// vars, so renderItemPrompt would blank {{phrase}} and the like.
func renderSnippets(template string, sections LanguageSections) string {
	return strings.NewReplacer(
		"{{grammarPrompt}}", sections.GrammarPrompt,
		"{{transcriptionPrompt}}", sections.TranscriptionPrompt,
		"{{dictionaryPrompt}}", sections.DictionaryPrompt,
	).Replace(template)
}
