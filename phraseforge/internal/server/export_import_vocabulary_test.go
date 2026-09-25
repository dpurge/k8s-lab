package server

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestListMetaUnchangedAllProvidedMatch covers the fully-matching case with
// every optional field actually provided — listMetaUnchanged must report
// true so apiImportVocabulary/apiImportModels skip the write.
func TestListMetaUnchangedAllProvidedMatch(t *testing.T) {
	existing := listMetaFields{Title: "Title", Language: "ron", Script: "latn", Tags: []string{"a", "b"}}
	incoming := existing
	if !listMetaUnchanged(existing, incoming) {
		t.Errorf("listMetaUnchanged(%+v, %+v) = false, want true", existing, incoming)
	}
}

// TestListMetaUnchangedOmittedFieldsNeverCountAsChanged covers this type's
// central difference from importFields/importUnchanged: a blank
// Title/Language/Script, or an empty Tags, means "the import omitted this
// field", never "the import wants to clear it" — so none of them alone may
// ever make an otherwise-identical list look changed.
func TestListMetaUnchangedOmittedFieldsNeverCountAsChanged(t *testing.T) {
	existing := listMetaFields{Title: "Title", Language: "ron", Script: "latn", Tags: []string{"a", "b"}}
	incoming := listMetaFields{} // every field omitted
	if !listMetaUnchanged(existing, incoming) {
		t.Errorf("listMetaUnchanged with every field omitted = false, want true")
	}
}

// TestListMetaUnchangedDetectsEachProvidedFieldDifference covers that a
// difference in any single provided field is enough to report changed.
func TestListMetaUnchangedDetectsEachProvidedFieldDifference(t *testing.T) {
	base := listMetaFields{Title: "T", Language: "ron", Script: "latn", Tags: []string{"a"}}
	cases := []struct {
		name   string
		mutate func(f listMetaFields) listMetaFields
	}{
		{"title", func(f listMetaFields) listMetaFields { f.Title = "different"; return f }},
		{"language", func(f listMetaFields) listMetaFields { f.Language = "fra"; return f }},
		{"script", func(f listMetaFields) listMetaFields { f.Script = "cyrl"; return f }},
		{"tags", func(f listMetaFields) listMetaFields { f.Tags = []string{"different"}; return f }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			incoming := c.mutate(base)
			if listMetaUnchanged(base, incoming) {
				t.Errorf("listMetaUnchanged with %s changed = true, want false", c.name)
			}
		})
	}
}

// TestListMetaUnchangedEmptyIncomingTagsNeverClearsExisting covers that an
// incoming Tags of length zero is treated as "tags omitted" (per
// apiImportVocabulary's "tags.SetFor if tags given" rule), not as "clear all
// tags" — a deliberate difference from importFields/importUnchanged.
func TestListMetaUnchangedEmptyIncomingTagsNeverClearsExisting(t *testing.T) {
	existing := listMetaFields{Tags: []string{"a", "b"}}
	incoming := listMetaFields{Tags: nil}
	if !listMetaUnchanged(existing, incoming) {
		t.Errorf("listMetaUnchanged with omitted tags = false, want true")
	}
}

// TestVocabItemsUnchangedIdentical covers the fully-matching case.
func TestVocabItemsUnchangedIdentical(t *testing.T) {
	existing := []vocabItemFields{
		{Phrase: "p1", Grammar: "g1", Transcription: "t1", Translations: map[string]vocabTranslationFields{"en": {Translation: "e1", Notes: "n1"}}},
		{Phrase: "p2"},
	}
	incoming := []vocabItemFields{existing[0], existing[1]}
	if !vocabItemsUnchanged(existing, incoming) {
		t.Errorf("vocabItemsUnchanged(identical) = false, want true")
	}
}

// TestVocabItemsUnchangedDetectsCountDifference covers that a different
// item count is always a change, regardless of any per-item comparison.
func TestVocabItemsUnchangedDetectsCountDifference(t *testing.T) {
	existing := []vocabItemFields{{Phrase: "p1"}, {Phrase: "p2"}}
	incoming := []vocabItemFields{{Phrase: "p1"}}
	if vocabItemsUnchanged(existing, incoming) {
		t.Errorf("vocabItemsUnchanged with differing item counts = true, want false")
	}
}

// TestVocabItemsUnchangedDetectsFieldDifference covers each primitive
// per-item field (phrase/grammar/transcription) individually.
func TestVocabItemsUnchangedDetectsFieldDifference(t *testing.T) {
	base := []vocabItemFields{{Phrase: "p", Grammar: "g", Transcription: "t"}}
	cases := []struct {
		name   string
		mutate func(f vocabItemFields) vocabItemFields
	}{
		{"phrase", func(f vocabItemFields) vocabItemFields { f.Phrase = "different"; return f }},
		{"grammar", func(f vocabItemFields) vocabItemFields { f.Grammar = "different"; return f }},
		{"transcription", func(f vocabItemFields) vocabItemFields { f.Transcription = "different"; return f }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			incoming := []vocabItemFields{c.mutate(base[0])}
			if vocabItemsUnchanged(base, incoming) {
				t.Errorf("vocabItemsUnchanged with %s changed = true, want false", c.name)
			}
		})
	}
}

