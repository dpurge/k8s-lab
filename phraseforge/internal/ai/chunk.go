package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"phraseforge/internal/checkpoint"
)

const (
	kindTitle         = "title"
	contentTypeDialog = "dialog"
)

const (
	// defaultNumCtx is the window used for a purpose whose NumCtx is 0. It is
	// Ollama's own default for the CPU-only prod node and the dev machine
	// (both report context_length 4096), and is sent explicitly with the
	// call, so the chunk size and the call agree on the window instead of
	// the call quietly running under whatever the server defaults to.
	defaultNumCtx = 4096

	// minChunkRunes keeps a tiny configured NumCtx from producing chunks too
	// small to be useful.
	minChunkRunes = 512

	// runesPerToken is the runes-per-token assumed when sizing chunks, for
	// every script. Measured for diacritized Arabic on gemma4:12b: 3618
	// runes -> 2238 tokens (about 1.6), where the 3 assumed earlier (a hedge
	// below the ~4 usually assumed for prose) let a chunk plus its reply
	// overflow the window, which Ollama truncates silently. Costs about twice
	// the calls for Latin text; completeness over speed.
	runesPerToken = 1.5
)

// effectiveNumCtx is n, or defaultNumCtx when n is unset (zero or negative).
func effectiveNumCtx(n int) int {
	if n <= 0 {
		return defaultNumCtx
	}
	return n
}

// chunkRunes is how many runes of input one LLM call should carry for a
// context window of numCtx tokens. Half the window goes to the input (the
// reply is about as long and shares the window), minus 512 tokens for the
// system prompt and the call's preamble, at runesPerToken runes per token.
func chunkRunes(numCtx int) int {
	return max(int(float64(effectiveNumCtx(numCtx)/2-512)*runesPerToken), minChunkRunes)
}

// chunkPiece is one chunk of a split text with what separated it from the
// chunk before it in the original (a blank line, a line break, a space, or
// nothing), so replies can be put back together the way the text was cut.
type chunkPiece struct {
	text      string
	sepBefore string // empty for the first chunk
}

// splitChunks cuts text into pieces of at most maxRunes runes, see
// splitChunkPieces. Text that already fits is returned as a single chunk.
// Whitespace-only text yields no chunks.
func splitChunks(text string, maxRunes int) []string {
	pieces := splitChunkPieces(text, maxRunes)
	if pieces == nil {
		return nil
	}
	chunks := make([]string, len(pieces))
	for i, p := range pieces {
		chunks[i] = p.text
	}
	return chunks
}

// splitChunkPieces cuts text at the coarsest boundary that fits: whole
// paragraphs (blank-line separated) are packed together; a paragraph that is
// itself too long is cut at line breaks, which for a dialog are the turn
// boundaries; a line that is still too long is cut at sentence ends, then at
// words, and only text without any of those (unpunctuated CJK, say) is cut by
// runes as a last resort. Each piece records the separator it was cut at.
func splitChunkPieces(text string, maxRunes int) []chunkPiece {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes {
		return []chunkPiece{{text: text}}
	}
	return splitBy(text, chunkTiers, maxRunes)
}

// tier cuts text into parts at one kind of boundary: seps[i] is what stood
// between parts[i] and parts[i+1] in the text, so joining parts with seps
// gives the text back.
type tier func(text string) (parts, seps []string)

// chunkTiers are the boundaries to cut at, coarsest first.
var chunkTiers = []tier{splitParagraphs, splitLines, splitSentences, splitWords}

func splitParagraphs(text string) ([]string, []string) { return splitOn(text, "\n\n") }
func splitLines(text string) ([]string, []string)      { return splitOn(text, "\n") }

func splitWords(text string) ([]string, []string) { return splitOn(text, " ") }

func splitOn(text, sep string) ([]string, []string) {
	parts := strings.Split(text, sep)
	seps := make([]string, max(len(parts)-1, 0))
	for i := range seps {
		seps[i] = sep
	}
	return parts, seps
}

