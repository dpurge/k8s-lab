package ai

import (
	"strings"
	"unicode/utf8"
)

// minHalfShare is the smallest share of a chunk, in runes, that either half
// may have for a boundary to be taken. A paragraph break near one end would
// barely shrink the chunk and burn the split depth, so it is passed over for
// a finer boundary.
const minHalfShare = 0.25

// splitHalf cuts chunk in two near its middle, at the coarsest boundary that
// leaves both halves a real share: a paragraph break, else a line break, else
// the end of a sentence, else a word, else (last resort, text without any
// whitespace such as unpunctuated CJK) a rune. separator is what to put
// between the halves when reassembling, so that first + separator + second
// is the chunk again apart from whitespace trimmed at the cut. ok is false
// when chunk is too short to cut.
func splitHalf(chunk string) (first, second, separator string, ok bool) {
	total := utf8.RuneCountInString(chunk)
	if total < 2 {
		return "", "", "", false
	}
	for _, sep := range []string{"\n\n", "\n"} {
		if at, found := nearestBoundary(chunk, total, func(i int, runes []rune) (int, bool) {
			if !hasRunesAt(runes, i, sep) {
				return 0, false
			}
			// A line break that is part of a blank line belongs to the
			// paragraph tier: cutting there would lose the blank line.
			if sep == "\n" && ((i > 0 && runes[i-1] == '\n') || (i+1 < len(runes) && runes[i+1] == '\n')) {
				return 0, false
			}
			return len([]rune(sep)), true
		}); found {
			return finishSplit(chunk, at, len([]rune(sep)), sep)
		}
	}
	if at, width, found := nearestSentenceEnd(chunk, total); found {
		separator = ""
		if width > 0 {
			separator = " " // CJK and Arabic full stops need no space, Latin ones do
		}
		first, second, _, ok = finishSplit(chunk, at, width, separator)
		return first, second, separator, ok
	}
	if at, found := nearestBoundary(chunk, total, func(i int, runes []rune) (int, bool) {
		return 1, runes[i] == ' ' || runes[i] == '\t'
	}); found {
		return finishSplit(chunk, at, 1, " ")
	}
	runes := []rune(chunk)
	return string(runes[:total/2]), string(runes[total/2:]), "", true
}

// finishSplit cuts chunk at rune index at, dropping width runes of boundary.
func finishSplit(chunk string, at, width int, sep string) (string, string, string, bool) {
	runes := []rune(chunk)
	first := strings.TrimRight(string(runes[:at]), " \t\n")
	second := strings.TrimLeft(string(runes[at+width:]), " \t\n")
	return first, second, sep, first != "" && second != ""
}

// nearestBoundary returns the rune index of the boundary closest to the
// middle of chunk for which match reports a boundary, among those leaving
// both halves at least minHalfShare of it.
func nearestBoundary(chunk string, total int, match func(i int, runes []rune) (int, bool)) (int, bool) {
	runes := []rune(chunk)
	mid := total / 2
	best, bestDistance := -1, total+1
	for i := range runes {
		width, ok := match(i, runes)
		if !ok || !bothHalvesBigEnough(i, width, total) {
			continue
		}
		if d := abs(i - mid); d < bestDistance {
			best, bestDistance = i, d
		}
	}
	return best, best >= 0
}

func bothHalvesBigEnough(at, width, total int) bool {
	min := int(float64(total) * minHalfShare)
	return at >= min && total-(at+width) >= min
}

// sentenceEnds are the runes that end a sentence in the scripts phraseforge
// handles: Latin, CJK, Arabic/Persian and Devanagari/Urdu full stops.
const sentenceEnds = ".!?。！？؟۔।"

// nearestSentenceEnd finds the sentence end closest to the middle: the rune
// index where the cut goes (just after the terminator, so it stays with the
// first half) and the width of the whitespace dropped there.
func nearestSentenceEnd(chunk string, total int) (at, width int, found bool) {
	runes := []rune(chunk)
	mid := total / 2
	best, bestWidth, bestDistance := -1, 0, total+1
	for i, r := range runes {
		if !strings.ContainsRune(sentenceEnds, r) {
			continue
		}
		cut := i + 1
		w := 0
		for cut+w < len(runes) && (runes[cut+w] == ' ' || runes[cut+w] == '\t') {
			w++
		}
		// Latin terminators count only before whitespace, so "3.5" or "e.g."
		// inside a sentence is not a boundary; CJK/Arabic ones need none.
		if w == 0 && strings.ContainsRune(".!?", r) {
			continue
		}
		if !bothHalvesBigEnough(cut, w, total) {
			continue
		}
		if d := abs(cut - mid); d < bestDistance {
			best, bestWidth, bestDistance = cut, w, d
		}
	}
	return best, bestWidth, best >= 0
}

func hasRunesAt(runes []rune, i int, s string) bool {
	want := []rune(s)
	if i+len(want) > len(runes) {
		return false
	}
	for j, r := range want {
		if runes[i+j] != r {
			return false
		}
	}
	return true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
