package ai

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"k8s-lab/shared/llm"

	"phraseforge/internal/checkpoint"
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
	got, err := generateChunks(context.Background(), "translation", "text", []string{"only"}, nil, nil, gen)
	if err != nil || got != "  padded output \n" || !reflect.DeepEqual(seen, []string{"only"}) {
		t.Errorf("got (%q, %v) after %q, want Generate's output untouched after one call", got, err, seen)
	}
}

func TestGenerateChunksJoinsTextWithBlankLineAndDialogWithNewline(t *testing.T) {
	for contentType, want := range map[string]string{"text": "out1\n\nout2\n\nout3", "dialog": "out1\nout2\nout3"} {
		var seen []string
		got, err := generateChunks(context.Background(), "translation", contentType, []string{"a", "b", "c"}, nil, nil, recordingGenerate(&seen))
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
	got, err := generateChunks(context.Background(), "title", "text", []string{"first", "second", "third"}, nil, nil, recordingGenerate(&seen))
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
	_, err := generateChunks(context.Background(), "translation", "text", []string{"a", "b", "c"}, nil, nil, gen)
	if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "chunk 2 of 3: ") || calls != 2 {
		t.Errorf("err = %v after %d calls, want 'chunk 2 of 3: ...' and no third call", err, calls)
	}
}

func TestGenerateChunksSingleChunkErrorIsNotWrapped(t *testing.T) {
	boom := errors.New("model fell over")
	_, err := generateChunks(context.Background(), "translation", "text", []string{"a"}, nil, nil,
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
	_, err := generateChunks(ctx, "translation", "text", []string{"a", "b", "c"}, nil, nil, gen)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Errorf("err = %v after %d calls, want context.Canceled after 1", err, calls)
	}
}

func TestGenerateChunksNoChunksIsAnError(t *testing.T) {
	if _, err := generateChunks(context.Background(), "translation", "text", nil, nil, nil, nil); err == nil {
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

	got, err := generateChunks(context.Background(), "translation", "text", chunks, nil, nil, gen)
	if err != nil || got != "T1\n\nT2" {
		t.Fatalf("got (%q, %v), want T1\\n\\nT2", got, err)
	}
	if !reflect.DeepEqual(fake.lastText, chunks) {
		t.Errorf("requests carried %q, want the chunks %q in order", fake.lastText, chunks)
	}
}

// memCheckpoint is an in-memory checkpoint.Store, standing in for a job row.
type memCheckpoint map[string][]json.RawMessage

func (m memCheckpoint) Load(_ context.Context, key string) ([]json.RawMessage, error) {
	return m[key], nil
}

func (m memCheckpoint) Save(_ context.Context, key string, results []json.RawMessage) error {
	m[key] = results
	return nil
}

// The point of checkpointing: a job that fails on the third of four chunks is
// retried (same checkpoint, as Retry copies it) and only pays for the chunks
// that are not done, and the result is the same as if it had never failed.
func TestGenerateChunksRetryResumesAtTheFirstUnfinishedChunk(t *testing.T) {
	chunks := []string{"a", "b", "c", "d"}
	store := memCheckpoint{}
	ctx := checkpoint.With(context.Background(), store)
	var calls []string
	generate := func(failOn string) func(context.Context, string) (string, error) {
		return func(_ context.Context, chunk string) (string, error) {
			calls = append(calls, chunk)
			if chunk == failOn {
				return "", errors.New("model fell over")
			}
			return strings.ToUpper(chunk), nil
		}
	}

	_, err := generateChunks(ctx, "translation", "text", chunks, nil, checkpoint.Start(ctx, "t", chunks), generate("c"))
	if err == nil || !strings.Contains(err.Error(), "chunk 3 of 4") {
		t.Fatalf("first run err = %v, want it to fail on chunk 3 of 4", err)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("first run called %v, want %v", calls, want)
	}

	calls = nil
	got, err := generateChunks(ctx, "translation", "text", chunks, nil, checkpoint.Start(ctx, "t", chunks), generate(""))
	if err != nil || got != "A\n\nB\n\nC\n\nD" {
		t.Fatalf("retry = (%q, %v), want the complete result", got, err)
	}
	if want := []string{"c", "d"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("retry called %v, want only the unfinished chunks %v", calls, want)
	}
}

func TestGenerateChunksSingleChunkIsCheckpointedToo(t *testing.T) {
	store := memCheckpoint{}
	ctx := checkpoint.With(context.Background(), store)
	calls := 0
	generate := func(context.Context, string) (string, error) { calls++; return "once", nil }
	for range 2 {
		got, err := generateChunks(ctx, "translation", "text", []string{"only"}, nil, checkpoint.Start(ctx, "t", []string{"only"}), generate)
		if err != nil || got != "once" {
			t.Fatalf("got (%q, %v)", got, err)
		}
	}
	if calls != 1 {
		t.Errorf("generate called %d times, want 1: the second run should reuse the saved reply", calls)
	}
}

// A saved reply that no longer decodes is regenerated, not trusted.
func TestGenerateChunksRegeneratesAnUnreadableSavedReply(t *testing.T) {
	chunks := []string{"only"}
	ctx := checkpoint.With(context.Background(), memCheckpoint{})
	checkpoint.Start(ctx, "t", chunks).Record(ctx, 0, json.RawMessage(`{"not":"a string"}`))
	got, err := generateChunks(ctx, "translation", "text", chunks, nil, checkpoint.Start(ctx, "t", chunks),
		func(context.Context, string) (string, error) { return "fresh", nil })
	if err != nil || got != "fresh" {
		t.Fatalf("got (%q, %v), want a fresh reply", got, err)
	}
}

// Replies are joined with the separator their chunk was cut at, so a chunk
// that began a new paragraph and one that continued a sentence differ.
func TestGenerateChunksJoinsWithTheSeparatorsTheTextWasCutAt(t *testing.T) {
	upper := func(_ context.Context, chunk string) (string, error) { return strings.ToUpper(chunk), nil }
	got, err := generateChunks(context.Background(), "translation", "text", []string{"a", "b", "c", "d"}, []string{"", "\n\n", "\n", " "}, nil, upper)
	if err != nil || got != "A\n\nB\nC D" {
		t.Fatalf("got (%q, %v), want A\\n\\nB\\nC D", got, err)
	}
	// Without separators: the default for a text.
	got, err = generateChunks(context.Background(), "translation", "text", []string{"a", "b"}, nil, nil, upper)
	if err != nil || got != "A\n\nB" {
		t.Fatalf("default join = (%q, %v)", got, err)
	}
}
