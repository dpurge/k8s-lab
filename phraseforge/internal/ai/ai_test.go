package ai

import (
	"context"
	"strings"
	"testing"

	"phraseforge/internal/config"
)

// TestPurposeDefault covers the five kinds Generate supports plus the
// lenient "unrecognized kind falls back to Translation" behavior this
// package has always had.
func TestPurposeDefault(t *testing.T) {
	cfg := config.Config{
		Transcription: config.PurposeConfig{Prompt: "transcription-model"},
		Translation:   config.PurposeConfig{Prompt: "translation-model"},
		Title:         config.PurposeConfig{Prompt: "title-model"},
		ProcessText:   config.PurposeConfig{Prompt: "process-text-model"},
		ProcessDialog: config.PurposeConfig{Prompt: "process-dialog-model"},
	}
	svc := &Service{cfg: cfg}

	cases := []struct {
		kind string
		want string
	}{
		{"transcription", "transcription-model"},
		{"translation", "translation-model"},
		{"title", "title-model"},
		{"process_text", "process-text-model"},
		{"process_dialog", "process-dialog-model"},
		{"something_unrecognized", "translation-model"},
	}
	for _, c := range cases {
		if got := svc.purposeDefault(c.kind).Prompt; got != c.want {
			t.Errorf("purposeDefault(%q).Prompt = %q, want %q", c.kind, got, c.want)
		}
	}
}

// TestJobKind covers the field-to-job-kind mapping used by every enqueue
// call site (export/import backfill, the View-page Generate buttons,
// item-level generation) so the Jobs page shows what a "generic LLM call"
// job actually did instead of one bucket for all of them.
func TestJobKind(t *testing.T) {
	cases := []struct {
		payloadKind string
		want        string
	}{
		{"title", KindGenerateTitle},
		{"transcription", KindGenerateTranscription},
		{"translation", KindGenerateTranslation},
		{"something_unrecognized", KindLLMGenerate},
	}
	for _, c := range cases {
		if got := JobKind(c.payloadKind); got != c.want {
			t.Errorf("JobKind(%q) = %q, want %q", c.payloadKind, got, c.want)
		}
	}
}

// TestResolveTimeoutSeconds covers the three-tier fallback (llm-purpose-
// timeout-and-prompt-config): an admin override wins when set; otherwise
// the purpose default; a purpose default of 0 (no config.yaml value) is
// itself a valid "no purpose-level override" signal, resolving to 0 —
// llm.New's own doc comment covers the last fallback tier (0 means apply
// shared/llm's built-in default), so this function needs no third
// parameter for it.
func TestResolveTimeoutSeconds(t *testing.T) {
	ptr := func(v int) *int { return &v }
	cases := []struct {
		name           string
		override       *int
		purposeDefault int
		want           int
	}{
		{"override wins over purpose default", ptr(45), 120, 45},
		{"no override falls through to purpose default", nil, 120, 120},
		{"no override, no purpose default falls through to 0 (llm.New's own default)", nil, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveTimeoutSeconds(c.override, c.purposeDefault); got != c.want {
				t.Errorf("resolveTimeoutSeconds(%v, %d) = %d, want %d", c.override, c.purposeDefault, got, c.want)
			}
		})
	}
}

// TestTextWriteback covers HandleGenerate's resource_type -> store dispatch.
func TestTextWriteback(t *testing.T) {
	texts := &fakeTextWriteback{}
	dialogs := &fakeTextWriteback{}
	svc := &Service{texts: texts, dialogs: dialogs}

	if got := svc.textWriteback("text"); got != texts {
		t.Errorf("textWriteback(%q) = %v, want the texts store", "text", got)
	}
	if got := svc.textWriteback("dialog"); got != dialogs {
		t.Errorf("textWriteback(%q) = %v, want the dialogs store", "dialog", got)
	}
	if got := svc.textWriteback("bogus"); got != nil {
		t.Errorf("textWriteback(%q) = %v, want nil", "bogus", got)
	}
}

type fakeTextWriteback struct {
	transcription string
	calls         int
	title         string
	titleCalls    int
	// blank gates whether the guarded setters report applied=true — mirrors
	// the real stores' "already non-blank" skip case (background-generate-
	// title-transcription-translation). Default false means every call
	// applies, matching every pre-existing test's expectations.
	alreadyHasTitle         bool
	alreadyHasTranscription bool
}

