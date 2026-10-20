package router

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"strings"
	"time"
)

// Log messages. They are stable and greppable; see the Troubleshooting section
// of the package documentation for their fields.
const (
	msgCandidates   = "llm candidates"
	msgNoModel      = "llm no model"
	msgAttempt      = "llm attempt"
	msgBreakerOpen  = "llm breaker open"
	msgProviderDown = "llm provider down"
	msgBreakerShut  = "llm breaker closed"
	msgCallDone     = "llm call done"

	msgProviderUnavailable = "llm provider unavailable"
	msgProviderAvailable   = "llm provider available"
)

// maxLoggedErrorChars bounds the provider error text in a log line.
const maxLoggedErrorChars = 500

// WithLogger sets the logger for the router's log lines; the default is
// slog.Default(), looked up at each call.
func WithLogger(l *slog.Logger) Option { return func(r *Router) { r.logger = l } }

// WithLogAttrs sets a function that supplies extra attributes for every log
// line of a call, such as the task and user the call belongs to. It is called
// with the call's context, once per line.
func WithLogAttrs(f func(ctx context.Context) []slog.Attr) Option {
	return func(r *Router) { r.logAttrs = f }
}

// WithCallIDs replaces how a call's id is made; tests use it to be
// deterministic. The default is 8 random hex characters.
func WithCallIDs(next func() string) Option { return func(r *Router) { r.newCallID = next } }

func randomCallID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; an id that merely
		// repeats is better than failing a model call over a log field.
		return "00000000"
	}
	return hex.EncodeToString(b[:])
}

// callLog writes the log lines of one Complete().
type callLog struct {
	r      *Router
	callID string
	tags   []string
}

func (r *Router) newCallLog(callID string, tags []string) *callLog {
	if tags == nil {
		tags = []string{}
	}
	return &callLog{r: r, callID: callID, tags: tags}
}

func (l *callLog) logger() *slog.Logger {
	if l.r.logger != nil {
		return l.r.logger
	}
	return slog.Default()
}

// emit writes one line with the call id, the tags, the caller's attributes and
// then attrs.
func (l *callLog) emit(ctx context.Context, level slog.Level, msg string, attrs ...slog.Attr) {
	all := make([]slog.Attr, 0, len(attrs)+4)
	all = append(all, slog.String("call_id", l.callID), slog.Any("tags", l.tags))
	if l.r.logAttrs != nil {
		all = append(all, l.r.logAttrs(ctx)...)
	}
	all = append(all, attrs...)
	l.logger().LogAttrs(ctx, level, msg, all...)
}

// skippedModel is a candidate left out because the breaker holds it.
type skippedModel struct {
	ModelID        string `json:"model_id"`
	Reason         string `json:"reason"`
	AvailableAgain string `json:"available_again,omitempty"`
}

const (
	skipModelUnavailable    = "model_unavailable"
	skipProviderUnavailable = "provider_unavailable"
)

func (l *callLog) candidates(ctx context.Context, ids []string, skipped []skippedModel) {
	if skipped == nil {
		skipped = []skippedModel{}
	}
	l.emit(ctx, slog.LevelDebug, msgCandidates, slog.Any("candidates", ids), slog.Any("skipped", skipped))
}

func (l *callLog) noModel(ctx context.Context, modelsChecked []string) {
	l.emit(ctx, slog.LevelWarn, msgNoModel, slog.Any("models_checked", modelsChecked))
}

func (l *callLog) attempt(ctx context.Context, n, of int, providerType string, a Attempt, f *Failure) {
	level := slog.LevelInfo
	if a.Outcome != OutcomeOK && a.Outcome != OutcomeCanceled {
		level = slog.LevelWarn
	}
	attrs := []slog.Attr{
		slog.Int("attempt", n), slog.Int("of", of),
		slog.String("model_id", a.ModelID), slog.String("provider_id", a.ProviderID),
		slog.String("provider_type", providerType), slog.String("model_name", a.ModelName),
		slog.String("outcome", a.Outcome),
	}
	if f != nil {
		attrs = append(attrs, slog.String("failure_kind", string(f.Kind)))
	}
	attrs = append(attrs,
		slog.Int64("duration_ms", a.Duration.Milliseconds()),
		slog.Int("input_tokens", a.InputTokens), slog.Int("output_tokens", a.OutputTokens),
	)
	if a.HTTPStatus != 0 {
		attrs = append(attrs, slog.Int("http_status", a.HTTPStatus))
	}
	if a.RequestID != "" {
		attrs = append(attrs, slog.String("request_id", a.RequestID))
	}
	if f != nil {
		attrs = append(attrs, slog.String("error", boundedText(a.Error)))
	}
	l.emit(ctx, level, msgAttempt, attrs...)
}

func (l *callLog) breakerOpen(ctx context.Context, modelID, providerID string, kind FailureKind, t transition, cooldown time.Duration) {
	l.emit(ctx, slog.LevelWarn, msgBreakerOpen,
		slog.String("model_id", modelID), slog.String("provider_id", providerID),
		slog.String("reason", string(kind)), slog.Int("consecutive_failures", t.failures),
		slog.Int("cooldown_seconds", int(cooldown.Seconds())),
		slog.String("available_again", t.until.UTC().Format(time.RFC3339)))
}

func (l *callLog) providerDown(ctx context.Context, providerID string, modelIDs []string, t transition, cooldown time.Duration) {
	l.emit(ctx, slog.LevelWarn, msgProviderDown,
		slog.String("provider_id", providerID), slog.Any("models", modelIDs),
		slog.Int("cooldown_seconds", int(cooldown.Seconds())),
		slog.String("available_again", t.until.UTC().Format(time.RFC3339)))
}

func (l *callLog) breakerClosed(ctx context.Context, modelID string, previousFailures int) {
	l.emit(ctx, slog.LevelInfo, msgBreakerShut,
		slog.String("model_id", modelID), slog.Int("previous_consecutive_failures", previousFailures))
}

// callSummary is the content of the "llm call done" line.
type callSummary struct {
	outcome  string // "ok", "failed" or "canceled"
	attempts int
	winner   string
	total    time.Duration
	fellBack bool
	chain    []string // "model_id:failure_kind", set when the call failed
}

func (l *callLog) done(ctx context.Context, s callSummary) {
	level := slog.LevelInfo
	if s.outcome == callFailed {
		level = slog.LevelWarn
	}
	attrs := []slog.Attr{slog.String("outcome", s.outcome), slog.Int("attempts", s.attempts)}
	if s.winner != "" {
		attrs = append(attrs, slog.String("model_id", s.winner))
	}
	attrs = append(attrs, slog.Int64("total_duration_ms", s.total.Milliseconds()), slog.Bool("fell_back", s.fellBack))
	if s.outcome == callFailed {
		attrs = append(attrs, slog.Any("chain", s.chain))
	}
	l.emit(ctx, level, msgCallDone, attrs...)
}

// Outcomes of a whole call, as logged by "llm call done".
const (
	callOK       = "ok"
	callFailed   = "failed"
	callCanceled = "canceled"
)

// boundedText cuts s to at most maxLoggedErrorChars bytes, on a character
// boundary, marking the cut. Provider error text is already free of keys and
// prompts; the bound only keeps a log line a sane size.
func boundedText(s string) string {
	if len(s) <= maxLoggedErrorChars {
		return s
	}
	const mark = "…"
	cut := strings.ToValidUTF8(s[:maxLoggedErrorChars-len(mark)], "")
	return cut + mark
}
