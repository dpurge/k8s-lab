package ai

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"phraseforge/internal/config"
	"phraseforge/internal/jobs"
)

var modelsInput = ReviewInput{
	Language: "deu", Phrase: "Ich warte auf ...", Transcription: "tr",
	Translations: []ReviewTranslation{{Locale: "pl", Translation: "Czekam na ..."}, {Locale: "en", Translation: "I am waiting for ...; (accusative)"}},
}

func TestReviewModelsPurposeAndPrompt(t *testing.T) {
	if !IsValidKind(KindReviewModels) {
		t.Errorf("%q is not in ValidKinds, so an admin could not override its settings", KindReviewModels)
	}
	for _, p := range PromptPlaceholders(KindReviewModels) {
		if !strings.Contains(config.DefaultReviewModelsPrompt, "{{"+p+"}}") {
			t.Errorf("the default review_models prompt does not use {{%s}}", p)
		}
	}
	got := renderReviewModelsPrompt("{{sourceLanguage}}|{{phrase}}|{{transcription}}|{{translations}}|{{transcriptionPrompt}}|{{x}}",
		ReviewInput{Phrase: "{{translations}}", Translations: []ReviewTranslation{{Locale: "en", Translation: "a"}}}, "German", "rules")
	if want := "German|{{translations}}|none|- en: a|rules|{{x}}"; got != want {
		t.Errorf("render = %q, want %q (one pass, none for no transcription)", got, want)
	}
}

