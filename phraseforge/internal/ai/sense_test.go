package ai

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseVocabularyItemResponseReadsAnOptionalNewSense(t *testing.T) {
	for name, c := range map[string]struct{ field, want string }{
		"absent":                   {``, ""},
		"null":                     {`,"newSense":null`, ""},
		"a sense":                  {`,"newSense":"  uczelnia "`, "uczelnia"},
		"blank":                    {`,"newSense":"   "`, ""},
		"a number":                 {`,"newSense":5`, ""},
		"an array":                 {`,"newSense":["uczelnia"]`, ""},
		"an object":                {`,"newSense":{"sense":"uczelnia"}`, ""},
		"a boolean":                {`,"newSense":true`, ""},
		"with the other optionals": {`,"example":null,"rewrite":null,"newSense":"uczelnia"`, "uczelnia"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseVocabularyItemResponse(`{"phrase":"Schule","translation":"szkoła"`+c.field+`}`, "Schule")
			if err != nil {
				t.Fatalf("err = %v, want none: a bad newSense must not make the reply invalid", err)
			}
			if got.NewSense != c.want {
				t.Errorf("NewSense = %q, want %q", got.NewSense, c.want)
			}
		})
	}
}

func TestVocabularyItemSchemaDescribesNewSense(t *testing.T) {
	if !strings.Contains(string(VocabularyItemSchema), `"newSense":{"type":["string","null"]}`) {
		t.Errorf("VocabularyItemSchema = %s, want an optional newSense", VocabularyItemSchema)
	}
	if strings.Contains(string(ModelsItemSchema), "newSense") {
		t.Errorf("ModelsItemSchema = %s, want no newSense", ModelsItemSchema)
	}
}

func TestRenderItemPromptFillsTranslationAndLeavesATemplateWithoutItAlone(t *testing.T) {
	v := ItemPromptVars{SourceLanguage: "German", TargetLanguage: "Polish", Phrase: "Schule", Translation: "szkoła; nauka"}
	if got, want := renderItemPrompt("{{phrase}} is now: {{translation}}.", v), "Schule is now: szkoła; nauka."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := renderItemPrompt("[{{translation}}]", ItemPromptVars{}); got != "[]" {
		t.Errorf("without a translation = %q, want it empty", got)
	}
	const legacy = "Translate {{phrase}} to {{targetLanguage}}."
	with := v
	without := v
	without.Translation = ""
	if renderItemPrompt(legacy, with) != renderItemPrompt(legacy, without) {
		t.Error("a template without {{translation}} changed when a translation was given")
	}
	if got := renderItemPrompt("{{translation}}", ItemPromptVars{Translation: "{{phrase}}"}); got != "{{phrase}}" {
		t.Errorf("got %q, want braces inside the value left as written", got)
	}
}

func senseJob(phrase string, sense bool) loadedItem {
	it := withUsage(vocabPayload(phrase), "den Koffer packen")
	it.Sense = sense
	return it
}

const senseReply = `{"phrase":"Schule","translation":"szkoła","newSense":"uczelnia"}`

func TestDecideItemWritesKeepsANewSenseOnlyForAJobWithTheSenseFlag(t *testing.T) {
	got, err := decideItemWrites(senseJob("Schule", true), senseReply, LanguageSections{})
	if err != nil || got.Sense != "uczelnia" {
		t.Fatalf("flagged job: sense = %q (err %v), want uczelnia", got.Sense, err)
	}
	if got, _ := decideItemWrites(senseJob("Schule", false), senseReply, LanguageSections{}); got.Sense != "" {
		t.Errorf("job without the flag: sense = %q, want none even though the reply has one", got.Sense)
	}
	if got, _ := decideItemWrites(senseJob("Schule", true), `{"phrase":"Schule","translation":"szkoła","newSense":null}`, LanguageSections{}); got.Sense != "" {
		t.Errorf("null newSense: sense = %q, want none", got.Sense)
	}
	m := loadedItem{ItemJobPayload: ItemJobPayload{ResourceType: resourceModelsItem, ItemID: 7, Locale: "pl", Usage: "u", Sense: true}, ItemSource: ItemSource{Phrase: "Schule", Language: "deu"}}
	if got, err := decideItemWrites(m, senseReply, LanguageSections{}); err != nil || got.Sense != "" {
		t.Errorf("models item: sense = %q (err %v), want none", got.Sense, err)
	}
}

