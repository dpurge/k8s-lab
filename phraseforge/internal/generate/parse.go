package generate

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"phraseforge/internal/models"
	"phraseforge/internal/textnorm"
	"phraseforge/internal/vocabulary"
)

// Candidate is one vocabulary item generate_vocabulary extracted from the
// text, with the short phrase that shows how the word was used there. Usage
// is empty when the model gave none (or gave one over the examples' length
// limit); it is only a hint for the item job and is never stored itself.
type Candidate struct {
	vocabulary.Item
	Usage string
}

// usageSeparator splits a line into the item part and its usage: a pipe with
// whitespace before it and whitespace (or the end of the line) after it, so a
// pipe inside a word is not a separator.
var usageSeparator = regexp.MustCompile(`\s\|(?:\s+|$)`)

// splitUsage returns line without its usage part, and the cleaned usage. The
// usage is trimmed and NFC-normalised like every other stored text; one longer
// than an example may be (vocabulary.MaxExampleChars characters) is dropped,
// since a long one is a sentence, not the short phrase asked for.
func splitUsage(line string) (head, usage string) {
	loc := usageSeparator.FindStringIndex(line)
	if loc == nil {
		return line, ""
	}
	usage = textnorm.Phrase(line[loc[1]:])
	if utf8.RuneCountInString(usage) > vocabulary.MaxExampleChars {
		usage = ""
	}
	return strings.TrimSpace(line[:loc[0]]), usage
}

// vocabularyLinePattern matches one generate_vocabulary response line in the
// exact format server/markdown.go's vocabularyMarkdown already produces for
// a round-trip vocabulary block: `phrase {grammar} [transcription] =
// translation`, with grammar/transcription/translation each fully optional.
// The trailing `= translation` portion, if present, is matched (so it
// doesn't leak into an earlier group) but deliberately not captured into a
// stored field: vocabulary_items has no translation column of its own
// (translation is per-site-locale, in vocabulary_item_translation), and this
// feature does not backfill it (see the feature spec's Out of Scope). A usage
// (` | usage`) is split off before this pattern is applied (see splitUsage).
var vocabularyLinePattern = regexp.MustCompile(`^([^{\[=]+?)\s*(?:\{([^}]*)\})?\s*(?:\[([^\]]*)\])?\s*(?:=.*)?$`)

// modelsLinePattern mirrors vocabularyLinePattern, without a grammar group.
var modelsLinePattern = regexp.MustCompile(`^([^\[=]+?)\s*(?:\[([^\]]*)\])?\s*(?:=.*)?$`)

// ParseVocabularyLine parses one line of generate_vocabulary's LLM output
// into a candidate. Returns ok=false for a blank line or a line that does not
// match the format (e.g. one that starts with "{"/"["/"="/"|", has its
// grammar in square brackets, or is otherwise malformed). An optional usage
// (` | usage`) is split off first, so what precedes it is parsed exactly as a
// line without one, `= translation` included.
func ParseVocabularyLine(line string) (Candidate, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "|") {
		return Candidate{}, false
	}
	head, usage := splitUsage(line)
	m := vocabularyLinePattern.FindStringSubmatch(head)
	if m == nil {
		return Candidate{}, false
	}
	phrase := strings.TrimSpace(m[1])
	if phrase == "" {
		return Candidate{}, false
	}
	return Candidate{
		Item:  vocabulary.Item{Phrase: phrase, Grammar: strings.TrimSpace(m[2]), Transcription: strings.TrimSpace(m[3])},
		Usage: usage,
	}, true
}

// twoBracketGroups spots the format drift seen in production: a grammar tag
// written in square brackets ahead of the transcription (`phrase [noun]
// [translit]`) instead of in curly braces.
var twoBracketGroups = regexp.MustCompile(`\[[^\]]*\]\s*\[`)

const (
	vocabularyFormat = "phrase {grammar} [transcription] = translation | usage"
	modelsFormat     = "phrase [transcription] = translation"

	// The *FormatHelp texts are what a correction call shows the model; they
	// match the format the generate_* prompts ask for.
	vocabularyFormatHelp = vocabularyFormat + " — {grammar} is a short grammar tag in curly braces, [transcription] is a romanized reading in square brackets, = translation is the item's translation, and | usage (a space, a pipe and a space) is a short phrase from the text in which the word is used; each of the four is optional and must be omitted entirely (not left empty) when not applicable"
	modelsFormatHelp     = modelsFormat + " — [transcription] is a romanized reading in square brackets and = translation is the item's translation; each of the two is optional and must be omitted entirely (not left empty) when not applicable"
)

// rejectionReason explains why line failed to parse as format. hasGrammar is
// true for the vocabulary format, where a grammar tag belongs in curly braces.
func rejectionReason(line, format string, hasGrammar bool) string {
	switch {
	case hasGrammar && line[0] == '|':
		return "the line must start with the phrase, before the | usage"
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
func ParseVocabularyLines(body string) ([]Candidate, []RejectedLine) {
	var out []Candidate
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