// splitSentences cuts text after each sentence end (see sentenceEnds; a Latin
// terminator counts only before whitespace, so "3.14" stays whole). The
// whitespace after a sentence is its separator; CJK and Arabic full stops
// need none.
func splitSentences(text string) ([]string, []string) {
	runes := []rune(text)
	var parts, seps []string
	start := 0
	for i := 0; i < len(runes); i++ {
		if !strings.ContainsRune(sentenceEnds, runes[i]) {
			continue
		}
		end := i + 1
		space := end
		for space < len(runes) && (runes[space] == ' ' || runes[space] == '\t') {
			space++
		}
		if space == end && space < len(runes) && strings.ContainsRune(".!?", runes[i]) {
			continue // "3.14", "e.g.x": not a sentence end
		}
		parts = append(parts, string(runes[start:end]))
		seps = append(seps, string(runes[end:space]))
		start, i = space, space-1
	}
	if start < len(runes) {
		parts = append(parts, string(runes[start:]))
	} else if len(seps) > 0 {
		seps = seps[:len(seps)-1] // trailing whitespace after the last sentence
	}
	return parts, seps
}

// splitBy packs the parts of text cut by tiers[0] greedily into chunks of at
// most maxRunes runes; a part that is too long on its own is split further by
// the next tier and its pieces stand as chunks of their own, so a finer cut
// is never glued to a coarser one. With no tiers left it cuts by runes.
func splitBy(text string, tiers []tier, maxRunes int) []chunkPiece {
	if len(tiers) == 0 {
		var pieces []chunkPiece
		for _, c := range cutRunes(text, maxRunes) {
			pieces = append(pieces, chunkPiece{text: c})
		}
		return pieces
	}
	parts, seps := tiers[0](text)
	var pieces []chunkPiece
	current, currentSep := "", ""
	flush := func() {
		if strings.TrimSpace(current) != "" {
			pieces = append(pieces, chunkPiece{text: current, sepBefore: currentSep})
		}
		current = ""
	}
	for i, part := range parts {
		sepBefore := ""
		if i > 0 {
			sepBefore = seps[i-1]
		}
		switch {
		case utf8.RuneCountInString(part) > maxRunes:
			flush()
			finer := splitBy(part, tiers[1:], maxRunes)
			if len(finer) > 0 {
				finer[0].sepBefore = sepBefore
			}
			pieces = append(pieces, finer...)
		case current == "":
			current, currentSep = part, sepBefore
		case utf8.RuneCountInString(current)+utf8.RuneCountInString(sepBefore)+utf8.RuneCountInString(part) <= maxRunes:
			current += sepBefore + part
		default:
			flush()
			current, currentSep = part, sepBefore
		}
	}
	flush()
	return pieces
}

// cutRunes splits text every maxRunes runes, never inside a UTF-8 sequence.
func cutRunes(text string, maxRunes int) []string {
	runes := []rune(text)
	var chunks []string
	for len(runes) > maxRunes {
		chunks = append(chunks, string(runes[:maxRunes]))
		runes = runes[maxRunes:]
	}
	if len(runes) > 0 {
		chunks = append(chunks, string(runes))
	}
	return chunks
}

// ChunksFor splits content into pieces that each fit one call of kind
// together with its reply (see chunkRunes). Callers that must parse each reply
// on its own, such as item generation, generate chunk by chunk themselves.
func (s *Service) ChunksFor(kind, content string) []string {
	return splitChunks(content, chunkRunes(s.purposeDefault(kind).NumCtx))
}

