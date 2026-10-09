// Package textnorm is the one place phrase text is normalised before it is
// stored or compared, so that text that looks identical is the same record.
package textnorm

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Phrase trims surrounding whitespace and converts s to Unicode NFC. NFC
// matters most for vocalised text (Arabic, for one): the same word typed with
// its combining marks in a different order is canonically equivalent, renders
// identically, and would otherwise be a second stored phrase.
func Phrase(s string) string {
	return norm.NFC.String(strings.TrimSpace(s))
}
