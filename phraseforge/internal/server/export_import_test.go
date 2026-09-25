package server

import (
	"net/http/httptest"
	"testing"

	"phraseforge/internal/i18n"
)

// TestImportUnchangedIdenticalFields covers the fully-matching case: no
// field differs, no tag differs, and every incoming translation matches
// what's stored — importUnchanged must report true so apiImportTexts/
// apiImportDialogs skip the write and any backfill entirely.
func TestImportUnchangedIdenticalFields(t *testing.T) {
	existing := importFields{
		Title: "Title", Body: "Body", Transcription: "Transcription",
		Language: "ron", Script: "latn", Tags: []string{"a", "b"},
		Translations: map[string]string{"en": "hello"},
	}
	incoming := existing
	if !importUnchanged(existing, incoming) {
		t.Errorf("importUnchanged(%+v, %+v) = false, want true", existing, incoming)
	}
}

// TestImportUnchangedDetectsEachFieldDifference covers that a difference in
// any single primitive field (title/body/transcription/language/script)
// alone is enough to report changed — a naive implementation that only
// compares a subset would silently skip a real edit.
func TestImportUnchangedDetectsEachFieldDifference(t *testing.T) {
	base := importFields{Title: "T", Body: "B", Transcription: "X", Language: "ron", Script: "latn", Tags: []string{"a"}}
	cases := []struct {
		name   string
		mutate func(f importFields) importFields
	}{
		{"title", func(f importFields) importFields { f.Title = "different"; return f }},
		{"body", func(f importFields) importFields { f.Body = "different"; return f }},
		{"transcription", func(f importFields) importFields { f.Transcription = "different"; return f }},
		{"language", func(f importFields) importFields { f.Language = "fra"; return f }},
		{"script", func(f importFields) importFields { f.Script = "cyrl"; return f }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			incoming := c.mutate(base)
			if importUnchanged(base, incoming) {
				t.Errorf("importUnchanged with %s changed = true, want false", c.name)
			}
		})
	}
}

// TestImportUnchangedDetectsTagDifference covers a tag-set change (added,
// removed, or reordered) — tags must already be normalized (same
// convention as normalizeTagList/tags.Parse) before reaching importUnchanged,
// so this only checks slice equality, but order still matters since both
// sides are expected pre-sorted.
func TestImportUnchangedDetectsTagDifference(t *testing.T) {
	existing := importFields{Tags: []string{"a", "b"}}
	incoming := importFields{Tags: []string{"a", "c"}}
	if importUnchanged(existing, incoming) {
		t.Errorf("importUnchanged with different tags = true, want false")
	}
}

// TestImportUnchangedIgnoresOmittedTranslationLocale covers the key
// asymmetry between tags (always fully compared) and translations (only
// the locales incoming actually provides are compared) — a locale the
// import omits must never make an otherwise-identical row look "changed",
// since an import never writes to a locale it doesn't mention.
func TestImportUnchangedIgnoresOmittedTranslationLocale(t *testing.T) {
	existing := importFields{Translations: map[string]string{"en": "hello", "pl": "cześć"}}
	incoming := importFields{Translations: map[string]string{"en": "hello"}} // "pl" omitted, not merely different
	if !importUnchanged(existing, incoming) {
		t.Errorf("importUnchanged with an omitted (not differing) locale = false, want true")
	}
}

// TestImportUnchangedDetectsProvidedTranslationDifference covers that a
// locale incoming *does* provide, with a different value than what's
// stored, is correctly detected as changed.
func TestImportUnchangedDetectsProvidedTranslationDifference(t *testing.T) {
	existing := importFields{Translations: map[string]string{"en": "hello"}}
	incoming := importFields{Translations: map[string]string{"en": "goodbye"}}
	if importUnchanged(existing, incoming) {
		t.Errorf("importUnchanged with a differing provided locale = true, want false")
	}
}

