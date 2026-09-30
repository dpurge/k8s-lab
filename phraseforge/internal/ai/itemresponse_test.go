package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseVocabularyItemResponseValid(t *testing.T) {
	// The exact reply prod gemma4:12b returned for der Koffer (2026-09-30),
	// with grammar corrected to the source phrase's tags.
	raw := `{"phrase": "der Koffer", "translation": "walizka", "grammar": "N m", "transcription": "", "notes": ""}`
	got, err := parseVocabularyItemResponse(raw, "der Koffer")
	if err != nil {
		t.Fatal(err)
	}
	want := VocabularyItemResponse{Phrase: "der Koffer", Grammar: "N m", Translation: "walizka"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseVocabularyItemResponseNullAndMissingOptionals(t *testing.T) {
	got, err := parseVocabularyItemResponse(`{"phrase":"名字","translation":" imię ","transcription":"míngzi","grammar":null}`, "名字")
	if err != nil {
		t.Fatal(err)
	}
	if got.Grammar != "" || got.Notes != "" || got.Transcription != "míngzi" || got.Translation != "imię" {
		t.Errorf("got %+v, want blank grammar/notes, trimmed translation, transcription kept", got)
	}
}

func TestParseVocabularyItemResponseRejects(t *testing.T) {
	cases := []struct {
		name, raw, wantErr string
	}{
		{"not JSON", `walizka`, "invalid JSON"},
		{"code fence", "```json\n{\"phrase\":\"der Koffer\",\"translation\":\"walizka\"}\n```", "invalid JSON"},
		{"missing translation", `{"phrase":"der Koffer"}`, `missing "translation"`},
		{"blank translation", `{"phrase":"der Koffer","translation":"  "}`, `missing "translation"`},
		{"missing phrase", `{"translation":"walizka"}`, `missing "phrase"`},
		{"grammar wrong type", `{"phrase":"der Koffer","translation":"walizka","grammar":7}`, `"grammar" must be a string or null`},
		{"phrase mismatch", `{"phrase":"die Tasche","translation":"torba"}`, "does not match"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseVocabularyItemResponse(c.raw, "der Koffer")
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("err = %v, want it to contain %q", err, c.wantErr)
			}
			if err != nil && !strings.HasPrefix(err.Error(), "vocabulary item response: ") {
				t.Errorf("err = %q, want the vocabulary item response prefix", err)
			}
		})
	}
}

// TestParseItemResponsePhraseNFC: "é" composed (U+00E9) in the stored
// phrase vs decomposed (e + U+0301) in the reply is the same phrase.
func TestParseItemResponsePhraseNFC(t *testing.T) {
	if _, err := parseVocabularyItemResponse(`{"phrase":" café ","translation":"kawiarnia"}`, "café"); err != nil {
		t.Errorf("NFC-equal phrase rejected: %v", err)
	}
}

func TestParseModelsItemResponse(t *testing.T) {
	got, err := parseModelsItemResponse(`{"phrase":"我叫…","transcription":"wǒ jiào…","translation":"Nazywam się…"}`, "我叫…")
	if err != nil {
		t.Fatal(err)
	}
	if got != (ModelsItemResponse{Phrase: "我叫…", Transcription: "wǒ jiào…", Translation: "Nazywam się…"}) {
		t.Errorf("got %+v", got)
	}
	if _, err := parseModelsItemResponse(`{"phrase":"我叫…","transcription":1,"translation":"x"}`, "我叫…"); err == nil || !strings.HasPrefix(err.Error(), "models item response: ") {
		t.Errorf("err = %v, want a models item response type error", err)
	}
}

// TestItemSchemasAreValidJSON: the schemas are sent verbatim as Ollama's
// format; a typo would only surface at call time otherwise.
func TestItemSchemasAreValidJSON(t *testing.T) {
	for name, schema := range map[string]json.RawMessage{"vocabulary": VocabularyItemSchema, "models": ModelsItemSchema} {
		var v struct {
			Required   []string       `json:"required"`
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(schema, &v); err != nil {
			t.Errorf("%s schema: %v", name, err)
			continue
		}
		if len(v.Required) != 2 || v.Properties["translation"] == nil {
			t.Errorf("%s schema: required=%v, want phrase+translation", name, v.Required)
		}
	}
}
