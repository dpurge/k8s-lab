package ai

import (
	"strings"
	"testing"

	"phraseforge/internal/config"
)

func TestRenderItemPromptFillsAllPlaceholders(t *testing.T) {
	got := renderItemPrompt(config.DefaultVocabularyItemPrompt, ItemPromptVars{
		SourceLanguage: "Mandarin Chinese", TargetLanguage: "Polish", Phrase: "名字",
		GrammarPrompt: "N = noun", TranscriptionPrompt: "Transcribe using pinyin.",
	})
	if strings.Contains(got, "{{") {
		t.Errorf("unrendered placeholder left in:\n%s", got)
	}
	for _, want := range []string{"N = noun\n\nTranscribe using pinyin.\n", "Translate from Mandarin Chinese to Polish: 名字\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered prompt missing %q:\n%s", want, got)
		}
	}
}

// TestRenderItemPromptBlankSections matches the eval's behavior for a
// language without a transcription file: the placeholder becomes "".
func TestRenderItemPromptBlankSections(t *testing.T) {
	got := renderItemPrompt("A\n\n{{grammarPrompt}}\n\n{{transcriptionPrompt}}\n\nB {{phrase}}", ItemPromptVars{Phrase: "der Koffer", GrammarPrompt: "N = noun"})
	want := "A\n\nN = noun\n\n\n\nB der Koffer"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderItemPromptIsSinglePass(t *testing.T) {
	got := renderItemPrompt("{{phrase}} {{unknown}}", ItemPromptVars{Phrase: "x {{grammarPrompt}}", GrammarPrompt: "EXPANDED"})
	if got != "x {{grammarPrompt}} {{unknown}}" {
		t.Errorf("got %q, want the phrase's own braces and unknown placeholders left untouched", got)
	}
}
