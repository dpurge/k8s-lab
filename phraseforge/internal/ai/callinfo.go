package ai

import (
	"context"
	"log/slog"
)

// callInfo is what callLLM tells the router's log about a call, through the
// context: the router writes its own lines (see shared/router) and LogAttrs adds
// these to each. Never content.
type callInfo struct {
	kind, source, target string
}

type callInfoKey struct{}

func withCallInfo(ctx context.Context, info callInfo) context.Context {
	return context.WithValue(ctx, callInfoKey{}, info)
}

// WithCallDetails returns ctx carrying a call's kind and languages as callLLM
// sets them; for tests of what reads them back (CallDetails, LogAttrs).
func WithCallDetails(ctx context.Context, kind, source, target string) context.Context {
	return withCallInfo(ctx, callInfo{kind: kind, source: source, target: target})
}

// LogAttrs supplies the attributes of a router log line from the context
// callLLM sets: kind, source_language and target_language, each only when
// known. Pass it to router.WithLogAttrs.
func LogAttrs(ctx context.Context) []slog.Attr {
	info, ok := ctx.Value(callInfoKey{}).(callInfo)
	if !ok {
		return nil
	}
	var attrs []slog.Attr
	for _, a := range []slog.Attr{slog.String("kind", info.kind), slog.String("source_language", info.source), slog.String("target_language", info.target)} {
		if a.Value.String() != "" {
			attrs = append(attrs, a)
		}
	}
	return attrs
}

// CallDetails returns the kind and the source and target languages callLLM put
// in ctx, each empty when unknown. It is what the call recorder stores with an
// attempt; never content.
func CallDetails(ctx context.Context) (kind, source, target string) {
	info, _ := ctx.Value(callInfoKey{}).(callInfo)
	return info.kind, info.source, info.target
}