func (f *fakeTextWriteback) SetTitleIfBlank(ctx context.Context, id int64, title string) (bool, error) {
	if f.alreadyHasTitle {
		return false, nil
	}
	f.titleCalls++
	f.title = title
	return true, nil
}
func (f *fakeTextWriteback) SetTranscriptionIfBlank(ctx context.Context, id int64, transcription string) (bool, error) {
	if f.alreadyHasTranscription {
		return false, nil
	}
	f.calls++
	f.transcription = transcription
	return true, nil
}

// fakeTranslationWriteback is a fake TranslationWriteback (the plain,
// non-item resource path) — used to confirm an item resource type never
// falls through to it.
type fakeTranslationWriteback struct {
	calls             int
	alreadyHasContent bool // when false (default), every call applies
}

func (f *fakeTranslationWriteback) SetIfAbsent(ctx context.Context, resourceType string, resourceID int64, locale, text string) (bool, error) {
	if f.alreadyHasContent {
		return false, nil
	}
	f.calls++
	return true, nil
}

// TestWritebackTitle covers HandleGenerate's newly-wired "title" dispatch:
// a text/dialog resource type reaches TextWriteback.SetTitle (previously
// dead code — see writeback's doc comment history), while any other
// resource type (vocabulary/models items have no title) is a per-job error.
func TestWritebackTitle(t *testing.T) {
	texts := &fakeTextWriteback{}
	dialogs := &fakeTextWriteback{}
	svc := &Service{texts: texts, dialogs: dialogs}

	if applied, err := svc.writeback(context.Background(), generatePayload{Kind: "title", ResourceType: "text", ResourceID: 1}, "  A Title  "); err != nil || !applied {
		t.Fatalf("writeback: applied=%v err=%v", applied, err)
	}
	if texts.titleCalls != 1 || texts.title != "A Title" {
		t.Errorf("texts.{titleCalls,title} = %d,%q, want 1,%q", texts.titleCalls, texts.title, "A Title")
	}
	if dialogs.titleCalls != 0 {
		t.Errorf("dialogs.titleCalls = %d, want 0", dialogs.titleCalls)
	}

	if applied, err := svc.writeback(context.Background(), generatePayload{Kind: "title", ResourceType: "dialog", ResourceID: 1}, "Dialog Title"); err != nil || !applied {
		t.Fatalf("writeback: applied=%v err=%v", applied, err)
	}
	if dialogs.titleCalls != 1 || dialogs.title != "Dialog Title" {
		t.Errorf("dialogs.{titleCalls,title} = %d,%q, want 1,%q", dialogs.titleCalls, dialogs.title, "Dialog Title")
	}

	_, err := svc.writeback(context.Background(), generatePayload{Kind: "title", ResourceType: "vocabulary_item", ResourceID: 1}, "x")
	if err == nil {
		t.Fatal("writeback(title, vocabulary_item) = nil error, want an error — vocabulary items have no title")
	}
}

// TestWritebackTitleSkipsWhenAlreadyBlank covers the universal blank-check
// rule: a target field that already has content is a job success (applied
// false, no error), not a failure — background-generate-title-
// transcription-translation.
func TestWritebackTitleSkipsWhenAlreadyBlank(t *testing.T) {
	texts := &fakeTextWriteback{alreadyHasTitle: true}
	svc := &Service{texts: texts}

	applied, err := svc.writeback(context.Background(), generatePayload{Kind: "title", ResourceType: "text", ResourceID: 1}, "A Title")
	if err != nil {
		t.Fatalf("writeback: %v, want nil error (a skip is still job success)", err)
	}
	if applied {
		t.Error("applied = true, want false — the title already had content")
	}
	if texts.titleCalls != 0 {
		t.Errorf("titleCalls = %d, want 0 — a skip must never write", texts.titleCalls)
	}
}

// TestWritebackRejectsEmptyTitleResult extends TestWritebackRejectsEmptyResult's
// coverage to the title path: it may not reach its writeback interface with an
// empty result either.
func TestWritebackRejectsEmptyTitleResult(t *testing.T) {
	texts := &fakeTextWriteback{}
	svc := &Service{texts: texts}

	p := generatePayload{Kind: "title", ResourceType: "text", ResourceID: 1}
	if _, err := svc.writeback(context.Background(), p, "   \n\t  "); err == nil {
		t.Error("writeback(kind=title, empty result) = nil error, want an error")
	}
	if texts.titleCalls != 0 {
		t.Errorf("an empty result must never reach the writeback interface: titleCalls=%d", texts.titleCalls)
	}
}

