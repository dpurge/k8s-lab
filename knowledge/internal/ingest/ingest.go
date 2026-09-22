package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"knowledge/internal/generate"
	"knowledge/internal/jobs"
	"knowledge/internal/queue"
	"knowledge/internal/translate"
)

// KindAcquire and KindChunk are the queue.Service operation kinds
// registered for ingest's two stages. Both are background priority: an
// interactive chat reply or manual generate always jumps ahead of them.
// Exported so main.go can call queue.Register(ingest.KindAcquire, ...).
const (
	KindAcquire = "ingest_acquire"
	KindChunk   = "ingest_chunk"
)

// acquirePayload is the queue.Operation payload for KindAcquire.
type acquirePayload struct {
	JobID string `json:"job_id"`
}

// chunkPayload is the queue.Operation payload for KindChunk. It carries
// only the job id and this chunk's index — never chunk text itself. The
// handler re-derives the actual chunk list by re-chunking job.SourceText
// (a pure, deterministic function of that already-durably-stored text),
// so nothing about the source's content needs to be duplicated across
// every chunk's operation row.
type chunkPayload struct {
	JobID string `json:"job_id"`
	Index int    `json:"index"`
}

// ErrValidation is returned when the caller's input is malformed (empty
// URL, unsupported text extension, empty/invalid content, etc.), following
// the same errors.Is-checkable sentinel convention as auth.ErrValidation
// and qdrant.ErrValidation.
var ErrValidation = errors.New("ingest: validation failed")

// ErrJobRunning is returned when another ingest job is already in
// progress. jobs.Current is single-job by design, and stacking a second
// concurrent, LLM-heavy run against one Ollama instance has no benefit.
var ErrJobRunning = errors.New("ingest: a job is already running")

// ErrNotRetryable is returned when a job can't be retried: it already
// succeeded (retrying would silently duplicate every draft it produced,
// with no way to tell the copies apart), or it has no stored source
// (source_kind is empty, e.g. a job row created before this feature).
var ErrNotRetryable = errors.New("ingest: job is not retryable")

// finishTimeout bounds the final Finish write in the finish helper below.
// It is deliberately short and derived from context.Background rather than
// the run's own (possibly already-expired) context, so a timed-out run can
// still record its failure.
const finishTimeout = 10 * time.Second

// source bundles one ingest run's origin, reference, optional pre-supplied
// content, and tags. It replaces threading sourceKind/sourceRef/rawText/
// tags as four separate parameters through start/run/acquireText/
// processChunk, where three adjacent string parameters could be silently
// swapped at a call site with no compile error.
type source struct {
	kind string // "url" or "text"
	ref  string // URL or filename
	text string // pre-supplied content; empty for kind == "url"
	tags []string
}

// Promoter creates a real, searchable knowledge item from a draft's final
// fields. It is the one sanctioned way ingest crosses into Qdrant: the
// package otherwise imports no qdrant code at all, so "drafts never reach
// the searchable corpus" stays an import-graph invariant, not a convention
// — this interface carries only primitives across that boundary, never a
// qdrant-typed value.
type Promoter interface {
	Promote(ctx context.Context, title, summary, body string, tags []string) (id string, err error)
}

// Service orchestrates the ingest pipeline: source acquisition, chunking,
// per-chunk translation and generation, and draft persistence. Acquisition
// and each chunk run as separate queue.Service operations (KindAcquire,
// KindChunk) rather than a per-job goroutine, so ingest's LLM/network work
// is sequenced through the same single-worker, priority-ordered queue as
// chat and generate.
type Service struct {
	db        *pgxpool.Pool
	jobs      *jobs.Service
	translate *translate.Service
	generate  *generate.Service
	promoter  Promoter
	queue     *queue.Service
}

