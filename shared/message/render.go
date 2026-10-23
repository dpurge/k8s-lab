package message

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	maxRenderLength    = 300
	correlationIDChars = 8
)

// Render describes an envelope in one line of at most 300 characters, for logs
// and admin pages. It names the payload's top-level keys and never its values:
// payloads can hold what a user typed.
func Render(env Envelope) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s->%s [%s]", env.Type, env.Operation, env.From, env.To, firstRunes(env.CorrelationID, correlationIDChars))
	if env.Summary != "" {
		b.WriteString(" summary: " + strconv.Quote(env.Summary))
	}
	if env.Error != nil {
		fmt.Fprintf(&b, " error %s: %s", env.Error.Code, env.Error.Message)
		if env.Error.Retryable {
			b.WriteString(" (retryable)")
		}
	}
	if keys := payloadKeys(env.Payload); len(keys) > 0 {
		b.WriteString(" payload keys: " + strings.Join(keys, ", "))
	}
	return truncateRunes(singleLine(b.String()), maxRenderLength)
}

// payloadKeys returns the sorted top-level keys of a payload object, and nothing
// for anything else.
func payloadKeys(payload json.RawMessage) []string {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func firstRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// truncateRunes cuts s to n characters, the last one being an ellipsis when
// something was cut.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// singleLine turns control characters (newlines among them) into spaces.
func singleLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
