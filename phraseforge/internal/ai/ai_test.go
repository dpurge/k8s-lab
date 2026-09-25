package ai

import (
	"context"
	"errors"
	"testing"

	"phraseforge/internal/config"
)

// TestPurposeDefault covers the five kinds Generate supports plus the
// lenient "unrecognized kind falls back to Translation" behavior this
// package has always had.
func TestPurposeDefault(t *testing.T) {
	cfg := config.Config{
		Transcription: config.PurposeConfig{Model: "transcription-model"},
		Translation:   config.PurposeConfig{Model: "translation-model"},
		Title:         config.PurposeConfig{Model: "title-model"},
		ProcessText:   config.PurposeConfig{Model: "process-text-model"},
		ProcessDialog: config.PurposeConfig{Model: "process-dialog-model"},
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
		if got := svc.purposeDefault(c.kind).Model; got != c.want {
			t.Errorf("purposeDefault(%q).Model = %q, want %q", c.kind, got, c.want)
		}
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
}

func (f *fakeTextWriteback) SetTitle(ctx context.Context, id int64, title string) error {
	f.titleCalls++
	f.title = title
	return nil
}
func (f *fakeTextWriteback) SetTranscription(ctx context.Context, id int64, transcription string) error {
	f.calls++
	f.transcription = transcription
	return nil
}

// fakeItemTranscriptionWriteback is a fake ItemTranscriptionWriteback,
// recording the (listID, position, phrase, transcription) it was called
// with — stands in for *vocabulary.Store/*models.Store in HandleGenerate's
// item-transcription writeback dispatch tests. If wantPhrase is set and
// doesn't match the phrase passed in, it returns errPhraseMismatch instead
// of recording the call — simulating the real stores' guarded UPDATE
// affecting zero rows when the item at that position has moved or been
// replaced since the job was enqueued (see B3 in
// specs/features/phraseforge-export-import.md).
type fakeItemTranscriptionWriteback struct {
	listID        int64
	position      int
	phrase        string
	transcription string
	calls         int
	wantPhrase    string
}

func (f *fakeItemTranscriptionWriteback) SetItemTranscription(ctx context.Context, listID int64, position int, phrase, transcription string) error {
	if f.wantPhrase != "" && phrase != f.wantPhrase {
		return errPhraseMismatch
	}
	f.calls++
	f.listID = listID
	f.position = position
	f.phrase = phrase
	f.transcription = transcription
	return nil
}

// fakeItemTranslationWriteback is a fake ItemTranslationWriteback, standing
// in for the main.go-level vocabulary/models adapter in HandleGenerate's
// item-translation writeback dispatch tests. Mirrors
// fakeItemTranscriptionWriteback's wantPhrase/errPhraseMismatch simulation.
type fakeItemTranslationWriteback struct {
	resourceType string
	listID       int64
	position     int
	phrase       string
	locale       string
	translation  string
	calls        int
	wantPhrase   string
}

func (f *fakeItemTranslationWriteback) SetItemTranslation(ctx context.Context, resourceType string, listID int64, position int, phrase, locale, translation string) error {
	if f.wantPhrase != "" && phrase != f.wantPhrase {
		return errPhraseMismatch
	}
	f.calls++
	f.resourceType = resourceType
	f.listID = listID
	f.position = position
	f.phrase = phrase
	f.locale = locale
	f.translation = translation
	return nil
}

// errPhraseMismatch stands in for vocabulary.ErrItemNotFound/
// models.ErrItemNotFound (the ai package never imports either store package
// directly, so it can't reference those sentinels by name) — the real
// stores' guarded UPDATE returns exactly this class of error when phrase no
// longer matches what's stored at (listID, position).
var errPhraseMismatch = errors.New("item phrase mismatch: item has moved or been replaced")

// fakeTranslationWriteback is a fake TranslationWriteback (the plain,
// non-item resource path) — used to confirm an item resource type never
// falls through to it.
type fakeTranslationWriteback struct {
	calls int
}

func (f *fakeTranslationWriteback) Set(ctx context.Context, resourceType string, resourceID int64, locale, text string) error {
	f.calls++
	return nil
}

// TestWritebackTitle covers HandleGenerate's newly-wired "title" dispatch:
// a text/dialog resource type reaches TextWriteback.SetTitle (previously
// dead code — see writeback's doc comment history), while any other
// resource type (vocabulary/models items have no title) is a per-job error.
func TestWritebackTitle(t *testing.T) {
	texts := &fakeTextWriteback{}
	dialogs := &fakeTextWriteback{}
	svc := &Service{texts: texts, dialogs: dialogs}

	if err := svc.writeback(context.Background(), generatePayload{Kind: "title", ResourceType: "text", ResourceID: 1}, "  A Title  "); err != nil {
		t.Fatalf("writeback: %v", err)
	}
	if texts.titleCalls != 1 || texts.title != "A Title" {
		t.Errorf("texts.{titleCalls,title} = %d,%q, want 1,%q", texts.titleCalls, texts.title, "A Title")
	}
	if dialogs.titleCalls != 0 {
		t.Errorf("dialogs.titleCalls = %d, want 0", dialogs.titleCalls)
	}

	if err := svc.writeback(context.Background(), generatePayload{Kind: "title", ResourceType: "dialog", ResourceID: 1}, "Dialog Title"); err != nil {
		t.Fatalf("writeback: %v", err)
	}
	if dialogs.titleCalls != 1 || dialogs.title != "Dialog Title" {
		t.Errorf("dialogs.{titleCalls,title} = %d,%q, want 1,%q", dialogs.titleCalls, dialogs.title, "Dialog Title")
	}

	err := svc.writeback(context.Background(), generatePayload{Kind: "title", ResourceType: "vocabulary_item", ResourceID: 1}, "x")
	if err == nil {
		t.Fatal("writeback(title, vocabulary_item) = nil error, want an error — vocabulary items have no title")
	}
}

// TestWritebackItemTranscription covers HandleGenerate's new item-level
// transcription dispatch: resource type selects vocabulary vs. models,
// position 0 (a valid real position) must still dispatch — resource type
// alone, not a zero-check on ItemPosition, disambiguates — and the payload's
// Content (the item's phrase, per buildBackfillPayload) is threaded through
// as the writeback's stale-target guard.
func TestWritebackItemTranscription(t *testing.T) {
	vocab := &fakeItemTranscriptionWriteback{}
	modelsW := &fakeItemTranscriptionWriteback{}
	svc := &Service{vocabItems: vocab, modelsItems: modelsW}

	p := generatePayload{Kind: "transcription", ResourceType: "vocabulary_item", ResourceID: 42, ItemPosition: 3, Content: "phrase-a"}
	if err := svc.writeback(context.Background(), p, "  trxn  "); err != nil {
		t.Fatalf("writeback: %v", err)
	}
	if vocab.calls != 1 || vocab.listID != 42 || vocab.position != 3 || vocab.phrase != "phrase-a" || vocab.transcription != "trxn" {
		t.Errorf("vocab writeback = %+v, want calls=1 listID=42 position=3 phrase=phrase-a transcription=%q", vocab, "trxn")
	}
	if modelsW.calls != 0 {
		t.Errorf("modelsW.calls = %d, want 0", modelsW.calls)
	}

	p2 := generatePayload{Kind: "transcription", ResourceType: "models_item", ResourceID: 7, ItemPosition: 0, Content: "phrase-b"}
	if err := svc.writeback(context.Background(), p2, "zero-position"); err != nil {
		t.Fatalf("writeback: %v", err)
	}
	if modelsW.calls != 1 || modelsW.listID != 7 || modelsW.position != 0 || modelsW.phrase != "phrase-b" {
		t.Errorf("modelsW writeback = %+v, want calls=1 listID=7 position=0 phrase=phrase-b — position 0 must dispatch, not be treated as unset", modelsW)
	}
}

// TestWritebackItemTranscriptionPhraseMismatchFailsJob covers B3
// (specs/features/phraseforge-export-import.md): if the item at (listID,
// position) no longer has the phrase the job was generated for — it moved
// via a manual delete or a re-import while this job sat queued —
// SetItemTranscription's guarded UPDATE affects zero rows and returns an
// error; writeback must propagate that error (failing the job) rather than
// treating it as a silent no-op success.
func TestWritebackItemTranscriptionPhraseMismatchFailsJob(t *testing.T) {
	vocab := &fakeItemTranscriptionWriteback{wantPhrase: "original-phrase"}
	svc := &Service{vocabItems: vocab}

	p := generatePayload{Kind: "transcription", ResourceType: "vocabulary_item", ResourceID: 42, ItemPosition: 3, Content: "a-different-phrase"}
	err := svc.writeback(context.Background(), p, "trxn")
	if err == nil {
		t.Fatal("writeback with a stale phrase = nil error, want an error — the job must fail, not silently succeed")
	}
	if vocab.calls != 0 {
		t.Errorf("vocab.calls = %d, want 0 — a phrase mismatch must never be recorded as an applied write", vocab.calls)
	}
}

// TestWritebackItemTranslation covers HandleGenerate's new item-level
// translation dispatch: a vocabulary_item/models_item resource type reaches
// ItemTranslationWriteback, never the plain TranslationWriteback used for
// text/dialog resource types, and the payload's Content (the item's phrase)
// is threaded through as the writeback's stale-target guard.
func TestWritebackItemTranslation(t *testing.T) {
	items := &fakeItemTranslationWriteback{}
	plain := &fakeTranslationWriteback{}
	svc := &Service{itemTranslations: items, translations: plain}

	p := generatePayload{Kind: "translation", ResourceType: "vocabulary_item", ResourceID: 42, ItemPosition: 2, Locale: "pl", Content: "phrase-a"}
	if err := svc.writeback(context.Background(), p, "  przetlumaczone  "); err != nil {
		t.Fatalf("writeback: %v", err)
	}
	if items.calls != 1 || items.resourceType != "vocabulary_item" || items.listID != 42 || items.position != 2 || items.phrase != "phrase-a" || items.locale != "pl" || items.translation != "przetlumaczone" {
		t.Errorf("items writeback = %+v, want the vocabulary_item dispatch fields", items)
	}
	if plain.calls != 0 {
		t.Errorf("plain.calls = %d, want 0 — an item resource type must not fall through to the plain TranslationWriteback path", plain.calls)
	}

	p2 := generatePayload{Kind: "translation", ResourceType: "text", ResourceID: 5, Locale: "en"}
	if err := svc.writeback(context.Background(), p2, "translated"); err != nil {
		t.Fatalf("writeback: %v", err)
	}
	if plain.calls != 1 {
		t.Errorf("plain.calls = %d, want 1 — a text resource type must still use the plain TranslationWriteback path", plain.calls)
	}
}

// TestWritebackItemTranslationPhraseMismatchFailsJob mirrors
// TestWritebackItemTranscriptionPhraseMismatchFailsJob for the translation
// writeback path (B3).
func TestWritebackItemTranslationPhraseMismatchFailsJob(t *testing.T) {
	items := &fakeItemTranslationWriteback{wantPhrase: "original-phrase"}
	svc := &Service{itemTranslations: items}

	p := generatePayload{Kind: "translation", ResourceType: "vocabulary_item", ResourceID: 42, ItemPosition: 2, Locale: "pl", Content: "a-different-phrase"}
	err := svc.writeback(context.Background(), p, "przetlumaczone")
	if err == nil {
		t.Fatal("writeback with a stale phrase = nil error, want an error — the job must fail, not silently succeed")
	}
	if items.calls != 0 {
		t.Errorf("items.calls = %d, want 0 — a phrase mismatch must never be recorded as an applied write", items.calls)
	}
}

// TestWritebackRejectsEmptyResultForNewPaths extends
// TestWritebackRejectsEmptyResult's coverage to the title/item-transcription/
// item-translation paths added in this pass — none of them may reach their
// writeback interface with an empty result either.
func TestWritebackRejectsEmptyResultForNewPaths(t *testing.T) {
	texts := &fakeTextWriteback{}
	vocab := &fakeItemTranscriptionWriteback{}
	items := &fakeItemTranslationWriteback{}
	svc := &Service{texts: texts, vocabItems: vocab, itemTranslations: items}

	cases := []generatePayload{
		{Kind: "title", ResourceType: "text", ResourceID: 1},
		{Kind: "transcription", ResourceType: "vocabulary_item", ResourceID: 1, ItemPosition: 0},
		{Kind: "translation", ResourceType: "vocabulary_item", ResourceID: 1, ItemPosition: 0, Locale: "en"},
	}
	for _, p := range cases {
		if err := svc.writeback(context.Background(), p, "   \n\t  "); err == nil {
			t.Errorf("writeback(kind=%q, empty result) = nil error, want an error", p.Kind)
		}
	}
	if texts.titleCalls != 0 || vocab.calls != 0 || items.calls != 0 {
		t.Errorf("an empty result must never reach any writeback interface: titleCalls=%d vocabCalls=%d itemsCalls=%d", texts.titleCalls, vocab.calls, items.calls)
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

	err := svc.writeback(context.Background(), p, "   \n\t  ")
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

	if err := svc.writeback(context.Background(), p, "  hello  \n"); err != nil {
		t.Fatalf("writeback: %v", err)
	}
	if texts.transcription != "hello" {
		t.Errorf("SetTranscription called with %q, want trimmed %q", texts.transcription, "hello")
	}
}
