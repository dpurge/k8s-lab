package ai

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkRunes(t *testing.T) {
	cases := map[int]int{
		8192: 10752, // (4096-512)*3
		4096: 4608,  // (2048-512)*3
		0:    4608,  // unset NumCtx is treated as 4096
		-1:   4608,
		1024: 512, // would be 0: floored at minChunkRunes
	}
	for numCtx, want := range cases {
		if got := chunkRunes(numCtx); got != want {
			t.Errorf("chunkRunes(%d) = %d, want %d", numCtx, got, want)
		}
	}
}

func TestSplitChunksShortTextIsOneChunkUnchanged(t *testing.T) {
	text := "First paragraph.\n\nSecond paragraph."
	if got := splitChunks(text, 100); !reflect.DeepEqual(got, []string{text}) {
		t.Errorf("got %q, want the text as a single chunk", got)
	}
}

func TestSplitChunksEmptyYieldsNone(t *testing.T) {
	for _, text := range []string{"", "  \n\n \t "} {
		if got := splitChunks(text, 100); got != nil {
			t.Errorf("splitChunks(%q) = %q, want none", text, got)
		}
	}
}

func TestSplitChunksNormalizesCRLF(t *testing.T) {
	got := splitChunks("one\r\ntwo\r\n\r\nthree", 100)
	if want := []string{"one\ntwo\n\nthree"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSplitChunksPacksParagraphsWithoutCuttingThem(t *testing.T) {
	text := "aaaa aaaa\n\nbbbb bbbb\n\ncccc cccc\n\ndddd dddd"
	got := splitChunks(text, 22) // two 9-rune paragraphs plus the separator fit; three do not
	want := []string{"aaaa aaaa\n\nbbbb bbbb", "cccc cccc\n\ndddd dddd"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSplitChunksCutsOversizeParagraphAtLines(t *testing.T) {
	text := "A: hello there\nB: hi\nA: how are you\nB: fine"
	got := splitChunks(text, 25)
	want := []string{"A: hello there\nB: hi", "A: how are you\nB: fine"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSplitChunksFineCutsAreNotGluedToCoarserParts(t *testing.T) {
	long := "l1 l1 l1\nl2 l2 l2\nl3 l3 l3"
	text := "short\n\n" + long + "\n\ntail"
	got := splitChunks(text, 18)
	want := []string{"short", "l1 l1 l1\nl2 l2 l2", "l3 l3 l3", "tail"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSplitChunksHardSplitIsRuneSafe(t *testing.T) {
	text := strings.Repeat("字", 25) + strings.Repeat("é", 5) // one line, no separators
	got := splitChunks(text, 10)
	for _, chunk := range got {
		if !utf8.ValidString(chunk) || utf8.RuneCountInString(chunk) > 10 {
			t.Errorf("chunk %q is invalid UTF-8 or longer than 10 runes", chunk)
		}
	}
	if len(got) != 3 || strings.Join(got, "") != text {
		t.Errorf("got %d chunks %q, want 3 chunks that rejoin to the text", len(got), got)
	}
}

func TestSplitChunksNeverExceedsLimitAndKeepsEveryWord(t *testing.T) {
	var paragraphs []string
	for i := 0; i < 40; i++ {
		paragraphs = append(paragraphs, strings.Repeat("word ", 3+i%7)+"end")
	}
	text := strings.Join(paragraphs, "\n\n")
	got := splitChunks(text, 60)
	if len(got) < 2 {
		t.Fatalf("got %d chunks, want the text split", len(got))
	}
	for _, chunk := range got {
		if n := utf8.RuneCountInString(chunk); n > 60 {
			t.Errorf("chunk of %d runes exceeds the limit: %q", n, chunk)
		}
	}
	if !reflect.DeepEqual(strings.Fields(strings.Join(got, "\n\n")), strings.Fields(text)) {
		t.Error("the words of the rejoined chunks differ from the original")
	}
}
