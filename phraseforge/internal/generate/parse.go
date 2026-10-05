package generate

import (
	"regexp"
	"strings"

	"phraseforge/internal/models"
	"phraseforge/internal/vocabulary"
)

// vocabularyLinePattern matches one generate_vocabulary response line in the
// exact format server/markdown.go's vocabularyMarkdown already produces for
// a round-trip vocabulary block: `phrase {grammar} [transcription] =
// translation`, with grammar/transcription/translation each fully optional.
// The trailing `= translation` portion, if present, is matched (so it
// doesn't leak into an earlier group) but deliberately not captured into a
// stored field: vocabulary_items has no translation column of its own
// (translation is per-site-locale, in vocabulary_item_translation), and this
// feature does not backfill it (see the feature spec's Out of Scope).
var vocabularyLinePattern = regexp.MustCompile(`^([^{\[=]+?)\s*(?:\{([^}]*)\})?\s*(?:\[([^\]]*)\])?\s*(?:=.*)?$`)

// modelsLinePattern mirrors vocabularyLinePattern, without a grammar group.
var modelsLinePattern = regexp.MustCompile(`^([^\[=]+?)\s*(?:\[([^\]]*)\])?\s*(?:=.*)?$`)

// ParseVocabularyLine parses one line of generate_vocabulary's LLM output
// into a vocabulary item. Returns ok=false for a blank line or a line that
// does not match the format (e.g. one that starts with "{"/"["/"=", has its
// grammar in square brackets, or is otherwise malformed).
func ParseVocabularyLine(line string) (vocabulary.Item, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return vocabulary.Item{}, false
	}
	m := vocabularyLinePattern.FindStringSubmatch(line)
	if m == nil {
		return vocabulary.Item{}, false
	}
	phrase := strings.TrimSpace(m[1])
	if phrase == "" {
		return vocabulary.Item{}, false
	}
	return vocabulary.Item{Phrase: phrase, Grammar: strings.TrimSpace(m[2]), Transcription: strings.TrimSpace(m[3])}, true
}

// twoBracketGroups spots the format drift seen in production: a grammar tag
// written in square brackets ahead of the transcription (`phrase [noun]
// [translit]`) instead of in curly braces.
var twoBracketGroups = regexp.MustCompile(`\[[^\]]*\]\s*\[`)

const (
	vocabularyFormat = "phrase {grammar} [transcription] = translation"
	modelsFormat     = "phrase [transcription] = translation"

	// The *FormatHelp texts are what a correction call shows the model; they
	// match the format the generate_* prompts ask for.
	vocabularyFormatHelp = vocabularyFormat + " — {grammar} is a short grammar tag in curly braces, [transcription] is a romanized reading in square brackets, and = translation is the item's translation; each of the three is optional and must be omitted entirely (not left empty) when not applicable"
	modelsFormatHelp     = modelsFormat + " — [transcription] is a romanized reading in square brackets and = translation is the item's translation; each of the two is optional and must be omitted entirely (not left empty) when not applicable"
)

// rejectionReason explains why line failed to parse as format. hasGrammar is
// true for the vocabulary format, where a grammar tag belongs in curly braces.
func rejectionReason(line, format string, hasGrammar bool) string {
	switch {
	case strings.ContainsAny(line[:1], "{[="):
		return "the line must start with the phrase, before any {, [ or ="
	case twoBracketGroups.MatchString(line) && hasGrammar:
		return "grammar must be in curly braces {...} before the [transcription]; found two [...] groups"
	case twoBracketGroups.MatchString(line):
		return "only one [transcription] group is allowed; found two [...] groups"
	}
	return "does not match the format `" + format + "`"
}

// ParseVocabularyLines parses every line of body. Blank lines are ignored;
// every other line that does not match the format is returned as a
// RejectedLine, so the caller can send it back to the model instead of
// silently losing it.
func ParseVocabularyLines(body string) ([]vocabulary.Item, []RejectedLine) {
	var out []vocabulary.Item
	var rejected []RejectedLine
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if it, ok := ParseVocabularyLine(line); ok {
			out = append(out, it)
		} else {
			rejected = append(rejected, RejectedLine{Line: line, Reason: rejectionReason(line, vocabularyFormat, true)})
		}
	}
	return out, rejected
}

// ParseModelsLine mirrors ParseVocabularyLine for the models line format
// (no grammar group) — see that function's doc comment.
func ParseModelsLine(line string) (models.Item, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return models.Item{}, false
	}
	m := modelsLinePattern.FindStringSubmatch(line)
	if m == nil {
		return models.Item{}, false
	}
	phrase := strings.TrimSpace(m[1])
	if phrase == "" {
		return models.Item{}, false
	}
	return models.Item{Phrase: phrase, Transcription: strings.TrimSpace(m[2])}, true
}

// ParseModelsLines mirrors ParseVocabularyLines — see that function's doc
// comment.
func ParseModelsLines(body string) ([]models.Item, []RejectedLine) {
	var out []models.Item
	var rejected []RejectedLine
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if it, ok := ParseModelsLine(line); ok {
			out = append(out, it)
		} else {
			rejected = append(rejected, RejectedLine{Line: line, Reason: rejectionReason(line, modelsFormat, false)})
		}
	}
	return out, rejected
}
