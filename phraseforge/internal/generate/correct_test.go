package generate

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"phraseforge/internal/checkpoint"
	"phraseforge/internal/vocabulary"
)

// fixes scripts parseWithCorrection's correction calls: each call returns the
// next reply and records exactly which lines were sent.
type fixes struct {
	replies []string
	sent    [][]RejectedLine
	err     error
}

func (f *fixes) fix(_ context.Context, rejected []RejectedLine) (string, error) {
	f.sent = append(f.sent, rejected)
	if f.err != nil {
		return "", f.err
	}
	reply := f.replies[min(len(f.sent)-1, len(f.replies)-1)]
	return reply, nil
}

func phrases(items []vocabulary.Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Phrase)
	}
	return out
}

func TestParseWithCorrectionValidOutputNeedsNoCorrection(t *testing.T) {
	f := &fixes{}
	items, skipped, _, err := parseWithCorrection(context.Background(), "a {noun}\nb {verb}", 3, ParseVocabularyLines, f.fix)
	if err != nil || skipped != 0 || !reflect.DeepEqual(phrases(items), []string{"a", "b"}) {
		t.Fatalf("got (%v, %d, %v)", phrases(items), skipped, err)
	}
	if len(f.sent) != 0 {
		t.Errorf("fix was called %d times, want 0", len(f.sent))
	}
}

// Only the rejected lines go back; the valid ones are kept as they are and
// the corrected ones follow them.
func TestParseWithCorrectionSendsOnlyRejectedLinesAndKeepsTheRest(t *testing.T) {
	f := &fixes{replies: []string{"b {verb} [x]\nc {noun}"}}
	out := "a {noun}\nb [verb] [x]\nc [noun] [z]\nd {noun}"
	items, skipped, _, err := parseWithCorrection(context.Background(), out, 3, ParseVocabularyLines, f.fix)
	if err != nil || skipped != 0 {
		t.Fatalf("got skipped=%d err=%v, want none", skipped, err)
	}
	if want := []string{"a", "d", "b", "c"}; !reflect.DeepEqual(phrases(items), want) {
		t.Errorf("items = %v, want %v", phrases(items), want)
	}
	if len(f.sent) != 1 || len(f.sent[0]) != 2 || f.sent[0][0].Line != "b [verb] [x]" || f.sent[0][1].Line != "c [noun] [z]" {
		t.Errorf("fix received %+v, want exactly the two rejected lines", f.sent)
	}
}

func TestParseWithCorrectionDropsLinesStillBadAfterTheRoundsAndCountsThem(t *testing.T) {
	f := &fixes{replies: []string{"b [verb] [x]"}} // stays malformed every round
	items, skipped, _, err := parseWithCorrection(context.Background(), "a {noun}\nb [verb] [x]\nc [noun] [y]", 2, ParseVocabularyLines, f.fix)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(phrases(items), []string{"a"}) || skipped != 2 {
		t.Errorf("got items=%v skipped=%d, want [a] and 2", phrases(items), skipped)
	}
	if len(f.sent) != 2 {
		t.Errorf("fix called %d times, want the 2 rounds allowed", len(f.sent))
	}
}

func TestParseWithCorrectionBlankOutputIsEmptyNotAnError(t *testing.T) {
	items, skipped, _, err := parseWithCorrection(context.Background(), "  \n", 3, ParseVocabularyLines, (&fixes{}).fix)
	if err != nil || len(items) != 0 || skipped != 0 {
		t.Fatalf("got (%v, %d, %v), want empty and no error", items, skipped, err)
	}
}

// A failing correction call must not throw away the lines that were valid.
func TestParseWithCorrectionFixFailureKeepsValidItems(t *testing.T) {
	f := &fixes{err: errors.New("boom")}
	items, skipped, _, err := parseWithCorrection(context.Background(), "a {noun}\nb [verb] [x]", 3, ParseVocabularyLines, f.fix)
	if err != nil || !reflect.DeepEqual(phrases(items), []string{"a"}) || skipped != 1 {
		t.Fatalf("got (%v, %d, %v), want [a], 1, nil", phrases(items), skipped, err)
	}
	if len(f.sent) != 1 {
		t.Errorf("fix called %d times after failing, want 1", len(f.sent))
	}
}

func TestParseWithCorrectionReturnsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fixes{err: context.Canceled}
	_, _, _, err := parseWithCorrection(ctx, "a {noun}\nb [verb] [x]", 3, ParseVocabularyLines, f.fix)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestParseWithCorrectionWorksForModelsToo(t *testing.T) {
	f := &fixes{replies: []string{"I am ___ [ai æm]"}}
	items, skipped, _, err := parseWithCorrection(context.Background(), "I am ___ [x] [ai æm]", 3, ParseModelsLines, f.fix)
	if err != nil || skipped != 0 || len(items) != 1 || items[0].Transcription != "ai æm" {
		t.Fatalf("got (%+v, %d, %v)", items, skipped, err)
	}
}

