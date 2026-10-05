package ai

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"phraseforge/internal/config"
)

func TestChunkRunes(t *testing.T) {
	cases := map[int]int{
		8192: 5376, // (4096-512)*1.5
		4096: 2304, // (2048-512)*1.5
		0:    2304, // unset NumCtx is treated as 4096
		-1:   2304,
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

// A chunk plus its reply must fit the window for dense scripts: diacritized
// Arabic measured about 1.6 runes per token on gemma4:12b.
func TestChunkRunesFitDiacritizedArabicWithItsReply(t *testing.T) {
	for _, numCtx := range []int{4096, 8192} {
		inputTokens := float64(chunkRunes(numCtx)) / 1.6
		if total := inputTokens * 2; total > float64(numCtx) {
			t.Errorf("numCtx %d: a full chunk is ~%.0f tokens and needs ~%.0f with an equal reply", numCtx, inputTokens, total)
		}
	}
}

// Generate sends the window it sized chunks for, even when the purpose leaves
// NumCtx unset.
func TestGenerateDefaultMakesNumCtxExplicit(t *testing.T) {
	s := &Service{cfg: config.Config{
		Translation:        config.PurposeConfig{},
		GenerateVocabulary: config.PurposeConfig{NumCtx: 8192},
	}}
	if got := s.generateDefault("translation").NumCtx; got != 4096 {
		t.Errorf("translation NumCtx = %d, want the 4096 default", got)
	}
	if got := s.generateDefault("generate_vocabulary").NumCtx; got != 8192 {
		t.Errorf("generate_vocabulary NumCtx = %d, want the configured 8192", got)
	}
}

func TestEffectiveNumCtx(t *testing.T) {
	for in, want := range map[int]int{0: 4096, -5: 4096, 2048: 2048, 8192: 8192} {
		if got := effectiveNumCtx(in); got != want {
			t.Errorf("effectiveNumCtx(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestChunksForSplitsLongTextForItemGeneration(t *testing.T) {
	s := &Service{cfg: config.Config{GenerateVocabulary: config.PurposeConfig{NumCtx: 8192}}}
	short := strings.Repeat("ا", 3618)
	if got := s.ChunksFor("generate_vocabulary", short); len(got) != 1 {
		t.Errorf("a 3618-rune text (2238 tokens measured) split into %d chunks, want 1", len(got))
	}
	long := strings.Repeat(strings.Repeat("ا", 1000)+"\n\n", 12)
	chunks := s.ChunksFor("generate_vocabulary", long)
	if len(chunks) < 2 {
		t.Fatalf("a 12000-rune text stayed in %d chunk(s), want it split", len(chunks))
	}
	for i, c := range chunks {
		if n := len([]rune(c)); n > 5376 {
			t.Errorf("chunk %d has %d runes, want at most 5376", i, n)
		}
	}
}

// reassemble puts pieces back together with the separators they were cut at.
func reassemble(pieces []chunkPiece) string {
	var b strings.Builder
	for _, p := range pieces {
		b.WriteString(p.sepBefore + p.text)
	}
	return b.String()
}

// The point of recording separators: however finely a text is cut, joining the
// pieces the way they were cut gives the original text back, paragraphs,
// lines and sentences where they were.
func TestSplitChunkPiecesReassembleToTheOriginalAtEverySize(t *testing.T) {
	text := "First paragraph. It has three sentences. The last one costs 3.14 euros.\n\n" +
		"Second paragraph, a single line without any full stop but with quite a few words in it\n" +
		"and a second line that carries two sentences. Here is the second!\n\n" +
		"这是第一句话。这是第二句话。这是第三句话。\n\n" +
		"هذه الجملة الأولى؟ هذه الجملة الثانية؟ وهذه الثالثة.\n\n" +
		strings.Repeat("ا", 40)
	for _, limit := range []int{300, 120, 80, 60, 45, 30, 20, 12, 8} {
		pieces := splitChunkPieces(text, limit)
		if got := reassemble(pieces); got != text {
			t.Errorf("limit %d: reassembled\n%q\nwant\n%q", limit, got, text)
		}
		for i, p := range pieces {
			if n := utf8.RuneCountInString(p.text); n > limit {
				t.Errorf("limit %d: piece %d has %d runes", limit, i, n)
			}
		}
		if len(pieces) > 0 && pieces[0].sepBefore != "" {
			t.Errorf("limit %d: first piece has a separator %q", limit, pieces[0].sepBefore)
		}
	}
}

// A paragraph with no line breaks is cut between sentences, not inside one.
func TestSplitChunkPiecesCutsALongParagraphAtSentenceEnds(t *testing.T) {
	sentences := []string{"Alpha beta gamma.", "Delta epsilon zeta!", "Eta theta iota?", "Kappa lambda mu.", "Nu xi omicron."}
	pieces := splitChunkPieces(strings.Join(sentences, " "), 40)
	if len(pieces) < 2 {
		t.Fatalf("got %d pieces, want the paragraph cut", len(pieces))
	}
	for i, p := range pieces {
		if !strings.ContainsAny(p.text[len(p.text)-1:], ".!?") {
			t.Errorf("piece %d %q does not end at a sentence end", i, p.text)
		}
		if i > 0 && p.sepBefore != " " {
			t.Errorf("piece %d separator = %q, want a space", i, p.sepBefore)
		}
	}
}

func TestSplitChunkPiecesCutsASentenceWithoutPunctuationAtWords(t *testing.T) {
	text := strings.Repeat("word ", 30) + "end"
	pieces := splitChunkPieces(text, 33)
	if reassemble(pieces) != text {
		t.Fatalf("reassembled %q", reassemble(pieces))
	}
	for i, p := range pieces {
		if strings.HasPrefix(p.text, "ord") || strings.HasSuffix(p.text, "wor") {
			t.Errorf("piece %d %q was cut inside a word", i, p.text)
		}
	}
}

func TestSplitChunkPiecesCutsRunesOnlyWhenThereIsNothingElse(t *testing.T) {
	text := strings.Repeat("字", 50)
	pieces := splitChunkPieces(text, 20)
	if len(pieces) != 3 || reassemble(pieces) != text {
		t.Fatalf("got %d pieces reassembling to %d runes", len(pieces), utf8.RuneCountInString(reassemble(pieces)))
	}
	for _, p := range pieces {
		if p.sepBefore != "" {
			t.Errorf("a rune cut has separator %q", p.sepBefore)
		}
	}
}

func TestSplitSentences(t *testing.T) {
	tests := []struct {
		name, text string
		parts      []string
		seps       []string
	}{
		{"latin", "One. Two! Three?", []string{"One.", "Two!", "Three?"}, []string{" ", " "}},
		{"decimals and no space stay whole", "pi is 3.14 or e.g.x here. Done.", []string{"pi is 3.14 or e.g.x here.", "Done."}, []string{" "}},
		{"cjk needs no space", "一。二。三。", []string{"一。", "二。", "三。"}, []string{"", ""}},
		{"arabic", "أولى؟ ثانية؟", []string{"أولى؟", "ثانية؟"}, []string{" "}},
		{"trailing text without a terminator", "End. and then more", []string{"End.", "and then more"}, []string{" "}},
		{"trailing space after the last sentence", "End. ", []string{"End."}, []string{}},
		{"no terminator at all", "just words", []string{"just words"}, []string{}},
	}
	for _, tt := range tests {
		parts, seps := splitSentences(tt.text)
		if !sameStrings(parts, tt.parts) || !sameStrings(seps, tt.seps) {
			t.Errorf("%s: splitSentences(%q) = (%q, %q), want (%q, %q)", tt.name, tt.text, parts, seps, tt.parts, tt.seps)
		}
	}
}

// sameStrings is DeepEqual that does not tell a nil slice from an empty one.
func sameStrings(a, b []string) bool {
	return len(a) == len(b) && (len(a) == 0 || reflect.DeepEqual(a, b))
}