// New builds a Service from its already-constructed dependencies, matching
// the constructor shape used by chat/generate/translate.
func New(db *pgxpool.Pool, jobsSvc *jobs.Service, translateSvc *translate.Service, generateSvc *generate.Service, promoter Promoter, q *queue.Service) *Service {
	return &Service{db: db, jobs: jobsSvc, translate: translateSvc, generate: generateSvc, promoter: promoter, queue: q}
}

// StartURL validates rawURL, guards against a job already running, starts a
// new "ingest" job, and enqueues the KindAcquire operation that fetches and
// processes it. It returns as soon as the job is recorded; the queue's
// worker does the work and reports progress via jobs.SetStep/Finish.
// Scheme/host validation happens synchronously here (per the spec's HTTP
// contract: a bad scheme or missing host is a 400, not a job that starts
// and then fails) — fetchURL repeats the same check inside HandleAcquire
// since it has no other caller to trust.
func (s *Service) StartURL(ctx context.Context, rawURL string, tags []string) (jobs.Job, error) {
	if err := validateURL(rawURL); err != nil {
		return jobs.Job{}, fmt.Errorf("%w: %w", ErrValidation, err)
	}
	return s.start(ctx, source{kind: "url", ref: rawURL, tags: tags})
}

// StartText validates filename/content via validateText, guards against a
// job already running, starts a new "ingest" job, and enqueues the
// KindAcquire operation that begins chunking (no fetch, no HTML stripping
// needed for this source kind).
func (s *Service) StartText(ctx context.Context, filename, content string, tags []string) (jobs.Job, error) {
	if err := validateText(filename, content); err != nil {
		return jobs.Job{}, fmt.Errorf("%w: %w", ErrValidation, err)
	}
	return s.start(ctx, source{kind: "text", ref: filename, text: content, tags: tags})
}

// start enforces the single-concurrent-job guard, starts the job row, and
// enqueues the KindAcquire operation that begins the pipeline — it returns
// as soon as that's done, never waiting on any fetch or LLM call. The
// queue's own worker (started once, for the app's whole lifetime, in
// main.go) does everything from here on, one operation at a time.
func (s *Service) start(ctx context.Context, src source) (jobs.Job, error) {
	current, err := s.jobs.Current(ctx)
	if err != nil {
		return jobs.Job{}, err
	}
	if current != nil {
		return jobs.Job{}, ErrJobRunning
	}

	job, err := s.jobs.Start(ctx, "ingest", src.kind, src.ref, src.text, src.tags)
	if err != nil {
		// The Current pre-check above is a fast-path optimization only; the
		// DB's partial unique index is the real guard against two
		// near-simultaneous requests both starting a job (see jobs.Start).
		// Translate its sentinel so callers only ever need to check
		// ErrJobRunning, regardless of which guard caught the race.
		if errors.Is(err, jobs.ErrAlreadyRunning) {
			return jobs.Job{}, ErrJobRunning
		}
		return jobs.Job{}, err
	}
	slog.Info("ingest job started", "job_id", job.ID, "source_kind", src.kind)

	if _, err := s.queue.Enqueue(context.WithoutCancel(ctx), KindAcquire, queue.PriorityBackground, acquirePayload{JobID: job.ID}); err != nil {
		s.finish(context.Background(), job.ID, err)
		return jobs.Job{}, err
	}

	return job, nil
}

// finish records a job's outcome using a fresh, short-lived deadline rather
// than the caller's own ctx, so a worker shutdown mid-run can still write
// the failure — reusing an already-cancelled ctx here would make Finish
// fail too, leaving the job stuck at status='running' forever (see
// FailStale's doc comment for the resulting blast radius). It also logs
// the job's outcome and duration (never its source/body content), per this
// feature's logging requirement.
func (s *Service) finish(ctx context.Context, jobID string, jobErr error) {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	outcome, sourceKind := "done", ""
	var createdAt time.Time
	if job, err := s.jobs.Get(finishCtx, jobID); err == nil {
		sourceKind, createdAt = job.SourceKind, job.CreatedAt
	}
	if jobErr != nil {
		outcome = "failed"
	}
	if err := s.jobs.Finish(finishCtx, jobID, jobErr); err != nil {
		log.Printf("ingest: job %s: failed to record finish: %v", jobID, err)
	}
	fields := []any{"job_id", jobID, "source_kind", sourceKind, "outcome", outcome}
	if !createdAt.IsZero() {
		fields = append(fields, "duration_ms", time.Since(createdAt).Milliseconds())
	}
	slog.Info("ingest job finished", fields...)
}

