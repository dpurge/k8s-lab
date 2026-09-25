package texts

import (
	"context"
	"html/template"
	"time"

	"github.com/dpurge/cli-tools/pkg/tool/markdown"
	"github.com/jackc/pgx/v5/pgxpool"

	"phraseforge/internal/pagination"
)

// Text is one PhraseForge markdown text.
type Text struct {
	ID            int64
	UserID        int64
	Title         string
	Body          string
	Transcription string // "" if none — cli-tools' "pinned Latin/LTR romanization"
	Language      string
	Script        string
	IngestSource  string // "" if manually created or pasted-text-ingested; else the URL/filename it came from
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Store persists texts in Postgres.
type Store struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

const selectCols = `id, user_id, title, body, coalesce(transcription, ''), language, script, coalesce(ingest_source, ''), created_at, updated_at`

func scanText(row interface{ Scan(...any) error }, t *Text) error {
	return row.Scan(&t.ID, &t.UserID, &t.Title, &t.Body, &t.Transcription, &t.Language, &t.Script, &t.IngestSource, &t.CreatedAt, &t.UpdatedAt)
}

// List returns texts in the given languages, most recent first. If all is
// true (the caller is admin), languages is ignored and every text is returned.
func (s *Store) List(ctx context.Context, languages []string, all bool) ([]Text, error) {
	var rows pgxRows
	var err error
	if all {
		rows, err = s.db.Query(ctx, `SELECT `+selectCols+` FROM texts ORDER BY created_at DESC`)
	} else {
		if len(languages) == 0 {
			return nil, nil // no language access granted yet — nothing to show
		}
		rows, err = s.db.Query(ctx, `SELECT `+selectCols+` FROM texts WHERE language = ANY($1) ORDER BY created_at DESC`, languages)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Text
	for rows.Next() {
		var t Text
		if err := scanText(rows, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListPage mirrors List's language/all semantics exactly, adding keyset
// pagination (see specs/features/phraseforge-spa-pagination.md): tagIDs
// nil means no tag filter; non-nil (including empty) restricts to exactly
// those ids — the caller computes it from tags.Store.ResourceIDsWithTag/
// ResourceIDsWithAllTags before calling this, so the restriction applies
// in SQL before LIMIT, not after (the bug this feature fixes — filtering
// an already-paginated page in Go can yield an incorrectly-empty page even
// when matches exist further back). cursor nil means "first page". Fetches
// limit+1 rows and trims the extra one to compute hasMore, without a
// separate COUNT(*) query.
func (s *Store) ListPage(ctx context.Context, languages []string, all bool, tagIDs []int64, cursor *pagination.Cursor, limit int) (items []Text, hasMore bool, err error) {
	if !all && len(languages) == 0 {
		return nil, false, nil // no language access granted yet — nothing to show
	}
	var cursorCreatedAt any
	var cursorID any
	if cursor != nil {
		cursorCreatedAt, cursorID = cursor.CreatedAt, cursor.ID
	}
	var rows pgxRows
	if all {
		rows, err = s.db.Query(ctx, `
			SELECT `+selectCols+` FROM texts
			WHERE ($1::bigint[] IS NULL OR id = ANY($1))
			  AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3))
			ORDER BY created_at DESC, id DESC
			LIMIT $4`, tagIDs, cursorCreatedAt, cursorID, limit+1)
	} else {
		rows, err = s.db.Query(ctx, `
			SELECT `+selectCols+` FROM texts
			WHERE language = ANY($1)
			  AND ($2::bigint[] IS NULL OR id = ANY($2))
			  AND ($3::timestamptz IS NULL OR (created_at, id) < ($3, $4))
			ORDER BY created_at DESC, id DESC
			LIMIT $5`, languages, tagIDs, cursorCreatedAt, cursorID, limit+1)
	}
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var out []Text
	for rows.Next() {
		var t Text
		if err := scanText(rows, &t); err != nil {
			return nil, false, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(out) > limit {
		out = out[:limit]
		hasMore = true
	}
	return out, hasMore, nil
}

// Get fetches one text by id, regardless of language — callers check
// CanView(text.Language) themselves before showing it.
func (s *Store) Get(ctx context.Context, id int64) (Text, error) {
	var t Text
	err := scanText(s.db.QueryRow(ctx, `SELECT `+selectCols+` FROM texts WHERE id = $1`, id), &t)
	return t, err
}

// Create inserts a new text, recording userID as its creator. ingestSource is
// the URL or original filename it was ingested from, or "" for a manually
// created or pasted-text-ingested row.
func (s *Store) Create(ctx context.Context, userID int64, title, body, transcription, language, script, ingestSource string) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx,
		`INSERT INTO texts (user_id, title, body, transcription, language, script, ingest_source) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		userID, title, body, nullIfEmpty(transcription), language, script, nullIfEmpty(ingestSource)).Scan(&id)
	return id, err
}

// Update overwrites a text. Callers check CanEdit(language) — both the
// existing and, if changed, the new language — before calling this.
func (s *Store) Update(ctx context.Context, id int64, title, body, transcription, language, script string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE texts SET title = $1, body = $2, transcription = $3, language = $4, script = $5, updated_at = now() WHERE id = $6`,
		title, body, nullIfEmpty(transcription), language, script, id)
	return err
}

// GetBody returns just a text's body — used by phraseforge/internal/ingest's
// retry-idempotency check to recover the content an earlier, already-
// succeeded run of the same (job-id-carried-forward) job produced, without
// exposing the rest of Get's Text struct to a caller that only needs this
// one field.
func (s *Store) GetBody(ctx context.Context, id int64) (string, error) {
	t, err := s.Get(ctx, id)
	return t.Body, err
}

// SetTitleIfBlank writes title only if the row's title is still blank —
// used by a background generate job's completion (background-generate-
// title-transcription-translation) so it never clobbers a value a user
// already set, even a minutes-long job that outlives an edit. The blank
// check runs inside the same guarded UPDATE (not a Go-side read-then-write)
// so a concurrent edit can never slip between the check and the write.
// applied is false (not an error) when the title was already non-blank.
func (s *Store) SetTitleIfBlank(ctx context.Context, id int64, title string) (applied bool, err error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE texts SET title = $1, updated_at = now() WHERE id = $2 AND coalesce(trim(title), '') = ''`,
		title, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SetTranscriptionIfBlank mirrors SetTitleIfBlank — see that method's doc
// comment.
func (s *Store) SetTranscriptionIfBlank(ctx context.Context, id int64, transcription string) (applied bool, err error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE texts SET transcription = $1, updated_at = now() WHERE id = $2 AND coalesce(trim(transcription), '') = ''`,
		nullIfEmpty(transcription), id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Delete removes a text by id.
func (s *Store) Delete(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM texts WHERE id = $1`, id)
	return err
}

// RenderHTML converts a text's markdown body to HTML using cli-tools' PhraseForge
// Goldmark extension (vocabulary/dialog/section blocks, CommonMark/GFM otherwise).
func RenderHTML(body string) (template.HTML, error) {
	out, err := markdown.ToHTML([]byte(body))
	if err != nil {
		return "", err
	}
	return template.HTML(out), nil // #nosec G203 -- goldmark output, not raw user HTML
}

// pgxRows is the subset of pgx.Rows this package needs — lets scanText share
// code between Query's row iterator and QueryRow's single-row result.
type pgxRows interface {
	Next() bool
	Scan(...any) error
	Close()
	Err() error
}
