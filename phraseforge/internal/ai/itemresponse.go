package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// VocabularyItemSchema is prompt-eval's is-json schema for
// vocabulary-translation, sent to Ollama as "format" so the reply is
// schema-shaped JSON. parseVocabularyItemResponse enforces the same rules
// in Go, since format is only a constraint on generation, not a guarantee
// the caller can skip checking (and OpenRouter ignores it).
var VocabularyItemSchema = json.RawMessage(`{"type":"object","required":["phrase","translation"],"properties":{"phrase":{"type":"string"},"grammar":{"type":["string","null"]},"transcription":{"type":["string","null"]},"translation":{"type":"string"},"notes":{"type":["string","null"]},"example":{"type":["string","null"]},"newSense":{"type":["string","null"]},"rewrite":{"type":["object","null"],"required":["phrase"],"properties":{"phrase":{"type":"string"},"grammar":{"type":["string","null"]},"transcription":{"type":["string","null"]}}}}}`)

// ModelsItemSchema is VocabularyItemSchema reduced to what a models item
// stores.
var ModelsItemSchema = json.RawMessage(`{"type":"object","required":["phrase","translation"],"properties":{"phrase":{"type":"string"},"transcription":{"type":["string","null"]},"translation":{"type":"string"}}}`)

// VocabularyItemResponse is a validated vocabulary item reply; optional
// fields are "" when the model returned null, "", or nothing.
type VocabularyItemResponse struct {
	Phrase, Grammar, Transcription, Translation, Notes string
	// Example is a short phrase showing the word in use, "" when the reply had
	// none. It is optional and never makes a reply invalid: anything that is
	// not a string counts as none.
	Example string
	// NewSense is a further meaning of the phrase in the target language, ""
	// when the reply had none; optional like Example, anything that is not a
	// string counts as none.
	NewSense string
	// Rewrite is the phrase the model proposes instead of the stored one, nil
	// when the reply had none. Like Example it is optional and never makes a
	// reply invalid: anything that is not the expected object counts as none.
	Rewrite *PhraseRewrite
}

// PhraseRewrite is a proposed replacement of an item's phrase together with
// its grammar and transcription, which go stale with the phrase. An empty
// Grammar or Transcription means "keep the stored value".
type PhraseRewrite struct {
	Phrase, Grammar, Transcription string
}

// ModelsItemResponse is a validated models item reply.
type ModelsItemResponse struct {
	Phrase, Transcription, Translation string
}

func parseVocabularyItemResponse(raw, storedPhrase string) (VocabularyItemResponse, error) {
	var r struct {
		Phrase        *string `json:"phrase"`
		Grammar       *string `json:"grammar"`
		Transcription *string `json:"transcription"`
		Translation   *string `json:"translation"`
		Notes         *string `json:"notes"`
		Example       any     `json:"example"`
		NewSense      any     `json:"newSense"`
		Rewrite       any     `json:"rewrite"`
	}
	const what = "vocabulary item response"
	if err := decodeItemResponse(what, raw, &r); err != nil {
		return VocabularyItemResponse{}, err
	}
	if err := checkRequiredAndPhrase(what, r.Phrase, r.Translation, storedPhrase); err != nil {
		return VocabularyItemResponse{}, err
	}
	return VocabularyItemResponse{
		Phrase: trimmed(r.Phrase), Grammar: trimmed(r.Grammar), Transcription: trimmed(r.Transcription),
		Translation: trimmed(r.Translation), Notes: trimmed(r.Notes),
		Example:  stringOrEmpty(r.Example),
		NewSense: stringOrEmpty(r.NewSense),
		Rewrite:  rewriteOf(r.Rewrite),
	}, nil
}

// rewriteOf reads the optional "rewrite" reply field: nil unless v is an object
// with a non-blank string phrase whose grammar and transcription, when present,
// are strings or null.
func rewriteOf(v any) *PhraseRewrite {
	fields, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	phrase, ok := fields["phrase"].(string)
	if !ok || strings.TrimSpace(phrase) == "" {
		return nil
	}
	optional := func(name string) (string, bool) {
		switch value := fields[name].(type) {
		case nil:
			return "", true
		case string:
			return strings.TrimSpace(value), true
		default:
			return "", false
		}
	}
	grammar, grammarOK := optional("grammar")
	transcription, transcriptionOK := optional("transcription")
	if !grammarOK || !transcriptionOK {
		return nil
	}
	return &PhraseRewrite{Phrase: strings.TrimSpace(phrase), Grammar: grammar, Transcription: transcription}
}

func parseModelsItemResponse(raw, storedPhrase string) (ModelsItemResponse, error) {
	var r struct {
		Phrase        *string `json:"phrase"`
		Transcription *string `json:"transcription"`
		Translation   *string `json:"translation"`
	}
	const what = "models item response"
	if err := decodeItemResponse(what, raw, &r); err != nil {
		return ModelsItemResponse{}, err
	}
	if err := checkRequiredAndPhrase(what, r.Phrase, r.Translation, storedPhrase); err != nil {
		return ModelsItemResponse{}, err
	}
	return ModelsItemResponse{Phrase: trimmed(r.Phrase), Transcription: trimmed(r.Transcription), Translation: trimmed(r.Translation)}, nil
}

// decodeItemResponse decodes raw into out (a struct of *string fields, so
// null decodes to nil and a non-string value is a type error), naming the
// offending field on a type error.
func decodeItemResponse(what, raw string, out any) error {
	err := json.Unmarshal([]byte(strings.TrimSpace(raw)), out)
	if err == nil {
		return nil
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return fmt.Errorf("%s: %q must be a string or null, got %s", what, typeErr.Field, typeErr.Value)
	}
	return fmt.Errorf("%s: invalid JSON: %w", what, err)
}

// checkRequiredAndPhrase enforces the schema's required fields as
// non-blank and that the echoed phrase is the item's own (compared after
// trimming and NFC normalization, so a composed/decomposed accent
// difference doesn't count as a mismatch).
func checkRequiredAndPhrase(what string, phrase, translation *string, storedPhrase string) error {
	if trimmed(phrase) == "" {
		return fmt.Errorf("%s: missing \"phrase\"", what)
	}
	if trimmed(translation) == "" {
		return fmt.Errorf("%s: missing \"translation\"", what)
	}
	if norm.NFC.String(trimmed(phrase)) != norm.NFC.String(strings.TrimSpace(storedPhrase)) {
		return &phraseMismatchError{what: what, got: trimmed(phrase), stored: storedPhrase}
	}
	return nil
}

// phraseMismatchError is a reply whose phrase is not the item's own. It is
// typed so a vocabulary job that may rewrite the phrase can tell it from the
// other reply errors and take the reply's phrase as a rewrite instead.
type phraseMismatchError struct{ what, got, stored string }

func (e *phraseMismatchError) Error() string {
	return fmt.Sprintf("%s: phrase %q does not match the item's phrase %q", e.what, e.got, e.stored)
}

// stringOrEmpty returns v trimmed if it is a string and "" for anything else
// (null, a number, an array), for a field whose absence is never an error.
func stringOrEmpty(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func trimmed(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}
