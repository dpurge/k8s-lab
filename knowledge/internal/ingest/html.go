package ingest

import (
	"html"
	"regexp"
	"strings"
)

// scriptOrStyleBlock matches a <script>...</script> or <style>...</style>
// element, tag and content together, case-insensitively, so neither the
// markup nor any embedded JS/CSS (which may itself contain "<" or ">")
// leaks into the stripped output. Go's RE2 engine has no backreferences, so
// the two element types are spelled out separately rather than matched with
// one captured tag name.
var scriptOrStyleBlock = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>|<style\b[^>]*>.*?</style\s*>`)

// htmlComment matches an HTML comment, including its delimiters.
var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// blockClosingTag matches the closing tags and self-closing breaks that mark
// the end of a block-level element; these become a newline rather than
// being dropped, so paragraph/line structure survives as plain text.
var blockClosingTag = regexp.MustCompile(`(?i)</\s*(p|div|h[1-6]|li)\s*>|<\s*br\s*/?\s*>`)

// anyTag matches any remaining HTML tag once script/style/comment blocks and
// block-closing tags have already been handled.
var anyTag = regexp.MustCompile(`<[^>]*>`)

// runOfSpaces matches two or more consecutive spaces or tabs.
var runOfSpaces = regexp.MustCompile(`[ \t]{2,}`)

// runOfNewlines matches three or more consecutive newlines.
var runOfNewlines = regexp.MustCompile(`\n{3,}`)

// stripHTML converts an HTML string into plain text using a hand-rolled tag
// scanner, not a real HTML parser: <script>/<style> blocks and comments are
// removed entirely, block-level closing tags become newlines, every other
// tag is dropped, entities are unescaped, and excess whitespace is
// collapsed. It is intentionally imperfect on pathological markup — every
// ingested document is human-reviewed before becoming a real knowledge
// item, so a best-effort plain-text extraction is an accepted trade-off for
// avoiding a full HTML-parsing dependency.
func stripHTML(source string) string {
	text := scriptOrStyleBlock.ReplaceAllString(source, "")
	text = htmlComment.ReplaceAllString(text, "")
	text = blockClosingTag.ReplaceAllString(text, "\n")
	text = anyTag.ReplaceAllString(text, "")
	text = html.UnescapeString(text)
	text = runOfSpaces.ReplaceAllString(text, " ")
	text = runOfNewlines.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}
