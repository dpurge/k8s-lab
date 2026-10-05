package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"k8s-lab/shared/llm"
)

// ContextOverflowError reports a call whose prompt plus reply used the whole
// context window. Ollama raises no error then: it cuts the prompt, or shifts
// the context during the reply and drops the oldest tokens, including the
// system prompt, and still ends with done_reason "stop". The reply is
// therefore not to be trusted, and the input is to be sent in smaller pieces.
type ContextOverflowError struct {
	PromptTokens, ReplyTokens, NumCtx int
}

func (e *ContextOverflowError) Error() string {
	return fmt.Sprintf("context window exceeded: %d prompt + %d reply tokens against num_ctx %d", e.PromptTokens, e.ReplyTokens, e.NumCtx)
}

// contextOverflow returns a *ContextOverflowError when r's reported token
// counts reach numCtx. It reports nothing when the window is unknown (numCtx
// <= 0) or the provider reported no counts (an OpenAI-compatible provider),
// since neither can be judged.
func contextOverflow(r llm.Response, numCtx int) error {
	if numCtx <= 0 || r.PromptTokens+r.ReplyTokens == 0 {
		return nil
	}
	if r.PromptTokens+r.ReplyTokens < numCtx {
		return nil
	}
	return &ContextOverflowError{PromptTokens: r.PromptTokens, ReplyTokens: r.ReplyTokens, NumCtx: numCtx}
}

// maxSplitDepth bounds how often one chunk may be halved after overflowing:
// 4 levels is down to a sixteenth of the chunk, far below any useful size.
const maxSplitDepth = 4

// generateWithSplit calls call for chunk and, if the call overflowed the
// context window, halves the chunk (see splitHalf) and generates each half,
// recursively up to maxSplitDepth, then reassembles the replies with the
// separator the chunk was split on — a blank line for a paragraph break, so
// the text keeps its structure. joinLines forces a newline instead, for
// replies that are lists with one item per line. Any other error is returned
// as it is. A chunk that fits makes exactly one call and its reply is
// returned untouched.
func generateWithSplit(ctx context.Context, chunk string, joinLines bool, call func(context.Context, string) (string, error)) (string, error) {
	return generateWithSplitDepth(ctx, chunk, joinLines, 0, call)
}

func generateWithSplitDepth(ctx context.Context, chunk string, joinLines bool, depth int, call func(context.Context, string) (string, error)) (string, error) {
	out, err := call(ctx, chunk)
	var overflow *ContextOverflowError
	if !errors.As(err, &overflow) {
		return out, err
	}
	if depth >= maxSplitDepth {
		return "", fmt.Errorf("still over the context window after splitting the chunk %d times: %w", depth, err)
	}
	first, second, separator, ok := splitHalf(chunk)
	if !ok {
		return "", fmt.Errorf("chunk too short to split further: %w", err)
	}
	if joinLines {
		separator = "\n"
	}
	firstOut, err := generateWithSplitDepth(ctx, first, joinLines, depth+1, call)
	if err != nil {
		return "", err
	}
	secondOut, err := generateWithSplitDepth(ctx, second, joinLines, depth+1, call)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(firstOut) + separator + strings.TrimSpace(secondOut), nil
}
