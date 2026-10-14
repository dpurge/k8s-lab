package server

import (
	"context"
	"testing"
)

func TestVocabAPIDetailSaysWhetherTheLanguageHasTranscriptions(t *testing.T) {
	f := newVocabAPI(t)
	ctx := context.Background()
	setNeeds := func(needs bool) {
		t.Helper()
		if _, err := f.srv.db.Exec(ctx, `INSERT INTO ime_config(language, script, needs_transcription) VALUES ('arb','arab',$1)
			ON CONFLICT (language, script) DO UPDATE SET needs_transcription = excluded.needs_transcription`, needs); err != nil {
			t.Fatal(err)
		}
	}
	check := func(name string, want bool) {
		t.Helper()
		if d, _ := f.detail(t, f.adminID); d.NeedsTranscription != want {
			t.Errorf("%s: needsTranscription = %v, want %v", name, d.NeedsTranscription, want)
		}
	}

	setNeeds(false)
	check("not marked, no section", false)
	setNeeds(true)
	check("marked as needing one", true)
	setNeeds(false)
	if _, err := f.srv.db.Exec(ctx, `INSERT INTO language_llm_sections(language, transcription_prompt) VALUES ('arb','Use DIN 31635.')`); err != nil {
		t.Fatal(err)
	}
	check("a transcription section", true)
}