// HandleAcquire is the queue.HandlerFunc registered for KindAcquire. For a
// "url" job whose source_text is still empty, it fetches and extracts the
// text and persists it into the job row before chunking ever starts — a
// retried job whose earlier attempt already fetched successfully arrives
// here with source_text already populated (see Retry) and skips the fetch
// entirely, which is the fix for the bug where a URL retry always
// re-fetched even when the content had already been cached. A "text" job's
// source_text was already set at job creation (StartText), so there is
// nothing to acquire for it beyond the step label.
func (s *Service) HandleAcquire(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p acquirePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	job, err := s.jobs.Get(ctx, p.JobID)
	if err != nil {
		s.finish(ctx, p.JobID, err)
		return nil, err
	}

	if job.SourceKind == "url" && job.SourceText == "" {
		if err := s.jobs.SetStep(ctx, p.JobID, fmt.Sprintf("fetching %s", urlHost(job.SourceRef))); err != nil {
			log.Printf("ingest: job %s: failed to set step: %v", p.JobID, err)
		}
		body, contentType, err := fetchURL(ctx, job.SourceRef)
		if err != nil {
			s.finish(ctx, p.JobID, err)
			return nil, err
		}
		text := string(body)
		if contentType == "text/html" || contentType == "application/xhtml+xml" {
			if err := s.jobs.SetStep(ctx, p.JobID, "extracting text"); err != nil {
				log.Printf("ingest: job %s: failed to set step: %v", p.JobID, err)
			}
			text = stripHTML(text)
		}
		if err := s.jobs.SetSourceText(ctx, p.JobID, text); err != nil {
			s.finish(ctx, p.JobID, err)
			return nil, err
		}
	} else if job.SourceKind == "text" {
		if err := s.jobs.SetStep(ctx, p.JobID, "reading input"); err != nil {
			log.Printf("ingest: job %s: failed to set step: %v", p.JobID, err)
		}
	}

	if err := s.jobs.SetStep(ctx, p.JobID, "chunking"); err != nil {
		log.Printf("ingest: job %s: failed to set step: %v", p.JobID, err)
	}
	if _, err := s.queue.Enqueue(ctx, KindChunk, queue.PriorityBackground, chunkPayload{JobID: p.JobID, Index: 0}); err != nil {
		s.finish(ctx, p.JobID, err)
		return nil, err
	}
	return nil, nil
}

// HandleChunk is the queue.HandlerFunc registered for KindChunk. It
// re-derives the chunk list from the job's own stored source_text (a pure
// function of that text, so nothing about it needs to be duplicated across
// operations), processes exactly one chunk, then either enqueues the next
// chunk's operation or finishes the job — never both, and never loops
// in-process, so a chat reply or manual generate enqueued in the meantime
// gets a chance to run before the next chunk starts.
func (s *Service) HandleChunk(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p chunkPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	job, err := s.jobs.Get(ctx, p.JobID)
	if err != nil {
		s.finish(ctx, p.JobID, err)
		return nil, err
	}
	chunks, err := chunk(job.SourceText)
	if err != nil {
		s.finish(ctx, p.JobID, err)
		return nil, err
	}
	total := len(chunks)
	if p.Index >= total {
		s.finish(ctx, p.JobID, nil)
		return nil, nil
	}

	src := source{kind: job.SourceKind, ref: job.SourceRef, tags: job.SourceTags}
	if err := s.processChunk(ctx, p.JobID, src, p.Index, total, chunks[p.Index]); err != nil {
		s.finish(ctx, p.JobID, err)
		return nil, err
	}

	if p.Index+1 < total {
		if _, err := s.queue.Enqueue(ctx, KindChunk, queue.PriorityBackground, chunkPayload{JobID: p.JobID, Index: p.Index + 1}); err != nil {
			s.finish(ctx, p.JobID, err)
			return nil, err
		}
		return nil, nil
	}
	s.finish(ctx, p.JobID, nil)
	return nil, nil
}

