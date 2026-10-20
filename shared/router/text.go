package router

import (
	"strings"
	"unicode/utf8"
)

// ellipsis marks text TruncateText cut.
const ellipsis = "…"

// TruncateText returns s with invalid UTF-8 removed and, when it is longer than
// max bytes, cut to at most max bytes on a rune boundary followed by an
// ellipsis (so the result is at most max+len("…") bytes). It is how provider
// error text is bounded: a cut through the middle of a multibyte rune would
// otherwise leave invalid UTF-8 in a log line, an API reply or a database
// column.
func TruncateText(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= max {
		return s
	}
	if max < 0 {
		max = 0
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}
