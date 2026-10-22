package modelrouter

import (
	"context"
	"log/slog"

	"knowledge/internal/queue"
)

// The kinds of model call, as recorded and logged.
const (
	KindChat            = "chat"
	KindGenerateTitle   = "generate_title"
	KindGenerateSummary = "generate_summary"
	KindTranslate       = "translate"
	KindEmbed           = "embed"
)

type kindKey struct{}

// WithKind returns ctx saying which kind of call is being made.
func WithKind(ctx context.Context, kind string) context.Context {
	return context.WithValue(ctx, kindKey{}, kind)
}

// Kind is the kind of call ctx was made for; empty when none was said.
func Kind(ctx context.Context) string {
	kind, _ := ctx.Value(kindKey{}).(string)
	return kind
}

// LogAttrs are the attributes every router log line of a call carries: its kind
// and, when it ran in a queue operation, that operation's id. Never content.
func LogAttrs(ctx context.Context) []slog.Attr {
	var attrs []slog.Attr
	if kind := Kind(ctx); kind != "" {
		attrs = append(attrs, slog.String("kind", kind))
	}
	if id, ok := queue.OperationID(ctx); ok {
		attrs = append(attrs, slog.String("operation_id", id))
	}
	return attrs
}