// TestWritebackRefusesItemResourceTypes covers a pre-existing item-level
// generate_translation/generate_transcription row: items are filled by
// HandleItemTranslation now, so HandleGenerate must fail such a row instead of
// writing anything — in particular the plain translation store, which would
// otherwise store it under an item resource type it knows nothing about.
func TestWritebackRefusesItemResourceTypes(t *testing.T) {
	texts := &fakeTextWriteback{}
	plain := &fakeTranslationWriteback{}
	svc := &Service{texts: texts, translations: plain}

	for _, resourceType := range []string{"vocabulary_item", "models_item"} {
		for _, kind := range []string{"transcription", "translation"} {
			p := generatePayload{Kind: kind, ResourceType: resourceType, ResourceID: 1, Locale: "en"}
			if _, err := svc.writeback(context.Background(), p, "result"); err == nil {
				t.Errorf("writeback(kind=%q, resource_type=%q) = nil error, want it refused", kind, resourceType)
			}
		}
	}
	if plain.calls != 0 || texts.calls != 0 {
		t.Errorf("a refused item row must write nothing: translation calls=%d, transcription calls=%d", plain.calls, texts.calls)
	}
}

// TestWritebackRejectsEmptyResult covers that an empty/whitespace-only LLM
// result fails the job instead of reaching SetTranscription/translations.Set
// — either of which treats an empty body as "delete this value", so writing
// one through on an empty generation would silently destroy existing data
// rather than merely fail to update it (see writeback's doc comment).
func TestWritebackRejectsEmptyResult(t *testing.T) {
	texts := &fakeTextWriteback{}
	svc := &Service{texts: texts}
	p := generatePayload{Kind: "transcription", ResourceType: "text", ResourceID: 1}

	_, err := svc.writeback(context.Background(), p, "   \n\t  ")
	if err == nil {
		t.Fatal("writeback(empty result) = nil error, want an error")
	}
	if texts.calls != 0 {
		t.Errorf("SetTranscription called %d times, want 0 — an empty result must never reach the writeback interface", texts.calls)
	}
}

// TestWritebackTrimsResult covers that a non-empty result is trimmed before
// being written back, matching the interactive /llm/generate path and
// ingest.go's title-handling.
func TestWritebackTrimsResult(t *testing.T) {
	texts := &fakeTextWriteback{}
	svc := &Service{texts: texts}
	p := generatePayload{Kind: "transcription", ResourceType: "text", ResourceID: 1}

	if applied, err := svc.writeback(context.Background(), p, "  hello  \n"); err != nil || !applied {
		t.Fatalf("writeback: applied=%v err=%v", applied, err)
	}
	if texts.transcription != "hello" {
		t.Errorf("SetTranscription called with %q, want trimmed %q", texts.transcription, "hello")
	}
}

// TestDefaultPrompts: every kind has a default (the admin form's starting
// text), taken from that kind's purpose config.
func TestDefaultPrompts(t *testing.T) {
	svc := &Service{cfg: config.Config{
		Title:          config.PurposeConfig{Prompt: "title prompt"},
		Translation:    config.PurposeConfig{Prompt: "translation prompt"},
		VocabularyItem: config.PurposeConfig{Prompt: "vocabulary item prompt"},
	}}
	defaults := svc.DefaultPrompts()
	if len(defaults) != len(ValidKinds) {
		t.Errorf("DefaultPrompts has %d kinds, want %d (ValidKinds)", len(defaults), len(ValidKinds))
	}
	for kind, want := range map[string]string{"title": "title prompt", "translation": "translation prompt", "vocabulary_item": "vocabulary item prompt"} {
		if defaults[kind] != want {
			t.Errorf("DefaultPrompts()[%q] = %q, want %q", kind, defaults[kind], want)
		}
	}
}

func TestPromptPlaceholders(t *testing.T) {
	if got, want := strings.Join(PromptPlaceholders("title"), ","), "grammarPrompt,transcriptionPrompt,dictionaryPrompt"; got != want {
		t.Errorf("title placeholders = %q, want %q", got, want)
	}
	for kind, want := range map[string]string{
		"vocabulary_item": "sourceLanguage,targetLanguage,phrase,grammarPrompt,transcriptionPrompt,dictionaryPrompt,usage,examples,translation",
		"models_item":     "sourceLanguage,targetLanguage,phrase,grammarPrompt,transcriptionPrompt,dictionaryPrompt",
	} {
		if got := strings.Join(PromptPlaceholders(kind), ","); got != want {
			t.Errorf("%s placeholders = %q, want %q", kind, got, want)
		}
	}
}
