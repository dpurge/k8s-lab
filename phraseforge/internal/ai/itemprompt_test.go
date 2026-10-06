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

func TestRenderSnippetsFillsOnlyTheTwoSnippetPlaceholders(t *testing.T) {
	sections := LanguageSections{GrammarPrompt: "G", TranscriptionPrompt: "T"}
	got := renderSnippets("a {{grammarPrompt}} b {{transcriptionPrompt}} c {{phrase}} {{unknown}}", sections)
	if want := "a G b T c {{phrase}} {{unknown}}"; got != want {
		t.Errorf("renderSnippets = %q, want %q", got, want)
	}
}

func TestRenderSnippetsBlankSectionRendersEmpty(t *testing.T) {
	got := renderSnippets("x\n\n{{grammarPrompt}}\n\ny", LanguageSections{})
	if want := "x\n\n\n\ny"; got != want {
		t.Errorf("renderSnippets = %q, want %q", got, want)
	}
}

func TestRenderSnippetsDoesNotExpandPlaceholdersInsideASnippet(t *testing.T) {
	sections := LanguageSections{GrammarPrompt: "{{transcriptionPrompt}}", TranscriptionPrompt: "T"}
	if got, want := renderSnippets("{{grammarPrompt}}", sections), "{{transcriptionPrompt}}"; got != want {
		t.Errorf("renderSnippets = %q, want %q (one pass)", got, want)
	}
}

func TestUsesSnippets(t *testing.T) {
	cases := map[string]bool{
		"plain prompt":                    false,
		"has {{phrase}} only":             false,
		"has {{grammarPrompt}}":           true,
		"has {{transcriptionPrompt}} too": true,
	}
	for template, want := range cases {
		if got := usesSnippets(template); got != want {
			t.Errorf("usesSnippets(%q) = %v, want %v", template, got, want)
		}
	}
}

// The three generate prompts that use transcription or grammar tags carry the
// placeholders, so the language's snippets reach them; the others do not.
func TestDefaultGeneratePromptsCarrySnippetPlaceholders(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		want   []string
	}{
		{"transcription", config.DefaultTranscriptionPrompt, []string{"{{transcriptionPrompt}}"}},
		{"generate_vocabulary", config.DefaultGenerateVocabularyPrompt, []string{"{{grammarPrompt}}", "{{transcriptionPrompt}}"}},
		{"generate_models", config.DefaultGenerateModelsPrompt, []string{"{{transcriptionPrompt}}"}},
		{"translation", config.DefaultTranslationPrompt, nil},
		{"title", config.DefaultTitlePrompt, nil},
	}
	for _, c := range cases {
		if got := usesSnippets(c.prompt); got != (len(c.want) > 0) {
			t.Errorf("%s: usesSnippets = %v, want %v", c.name, got, len(c.want) > 0)
		}
		for _, placeholder := range c.want {
			if !strings.Contains(c.prompt, placeholder) {
				t.Errorf("%s default prompt lacks %s", c.name, placeholder)
			}
		}
	}
}
