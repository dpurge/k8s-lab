package ai

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"phraseforge/internal/i18n"
)

var testLocales = []i18n.Locale{{Code: "en"}, {Code: "pl"}}

func TestItemCallLocales(t *testing.T) {
	cases := []struct {
		name     string
		item     ItemState
		sections LanguageSections
		want     []string
	}{
		{"fresh item: every locale",
			ItemState{ResourceType: resourceVocabularyItem}, cmnSections, []string{"en", "pl"}},
		{"one locale missing: only that one",
			ItemState{ResourceType: resourceVocabularyItem, Translated: map[string]bool{"en": true}, Grammar: "N", Transcription: "x"}, cmnSections, []string{"pl"}},
		{"all translated, grammar blank with section: first locale",
			ItemState{ResourceType: resourceVocabularyItem, Translated: map[string]bool{"en": true, "pl": true}, Transcription: "yōngyǒu"}, cmnSections, []string{"en"}},
		{"all translated, grammar blank but no grammar section: none",
			ItemState{ResourceType: resourceVocabularyItem, Translated: map[string]bool{"en": true, "pl": true}, Transcription: "x"}, LanguageSections{TranscriptionPrompt: "pinyin"}, nil},
		{"all translated, transcription blank with section: first locale",
			ItemState{ResourceType: resourceModelsItem, Translated: map[string]bool{"en": true, "pl": true}}, cmnSections, []string{"en"}},
		{"models ignore grammar",
			ItemState{ResourceType: resourceModelsItem, Translated: map[string]bool{"en": true, "pl": true}, Transcription: "x"}, cmnSections, nil},
		{"complete item needing an example: first locale",
			ItemState{ResourceType: resourceVocabularyItem, Translated: map[string]bool{"en": true, "pl": true}, Grammar: "V", Transcription: "x", NeedsExample: true}, cmnSections, []string{"en"}},
		{"item missing a locale and needing an example: only the missing locale",
			ItemState{ResourceType: resourceVocabularyItem, Translated: map[string]bool{"en": true}, Grammar: "V", Transcription: "x", NeedsExample: true}, cmnSections, []string{"pl"}},
		{"complete item: none",
			ItemState{ResourceType: resourceVocabularyItem, Translated: map[string]bool{"en": true, "pl": true}, Grammar: "V", Transcription: "x"}, cmnSections, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ItemCallLocales(c.item, c.sections, testLocales); !slices.Equal(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestNewItemJobKindAndPayload(t *testing.T) {
	j, err := NewItemJob(resourceModelsItem, 3, "pl", "")
	if err != nil {
		t.Fatal(err)
	}
	if j.Kind != KindGenerateModelsItem {
		t.Errorf("kind = %q, want %q", j.Kind, KindGenerateModelsItem)
	}
	var p ItemJobPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p != (ItemJobPayload{ResourceType: resourceModelsItem, ItemID: 3, Locale: "pl"}) {
		t.Errorf("payload = %+v", p)
	}
	if string(j.Payload) != `{"resource_type":"models_item","item_id":3,"locale":"pl"}` {
		t.Errorf("payload JSON = %s, want only resource_type, item_id and locale", j.Payload)
	}
	if j, _ := NewItemJob(resourceVocabularyItem, 3, "en", ""); j.Kind != KindGenerateVocabularyItem {
		t.Errorf("vocabulary kind = %q", j.Kind)
	}
	if _, err := NewItemJob(resourceVocabularyItem, 3, "xx", ""); err == nil {
		t.Error("invalid locale accepted")
	}
	if _, err := NewItemJob(resourceVocabularyItem, 0, "en", ""); err == nil {
		t.Error("missing item id accepted")
	}
}

func TestNewItemJobCarriesTheUsageOfAVocabularyItemOnly(t *testing.T) {
	eighty := strings.Repeat("ك", MaxUsageChars)
	cases := []struct {
		name         string
		resourceType string
		usage        string
		want         string // the payload JSON
	}{
		{"vocabulary with a usage", resourceVocabularyItem, "كِتَابٌ جَدِيدٌ", `{"resource_type":"vocabulary_item","item_id":3,"locale":"pl","usage":"كِتَابٌ جَدِيدٌ"}`},
		{"no usage leaves the key out", resourceVocabularyItem, "", `{"resource_type":"vocabulary_item","item_id":3,"locale":"pl"}`},
		{"blank usage leaves the key out", resourceVocabularyItem, "  \t ", `{"resource_type":"vocabulary_item","item_id":3,"locale":"pl"}`},
		{"trimmed and NFC-normalised", resourceVocabularyItem, "  un cafe\u0301 noir ", `{"resource_type":"vocabulary_item","item_id":3,"locale":"pl","usage":"un café noir"}`},
		{"exactly the limit is kept", resourceVocabularyItem, eighty, `{"resource_type":"vocabulary_item","item_id":3,"locale":"pl","usage":"` + eighty + `"}`},
		{"over the limit is dropped, not an error", resourceVocabularyItem, strings.Repeat("x", MaxUsageChars+1), `{"resource_type":"vocabulary_item","item_id":3,"locale":"pl"}`},
		{"a models item never carries one", resourceModelsItem, "我叫…", `{"resource_type":"models_item","item_id":3,"locale":"pl"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j, err := NewItemJob(c.resourceType, 3, "pl", c.usage)
			if err != nil {
				t.Fatal(err)
			}
			if string(j.Payload) != c.want {
				t.Errorf("payload = %s, want %s", j.Payload, c.want)
			}
		})
	}
}

// The usage goes into every job of the item, one per locale it still needs.
func TestItemJobsForPutsTheUsageInEveryJobOfTheItem(t *testing.T) {
	svc := &Service{}
	jobs, err := svc.ItemJobsFor(context.Background(), ItemState{ResourceType: resourceVocabularyItem}, 5, "deu", "ein Koffer", &LanguageSections{}, testLocales)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("jobs = %d, want one per locale (2)", len(jobs))
	}
	for _, j := range jobs {
		var p ItemJobPayload
		if err := json.Unmarshal(j.Payload, &p); err != nil || p.Usage != "ein Koffer" || p.ItemID != 5 {
			t.Errorf("payload = %s (err %v), want item 5 with the usage", j.Payload, err)
		}
	}
	none, err := svc.ItemJobsFor(context.Background(), ItemState{ResourceType: resourceVocabularyItem}, 5, "deu", "", &LanguageSections{}, testLocales)
	if err != nil || len(none) != 2 || strings.Contains(string(none[0].Payload), "usage") {
		t.Errorf("jobs without a usage = %s (err %v), want no usage key", none, err)
	}
}

func TestItemCallLocalesWithNeedsSenseCoversEveryLocaleThePhraseHasATranslationIn(t *testing.T) {
	cases := []struct {
		name string
		item ItemState
		want []string
	}{
		{"all translated: a job per locale",
			ItemState{ResourceType: resourceVocabularyItem, Translated: map[string]bool{"en": true, "pl": true}, Grammar: "N", Transcription: "x", NeedsSense: true}, []string{"en", "pl"}},
		{"one missing: the missing one and the translated one",
			ItemState{ResourceType: resourceVocabularyItem, Translated: map[string]bool{"en": true}, Grammar: "N", Transcription: "x", NeedsSense: true}, []string{"en", "pl"}},
		{"both verdicts: still one job per locale, the first one serves both",
			ItemState{ResourceType: resourceVocabularyItem, Translated: map[string]bool{"en": true, "pl": true}, Grammar: "N", Transcription: "x", NeedsSense: true, NeedsExample: true}, []string{"en", "pl"}},
		{"needsExample alone is still only the first locale",
			ItemState{ResourceType: resourceVocabularyItem, Translated: map[string]bool{"en": true, "pl": true}, Grammar: "N", Transcription: "x", NeedsExample: true}, []string{"en"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ItemCallLocales(c.item, cmnSections, testLocales); !slices.Equal(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func jobPayload(t *testing.T, j ItemJob) ItemJobPayload {
	t.Helper()
	var p ItemJobPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestItemJobsForFlagsASenseOnlyForTheLocalesThatAlreadyHaveATranslation(t *testing.T) {
	svc := &Service{}
	item := ItemState{ResourceType: resourceVocabularyItem, Translated: map[string]bool{"pl": true}, Grammar: "N", Transcription: "x", NeedsSense: true}

	jobs, err := svc.ItemJobsFor(context.Background(), item, 5, "deu", "den Koffer packen", &LanguageSections{}, testLocales)
	if err != nil {
		t.Fatal(err)
	}

	flags := map[string]bool{}
	for _, j := range jobs {
		p := jobPayload(t, j)
		flags[p.Locale] = p.Sense
		if p.Usage != "den Koffer packen" {
			t.Errorf("job for %s has usage %q, want the usage carried", p.Locale, p.Usage)
		}
	}
	if want := map[string]bool{"en": false, "pl": true}; !reflect.DeepEqual(flags, want) {
		t.Errorf("sense flags by locale = %v, want %v (en has no translation yet, so its job is the usual one)", flags, want)
	}
}

func TestANewItemJobHasNoSenseFlagWithoutAUsageOrForAModelsItemAndOmitsItWhenFalse(t *testing.T) {
	noUsage, err := newItemJob(resourceVocabularyItem, 5, "pl", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if jobPayload(t, noUsage).Sense || strings.Contains(string(noUsage.Payload), "sense") {
		t.Errorf("payload = %s, want no sense flag without a usage", noUsage.Payload)
	}
	models, err := newItemJob(resourceModelsItem, 5, "pl", "u", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(models.Payload), "sense") {
		t.Errorf("payload = %s, want no sense flag for a models item", models.Payload)
	}
	plain, err := NewItemJob(resourceVocabularyItem, 5, "pl", "u")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain.Payload), "sense") {
		t.Errorf("payload = %s, want the flag omitted when false", plain.Payload)
	}
	flagged, err := newItemJob(resourceVocabularyItem, 5, "pl", "u", true)
	if err != nil {
		t.Fatal(err)
	}
	if !jobPayload(t, flagged).Sense || !strings.Contains(string(flagged.Payload), `"sense":true`) {
		t.Errorf("payload = %s, want sense true", flagged.Payload)
	}
}