// scriptedChunks answers generate with the next scripted reply per chunk and
// records which chunks it was asked for.
type scriptedChunks struct {
	replies []string
	asked   []string
	err     error
}

func (g *scriptedChunks) generate(_ context.Context, chunk string) (string, error) {
	g.asked = append(g.asked, chunk)
	if g.err != nil {
		return "", g.err
	}
	return g.replies[len(g.asked)-1], nil
}

func vocabPhrase(it vocabulary.Item) string { return it.Phrase }

// The production bug: every line rejected, nothing corrected.
func TestGenerateItemsFailsWhenNothingIsValid(t *testing.T) {
	g := &scriptedChunks{replies: []string{"وُلِدَ [verb] [wulida] = was born"}}
	f := &fixes{replies: []string{"still [bad] [x]"}}
	_, _, err := generateItems(context.Background(), []string{"text"}, 3, g.generate, ParseVocabularyLines, f.fix, vocabPhrase, nil)
	if err == nil {
		t.Fatal("got nil error for output with no valid line")
	}
	for _, want := range []string{"no valid lines", "still [bad] [x]", "curly braces"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestGenerateItemsBlankContentIsAnError(t *testing.T) {
	_, _, err := generateItems(context.Background(), nil, 3, (&scriptedChunks{}).generate, ParseVocabularyLines, (&fixes{}).fix, vocabPhrase, nil)
	if err == nil || !strings.Contains(err.Error(), "content is required") {
		t.Fatalf("err = %v, want content is required", err)
	}
}

func TestGenerateItemsBlankReplyIsEmptyNotAnError(t *testing.T) {
	g := &scriptedChunks{replies: []string{"  \n"}}
	items, skipped, err := generateItems(context.Background(), []string{"text"}, 3, g.generate, ParseVocabularyLines, (&fixes{}).fix, vocabPhrase, nil)
	if err != nil || len(items) != 0 || skipped != 0 {
		t.Fatalf("got (%v, %d, %v), want empty and no error", items, skipped, err)
	}
}

// Chunks are generated one at a time, in order; an item an earlier chunk
// already produced is dropped, the first occurrence keeps its place.
func TestGenerateItemsChunksInOrderAndDeduplicatesAcrossChunks(t *testing.T) {
	g := &scriptedChunks{replies: []string{"a {noun}\nb {noun}", "b {noun}\nc {noun}\na {verb}", "d {noun}"}}
	items, skipped, err := generateItems(context.Background(), []string{"one", "two", "three"}, 3, g.generate, ParseVocabularyLines, (&fixes{}).fix, vocabPhrase, nil)
	if err != nil || skipped != 0 {
		t.Fatalf("got skipped=%d err=%v", skipped, err)
	}
	if want := []string{"a", "b", "c", "d"}; !reflect.DeepEqual(phrases(items), want) {
		t.Errorf("items = %v, want %v", phrases(items), want)
	}
	if want := []string{"one", "two", "three"}; !reflect.DeepEqual(g.asked, want) {
		t.Errorf("chunks asked = %v, want %v", g.asked, want)
	}
}

func TestGenerateItemsKeepsRepeatsWithinOneChunk(t *testing.T) {
	g := &scriptedChunks{replies: []string{"a {noun}\na {noun}"}}
	items, _, err := generateItems(context.Background(), []string{"one"}, 3, g.generate, ParseVocabularyLines, (&fixes{}).fix, vocabPhrase, nil)
	if err != nil || len(items) != 2 {
		t.Fatalf("got (%v, %v), want both repeats kept", phrases(items), err)
	}
}

// One chunk with nothing valid must not sink a job whose other chunks worked;
// its rejected lines are counted.
func TestGenerateItemsAChunkWithNothingValidIsSkippedNotFatal(t *testing.T) {
	g := &scriptedChunks{replies: []string{"a {noun}", "x [bad] [y]"}}
	f := &fixes{replies: []string{"x [bad] [y]"}}
	items, skipped, err := generateItems(context.Background(), []string{"one", "two"}, 1, g.generate, ParseVocabularyLines, f.fix, vocabPhrase, nil)
	if err != nil || !reflect.DeepEqual(phrases(items), []string{"a"}) || skipped != 1 {
		t.Fatalf("got (%v, %d, %v), want [a], 1, nil", phrases(items), skipped, err)
	}
}

func TestGenerateItemsNamesTheFailingChunk(t *testing.T) {
	g := &scriptedChunks{err: errors.New("boom")}
	_, _, err := generateItems(context.Background(), []string{"one", "two"}, 3, g.generate, ParseVocabularyLines, (&fixes{}).fix, vocabPhrase, nil)
	if err == nil || !strings.Contains(err.Error(), "chunk 1 of 2") || !errors.Is(err, g.err) {
		t.Fatalf("err = %v, want it to name chunk 1 of 2 and wrap the cause", err)
	}
	_, _, err = generateItems(context.Background(), []string{"only"}, 3, g.generate, ParseVocabularyLines, (&fixes{}).fix, vocabPhrase, nil)
	if err != g.err {
		t.Fatalf("single-chunk err = %v, want the cause unwrapped", err)
	}
}

func TestGenerateItemsStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := &scriptedChunks{replies: []string{"a {noun}"}}
	_, _, err := generateItems(ctx, []string{"one"}, 3, g.generate, ParseVocabularyLines, (&fixes{}).fix, vocabPhrase, nil)
	if !errors.Is(err, context.Canceled) || len(g.asked) != 0 {
		t.Fatalf("err = %v, asked %v, want cancelled before any call", err, g.asked)
	}
}

func TestNoValidLinesErrorTruncatesTheQuotedLines(t *testing.T) {
	long := strings.Repeat("x", 500)
	err := noValidLinesError([]RejectedLine{{Line: long, Reason: "r"}, {Line: "2", Reason: "r"}, {Line: "3", Reason: "r"}, {Line: "4", Reason: "r"}})
	if strings.Contains(err.Error(), long) || strings.Contains(err.Error(), `"4"`) {
		t.Errorf("error %q quotes too much", err)
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

// A job that fails on the second chunk is retried with the checkpoint copied:
// the first chunk, including its correction round, is not paid for again, and
// the outcome is the one an uninterrupted run gives.
func TestGenerateItemsRetryResumesWithoutRepeatingFinishedChunks(t *testing.T) {
	chunks := []string{"one", "two"}
	store := memCheckpoint{}
	ctx := checkpoint.With(context.Background(), store)
	replies := map[string]string{"one": "a {noun}\nb [verb] [x]", "two": "c {noun}"}
	var generated []string
	generate := func(failOn string) func(context.Context, string) (string, error) {
		return func(_ context.Context, chunk string) (string, error) {
			generated = append(generated, chunk)
			if chunk == failOn {
				return "", errors.New("model fell over")
			}
			return replies[chunk], nil
		}
	}
	f := &fixes{replies: []string{"b {verb} [x]"}}
	run := func(failOn string) ([]vocabulary.Item, int, error) {
		return generateItems(ctx, chunks, 3, generate(failOn), ParseVocabularyLines, f.fix, vocabPhrase, checkpoint.Start(ctx, "items", chunks))
	}

	if _, _, err := run("two"); err == nil || !strings.Contains(err.Error(), "chunk 2 of 2") {
		t.Fatalf("first run err = %v, want it to fail on chunk 2 of 2", err)
	}
	generated = nil
	fixesBefore := len(f.sent)

	items, skipped, err := run("")
	if err != nil || skipped != 0 {
		t.Fatalf("retry: skipped=%d err=%v", skipped, err)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(phrases(items), want) {
		t.Errorf("items = %v, want %v", phrases(items), want)
	}
	if want := []string{"two"}; !reflect.DeepEqual(generated, want) {
		t.Errorf("retry generated %v, want only %v", generated, want)
	}
	if len(f.sent) != fixesBefore {
		t.Errorf("retry made %d more correction calls, want none for the finished chunk", len(f.sent)-fixesBefore)
	}
}

// Dedupe across chunks and the skipped count survive a resume.
func TestGenerateItemsResumeKeepsDedupeAndSkippedCounts(t *testing.T) {
	chunks := []string{"one", "two"}
	ctx := checkpoint.With(context.Background(), memCheckpoint{})
	replies := map[string]string{"one": "a {noun}\nx [bad] [y]", "two": "a {verb}\nb {noun}"}
	generate := func(_ context.Context, chunk string) (string, error) { return replies[chunk], nil }
	f := &fixes{replies: []string{"x [bad] [y]"}}
	var want []string
	for range 2 { // the second run is served entirely from the checkpoint
		items, skipped, err := generateItems(ctx, chunks, 1, generate, ParseVocabularyLines, f.fix, vocabPhrase, checkpoint.Start(ctx, "items", chunks))
		if err != nil || skipped != 1 {
			t.Fatalf("skipped=%d err=%v, want 1 and none", skipped, err)
		}
		if want == nil {
			want = phrases(items)
			if !reflect.DeepEqual(want, []string{"a", "b"}) {
				t.Fatalf("items = %v, want [a b]", want)
			}
		} else if !reflect.DeepEqual(phrases(items), want) {
			t.Errorf("resumed items = %v, want %v", phrases(items), want)
		}
	}
	if len(f.sent) != 1 {
		t.Errorf("fix called %d times over both runs, want 1", len(f.sent))
	}
}
