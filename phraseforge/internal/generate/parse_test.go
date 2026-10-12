package generate

import (
	"reflect"
	"strings"
	"testing"

	"phraseforge/internal/models"
	"phraseforge/internal/vocabulary"
)

func TestParseVocabularyLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want Candidate
		ok   bool
	}{
		{"phrase only", "cat", Candidate{Item: vocabulary.Item{Phrase: "cat"}}, true},
		{"full form", "cat {noun} [kat] = kot", Candidate{Item: vocabulary.Item{Phrase: "cat", Grammar: "noun", Transcription: "kat"}}, true},
		{"grammar only", "run {verb}", Candidate{Item: vocabulary.Item{Phrase: "run", Grammar: "verb"}}, true},
		{"transcription only", "kot [kot]", Candidate{Item: vocabulary.Item{Phrase: "kot", Transcription: "kot"}}, true},
		{"translation only", "kot = cat", Candidate{Item: vocabulary.Item{Phrase: "kot"}}, true},
		{"transcription and translation, no grammar", "kot [kot] = cat", Candidate{Item: vocabulary.Item{Phrase: "kot", Transcription: "kot"}}, true},
		{"empty line", "", Candidate{}, false},
		{"whitespace only", "   ", Candidate{}, false},
		{"no phrase, grammar only", "{noun} = kot", Candidate{}, false},
		{"no phrase, starts with translation", "= kot", Candidate{}, false},
		{"unclosed brace", "cat {noun", Candidate{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseVocabularyLine(tt.line)
			if ok != tt.ok {
				t.Fatalf("ParseVocabularyLine(%q) ok = %v, want %v", tt.line, ok, tt.ok)
			}
			if ok && !reflect.DeepEqual(got, tt.want) {
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
	want := []Candidate{
		{Item: vocabulary.Item{Phrase: "cat", Grammar: "noun", Transcription: "kat"}},
		{Item: vocabulary.Item{Phrase: "run", Grammar: "verb"}},
		{Item: vocabulary.Item{Phrase: "kot", Transcription: "kot"}},
	}
	if len(got) != len(want) {
		t.Fatalf("ParseVocabularyLines(%q) = %+v, want %+v", body, got, want)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
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
	wantItems := []Candidate{{Item: vocabulary.Item{Phrase: "حَصَلَ", Grammar: "verb", Transcription: "haṣala"}}}
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

func TestParseVocabularyLineUsage(t *testing.T) {
	eighty := strings.Repeat("ك", vocabulary.MaxExampleChars)
	tests := []struct {
		name string
		line string
		want Candidate
		ok   bool
	}{
		{"phrase and usage", "cat | a black cat", Candidate{Item: vocabulary.Item{Phrase: "cat"}, Usage: "a black cat"}, true},
		{"every part", "kot {N} [kot] | mały kot", Candidate{Item: vocabulary.Item{Phrase: "kot", Grammar: "N", Transcription: "kot"}, Usage: "mały kot"}, true},
		{"legacy translation before the usage", "kot {N} [kot] = cat | mały kot", Candidate{Item: vocabulary.Item{Phrase: "kot", Grammar: "N", Transcription: "kot"}, Usage: "mały kot"}, true},
		{"legacy translation alone has no usage", "kot {N} = cat", Candidate{Item: vocabulary.Item{Phrase: "kot", Grammar: "N"}}, true},
		{"diacritized Arabic keeps its bytes", "كَتَبَ {V} [kataba] | كَتَبَ الدَّرْسَ", Candidate{Item: vocabulary.Item{Phrase: "كَتَبَ", Grammar: "V", Transcription: "kataba"}, Usage: "كَتَبَ الدَّرْسَ"}, true},
		{"CJK", "拥有 {V} [yōngyǒu] | 拥有自己的房子", Candidate{Item: vocabulary.Item{Phrase: "拥有", Grammar: "V", Transcription: "yōngyǒu"}, Usage: "拥有自己的房子"}, true},
		{"usage trimmed and NFC-normalised", "cafe | un cafe\u0301 noir  ", Candidate{Item: vocabulary.Item{Phrase: "cafe"}, Usage: "un café noir"}, true},
		{"trailing separator without a usage", "cat {noun} |", Candidate{Item: vocabulary.Item{Phrase: "cat", Grammar: "noun"}}, true},
		{"a pipe inside a word is not a separator", "a|b {N}", Candidate{Item: vocabulary.Item{Phrase: "a|b", Grammar: "N"}}, true},
		{"later pipes belong to the usage", "cat | a | b", Candidate{Item: vocabulary.Item{Phrase: "cat"}, Usage: "a | b"}, true},
		{"usage of exactly the limit is kept", "كتاب | " + eighty, Candidate{Item: vocabulary.Item{Phrase: "كتاب"}, Usage: eighty}, true},
		{"usage over the limit is dropped, the candidate kept", "cat {N} | " + strings.Repeat("x", vocabulary.MaxExampleChars+1), Candidate{Item: vocabulary.Item{Phrase: "cat", Grammar: "N"}}, true},
		{"a line that starts with the usage is rejected", "| a usage", Candidate{}, false},
		{"no phrase before the usage is rejected", "{noun} | a usage", Candidate{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseVocabularyLine(tt.line)
			if ok != tt.ok {
				t.Fatalf("ParseVocabularyLine(%q) ok = %v, want %v", tt.line, ok, tt.ok)
			}
			if ok && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseVocabularyLine(%q) = %+v, want %+v", tt.line, got, tt.want)
			}
		})
	}
}

func TestParseVocabularyLinesGivesALeadingPipeItsOwnReason(t *testing.T) {
	got, rejected := ParseVocabularyLines("cat {noun} | a black cat\n| a lonely usage")
	if len(got) != 1 || got[0].Usage != "a black cat" {
		t.Fatalf("candidates = %+v, want the first line with its usage", got)
	}
	want := []RejectedLine{{Line: "| a lonely usage", Reason: "the line must start with the phrase, before the | usage"}}
	if !reflect.DeepEqual(rejected, want) {
		t.Fatalf("rejected = %+v, want %+v", rejected, want)
	}
}

func TestVocabularyFormatHelpTellsTheModelAboutTheUsagePart(t *testing.T) {
	for _, want := range []string{"| usage", "= translation", "each of the four is optional"} {
		if !strings.Contains(vocabularyFormatHelp, want) {
			t.Errorf("vocabularyFormatHelp = %q, want it to mention %q", vocabularyFormatHelp, want)
		}
	}
}
