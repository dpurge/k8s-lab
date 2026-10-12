package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"phraseforge/internal/jobs"
)

var (
	deuSections = LanguageSections{Language: "deu", GrammarPrompt: "N = noun"}
	cmnSections = LanguageSections{Language: "cmn", GrammarPrompt: "N = noun", TranscriptionPrompt: "Transcribe using pinyin."}
)

func vocabPayload(phrase string) loadedItem {
	return loadedItem{
		ItemJobPayload: ItemJobPayload{ResourceType: resourceVocabularyItem, ItemID: 7, Locale: "pl"},
		ItemSource:     ItemSource{Phrase: phrase, Language: "deu"},
	}
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
	it := loadedItem{
		ItemJobPayload: ItemJobPayload{ResourceType: resourceModelsItem, ItemID: 1, Locale: "pl"},
		ItemSource:     ItemSource{Phrase: "我叫…", Language: "cmn"},
	}
	got, err := decideItemWrites(it, `{"phrase":"我叫…","transcription":"wǒ jiào…","translation":"Nazywam się…"}`, cmnSections)
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

type fakeItemStore struct {
	calls                []string
	translationApplied   bool
	transcriptionApplied bool
	grammarApplied       bool
	exampleApplied       bool
	err                  error
	exampleErr           error // returned by FillExample only
	rewriteApplied       bool
	senseApplied         bool
	senseErr             error  // returned by AppendSense only
	senseFrom            string // the phrase AppendSense was told the item had
	rewriteErr           error  // returned by RewritePhrase only
	rewriteFrom          string // the phrase RewritePhrase was told the item had
	source               ItemSource
	gotNotes, gotLocale  string
	gotItemID            int64
	gotPhrase            string
}

func (f *fakeItemStore) ItemSource(_ context.Context, resourceType string, itemID int64) (ItemSource, error) {
	f.calls = append(f.calls, "source:"+resourceType)
	f.gotItemID = itemID
	return f.source, f.err
}

func (f *fakeItemStore) FillTranslation(_ context.Context, resourceType string, itemID int64, phrase, locale, translation, notes string) (bool, error) {
	f.calls = append(f.calls, "translation:"+resourceType+":"+translation)
	f.gotItemID, f.gotPhrase, f.gotNotes, f.gotLocale = itemID, phrase, notes, locale
	return f.translationApplied, f.err
}

func (f *fakeItemStore) FillTranscription(_ context.Context, resourceType string, itemID int64, phrase, transcription string) (bool, error) {
	f.calls = append(f.calls, "transcription:"+transcription)
	return f.transcriptionApplied, f.err
}

func (f *fakeItemStore) FillExample(_ context.Context, itemID int64, phrase, example string) (bool, error) {
	f.calls = append(f.calls, "example:"+example)
	if f.exampleErr != nil {
		return false, f.exampleErr
	}
	return f.exampleApplied, f.err
}

func (f *fakeItemStore) RewritePhrase(_ context.Context, itemID int64, phrase, newPhrase, grammar, transcription string) (bool, error) {
	f.calls = append(f.calls, "rewrite:"+newPhrase+"|"+grammar+"|"+transcription)
	f.rewriteFrom = phrase
	if f.rewriteErr != nil {
		return false, f.rewriteErr
	}
	return f.rewriteApplied, f.err
}

func (f *fakeItemStore) AppendSense(_ context.Context, itemID int64, phrase, locale, sense string) (bool, error) {
	f.calls = append(f.calls, "sense:"+locale+":"+sense)
	f.senseFrom = phrase
	if f.senseErr != nil {
		return false, f.senseErr
	}
	return f.senseApplied, f.err
}

func (f *fakeItemStore) FillGrammar(_ context.Context, itemID int64, phrase, grammar string) (bool, error) {
	f.calls = append(f.calls, "grammar:"+grammar)
	return f.grammarApplied, f.err
}

func TestApplyItemWritesVocabulary(t *testing.T) {
	f := &fakeItemStore{translationApplied: true, grammarApplied: true, transcriptionApplied: false}
	svc := &Service{items: f}
	applied, err := svc.applyItemWrites(context.Background(), vocabPayload("名字"), itemWrites{Translation: "imię", Transcription: "míngzi", Grammar: "N", Notes: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, ",") != "translation:vocabulary_item:imię,transcription:míngzi,grammar:N" {
		t.Errorf("calls = %v, want translation, then transcription, then grammar", f.calls)
	}
	if f.gotNotes != "x" || f.gotLocale != "pl" || f.gotItemID != 7 || f.gotPhrase != "名字" {
		t.Errorf("notes/locale/item/phrase = %q/%q/%d/%q, want x/pl/7/名字 (the item id and the phrase the job started with)", f.gotNotes, f.gotLocale, f.gotItemID, f.gotPhrase)
	}
	if !applied["translation"] || applied["transcription"] || !applied["grammar"] {
		t.Errorf("applied = %v, want translation+grammar true, transcription false (already set)", applied)
	}
}

func TestApplyItemWritesSkipsBlankFields(t *testing.T) {
	f := &fakeItemStore{translationApplied: true}
	svc := &Service{items: f}
	it := loadedItem{
		ItemJobPayload: ItemJobPayload{ResourceType: resourceModelsItem, ItemID: 1, Locale: "en"},
		ItemSource:     ItemSource{Phrase: "Ich heiße…", Language: "deu"},
	}
	applied, err := svc.applyItemWrites(context.Background(), it, itemWrites{Translation: "My name is…"})
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
	f := &fakeItemStore{err: errors.New("connection reset")}
	svc := &Service{items: f}
	_, err := svc.applyItemWrites(context.Background(), vocabPayload("der Koffer"), itemWrites{Translation: "walizka", Grammar: "N m"})
	if err == nil || errors.Is(err, jobs.ErrSkipped) {
		t.Fatalf("err = %v, want the store's own error, not a skip", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("calls = %v, want to stop after the failed translation write", f.calls)
	}
}

// An item deleted or changed while the job ran skips it (cancelled), whichever
// write notices.
func TestApplyItemWritesGoneOrChangedItemSkipsJob(t *testing.T) {
	for name, storeErr := range map[string]error{"gone": ErrItemGone, "changed": ErrItemChanged} {
		f := &fakeItemStore{err: storeErr}
		svc := &Service{items: f}
		_, err := svc.applyItemWrites(context.Background(), vocabPayload("der Koffer"), itemWrites{Translation: "walizka"})
		if !errors.Is(err, jobs.ErrSkipped) {
			t.Errorf("%s: err = %v, want it to wrap jobs.ErrSkipped", name, err)
		}
	}
}

func TestHandleItemTranslationSkipsAnItemThatNoLongerExists(t *testing.T) {
	svc := &Service{items: &fakeItemStore{err: ErrItemGone}}
	_, err := svc.HandleItemTranslation(context.Background(), "job-1", json.RawMessage(`{"resource_type":"vocabulary_item","item_id":7,"locale":"pl"}`))
	if !errors.Is(err, jobs.ErrSkipped) || !strings.Contains(err.Error(), "item no longer exists") {
		t.Fatalf("err = %v, want a skip saying the item no longer exists", err)
	}
}

// A job queued before item ids existed carries list_id/position/phrase and no
// item_id; it fails with a message that says what is missing.
func TestHandleItemTranslationFailsAnOldStylePayload(t *testing.T) {
	f := &fakeItemStore{}
	svc := &Service{items: f}
	old := json.RawMessage(`{"resource_type":"vocabulary_item","list_id":32,"position":29,"language":"arb","locale":"pl","phrase":"وَحِيدًا"}`)
	_, err := svc.HandleItemTranslation(context.Background(), "job-1", old)
	if err == nil || errors.Is(err, jobs.ErrSkipped) || !strings.Contains(err.Error(), "item_id is required") {
		t.Fatalf("err = %v, want a failure saying item_id is required", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("calls = %v, want the store untouched", f.calls)
	}
}

func TestItemJobPayloadValidate(t *testing.T) {
	good := vocabPayload("der Koffer").ItemJobPayload
	if err := good.validate(); err != nil {
		t.Errorf("valid payload rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ItemJobPayload){
		"resource type": func(p *ItemJobPayload) { p.ResourceType = "text" },
		"locale":        func(p *ItemJobPayload) { p.Locale = "de" },
		"item id":       func(p *ItemJobPayload) { p.ItemID = 0 },
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

func TestDecideItemWritesVocabularyExample(t *testing.T) {
	eighty := strings.Repeat("ك", MaxUsageChars)
	cases := []struct {
		name    string
		example string // the reply's example field, raw JSON
		want    string
	}{
		{"usable", `"ein schwerer Koffer"`, "ein schwerer Koffer"},
		{"trimmed and NFC", `"  un cafe\u0301 noir "`, "un café noir"},
		{"exactly the limit", `"` + eighty + `"`, eighty},
		{"blank", `"   "`, ""},
		{"null", `null`, ""},
		{"over the limit means none, not an error", `"` + strings.Repeat("x", MaxUsageChars+1) + `"`, ""},
	}
	for _, c := range cases {
		raw := `{"phrase":"der Koffer","translation":"walizka","example":` + c.example + `}`
		got, err := decideItemWrites(vocabPayload("der Koffer"), raw, deuSections)
		if err != nil || got.Example != c.want {
			t.Errorf("%s: (%q, %v), want %q and no error", c.name, got.Example, err, c.want)
		}
	}
}

func TestDecideItemWritesModelsNeverCarryAnExample(t *testing.T) {
	it := loadedItem{
		ItemJobPayload: ItemJobPayload{ResourceType: resourceModelsItem, ItemID: 1, Locale: "pl"},
		ItemSource:     ItemSource{Phrase: "我叫…", Language: "cmn"},
	}
	got, err := decideItemWrites(it, `{"phrase":"我叫…","translation":"Nazywam się…","example":"我叫王明"}`, cmnSections)
	if err != nil || got.Example != "" {
		t.Errorf("(%+v, %v), want no example for a models item", got, err)
	}
}

func TestApplyItemWritesStoresTheExampleLastAndReportsIt(t *testing.T) {
	f := &fakeItemStore{translationApplied: true, grammarApplied: true, transcriptionApplied: true, exampleApplied: true}
	svc := &Service{items: f}
	applied, err := svc.applyItemWrites(context.Background(), vocabPayload("名字"), itemWrites{Translation: "imię", Transcription: "míngzi", Grammar: "N", Example: "他的名字"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, ",") != "translation:vocabulary_item:imię,transcription:míngzi,grammar:N,example:他的名字" {
		t.Errorf("calls = %v, want the example after the other writes", f.calls)
	}
	if !applied["example"] {
		t.Errorf("applied = %v, want example true", applied)
	}

	g := &fakeItemStore{translationApplied: true, exampleApplied: false} // the store found it a duplicate or full
	applied, err = (&Service{items: g}).applyItemWrites(context.Background(), vocabPayload("名字"), itemWrites{Translation: "imię", Example: "他的名字"})
	if err != nil || applied["example"] {
		t.Errorf("(%v, %v), want example false and no error when the store did not add it", applied, err)
	}
}

func TestApplyItemWritesWithoutAnExampleNeverCallsFillExample(t *testing.T) {
	f := &fakeItemStore{translationApplied: true}
	applied, err := (&Service{items: f}).applyItemWrites(context.Background(), vocabPayload("名字"), itemWrites{Translation: "imię"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, ",") != "translation:vocabulary_item:imię" || applied["example"] {
		t.Errorf("calls = %v, applied = %v, want only the translation write and example false", f.calls, applied)
	}
	m := loadedItem{
		ItemJobPayload: ItemJobPayload{ResourceType: resourceModelsItem, ItemID: 1, Locale: "pl"},
		ItemSource:     ItemSource{Phrase: "x", Language: "deu"},
	}
	applied, err = (&Service{items: &fakeItemStore{translationApplied: true}}).applyItemWrites(context.Background(), m, itemWrites{Translation: "y"})
	if _, has := applied["example"]; err != nil || has {
		t.Errorf("models applied = %v (err %v), want no example key", applied, err)
	}
}

func TestApplyItemWritesAnItemGoneAtTheExampleWriteSkipsTheJob(t *testing.T) {
	f := &fakeItemStore{translationApplied: true, exampleErr: ErrItemGone}
	_, err := (&Service{items: f}).applyItemWrites(context.Background(), vocabPayload("名字"), itemWrites{Translation: "imię", Example: "他的名字"})
	if !errors.Is(err, jobs.ErrSkipped) {
		t.Errorf("err = %v, want it to wrap jobs.ErrSkipped", err)
	}
}