// GenerateChunked is Generate for content that may not fit one LLM call: the
// content is split to fit the purpose's context window (see chunkRunes) and
// each chunk goes through Generate in turn, then the outputs are joined. A
// title is derived from the first chunk alone. Content that fits in one
// chunk behaves exactly like Generate.
func (s *Service) GenerateChunked(ctx context.Context, kind, sourceLanguage, targetLanguage, contentType, content string) (string, error) {
	pieces := splitChunkPieces(content, chunkRunes(s.purposeDefault(kind).NumCtx))
	chunks := make([]string, len(pieces))
	separators := make([]string, len(pieces))
	for i, p := range pieces {
		chunks[i], separators[i] = p.text, p.sepBefore
	}
	run := checkpoint.Start(ctx, "chunked/"+kind+"/"+sourceLanguage+"/"+targetLanguage+"/"+contentType, chunks)
	return generateChunks(ctx, kind, contentType, chunks, separators, run, func(ctx context.Context, chunk string) (string, error) {
		return s.generateSplitting(ctx, kind, sourceLanguage, targetLanguage, contentType, chunk, false)
	})
}

// GenerateLines is Generate for one chunk of input whose reply is a list, one
// item per line, such as the generate_vocabulary and generate_models
// kinds: if the call overflows the context window the chunk is halved and the
// halves' replies are joined line by line. See generateWithSplit.
func (s *Service) GenerateLines(ctx context.Context, kind, sourceLanguage, targetLanguage, contentType, chunk string) (string, error) {
	return s.generateSplitting(ctx, kind, sourceLanguage, targetLanguage, contentType, chunk, true)
}

// generateSplitting is Generate that survives a chunk overflowing the context
// window by splitting it, see generateWithSplit.
func (s *Service) generateSplitting(ctx context.Context, kind, sourceLanguage, targetLanguage, contentType, chunk string, joinLines bool) (string, error) {
	return generateWithSplit(ctx, chunk, joinLines, func(ctx context.Context, piece string) (string, error) {
		return s.Generate(ctx, kind, sourceLanguage, targetLanguage, contentType, piece)
	})
}

// generateChunks runs generate over chunks one after another (the job queue
// has a single worker, and each call is already long on a CPU-only node) and
// joins the outputs with the separator each chunk was cut at, so paragraphs,
// lines and sentences land where they were. A failing chunk fails the
// whole call, naming which chunk it was.
func generateChunks(ctx context.Context, kind, contentType string, chunks, separators []string, run *checkpoint.Run, generate func(context.Context, string) (string, error)) (string, error) {
	switch {
	case len(chunks) == 0:
		return "", fmt.Errorf("content is required")
	case len(chunks) == 1:
		return chunkOutput(ctx, run, 0, chunks[0], generate)
	case kind == kindTitle:
		return chunkOutput(ctx, run, 0, chunks[0], generate)
	}
	// separators[i] is what stood before chunk i in the text; without them
	// (nil) every chunk is joined the way a text or a dialog is usually laid
	// out: a blank line, or a line break between turns.
	separator := "\n\n"
	if contentType == contentTypeDialog {
		separator = "\n"
	}
	var joined strings.Builder
	for i, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		out, err := chunkOutput(ctx, run, i, chunk, generate)
		if err != nil {
			return "", fmt.Errorf("chunk %d of %d: %w", i+1, len(chunks), err)
		}
		if i > 0 {
			if i < len(separators) {
				joined.WriteString(separators[i])
			} else {
				joined.WriteString(separator)
			}
		}
		joined.WriteString(strings.TrimSpace(out))
	}
	return joined.String(), nil
}

// chunkOutput is chunk i's reply: the one an earlier run of this job saved
// (see the checkpoint package) if there is one, else a fresh call whose
// reply is saved for a Retry. A saved reply that cannot be read is
// regenerated.
func chunkOutput(ctx context.Context, run *checkpoint.Run, i int, chunk string, generate func(context.Context, string) (string, error)) (string, error) {
	if saved, ok := run.Done(i); ok {
		var out string
		if json.Unmarshal(saved, &out) == nil {
			return out, nil
		}
	}
	out, err := generate(ctx, chunk)
	if err != nil {
		return "", err
	}
	if raw, err := json.Marshal(out); err == nil {
		run.Record(ctx, i, raw)
	}
	return out, nil
}
