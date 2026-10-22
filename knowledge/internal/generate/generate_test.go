package generate

import (
	"context"
	"errors"
	"testing"

	"knowledge/internal/config"
	"knowledge/internal/modelrouter"
)

type scripted struct {
	reply string
	err   error
	got   []modelrouter.Message
}

func (s *scripted) Complete(_ context.Context, messages []modelrouter.Message) (string, error) {
	s.got = messages
	return s.reply, s.err
}

func TestTitleAndSummaryEachUseTheirOwnModelCallAndPrompt(t *testing.T) {
	title := &scripted{reply: "  A Title\n"}
	summary := &scripted{reply: "A summary."}
	svc := New(config.Config{
		GenerateTitle:   config.PurposeConfig{Prompt: "title prompt"},
		GenerateSummary: config.PurposeConfig{Prompt: "summary prompt"},
	}, nil, nil, title, summary)

	got, err := svc.llmComplete(context.Background(), svc.title, "body text")
	if err != nil || got != "A Title" {
		t.Fatalf("title = %q, %v; want the trimmed reply", got, err)
	}
	if len(title.got) != 2 || title.got[0].Content != "title prompt" || title.got[1].Content != "body text" || len(summary.got) != 0 {
		t.Errorf("title call messages = %+v (summary saw %+v), want the title prompt and the body on the title model only", title.got, summary.got)
	}

	got, err = svc.llmComplete(context.Background(), svc.summary, "body text")
	if err != nil || got != "A summary." {
		t.Fatalf("summary = %q, %v", got, err)
	}
	if summary.got[0].Content != "summary prompt" {
		t.Errorf("summary system prompt = %q, want the summary prompt", summary.got[0].Content)
	}
}

func TestGenerateReturnsTheModelCallError(t *testing.T) {
	boom := errors.New("all models failed")
	svc := New(config.Config{}, nil, nil, &scripted{err: boom}, &scripted{})
	if _, err := svc.llmComplete(context.Background(), svc.title, "x"); !errors.Is(err, boom) {
		t.Errorf("error = %v, want the model call's error", err)
	}
}
