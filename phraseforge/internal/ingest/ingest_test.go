package ingest

import (
	"encoding/json"
	"testing"

	"phraseforge/internal/i18n"
)

// TestBuildFollowUpPayloadsTranslationPerLocale covers the one-translation-
// job-per-site-locale rule, including that target_language and locale are
// both set to the locale's own code (not translated through any other
// mapping) — matching the existing interactive Translate button's
// convention (its target_language comes straight from a <select> populated
// with locale codes).
func TestBuildFollowUpPayloadsTranslationPerLocale(t *testing.T) {
	locales := []i18n.Locale{{Code: "en", Name: "English"}, {Code: "pl", Name: "Polski"}}
	got := buildFollowUpPayloads("text", "rus", "cleaned body", 42, locales, false)

	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (one per locale, no transcription)", len(got))
	}
	for i, loc := range locales {
		p := got[i]
		if p.Kind != "translation" {
			t.Errorf("payload[%d].Kind = %q, want %q", i, p.Kind, "translation")
		}
		if p.SourceLanguage != "rus" {
			t.Errorf("payload[%d].SourceLanguage = %q, want %q", i, p.SourceLanguage, "rus")
		}
		if p.TargetLanguage != loc.Code {
			t.Errorf("payload[%d].TargetLanguage = %q, want locale code %q", i, p.TargetLanguage, loc.Code)
		}
		if p.Locale != loc.Code {
			t.Errorf("payload[%d].Locale = %q, want locale code %q", i, p.Locale, loc.Code)
		}
		if p.ContentType != "text" || p.ResourceType != "text" {
			t.Errorf("payload[%d] ContentType/ResourceType = %q/%q, want %q/%q", i, p.ContentType, p.ResourceType, "text", "text")
		}
		if p.ResourceID != 42 {
			t.Errorf("payload[%d].ResourceID = %d, want 42", i, p.ResourceID)
		}
		if p.Content != "cleaned body" {
			t.Errorf("payload[%d].Content = %q, want %q", i, p.Content, "cleaned body")
		}
	}
}

// TestBuildFollowUpPayloadsTranscriptionSentinel covers the
// needs-transcription branch, in particular that target_language is fixed
// to the "transcription" sentinel (see transcriptionTargetLanguage's doc
// comment) rather than the real language or empty — a real language there
// would silently bypass any admin-configured transcription prompt override,
// since llm_prompts is looked up by the exact (kind, source_language,
// target_language) triple.
func TestBuildFollowUpPayloadsTranscriptionSentinel(t *testing.T) {
	locales := []i18n.Locale{{Code: "en", Name: "English"}}
	got := buildFollowUpPayloads("dialog", "jpn", "cleaned dialog", 7, locales, true)

	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (1 translation + 1 transcription)", len(got))
	}
	transcription := got[len(got)-1]
	if transcription.Kind != "transcription" {
		t.Fatalf("last payload.Kind = %q, want %q", transcription.Kind, "transcription")
	}
	if transcription.TargetLanguage != "transcription" {
		t.Errorf("transcription.TargetLanguage = %q, want sentinel %q", transcription.TargetLanguage, "transcription")
	}
	if transcription.SourceLanguage != "jpn" {
		t.Errorf("transcription.SourceLanguage = %q, want %q", transcription.SourceLanguage, "jpn")
	}
	if transcription.Locale != "" {
		t.Errorf("transcription.Locale = %q, want empty (locale is translation-writeback-only)", transcription.Locale)
	}
	if transcription.ResourceType != "dialog" || transcription.ContentType != "dialog" {
		t.Errorf("transcription ResourceType/ContentType = %q/%q, want %q/%q", transcription.ResourceType, transcription.ContentType, "dialog", "dialog")
	}
}

// TestBuildFollowUpPayloadsNoTranscriptionWhenNotNeeded covers that no
// transcription payload is appended when needsTranscription is false.
func TestBuildFollowUpPayloadsNoTranscriptionWhenNotNeeded(t *testing.T) {
	locales := []i18n.Locale{{Code: "en", Name: "English"}, {Code: "pl", Name: "Polski"}}
	got := buildFollowUpPayloads("text", "eng", "content", 1, locales, false)
	for _, p := range got {
		if p.Kind == "transcription" {
			t.Fatalf("got a transcription payload with needsTranscription=false: %+v", p)
		}
	}
}

