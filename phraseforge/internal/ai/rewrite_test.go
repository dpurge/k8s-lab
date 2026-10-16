package ai

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseVocabularyItemResponseReadsAnOptionalRewrite(t *testing.T) {
	cases := []struct {
		name    string
		rewrite string
		want    *PhraseRewrite
	}{
		{"absent", ``, nil},
		{"null", `,"rewrite":null`, nil},
		{"all three", `,"rewrite":{"phrase":" فِي ","grammar":"Prep","transcription":"fī"}`, &PhraseRewrite{Phrase: "فِي", Grammar: "Prep", Transcription: "fī"}},
		{"null grammar and transcription keep the stored ones", `,"rewrite":{"phrase":"فِي","grammar":null,"transcription":null}`, &PhraseRewrite{Phrase: "فِي"}},
		{"grammar and transcription absent", `,"rewrite":{"phrase":"فِي"}`, &PhraseRewrite{Phrase: "فِي"}},
		{"not an object", `,"rewrite":"فِي"`, nil},
		{"an array", `,"rewrite":["فِي"]`, nil},
		{"no phrase", `,"rewrite":{"grammar":"Prep"}`, nil},
		{"blank phrase", `,"rewrite":{"phrase":"  "}`, nil},
		{"phrase of the wrong type", `,"rewrite":{"phrase":5}`, nil},
		{"grammar of the wrong type", `,"rewrite":{"phrase":"فِي","grammar":["Prep"]}`, nil},
		{"transcription of the wrong type", `,"rewrite":{"phrase":"فِي","transcription":1}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := `{"phrase":"فِيهِ","translation":"w nim"` + c.rewrite + `}`
			got, err := parseVocabularyItemResponse(raw, "فِيهِ")
			if err != nil {
				t.Fatalf("err = %v, want none: a bad rewrite must not make the reply invalid", err)
			}
			if !reflect.DeepEqual(got.Rewrite, c.want) {
				t.Errorf("Rewrite = %+v, want %+v", got.Rewrite, c.want)
			}
		})
	}
}

func TestVocabularyItemSchemaDescribesTheRewrite(t *testing.T) {
	if !strings.Contains(string(VocabularyItemSchema), `"rewrite":{"type":["object","null"]`) {
		t.Errorf("VocabularyItemSchema = %s, want an optional rewrite object", VocabularyItemSchema)
	}
	if strings.Contains(string(ModelsItemSchema), "rewrite") {
		t.Errorf("ModelsItemSchema = %s, want no rewrite", ModelsItemSchema)
	}
}

func withUsage(it loadedItem, usage string) loadedItem {
	it.Usage = usage
	return it
}

const rewriteReply = `{"phrase":"Kinder","translation":"dzieci","rewrite":{"phrase":"das Kind","grammar":"N n","transcription":"kint"}}`

func TestDecideItemWritesKeepsARewriteOnlyForAJobWithAUsageAndADifferentPhrase(t *testing.T) {
	sections := LanguageSections{GrammarPrompt: "N = noun", TranscriptionPrompt: "latin"}
	want := &PhraseRewrite{Phrase: "das Kind", Grammar: "N n", Transcription: "kint"}

	got, err := decideItemWrites(withUsage(vocabPayload("Kinder"), "die Kinder spielen"), rewriteReply, sections)
	if err != nil || !reflect.DeepEqual(got.Rewrite, want) {
		t.Fatalf("with a usage: rewrite = %+v (err %v), want %+v", got.Rewrite, err, want)
	}
	if got, _ := decideItemWrites(vocabPayload("Kinder"), rewriteReply, sections); got.Rewrite != nil {
		t.Errorf("without a usage: rewrite = %+v, want none", got.Rewrite)
	}
	tooLong := strings.Repeat("x", MaxUsageChars+1)
	if got, _ := decideItemWrites(withUsage(vocabPayload("Kinder"), tooLong), rewriteReply, sections); got.Rewrite != nil {
		t.Errorf("with a usage that is dropped as too long: rewrite = %+v, want none", got.Rewrite)
	}
	same := `{"phrase":"Kinder","translation":"dzieci","rewrite":{"phrase":"  Kinder ","grammar":"N pl"}}`
	if got, _ := decideItemWrites(withUsage(vocabPayload("Kinder"), "u"), same, sections); got.Rewrite != nil {
		t.Errorf("unchanged phrase: rewrite = %+v, want none", got.Rewrite)
	}
	decomposed := `{"phrase":"Kinder","translation":"x","rewrite":{"phrase":"café"}}`
	if got, _ := decideItemWrites(withUsage(vocabPayload("café"), "u"), decomposed, sections); got.Rewrite != nil {
		t.Errorf("same phrase in another Unicode form: rewrite = %+v, want none", got.Rewrite)
	}
}

func TestDecideItemWritesDropsARewritesGrammarAndTranscriptionWhereTheLanguageHasNoSection(t *testing.T) {
	it := withUsage(vocabPayload("Kinder"), "die Kinder spielen")

	got, _ := decideItemWrites(it, rewriteReply, LanguageSections{GrammarPrompt: "N = noun"})
	if want := (&PhraseRewrite{Phrase: "das Kind", Grammar: "N n"}); !reflect.DeepEqual(got.Rewrite, want) {
		t.Errorf("no transcription section: rewrite = %+v, want %+v", got.Rewrite, want)
	}
	got, _ = decideItemWrites(it, rewriteReply, LanguageSections{})
	if want := (&PhraseRewrite{Phrase: "das Kind"}); !reflect.DeepEqual(got.Rewrite, want) {
		t.Errorf("no sections: rewrite = %+v, want %+v", got.Rewrite, want)
	}
}

func TestDecideItemWritesNeverRewritesAModelsItem(t *testing.T) {
	m := loadedItem{ItemJobPayload: ItemJobPayload{ResourceType: resourceModelsItem, ItemID: 7, Locale: "pl", Usage: "u"}, ItemSource: ItemSource{Phrase: "Kinder", Language: "deu"}}
	got, err := decideItemWrites(m, rewriteReply, LanguageSections{})
	if err != nil || got.Rewrite != nil {
		t.Fatalf("models item: rewrite = %+v (err %v), want none", got.Rewrite, err)
	}
}

func TestApplyItemWritesAppliesTheRewriteLastWithThePhraseTheJobStartedWith(t *testing.T) {
	f := &fakeItemStore{translationApplied: true, grammarApplied: true, exampleApplied: true, rewriteApplied: true}
	svc := &Service{items: f}
	w := itemWrites{Translation: "dzieci", Grammar: "N pl", Example: "die Kinder spielen", Rewrite: &PhraseRewrite{Phrase: "das Kind", Grammar: "N n", Transcription: "kint"}}

	applied, err := svc.applyItemWrites(context.Background(), vocabPayload("Kinder"), w)
	if err != nil {
		t.Fatal(err)
	}

	want := "translation:vocabulary_item:dzieci,grammar:N pl,example:die Kinder spielen,rewrite:das Kind|N n|kint"
	if got := strings.Join(f.calls, ","); got != want {
		t.Errorf("calls = %s, want %s (the rewrite last)", got, want)
	}
	if f.rewriteFrom != "Kinder" {
		t.Errorf("rewrite was told the item had %q, want the phrase the job started with", f.rewriteFrom)
	}
	if !applied["rewrite"] {
		t.Errorf("applied = %v, want rewrite recorded as applied", applied)
	}
}

func TestApplyItemWritesSurvivesARewriteThatIsSkippedOrFails(t *testing.T) {
	for name, err := range map[string]error{"item changed": ErrItemChanged, "item gone": ErrItemGone, "other error": errors.New("db down")} {
		t.Run(name, func(t *testing.T) {
			f := &fakeItemStore{translationApplied: true, rewriteErr: err}
			applied, got := (&Service{items: f}).applyItemWrites(context.Background(), vocabPayload("Kinder"), itemWrites{Translation: "dzieci", Rewrite: &PhraseRewrite{Phrase: "das Kind"}})
			if got != nil {
				t.Fatalf("err = %v, want the job to succeed", got)
			}
			if !applied["translation"] || applied["rewrite"] {
				t.Errorf("applied = %v, want the translation kept and the rewrite not applied", applied)
			}
		})
	}
}

func TestApplyItemWritesWithoutARewriteMakesNoRewriteCallAndReportsItNotApplied(t *testing.T) {
	f := &fakeItemStore{translationApplied: true}
	applied, err := (&Service{items: f}).applyItemWrites(context.Background(), vocabPayload("Kinder"), itemWrites{Translation: "dzieci"})
	if err != nil || strings.Contains(strings.Join(f.calls, ","), "rewrite") {
		t.Fatalf("calls = %v (err %v), want no rewrite call", f.calls, err)
	}
	if applied["rewrite"] {
		t.Errorf("applied = %v, want rewrite false", applied)
	}
	m := loadedItem{ItemJobPayload: ItemJobPayload{ResourceType: resourceModelsItem, ItemID: 7, Locale: "pl"}, ItemSource: ItemSource{Phrase: "x"}}
	applied, _ = (&Service{items: &fakeItemStore{translationApplied: true}}).applyItemWrites(context.Background(), m, itemWrites{Translation: "y"})
	if _, has := applied["rewrite"]; has {
		t.Errorf("models item applied = %v, want no rewrite key", applied)
	}
}

func TestItemJobRewritesThePhraseAfterItsOtherWritesOnlyWhenItCarriesAUsage(t *testing.T) {
	source := ItemSource{Phrase: "Kinder", Language: "deu"}
	reply := `{"phrase":"Kinder","translation":"dzieci","rewrite":{"phrase":"das Kind"}}`

	withUsagePayload := `{"resource_type":"vocabulary_item","item_id":7,"locale":"pl","usage":"die Kinder spielen"}`
	store, _, _, err := runItemJob(t, itemJobTemplate, source, withUsagePayload, reply)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(store.calls); n < 3 || !strings.HasPrefix(store.calls[n-1], "rewrite:das Kind|") || store.rewriteFrom != "Kinder" {
		t.Errorf("calls = %v (rewrite from %q), want the rewrite last, from the stored phrase", store.calls, store.rewriteFrom)
	}

	noUsagePayload := `{"resource_type":"vocabulary_item","item_id":7,"locale":"pl"}`
	store, _, _, err = runItemJob(t, itemJobTemplate, source, noUsagePayload, reply)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(store.calls, ","), "rewrite:") {
		t.Errorf("calls = %v, want no rewrite for a job without a usage", store.calls)
	}
}

func TestDecideItemWritesTakesAChangedPhraseAsTheRewriteForAJobWithAUsage(t *testing.T) {
	sections := LanguageSections{GrammarPrompt: "N = noun", TranscriptionPrompt: "latin"}
	changed := `{"phrase":"مُلَاحَظَاتٌ","grammar":"N f pl","transcription":"mulāḥaẓāt","translation":"notatki"}`
	it := withUsage(vocabPayload("مُلَاحَظَاتٍ"), "دوّن لنا ملاحظات")

	got, err := decideItemWrites(it, changed, sections)
	want := &PhraseRewrite{Phrase: "مُلَاحَظَاتٌ", Grammar: "N f pl", Transcription: "mulāḥaẓāt"}
	if err != nil || !reflect.DeepEqual(got.Rewrite, want) || got.Translation != "notatki" {
		t.Fatalf("changed phrase: writes = %+v (err %v), want rewrite %+v and the translation", got, err, want)
	}

	explicit := `{"phrase":"مُلَاحَظَاتٌ","translation":"notatki","rewrite":{"phrase":"مُلَاحَظَةٌ","grammar":"N f sg"}}`
	got, err = decideItemWrites(it, explicit, sections)
	if err != nil || got.Rewrite == nil || got.Rewrite.Phrase != "مُلَاحَظَةٌ" {
		t.Fatalf("explicit rewrite beside a changed phrase: rewrite = %+v (err %v), want the explicit one", got.Rewrite, err)
	}
}

func TestDecideItemWritesRejectsAChangedPhraseForAJobWithoutAUsageAndSaysWhy(t *testing.T) {
	_, err := decideItemWrites(vocabPayload("Koffer"), `{"phrase":"der Koffer","translation":"walizka"}`, LanguageSections{})
	if err == nil || !strings.Contains(err.Error(), `does not match the item's phrase "Koffer"`) || !strings.Contains(err.Error(), "may not change the phrase") {
		t.Fatalf("err = %v, want the mismatch plus why this job may not change the phrase", err)
	}
}
