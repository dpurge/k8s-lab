// Package callctx carries who a piece of work is for through a context, so that
// a model call deep inside a workflow can be recorded against the task and the
// user without every function in between passing them along.
package callctx

import "context"

// Info says which task a call belongs to and which user pays for it.
type Info struct {
	// TaskID is empty for a call made outside a task.
	TaskID string
	// UserID is required: every call is attributed to someone.
	UserID string
}

type key struct{}

// With returns a context that carries info.
func With(ctx context.Context, info Info) context.Context {
	return context.WithValue(ctx, key{}, info)
}

// From returns the info carried by ctx. It reports false when there is none or
// when it names no user.
func From(ctx context.Context) (Info, bool) {
	info, ok := ctx.Value(key{}).(Info)
	if !ok || info.UserID == "" {
		return Info{}, false
	}
	return info, true
}
