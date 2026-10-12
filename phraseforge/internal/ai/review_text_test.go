package ai

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"phraseforge/internal/config"
	"phraseforge/internal/jobs"
)

var textInput = TextReviewInput{
	Language: "deu", Body: "Der Hund schläft. Die Kinder spielen.", Transcription: "tr", Version: "v1",
	Translations: []ReviewTranslation{{Locale: "pl", Translation: "Pies śpi. Dzieci się bawią."}, {Locale: "en", Translation: "The dog sleeps. The kids play cats."}},
	Vocabulary:   []TextReviewVocabulary{{Phrase: "der Hund", Translations: map[string]string{"pl": "pies", "en": "dog"}}, {Phrase: "spielen"}},
}

func TestFormatTextReviewInputs(t *testing.T) {
	if got, want := formatTextTranslations(textInput.Translations), "[en]\nThe dog sleeps. The kids play cats.\n\n[pl]\nPies śpi. Dzieci się bawią."; got != want {
		t.Errorf("translations = %q, want %q", got, want)
	}
	if got, want := formatTextVocabulary(textInput.Vocabulary), "- der Hund = en: dog | pl: pies\n- spielen"; got != want {
		t.Errorf("vocabulary = %q, want %q", got, want)
	}
	if formatTextTranslations(nil) != "none" || formatTextVocabulary(nil) != "none" {
		t.Error("empty inputs must be spelled none")
	}
}

func TestReviewTextPromptsAndPurposesAreSeparate(t *testing.T) {
	prompts := map[string]string{KindReviewTranscription: config.DefaultReviewTranscriptionPrompt, KindReviewTranslation: config.DefaultReviewTranslationPrompt}
	if prompts[KindReviewTranscription] == prompts[KindReviewTranslation] {
		t.Error("the two reviews must have their own prompts")
	}
	for kind, prompt := range prompts {
		if !IsValidKind(kind) {
			t.Errorf("%q is not in ValidKinds", kind)
		}
		for _, p := range PromptPlaceholders(kind) {
			if !strings.Contains(prompt, "{{"+p+"}}") {
				t.Errorf("the default %s prompt does not use {{%s}}", kind, p)
			}
		}
	}
	if reviewKindOf(ReviewPartTranscription) != KindReviewTranscription || reviewKindOf(ReviewPartTranslation) != KindReviewTranslation {
		t.Error("reviewKindOf maps a part to the wrong purpose")
	}
	got := renderTextReviewPrompt("{{sourceLanguage}}|{{text}}|{{transcription}}|{{translations}}|{{vocabulary}}|{{transcriptionPrompt}}|{{x}}",
		textReviewPromptVars{SourceLanguage: "German", Text: "{{vocabulary}}", Transcription: "t", Translations: "tr", Vocabulary: "v", TranscriptionPrompt: "p"})
	if want := "German|{{vocabulary}}|t|tr|v|p|{{x}}"; got != want {
		t.Errorf("render = %q, want %q (one pass)", got, want)
	}
}

func TestReviewTextPurposesFallBackToTheReviewVocabularyThenTheItemModelEachOnItsOwn(t *testing.T) {
	item := config.PurposeConfig{Provider: "ollama", Model: "small"}
	big := config.PurposeConfig{Provider: "openrouter", Model: "big"}
	cases := []struct {
		name                               string
		vocab, transcription, translation  config.PurposeConfig
		wantTranscription, wantTranslation string
	}{
		{"nothing set: the item model", config.PurposeConfig{}, config.PurposeConfig{}, config.PurposeConfig{}, "ollama/small", "ollama/small"},
		{"the vocabulary review model serves both", big, config.PurposeConfig{}, config.PurposeConfig{}, "openrouter/big", "openrouter/big"},
		{"each has its own setting", big, config.PurposeConfig{Provider: "openrouter", Model: "t1"}, config.PurposeConfig{Provider: "ollama", Model: "t2"}, "openrouter/t1", "ollama/t2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.transcription.NumCtx, c.translation.NumCtx = 16384, 16384
			s := &Service{cfg: config.Config{VocabularyItem: item, ReviewVocabulary: c.vocab, ReviewTranscription: c.transcription, ReviewTranslation: c.translation}}
			for kind, want := range map[string]string{KindReviewTranscription: c.wantTranscription, KindReviewTranslation: c.wantTranslation} {
				got := s.purposeDefault(kind)
				if got.Provider+"/"+got.Model != want || got.NumCtx != 16384 {
					t.Errorf("%s: got %+v, want %s with its own numCtx", kind, got, want)
				}
			}
		})
	}
}

