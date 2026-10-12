package ai

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"phraseforge/internal/config"
)

// exampleOn is the verdicts of a reply that marks the items at idx needsExample only.
func exampleOn(idx ...int) map[int]CheckVerdict {
	out := map[int]CheckVerdict{}
	for _, i := range idx {
		out[i] = CheckVerdict{NeedsExample: true}
	}
	return out
}

func TestFormatCheckItemsNumbersFromOneAndSpellsOutWhatIsMissing(t *testing.T) {
	got := formatCheckItems([]CheckItem{
		{Phrase: "der Koffer", Usage: "den Koffer packen", Examples: []string{"ein schwerer Koffer", "der alte Koffer"}, Translations: map[string]string{"pl": "walizka", "en": "suitcase; case"}},
		{Phrase: "der Tag", Usage: "jeden Tag"},
	})
	want := "1. der Koffer\n   usage: den Koffer packen\n   examples: ein schwerer Koffer | der alte Koffer\n   translations: en: suitcase; case | pl: walizka\n" +
		"2. der Tag\n   usage: jeden Tag\n   examples: none\n   translations: none\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderCheckPromptFillsBothPlaceholdersWithoutExpandingTheirValues(t *testing.T) {
	got := renderCheckPrompt("{{sourceLanguage}}:\n{{candidates}}\n{{unknown}}", "German", "1. {{sourceLanguage}}")
	if want := "German:\n1. {{sourceLanguage}}\n{{unknown}}"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseCheckVerdicts(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[int]CheckVerdict
	}{
		{"yes and no", `{"results":[{"n":1,"needsExample":true},{"n":2,"needsExample":false},{"n":3,"needsExample":true}]}`, exampleOn(0, 2)},
		{"surrounding whitespace", "\n {\"results\":[{\"n\":2,\"needsExample\":true}]} \n", exampleOn(1)},
		{"entries missing from the reply mean no", `{"results":[{"n":2,"needsExample":true}]}`, exampleOn(1)},
		{"unknown numbers are ignored", `{"results":[{"n":0,"needsExample":true},{"n":4,"needsExample":true},{"n":-1,"needsExample":true}]}`, map[int]CheckVerdict{}},
		{"non-boolean verdicts are ignored", `{"results":[{"n":1,"needsExample":"true"},{"n":2,"needsExample":1},{"n":3,"needsExample":null}]}`, map[int]CheckVerdict{}},
		{"malformed entries are ignored", `{"results":["x",{"n":"1","needsExample":true},{"needsExample":true},{"n":2},{"n":3,"needsExample":true}]}`, exampleOn(2)},
		{"an empty results array is a valid answer of none", `{"results":[]}`, map[int]CheckVerdict{}},
		{"needsSense alone", `{"results":[{"n":1,"needsExample":false,"needsSense":true}]}`, map[int]CheckVerdict{0: {NeedsSense: true}}},
		{"both verdicts on one item, others separate", `{"results":[{"n":1,"needsExample":true,"needsSense":true},{"n":2,"needsExample":true,"needsSense":false},{"n":3,"needsExample":false,"needsSense":true}]}`,
			map[int]CheckVerdict{0: {NeedsExample: true, NeedsSense: true}, 1: {NeedsExample: true}, 2: {NeedsSense: true}}},
		{"an absent needsSense counts as false", `{"results":[{"n":1,"needsExample":true}]}`, exampleOn(0)},
		{"a non-boolean needsSense counts as false", `{"results":[{"n":1,"needsExample":true,"needsSense":"yes"},{"n":2,"needsExample":false,"needsSense":1},{"n":3,"needsExample":false,"needsSense":null}]}`, exampleOn(0)},
		{"an entry without a boolean needsExample is ignored even with needsSense", `{"results":[{"n":1,"needsSense":true},{"n":2,"needsExample":"no","needsSense":true}]}`, map[int]CheckVerdict{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseCheckVerdicts(c.raw, 3)
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestParseCheckVerdictsRejectsAReplyThatIsNotTheExpectedObject(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":          "yes, both",
		"an array":          `[{"n":1,"needsExample":true}]`,
		"no results":        `{"answer":[]}`,
		"results not array": `{"results":"none"}`,
		"null results":      `{"results":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := parseCheckVerdicts(raw, 3); err == nil {
				t.Errorf("got %v, want an error", got)
			}
		})
	}
}

func TestCheckVocabularyAnnouncesItsKindAndPlaceholders(t *testing.T) {
	if !IsValidKind(KindCheckVocabulary) {
		t.Errorf("%q is not in ValidKinds, so an admin could not override its prompt", KindCheckVocabulary)
	}
	if got, want := PromptPlaceholders(KindCheckVocabulary), []string{"sourceLanguage", "candidates"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placeholders = %v, want %v", got, want)
	}
	for _, placeholder := range PromptPlaceholders(KindCheckVocabulary) {
		if !strings.Contains(config.DefaultCheckVocabularyPrompt, "{{"+placeholder+"}}") {
			t.Errorf("the default check prompt does not use {{%s}}", placeholder)
		}
	}
}

func checkVocabularyService(t *testing.T, replies ...string) (*Service, *fakeOllama) {
	t.Helper()
	pool := newAIDB(t)
	ollama := newFakeOllama(t, replies)
	cfg := config.Config{
		Providers:       map[string]config.ProviderConfig{"ollama": {BaseURL: ollama.URL}},
		CheckVocabulary: config.PurposeConfig{Provider: "ollama", Model: "test-model", Prompt: "Check {{sourceLanguage}}:\n{{candidates}}", MaxAttempts: 3},
	}
	return New(pool, cfg, nil, nil, nil, nil), ollama
}

var checkItems = []CheckItem{
	{Phrase: "der Koffer", Usage: "den Koffer packen", Examples: []string{"ein schwerer Koffer"}},
	{Phrase: "der Tag", Usage: "jeden Tag", Translations: map[string]string{"pl": "dzień"}},
}

func TestCheckVocabularySendsOneCallWithTheCandidatesAndReturnsTheMarkedOnes(t *testing.T) {
	s, ollama := checkVocabularyService(t, `{"results":[{"n":1,"needsExample":true},{"n":2,"needsExample":false}]}`)

	got, err := s.CheckVocabulary(context.Background(), "deu", checkItems)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got, exampleOn(0)) {
		t.Errorf("verdicts = %v, want only the first item", got)
	}
	want := "Check German:\n" + formatCheckItems(checkItems)
	if len(ollama.lastText) != 1 || ollama.lastText[0] != want {
		t.Fatalf("the model was sent %q, want one call with %q", ollama.lastText, want)
	}
}

func TestCheckVocabularySendsAnInvalidReplyBackForCorrection(t *testing.T) {
	s, ollama := checkVocabularyService(t, `both need one`, `{"results":[{"n":2,"needsExample":true}]}`)

	got, err := s.CheckVocabulary(context.Background(), "deu", checkItems)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, exampleOn(1)) {
		t.Errorf("verdicts = %v, want the second item", got)
	}
	if len(ollama.msgCount) != 2 || ollama.msgCount[1] != 3 {
		t.Errorf("messages per request = %v, want [1 3]", ollama.msgCount)
	}
}

func TestCheckVocabularyFailsAfterItsAttemptBudgetOnAReplyThatStaysInvalid(t *testing.T) {
	s, ollama := checkVocabularyService(t, `nope`, `nope`, `nope`)

	got, err := s.CheckVocabulary(context.Background(), "deu", checkItems)
	if err == nil || got != nil {
		t.Fatalf("got %v, %v, want no verdicts and an error", got, err)
	}
	if len(ollama.msgCount) != 3 {
		t.Errorf("model calls = %d, want the 3 attempts of the budget", len(ollama.msgCount))
	}
}

func TestCheckVocabularyWithNothingToCheckMakesNoCall(t *testing.T) {
	s, ollama := checkVocabularyService(t, `{"results":[]}`)
	if got, err := s.CheckVocabulary(context.Background(), "deu", nil); got != nil || err != nil || len(ollama.msgCount) != 0 {
		t.Fatalf("got %v, %v after %d calls, want nothing", got, err, len(ollama.msgCount))
	}
}

func TestCheckVocabularySchemaRequiresBothVerdicts(t *testing.T) {
	if !strings.Contains(string(CheckVocabularySchema), `"required":["n","needsExample","needsSense"]`) || !strings.Contains(string(CheckVocabularySchema), `"needsSense":{"type":"boolean"}`) {
		t.Errorf("CheckVocabularySchema = %s, want n, needsExample and needsSense required booleans", CheckVocabularySchema)
	}
}
