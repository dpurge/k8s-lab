package ai

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitHalfPrefersTheParagraphBreakNearestTheMiddle(t *testing.T) {
	chunk := "one one one.\n\ntwo two two two.\n\nthree three.\n\nfour."
	first, second, sep, ok := splitHalf(chunk)
	if !ok || sep != "\n\n" {
		t.Fatalf("got ok=%v sep=%q, want a paragraph split", ok, sep)
	}
	if first != "one one one.\n\ntwo two two two." || second != "three three.\n\nfour." {
		t.Errorf("halves = %q | %q", first, second)
	}
}

// A paragraph break close to one end would barely shrink the chunk, so a
// finer boundary near the middle is taken instead.
func TestSplitHalfSkipsAParagraphBreakTooCloseToTheEnd(t *testing.T) {
	chunk := "tiny.\n\n" + strings.Repeat("a", 60) + "\n" + strings.Repeat("b", 60)
	first, second, sep, ok := splitHalf(chunk)
	if !ok || sep != "\n" || first != "tiny.\n\n"+strings.Repeat("a", 60) || second != strings.Repeat("b", 60) {
		t.Fatalf("got (%q, %q, %q, %v), want a line split", first, second, sep, ok)
	}
}

func TestSplitHalfLineBreakIsNotHalfAParagraphBreak(t *testing.T) {
	// The only break near the middle is the paragraph break; the single
	// newline close to the start is too uneven to use.
	chunk := "a\nbbbbbbbbbb\n\ncccccccccc dddddddddd"
	_, _, sep, ok := splitHalf(chunk)
	if !ok || sep != "\n\n" {
		t.Fatalf("got ok=%v sep=%q, want the paragraph break", ok, sep)
	}
}

func TestSplitHalfFallsBackToSentenceEnds(t *testing.T) {
	cases := map[string]struct{ first, second, sep string }{
		"Latin":  {"First sentence is here.", "Second sentence is here.", " "},
		"CJK":    {"这是第一句话。", "这是第二句话。", ""},
		"Arabic": {"هذه الجملة الأولى؟", "هذه الجملة الثانية؟", " "},
	}
	chunks := map[string]string{
		"Latin":  "First sentence is here. Second sentence is here.",
		"CJK":    "这是第一句话。这是第二句话。",
		"Arabic": "هذه الجملة الأولى؟ هذه الجملة الثانية؟",
	}
	for name, want := range cases {
		first, second, sep, ok := splitHalf(chunks[name])
		if !ok || first != want.first || second != want.second || sep != want.sep {
			t.Errorf("%s: got (%q, %q, %q, %v), want %+v", name, first, second, sep, ok, want)
		}
	}
}

func TestSplitHalfNeverCutsInsideANumberAndFallsBackToAWord(t *testing.T) {
	chunk := "pi is 3.14159 and the other numbers matter a great deal here"
	first, second, sep, ok := splitHalf(chunk)
	if !ok || sep != " " || first+" "+second != chunk {
		t.Fatalf("got (%q, %q, %q, %v), want a word-boundary split that rejoins to the chunk", first, second, sep, ok)
	}
}

func TestSplitHalfLastResortCutsRunesNeverInsideACharacter(t *testing.T) {
	chunk := strings.Repeat("ا", 100) // no boundary of any kind
	first, second, sep, ok := splitHalf(chunk)
	if !ok || sep != "" || utf8.RuneCountInString(first) != 50 || utf8.RuneCountInString(second) != 50 {
		t.Fatalf("got (%d, %d runes, %q, %v)", utf8.RuneCountInString(first), utf8.RuneCountInString(second), sep, ok)
	}
	if !utf8.ValidString(first) || !utf8.ValidString(second) {
		t.Error("a half is not valid UTF-8")
	}
}

func TestSplitHalfTooShortToCut(t *testing.T) {
	for _, chunk := range []string{"", "x"} {
		if _, _, _, ok := splitHalf(chunk); ok {
			t.Errorf("splitHalf(%q) ok = true, want false", chunk)
		}
	}
}
