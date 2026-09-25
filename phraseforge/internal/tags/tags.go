package tags

import (
	"context"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

// Parse splits a user-entered comma-separated tag list into normalized names:
// trimmed, lowercased, deduplicated, empties dropped. Shared by every
// resource type's create/edit handler so "Travel, travel ,ROMANIAN" always
// becomes the same two tags.
func Parse(input string) []string {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(input, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ensureTagIDs gets-or-creates each name and returns their tag ids.
func (s *Store) ensureTagIDs(ctx context.Context, names []string) ([]int64, error) {
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		var id int64
		err := s.db.QueryRow(ctx, `
			INSERT INTO tag (name) VALUES ($1)
			ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
			RETURNING id`, name).Scan(&id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// SetFor replaces the complete tag set on one resource. Pass already-Parsed
// names. An empty slice clears all tags from the resource.
func (s *Store) SetFor(ctx context.Context, resourceType string, resourceID int64, names []string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	if _, err := tx.Exec(ctx, `DELETE FROM tagging WHERE resource_type = $1 AND resource_id = $2`, resourceType, resourceID); err != nil {
		return err
	}
	for _, name := range names {
		var tagID int64
		err := tx.QueryRow(ctx, `
			INSERT INTO tag (name) VALUES ($1)
			ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
			RETURNING id`, name).Scan(&tagID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO tagging (tag_id, resource_type, resource_id) VALUES ($1, $2, $3)`,
			tagID, resourceType, resourceID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeleteFor removes every tagging on one resource — the caller (e.g. the
// server's handleDelete, alongside texts.Store.Delete) is responsible for
// calling this when a resource is actually deleted, since tagging.resource_id
// has no real FK to enforce cleanup automatically (see schema.sql).
func (s *Store) DeleteFor(ctx context.Context, resourceType string, resourceID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM tagging WHERE resource_type = $1 AND resource_id = $2`, resourceType, resourceID)
	return err
}

// For returns one resource's tag names, alphabetically.
func (s *Store) For(ctx context.Context, resourceType string, resourceID int64) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT t.name FROM tagging tg JOIN tag t ON t.id = tg.tag_id
		WHERE tg.resource_type = $1 AND tg.resource_id = $2 ORDER BY t.name`, resourceType, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// ForMany returns tag names for several resources at once, keyed by resource
// id — avoids one query per row on a list page.
func (s *Store) ForMany(ctx context.Context, resourceType string, resourceIDs []int64) (map[int64][]string, error) {
	out := make(map[int64][]string, len(resourceIDs))
	if len(resourceIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT tg.resource_id, t.name FROM tagging tg JOIN tag t ON t.id = tg.tag_id
		WHERE tg.resource_type = $1 AND tg.resource_id = ANY($2)
		ORDER BY t.name`, resourceType, resourceIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = append(out[id], name)
	}
	return out, rows.Err()
}

// ResourceIDsWithTag returns the resource ids of resourceType tagged with
// tagName — the basis for "browse by tag" filtering.
func (s *Store) ResourceIDsWithTag(ctx context.Context, resourceType, tagName string) ([]int64, error) {
	rows, err := s.db.Query(ctx, `
		SELECT tg.resource_id FROM tagging tg JOIN tag t ON t.id = tg.tag_id
		WHERE tg.resource_type = $1 AND t.name = $2`, resourceType, strings.ToLower(strings.TrimSpace(tagName)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ResourceIDsWithAllTags returns the resource ids of resourceType that carry
// every one of names (ALL-match) — the basis for export's optional tags=
// filter. names must already be normalized (Parse) and non-empty: an empty
// names slice would make the HAVING count meaningless (every resource_id
// with at least one tagging would satisfy COUNT(...) >= 0), which is not
// "no filter" — callers must skip calling this when there's nothing to
// filter by, matching ResourceIDsWithTag's own single-tag precedent.
func (s *Store) ResourceIDsWithAllTags(ctx context.Context, resourceType string, names []string) ([]int64, error) {
	rows, err := s.db.Query(ctx, `
		SELECT tg.resource_id FROM tagging tg JOIN tag t ON t.id = tg.tag_id
		WHERE tg.resource_type = $1 AND t.name = ANY($2)
		GROUP BY tg.resource_id
		HAVING COUNT(DISTINCT t.name) = $3`, resourceType, names, len(names))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AllNames returns every declared tag name, for a datalist autocomplete on
// the tag input field. Tags are shared/global across resource types — a
// "travel" tag means the same thing whether it's on a text or a future dialog.
func (s *Store) AllNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT name FROM tag ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
