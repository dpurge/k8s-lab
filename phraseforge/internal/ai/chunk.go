package ai

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	kindTitle         = "title"
	contentTypeDialog = "dialog"
)

const (
	// defaultNumCtx stands in for a purpose whose NumCtx is 0 (Ollama's own
	// default window). Assumed, not known: the real default depends on the
	// Ollama version, and 4096 is the conservative end.
	defaultNumCtx = 4096

	// minChunkRunes keeps a tiny configured NumCtx from producing chunks too
	// small to be useful.
	minChunkRunes = 512
)

// chunkRunes is how many runes of input one LLM call should carry for a
// context window of numCtx tokens. Half the window goes to the input (the
// reply is about as long and shares the window), minus 512 tokens for the
// system prompt and the call's preamble, at 3 runes per token — a hedge
// below the ~4 chars/token usually assumed for prose, for non-Latin scripts.
func chunkRunes(numCtx int) int {
	if numCtx <= 0 {
		numCtx = defaultNumCtx
	}
	return max((numCtx/2-512)*3, minChunkRunes)
}

// splitChunks cuts text into pieces of at most maxRunes runes, at the
// coarsest boundary that fits: whole paragraphs (blank-line separated) are
// packed together; a paragraph that is itself too long is cut at line
// breaks, which for a dialog are the turn boundaries; a line that is still
// too long is cut by runes as a last resort. Text that already fits is
// returned as a single chunk. Whitespace-only text yields no chunks.
func splitChunks(text string, maxRunes int) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes {
		return []string{text}
	}
	return splitBy(text, []string{"\n\n", "\n"}, maxRunes)
}

// splitBy packs the parts of text separated by seps[0] greedily into chunks
// of at most maxRunes runes; a part that is too long on its own is split
// further by the next separator and its pieces stand as chunks of their own,
// so a finer cut is never glued to a coarser one. With no separators left it
// cuts by runes.
func splitBy(text string, seps []string, maxRunes int) []string {
	if len(seps) == 0 {
		return cutRunes(text, maxRunes)
	}
	sep := seps[0]
	var chunks []string
	current := ""
	flush := func() {
		if strings.TrimSpace(current) != "" {
			chunks = append(chunks, current)
		}
		current = ""
	}
	for _, part := range strings.Split(text, sep) {
		switch {
		case utf8.RuneCountInString(part) > maxRunes:
			flush()
			chunks = append(chunks, splitBy(part, seps[1:], maxRunes)...)
		case current == "":
			current = part
		case utf8.RuneCountInString(current)+utf8.RuneCountInString(sep)+utf8.RuneCountInString(part) <= maxRunes:
			current += sep + part
		default:
			flush()
			current = part
		}
	}
	flush()
	return chunks
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

// GenerateChunked is Generate for content that may not fit one LLM call: the
// content is split to fit the purpose's context window (see chunkRunes) and
// each chunk goes through Generate in turn, then the outputs are joined. A
// title is derived from the first chunk alone. Content that fits in one
// chunk behaves exactly like Generate.
func (s *Service) GenerateChunked(ctx context.Context, kind, sourceLanguage, targetLanguage, contentType, content string) (string, error) {
	chunks := splitChunks(content, chunkRunes(s.purposeDefault(kind).NumCtx))
	return generateChunks(ctx, kind, contentType, chunks, func(ctx context.Context, chunk string) (string, error) {
		return s.Generate(ctx, kind, sourceLanguage, targetLanguage, contentType, chunk)
	})
}

// generateChunks runs generate over chunks one after another (the job queue
// has a single worker, and each call is already long on a CPU-only node) and
// joins the outputs: with a blank line for text, a single newline for a
// dialog, so a dialog's turns stay one per line. A failing chunk fails the
// whole call, naming which chunk it was.
func generateChunks(ctx context.Context, kind, contentType string, chunks []string, generate func(context.Context, string) (string, error)) (string, error) {
	switch {
	case len(chunks) == 0:
		return "", fmt.Errorf("content is required")
	case len(chunks) == 1:
		return generate(ctx, chunks[0])
	case kind == kindTitle:
		return generate(ctx, chunks[0])
	}
	separator := "\n\n"
	if contentType == contentTypeDialog {
		separator = "\n"
	}
	outputs := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		out, err := generate(ctx, chunk)
		if err != nil {
			return "", fmt.Errorf("chunk %d of %d: %w", i+1, len(chunks), err)
		}
		outputs = append(outputs, strings.TrimSpace(out))
	}
	return strings.Join(outputs, separator), nil
}