// TestVocabItemsUnchangedIgnoresOmittedTranslationLocale covers that a
// locale the incoming item doesn't mention is never compared, mirroring
// importUnchanged's own convention for text/dialog translations.
func TestVocabItemsUnchangedIgnoresOmittedTranslationLocale(t *testing.T) {
	existing := []vocabItemFields{{Phrase: "p", Translations: map[string]vocabTranslationFields{
		"en": {Translation: "hello"}, "pl": {Translation: "cześć"},
	}}}
	incoming := []vocabItemFields{{Phrase: "p", Translations: map[string]vocabTranslationFields{
		"en": {Translation: "hello"},
	}}} // "pl" omitted, not merely different
	if !vocabItemsUnchanged(existing, incoming) {
		t.Errorf("vocabItemsUnchanged with an omitted (not differing) locale = false, want true")
	}
}

// TestVocabItemsUnchangedDetectsProvidedTranslationDifference covers that a
// locale incoming does provide, with a different value than stored, is
// detected as changed (including a Notes-only difference).
func TestVocabItemsUnchangedDetectsProvidedTranslationDifference(t *testing.T) {
	existing := []vocabItemFields{{Translations: map[string]vocabTranslationFields{"en": {Translation: "hello", Notes: "n"}}}}
	incoming := []vocabItemFields{{Translations: map[string]vocabTranslationFields{"en": {Translation: "hello", Notes: "different"}}}}
	if vocabItemsUnchanged(existing, incoming) {
		t.Errorf("vocabItemsUnchanged with a differing provided locale's notes = true, want false")
	}
}

// TestValidateVocabImportItemsRejectsBlankPhrase covers the "one bad item in
// a list" rule: any item with a blank (or whitespace-only) phrase reports an
// error naming that item's index, so the caller can abort the whole list as
// a single list-level error before writing anything.
func TestValidateVocabImportItemsRejectsBlankPhrase(t *testing.T) {
	items := []apiVocabImportItem{{Phrase: "ok"}, {Phrase: "   "}}
	err := validateVocabImportItems(items)
	if err == nil {
		t.Fatal("validateVocabImportItems with a blank-phrase item = nil, want an error")
	}
}

// TestValidateVocabImportItemsAcceptsAllValid covers the no-op case: every
// item has a non-blank phrase.
func TestValidateVocabImportItemsAcceptsAllValid(t *testing.T) {
	items := []apiVocabImportItem{{Phrase: "one"}, {Phrase: "two"}}
	if err := validateVocabImportItems(items); err != nil {
		t.Errorf("validateVocabImportItems with all-valid items = %v, want nil", err)
	}
}

// TestDecideItemBackfillAllProvided covers the no-op case: a transcription
// (language needs one) and both site locales' translations are present —
// decideItemBackfill must return nothing (and, in particular, no "title"
// decision — vocabulary/models items have no title field at all).
func TestDecideItemBackfillAllProvided(t *testing.T) {
	provided := map[string]string{"en": "hello", "pl": "cześć"}
	got := decideItemBackfill("Transcription", true, provided, siteLocalesForTest)
	if len(got) != 0 {
		t.Errorf("decideItemBackfill with everything provided = %+v, want empty", got)
	}
}

// TestDecideItemBackfillTranscriptionOnlyWhenNeeded covers that a blank
// transcription is only a decision when needsTranscription is true.
func TestDecideItemBackfillTranscriptionOnlyWhenNeeded(t *testing.T) {
	provided := map[string]string{"en": "hello", "pl": "cześć"}
	got := decideItemBackfill("", false, provided, siteLocalesForTest)
	for _, d := range got {
		if d.Kind == "transcription" {
			t.Fatalf("decideItemBackfill with needsTranscription=false = %+v, want no transcription decision", got)
		}
	}

	got = decideItemBackfill("", true, provided, siteLocalesForTest)
	found := false
	for _, d := range got {
		if d.Kind == "transcription" {
			found = true
		}
	}
	if !found {
		t.Fatalf("decideItemBackfill with needsTranscription=true and blank transcription = %+v, want a transcription decision", got)
	}
}

// TestDecideItemBackfillMissingLocalesOnly covers the one-job-per-missing-
// locale rule, and that a provided locale is never re-generated.
func TestDecideItemBackfillMissingLocalesOnly(t *testing.T) {
	provided := map[string]string{"en": "hello"} // "pl" missing
	got := decideItemBackfill("Transcription", false, provided, siteLocalesForTest)
	if len(got) != 1 {
		t.Fatalf("decideItemBackfill with one missing locale = %+v, want exactly one decision", got)
	}
	if got[0].Kind != "translation" || got[0].Locale != "pl" {
		t.Errorf("decideItemBackfill missing-locale decision = %+v, want {Kind: translation, Locale: pl}", got[0])
	}
}

