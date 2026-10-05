package generate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"golang.org/x/text/unicode/norm"

	"phraseforge/internal/ai"
	"phraseforge/internal/checkpoint"
)

// RejectedLine is a line of LLM output that did not match the required
// format, with a reason the model can act on.
type RejectedLine = ai.RejectedLine

const (
	// maxSampleLines and maxSampleRunes bound how much of the model's output
	// an error message quotes.
	maxSampleLines = 3
	maxSampleRunes = 120
)

// parseWithCorrection parses out with parse, then sends only the rejected
// lines to fix — at most rounds times, each round re-parsing the reply and
// carrying forward whatever is still rejected. Lines that already parsed are
// never resent. Corrected items follow the originally valid ones.
//
// Lines still rejected afterwards are dropped: skipped counts how many
// originally rejected lines were not recovered, and unfixed are the lines
// still rejected after the last round. A failed correction call (other than
// the caller's own cancellation) ends the rounds the same way, so the valid
// items are not lost to it.
func parseWithCorrection[T any](
	ctx context.Context,
	out string,
	rounds int,
	parse func(string) ([]T, []RejectedLine),
	fix func(context.Context, []RejectedLine) (string, error),
) (items []T, skipped int, unfixed []RejectedLine, err error) {
	items, rejected := parse(out)
	initiallyRejected, recovered := len(rejected), 0
	for round := 1; round <= rounds && len(rejected) > 0; round++ {
		sent := len(rejected)
		reply, fixErr := fix(ctx, rejected)
		if fixErr != nil {
			if ctx.Err() != nil {
				return nil, 0, nil, ctx.Err()
			}
			slog.Warn("correcting rejected lines failed", "round", round, "rejected", len(rejected), "error", fixErr)
			break
		}
		var fixed []T
		fixed, rejected = parse(reply)
		items = append(items, fixed...)
		recovered += len(fixed)
		// Counts only — the lines themselves are model output.
		slog.Info("corrected rejected lines", "round", round, "sent", sent, "recovered", len(fixed), "still_rejected", len(rejected))
	}
	return items, max(initiallyRejected-recovered, 0), rejected, nil
}

// chunkOutcome is what one chunk of item generation came to, in the form a
// checkpoint keeps (see the checkpoint package) so a Retry does not repeat the
// chunk's generation or its correction rounds.
type chunkOutcome[T any] struct {
	Items    []T            `json:"items"`
	Skipped  int            `json:"skipped"`
	Unfixed  []RejectedLine `json:"unfixed"`
	Answered bool           `json:"answered"`
}

// generateItems generates and parses item lines chunk by chunk: each chunk's
// reply goes through parseWithCorrection, and an item whose phrase an earlier
// chunk already produced is dropped (an item repeated within one chunk is
// kept, as for a single call). A chunk whose reply has nothing valid is
// skipped, not fatal; the call fails only if no chunk produced a valid item
// although the model answered, with an error quoting the first rejected lines
// — an empty result must not look like success. run (nil is fine) saves each
// finished chunk and supplies the ones an earlier run of the job finished.
func generateItems[T any](
	ctx context.Context,
	chunks []string,
	rounds int,
	generate func(context.Context, string) (string, error),
	parse func(string) ([]T, []RejectedLine),
	fix func(context.Context, []RejectedLine) (string, error),
	phrase func(T) string,
	run *checkpoint.Run,
) (items []T, skipped int, err error) {
	if len(chunks) == 0 {
		return nil, 0, fmt.Errorf("content is required")
	}
	seen := map[string]bool{}
	var firstUnfixed []RejectedLine
	answered := false
	for i, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		outcome, err := chunkItems(ctx, run, i, chunk, rounds, generate, parse, fix)
		if err != nil {
			if len(chunks) == 1 {
				return nil, 0, err
			}
			return nil, 0, fmt.Errorf("chunk %d of %d: %w", i+1, len(chunks), err)
		}
		skipped += outcome.Skipped
		answered = answered || outcome.Answered
		if len(firstUnfixed) == 0 {
			firstUnfixed = outcome.Unfixed
		}
		var added []string
		for _, it := range outcome.Items {
			key := norm.NFC.String(strings.TrimSpace(phrase(it)))
			if seen[key] {
				continue
			}
			added = append(added, key)
			items = append(items, it)
		}
		for _, key := range added {
			seen[key] = true
		}
	}
	if len(items) == 0 && answered {
		return nil, 0, noValidLinesError(firstUnfixed)
	}
	return items, skipped, nil
}

// chunkItems is chunk i's outcome: the one an earlier run saved if it can be
// read, else a fresh generation, parse and correction, which is then saved.
func chunkItems[T any](
	ctx context.Context,
	run *checkpoint.Run,
	i int,
	chunk string,
	rounds int,
	generate func(context.Context, string) (string, error),
	parse func(string) ([]T, []RejectedLine),
	fix func(context.Context, []RejectedLine) (string, error),
) (chunkOutcome[T], error) {
	if saved, ok := run.Done(i); ok {
		var outcome chunkOutcome[T]
		if json.Unmarshal(saved, &outcome) == nil {
			return outcome, nil
		}
	}
	out, err := generate(ctx, chunk)
	if err != nil {
		return chunkOutcome[T]{}, err
	}
	items, skipped, unfixed, err := parseWithCorrection(ctx, out, rounds, parse, fix)
	if err != nil {
		return chunkOutcome[T]{}, err
	}
	outcome := chunkOutcome[T]{Items: items, Skipped: skipped, Unfixed: unfixed, Answered: strings.TrimSpace(out) != ""}
	if raw, err := json.Marshal(outcome); err == nil {
		run.Record(ctx, i, raw)
	}
	return outcome, nil
}

func noValidLinesError(rejected []RejectedLine) error {
	if len(rejected) == 0 {
		return fmt.Errorf("model output had no valid lines")
	}
	var sample []string
	for _, r := range rejected[:min(len(rejected), maxSampleLines)] {
		line := []rune(r.Line)
		if len(line) > maxSampleRunes {
			line = append(line[:maxSampleRunes], '…')
		}
		sample = append(sample, fmt.Sprintf("%q (%s)", string(line), r.Reason))
	}
	return fmt.Errorf("model output had no valid lines; %d rejected, first: %s", len(rejected), strings.Join(sample, "; "))
}
