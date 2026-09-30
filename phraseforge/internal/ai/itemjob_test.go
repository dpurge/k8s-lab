package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

var (
	deuSections = LanguageSections{Language: "deu", GrammarPrompt: "N = noun"}
	cmnSections = LanguageSections{Language: "cmn", GrammarPrompt: "N = noun", TranscriptionPrompt: "Transcribe using pinyin."}
)

func vocabPayload(phrase string) ItemJobPayload {
	return ItemJobPayload{ResourceType: resourceVocabularyItem, ListID: 7, Position: 2, Language: "deu", Locale: "pl", Phrase: phrase}
}

func TestDecideItemWritesVocabulary(t *testing.T) {
	raw := `{"phrase":"der Koffer","grammar":"N m","transcription":"koffer","translation":"walizka","notes":"n."}`
	got, err := decideItemWrites(vocabPayload("der Koffer"), raw, deuSections)
	if err != nil {
		t.Fatal(err)
	}
	// deu has no transcription section: the volunteered "koffer" is dropped.
	want := itemWrites{Translation: "walizka", Notes: "n.", Grammar: "N m"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestDecideItemWritesNoSections(t *testing.T) {
	raw := `{"phrase":"der Koffer","grammar":"N m","translation":"walizka"}`
	got, err := decideItemWrites(vocabPayload("der Koffer"), raw, LanguageSections{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Grammar != "" || got.Transcription != "" || got.Translation != "walizka" {
		t.Errorf("got %+v, want translation only without sections", got)
	}
}

func TestDecideItemWritesModels(t *testing.T) {
	p := ItemJobPayload{ResourceType: resourceModelsItem, ListID: 1, Language: "cmn", Locale: "pl", Phrase: "我叫…"}
	got, err := decideItemWrites(p, `{"phrase":"我叫…","transcription":"wǒ jiào…","translation":"Nazywam się…"}`, cmnSections)
	if err != nil {
		t.Fatal(err)
	}
	if got != (itemWrites{Translation: "Nazywam się…", Transcription: "wǒ jiào…"}) {
		t.Errorf("got %+v", got)
	}
}

func TestDecideItemWritesInvalidReplyFails(t *testing.T) {
	if _, err := decideItemWrites(vocabPayload("der Koffer"), `{"phrase":"der Koffer"}`, deuSections); err == nil || !strings.Contains(err.Error(), `missing "translation"`) {
		t.Errorf("err = %v, want a missing translation error", err)
	}
}

type fakeItemStores struct {
	calls                []string
	translationApplied   bool
	transcriptionApplied bool
	grammarApplied       bool
	err                  error
	gotNotes, gotLocale  string
}

func (f *fakeItemStores) SetItemTranslationWithNotes(_ context.Context, resourceType string, listID int64, position int, phrase, locale, translation, notes string) (bool, error) {
	f.calls = append(f.calls, "translation:"+resourceType+":"+translation)
	f.gotNotes, f.gotLocale = notes, locale
	return f.translationApplied, f.err
}

func (f *fakeItemStores) SetItemGrammarIfBlank(_ context.Context, listID int64, position int, phrase, grammar string) (bool, error) {
	f.calls = append(f.calls, "grammar:"+grammar)
	return f.grammarApplied, f.err
}

func (f *fakeItemStores) SetItemTranscriptionIfBlank(_ context.Context, listID int64, position int, phrase, transcription string) (bool, error) {
	f.calls = append(f.calls, "transcription:"+transcription)
	return f.transcriptionApplied, f.err
}

func TestApplyItemWritesVocabulary(t *testing.T) {
	f := &fakeItemStores{translationApplied: true, grammarApplied: true, transcriptionApplied: false}
	svc := &Service{itemReplies: f, vocabItems: f}
	applied, err := svc.applyItemWrites(context.Background(), vocabPayload("名字"), itemWrites{Translation: "imię", Transcription: "míngzi", Grammar: "N", Notes: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, ",") != "translation:vocabulary_item:imię,transcription:míngzi,grammar:N" {
		t.Errorf("calls = %v, want translation, then transcription, then grammar", f.calls)
	}
	if f.gotNotes != "x" || f.gotLocale != "pl" {
		t.Errorf("notes/locale = %q/%q, want x/pl", f.gotNotes, f.gotLocale)
	}
	if !applied["translation"] || applied["transcription"] || !applied["grammar"] {
		t.Errorf("applied = %v, want translation+grammar true, transcription false (already set)", applied)
	}
}

func TestApplyItemWritesSkipsBlankFields(t *testing.T) {
	f := &fakeItemStores{translationApplied: true}
	svc := &Service{itemReplies: f, modelsItems: f}
	p := ItemJobPayload{ResourceType: resourceModelsItem, ListID: 1, Language: "deu", Locale: "en", Phrase: "Ich heiße…"}
	applied, err := svc.applyItemWrites(context.Background(), p, itemWrites{Translation: "My name is…"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Errorf("calls = %v, want only the translation write", f.calls)
	}
	if _, ok := applied["grammar"]; ok {
		t.Errorf("applied = %v, want no grammar key for a models item", applied)
	}
}

func TestApplyItemWritesStoreErrorFailsJob(t *testing.T) {
	f := &fakeItemStores{err: errors.New("vocabulary item not found")}
	svc := &Service{itemReplies: f, vocabItems: f}
	if _, err := svc.applyItemWrites(context.Background(), vocabPayload("der Koffer"), itemWrites{Translation: "walizka", Grammar: "N m"}); err == nil {
		t.Fatal("err = nil, want the store's phrase-guard error")
	}
	if len(f.calls) != 1 {
		t.Errorf("calls = %v, want to stop after the failed translation write", f.calls)
	}
}

func TestItemJobPayloadValidate(t *testing.T) {
	good := vocabPayload("der Koffer")
	if err := good.validate(); err != nil {
		t.Errorf("valid payload rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ItemJobPayload){
		"resource type": func(p *ItemJobPayload) { p.ResourceType = "text" },
		"locale":        func(p *ItemJobPayload) { p.Locale = "de" },
		"phrase":        func(p *ItemJobPayload) { p.Phrase = " " },
		"list id":       func(p *ItemJobPayload) { p.ListID = 0 },
	} {
		p := good
		mutate(&p)
		if err := p.validate(); err == nil {
			t.Errorf("%s: invalid payload accepted", name)
		}
	}
}

func TestLocalePromptName(t *testing.T) {
	if localePromptName("pl") != "Polish" || localePromptName("en") != "English" {
		t.Errorf("pl/en = %q/%q, want Polish/English", localePromptName("pl"), localePromptName("en"))
	}
}
