package jobs

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrAlreadyRunning is returned by Start when the jobs_one_running_idx
// partial unique index (see db.schema) rejects a second concurrent
// "running" row of the same kind — the DB-level backstop for the
// single-concurrent-job-per-kind guard (e.g. two "ingest" jobs can't run at
// once, since they'd contend for the same LLM), catching the race that
// Current's separate read-then-insert pre-check in ingest.Service.start
// cannot fully close on its own. A different job kind is free to run
// concurrently; the index is scoped on (kind) precisely so it doesn't.
var ErrAlreadyRunning = errors.New("jobs: a job is already running")

// ErrNotFound is returned by Get/Retry-adjacent lookups for a missing or
// malformed job id.
var ErrNotFound = errors.New("jobs: job not found")

// ErrRunning is returned by Delete when the target job is still running:
// removing the row would free jobs_one_running_idx while the job's detached
// goroutine is still writing to it via SetStep/Finish, silently disabling
// the single-concurrent-job guard for the rest of that run.
var ErrRunning = errors.New("jobs: job is still running")

// uniqueViolation is Postgres error code 23505 (unique_violation).
const uniqueViolation = "23505"

type Service struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Service { return &Service{db: db} }

type Job struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	Step       string    `json:"step"`
	Error      string    `json:"error,omitempty"`
	SourceKind string    `json:"source_kind,omitempty"`
	SourceRef  string    `json:"source_ref,omitempty"`
	SourceTags []string  `json:"source_tags,omitempty"`
	SourceText string    `json:"-"` // internal only (retry), never sent to the client — can be large
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// isUUID reports whether id has the shape this package's own uuid()
// produces (36 chars, hyphens at 8/13/18/23, lowercase hex elsewhere) — the
// same guard internal/ingest's drafts.go uses, duplicated here rather than
// shared, matching this codebase's existing uuid()-duplication precedent.
// It exists so a malformed path-parameter id never reaches Postgres.
func isUUID(id string) bool {
	const length = 36
	hyphenAt := map[int]bool{8: true, 13: true, 18: true, 23: true}
	if len(id) != length {
		return false
	}
	for i := 0; i < length; i++ {
		c := id[i]
		if hyphenAt[i] {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func uuid() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// Start records a new running job of the given kind, with the source it was
// launched from (sourceKind/sourceRef/sourceText — empty for a job kind that
// has no notion of a source) so a failed job can later be retried from the
// same input. If another job of the same kind is already running,
// jobs_one_running_idx rejects the insert with a unique violation, which
// Start translates into ErrAlreadyRunning.
func (s *Service) Start(ctx context.Context, kind, sourceKind, sourceRef, sourceText string, sourceTags []string) (Job, error) {
	id, err := uuid()
	if err != nil {
		return Job{}, err
	}
	if sourceTags == nil {
		sourceTags = []string{}
	}
	var j Job
	err = s.db.QueryRow(ctx,
		`INSERT INTO jobs(id,kind,status,source_kind,source_ref,source_text,source_tags) VALUES($1,$2,'running',$3,$4,$5,$6)
		 RETURNING `+jobColumns,
		id, kind, sourceKind, sourceRef, sourceText, sourceTags,
	).Scan(scanArgs(&j)...)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return Job{}, ErrAlreadyRunning
		}
		return Job{}, err
	}
	return j, nil
}

// SetStep updates a running job's human-readable current step.
func (s *Service) SetStep(ctx context.Context, id, step string) error {
	_, err := s.db.Exec(ctx, "UPDATE jobs SET step=$1, updated_at=now() WHERE id=$2", step, id)
	return err
}

// SetSourceText persists text acquired after the job row was created (a
// URL fetch's result) so a later retry can reuse it instead of re-fetching
// — see ingest.Service.HandleAcquire.
func (s *Service) SetSourceText(ctx context.Context, id, text string) error {
	_, err := s.db.Exec(ctx, "UPDATE jobs SET source_text=$1, updated_at=now() WHERE id=$2", text, id)
	return err
}

// jobColumns is the column list every full-row Job query scans, factored
// out so List/Get/Current/Start's RETURNING stay in sync by construction.
const jobColumns = "id,kind,status,step,coalesce(error,''),coalesce(source_kind,''),coalesce(source_ref,''),coalesce(source_text,''),source_tags,created_at,updated_at"

func scanArgs(j *Job) []any {
	return []any{&j.ID, &j.Kind, &j.Status, &j.Step, &j.Error, &j.SourceKind, &j.SourceRef, &j.SourceText, &j.SourceTags, &j.CreatedAt, &j.UpdatedAt}
}

type scanner interface {
	Scan(dest ...any) error
}

func scanJob(row scanner) (Job, error) {
	var j Job
	err := row.Scan(scanArgs(&j)...)
	return j, err
}

// Get returns one job by id, or ErrNotFound if it doesn't exist or id is
// malformed. isUUID is checked before any query runs.
func (s *Service) Get(ctx context.Context, id string) (Job, error) {
	if !isUUID(id) {
		return Job{}, ErrNotFound
	}
	row := s.db.QueryRow(ctx, "SELECT "+jobColumns+" FROM jobs WHERE id=$1", id)
	j, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return j, err
}

// List returns the most recent 500 jobs of any status, most recent first.
// Unlike drafts, job rows are never deleted by the pipeline itself, so this
// cap is load-bearing, not merely defensive.
func (s *Service) List(ctx context.Context) ([]Job, error) {
	rows, err := s.db.Query(ctx, "SELECT "+jobColumns+" FROM jobs ORDER BY created_at DESC LIMIT 500")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Delete removes a job row permanently. Idempotent for a missing or
// malformed id (matches discardDraft/deleteChat), but refuses a still-
// running job with ErrRunning: removing that row would free
// jobs_one_running_idx while the job's own goroutine is still writing to it,
// silently disabling the single-concurrent-job guard mid-run.
func (s *Service) Delete(ctx context.Context, id string) error {
	if !isUUID(id) {
		return nil
	}
	tag, err := s.db.Exec(ctx, "DELETE FROM jobs WHERE id=$1 AND status <> 'running'", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var status string
	err = s.db.QueryRow(ctx, "SELECT status FROM jobs WHERE id=$1", id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already gone
	}
	if err != nil {
		return err
	}
	return ErrRunning
}

// Finish marks a job done (jobErr == nil) or failed (jobErr's message is
// recorded). Named jobErr, not err, so it's never confused with this
// method's own return value. On success, source_text is cleared: retry of
// a done job is rejected by design, so a successful job's stored source
// text is dead weight duplicating content already in knowledge_drafts.body
// — clearing it is what keeps unbounded row growth limited to failed jobs
// only, since this slice has no pruning.
func (s *Service) Finish(ctx context.Context, id string, jobErr error) error {
	status := "done"
	var errText *string
	if jobErr != nil {
		status = "failed"
		text := jobErr.Error()
		errText = &text
	}
	_, err := s.db.Exec(ctx,
		`UPDATE jobs SET status=$1, error=$2, updated_at=now(),
		 source_text = CASE WHEN $1='done' THEN '' ELSE source_text END
		 WHERE id=$3`,
		status, errText, id)
	return err
}

// Current returns the most recently updated running job, or nil if none.
func (s *Service) Current(ctx context.Context) (*Job, error) {
	var j Job
	err := s.db.QueryRow(ctx, "SELECT id,kind,status,step,coalesce(error,''),created_at,updated_at FROM jobs WHERE status='running' ORDER BY updated_at DESC LIMIT 1").
		Scan(&j.ID, &j.Kind, &j.Status, &j.Step, &j.Error, &j.CreatedAt, &j.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// FailStale marks any still-"running" job as failed. A server restart
// mid-run leaves a job row stuck at status='running' forever, since nothing
// else ever calls Finish for it; left alone, that row would both pin
// Current's "current job" status line permanently and permanently trip the
// single-concurrent-job 409 guard in the ingest handlers. Called once at
// server startup, before any new job can start.
func (s *Service) FailStale(ctx context.Context) error {
	_, err := s.db.Exec(ctx, "UPDATE jobs SET status='failed', error='interrupted by restart', updated_at=now() WHERE status='running'")
	return err
}
