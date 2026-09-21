package ingest

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"knowledge/internal/generate"
	"knowledge/internal/jobs"
	"knowledge/internal/translate"
)

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

// baseTimeout bounds the fetch/read-and-chunk phase of a run: the part that
// happens before the number of chunks — and therefore the amount of
// per-chunk LLM work ahead — is known. Fetch is already separately bounded
// by fetchTimeout and MaxSourceBytes, and chunking is pure in-memory
// computation, so a few minutes of headroom here is generous. It is also
// reused as the fixed component of the per-chunk deadline derived in run,
// covering the same fetch/chunk phase inside that larger budget.
const baseTimeout = 5 * time.Minute

// perChunkTimeout bounds the LLM/save budget for a single chunk:
// translate, title, and summary are three separate LLM calls, each already
// bounded by shared/llm's 2-minute client timeout, plus one draft save.
// 8 minutes is 4x the per-call timeout, giving comfortable margin (more
// than the minimum 3x) for one call to run slow without starving the
// others. run multiplies this by the actual chunk count once Chunk has
// run, so the deadline scales with the real amount of work rather than
// being fixed before that count is known.
const perChunkTimeout = 8 * time.Minute

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
// per-chunk translation and generation, and draft persistence.
type Service struct {
	db        *pgxpool.Pool
	jobs      *jobs.Service
	translate *translate.Service
	generate  *generate.Service
	promoter  Promoter
}

// New builds a Service from its already-constructed dependencies, matching
// the constructor shape used by chat/generate/translate.
func New(db *pgxpool.Pool, jobsSvc *jobs.Service, translateSvc *translate.Service, generateSvc *generate.Service, promoter Promoter) *Service {
	return &Service{db: db, jobs: jobsSvc, translate: translateSvc, generate: generateSvc, promoter: promoter}
}

// StartURL validates rawURL, guards against a job already running, starts a
// new "ingest" job, and launches the fetch+chunk+LLM pipeline in a detached
// goroutine. It returns as soon as the job is recorded; the goroutine does
// the work and reports progress via jobs.SetStep/Finish. Scheme/host
// validation happens synchronously here (per the spec's HTTP contract: a bad
// scheme or missing host is a 400, not a job that starts and then fails) —
// fetchURL repeats the same check inside the goroutine since it has no other
// caller to trust.
func (s *Service) StartURL(ctx context.Context, rawURL string, tags []string) (jobs.Job, error) {
	if err := validateURL(rawURL); err != nil {
		return jobs.Job{}, fmt.Errorf("%w: %w", ErrValidation, err)
	}
	return s.start(ctx, source{kind: "url", ref: rawURL, tags: tags})
}

// StartText validates filename/content via validateText, guards against a
// job already running, starts a new "ingest" job, and launches the
// chunk+LLM pipeline (no fetch, no HTML stripping) in a detached goroutine.
func (s *Service) StartText(ctx context.Context, filename, content string, tags []string) (jobs.Job, error) {
	if err := validateText(filename, content); err != nil {
		return jobs.Job{}, fmt.Errorf("%w: %w", ErrValidation, err)
	}
	return s.start(ctx, source{kind: "text", ref: filename, text: content, tags: tags})
}

// start enforces the single-concurrent-job guard, starts the job row, and
// launches run in a goroutine over a context detached from ctx — the
// goroutine must outlive the HTTP request that triggered it, so it gets its
// own timeout rather than inheriting the request's cancellation. Only
// baseTimeout is applied here: the chunk count (and therefore the size of
// the much larger per-chunk budget) isn't known until run calls Chunk, so
// run derives its own, larger deadline for that phase once it is.
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

	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), baseTimeout)
	go func() {
		defer cancel()
		s.run(runCtx, job.ID, src)
	}()

	return job, nil
}