// urlHost returns raw's host for display in a job step name, falling back
// to raw itself if it doesn't parse — this is cosmetic only; fetchURL does
// the real scheme/host validation.
func urlHost(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// processChunk translates, titles, summarizes, and saves one chunk as a
// draft, reporting a SetStep before each stage. A SetStep failure is
// logged but not fatal — it is a cosmetic progress label, and losing it
// must never discard already-completed or still-pending LLM work. Only a
// real stage failure (translate/generate/save) is fatal: it returns
// immediately without attempting the remaining stages for this chunk.
func (s *Service) processChunk(ctx context.Context, jobID string, src source, index, total int, chunkText string) error {
	label := fmt.Sprintf("chunk %d/%d", index+1, total)

	if err := s.jobs.SetStep(ctx, jobID, label+": translating"); err != nil {
		log.Printf("ingest: job %s: failed to set step: %v", jobID, err)
	}
	translated, err := s.translate.Translate(ctx, chunkText)
	if err != nil {
		return fmt.Errorf("%s: translate: %w", label, err)
	}

	if err := s.jobs.SetStep(ctx, jobID, label+": generating title"); err != nil {
		log.Printf("ingest: job %s: failed to set step: %v", jobID, err)
	}
	// TitleDirect/SummaryDirect, not Title/Summary: this runs inside a
	// KindChunk operation, which already owns the queue's one worker slot —
	// calling Title/Summary here would enqueue-and-await a second operation
	// that the same (single, busy) worker could never get to, deadlocking.
	title, err := s.generate.TitleDirect(ctx, translated)
	if err != nil {
		return fmt.Errorf("%s: generate title: %w", label, err)
	}

	if err := s.jobs.SetStep(ctx, jobID, label+": generating summary"); err != nil {
		log.Printf("ingest: job %s: failed to set step: %v", jobID, err)
	}
	summary, err := s.generate.SummaryDirect(ctx, translated)
	if err != nil {
		return fmt.Errorf("%s: generate summary: %w", label, err)
	}

	if err := s.jobs.SetStep(ctx, jobID, label+": saving draft"); err != nil {
		log.Printf("ingest: job %s: failed to set step: %v", jobID, err)
	}
	if _, err := createDraft(ctx, s.db, jobID, src.kind, src.ref, index, title, summary, translated, src.tags); err != nil {
		return fmt.Errorf("%s: save draft: %w", label, err)
	}

	return nil
}

// GetDraft returns one draft's full row, including body. It has no
// validation step of its own: getDraft's isUUID check at the DB-layer
// boundary is the only guard a pure lookup needs, unlike UpdateDraft, which
// must also validate the fields it is about to write.
func (s *Service) GetDraft(ctx context.Context, id string) (Draft, error) {
	return getDraft(ctx, s.db, id)
}

// UpdateDraft validates title/summary/body via validateDraftFields before
// delegating to updateDraft, which — per its own doc comment — trusts its
// caller to have already done so. This is the one place in the draft
// lifecycle where that validation actually happens.
func (s *Service) UpdateDraft(ctx context.Context, id, title, summary, body string, tags []string) (Draft, error) {
	if err := validateDraftFields(title, summary, body); err != nil {
		return Draft{}, err
	}
	return updateDraft(ctx, s.db, id, title, summary, body, tags)
}

// DiscardDraft deletes a draft row permanently. Like GetDraft, it has no
// validation step of its own: deleteDraft's idempotent, isUUID-guarded
// delete is already the whole operation.
func (s *Service) DiscardDraft(ctx context.Context, id string) error {
	return deleteDraft(ctx, s.db, id)
}

// ListDrafts returns all draft summaries pending review, most recent first.
// See DraftSummary for why the full Body is omitted here.
func (s *Service) ListDrafts(ctx context.Context) ([]DraftSummary, error) {
	return listDrafts(ctx, s.db)
}

// approveDraftFields validates a claimed draft's own stored fields and,
// only if they pass, promotes it via s.promoter. It is factored out of
// ApproveDraft as its own method — rather than left inline as the closure
// passed to claimDraft — so this validate-then-promote logic can be unit
// tested directly against a Draft value (see
// TestApproveDraft_EmptyFieldsNeverCallsPromoter), without needing a live
// Postgres row to exercise claimDraft's DELETE...RETURNING/transaction
// machinery just to construct one.
//
// validateDraftFields runs before the promoter is ever called, so a draft
// that somehow ended up with an empty title/summary/body (e.g.
// generate.Title returning "" on an empty LLM completion) is rejected as
// ingest.ErrValidation here, inside this package's own validation function,
// rather than surfacing two packages downstream as qdrant.ErrValidation.
func (s *Service) approveDraftFields(ctx context.Context, d Draft) (string, error) {
	if err := validateDraftFields(d.Title, d.Summary, d.Body); err != nil {
		return "", err
	}
	return s.promoter.Promote(ctx, d.Title, d.Summary, d.Body, d.Tags)
}

// ApproveDraft promotes a draft into Qdrant as a real knowledge item, then
// removes the draft row, inside one transaction (see claimDraft). It
// returns the new knowledge item's id.
//
// Returning an error from approveDraftFields before it ever calls
// s.promoter.Promote still triggers claimDraft's normal rollback path (any
// non-nil error from the promote callback rolls back, regardless of which
// specific error it is), so the draft row is restored exactly as it would
// be for any other promote failure.
func (s *Service) ApproveDraft(ctx context.Context, id string) (string, error) {
	return claimDraft(ctx, s.db, id, func(d Draft) (string, error) {
		return s.approveDraftFields(ctx, d)
	})
}

// retrySource validates that job can be retried and, if so, returns the
// source to relaunch it from. Pure function over values — no dependency
// use — so every branch is unit-testable without a live job row.
func retrySource(job jobs.Job) (source, error) {
	switch {
	case job.Status == "running":
		return source{}, ErrJobRunning
	case job.Status != "failed":
		return source{}, fmt.Errorf("%w: job already %s", ErrNotRetryable, job.Status)
	case job.SourceKind != "url" && job.SourceKind != "text":
		return source{}, fmt.Errorf("%w: no stored source to retry from", ErrNotRetryable)
	}
	return source{kind: job.SourceKind, ref: job.SourceRef, text: job.SourceText, tags: job.SourceTags}, nil
}

// Retry re-launches a failed job from its stored source, as a brand-new
// job row — the original failed row is left untouched, as permanent
// history. It calls s.start directly (not StartURL/StartText): the source
// was already validated when the original job was submitted, and — this
// is the fix for the bug where a URL retry always re-fetched even when the
// content was already cached — going through StartURL would discard
// src.text (it has no text parameter at all), throwing away a successful
// prior fetch. start passes src.text through to the new job row
// unconditionally, exactly like it already does for a "text" source, and
// HandleAcquire skips re-fetching a "url" source once it sees that text is
// already there.
func (s *Service) Retry(ctx context.Context, id string) (jobs.Job, error) {
	if !isUUID(id) {
		return jobs.Job{}, ErrNotFound
	}
	job, err := s.jobs.Get(ctx, id)
	if err != nil {
		if errors.Is(err, jobs.ErrNotFound) {
			return jobs.Job{}, ErrNotFound
		}
		return jobs.Job{}, err
	}
	src, err := retrySource(job)
	if err != nil {
		return jobs.Job{}, err
	}
	return s.start(ctx, src)
}
