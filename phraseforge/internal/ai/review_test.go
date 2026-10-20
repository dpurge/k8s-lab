package ai

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"phraseforge/internal/config"
)

var (
	reviewSections = LanguageSections{Language: "deu", GrammarPrompt: "N m f n", TranscriptionPrompt: "", DictionaryPrompt: "Nouns with the article."}
	reviewItem     = ReviewInput{
		Language: "deu", Phrase: "den Hund", Grammar: "N m",
		Examples: []string{"ein großer Hund", "ein großer Hund", "Der Hund läuft sehr schnell durch den Park"},
		Translations: []ReviewTranslation{
			{Locale: "pl", Translation: "pies", Notes: "zwierzę domowe"},
			{Locale: "en", Translation: "dog (animal)"},
		},
	}
)

func TestFormatReviewInputs(t *testing.T) {
	if got, want := formatReviewExamples(reviewItem.Examples), "1. ein großer Hund\n2. ein großer Hund\n3. Der Hund läuft sehr schnell durch den Park"; got != want {
		t.Errorf("examples = %q, want %q", got, want)
	}
	if got := formatReviewExamples(nil); got != "none" {
		t.Errorf("no examples = %q, want none", got)
	}
	// English first as the reference, notes under their locale, no trailing newline.
	if got, want := formatReviewTranslations(reviewItem.Translations), "- en: dog (animal)\n- pl: pies\n  notes: zwierzę domowe"; got != want {
		t.Errorf("translations = %q, want %q", got, want)
	}
	if got := formatReviewTranslations(nil); got != "none" {
		t.Errorf("no translations = %q, want none", got)
	}
}

func TestRenderReviewPromptFillsEveryPlaceholderInOnePass(t *testing.T) {
	var all []string
	for _, p := range PromptPlaceholders(KindReviewVocabulary) {
		all = append(all, "{{"+p+"}}")
	}
	got := renderReviewPrompt(strings.Join(all, "|")+"|{{unknown}}", reviewPromptVars{
		SourceLanguage: "German", Phrase: "{{grammar}}", Grammar: "g", Transcription: "t", Examples: "e", Translations: "tr",
		DictionaryPrompt: "d", GrammarPrompt: "gp", TranscriptionPrompt: "tp",
	})
	if want := "German|{{grammar}}|g|t|e|tr|d|gp|tp|{{unknown}}"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	for _, p := range PromptPlaceholders(KindReviewVocabulary) {
		if !strings.Contains(config.DefaultReviewVocabularyPrompt, "{{"+p+"}}") {
			t.Errorf("the default review prompt does not use {{%s}}", p)
		}
	}
	if !IsValidKind(KindReviewVocabulary) {
		t.Errorf("%q is not in ValidKinds", KindReviewVocabulary)
	}
}

func TestEveryReviewPurposeUsesItsOwnModelTags(t *testing.T) {
	own := func(name string) config.PurposeConfig {
		return config.PurposeConfig{ModelTags: []string{"tag-" + name}, NumCtx: 8192, MaxAttempts: 3}
	}
	s := &Service{cfg: config.Config{
		VocabularyItem:      own("item"),
		ReviewVocabulary:    own("vocabulary"),
		ReviewModels:        own("models"),
		ReviewTranscription: own("transcription"),
		ReviewTranslation:   own("translation"),
	}}
	for kind, name := range map[string]string{
		KindReviewVocabulary: "vocabulary", KindReviewModels: "models",
		KindReviewTranscription: "transcription", KindReviewTranslation: "translation",
	} {
		if got := s.purposeDefault(kind); !reflect.DeepEqual(got, own(name)) {
			t.Errorf("%s: got %+v, want its own setting %+v", kind, got, own(name))
		}
	}

	// A purpose left blank stays blank: nothing is borrowed from another purpose.
	for _, kind := range []string{KindReviewVocabulary, KindReviewModels, KindReviewTranscription, KindReviewTranslation} {
		blank := &Service{cfg: config.Config{
			VocabularyItem: own("item"), ReviewVocabulary: own("vocabulary"), ReviewModels: own("models"),
			ReviewTranscription: own("transcription"), ReviewTranslation: own("translation"),
		}}
		switch kind {
		case KindReviewVocabulary:
			blank.cfg.ReviewVocabulary = config.PurposeConfig{}
		case KindReviewModels:
			blank.cfg.ReviewModels = config.PurposeConfig{}
		case KindReviewTranscription:
			blank.cfg.ReviewTranscription = config.PurposeConfig{}
		case KindReviewTranslation:
			blank.cfg.ReviewTranslation = config.PurposeConfig{}
		}
		if got := blank.purposeDefault(kind); len(got.ModelTags) != 0 {
			t.Errorf("%s left blank: got tags %v, want it blank (no fallback)", kind, got.ModelTags)
		}
	}
}

