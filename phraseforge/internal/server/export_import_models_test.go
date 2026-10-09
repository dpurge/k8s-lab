package server

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestModelsItemsUnchangedIdentical covers the fully-matching case.
func TestModelsItemsUnchangedIdentical(t *testing.T) {
	existing := []modelsItemFields{
		{Phrase: "p1", Transcription: "t1", Translations: map[string]string{"en": "e1"}},
		{Phrase: "p2"},
	}
	incoming := []modelsItemFields{existing[0], existing[1]}
	if !modelsItemsUnchanged(existing, incoming) {
		t.Errorf("modelsItemsUnchanged(identical) = false, want true")
	}
}

// TestModelsItemsUnchangedDetectsCountDifference covers that a different
// item count is always a change.
func TestModelsItemsUnchangedDetectsCountDifference(t *testing.T) {
	existing := []modelsItemFields{{Phrase: "p1"}, {Phrase: "p2"}}
	incoming := []modelsItemFields{{Phrase: "p1"}}
	if modelsItemsUnchanged(existing, incoming) {
		t.Errorf("modelsItemsUnchanged with differing item counts = true, want false")
	}
}

// TestModelsItemsUnchangedDetectsFieldDifference covers each primitive
// per-item field (phrase/transcription) individually.
func TestModelsItemsUnchangedDetectsFieldDifference(t *testing.T) {
	base := []modelsItemFields{{Phrase: "p", Transcription: "t"}}
	cases := []struct {
		name   string
		mutate func(f modelsItemFields) modelsItemFields
	}{
		{"phrase", func(f modelsItemFields) modelsItemFields { f.Phrase = "different"; return f }},
		{"transcription", func(f modelsItemFields) modelsItemFields { f.Transcription = "different"; return f }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			incoming := []modelsItemFields{c.mutate(base[0])}
			if modelsItemsUnchanged(base, incoming) {
				t.Errorf("modelsItemsUnchanged with %s changed = true, want false", c.name)
			}
		})
	}
}

// TestModelsItemsUnchangedIgnoresOmittedTranslationLocale covers that a
// locale the incoming item doesn't mention is never compared.
func TestModelsItemsUnchangedIgnoresOmittedTranslationLocale(t *testing.T) {
	existing := []modelsItemFields{{Phrase: "p", Translations: map[string]string{"en": "hello", "pl": "cześć"}}}
	incoming := []modelsItemFields{{Phrase: "p", Translations: map[string]string{"en": "hello"}}} // "pl" omitted
	if !modelsItemsUnchanged(existing, incoming) {
		t.Errorf("modelsItemsUnchanged with an omitted (not differing) locale = false, want true")
	}
}

// TestModelsItemsUnchangedDetectsProvidedTranslationDifference covers that a
// locale incoming does provide, with a different value than stored, is
// detected as changed.
func TestModelsItemsUnchangedDetectsProvidedTranslationDifference(t *testing.T) {
	existing := []modelsItemFields{{Translations: map[string]string{"en": "hello"}}}
	incoming := []modelsItemFields{{Translations: map[string]string{"en": "goodbye"}}}
	if modelsItemsUnchanged(existing, incoming) {
		t.Errorf("modelsItemsUnchanged with a differing provided locale = true, want false")
	}
}

// TestValidateModelsImportItemsRejectsBlankPhrase mirrors
// TestValidateVocabImportItemsRejectsBlankPhrase — see that test's doc
// comment.
func TestValidateModelsImportItemsRejectsBlankPhrase(t *testing.T) {
	items := []apiModelsImportItem{{Phrase: "ok"}, {Phrase: ""}}
	if err := validateModelsImportItems(items); err == nil {
		t.Fatal("validateModelsImportItems with a blank-phrase item = nil, want an error")
	}
}

// TestValidateModelsImportItemsAcceptsAllValid covers the no-op case.
func TestValidateModelsImportItemsAcceptsAllValid(t *testing.T) {
	items := []apiModelsImportItem{{Phrase: "one"}, {Phrase: "two"}}
	if err := validateModelsImportItems(items); err != nil {
		t.Errorf("validateModelsImportItems with all-valid items = %v, want nil", err)
	}
}

// TestToModelsTranslationFieldsEmptyIsNil mirrors
// TestToVocabTranslationFieldsEmptyIsNil.
func TestToModelsTranslationFieldsEmptyIsNil(t *testing.T) {
	if got := toModelsTranslationFields(nil); got != nil {
		t.Errorf("toModelsTranslationFields(nil) = %+v, want nil", got)
	}
	if got := toModelsTranslationFields(map[string]apiModelsImportItemTranslation{}); got != nil {
		t.Errorf("toModelsTranslationFields({}) = %+v, want nil", got)
	}
}

// TestToModelsTranslationFieldsConvertsProvidedEntries covers the normal
// conversion path.
func TestToModelsTranslationFieldsConvertsProvidedEntries(t *testing.T) {
	in := map[string]apiModelsImportItemTranslation{"en": {Translation: "hello"}}
	got := toModelsTranslationFields(in)
	if got["en"] != "hello" {
		t.Errorf("toModelsTranslationFields(%+v)[\"en\"] = %q, want %q", in, got["en"], "hello")
	}
}

