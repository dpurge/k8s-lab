package generate

import (
	"reflect"
	"testing"

	"phraseforge/internal/models"
	"phraseforge/internal/vocabulary"
)

func TestParseVocabularyLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want vocabulary.Item
		ok   bool
	}{
		{"phrase only", "cat", vocabulary.Item{Phrase: "cat"}, true},
		{"full form", "cat {noun} [kat] = kot", vocabulary.Item{Phrase: "cat", Grammar: "noun", Transcription: "kat"}, true},
		{"grammar only", "run {verb}", vocabulary.Item{Phrase: "run", Grammar: "verb"}, true},
		{"transcription only", "kot [kot]", vocabulary.Item{Phrase: "kot", Transcription: "kot"}, true},
		{"translation only", "kot = cat", vocabulary.Item{Phrase: "kot"}, true},
		{"transcription and translation, no grammar", "kot [kot] = cat", vocabulary.Item{Phrase: "kot", Transcription: "kot"}, true},
		{"empty line", "", vocabulary.Item{}, false},
		{"whitespace only", "   ", vocabulary.Item{}, false},
		{"no phrase, grammar only", "{noun} = kot", vocabulary.Item{}, false},
		{"no phrase, starts with translation", "= kot", vocabulary.Item{}, false},
		{"unclosed brace", "cat {noun", vocabulary.Item{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseVocabularyLine(tt.line)
			if ok != tt.ok {
				t.Fatalf("ParseVocabularyLine(%q) ok = %v, want %v", tt.line, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Fatalf("ParseVocabularyLine(%q) = %+v, want %+v", tt.line, got, tt.want)
			}
		})
	}
}

func TestParseVocabularyLines(t *testing.T) {
	body := "cat {noun} [kat] = kot\n\n{noun} = kot\nrun {verb}\n   \nkot [kot]"
	got, rejected := ParseVocabularyLines(body)
	wantRejected := []RejectedLine{{Line: "{noun} = kot", Reason: "the line must start with the phrase, before any {, [ or ="}}
	if !reflect.DeepEqual(rejected, wantRejected) {
		t.Fatalf("ParseVocabularyLines(%q) rejected = %+v, want %+v", body, rejected, wantRejected)
	}
	want := []vocabulary.Item{
		{Phrase: "cat", Grammar: "noun", Transcription: "kat"},
		{Phrase: "run", Grammar: "verb"},
		{Phrase: "kot", Transcription: "kot"},
	}
	if len(got) != len(want) {
		t.Fatalf("ParseVocabularyLines(%q) = %+v, want %+v", body, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParseVocabularyLines(%q)[%d] = %+v, want %+v", body, i, got[i], want[i])
		}
	}
}

func TestParseModelsLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want models.Item
		ok   bool
	}{
		{"phrase only", "I am ___", models.Item{Phrase: "I am ___"}, true},
		{"full form", "I am ___ [ai æm] = jestem ___", models.Item{Phrase: "I am ___", Transcription: "ai æm"}, true},
		{"transcription only", "I am ___ [ai æm]", models.Item{Phrase: "I am ___", Transcription: "ai æm"}, true},
		{"translation only", "I am ___ = jestem ___", models.Item{Phrase: "I am ___"}, true},
		{"empty line", "", models.Item{}, false},
		{"no phrase, transcription only", "[ai æm] = jestem ___", models.Item{}, false},
		{"no phrase, translation only", "= jestem ___", models.Item{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseModelsLine(tt.line)
			if ok != tt.ok {
				t.Fatalf("ParseModelsLine(%q) ok = %v, want %v", tt.line, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Fatalf("ParseModelsLine(%q) = %+v, want %+v", tt.line, got, tt.want)
			}
		})
	}
}

func TestParseModelsLines(t *testing.T) {
	body := "I am ___ [ai æm] = jestem ___\n\n= jestem ___\nYou are ___"
	got, rejected := ParseModelsLines(body)
	wantRejected := []RejectedLine{{Line: "= jestem ___", Reason: "the line must start with the phrase, before any {, [ or ="}}
	if !reflect.DeepEqual(rejected, wantRejected) {
		t.Fatalf("ParseModelsLines(%q) rejected = %+v, want %+v", body, rejected, wantRejected)
	}
	want := []models.Item{
		{Phrase: "I am ___", Transcription: "ai æm"},
		{Phrase: "You are ___"},
	}
	if len(got) != len(want) {
		t.Fatalf("ParseModelsLines(%q) = %+v, want %+v", body, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParseModelsLines(%q)[%d] = %+v, want %+v", body, i, got[i], want[i])
		}
	}
}

// Lines captured from gemma4:12b for a diacritized Arabic text in production:
// grammar in square brackets instead of curly braces. The whole reply was
// silently dropped before rejected lines were reported.
func TestParseVocabularyLinesRejectsBracketGrammar(t *testing.T) {
	body := "وُلِدَ [verb] [wulida] = was born\nدِمَشْقَ [noun] [Dimashqa] = Damascus\nحَصَلَ {verb} [haṣala] = obtained"
	got, rejected := ParseVocabularyLines(body)
	wantItems := []vocabulary.Item{{Phrase: "حَصَلَ", Grammar: "verb", Transcription: "haṣala"}}
	if !reflect.DeepEqual(got, wantItems) {
		t.Fatalf("items = %+v, want %+v", got, wantItems)
	}
	const reason = "grammar must be in curly braces {...} before the [transcription]; found two [...] groups"
	wantRejected := []RejectedLine{
		{Line: "وُلِدَ [verb] [wulida] = was born", Reason: reason},
		{Line: "دِمَشْقَ [noun] [Dimashqa] = Damascus", Reason: reason},
	}
	if !reflect.DeepEqual(rejected, wantRejected) {
		t.Fatalf("rejected = %+v, want %+v", rejected, wantRejected)
	}
}

func TestParseModelsLinesRejectsTwoBracketGroups(t *testing.T) {
	got, rejected := ParseModelsLines("I am ___ [x] [ai æm] = jestem ___\nlost phrase [ok = broken")
	if len(got) != 0 {
		t.Fatalf("items = %+v, want none", got)
	}
	wantRejected := []RejectedLine{
		{Line: "I am ___ [x] [ai æm] = jestem ___", Reason: "only one [transcription] group is allowed; found two [...] groups"},
		{Line: "lost phrase [ok = broken", Reason: "does not match the format `phrase [transcription] = translation`"},
	}
	if !reflect.DeepEqual(rejected, wantRejected) {
		t.Fatalf("rejected = %+v, want %+v", rejected, wantRejected)
	}
}
