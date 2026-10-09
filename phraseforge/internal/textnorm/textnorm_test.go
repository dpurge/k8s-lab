package textnorm

import "testing"

func TestPhraseTrimsWhitespace(t *testing.T) {
	if got := Phrase("  cat \t\n"); got != "cat" {
		t.Fatalf("Phrase = %q, want %q", got, "cat")
	}
}

func TestPhraseConvertsToNFC(t *testing.T) {
	decomposed := "é" // e + combining acute accent
	composed := "é"    // é
	if got := Phrase(decomposed); got != composed {
		t.Fatalf("Phrase(%q) = %q, want %q", decomposed, got, composed)
	}
}

func TestPhraseOrdersCombiningMarksCanonically(t *testing.T) {
	// Arabic fatha (U+064E) and shadda (U+0651) in either order are the same
	// text; NFC puts them in canonical order, so both spellings become one.
	a := "سَّ"
	b := "سَّ"
	if Phrase(a) != Phrase(b) {
		t.Fatalf("Phrase(%q) = %q, Phrase(%q) = %q, want equal", a, Phrase(a), b, Phrase(b))
	}
}

func TestPhraseKeepsAlreadyNormalTextAndBlankStaysBlank(t *testing.T) {
	if got := Phrase("كتاب"); got != "كتاب" {
		t.Fatalf("Phrase changed already normal text: %q", got)
	}
	if got := Phrase(" \t "); got != "" {
		t.Fatalf("Phrase of blank = %q, want empty", got)
	}
}
