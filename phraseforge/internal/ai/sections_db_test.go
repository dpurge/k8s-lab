package ai

import (
	"context"
	"reflect"
	"testing"

	"phraseforge/internal/config"
)

// TestLanguageSectionsDictionaryRoundTrip: the third snippet is stored,
// listed and overwritten with the other two, and a language without a row
// gets blank sections.
func TestLanguageSectionsDictionaryRoundTrip(t *testing.T) {
	pool := newAIDB(t)
	svc := New(pool, config.Config{}, nil, nil, nil, nil)
	ctx := context.Background()

	if got, err := svc.GetLanguageSections(ctx, "deu"); err != nil || got != (LanguageSections{Language: "deu"}) {
		t.Fatalf("no row: got %+v, %v; want blank sections", got, err)
	}

	want := LanguageSections{Language: "deu", GrammarPrompt: "N m f n", TranscriptionPrompt: "", DictionaryPrompt: "Nouns with the article."}
	if err := svc.SetLanguageSections(ctx, want); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := svc.GetLanguageSections(ctx, "deu")
	if err != nil || got != want {
		t.Fatalf("get = %+v, %v; want %+v", got, err, want)
	}
	list, err := svc.ListLanguageSections(ctx)
	if err != nil || !reflect.DeepEqual(list, []LanguageSections{want}) {
		t.Fatalf("list = %+v, %v; want [%+v]", list, err, want)
	}

	want.DictionaryPrompt = ""
	if err := svc.SetLanguageSections(ctx, want); err != nil {
		t.Fatalf("set again: %v", err)
	}
	if got, _ := svc.GetLanguageSections(ctx, "deu"); got.DictionaryPrompt != "" || got.GrammarPrompt != "N m f n" {
		t.Errorf("blanking the dictionary snippet changed %+v", got)
	}
}
