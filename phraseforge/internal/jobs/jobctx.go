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

type userIDKey struct{}

// WithUserID returns ctx carrying the id of the user a request, or a job, is
// for. The server sets it for every signed-in request and runOne for the job it
// runs, so Enqueue can record who asked for a job and the model-call recorder
// can record who a call was made for, without any signature carrying it.
func WithUserID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, userIDKey{}, id)
}

// UserID is the id of the user ctx belongs to; ok is false when there is none
// (the system's own work).
func UserID(ctx context.Context) (id int64, ok bool) {
	id, ok = ctx.Value(userIDKey{}).(int64)
	return id, ok
}
