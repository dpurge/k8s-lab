package jobs

import "context"

type jobIDKey struct{}

// WithJobID returns ctx carrying the id of the job being run. runOne sets it
// before calling a handler, so what a handler does in turn (such as a model
// call, which llmcalls records against the job) can tell which job it is for
// without any handler's signature carrying it.
func WithJobID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, jobIDKey{}, id)
}

// JobID is the id of the job ctx belongs to; ok is false outside a job, as for
// an interactive call.
func JobID(ctx context.Context) (id string, ok bool) {
	id, ok = ctx.Value(jobIDKey{}).(string)
	return id, ok && id != ""
}
