package ai

import (
	"encoding/json"
	"slices"
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
	j, err := NewItemJob(resourceModelsItem, 3, 0, "cmn", "pl", "我叫…")
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
	if p != (ItemJobPayload{ResourceType: resourceModelsItem, ListID: 3, Position: 0, Language: "cmn", Locale: "pl", Phrase: "我叫…"}) {
		t.Errorf("payload = %+v", p)
	}
	if j, _ := NewItemJob(resourceVocabularyItem, 3, 1, "deu", "en", "der Koffer"); j.Kind != KindGenerateVocabularyItem {
		t.Errorf("vocabulary kind = %q", j.Kind)
	}
	if _, err := NewItemJob(resourceVocabularyItem, 3, 1, "deu", "xx", "der Koffer"); err == nil {
		t.Error("invalid locale accepted")
	}
}
