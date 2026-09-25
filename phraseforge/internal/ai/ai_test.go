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
	listID            int64
	position          int
	phrase            string
	transcription     string
	calls             int
	wantPhrase        string
	alreadyHasContent bool // when false (default), every non-mismatched call applies
}

func (f *fakeItemTranscriptionWriteback) SetItemTranscriptionIfBlank(ctx context.Context, listID int64, position int, phrase, transcription string) (bool, error) {
	if f.wantPhrase != "" && phrase != f.wantPhrase {
		return false, errPhraseMismatch
	}
	if f.alreadyHasContent {
		return false, nil
	}
	f.calls++
	f.listID = listID
	f.position = position
	f.phrase = phrase
	f.transcription = transcription
	return true, nil
}

// fakeItemTranslationWriteback is a fake ItemTranslationWriteback, standing
// in for the main.go-level vocabulary/models adapter in HandleGenerate's
// item-translation writeback dispatch tests. Mirrors
// fakeItemTranscriptionWriteback's wantPhrase/errPhraseMismatch simulation.
type fakeItemTranslationWriteback struct {
	resourceType      string
	listID            int64
	position          int
	phrase            string
	locale            string
	translation       string
	calls             int
	wantPhrase        string
	alreadyHasContent bool // when false (default), every non-mismatched call applies
}

func (f *fakeItemTranslationWriteback) SetItemTranslation(ctx context.Context, resourceType string, listID int64, position int, phrase, locale, translation string) (bool, error) {
	if f.wantPhrase != "" && phrase != f.wantPhrase {
		return false, errPhraseMismatch
	}
	if f.alreadyHasContent {
		return false, nil
	}
	f.calls++
	f.resourceType = resourceType
	f.listID = listID
	f.position = position
	f.phrase = phrase
	f.locale = locale
	f.translation = translation
	return true, nil
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
	if applied, err := svc.writeback(context.Background(), p, "  trxn  "); err != nil || !applied {
		t.Fatalf("writeback: applied=%v err=%v", applied, err)
	}
	if vocab.calls != 1 || vocab.listID != 42 || vocab.position != 3 || vocab.phrase != "phrase-a" || vocab.transcription != "trxn" {
		t.Errorf("vocab writeback = %+v, want calls=1 listID=42 position=3 phrase=phrase-a transcription=%q", vocab, "trxn")
	}
	if modelsW.calls != 0 {
		t.Errorf("modelsW.calls = %d, want 0", modelsW.calls)
	}

	p2 := generatePayload{Kind: "transcription", ResourceType: "models_item", ResourceID: 7, ItemPosition: 0, Content: "phrase-b"}
	if applied, err := svc.writeback(context.Background(), p2, "zero-position"); err != nil || !applied {
		t.Fatalf("writeback: applied=%v err=%v", applied, err)
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
	_, err := svc.writeback(context.Background(), p, "trxn")
	if err == nil {
		t.Fatal("writeback with a stale phrase = nil error, want an error — the job must fail, not silently succeed")
	}
	if vocab.calls != 0 {
		t.Errorf("vocab.calls = %d, want 0 — a phrase mismatch must never be recorded as an applied write", vocab.calls)
	}
}

// TestWritebackItemTranscriptionSkipsWhenAlreadyBlank mirrors
// TestWritebackTitleSkipsWhenAlreadyBlank for the item-transcription path.
func TestWritebackItemTranscriptionSkipsWhenAlreadyBlank(t *testing.T) {
	vocab := &fakeItemTranscriptionWriteback{alreadyHasContent: true}
	svc := &Service{vocabItems: vocab}

	p := generatePayload{Kind: "transcription", ResourceType: "vocabulary_item", ResourceID: 42, ItemPosition: 3, Content: "phrase-a"}
	applied, err := svc.writeback(context.Background(), p, "trxn")
	if err != nil {
		t.Fatalf("writeback: %v, want nil error (a skip is still job success)", err)
	}
	if applied {
		t.Error("applied = true, want false — the item's transcription already had content")
	}
	if vocab.calls != 0 {
		t.Errorf("vocab.calls = %d, want 0 — a skip must never write", vocab.calls)
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
	if applied, err := svc.writeback(context.Background(), p, "  przetlumaczone  "); err != nil || !applied {
		t.Fatalf("writeback: applied=%v err=%v", applied, err)
	}
	if items.calls != 1 || items.resourceType != "vocabulary_item" || items.listID != 42 || items.position != 2 || items.phrase != "phrase-a" || items.locale != "pl" || items.translation != "przetlumaczone" {
		t.Errorf("items writeback = %+v, want the vocabulary_item dispatch fields", items)
	}
	if plain.calls != 0 {
		t.Errorf("plain.calls = %d, want 0 — an item resource type must not fall through to the plain TranslationWriteback path", plain.calls)
	}

	p2 := generatePayload{Kind: "translation", ResourceType: "text", ResourceID: 5, Locale: "en"}
	if applied, err := svc.writeback(context.Background(), p2, "translated"); err != nil || !applied {
		t.Fatalf("writeback: applied=%v err=%v", applied, err)
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
	_, err := svc.writeback(context.Background(), p, "przetlumaczone")
	if err == nil {
		t.Fatal("writeback with a stale phrase = nil error, want an error — the job must fail, not silently succeed")
	}
	if items.calls != 0 {
		t.Errorf("items.calls = %d, want 0 — a phrase mismatch must never be recorded as an applied write", items.calls)
	}
}

// TestWritebackItemTranslationSkipsWhenAlreadyBlank mirrors
// TestWritebackTitleSkipsWhenAlreadyBlank for the item-translation path.
func TestWritebackItemTranslationSkipsWhenAlreadyBlank(t *testing.T) {
	items := &fakeItemTranslationWriteback{alreadyHasContent: true}
	svc := &Service{itemTranslations: items}

	p := generatePayload{Kind: "translation", ResourceType: "vocabulary_item", ResourceID: 42, ItemPosition: 2, Locale: "pl", Content: "phrase-a"}
	applied, err := svc.writeback(context.Background(), p, "przetlumaczone")
	if err != nil {
		t.Fatalf("writeback: %v, want nil error (a skip is still job success)", err)
	}
	if applied {
		t.Error("applied = true, want false — the item already had a translation for this locale")
	}
	if items.calls != 0 {
		t.Errorf("items.calls = %d, want 0 — a skip must never write", items.calls)
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
		if _, err := svc.writeback(context.Background(), p, "   \n\t  "); err == nil {
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
