package ingest

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned by getDraft/updateDraft/claimDraft when no draft
// matches the given id — either because the id is well-formed but no row
// exists, or because it isn't even UUID-shaped (see isUUID), following the
// same errors.Is-checkable sentinel convention as jobs.Current's nil-return
// case and this package's own ErrValidation. claimDraft's double-approve
// guard depends on this: a second concurrent approve finds no row left and
// returns this same sentinel.
var ErrNotFound = errors.New("ingest: draft not found")

// Draft is one chunk produced by the ingest pipeline, pending human review
// before it can become a real knowledge item. It is never written to
// Qdrant, so it never appears in search or chat results.
type Draft struct {
	ID         string    `json:"id"`
	JobID      string    `json:"job_id,omitempty"`
	SourceKind string    `json:"source_kind"`
	SourceRef  string    `json:"source_ref"`
	ChunkIndex int       `json:"chunk_index"`
	Title      string    `json:"title"`
	Summary    string    `json:"summary"`
	Body       string    `json:"body"`
	Tags       []string  `json:"tags"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// DraftSummary is Draft's list-view projection: every field except Body,
// mirroring qdrant.Item/qdrant.ListItem's existing split so a list endpoint
// never pulls potentially large chunk bodies over the wire just to render a
// list. getDraft/GetDraft return the full Draft (with Body) for the
// single-draft review endpoint; createDraft also uses the full Draft
// internally, since it has just written body itself.
type DraftSummary struct {
	ID         string    `json:"id"`
	JobID      string    `json:"job_id,omitempty"`
	SourceKind string    `json:"source_kind"`
	SourceRef  string    `json:"source_ref"`
	ChunkIndex int       `json:"chunk_index"`
	Title      string    `json:"title"`
	Summary    string    `json:"summary"`
	Tags       []string  `json:"tags"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
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

// createDraft inserts one draft row and returns it as persisted, including
// the created_at/updated_at defaults assigned by the database.
func createDraft(ctx context.Context, db *pgxpool.Pool, jobID string, sourceKind, sourceRef string, chunkIndex int, title, summary, body string, tags []string) (Draft, error) {
	id, err := uuid()
	if err != nil {
		return Draft{}, err
	}
	if tags == nil {
		tags = []string{}
	}
	// job_id is nullable (ON DELETE SET NULL); an empty jobID must bind as
	// SQL NULL, not the invalid empty-string UUID.
	var jobIDArg any
	if jobID != "" {
		jobIDArg = jobID
	}
	var d Draft
	err = db.QueryRow(ctx,
		`INSERT INTO knowledge_drafts(id, job_id, source_kind, source_ref, chunk_index, title, summary, body, tags)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 RETURNING id, coalesce(job_id::text,''), source_kind, source_ref, chunk_index, title, summary, body, tags, created_at, updated_at`,
		id, jobIDArg, sourceKind, sourceRef, chunkIndex, title, summary, body, tags,
	).Scan(&d.ID, &d.JobID, &d.SourceKind, &d.SourceRef, &d.ChunkIndex, &d.Title, &d.Summary, &d.Body, &d.Tags, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

// listDrafts returns the 500 most recently created draft rows, most recent
// first, or an empty (non-nil) slice if there are none, as DraftSummary —
// the body column is deliberately omitted from the SELECT, not just from
// the response shape, so listing never pulls it off disk or over the wire.
// deleteDraft and claimDraft both remove rows, so the table isn't strictly
// ever-growing, but the LIMIT remains a sane defensive ceiling regardless —
// it is a simple cap, not pagination; real pagination belongs to the future
// review-UI feature.
func listDrafts(ctx context.Context, db *pgxpool.Pool) ([]DraftSummary, error) {
	rows, err := db.Query(ctx,
		`SELECT id, coalesce(job_id::text,''), source_kind, source_ref, chunk_index, title, summary, tags, created_at, updated_at
		 FROM knowledge_drafts ORDER BY created_at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DraftSummary{}
	for rows.Next() {
		var d DraftSummary
		if err := rows.Scan(&d.ID, &d.JobID, &d.SourceKind, &d.SourceRef, &d.ChunkIndex, &d.Title, &d.Summary, &d.Tags, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// isUUID reports whether id has the exact shape this package's own uuid()
// function produces: 36 characters, hyphens at positions 8, 13, 18, and 23,
// and every other character a lowercase hex digit. It exists so a malformed
// {id} path parameter is rejected before it ever reaches Postgres, which
// would otherwise return a confusing 22P02 invalid_text_representation error
// instead of a clean not-found. It deliberately does not use regexp — a
// fixed-shape check this simple doesn't need it.
func isUUID(id string) bool {
	const (
		length = 36
	)
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
		isLowerHexDigit := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !isLowerHexDigit {
			return false
		}
	}
	return true
}

// validateDraftFields rejects an empty or whitespace-only title, summary, or
// body, wrapping ErrValidation the same way StartURL/StartText do, so
// callers can use errors.Is(err, ErrValidation) regardless of which
// validation function produced it. It does not validate tags: the existing
// qdrant.NormalizeTags (applied by the HTTP handler, not here, to keep this
// package qdrant-free) already handles that.
func validateDraftFields(title, summary, body string) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("%w: %w", ErrValidation, errors.New("title is required"))
	}
	if strings.TrimSpace(summary) == "" {
		return fmt.Errorf("%w: %w", ErrValidation, errors.New("summary is required"))
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("%w: %w", ErrValidation, errors.New("body is required"))
	}
	return nil
}

// getDraft returns one draft's full row, including body, unlike
// listDrafts's DraftSummary projection. isUUID is checked before any query
// runs, so a malformed id never reaches Postgres.
func getDraft(ctx context.Context, db *pgxpool.Pool, id string) (Draft, error) {
	if !isUUID(id) {
		return Draft{}, ErrNotFound
	}
	var d Draft
	err := db.QueryRow(ctx,
		`SELECT id, coalesce(job_id::text,''), source_kind, source_ref, chunk_index, title, summary, body, tags, created_at, updated_at
		 FROM knowledge_drafts WHERE id=$1`,
		id,
	).Scan(&d.ID, &d.JobID, &d.SourceKind, &d.SourceRef, &d.ChunkIndex, &d.Title, &d.Summary, &d.Body, &d.Tags, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	if err != nil {
		return Draft{}, err
	}
	return d, nil
}

// updateDraft overwrites a draft's title/summary/body/tags and returns the
// row as persisted. It never touches source_kind/source_ref/chunk_index/
// job_id: those four columns are provenance, immutable per the approved
// spec. isUUID is checked before any query runs, so a malformed id never
// reaches Postgres.
//
// This is a thin DB-layer function: it trusts its caller already validated
// title/summary/body via validateDraftFields. It does not call
// validateDraftFields itself — that is the service layer's job.
func updateDraft(ctx context.Context, db *pgxpool.Pool, id, title, summary, body string, tags []string) (Draft, error) {
	if !isUUID(id) {
		return Draft{}, ErrNotFound
	}
	if tags == nil {
		tags = []string{}
	}
	var d Draft
	err := db.QueryRow(ctx,
		`UPDATE knowledge_drafts SET title=$1, summary=$2, body=$3, tags=$4, updated_at=now()
		 WHERE id=$5
		 RETURNING id, coalesce(job_id::text,''), source_kind, source_ref, chunk_index, title, summary, body, tags, created_at, updated_at`,
		title, summary, body, tags, id,
	).Scan(&d.ID, &d.JobID, &d.SourceKind, &d.SourceRef, &d.ChunkIndex, &d.Title, &d.Summary, &d.Body, &d.Tags, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	if err != nil {
		return Draft{}, err
	}
	return d, nil
}

// deleteDraft removes a draft row permanently. It is idempotent by design,
// matching this codebase's existing s.delete/s.deleteChat convention: a
// malformed id is treated as already-deleted (isUUID is checked before any
// query runs, so it never reaches Postgres), and deleting an id that
// doesn't exist is not an error — both return nil, so the caller can always
// respond 204.
func deleteDraft(ctx context.Context, db *pgxpool.Pool, id string) error {
	if !isUUID(id) {
		return nil
	}
	_, err := db.Exec(ctx, `DELETE FROM knowledge_drafts WHERE id=$1`, id)
	return err
}

// claimDraft atomically removes a draft row and hands its fields to promote
// inside one transaction: BEGIN, DELETE ... RETURNING (0 rows -> rollback,
// ErrNotFound — this closes the double-approve race: a second concurrent
// approve blocks on the row lock, then finds nothing once the first
// transaction commits), call promote, and on a promote error, ROLLBACK so
// the draft row is restored untouched. Only on a successful promote does
// the transaction COMMIT.
//
// Deliberate trade-off: this holds a pool connection open across whatever
// promote does (an embedding HTTP call, which can take seconds) — accepted
// for this single-developer local dev tool, not a pattern to reuse for a
// multi-tenant service. The residual failure mode (a COMMIT failing in the
// sub-millisecond window after promote already succeeded) produces a
// harmless, visible duplicate knowledge item — never silent data loss of a
// draft, which is why the DELETE happens before promote, not after.
func claimDraft(ctx context.Context, db *pgxpool.Pool, id string, promote func(Draft) (string, error)) (string, error) {
	if !isUUID(id) {
		return "", ErrNotFound
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) // no-op if already committed

	var d Draft
	err = tx.QueryRow(ctx,
		`DELETE FROM knowledge_drafts WHERE id=$1
		 RETURNING id, coalesce(job_id::text,''), source_kind, source_ref, chunk_index, title, summary, body, tags, created_at, updated_at`,
		id,
	).Scan(&d.ID, &d.JobID, &d.SourceKind, &d.SourceRef, &d.ChunkIndex, &d.Title, &d.Summary, &d.Body, &d.Tags, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}

	newID, err := promote(d)
	if err != nil {
		return "", err // deferred Rollback restores the draft
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return newID, nil
}
