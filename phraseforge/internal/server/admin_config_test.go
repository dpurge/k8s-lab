package server

import (
	"encoding/json"
	"slices"
	"testing"

	"phraseforge/internal/ai"
	"phraseforge/internal/i18n"
)

// TestAdminConfigLanguageSectionsAbsentVsEmpty: an export written before
// language_sections existed must decode to nil (import leaves stored
// sections alone), while an explicit [] decodes to empty non-nil (import
// clears them) — apiImportAdminConfig branches on exactly this.
func TestAdminConfigLanguageSectionsAbsentVsEmpty(t *testing.T) {
	var old adminConfigExport
	if err := json.Unmarshal([]byte(`{"version":1,"ime_configs":[],"llm_prompts":[]}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.LanguageSections != nil {
		t.Errorf("absent language_sections = %#v, want nil", old.LanguageSections)
	}

	var cleared adminConfigExport
	if err := json.Unmarshal([]byte(`{"version":1,"language_sections":[]}`), &cleared); err != nil {
		t.Fatal(err)
	}
	if cleared.LanguageSections == nil || len(cleared.LanguageSections) != 0 {
		t.Errorf("explicit [] language_sections = %#v, want empty non-nil", cleared.LanguageSections)
	}

	var withRow adminConfigExport
	if err := json.Unmarshal([]byte(`{"version":1,"language_sections":[{"language":"cmn","grammar_prompt":"N = noun","transcription_prompt":"Transcribe using pinyin."}]}`), &withRow); err != nil {
		t.Fatal(err)
	}
	if len(withRow.LanguageSections) != 1 || withRow.LanguageSections[0].TranscriptionPrompt != "Transcribe using pinyin." {
		t.Errorf("language_sections = %#v, want the cmn row", withRow.LanguageSections)
	}
}

// TestEveryLLMKindHasAdminLabel: the Admin > LLM kind <select> is built
// from ai.ValidKinds, so every kind needs its label in the admin page's
// i18n key list and translated in every site locale — otherwise the UI
// shows the raw key (as it did for generate_vocabulary/generate_models).
func TestEveryLLMKindHasAdminLabel(t *testing.T) {
	for _, kind := range ai.ValidKinds {
		key := "admin.llm_kind_" + kind
		if !slices.Contains(adminAppI18nKeys, key) {
			t.Errorf("%s missing from adminAppI18nKeys", key)
		}
		for _, l := range i18n.Locales {
			if !i18n.Has(l.Code, key) {
				t.Errorf("%s not translated in locale %q", key, l.Code)
			}
		}
	}
}