func TestParseReviewModelsReply(t *testing.T) {
	kept := LanguageSections{TranscriptionPrompt: "rules"}
	t.Run("a phrase rewrite comes with the new transcription", func(t *testing.T) {
		raw := `{"phrase":{"ok":false,"suggestion":"Ich warte auf dich","reason":"Concrete."},"transcription":{"ok":false,"suggestion":"tr2","reason":""},"translations":[{"locale":"en","ok":false,"suggestion":"I am waiting for ...","reason":"No explanations."},{"locale":"fr","ok":false,"suggestion":"x","reason":"unknown locale"}]}`
		got, err := parseReviewModelsReply(raw, modelsInput, kept)
		want := []ReviewFinding{
			{Field: ReviewFieldPhrase, Action: ReviewReplace, Current: "Ich warte auf ...", Suggestion: "Ich warte auf dich", Transcription: "tr2", Reason: "Concrete."},
			{Field: ReviewFieldTranslation, Action: ReviewReplace, Locale: "en", Current: "I am waiting for ...; (accusative)", Suggestion: "I am waiting for ...", Reason: "No explanations."},
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, %v\nwant %+v", got, err, want)
		}
	})
	t.Run("a phrase rewrite without a transcription is sent back", func(t *testing.T) {
		raw := `{"phrase":{"ok":false,"suggestion":"Ich warte auf dich","reason":"r"},"transcription":{"ok":true,"suggestion":"","reason":""},"translations":[]}`
		if got, err := parseReviewModelsReply(raw, modelsInput, kept); err == nil {
			t.Errorf("got %+v, want an error asking for the transcription", got)
		}
		// Not needed when the language has no snippet, or the item no transcription.
		if _, err := parseReviewModelsReply(raw, modelsInput, LanguageSections{}); err != nil {
			t.Errorf("no snippet: %v", err)
		}
		bare := modelsInput
		bare.Transcription = ""
		if _, err := parseReviewModelsReply(raw, bare, kept); err != nil {
			t.Errorf("no transcription: %v", err)
		}
	})
	t.Run("a transcription fix alone, only where the language keeps one", func(t *testing.T) {
		raw := `{"phrase":{"ok":true,"suggestion":"","reason":""},"transcription":{"ok":false,"suggestion":"tr2","reason":"Wrong."},"translations":[]}`
		got, err := parseReviewModelsReply(raw, modelsInput, kept)
		want := []ReviewFinding{{Field: ReviewFieldTranscription, Action: ReviewReplace, Current: "tr", Suggestion: "tr2", Reason: "Wrong."}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, %v; want %+v", got, err, want)
		}
		if got, err := parseReviewModelsReply(raw, modelsInput, LanguageSections{}); err != nil || len(got) != 0 {
			t.Errorf("no snippet: got %+v, %v", got, err)
		}
	})
	t.Run("a remark without a fix", func(t *testing.T) {
		raw := `{"phrase":{"ok":false,"suggestion":"","reason":"Unnatural."},"transcription":{"ok":true,"suggestion":"","reason":""},"translations":[]}`
		got, err := parseReviewModelsReply(raw, modelsInput, kept)
		if err != nil || len(got) != 1 || got[0].Action != ReviewComment || got[0].Reason != "Unnatural." {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("a reply of the wrong shape is an error", func(t *testing.T) {
		for _, raw := range []string{"fine", `[]`, `{"transcription":{"ok":true}}`, `{"phrase":null}`} {
			if got, err := parseReviewModelsReply(raw, modelsInput, kept); err == nil {
				t.Errorf("%q: got %+v, want an error", raw, got)
			}
		}
	})
}

type fakeModelsReviewStore = fakeReviewStore

const modelsFixReply = `{"phrase":{"ok":true,"suggestion":"","reason":""},"transcription":{"ok":true,"suggestion":"","reason":""},"translations":[{"locale":"en","ok":false,"suggestion":"I am waiting for ...","reason":"No explanations."}]}`

func modelsReviewService(t *testing.T, replies ...string) (*Service, *fakeOllama) {
	t.Helper()
	pool := newAIDB(t)
	ollama := newFakeOllama(t, replies)
	cfg := config.Config{
		Providers:    map[string]config.ProviderConfig{"ollama": {BaseURL: ollama.URL}},
		ReviewModels: config.PurposeConfig{Provider: "ollama", Model: "review-model", Prompt: "M {{phrase}} ({{sourceLanguage}})\n{{transcription}}\n{{translations}}\n{{transcriptionPrompt}}", MaxAttempts: 3},
	}
	svc := New(pool, cfg, nil, nil, nil, nil)
	if err := svc.SetLanguageSections(context.Background(), LanguageSections{Language: "deu", TranscriptionPrompt: "rules"}); err != nil {
		t.Fatal(err)
	}
	return svc, ollama
}

func TestReviewModelsItemSendsOneCallAndReturnsTheFindings(t *testing.T) {
	s, ollama := modelsReviewService(t, modelsFixReply)
	got, err := s.ReviewModelsItem(context.Background(), modelsInput)
	if err != nil {
		t.Fatal(err)
	}
	want := "M Ich warte auf ... (German)\ntr\n- en: I am waiting for ...; (accusative)\n- pl: Czekam na ...\nrules"
	if len(ollama.lastText) != 1 || ollama.lastText[0] != want {
		t.Errorf("sent %q, want %q", ollama.lastText, want)
	}
	if len(got.Findings) != 1 || got.Findings[0].Field != ReviewFieldTranslation || got.Findings[0].Locale != "en" || string(got.Response) != modelsFixReply {
		t.Errorf("findings %+v response %s", got.Findings, got.Response)
	}
}

func TestReviewModelsItemCorrectsAnInvalidReply(t *testing.T) {
	s, ollama := modelsReviewService(t, `looks fine`, modelsFixReply)
	if _, err := s.ReviewModelsItem(context.Background(), modelsInput); err != nil || len(ollama.msgCount) != 2 || ollama.msgCount[1] != 3 {
		t.Fatalf("err %v messages %v, want one correction round", err, ollama.msgCount)
	}
}

func TestHandleReviewModelsItemStoresOpenProposalsAndSkipsStaleItems(t *testing.T) {
	run := func(store *fakeModelsReviewStore, payload string) (json.RawMessage, error) {
		s, _ := modelsReviewService(t, modelsFixReply)
		if store != nil {
			s.SetModelsReviewStore(store)
		}
		return s.HandleReviewModelsItem(context.Background(), "job-1", json.RawMessage(payload))
	}
	store := &fakeModelsReviewStore{source: modelsInput}
	if _, err := run(store, `{"item_id":7}`); err != nil {
		t.Fatal(err)
	}
	var findings []ReviewFinding
	_ = json.Unmarshal(store.savedFindings, &findings)
	if store.saves != 1 || store.savedItem != 7 || store.savedPhrase != "Ich warte auf ..." || len(findings) != 1 || findings[0].Status != ReviewOpen {
		t.Errorf("saved %d as item %d phrase %q findings %+v", store.saves, store.savedItem, store.savedPhrase, findings)
	}
	if _, err := run(&fakeModelsReviewStore{source: modelsInput, saveErr: ErrItemChanged}, `{"item_id":7}`); !errors.Is(err, jobs.ErrSkipped) {
		t.Errorf("a changed item: err = %v, want a skip", err)
	}
	if _, err := run(&fakeModelsReviewStore{sourceErr: ErrItemGone}, `{"item_id":7}`); !errors.Is(err, jobs.ErrSkipped) {
		t.Errorf("a gone item: err = %v, want a skip", err)
	}
	if _, err := run(nil, `{"item_id":7}`); err == nil {
		t.Error("a missing store must fail the job")
	}
	if _, err := run(store, `{}`); err == nil {
		t.Error("a payload without item_id must fail")
	}
	if job, err := NewReviewModelsJob(9); err != nil || job.Kind != KindReviewModelsItem || string(job.Payload) != `{"item_id":9}` {
		t.Errorf("NewReviewModelsJob = %+v, %v", job, err)
	}
	if _, err := NewReviewModelsJob(0); err == nil {
		t.Error("item 0 must be refused")
	}
}
