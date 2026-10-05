// Package checkpoint lets a long job keep the results of the chunks it has
// finished, so a Retry of a failed or cancelled job resumes at the first
// chunk that is not done instead of paying for every chunk again — on the
// CPU-only prod node one chunk is minutes of LLM time.
//
// The job runner puts a Store for the running job into the handler's context
// (With); code that works through chunks asks for a Run (Start) and reports
// each finished chunk (Record). Without a Store in the context, a Run does
// nothing, so callers need no special case outside a job.
package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
)

// Store keeps a job's checkpoints, one list of chunk results per key.
type Store interface {
	// Load returns the results saved under key, or none if there are none.
	Load(ctx context.Context, key string) ([]json.RawMessage, error)
	// Save replaces the results saved under key.
	Save(ctx context.Context, key string, results []json.RawMessage) error
}

// ProgressReporter is implemented by a Store that can show how far a chunked
// call has got. It is optional: a Store without it just keeps results.
type ProgressReporter interface {
	// SetProgress reports done of total chunks finished.
	SetProgress(ctx context.Context, done, total int) error
}

type storeKey struct{}

// With returns ctx carrying s as the running job's checkpoint store.
func With(ctx context.Context, s Store) context.Context {
	return context.WithValue(ctx, storeKey{}, s)
}

// saveTimeout bounds one Save, which runs even after the job was cancelled.
const saveTimeout = 10 * time.Second

// Run is the checkpoint of one chunked call. A nil *Run is valid and does
// nothing.
type Run struct {
	store Store
	key   string
	total int
	done  []json.RawMessage
}

// Start opens the checkpoint for the call called name over chunks, loading
// what an earlier run of the same job saved. The key covers the name and the
// text of every chunk, so a changed text, chunk size or purpose never reuses
// a result that belongs to something else. A failure to load is logged and
// means starting over, never a failed job.
func Start(ctx context.Context, name string, chunks []string) *Run {
	store, ok := ctx.Value(storeKey{}).(Store)
	if !ok || store == nil {
		return nil
	}
	sum := sha256.Sum256([]byte(name + "\x00" + strings.Join(chunks, "\x00")))
	r := &Run{store: store, key: name + ":" + hex.EncodeToString(sum[:8]), total: len(chunks)}
	done, err := store.Load(ctx, r.key)
	if err != nil {
		slog.Warn("checkpoint: load failed, starting over", "key", r.key, "error", err)
		return r
	}
	r.done = done
	r.reportProgress(ctx)
	return r
}

// reportProgress shows done/total through the Store, when it can and when
// there is more than one chunk (one chunk has no progress to show). Failing
// to is logged: progress is information, never a reason to fail the job.
func (r *Run) reportProgress(ctx context.Context) {
	reporter, ok := r.store.(ProgressReporter)
	if !ok || r.total < 2 {
		return
	}
	if err := reporter.SetProgress(ctx, len(r.done), r.total); err != nil {
		slog.Warn("checkpoint: progress not recorded", "key", r.key, "error", err)
	}
}

// Done returns the saved result of chunk i, if chunk i was finished by an
// earlier run.
func (r *Run) Done(i int) (json.RawMessage, bool) {
	if r == nil || i < 0 || i >= len(r.done) {
		return nil, false
	}
	return r.done[i], true
}

// Record saves result as chunk i's. Chunks are finished in order, so a result
// is kept only as the next one after those already saved; the rest are
// ignored. Saving is best effort: it is logged when it fails, because losing
// a checkpoint costs a repeated chunk, not the job.
func (r *Run) Record(ctx context.Context, i int, result json.RawMessage) {
	if r == nil || i != len(r.done) {
		return
	}
	next := append(append([]json.RawMessage(nil), r.done...), result)
	// WithoutCancel: a chunk finished just as the job was cancelled should
	// still be kept for the Retry.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
	defer cancel()
	if err := r.store.Save(saveCtx, r.key, next); err != nil {
		slog.Warn("checkpoint: save failed", "key", r.key, "chunk", i, "error", err)
		return
	}
	r.done = next
	r.reportProgress(saveCtx)
}