// TestDecideItemBackfillNoLocalesProvided covers a brand-new item with no
// translations at all — every site locale is missing.
func TestDecideItemBackfillNoLocalesProvided(t *testing.T) {
	got := decideItemBackfill("Transcription", false, nil, siteLocalesForTest)
	if len(got) != len(siteLocalesForTest) {
		t.Fatalf("decideItemBackfill with no locales provided = %+v, want one decision per site locale", got)
	}
	for _, d := range got {
		if d.Kind != "translation" {
			t.Errorf("decideItemBackfill decision = %+v, want Kind translation", d)
		}
	}
}

// TestToVocabTranslationFieldsEmptyIsNil covers that an empty/nil input
// yields a nil map, matching vocabItemFields.Translations' "restricted to
// provided locales" convention (a nil map and an empty map behave the same
// on lookup, but nil keeps the zero-value comparison honest in tests).
func TestToVocabTranslationFieldsEmptyIsNil(t *testing.T) {
	if got := toVocabTranslationFields(nil); got != nil {
		t.Errorf("toVocabTranslationFields(nil) = %+v, want nil", got)
	}
	if got := toVocabTranslationFields(map[string]apiVocabImportItemTranslation{}); got != nil {
		t.Errorf("toVocabTranslationFields({}) = %+v, want nil", got)
	}
}

// TestToVocabTranslationFieldsConvertsProvidedEntries covers the normal
// conversion path.
func TestToVocabTranslationFieldsConvertsProvidedEntries(t *testing.T) {
	in := map[string]apiVocabImportItemTranslation{"en": {Translation: "hello", Notes: "note"}}
	got := toVocabTranslationFields(in)
	want := vocabTranslationFields{Translation: "hello", Notes: "note"}
	if got["en"] != want {
		t.Errorf("toVocabTranslationFields(%+v)[\"en\"] = %+v, want %+v", in, got["en"], want)
	}
}

// --- B1 regression coverage: an omitted items: key must leave existing
// items untouched, distinct from an explicit items: [] which wholesale-
// clears them. See specs/features/phraseforge-export-import.md's B1 fix and
// apiVocabImportList's own doc comment. ---

// TestVocabImportListItemsOmittedDecodesToNil covers the YAML decode step
// itself: an update-by-id list with no items: key at all (e.g. one that only
// renames the list) must decode Items to a nil pointer, not merely a nil/
// empty slice — before this fix, both "omitted" and "items: []" decoded
// identically, which is exactly what made a rename-only update wipe every
// item.
func TestVocabImportListItemsOmittedDecodesToNil(t *testing.T) {
	var req apiVocabImportRequest
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

// TestVocabImportListItemsExplicitEmptyDecodesToNonNil covers the opposite
// case: an explicit items: [] must decode to a non-nil pointer to an empty
// slice — the correct, deliberate way to clear every item, per
// apiVocabImportList's doc comment.
func TestVocabImportListItemsExplicitEmptyDecodesToNonNil(t *testing.T) {
	var req apiVocabImportRequest
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

// TestResolveVocabImportItemsOmitted covers resolveVocabImportItems' own
// contract directly: a nil Items pointer reports provided=false and a nil
// items slice — the caller (importUpdateVocabList) uses provided=false to
// skip the wholesale-replace step entirely, leaving existing items alone.
func TestResolveVocabImportItemsOmitted(t *testing.T) {
	items, provided := resolveVocabImportItems(apiVocabImportList{ID: 1, Title: "Renamed"})
	if provided {
		t.Error("provided = true, want false for an omitted items: key")
	}
	if items != nil {
		t.Errorf("items = %+v, want nil", items)
	}
}

// TestResolveVocabImportItemsExplicitEmpty covers the other side: a
// non-nil-but-empty Items reports provided=true, so the caller proceeds with
// a wholesale replace down to zero items — the deliberate "items: [] clears
// everything" behavior.
func TestResolveVocabImportItemsExplicitEmpty(t *testing.T) {
	empty := []apiVocabImportItem{}
	items, provided := resolveVocabImportItems(apiVocabImportList{ID: 1, Items: &empty})
	if !provided {
		t.Error("provided = false, want true for an explicit items: []")
	}
	if len(items) != 0 {
		t.Errorf("items = %+v, want empty", items)
	}
}

// TestResolveVocabImportItemsNonEmpty covers the normal case: a populated
// items: list is returned unchanged with provided=true.
func TestResolveVocabImportItemsNonEmpty(t *testing.T) {
	in := []apiVocabImportItem{{Phrase: "one"}, {Phrase: "two"}}
	items, provided := resolveVocabImportItems(apiVocabImportList{ID: 1, Items: &in})
	if !provided {
		t.Error("provided = false, want true")
	}
	if len(items) != 2 || items[0].Phrase != "one" || items[1].Phrase != "two" {
		t.Errorf("items = %+v, want the same two items unchanged", items)
	}
}
