package ai

import (
	"context"
	"strings"
	"testing"

	"phraseforge/internal/config"
)

// The correction call carries only the rejected lines and their errors,
// plus the format — neither the lines that parsed nor the source text.
func TestCorrectionMessagesCarryOnlyTheRejectedLines(t *testing.T) {
	msgs := correctionMessages("phrase {grammar} [transcription]", []RejectedLine{
		{Line: "a [x] [y]", Reason: "two groups"},
		{Line: "b [x] [y]", Reason: "two groups too"},
	})
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("messages = %+v, want a system and a user turn", msgs)
	}
	if !strings.Contains(msgs[0].Content, "phrase {grammar} [transcription]") {
		t.Errorf("system turn %q lacks the format", msgs[0].Content)
	}
	for _, want := range []string{"Line: a [x] [y]\nError: two groups", "Line: b [x] [y]\nError: two groups too"} {
		if !strings.Contains(msgs[1].Content, want) {
			t.Errorf("user turn %q lacks %q", msgs[1].Content, want)
		}
	}
}

func TestCorrectLinesSendsOneSmallRequestAndReturnsTheReply(t *testing.T) {
	fake := newFakeOllama(t, []string{"a {noun} [x] = A"})
	s := correctionService(fake.URL)

	got, err := s.correctLines(context.Background(), "generate_vocabulary", Prompt{Provider: "ollama", Model: "test-model"}, config.PurposeConfig{MaxAttempts: 3},
		"phrase {grammar}", []RejectedLine{{Line: "a [noun] [x] = A", Reason: "bad"}})
	if err != nil || got != "a {noun} [x] = A" {
		t.Fatalf("got (%q, %v), want the scripted reply", got, err)
	}
	if len(fake.msgCount) != 1 || fake.msgCount[0] != 2 {
		t.Errorf("requests carried %v messages, want one request with 2", fake.msgCount)
	}
	if !strings.Contains(fake.lastText[0], "Line: a [noun] [x] = A") {
		t.Errorf("request text %q lacks the rejected line", fake.lastText[0])
	}
}