var sectionsWithTranscription = LanguageSections{TranscriptionPrompt: "rules"}

func TestParseTranslationReviewReply(t *testing.T) {
	raw := `{"translations":[{"locale":"en","ok":false,"suggestion":"The dog sleeps. The kids play.","reason":"The cats are not in the text."},
	{"locale":"pl","ok":true,"suggestion":"","reason":""},{"locale":"fr","ok":false,"suggestion":"x","reason":"unknown locale"}]}`
	got, err := parseTextReviewReply(raw, ReviewPartTranslation, textInput, sectionsWithTranscription)
	if err != nil {
		t.Fatal(err)
	}
	want := []ReviewFinding{{Field: ReviewFieldTranslation, Action: ReviewReplace, Locale: "en", Current: "The dog sleeps. The kids play cats.", Suggestion: "The dog sleeps. The kids play.", Reason: "The cats are not in the text."}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseTranscriptionReviewReply(t *testing.T) {
	raw := `{"ok":false,"suggestion":"tr2","reason":"A sentence is missing."}`
	got, err := parseTextReviewReply(raw, ReviewPartTranscription, textInput, sectionsWithTranscription)
	want := []ReviewFinding{{Field: ReviewFieldTranscription, Action: ReviewReplace, Current: "tr", Suggestion: "tr2", Reason: "A sentence is missing."}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, %v; want %+v", got, err, want)
	}
	// Nothing to review without a snippet or without a transcription.
	if got, err := parseTextReviewReply(raw, ReviewPartTranscription, textInput, LanguageSections{}); err != nil || len(got) != 0 {
		t.Errorf("no snippet: got %+v, %v", got, err)
	}
	bare := textInput
	bare.Transcription = ""
	if got, err := parseTextReviewReply(raw, ReviewPartTranscription, bare, sectionsWithTranscription); err != nil || len(got) != 0 {
		t.Errorf("no transcription: got %+v, %v", got, err)
	}
}

func TestParseTextReviewReplyRejectsAReplyOfTheWrongShape(t *testing.T) {
	for name, c := range map[string][2]string{
		"transcription: not json": {ReviewPartTranscription, "fine"},
		"transcription: no ok":    {ReviewPartTranscription, `{"suggestion":"x"}`},
		"transcription: an array": {ReviewPartTranscription, `[]`},
		"translation: not json":   {ReviewPartTranslation, "fine"},
		"translation: no array":   {ReviewPartTranslation, `{"ok":true}`},
		"translation: null array": {ReviewPartTranslation, `{"translations":null}`},
	} {
		if got, err := parseTextReviewReply(c[1], c[0], textInput, sectionsWithTranscription); err == nil {
			t.Errorf("%s: got %+v, want an error", name, got)
		}
	}
}

func TestReviewTextSchemas(t *testing.T) {
	if !strings.Contains(string(ReviewTranscriptionSchema), `"required":["ok","suggestion","reason"]`) {
		t.Errorf("ReviewTranscriptionSchema = %s", ReviewTranscriptionSchema)
	}
	if !strings.Contains(string(ReviewTranslationSchema), `"required":["translations"]`) || !strings.Contains(string(ReviewTranslationSchema), `"locale"`) {
		t.Errorf("ReviewTranslationSchema = %s", ReviewTranslationSchema)
	}
}

type fakeTextReviewStore struct {
	source               TextReviewInput
	sourceErr, saveErr   error
	saves                int
	savedVersion         string
	savedFindings, resp  json.RawMessage
	savedType, savedPart string
}

func (f *fakeTextReviewStore) TextReviewSource(ctx context.Context, rt string, id int64) (TextReviewInput, error) {
	return f.source, f.sourceErr
}

func (f *fakeTextReviewStore) SaveTextReview(ctx context.Context, rt string, id int64, part, version string, findings, response json.RawMessage) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saves++
	f.savedType, f.savedPart, f.savedVersion, f.savedFindings, f.resp = rt, part, version, findings, response
	return nil
}

func textReviewService(t *testing.T, replies ...string) (*Service, *fakeOllama) {
	t.Helper()
	pool := newAIDB(t)
	ollama := newFakeOllama(t, replies)
	cfg := config.Config{
		Providers:           map[string]config.ProviderConfig{"ollama": {BaseURL: ollama.URL}},
		VocabularyItem:      config.PurposeConfig{Provider: "ollama", Model: "item-model"},
		ReviewTranscription: config.PurposeConfig{Prompt: "TR {{text}} ({{sourceLanguage}})\n{{transcription}}\n{{vocabulary}}\n{{transcriptionPrompt}}", MaxAttempts: 3},
		ReviewTranslation:   config.PurposeConfig{Prompt: "TL {{text}} ({{sourceLanguage}})\n{{translations}}\n{{vocabulary}}", MaxAttempts: 3},
	}
	svc := New(pool, cfg, nil, nil, nil, nil)
	if err := svc.SetLanguageSections(context.Background(), LanguageSections{Language: "deu", TranscriptionPrompt: "rules"}); err != nil {
		t.Fatal(err)
	}
	return svc, ollama
}

const (
	transcriptionOK  = `{"ok":true,"suggestion":"","reason":""}`
	transcriptionFix = `{"ok":false,"suggestion":"tr2","reason":"missing"}`
	translationOK    = `{"translations":[]}`
	translationFix   = `{"translations":[{"locale":"en","ok":false,"suggestion":"The dog sleeps. The kids play.","reason":"cats"}]}`
)

func runTextReviewJob(t *testing.T, store *fakeTextReviewStore, payload string, replies ...string) (*fakeOllama, error) {
	t.Helper()
	s, ollama := textReviewService(t, replies...)
	if store != nil {
		s.SetTextReviewStore(store)
	}
	_, err := s.HandleReviewResource(context.Background(), "job", json.RawMessage(payload))
	return ollama, err
}

func TestEachPartIsReviewedWithItsOwnPromptAndOnlyItsOwnData(t *testing.T) {
	store := &fakeTextReviewStore{source: textInput}
	ollama, err := runTextReviewJob(t, store, `{"resource_type":"dialog","resource_id":5,"part":"translation"}`, translationFix)
	if err != nil {
		t.Fatal(err)
	}
	if store.saves != 1 || store.savedType != "dialog" || store.savedPart != "translation" || store.savedVersion != "v1" {
		t.Fatalf("saved %d as %q part %q version %q", store.saves, store.savedType, store.savedPart, store.savedVersion)
	}
	var findings []ReviewFinding
	_ = json.Unmarshal(store.savedFindings, &findings)
	if len(findings) != 1 || findings[0].Status != ReviewOpen || findings[0].Locale != "en" {
		t.Errorf("findings = %+v", findings)
	}
	want := "TL " + textInput.Body + " (German)\n" + formatTextTranslations(textInput.Translations) + "\n" + formatTextVocabulary(textInput.Vocabulary)
	if len(ollama.lastText) != 1 || ollama.lastText[0] != want {
		t.Errorf("the translation review was sent %q, want %q (no transcription)", ollama.lastText, want)
	}

	store = &fakeTextReviewStore{source: textInput}
	ollama, err = runTextReviewJob(t, store, `{"resource_type":"text","resource_id":5,"part":"transcription"}`, transcriptionFix)
	if err != nil {
		t.Fatal(err)
	}
	want = "TR " + textInput.Body + " (German)\n" + textInput.Transcription + "\n" + formatTextVocabulary(textInput.Vocabulary) + "\nrules"
	if len(ollama.lastText) != 1 || ollama.lastText[0] != want || store.savedPart != "transcription" {
		t.Errorf("the transcription review was sent %q, want %q (no translations)", ollama.lastText, want)
	}
}

func TestHandleReviewResourceStoresAnEmptyReviewAndSkipsGoneOrChanged(t *testing.T) {
	store := &fakeTextReviewStore{source: textInput}
	if _, err := runTextReviewJob(t, store, `{"resource_type":"text","resource_id":5,"part":"translation"}`, translationOK); err != nil || string(store.savedFindings) != `[]` {
		t.Fatalf("err %v findings %s, want an empty review stored", err, store.savedFindings)
	}
	for name, st := range map[string]*fakeTextReviewStore{
		"gone":    {sourceErr: ErrItemGone},
		"changed": {source: textInput, saveErr: ErrItemChanged},
	} {
		if _, err := runTextReviewJob(t, st, `{"resource_type":"text","resource_id":5,"part":"translation"}`, translationOK); !errors.Is(err, jobs.ErrSkipped) {
			t.Errorf("%s: err = %v, want a skip", name, err)
		}
	}
}

func TestOnlyTheReviewedPartCountsTowardTheSizeLimit(t *testing.T) {
	long := textInput
	long.Translations = []ReviewTranslation{{Locale: "en", Translation: strings.Repeat("x", maxTextReviewChars)}}
	store := &fakeTextReviewStore{source: long}
	ollama, err := runTextReviewJob(t, store, `{"resource_type":"text","resource_id":5,"part":"translation"}`, translationOK)
	if err == nil || !strings.Contains(err.Error(), "too long") || len(ollama.msgCount) != 0 || store.saves != 0 {
		t.Errorf("translation: err %v after %d calls, saves %d; want a too-long error and no call", err, len(ollama.msgCount), store.saves)
	}
	// The same resource's transcription is small: the translations are not sent.
	if _, err := runTextReviewJob(t, &fakeTextReviewStore{source: long}, `{"resource_type":"text","resource_id":5,"part":"transcription"}`, transcriptionOK); err != nil {
		t.Errorf("transcription of the same resource: %v", err)
	}
}

func TestHandleReviewResourceCorrectsAnInvalidReplyAndRejectsBadPayloads(t *testing.T) {
	store := &fakeTextReviewStore{source: textInput}
	ollama, err := runTextReviewJob(t, store, `{"resource_type":"text","resource_id":5,"part":"transcription"}`, `looks fine`, transcriptionOK)
	if err != nil || len(ollama.msgCount) != 2 || ollama.msgCount[1] != 3 {
		t.Fatalf("err %v messages %v, want one correction round", err, ollama.msgCount)
	}
	for _, payload := range []string{`x`, `{}`, `{"resource_type":"model","resource_id":1,"part":"translation"}`, `{"resource_type":"text","part":"translation"}`, `{"resource_type":"text","resource_id":1}`, `{"resource_type":"text","resource_id":1,"part":"title"}`} {
		if _, err := runTextReviewJob(t, store, payload, transcriptionOK); err == nil {
			t.Errorf("payload %s accepted", payload)
		}
	}
	if _, err := runTextReviewJob(t, nil, `{"resource_type":"text","resource_id":5,"part":"translation"}`, translationOK); err == nil {
		t.Error("a missing store must fail the job")
	}
	if job, err := NewTextReviewJob("dialog", 9, "translation"); err != nil || job.Kind != KindReviewTextJob || string(job.Payload) != `{"resource_type":"dialog","resource_id":9,"part":"translation"}` {
		t.Errorf("NewTextReviewJob = %+v, %v", job, err)
	}
}
