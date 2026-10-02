package ai

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"k8s-lab/shared/llm"

	"phraseforge/internal/config"
)

// recordingGenerate returns "out<N>" for the Nth chunk and records the chunks
// it was called with, in call order.
func recordingGenerate(seen *[]string) func(context.Context, string) (string, error) {
	return func(_ context.Context, chunk string) (string, error) {
		*seen = append(*seen, chunk)
		return "out" + string(rune('0'+len(*seen))), nil
	}
}

func TestGenerateChunksSingleChunkIsPassedThroughUntouched(t *testing.T) {
	var seen []string
	gen := func(_ context.Context, chunk string) (string, error) {
		seen = append(seen, chunk)
		return "  padded output \n", nil
	}
	got, err := generateChunks(context.Background(), "translation", "text", []string{"only"}, gen)
	if err != nil || got != "  padded output \n" || !reflect.DeepEqual(seen, []string{"only"}) {
		t.Errorf("got (%q, %v) after %q, want Generate's output untouched after one call", got, err, seen)
	}
}

func TestGenerateChunksJoinsTextWithBlankLineAndDialogWithNewline(t *testing.T) {
	for contentType, want := range map[string]string{"text": "out1\n\nout2\n\nout3", "dialog": "out1\nout2\nout3"} {
		var seen []string
		got, err := generateChunks(context.Background(), "translation", contentType, []string{"a", "b", "c"}, recordingGenerate(&seen))
		if err != nil || got != want {
			t.Errorf("%s: got (%q, %v), want %q", contentType, got, err, want)
		}
		if !reflect.DeepEqual(seen, []string{"a", "b", "c"}) {
			t.Errorf("%s: chunks seen in order = %q", contentType, seen)
		}
	}
}

func TestGenerateChunksTitleUsesOnlyTheFirstChunk(t *testing.T) {
	var seen []string
	got, err := generateChunks(context.Background(), "title", "text", []string{"first", "second", "third"}, recordingGenerate(&seen))
	if err != nil || got != "out1" || !reflect.DeepEqual(seen, []string{"first"}) {
		t.Errorf("got (%q, %v) after %q, want one call on the first chunk", got, err, seen)
	}
}

func TestGenerateChunksNamesTheFailingChunk(t *testing.T) {
	boom := errors.New("model fell over")
	calls := 0
	gen := func(context.Context, string) (string, error) {
		calls++
		if calls == 2 {
			return "", boom
		}
		return "ok", nil
	}
	_, err := generateChunks(context.Background(), "translation", "text", []string{"a", "b", "c"}, gen)
	if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "chunk 2 of 3: ") || calls != 2 {
		t.Errorf("err = %v after %d calls, want 'chunk 2 of 3: ...' and no third call", err, calls)
	}
}

func TestGenerateChunksSingleChunkErrorIsNotWrapped(t *testing.T) {
	boom := errors.New("model fell over")
	_, err := generateChunks(context.Background(), "translation", "text", []string{"a"},
		func(context.Context, string) (string, error) { return "", boom })
	if err != boom {
		t.Errorf("err = %v, want the error exactly as Generate would return it", err)
	}
}

func TestGenerateChunksStopsBetweenChunksWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	gen := func(context.Context, string) (string, error) {
		calls++
		cancel()
		return "ok", nil
	}
	_, err := generateChunks(ctx, "translation", "text", []string{"a", "b", "c"}, gen)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Errorf("err = %v after %d calls, want context.Canceled after 1", err, calls)
	}
}

func TestGenerateChunksNoChunksIsAnError(t *testing.T) {
	if _, err := generateChunks(context.Background(), "translation", "text", nil, nil); err == nil {
		t.Error("err = nil, want 'content is required'")
	}
}

// TestChunkedCallsReachTheModelOneChunkAtATime: a long text becomes one
// request per chunk, in order, each carrying exactly that chunk, and the
// outputs come back joined.
func TestChunkedCallsReachTheModelOneChunkAtATime(t *testing.T) {
	text := "para one one\n\npara two two\n\npara three\n\npara four"
	chunks := splitChunks(text, 26)
	if len(chunks) != 2 {
		t.Fatalf("test setup: got %d chunks %q, want 2", len(chunks), chunks)
	}
	fake := newFakeOllama(t, []string{"T1", "T2"})
	s := retryingService(fake.URL, &sleepRecorder{})
	gen := func(ctx context.Context, chunk string) (string, error) {
		return s.callWithRetry(ctx, "translation", Prompt{Provider: "ollama", Model: "test-model"}, config.PurposeConfig{MaxAttempts: 3},
			[]llm.Message{{Role: "user", Content: chunk}})
	}

	got, err := generateChunks(context.Background(), "translation", "text", chunks, gen)
	if err != nil || got != "T1\n\nT2" {
		t.Fatalf("got (%q, %v), want T1\\n\\nT2", got, err)
	}
	if !reflect.DeepEqual(fake.lastText, chunks) {
		t.Errorf("requests carried %q, want the chunks %q in order", fake.lastText, chunks)
	}
}