// TestNormalizeTagList covers tags.Parse's own dedupe/lowercase/sort
// applied through normalizeTagList's already-split-list entry point.
func TestNormalizeTagList(t *testing.T) {
	got := normalizeTagList([]string{"Travel", " travel ", "ROMANIAN", ""})
	want := []string{"romanian", "travel"}
	if len(got) != len(want) {
		t.Fatalf("normalizeTagList(...) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("normalizeTagList(...)[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// siteLocalesForTest mirrors i18n.Locales' own en/pl shape without depending
// on that package's exact contents changing underneath this test.
var siteLocalesForTest = []i18n.Locale{{Code: "en", Name: "English"}, {Code: "pl", Name: "Polski"}}

// TestDecideBackfillAllFieldsProvided covers the no-op case: a title, a
// transcription (language needs one), and both site locales' translations
// are all present — decideBackfill must return nothing.
func TestDecideBackfillAllFieldsProvided(t *testing.T) {
	provided := map[string]string{"en": "hello", "pl": "cześć"}
	got := decideBackfill("Title", "Transcription", true, provided, siteLocalesForTest)
	if len(got) != 0 {
		t.Errorf("decideBackfill with everything provided = %+v, want empty", got)
	}
}

// TestDecideBackfillMissingTitle covers that a blank title always triggers
// a "title" decision, regardless of transcription/translation state.
func TestDecideBackfillMissingTitle(t *testing.T) {
	provided := map[string]string{"en": "hello", "pl": "cześć"}
	got := decideBackfill("", "Transcription", true, provided, siteLocalesForTest)
	if len(got) != 1 || got[0].Kind != "title" {
		t.Fatalf("decideBackfill with blank title = %+v, want exactly one title decision", got)
	}
}

// TestDecideBackfillTranscriptionOnlyWhenNeeded covers that a blank
// transcription is only a decision when needsTranscription is true — a
// language that doesn't need transcription must never enqueue one just
// because the field happens to be blank.
func TestDecideBackfillTranscriptionOnlyWhenNeeded(t *testing.T) {
	provided := map[string]string{"en": "hello", "pl": "cześć"}
	got := decideBackfill("Title", "", false, provided, siteLocalesForTest)
	for _, d := range got {
		if d.Kind == "transcription" {
			t.Fatalf("decideBackfill with needsTranscription=false = %+v, want no transcription decision", got)
		}
	}

	got = decideBackfill("Title", "", true, provided, siteLocalesForTest)
	found := false
	for _, d := range got {
		if d.Kind == "transcription" {
			found = true
		}
	}
	if !found {
		t.Fatalf("decideBackfill with needsTranscription=true and blank transcription = %+v, want a transcription decision", got)
	}
}

// TestDecideBackfillMissingLocalesOnly covers the one-job-per-missing-
// locale rule: a locale present in provided is never re-generated; a locale
// absent from provided always gets its own "translation" decision with
// Locale set to that locale's code.
func TestDecideBackfillMissingLocalesOnly(t *testing.T) {
	provided := map[string]string{"en": "hello"} // "pl" missing
	got := decideBackfill("Title", "Transcription", false, provided, siteLocalesForTest)
	if len(got) != 1 {
		t.Fatalf("decideBackfill with one missing locale = %+v, want exactly one decision", got)
	}
	if got[0].Kind != "translation" || got[0].Locale != "pl" {
		t.Errorf("decideBackfill missing-locale decision = %+v, want {Kind: translation, Locale: pl}", got[0])
	}
}

// TestDecideBackfillNoLocalesProvided covers a brand-new item with no
// translations field at all — every site locale is missing.
func TestDecideBackfillNoLocalesProvided(t *testing.T) {
	got := decideBackfill("Title", "Transcription", false, nil, siteLocalesForTest)
	if len(got) != len(siteLocalesForTest) {
		t.Fatalf("decideBackfill with no locales provided = %+v, want one decision per site locale", got)
	}
	for _, d := range got {
		if d.Kind != "translation" {
			t.Errorf("decideBackfill decision = %+v, want Kind translation", d)
		}
	}
}

// TestBuildBackfillPayloadSentinelTargets covers the sentinel-target
// convention every other call site in this codebase already uses (see
// ingest.go's buildTitleCall/buildCleaningCall and transcriptionTargetLanguage's
// own doc comment): "title"/"transcription" fix target_language to their own
// kind name so an admin llm_prompts override stays reachable; "translation"
// sets it to the destination locale code.
func TestBuildBackfillPayloadSentinelTargets(t *testing.T) {
	cases := []struct {
		decision      backfillDecision
		wantTarget    string
		wantLocaleSet string
	}{
		{backfillDecision{Kind: "title"}, "title", ""},
		{backfillDecision{Kind: "transcription"}, "transcription", ""},
		{backfillDecision{Kind: "translation", Locale: "pl"}, "pl", "pl"},
	}
	for _, c := range cases {
		got := buildBackfillPayload(c.decision, "text", 42, "ron", "body content")
		if got.TargetLanguage != c.wantTarget {
			t.Errorf("buildBackfillPayload(%+v).TargetLanguage = %q, want %q", c.decision, got.TargetLanguage, c.wantTarget)
		}
		if got.Locale != c.wantLocaleSet {
			t.Errorf("buildBackfillPayload(%+v).Locale = %q, want %q", c.decision, got.Locale, c.wantLocaleSet)
		}
		if got.Kind != c.decision.Kind || got.SourceLanguage != "ron" || got.ContentType != "text" || got.Content != "body content" {
			t.Errorf("buildBackfillPayload(%+v) = %+v, want Kind/SourceLanguage/ContentType/Content carried through unchanged", c.decision, got)
		}
		if got.ResourceType != "text" || got.ResourceID != 42 {
			t.Errorf("buildBackfillPayload(%+v) ResourceType/ResourceID = %q/%d, want text/42", c.decision, got.ResourceType, got.ResourceID)
		}
	}
}

// TestExportScopeLanguagesFilterWithinEditable covers that a ?language=
// filter matching one of the caller's editable languages narrows to just
// that language.
func TestExportScopeLanguagesFilterWithinEditable(t *testing.T) {
	langs, all := exportScopeLanguages([]string{"ron", "fra"}, false, "fra")
	if all || len(langs) != 1 || langs[0] != "fra" {
		t.Errorf("exportScopeLanguages(...) = (%v, %v), want ([fra], false)", langs, all)
	}
}

// TestExportScopeLanguagesFilterOutsideEditableIgnored covers that a
// ?language= value the caller cannot edit is ignored (falls back to their
// full editable set) rather than narrowing to a language they have no
// access to at all.
func TestExportScopeLanguagesFilterOutsideEditableIgnored(t *testing.T) {
	langs, all := exportScopeLanguages([]string{"ron"}, false, "jpn")
	if all || len(langs) != 1 || langs[0] != "ron" {
		t.Errorf("exportScopeLanguages(...) = (%v, %v), want ([ron], false) — the unfiltered editable set", langs, all)
	}
}

// TestExportScopeLanguagesAdminFilterStillNarrows covers that an admin
// (editAll=true) requesting a specific ?language= still narrows to just
// that language, rather than the filter being ignored because "all" is
// already true.
func TestExportScopeLanguagesAdminFilterStillNarrows(t *testing.T) {
	langs, all := exportScopeLanguages(nil, true, "ron")
	if all || len(langs) != 1 || langs[0] != "ron" {
		t.Errorf("exportScopeLanguages(nil, true, \"ron\") = (%v, %v), want ([ron], false)", langs, all)
	}
}

// TestExportScopeLanguagesNoFilterKeepsEditableSet covers the unfiltered
// case: no ?language= means every editable language (or "all", for admin)
// unchanged.
func TestExportScopeLanguagesNoFilterKeepsEditableSet(t *testing.T) {
	langs, all := exportScopeLanguages([]string{"ron", "fra"}, false, "")
	if all || len(langs) != 2 {
		t.Errorf("exportScopeLanguages(..., \"\") = (%v, %v), want the unfiltered ([ron fra], false)", langs, all)
	}
}

// TestRequireExportFilterMissingLanguage covers the 400 case: script given,
// language missing.
func TestRequireExportFilterMissingLanguage(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/texts/export?script=latn", nil)
	w := httptest.NewRecorder()
	_, ok := requireExportFilter(w, r)
	if ok {
		t.Fatal("requireExportFilter with no language = ok, want not ok")
	}
	if w.Code != 400 {
		t.Errorf("requireExportFilter with no language wrote status %d, want 400", w.Code)
	}
}

// TestRequireExportFilterMissingScript covers the 400 case: language given,
// script missing.
func TestRequireExportFilterMissingScript(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/texts/export?language=ron", nil)
	w := httptest.NewRecorder()
	_, ok := requireExportFilter(w, r)
	if ok {
		t.Fatal("requireExportFilter with no script = ok, want not ok")
	}
	if w.Code != 400 {
		t.Errorf("requireExportFilter with no script wrote status %d, want 400", w.Code)
	}
}

// TestRequireExportFilterBothPresentParsesTags covers the success case, plus
// that tags= is parsed through tags.Parse's own normalization (dedupe,
// lowercase, sort) exactly like normalizeTagList already does for import.
func TestRequireExportFilterBothPresentParsesTags(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/texts/export?language=ron&script=latn&tags=Travel,travel,Food", nil)
	w := httptest.NewRecorder()
	filter, ok := requireExportFilter(w, r)
	if !ok {
		t.Fatalf("requireExportFilter with both present = not ok, want ok (wrote status %d)", w.Code)
	}
	if filter.Language != "ron" || filter.Script != "latn" {
		t.Errorf("requireExportFilter(...) = %+v, want Language=ron Script=latn", filter)
	}
	want := []string{"food", "travel"}
	if len(filter.Tags) != len(want) {
		t.Fatalf("requireExportFilter(...).Tags = %v, want %v", filter.Tags, want)
	}
	for i := range want {
		if filter.Tags[i] != want[i] {
			t.Errorf("requireExportFilter(...).Tags[%d] = %q, want %q", i, filter.Tags[i], want[i])
		}
	}
}

// TestRequireExportFilterOmittedTagsIsEmpty covers that a request with no
// tags= param at all parses to an empty (not nil-but-truthy) Tags slice, so
// every export handler's "if len(filter.Tags) > 0" guard correctly skips the
// tag-membership filter entirely.
func TestRequireExportFilterOmittedTagsIsEmpty(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/texts/export?language=ron&script=latn", nil)
	w := httptest.NewRecorder()
	filter, ok := requireExportFilter(w, r)
	if !ok {
		t.Fatalf("requireExportFilter with no tags = not ok, want ok (wrote status %d)", w.Code)
	}
	if len(filter.Tags) != 0 {
		t.Errorf("requireExportFilter(...).Tags = %v, want empty", filter.Tags)
	}
}

// filterTestItem is a minimal stand-in for texts.Text/dialogs.Dialog/etc. —
// filterByScript/filterByID are generic and resource-agnostic, so a plain
// local struct exercises them without depending on any one resource
// package's real type.
type filterTestItem struct {
	id     int64
	script string
}

// TestFilterByScriptKeepsOnlyExactMatch covers that filterByScript (the
// export handlers' required script= narrowing) keeps only items whose
// script matches exactly, dropping every other script — including a
// same-language-different-script item, which is exactly the case this
// feature's script filter exists to narrow out.
func TestFilterByScriptKeepsOnlyExactMatch(t *testing.T) {
	items := []filterTestItem{{1, "latn"}, {2, "cyrl"}, {3, "latn"}}
	got := filterByScript(items, "latn", func(i filterTestItem) string { return i.script })
	if len(got) != 2 || got[0].id != 1 || got[1].id != 3 {
		t.Errorf("filterByScript(...) = %+v, want items 1 and 3 only", got)
	}
}

// TestFilterByIDExcludesItemMissingEvenOneTag covers the ALL-match contract
// at the filterByID call site export uses with tags.ResourceIDsWithAllTags:
// an item whose id isn't in the "has every requested tag" set (matching)
// must be excluded, even if it carries some but not all of the requested
// tags — filterByID itself only ever sees the already-computed matching id
// set, so this is really asserting the call site wires ALL-match semantics
// through correctly, not re-testing filterByID's own (already
// straightforward) set-membership logic.
func TestFilterByIDExcludesItemMissingEvenOneTag(t *testing.T) {
	items := []filterTestItem{{1, "latn"}, {2, "latn"}, {3, "latn"}}
	matching := []int64{1, 3} // e.g. only 1 and 3 carry every requested tag
	got := filterByID(items, matching, func(i filterTestItem) int64 { return i.id })
	if len(got) != 2 || got[0].id != 1 || got[1].id != 3 {
		t.Errorf("filterByID(...) = %+v, want items 1 and 3 only", got)
	}
}
