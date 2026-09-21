package ingest

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkShortInputProducesOneChunk(t *testing.T) {
	text := "A short paragraph, well under the target size."
	chunks, err := chunk(text)
	if err != nil {
		t.Fatalf("chunk() error = %v, want nil", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
	if chunks[0] != text {
		t.Errorf("chunks[0] = %q, want %q", chunks[0], text)
	}
}

func TestChunkMergesParagraphsUnderTarget(t *testing.T) {
	text := "First paragraph.\n\nSecond paragraph.\n\nThird paragraph."
	chunks, err := chunk(text)
	if err != nil {
		t.Fatalf("chunk() error = %v, want nil", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
	want := "First paragraph.\n\nSecond paragraph.\n\nThird paragraph."
	if chunks[0] != want {
		t.Errorf("chunks[0] = %q, want %q", chunks[0], want)
	}
}

func TestChunkSplitsAtParagraphBoundaryPastTarget(t *testing.T) {
	// Two paragraphs each just over half the target: together they exceed
	// targetChunkRunes, so they must land in separate chunks.
	first := strings.Repeat("a", 2500)
	second := strings.Repeat("b", 2500)
	text := first + "\n\n" + second

	chunks, err := chunk(text)
	if err != nil {
		t.Fatalf("chunk() error = %v, want nil", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2", len(chunks))
	}
	if chunks[0] != first {
		t.Errorf("chunks[0] = %q, want %q", chunks[0], first)
	}
	if chunks[1] != second {
		t.Errorf("chunks[1] = %q, want %q", chunks[1], second)
	}
}

func TestChunkPreSplitsOversizedParagraphAtWordBoundary(t *testing.T) {
	word := "lorem "
	text := strings.Repeat(word, 1000) // 6000 runes, well over maxChunkRunes

	chunks, err := chunk(text)
	if err != nil {
		t.Fatalf("chunk() error = %v, want nil", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("len(chunks) = %d, want at least 2", len(chunks))
	}
	for i, c := range chunks {
		if n := utf8.RuneCountInString(c); n > maxChunkRunes {
			t.Errorf("chunks[%d] has %d runes, want <= %d", i, n, maxChunkRunes)
		}
		if strings.HasPrefix(c, " ") || strings.HasSuffix(c, " ") {
			t.Errorf("chunks[%d] = %q, want no leading/trailing space from the split point", i, c)
		}
	}
}

func TestChunkHardCutsOversizedParagraphWithNoBreaks(t *testing.T) {
	// No spaces or newlines anywhere, non-ASCII so a byte-index cut would
	// corrupt a multi-byte rune.
	text := strings.Repeat("日本語", 2000) // 6000 runes, no break characters

	chunks, err := chunk(text)
	if err != nil {
		t.Fatalf("chunk() error = %v, want nil", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("len(chunks) = %d, want at least 2", len(chunks))
	}

	var rebuilt strings.Builder
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Errorf("chunks[%d] is not valid UTF-8: %q", i, c)
		}
		if n := utf8.RuneCountInString(c); n > maxChunkRunes {
			t.Errorf("chunks[%d] has %d runes, want <= %d", i, n, maxChunkRunes)
		}
		rebuilt.WriteString(c)
	}
	if got := rebuilt.String(); got != text {
		t.Errorf("rejoined chunks = %q, want original text (no runes lost or corrupted)", got)
	}
}

func TestChunkMergesShortFinalChunkIntoPrevious(t *testing.T) {
	// A first paragraph close to the target, followed by a small trailing
	// paragraph that can't join it (target would be exceeded) but is small
	// enough to be merged in after the fact.
	first := strings.Repeat("a", 3900)
	tail := strings.Repeat("b", 100)
	text := first + "\n\n" + tail

	chunks, err := chunk(text)
	if err != nil {
		t.Fatalf("chunk() error = %v, want nil", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1 (short tail merged into previous)", len(chunks))
	}
	want := first + "\n\n" + tail
	if chunks[0] != want {
		t.Errorf("chunks[0] = %q, want %q", chunks[0], want)
	}
}

func TestChunkRejectsWhitespaceOnlyInput(t *testing.T) {
	chunks, err := chunk("   \n\n\t  ")
	if !errors.Is(err, ErrNoContent) {
		t.Errorf("chunk(whitespace) error = %v, want ErrNoContent", err)
	}
	if chunks != nil {
		t.Errorf("chunk(whitespace) chunks = %v, want nil", chunks)
	}
}

func TestChunkRejectsEmptyInput(t *testing.T) {
	chunks, err := chunk("")
	if !errors.Is(err, ErrNoContent) {
		t.Errorf("chunk(\"\") error = %v, want ErrNoContent", err)
	}
	if chunks != nil {
		t.Errorf("chunk(\"\") chunks = %v, want nil", chunks)
	}
}

func TestChunkErrorsWhenExceedingMaxChunks(t *testing.T) {
	// Each paragraph is just over the target on its own, so none can merge
	// with a neighbor: one paragraph in, one chunk out.
	paragraph := strings.Repeat("a", targetChunkRunes+1)
	paragraphs := make([]string, maxChunks+1)
	for i := range paragraphs {
		paragraphs[i] = paragraph
	}
	text := strings.Join(paragraphs, "\n\n")

	chunks, err := chunk(text)
	if err == nil {
		t.Fatal("chunk() error = nil, want error for exceeding maxChunks")
	}
	if chunks != nil {
		t.Errorf("chunk() chunks = %v, want nil on error", chunks)
	}
	if !strings.Contains(err.Error(), "exceeds cap") {
		t.Errorf("chunk() error = %q, want it to mention the cap", err.Error())
	}
}

func TestChunkHandlesCJKTextWithoutCorruption(t *testing.T) {
	first := strings.Repeat("日本語のテスト文章です。", 20)
	second := strings.Repeat("これは二つ目の段落です。", 20)
	text := first + "\n\n" + second

	chunks, err := chunk(text)
	if err != nil {
		t.Fatalf("chunk() error = %v, want nil", err)
	}
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Errorf("chunks[%d] is not valid UTF-8: %q", i, c)
		}
	}
	if got, want := strings.Join(chunks, "\n\n"), first+"\n\n"+second; got != want {
		t.Errorf("rejoined chunks = %q, want %q", got, want)
	}
}
