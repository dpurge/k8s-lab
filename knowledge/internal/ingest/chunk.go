// Package ingest implements the ingest pipeline that turns a fetched URL or
// uploaded text file into draft knowledge items pending human review.
package ingest

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// targetChunkRunes is the size a chunk should grow to before starting a
	// new one, sized for Ollama's 4096-token default context window.
	targetChunkRunes = 4000
	// maxChunkRunes is the hard cap a single chunk may never exceed.
	maxChunkRunes = 5000
	// minTailRunes is the size below which a final chunk is merged into the
	// previous one rather than left as an undersized chunk on its own.
	minTailRunes = 500
	// maxChunks bounds a single ingest run at roughly this many LLM calls.
	maxChunks = 50
)

// ErrNoContent is returned when chunking finds no non-empty paragraphs to
// work with, after splitParagraphs has run — e.g. the acquired text was
// entirely whitespace. Distinct from ErrEmptyContent (source.go), which
// fires earlier and only for a "text" source: validateText rejects an
// empty/whitespace-only raw content string before it ever reaches
// chunking. ErrNoContent is the only one of the two reachable for a "url"
// source, since a fetched body never passes through validateText.
var ErrNoContent = errors.New("ingest: no content to chunk")

// chunk splits text into paragraph-based chunks sized for LLM context
// windows. It normalizes line endings, drops blank paragraphs, pre-splits
// any paragraph larger than maxChunkRunes, greedily packs paragraphs into
// chunks up to targetChunkRunes, and merges an undersized final chunk into
// the previous one when that keeps it within maxChunkRunes.
func chunk(text string) ([]string, error) {
	paragraphs := splitParagraphs(text)
	if len(paragraphs) == 0 {
		return nil, ErrNoContent
	}

	var pieces []string
	for _, paragraph := range paragraphs {
		pieces = append(pieces, splitOversized(paragraph)...)
	}

	chunks := mergeShortTail(buildChunks(pieces))

	if len(chunks) > maxChunks {
		return nil, fmt.Errorf("ingest: input produced %d chunks, exceeds cap of %d", len(chunks), maxChunks)
	}

	return chunks, nil
}

// splitParagraphs normalizes line endings and splits text into trimmed,
// non-empty paragraphs separated by one or more blank lines.
func splitParagraphs(text string) []string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	var paragraphs []string
	var current []string
	flush := func() {
		if len(current) > 0 {
			paragraphs = append(paragraphs, strings.Join(current, "\n"))
			current = nil
		}
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		current = append(current, line)
	}
	flush()

	trimmed := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		if p := strings.TrimSpace(paragraph); p != "" {
			trimmed = append(trimmed, p)
		}
	}
	return trimmed
}

// splitOversized returns paragraph unchanged if it fits within
// maxChunkRunes, otherwise repeatedly pre-splits it at the last space or
// newline before the limit, falling back to a hard cut on a rune boundary
// when no such break exists.
func splitOversized(paragraph string) []string {
	runes := []rune(paragraph)
	if len(runes) <= maxChunkRunes {
		return []string{paragraph}
	}

	var pieces []string
	for len(runes) > maxChunkRunes {
		breakAt := lastBreakBefore(runes, maxChunkRunes)

		var head, rest []rune
		if breakAt == -1 {
			head, rest = runes[:maxChunkRunes], runes[maxChunkRunes:]
		} else {
			head, rest = runes[:breakAt], runes[breakAt+1:]
		}

		if piece := strings.TrimSpace(string(head)); piece != "" {
			pieces = append(pieces, piece)
		}
		runes = rest
	}
	if piece := strings.TrimSpace(string(runes)); piece != "" {
		pieces = append(pieces, piece)
	}
	return pieces
}

// lastBreakBefore returns the rune index of the last space or newline in
// runes[:limit], or -1 if none exists.
func lastBreakBefore(runes []rune, limit int) int {
	for i := limit - 1; i >= 0; i-- {
		if runes[i] == ' ' || runes[i] == '\n' {
			return i
		}
	}
	return -1
}

// buildChunks greedily packs pieces into chunks, starting a new chunk
// whenever appending the next piece (joined by "\n\n") would exceed
// targetChunkRunes.
func buildChunks(pieces []string) []string {
	var chunks []string
	var current string
	for _, piece := range pieces {
		if current == "" {
			current = piece
			continue
		}
		if utf8.RuneCountInString(current)+2+utf8.RuneCountInString(piece) <= targetChunkRunes {
			current = current + "\n\n" + piece
		} else {
			chunks = append(chunks, current)
			current = piece
		}
	}
	if current != "" {
		chunks = append(chunks, current)
	}
	return chunks
}

// mergeShortTail merges the final chunk into the previous one when the
// final chunk is shorter than minTailRunes and the merged result would
// still fit within maxChunkRunes.
func mergeShortTail(chunks []string) []string {
	if len(chunks) < 2 {
		return chunks
	}

	last := chunks[len(chunks)-1]
	if utf8.RuneCountInString(last) >= minTailRunes {
		return chunks
	}

	prev := chunks[len(chunks)-2]
	merged := prev + "\n\n" + last
	if utf8.RuneCountInString(merged) > maxChunkRunes {
		return chunks
	}

	chunks = chunks[:len(chunks)-2]
	return append(chunks, merged)
}
