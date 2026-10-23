package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// excerptReach is how many bytes of the reply are quoted on each side of the
// place where the JSON broke.
const excerptReach = 40

// maxPathBytes keeps the refusal short: it is also stored in the task's
// failure message.
const maxPathBytes = 160

// The words in invalidJSONMessage that fence the quoted excerpt, so that
// RedactExcerpt can find it.
const (
	excerptLead = "Around it: "
	excerptTail = ". Open at that point:"
	// excerptOmitted stands in for the quoted text.
	excerptOmitted = "[excerpt omitted]"
)

// RedactExcerpt returns message without the quoted piece of the model's reply
// that invalidJSONMessage puts in it, which can hold anything the user or the
// model wrote. The offset, the open containers and the instruction stay. A
// message without an excerpt is returned unchanged. The excerpt is model text
// and may itself contain the closing words, so the last of them ends it: that
// can only remove more, never less.
func RedactExcerpt(message string) string {
	start := strings.Index(message, excerptLead)
	if start < 0 {
		return message
	}
	start += len(excerptLead)
	end := strings.LastIndex(message, excerptTail)
	if end < start {
		return message[:start] + excerptOmitted
	}
	return message[:start] + excerptOmitted + message[end:]
}

// invalidJSONMessage explains, in words fit to show the model, why text is not
// a JSON object: where the syntax broke, what surrounds that place and which
// containers were open there. Models lose count of closing brackets, and the
// bare library error ("invalid character '}' after array element") does not
// say where. text is what was actually parsed, so the offsets refer to it.
func invalidJSONMessage(text string, err error) string {
	pos, known := errorPosition(text, err)
	var ctx string
	switch {
	case known:
		ctx = fmt.Sprintf(" (at byte %d). Around it: %s.", pos, excerptAround(text, pos))
	case json.Valid([]byte(text)):
		// Valid JSON that is not an object: the original error says so.
		return fmt.Sprintf("reply is not a valid JSON object: %v", err)
	default:
		pos = len(text)
		ctx = " (position unknown)."
	}
	return fmt.Sprintf("reply is not a valid JSON object: %v%s Open at that point: %s. Send the whole reply again as ONE JSON object with nothing before or after it, and count the closing brackets against the example in the guide.",
		err, ctx, openContainers(text[:pos]))
}

// errorPosition returns the 0-based byte index of the byte that broke the JSON.
// encoding/json reports the number of bytes read when it gave up, so the
// offending byte is the one before that. Text after a complete value has no
// offset in the library's error, so the scanner finds it.
func errorPosition(text string, err error) (int, bool) {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) && syntax.Offset >= 1 && syntax.Offset <= int64(len(text)) {
		return int(syntax.Offset) - 1, true
	}
	return trailingContent(text)
}

// excerptAround quotes the text around pos with <<HERE>> in front of the byte
// at pos. Line breaks and tabs become spaces so it stays on one line.
func excerptAround(text string, pos int) string {
	start := max(pos-excerptReach, 0)
	for start < pos && !utf8.RuneStart(text[start]) {
		start++
	}
	end := min(pos+excerptReach, len(text))
	for end < len(text) && end > pos && !utf8.RuneStart(text[end]) {
		end--
	}
	flat := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ")
	return flat.Replace(text[start:pos]) + "<<HERE>>" + flat.Replace(text[pos:end])
}

// jsonFrame is one open object or array while scanning.
type jsonFrame struct {
	isArray   bool
	index     int    // array: the element being read
	key       string // object: the latest key
	expectKey bool   // object: the next string is a key
}

// jsonScan reads text the way a JSON parser does, without judging it: it keeps
// the stack of open containers and notes where the first value ended.
type jsonScan struct {
	stack    []jsonFrame
	valueEnd int // index just after the first complete top-level value, or -1
}

// scanJSON follows text up to limit (len(text) for all of it). Strings are
// skipped whole, with their escapes, so brackets inside them do not count.
func scanJSON(text string, limit int) jsonScan {
	s := jsonScan{valueEnd: -1}
	for i := 0; i < limit && i < len(text); i++ {
		switch c := text[i]; c {
		case '"':
			end := stringEnd(text, i)
			if n := len(s.stack); n > 0 && !s.stack[n-1].isArray && s.stack[n-1].expectKey {
				s.stack[n-1].key = unquote(text[i:min(end, len(text))])
				s.stack[n-1].expectKey = false
			}
			i = end - 1
		case '{':
			s.stack = append(s.stack, jsonFrame{expectKey: true})
		case '[':
			s.stack = append(s.stack, jsonFrame{isArray: true})
		case ',':
			if n := len(s.stack); n > 0 {
				if s.stack[n-1].isArray {
					s.stack[n-1].index++
				} else {
					s.stack[n-1].expectKey = true
				}
			}
		case '}', ']':
			if n := len(s.stack); n > 0 {
				s.stack = s.stack[:n-1]
				if n == 1 && s.valueEnd < 0 {
					s.valueEnd = i + 1
				}
			}
		}
	}
	return s
}

// stringEnd returns the index just after the string that starts at text[start].
func stringEnd(text string, start int) int {
	for i := start + 1; i < len(text); i++ {
		switch text[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return len(text)
}

func unquote(quoted string) string {
	if key, err := strconv.Unquote(quoted); err == nil {
		return key
	}
	return strings.Trim(quoted, `"`)
}

// trailingContent finds the first non-space byte after a complete top-level
// object or array.
func trailingContent(text string) (int, bool) {
	end := scanJSON(text, len(text)).valueEnd
	if end < 0 {
		return 0, false
	}
	for i := end; i < len(text); i++ {
		if !strings.ContainsRune(" \t\r\n", rune(text[i])) {
			return i, true
		}
	}
	return 0, false
}

// openContainers names the objects and arrays still open at the end of
// consumed, outermost first: "reply > action > payload > ui[0] > createSurface".
// Objects are named by the key that holds them, arrays by their key and the
// index of the element being read.
func openContainers(consumed string) string {
	stack := scanJSON(consumed, len(consumed)).stack
	if len(stack) == 0 {
		return "none (the reply object was already closed)"
	}
	path := []string{"reply"}
	for i := 1; i < len(stack); i++ {
		if parent := stack[i-1]; parent.isArray {
			path[len(path)-1] += fmt.Sprintf("[%d]", parent.index)
		} else {
			path = append(path, parent.key)
		}
	}
	if stack[len(stack)-1].isArray {
		path[len(path)-1] += fmt.Sprintf("[%d]", stack[len(stack)-1].index)
	}
	joined := strings.Join(path, " > ")
	for len(joined) > maxPathBytes && len(path) > 2 {
		path = path[1:]
		joined = "... > " + strings.Join(path, " > ")
	}
	return joined
}