// TestGeneratePayloadJSONShape guards against generatePayload's JSON tags
// drifting from ai.Service's own (unexported) llm_generate payload shape —
// the two are independently maintained by design (see generatePayload's doc
// comment), so nothing else catches that drift at compile time.
func TestGeneratePayloadJSONShape(t *testing.T) {
	p := generatePayload{
		Kind: "translation", SourceLanguage: "rus", TargetLanguage: "en",
		ContentType: "text", Content: "body",
		ResourceType: "text", ResourceID: 5, Locale: "en",
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"kind", "source_language", "target_language", "content_type", "content", "resource_type", "resource_id", "locale"} {
		if _, ok := m[key]; !ok {
			t.Errorf("marshaled payload missing key %q: %s", key, raw)
		}
	}
}

// TestBuildCleaningCallSentinelTarget covers that the process_text/
// process_dialog cleanup call's target_language is fixed to the kind's own
// name (the same sentinel convention transcriptionTargetLanguage documents),
// not the ingested content's real language — getting this wrong would make
// an admin override for these kinds unreachable, since the admin LLM-prompt
// editor's target field is also fixed to the kind's own name for every
// non-"translation" kind.
func TestBuildCleaningCallSentinelTarget(t *testing.T) {
	for _, kind := range []string{KindProcessText, KindProcessDialog} {
		got := buildCleaningCall(kind, "fra", "raw content")
		if got.Kind != kind {
			t.Errorf("buildCleaningCall(%q).Kind = %q, want %q", kind, got.Kind, kind)
		}
		if got.SourceLanguage != "fra" {
			t.Errorf("buildCleaningCall(%q).SourceLanguage = %q, want %q", kind, got.SourceLanguage, "fra")
		}
		if got.TargetLanguage != kind {
			t.Errorf("buildCleaningCall(%q).TargetLanguage = %q, want sentinel %q (kind's own name)", kind, got.TargetLanguage, kind)
		}
		if got.Content != "raw content" {
			t.Errorf("buildCleaningCall(%q).Content = %q, want %q", kind, got.Content, "raw content")
		}
	}
}

// TestBuildTitleCallSentinelTarget covers the same sentinel-target
// convention for the direct "title" call.
func TestBuildTitleCallSentinelTarget(t *testing.T) {
	got := buildTitleCall("fra", "cleaned content")
	if got.Kind != "title" {
		t.Errorf("buildTitleCall.Kind = %q, want %q", got.Kind, "title")
	}
	if got.SourceLanguage != "fra" {
		t.Errorf("buildTitleCall.SourceLanguage = %q, want %q", got.SourceLanguage, "fra")
	}
	if got.TargetLanguage != "title" {
		t.Errorf("buildTitleCall.TargetLanguage = %q, want sentinel %q", got.TargetLanguage, "title")
	}
	if got.Content != "cleaned content" {
		t.Errorf("buildTitleCall.Content = %q, want %q", got.Content, "cleaned content")
	}
}

// TestParseCreatedStep covers createdRow's retry-safety marker parsing: no
// marker (the normal, first-run path), a well-formed marker, and a malformed
// one.
func TestParseCreatedStep(t *testing.T) {
	if id, ok, err := parseCreatedStep(""); err != nil || ok || id != 0 {
		t.Errorf("parseCreatedStep(\"\") = (%d, %v, %v), want (0, false, nil)", id, ok, err)
	}
	if id, ok, err := parseCreatedStep("created:42"); err != nil || !ok || id != 42 {
		t.Errorf("parseCreatedStep(\"created:42\") = (%d, %v, %v), want (42, true, nil)", id, ok, err)
	}
	if _, ok, err := parseCreatedStep("created:not-a-number"); err == nil || ok {
		t.Errorf("parseCreatedStep(\"created:not-a-number\") = (_, %v, %v), want an error and ok=false", ok, err)
	}
}

// TestProcessPayloadJSONShape guards the process_text/process_dialog job
// payload's field names against drift from what the (not-yet-built) HTTP
// ingest endpoint is expected to construct.
func TestProcessPayloadJSONShape(t *testing.T) {
	raw := []byte(`{"content":"raw text","source":"https://example.com/a","language":"eng","script":"latn","user_id":3}`)
	var p processPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if p.Content != "raw text" || p.Source != "https://example.com/a" || p.Language != "eng" || p.Script != "latn" || p.UserID != 3 {
		t.Errorf("unmarshaled processPayload = %+v, want fields from raw JSON", p)
	}
}
