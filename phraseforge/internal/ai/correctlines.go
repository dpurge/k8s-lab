package ai

import (
	"context"
	"strings"

	"k8s-lab/shared/llm"
	"phraseforge/internal/config"
)

// RejectedLine is a line of LLM output that failed to parse, with the
// reason it did.
type RejectedLine struct {
	Line   string
	Reason string
}

// correctionSystemTemplate is the system turn of CorrectLines. Named
// placeholder ({{format}}, not %s) — this is prompt text, see
// userMessageTemplate in ai.go.
const correctionSystemTemplate = "You fix formatting errors in lines of language-learning items. The required line format is: {{format}}\nRespond ONLY with the corrected lines, one per line, in the same order. Fix only the format and keep each line's content. Do not add commentary, numbering, or code fences. Leave out a line you cannot fix."

// correctionMessages builds the two chat turns that send rejected lines back
// for correction: only those lines and their errors, never the lines that
// already parsed nor the source text, so the call stays small.
func correctionMessages(format string, rejected []RejectedLine) []llm.Message {
	var user strings.Builder
	user.WriteString("These lines were rejected:\n")
	for _, r := range rejected {
		user.WriteString("\nLine: " + r.Line + "\nError: " + r.Reason + "\n")
	}
	user.WriteString("\nReturn the corrected lines.")
	return []llm.Message{
		{Role: "system", Content: strings.NewReplacer("{{format}}", format).Replace(correctionSystemTemplate)},
		{Role: "user", Content: user.String()},
	}
}

// CorrectionRounds is how many correction rounds kind's purpose allows: its
// MaxAttempts budget.
func (s *Service) CorrectionRounds(kind string) int {
	return effectiveMaxAttempts(s.purposeDefault(kind).MaxAttempts)
}

// CorrectLines asks the model behind kind (provider, model and timeouts as
// for Generate, so the loaded model and its context window are reused) to fix
// rejected lines against format, returning its reply: the corrected lines,
// one per line. format is the line format description shown to the model.
func (s *Service) CorrectLines(ctx context.Context, kind, sourceLanguage, format string, rejected []RejectedLine) (string, error) {
	prompt := s.prompt(ctx, kind, sourceLanguage, kind)
	return s.correctLines(ctx, kind, prompt, s.purposeDefault(kind), format, rejected)
}

func (s *Service) correctLines(ctx context.Context, kind string, prompt Prompt, def config.PurposeConfig, format string, rejected []RejectedLine) (string, error) {
	return s.callWithRetry(ctx, kind, prompt, def, correctionMessages(format, rejected))
}