// run is the detached goroutine body launched by start. It must never
// return without calling s.jobs.Finish exactly once: this runs in the
// background with no caller left to hand an error to, so Finish is the only
// way a failure (or success) becomes visible through /api/v1/jobs/current.
func (s *Service) run(ctx context.Context, jobID string, src source) {
	text, err := s.acquireText(ctx, jobID, src)
	if err != nil {
		s.finish(ctx, jobID, err)
		return
	}

	if err := s.jobs.SetStep(ctx, jobID, "chunking"); err != nil {
		log.Printf("ingest: job %s: failed to set step: %v", jobID, err)
	}
	chunks, err := chunk(text)
	if err != nil {
		s.finish(ctx, jobID, err)
		return
	}

	// The per-chunk deadline is derived here, now that the chunk count is
	// known, instead of being fixed in start before Chunk ever ran: it must
	// scale with the actual amount of per-chunk LLM work ahead. ctx's own
	// deadline (baseTimeout, set by start) covered only the acquire+chunk
	// phase above; context.WithoutCancel strips that now-irrelevant, much
	// smaller deadline before WithTimeout applies the new, proportional one
	// — without it, the earlier and smaller deadline would still win.
	total := len(chunks)
	chunkTimeout := baseTimeout + perChunkTimeout*time.Duration(total)
	chunkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), chunkTimeout)
	defer cancel()

	for i, c := range chunks {
		if err := s.processChunk(chunkCtx, jobID, src, i, total, c); err != nil {
			s.finish(ctx, jobID, err)
			return
		}
	}

	s.finish(ctx, jobID, nil)
}

// finish records a job's outcome using a fresh, short-lived deadline rather
// than run's own ctx, so a run that hit its deadline can still write its
// failure — reusing an already-expired ctx here would make Finish fail too,
// leaving the job stuck at status='running' forever (see FailStale's doc
// comment for the resulting blast radius). context.WithoutCancel(ctx) keeps
// any trace/log values ctx may carry while discarding its
// deadline/cancellation, and WithTimeout then adds a fresh one. If Finish
// itself still fails (e.g. Postgres is genuinely down), that failure has no
// other caller to surface to, so it is logged here.
func (s *Service) finish(ctx context.Context, jobID string, jobErr error) {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if err := s.jobs.Finish(finishCtx, jobID, jobErr); err != nil {
		log.Printf("ingest: job %s: failed to record finish: %v", jobID, err)
	}
}

// acquireText fetches the URL or accepts the already-provided text,
// reporting the fetch/read and extract steps as it goes. For a "url"
// source, an HTML response is stripped to plain text; text/plain and
// text/markdown responses pass through unchanged. For a "text" source,
// src.text was already validated by the caller (StartText) and is used as
// is — no HTML stripping, and so no separate "extracting text" step: there
// is nothing to extract from content that is already plain text.
func (s *Service) acquireText(ctx context.Context, jobID string, src source) (string, error) {
	switch src.kind {
	case "url":
		if err := s.jobs.SetStep(ctx, jobID, fmt.Sprintf("fetching %s", urlHost(src.ref))); err != nil {
			log.Printf("ingest: job %s: failed to set step: %v", jobID, err)
		}
		body, contentType, err := fetchURL(ctx, src.ref)
		if err != nil {
			return "", err
		}
		if err := s.jobs.SetStep(ctx, jobID, "extracting text"); err != nil {
			log.Printf("ingest: job %s: failed to set step: %v", jobID, err)
		}
		if contentType == "text/html" || contentType == "application/xhtml+xml" {
			return stripHTML(string(body)), nil
		}
		return string(body), nil
	case "text":
		if err := s.jobs.SetStep(ctx, jobID, "reading input"); err != nil {
			log.Printf("ingest: job %s: failed to set step: %v", jobID, err)
		}
		return src.text, nil
	default:
		return "", fmt.Errorf("ingest: unknown source kind %q", src.kind)
	}
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
	title, err := s.generate.Title(ctx, translated)
	if err != nil {
		return fmt.Errorf("%s: generate title: %w", label, err)
	}

	if err := s.jobs.SetStep(ctx, jobID, label+": generating summary"); err != nil {
		log.Printf("ingest: job %s: failed to set step: %v", jobID, err)
	}
	summary, err := s.generate.Summary(ctx, translated)
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
// history. Retry re-enters through StartURL/StartText, so it gets the same
// synchronous validation and the same single-concurrent-job guard
// (jobs_one_running_idx) as a fresh submission; a stored-but-since-invalid
// source is rejected as ErrValidation, not silently retried.
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
	switch src.kind {
	case "url":
		return s.StartURL(ctx, src.ref, src.tags)
	default: // "text"
		return s.StartText(ctx, src.ref, src.text, src.tags)
	}
}