const (
	okEntry = `{"ok":true,"suggestion":"","reason":""}`
	fullOK  = `{"phrase":` + okEntry + `,"grammar":` + okEntry + `,"transcription":` + okEntry + `,"translations":[],"notes":[],"examples":[]}`
)

func TestParseReviewReplyNothingToReport(t *testing.T) {
	got, err := parseReviewReply(fullOK, reviewItem, reviewSections)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v; want no findings", got, err)
	}
}

func TestParseReviewReplyPhraseRewriteIsOneUnit(t *testing.T) {
	raw := `{"phrase":{"ok":false,"suggestion":"  der Hund ","reason":"Accusative form."},"grammar":{"ok":false,"suggestion":"N m","reason":"x"},"transcription":` + okEntry + `,"translations":[],"notes":[],"examples":[]}`
	got, err := parseReviewReply(raw, reviewItem, reviewSections)
	if err != nil {
		t.Fatal(err)
	}
	// The grammar entry is folded into the phrase finding: no separate grammar proposal.
	want := []ReviewFinding{{Field: ReviewFieldPhrase, Action: ReviewReplace, Current: "den Hund", Suggestion: "der Hund", Grammar: "N m", Reason: "Accusative form."}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseReviewReplyPhraseSuggestionNeedsGrammarAndTranscriptionOfTheNewPhrase(t *testing.T) {
	noGrammar := `{"phrase":{"ok":false,"suggestion":"der Hund","reason":"r"},"grammar":` + okEntry + `,"transcription":` + okEntry + `,"translations":[],"notes":[],"examples":[]}`
	if got, err := parseReviewReply(noGrammar, reviewItem, reviewSections); err == nil || !strings.Contains(err.Error(), `"grammar"`) {
		t.Errorf("got %+v, %v; want an error asking for a grammar suggestion", got, err)
	}
	// A language with a transcription snippet and an item with a transcription needs the third part too.
	chinese := ReviewInput{Language: "cmn", Phrase: "吃了", Grammar: "V", Transcription: "chī le"}
	sections := LanguageSections{GrammarPrompt: "g", TranscriptionPrompt: "t", DictionaryPrompt: "d"}
	noTranscription := `{"phrase":{"ok":false,"suggestion":"吃","reason":"r"},"grammar":{"ok":true,"suggestion":"V","reason":""},"transcription":` + okEntry + `,"translations":[],"notes":[],"examples":[]}`
	if _, err := parseReviewReply(noTranscription, chinese, sections); err == nil || !strings.Contains(err.Error(), `"transcription"`) {
		t.Errorf("err = %v, want one asking for a transcription suggestion", err)
	}
	withBoth := strings.Replace(noTranscription, `"transcription":`+okEntry, `"transcription":{"ok":false,"suggestion":"chī","reason":"r"}`, 1)
	got, err := parseReviewReply(withBoth, chinese, sections)
	if err != nil || len(got) != 1 || got[0].Transcription != "chī" || got[0].Grammar != "V" {
		t.Fatalf("got %+v, %v; want one phrase finding carrying grammar V and transcription chī", got, err)
	}
	// An item with no grammar needs no grammar suggestion.
	bare := ReviewInput{Language: "deu", Phrase: "den Hund"}
	if got, err := parseReviewReply(noGrammar, bare, reviewSections); err != nil || len(got) != 1 {
		t.Errorf("got %+v, %v; want the rewrite accepted for an item without grammar", got, err)
	}
}

func TestParseReviewReplyIgnoresChecksOfSectionsTheLanguageLacks(t *testing.T) {
	raw := `{"phrase":{"ok":false,"suggestion":"der Hund","reason":"r"},"grammar":{"ok":false,"suggestion":"N f","reason":"r"},"transcription":{"ok":false,"suggestion":"x","reason":"r"},"translations":[],"notes":[],"examples":[]}`
	got, err := parseReviewReply(raw, reviewItem, LanguageSections{})
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v; want nothing: no dictionary-form, grammar or transcription snippet", got, err)
	}
	// Without a dictionary-form snippet the phrase is not checked, so grammar stands alone.
	got, err = parseReviewReply(raw, reviewItem, LanguageSections{GrammarPrompt: "g"})
	if err != nil || len(got) != 1 || got[0].Field != ReviewFieldGrammar || got[0].Suggestion != "N f" {
		t.Fatalf("got %+v, %v; want the grammar proposal only", got, err)
	}
}

func TestParseReviewReplyTranslationsNotesAndExamples(t *testing.T) {
	raw := `{"phrase":` + okEntry + `,"grammar":` + okEntry + `,"transcription":` + okEntry + `,
	"translations":[
	  {"locale":"en","ok":false,"suggestion":"dog","reason":"No explanations."},
	  {"locale":"pl","ok":false,"suggestion":"","reason":"Too vague."},
	  {"locale":"fr","ok":false,"suggestion":"chien","reason":"not an item locale"},
	  {"locale":"en","ok":false,"suggestion":"canine","reason":"second entry of a locale"}],
	"notes":[{"locale":"pl","ok":false,"suggestion":"","reason":"The translation is enough."},{"locale":"en","ok":false,"suggestion":"","reason":"no notes there"}],
	"examples":[
	  {"index":3,"action":"shorten","suggestion":"Der Hund läuft schnell","reason":"Too long."},
	  {"index":2,"action":"remove","suggestion":"","reason":"Repeats 1."},
	  {"index":1,"action":"keep","suggestion":"","reason":""},
	  {"index":9,"action":"remove","suggestion":"","reason":"no such example"},
	  {"index":2,"action":"remove","suggestion":"","reason":"a repeated number"}]}`
	got, err := parseReviewReply(raw, reviewItem, reviewSections)
	if err != nil {
		t.Fatal(err)
	}
	want := []ReviewFinding{
		// In the item's locale order (pl, then en), the first entry of a locale counts.
		{Field: ReviewFieldTranslation, Action: ReviewComment, Locale: "pl", Current: "pies", Reason: "Too vague."},
		{Field: ReviewFieldTranslation, Action: ReviewReplace, Locale: "en", Current: "dog (animal)", Suggestion: "dog", Reason: "No explanations."},
		{Field: ReviewFieldNotes, Action: ReviewRemove, Locale: "pl", Current: "zwierzę domowe", Reason: "The translation is enough."},
		{Field: ReviewFieldExample, Action: ReviewRemove, Index: 2, Current: "ein großer Hund", Reason: "Repeats 1."},
		{Field: ReviewFieldExample, Action: ReviewReplace, Index: 3, Current: "Der Hund läuft sehr schnell durch den Park", Suggestion: "Der Hund läuft schnell", Reason: "Too long."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseReviewReplyDropsUnusableSuggestions(t *testing.T) {
	long := strings.Repeat("x", MaxUsageChars+1)
	raw := `{"phrase":` + okEntry + `,"grammar":` + okEntry + `,"transcription":` + okEntry + `,
	"translations":[{"locale":"pl","ok":true,"suggestion":"pies","reason":"same as stored"},{"locale":"en","ok":true,"suggestion":"","reason":"fine"}],
	"notes":["x",null,{"locale":"pl","ok":"no"}],
	"examples":[{"index":1,"action":"shorten","suggestion":"` + long + `","reason":"r"},{"index":2,"action":"shorten","suggestion":"","reason":"r"},{"index":"3","action":"remove"},{"index":3,"action":"explode"}]}`
	got, err := parseReviewReply(raw, reviewItem, reviewSections)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v; want no findings", got, err)
	}
}

func TestParseReviewReplyCapsALongReason(t *testing.T) {
	raw := `{"phrase":{"ok":false,"suggestion":"","reason":"` + strings.Repeat("é", 1000) + `"},"grammar":` + okEntry + `,"transcription":` + okEntry + `,"translations":[],"notes":[],"examples":[]}`
	got, err := parseReviewReply(raw, reviewItem, reviewSections)
	if err != nil || len(got) != 1 || got[0].Action != ReviewComment || len([]rune(got[0].Reason)) != maxReviewReasonChars {
		t.Fatalf("got %+v, %v; want one comment with the reason cut to %d characters", got, err, maxReviewReasonChars)
	}
}

func TestParseReviewReplyRejectsAReplyThatIsNotTheExpectedObject(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":        "all fine",
		"an array":        `[{"phrase":{}}]`,
		"no phrase":       `{"grammar":` + okEntry + `}`,
		"null phrase":     `{"phrase":null}`,
		"wrong list type": `{"phrase":` + okEntry + `,"translations":"none"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := parseReviewReply(raw, reviewItem, reviewSections); err == nil {
				t.Errorf("got %+v, want an error", got)
			}
		})
	}
}

func TestReviewVocabularySchemaRequiresEveryCheck(t *testing.T) {
	for _, want := range []string{`"required":["phrase","grammar","transcription","translations","notes","examples"]`, `"enum":["keep","remove","shorten"]`} {
		if !strings.Contains(string(ReviewVocabularySchema), want) {
			t.Errorf("ReviewVocabularySchema lacks %s", want)
		}
	}
}

func reviewService(t *testing.T, replies ...string) (*Service, *fakeOllama) {
	t.Helper()
	pool := newAIDB(t)
	ollama := newFakeOllama(t, replies)
	cfg := config.Config{
		ReviewVocabulary: config.PurposeConfig{Prompt: "Review {{phrase}} ({{sourceLanguage}})\nDICT {{dictionaryPrompt}}\n{{translations}}\n{{examples}}", MaxAttempts: 3},
	}
	svc := useFakeOllama(t, New(pool, cfg, nil, nil, nil, nil), ollama.URL)
	if err := svc.SetLanguageSections(context.Background(), reviewSections); err != nil {
		t.Fatal(err)
	}
	return svc, ollama
}

func TestReviewItemSendsOneCallWithTheEntryAndReturnsItsFindings(t *testing.T) {
	reply := `{"phrase":{"ok":false,"suggestion":"der Hund","reason":"r"},"grammar":{"ok":true,"suggestion":"N m","reason":""},"transcription":` + okEntry + `,"translations":[],"notes":[],"examples":[]}`
	s, ollama := reviewService(t, reply)

	got, err := s.ReviewItem(context.Background(), reviewItem)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 1 || got.Findings[0].Suggestion != "der Hund" || got.Findings[0].Grammar != "N m" {
		t.Errorf("findings = %+v", got.Findings)
	}
	if string(got.Response) != reply {
		t.Errorf("response = %s, want the model's reply kept as it came", got.Response)
	}
	want := "Review den Hund (German)\nDICT Nouns with the article.\n- en: dog (animal)\n- pl: pies\n  notes: zwierzę domowe\n" + formatReviewExamples(reviewItem.Examples)
	if len(ollama.lastText) != 1 || ollama.lastText[0] != want {
		t.Fatalf("the model was sent %q, want one call with %q", ollama.lastText, want)
	}
}

func TestReviewItemSendsAnInvalidReplyBackForCorrection(t *testing.T) {
	s, ollama := reviewService(t, `looks fine`, fullOK)
	got, err := s.ReviewItem(context.Background(), reviewItem)
	if err != nil || len(got.Findings) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if len(ollama.msgCount) != 2 || ollama.msgCount[1] != 3 {
		t.Errorf("messages per request = %v, want [1 3]", ollama.msgCount)
	}
}

func TestReviewItemFailsAfterItsAttemptBudgetOnAReplyThatStaysInvalid(t *testing.T) {
	s, ollama := reviewService(t, `nope`, `nope`, `nope`)
	if got, err := s.ReviewItem(context.Background(), reviewItem); err == nil || got.Findings != nil {
		t.Fatalf("got %+v, %v; want an error and no findings", got, err)
	}
	if len(ollama.msgCount) != 3 {
		t.Errorf("model calls = %d, want the 3 attempts of the budget", len(ollama.msgCount))
	}
}
