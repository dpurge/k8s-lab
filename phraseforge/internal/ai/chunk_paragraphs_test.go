package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// echoUpper stands in for a model that answers one paragraph per call.
func echoUpper(calls *[]string) func(context.Context, string) (string, error) {
	return func(_ context.Context, piece string) (string, error) {
		*calls = append(*calls, piece)
		return strings.ToUpper(piece), nil
	}
}

func TestAlignParagraphsKeepsAReplyThatMatches(t *testing.T) {
	var calls []string
	got, err := alignParagraphs(t.Context(), "a\n\nb", "A\n\nB", echoUpper(&calls))
	if err != nil || got != "A\n\nB" || len(calls) != 0 {
		t.Errorf("got %q, %v after %d calls; want the reply unchanged and no call", got, err, len(calls))
	}
}

func TestAlignParagraphsRedoesAChunkParagraphByParagraph(t *testing.T) {
	var calls []string
	// The model merged three paragraphs into two.
	got, err := alignParagraphs(t.Context(), "a\n\nb\n\nc", "A B\n\nC", echoUpper(&calls))
	if err != nil || got != "A\n\nB\n\nC" || strings.Join(calls, "|") != "a|b|c" {
		t.Errorf("got %q, %v, calls %q", got, err, calls)
	}
	// The model split one paragraph into two.
	calls = nil
	if got, err = alignParagraphs(t.Context(), "a\n\nb", "A\n\nB\n\nB2", echoUpper(&calls)); err != nil || got != "A\n\nB" {
		t.Errorf("split reply: got %q, %v", got, err)
	}
}

func TestAlignParagraphsCollapsesBlankLinesInAOneParagraphReply(t *testing.T) {
	var calls []string
	got, err := alignParagraphs(t.Context(), "one paragraph", "A\n\n  \nB", echoUpper(&calls))
	if err != nil || got != "A\nB" || len(calls) != 0 {
		t.Errorf("got %q, %v after %d calls", got, err, len(calls))
	}
	// Also inside a paragraph redone on its own.
	got, err = alignParagraphs(t.Context(), "a\n\nb", "A", func(_ context.Context, p string) (string, error) { return p + "\n\nmore", nil })
	if err != nil || got != "a\nmore\n\nb\nmore" {
		t.Errorf("redone paragraphs: got %q, %v", got, err)
	}
}

func TestAlignParagraphsFailsRatherThanStoreAMisalignedText(t *testing.T) {
	_, err := alignParagraphs(t.Context(), "a\n\nb", "A", func(_ context.Context, p string) (string, error) { return " \n", nil })
	if err == nil || !strings.Contains(err.Error(), "paragraph 1 of 2") {
		t.Errorf("empty reply: err = %v", err)
	}
	boom := errors.New("boom")
	_, err = alignParagraphs(t.Context(), "a\n\nb", "A", func(_ context.Context, p string) (string, error) { return "", boom })
	if !errors.Is(err, boom) {
		t.Errorf("model error: err = %v, want it wrapped", err)
	}
}

func TestAlignParagraphsKeepsABlankParagraphWithoutACall(t *testing.T) {
	var calls []string
	got, err := alignParagraphs(t.Context(), "a\n\n\n\nb", "A B", echoUpper(&calls))
	if err != nil || got != "A\n\n\n\nB" || strings.Join(calls, "|") != "a|b" {
		t.Errorf("got %q, %v, calls %q", got, err, calls)
	}
}
