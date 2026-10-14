package ai

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"phraseforge/internal/config"
)

func defaultPrompts(t *testing.T) map[string]string {
	t.Helper()
	t.Setenv("CONFIG_FILE", t.TempDir()+"/none.yaml") // no file: the compiled-in defaults
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return (&Service{cfg: cfg}).DefaultPrompts()
}

var placeholderPattern = regexp.MustCompile(`\{\{(\w+)\}\}`)

// snippetsOf is the language snippets a prompt takes: transcription rules, the
// dictionary form, the grammar tags.
func snippetsOf(prompt string) []string {
	var out []string
	for _, m := range placeholderPattern.FindAllStringSubmatch(prompt, -1) {
		switch m[1] {
		case "transcriptionPrompt":
			out = append(out, "transcription")
		case "dictionaryPrompt":
			out = append(out, "dictionary")
		case "grammarPrompt":
			out = append(out, "grammar")
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Each default prompt takes exactly the language snippets its purpose needs: the
// rules of a language are written once, in its snippet, and a prompt that needs
// them points at the snippet instead of repeating them.
func TestEveryDefaultPromptHasTheSnippetsItsPurposeNeeds(t *testing.T) {
	needs := map[string][]string{
		"transcription":        {"transcription"},
		"translation":          nil,
		"title":                nil,
		"process_text":         nil,
		"process_dialog":       nil,
		"generate_vocabulary":  {"dictionary", "grammar", "transcription"},
		"generate_models":      {"transcription"},
		"vocabulary_item":      {"dictionary", "grammar", "transcription"},
		"models_item":          {"transcription"},
		"check_vocabulary":     nil,
		"review_vocabulary":    {"dictionary", "grammar", "transcription"},
		"review_models":        {"transcription"},
		"review_transcription": {"transcription"},
		"review_translation":   nil,
	}
	prompts := defaultPrompts(t)
	for _, kind := range ValidKinds {
		want, known := needs[kind]
		if !known {
			t.Errorf("kind %q has no entry in this test: say which snippets its prompt needs", kind)
			continue
		}
		if got := snippetsOf(prompts[kind]); !slices.Equal(got, want) {
			t.Errorf("the default %s prompt takes the snippets %v, its purpose needs %v", kind, got, want)
		}
	}
}

// A prompt uses only the placeholders its kind fills in.
func TestEveryDefaultPromptUsesOnlyPlaceholdersItsKindFillsIn(t *testing.T) {
	for kind, prompt := range defaultPrompts(t) {
		allowed := PromptPlaceholders(kind)
		for _, m := range placeholderPattern.FindAllStringSubmatch(prompt, -1) {
			if !slices.Contains(allowed, m[1]) {
				t.Errorf("the default %s prompt uses {{%s}}, which that kind does not fill in (allowed: %v)", kind, m[1], allowed)
			}
		}
	}
}

// What a snippet describes is not spelled out again in a prompt, and the
// vocabulary prompts keep only the language-independent part of the rule.
func TestDefaultPromptsDoNotRepeatWhatTheSnippetsDescribe(t *testing.T) {
	prompts := defaultPrompts(t)
	for kind, prompt := range prompts {
		for _, inline := range []string{"Conj, Num", "verbs in the infinitive", "3rd person masculine singular perfect", "1st person singular present"} {
			if strings.Contains(prompt, inline) {
				t.Errorf("the default %s prompt spells out %q, which belongs in a language snippet", kind, inline)
			}
		}
	}
	// Without a dictionary snippet the model must still give the dictionary form,
	// not the form in the text (measured on Latin: inflected forms came back).
	if !strings.Contains(prompts["generate_vocabulary"], "never the inflected form found in the text") {
		t.Error("the generate_vocabulary prompt lost the language-independent dictionary-form rule")
	}
}

// A reviewer checks what the generator it reviews is told to do.
func TestReviewPromptsCheckTheRulesTheirGeneratorsEnforce(t *testing.T) {
	prompts := defaultPrompts(t)
	for _, c := range []struct {
		generator, reviewer string
		rules               []string
	}{
		{"vocabulary_item", "review_vocabulary", []string{"Lowercase", "full stop", "parentheses", `"; "`}},
		{"models_item", "review_models", []string{"capital letter", "parentheses", `"; "`, `gap "..."`}},
	} {
		for _, rule := range c.rules {
			gen, rev := strings.Contains(strings.ToLower(prompts[c.generator]), strings.ToLower(rule)), strings.Contains(strings.ToLower(prompts[c.reviewer]), strings.ToLower(rule))
			if !gen || !rev {
				t.Errorf("rule %q: in the %s prompt %v, in the %s prompt %v; both must state it", rule, c.generator, gen, c.reviewer, rev)
			}
		}
	}
}

// The vocabulary prompts take the dictionary rules from the snippet, which is
// rendered into them.
func TestVocabularyPromptsRenderTheDictionarySnippet(t *testing.T) {
	prompts := defaultPrompts(t)
	for _, kind := range []string{"generate_vocabulary", "vocabulary_item"} {
		rendered := renderSnippets(prompts[kind], LanguageSections{DictionaryPrompt: "DICTIONARY-RULES-OF-THE-LANGUAGE"})
		if !strings.Contains(rendered, "DICTIONARY-RULES-OF-THE-LANGUAGE") || strings.Contains(rendered, "{{dictionaryPrompt}}") {
			t.Errorf("%s: the dictionary snippet is not rendered into the prompt", kind)
		}
	}
}
