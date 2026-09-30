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
var VocabularyItemSchema = json.RawMessage(`{"type":"object","required":["phrase","translation"],"properties":{"phrase":{"type":"string"},"grammar":{"type":["string","null"]},"transcription":{"type":["string","null"]},"translation":{"type":"string"},"notes":{"type":["string","null"]}}}`)

// ModelsItemSchema is VocabularyItemSchema reduced to what a models item
// stores.
var ModelsItemSchema = json.RawMessage(`{"type":"object","required":["phrase","translation"],"properties":{"phrase":{"type":"string"},"transcription":{"type":["string","null"]},"translation":{"type":"string"}}}`)

// VocabularyItemResponse is a validated vocabulary item reply; optional
// fields are "" when the model returned null, "", or nothing.
type VocabularyItemResponse struct {
	Phrase, Grammar, Transcription, Translation, Notes string
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
	}, nil
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
		return fmt.Errorf("%s: phrase %q does not match the item's phrase %q", what, trimmed(phrase), storedPhrase)
	}
	return nil
}

func trimmed(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}
