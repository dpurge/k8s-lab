package translate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"knowledge/internal/config"
	"knowledge/internal/modelrouter"
)

// scripted is a modelrouter.Completer that records what it was asked and
// answers with a fixed reply or error.
type scripted struct {
	reply string
	err   error
	got   []modelrouter.Message
}

func (s *scripted) Complete(_ context.Context, messages []modelrouter.Message) (string, error) {
	s.got = messages
	return s.reply, s.err
}

func TestTranslateRejectsEmptyText(t *testing.T) {
	svc := New(config.Config{KnowledgeLanguage: "English"}, &scripted{})
	if _, err := svc.Translate(context.Background(), "   "); !errors.Is(err, ErrEmptyText) {
		t.Errorf("Translate(whitespace) error = %v, want ErrEmptyText", err)
	}
}

// TestTranslateSubstitutesAllLanguageOccurrences exercises the real
// Translate() path against a scripted model call, so it covers the actual
// strings.ReplaceAll substitution rather than reimplementing it.
func TestTranslateSubstitutesAllLanguageOccurrences(t *testing.T) {
	model := &scripted{reply: "  bonjour \n"}
	svc := New(config.Config{
		KnowledgeLanguage: "French",
		Translate:         config.PurposeConfig{Prompt: "already in {{language}}, unchanged. Otherwise into {{language}}."},
	}, model)
	out, err := svc.Translate(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Translate() error = %v", err)
	}
	if out != "bonjour" {
		t.Errorf("Translate() = %q, want the reply trimmed", out)
	}
	if len(model.got) != 2 || model.got[0].Role != "system" || model.got[1].Role != "user" || model.got[1].Content != "hello" {
		t.Fatalf("messages = %+v, want a system prompt and the user's text", model.got)
	}
	want := "already in French, unchanged. Otherwise into French."
	if model.got[0].Content != want {
		t.Errorf("system prompt sent to model = %q, want %q", model.got[0].Content, want)
	}
	if strings.Contains(model.got[0].Content, "{{language}}") {
		t.Errorf("system prompt still contains an unsubstituted {{language}} placeholder: %q", model.got[0].Content)
	}
}

func TestTranslateReturnsTheModelCallError(t *testing.T) {
	boom := errors.New("all models failed")
	svc := New(config.Config{KnowledgeLanguage: "English"}, &scripted{err: boom})
	if _, err := svc.Translate(context.Background(), "hello"); !errors.Is(err, boom) {
		t.Errorf("Translate() error = %v, want the model call's error", err)
	}
}
