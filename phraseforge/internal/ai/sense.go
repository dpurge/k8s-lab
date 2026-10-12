package ai

import (
	"strings"

	"phraseforge/internal/textnorm"
)

// senses splits a translation text on ";" into its trimmed, NFC-normalised,
// non-blank senses.
func senses(translation string) []string {
	var out []string
	for _, part := range strings.Split(translation, ";") {
		if sense := textnorm.Phrase(part); sense != "" {
			out = append(out, sense)
		}
	}
	return out
}

func hasSense(list []string, sense string) bool {
	for _, have := range list {
		if strings.EqualFold(have, sense) {
			return true
		}
	}
	return false
}

// senseFromTranslation recovers a new sense from a reply that put it into
// "translation" instead of "newSense", which the model does even when told to
// leave the existing translation unchanged. It returns the senses of the reply's
// translation that the existing translation lacks, joined with "; ", and only
// when the reply keeps every existing sense: a reply that dropped or changed one
// re-translated the phrase and is not a reliable source of an extra meaning.
// Empty when there is nothing new or nothing to add to.
func senseFromTranslation(existing, reply string) string {
	have := senses(existing)
	if len(have) == 0 {
		return ""
	}
	got := senses(reply)
	for _, sense := range have {
		if !hasSense(got, sense) {
			return ""
		}
	}
	var added []string
	for _, sense := range got {
		if !hasSense(have, sense) && !hasSense(added, sense) {
			added = append(added, sense)
		}
	}
	return strings.Join(added, "; ")
}
