package generate

import (
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
	got := ParseVocabularyLines(body)
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
	got := ParseModelsLines(body)
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