func TestApplyItemWritesAppendsTheSenseAfterTheExampleAndBeforeTheRewrite(t *testing.T) {
	f := &fakeItemStore{translationApplied: true, exampleApplied: true, senseApplied: true, rewriteApplied: true}
	svc := &Service{items: f}
	w := itemWrites{Translation: "szkoła", Example: "die Schule", Sense: "uczelnia", Rewrite: &PhraseRewrite{Phrase: "die Schule"}}

	applied, err := svc.applyItemWrites(context.Background(), senseJob("Schule", true), w)
	if err != nil {
		t.Fatal(err)
	}

	want := "translation:vocabulary_item:szkoła,example:die Schule,sense:pl:uczelnia,rewrite:die Schule||"
	if got := strings.Join(f.calls, ","); got != want {
		t.Errorf("calls = %s, want %s", got, want)
	}
	if f.senseFrom != "Schule" {
		t.Errorf("sense was told the item had %q, want the phrase the job started with", f.senseFrom)
	}
	if !applied["sense"] || !applied["rewrite"] {
		t.Errorf("applied = %v, want sense and rewrite recorded", applied)
	}
}

func TestApplyItemWritesSurvivesASenseThatIsSkippedOrFails(t *testing.T) {
	for name, err := range map[string]error{"item changed": ErrItemChanged, "item gone": ErrItemGone, "other error": errors.New("db down")} {
		t.Run(name, func(t *testing.T) {
			f := &fakeItemStore{translationApplied: true, senseErr: err}
			applied, got := (&Service{items: f}).applyItemWrites(context.Background(), senseJob("Schule", true), itemWrites{Translation: "szkoła", Sense: "uczelnia"})
			if got != nil {
				t.Fatalf("err = %v, want the job to succeed", got)
			}
			if !applied["translation"] || applied["sense"] {
				t.Errorf("applied = %v, want the translation kept and the sense not applied", applied)
			}
		})
	}
}

func TestApplyItemWritesWithoutASenseMakesNoAppendCallAndModelsHaveNoSenseKey(t *testing.T) {
	f := &fakeItemStore{translationApplied: true}
	applied, err := (&Service{items: f}).applyItemWrites(context.Background(), senseJob("Schule", true), itemWrites{Translation: "szkoła"})
	if err != nil || strings.Contains(strings.Join(f.calls, ","), "sense:") || applied["sense"] {
		t.Fatalf("calls = %v applied = %v (err %v), want no append call and sense false", f.calls, applied, err)
	}
	m := loadedItem{ItemJobPayload: ItemJobPayload{ResourceType: resourceModelsItem, ItemID: 7, Locale: "pl"}, ItemSource: ItemSource{Phrase: "x"}}
	applied, _ = (&Service{items: &fakeItemStore{translationApplied: true}}).applyItemWrites(context.Background(), m, itemWrites{Translation: "y"})
	if _, has := applied["sense"]; has {
		t.Errorf("models item applied = %v, want no sense key", applied)
	}
}

func TestItemJobShowsTheModelTheExistingTranslationAndAppendsASenseOnlyWithTheFlag(t *testing.T) {
	source := ItemSource{Phrase: "Schule", Language: "deu", Translations: map[string]string{"pl": "szkoła; nauka", "en": "school"}}
	template := "Now: {{translation}}|{{targetLanguage}}"

	flagged := `{"resource_type":"vocabulary_item","item_id":7,"locale":"pl","usage":"die Schule ist alt","sense":true}`
	store, ollama, _, err := runItemJob(t, template, source, flagged, senseReply)
	if err != nil {
		t.Fatal(err)
	}
	if len(ollama.lastText) != 1 || ollama.lastText[0] != "Now: szkoła; nauka|Polish" {
		t.Errorf("the model was sent %q, want the translation of the job's locale only", ollama.lastText)
	}
	if got := strings.Join(store.calls, ","); !strings.Contains(got, "sense:pl:uczelnia") {
		t.Errorf("calls = %s, want the sense appended in the job's locale", got)
	}

	plain := `{"resource_type":"vocabulary_item","item_id":7,"locale":"pl","usage":"die Schule ist alt"}`
	store, _, _, err = runItemJob(t, template, source, plain, senseReply)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(store.calls, ","), "sense:") {
		t.Errorf("calls = %v, want no append for a job without the flag", store.calls)
	}
	if !reflect.DeepEqual(source.Translations["en"], "school") {
		t.Error("source translations changed")
	}
}