// --- B1 regression coverage: mirrors export_import_vocabulary_test.go's own
// B1 tests — see that file's doc comments. ---

// TestModelsImportListItemsOmittedDecodesToNil mirrors
// TestVocabImportListItemsOmittedDecodesToNil.
func TestModelsImportListItemsOmittedDecodesToNil(t *testing.T) {
	var req apiModelsImportRequest
	yamlDoc := "lists:\n  - id: 1\n    title: Renamed Title\n"
	if err := yaml.Unmarshal([]byte(yamlDoc), &req); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if len(req.Lists) != 1 {
		t.Fatalf("len(req.Lists) = %d, want 1", len(req.Lists))
	}
	if req.Lists[0].Items != nil {
		t.Errorf("Items = %+v, want nil (items: key omitted entirely)", req.Lists[0].Items)
	}
}

// TestModelsImportListItemsExplicitEmptyDecodesToNonNil mirrors
// TestVocabImportListItemsExplicitEmptyDecodesToNonNil.
func TestModelsImportListItemsExplicitEmptyDecodesToNonNil(t *testing.T) {
	var req apiModelsImportRequest
	yamlDoc := "lists:\n  - id: 1\n    title: Renamed Title\n    items: []\n"
	if err := yaml.Unmarshal([]byte(yamlDoc), &req); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if len(req.Lists) != 1 {
		t.Fatalf("len(req.Lists) = %d, want 1", len(req.Lists))
	}
	if req.Lists[0].Items == nil {
		t.Fatal("Items = nil, want a non-nil pointer to an empty slice (items: [] is an explicit clear)")
	}
	if len(*req.Lists[0].Items) != 0 {
		t.Errorf("*Items = %+v, want an empty slice", *req.Lists[0].Items)
	}
}

// TestResolveModelsImportItemsOmitted mirrors TestResolveVocabImportItemsOmitted.
func TestResolveModelsImportItemsOmitted(t *testing.T) {
	items, provided := resolveModelsImportItems(apiModelsImportList{ID: 1, Title: "Renamed"})
	if provided {
		t.Error("provided = true, want false for an omitted items: key")
	}
	if items != nil {
		t.Errorf("items = %+v, want nil", items)
	}
}

// TestResolveModelsImportItemsExplicitEmpty mirrors
// TestResolveVocabImportItemsExplicitEmpty.
func TestResolveModelsImportItemsExplicitEmpty(t *testing.T) {
	empty := []apiModelsImportItem{}
	items, provided := resolveModelsImportItems(apiModelsImportList{ID: 1, Items: &empty})
	if !provided {
		t.Error("provided = false, want true for an explicit items: []")
	}
	if len(items) != 0 {
		t.Errorf("items = %+v, want empty", items)
	}
}

// TestModelsExportYAMLRoundTripsItemIDsIntoImport mirrors the vocabulary
// test: what an export writes, an import reads back into the same field, so
// an edited file's lines still name the phrases they edit.
func TestModelsExportYAMLRoundTripsItemIDsIntoImport(t *testing.T) {
	exported := apiModelsExportResponse{Lists: []apiModelsExportList{{
		ID: 7, Title: "T", Language: "arb", Script: "arab",
		Items: []apiModelsExportItem{
			{ID: 41, Phrase: "كيف حالك", Transcription: "kayfa haluk",
				Translations: map[string]apiModelsExportItemTranslation{"en": {Translation: "how are you"}}},
			{ID: 42, Phrase: "شكرا"},
		},
	}}}
	raw, err := yaml.Marshal(exported)
	if err != nil {
		t.Fatal(err)
	}
	var imported apiModelsImportRequest
	if err := yaml.Unmarshal(raw, &imported); err != nil {
		t.Fatalf("unmarshal export as import: %v\n%s", err, raw)
	}
	items := *imported.Lists[0].Items
	if len(items) != 2 || items[0].ID != 41 || items[1].ID != 42 {
		t.Fatalf("imported items = %+v, want ids 41 and 42 kept", items)
	}
	if items[0].Translations["en"].Translation != "how are you" {
		t.Fatalf("translations lost in the round trip: %+v", items[0].Translations)
	}
}

// TestModelsImportWithoutItemIDsDecodesToZero covers an old export or a
// hand-written file: no id means a new line, never an edit.
func TestModelsImportWithoutItemIDsDecodesToZero(t *testing.T) {
	var imported apiModelsImportRequest
	if err := yaml.Unmarshal([]byte("lists:\n  - id: 7\n    items:\n      - phrase: شكرا\n"), &imported); err != nil {
		t.Fatal(err)
	}
	if got := (*imported.Lists[0].Items)[0].ID; got != 0 {
		t.Fatalf("item id = %d, want 0 when the file has none", got)
	}
}
