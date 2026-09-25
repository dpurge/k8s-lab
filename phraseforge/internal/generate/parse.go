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
// into a vocabulary item. Returns ok=false for a blank line or a line with
// no recognizable phrase (e.g. one that starts with "{"/"["/"=", or is
// otherwise malformed) — the caller skips these rather than failing the
// whole job (a partial extraction beats a failed job over one bad line).
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

// ParseVocabularyLines parses every line of body, skipping blank/malformed
// lines (see ParseVocabularyLine) rather than failing the whole job.
func ParseVocabularyLines(body string) []vocabulary.Item {
	var out []vocabulary.Item
	for _, line := range strings.Split(body, "\n") {
		if it, ok := ParseVocabularyLine(line); ok {
			out = append(out, it)
		}
	}
	return out
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
func ParseModelsLines(body string) []models.Item {
	var out []models.Item
	for _, line := range strings.Split(body, "\n") {
		if it, ok := ParseModelsLine(line); ok {
			out = append(out, it)
		}
	}
	return out
}