func TestSenseFromTranslation(t *testing.T) {
	cases := []struct {
		name, existing, reply, want string
	}{
		{"the new sense after the old one", "ławka", "ławka; bank", "bank"},
		{"the new sense before the old one", "ławka", "bank; ławka", "bank"},
		{"several new senses, in the reply's order", "ławka", "ławka; bank; ława", "bank; ława"},
		{"an old sense among several", "ławka; ława", "ławka; bank; ława", "bank"},
		{"nothing new", "ławka", "ławka", ""},
		{"same senses in another case and Unicode form", "Café", "café; CAFÉ", ""},
		{"a repeat of the new sense is added once", "ławka", "ławka; bank; Bank", "bank"},
		{"the reply dropped an existing sense: not reliable", "ławka; ława", "ławka; bank", ""},
		{"the reply re-translated the phrase: not reliable", "ławka", "bank", ""},
		{"no existing translation to add to", "", "bank; ławka", ""},
		{"a blank reply", "ławka", "  ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := senseFromTranslation(c.existing, c.reply); got != c.want {
				t.Errorf("senseFromTranslation(%q, %q) = %q, want %q", c.existing, c.reply, got, c.want)
			}
		})
	}
}

func TestDecideItemWritesRecoversASenseFromATranslationThatKeptTheExistingOne(t *testing.T) {
	it := senseJob("Bank", true)
	it.Translations = map[string]string{"pl": "ławka", "en": "bench"}
	reply := func(translation, newSense string) string {
		return `{"phrase":"Bank","translation":"` + translation + `","newSense":` + newSense + `}`
	}

	if got, _ := decideItemWrites(it, reply("bank; ławka", "null"), LanguageSections{}); got.Sense != "bank" {
		t.Errorf("sense = %q, want bank recovered from the translation", got.Sense)
	}
	if got, _ := decideItemWrites(it, reply("bank; ławka", `"kasa"`), LanguageSections{}); got.Sense != "kasa" {
		t.Errorf("sense = %q, want the explicit newSense to win", got.Sense)
	}
	if got, _ := decideItemWrites(it, reply("ławka", "null"), LanguageSections{}); got.Sense != "" {
		t.Errorf("sense = %q, want none when the translation has nothing new", got.Sense)
	}
	if got, _ := decideItemWrites(it, reply("bench; bank", "null"), LanguageSections{}); got.Sense != "" {
		t.Errorf("sense = %q, want none: only the job's own locale (pl) counts, and this reply dropped ławka", got.Sense)
	}
	plain := senseJob("Bank", false)
	plain.Translations = it.Translations
	if got, _ := decideItemWrites(plain, reply("bank; ławka", "null"), LanguageSections{}); got.Sense != "" {
		t.Errorf("sense = %q, want none for a job without the flag", got.Sense)
	}
	noTranslation := senseJob("Bank", true)
	if got, _ := decideItemWrites(noTranslation, reply("bank; ławka", "null"), LanguageSections{}); got.Sense != "" {
		t.Errorf("sense = %q, want none when the job's locale has no translation yet", got.Sense)
	}
}

func TestItemJobAppendsTheSenseTheModelPutIntoTranslation(t *testing.T) {
	source := ItemSource{Phrase: "Bank", Language: "deu", Translations: map[string]string{"pl": "ławka"}}
	payload := `{"resource_type":"vocabulary_item","item_id":7,"locale":"pl","usage":"Geld auf der Bank einzahlen","sense":true}`

	store, _, _, err := runItemJob(t, itemJobTemplate, source, payload, `{"phrase":"Bank","translation":"bank; ławka","notes":"finanse; mebel","newSense":null}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(store.calls, ","); !strings.Contains(got, "sense:pl:bank") {
		t.Errorf("calls = %s, want the sense bank appended in pl", got)
	}
}
