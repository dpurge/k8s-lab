package vocabulary

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrItemNotFound is returned by UpdateItem/DeleteItem when position doesn't
// name an existing item in the list.
var ErrItemNotFound = errors.New("vocabulary item not found")

// List is one vocabulary list's own metadata (its items live separately).
type List struct {
	ID        int64
	UserID    int64
	Title     string
	Language  string
	Script    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Item is one vocabulary entry's common fields — the same for every viewer
// regardless of site locale.
type Item struct {
	Position      int
	Phrase        string
	Grammar       string
	Transcription string
}

// ItemTranslation is one item's per-locale translation/notes. Both may be
// empty — "position has an item but no translation yet in this locale" is
// the normal, expected state, not an error.
type ItemTranslation struct {
	Position    int
	Translation string
	Notes       string
}

type Store struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

const listCols = `id, user_id, title, language, script, created_at, updated_at`

func scanList(row interface{ Scan(...any) error }, l *List) error {
	return row.Scan(&l.ID, &l.UserID, &l.Title, &l.Language, &l.Script, &l.CreatedAt, &l.UpdatedAt)
}

// ListAll returns vocabulary lists in the given languages, most recent
// first. If all is true (the caller is admin), languages is ignored.
func (s *Store) ListAll(ctx context.Context, languages []string, all bool) ([]List, error) {
	var rows pgxRows
	var err error
	if all {
		rows, err = s.db.Query(ctx, `SELECT `+listCols+` FROM vocabulary_lists ORDER BY created_at DESC`)
	} else {
		if len(languages) == 0 {
			return nil, nil
		}
		rows, err = s.db.Query(ctx, `SELECT `+listCols+` FROM vocabulary_lists WHERE language = ANY($1) ORDER BY created_at DESC`, languages)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []List
	for rows.Next() {
		var l List
		if err := scanList(rows, &l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Get fetches one list by id, regardless of language — callers check
// CanView(list.Language) themselves before showing it.
func (s *Store) Get(ctx context.Context, id int64) (List, error) {
	var l List
	err := scanList(s.db.QueryRow(ctx, `SELECT `+listCols+` FROM vocabulary_lists WHERE id = $1`, id), &l)
	return l, err
}

// Create inserts a new (initially empty) list, recording userID as its creator.
func (s *Store) Create(ctx context.Context, userID int64, title, language, script string) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx,
		`INSERT INTO vocabulary_lists (user_id, title, language, script) VALUES ($1, $2, $3, $4) RETURNING id`,
		userID, title, language, script).Scan(&id)
	return id, err
}

// CreateFromText inserts a new list linked to a source text via
// source_text_id — used by phraseforge/internal/generate's "Generate
// Vocabulary" job the first time it runs for a given text (see
// specs/features/phraseforge-generate-vocab-models-from-text.md); a rerun
// instead finds the linked list via GetBySourceTextID and wholesale-replaces
// its items.
func (s *Store) CreateFromText(ctx context.Context, userID int64, title, language, script string, sourceTextID int64) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx,
		`INSERT INTO vocabulary_lists (user_id, title, language, script, source_text_id) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		userID, title, language, script, sourceTextID).Scan(&id)
	return id, err
}

// GetBySourceTextID returns the id of the list whose source_text_id equals
// textID, if one exists — found is false (not an error) when no list is
// linked to that text yet.
func (s *Store) GetBySourceTextID(ctx context.Context, textID int64) (id int64, found bool, err error) {
	err = s.db.QueryRow(ctx, `SELECT id FROM vocabulary_lists WHERE source_text_id = $1`, textID).Scan(&id)
	switch {
	case err == nil:
		return id, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return 0, false, nil
	default:
		return 0, false, err
	}
}

// UpdateMeta overwrites a list's own title/language/script (not its items).
func (s *Store) UpdateMeta(ctx context.Context, id int64, title, language, script string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE vocabulary_lists SET title = $1, language = $2, script = $3, updated_at = now() WHERE id = $4`,
		title, language, script, id)
	return err
}

// Delete removes a list (and its items/translations, via ON DELETE CASCADE).
func (s *Store) Delete(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM vocabulary_lists WHERE id = $1`, id)
	return err
}

// Begin starts a transaction against this store's own pool — exported so a
// caller (e.g. server.apiImportVocabulary's wholesale item replacement) can
// wrap a whole delete-existing-items + add-new-items + set-translations
// sequence for one list in a single transaction: a failure or client
// disconnect partway through must never leave a list's items permanently
// deleted with nothing re-added (see specs/features/
// phraseforge-export-import.md's B2 fix). The caller commits or rolls back
// tx itself once every step it wants atomic with the others has succeeded.
func (s *Store) Begin(ctx context.Context) (pgx.Tx, error) {
	return s.db.Begin(ctx)
}

// Items returns listID's common fields, in position order.
func (s *Store) Items(ctx context.Context, listID int64) ([]Item, error) {
	rows, err := s.db.Query(ctx,
		`SELECT position, phrase, coalesce(grammar, ''), coalesce(transcription, '')
		   FROM vocabulary_items WHERE list_id = $1 ORDER BY position`, listID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.Position, &it.Phrase, &it.Grammar, &it.Transcription); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// AddItem appends a new item at the next free position and returns it.
func (s *Store) AddItem(ctx context.Context, listID int64, phrase, grammar, transcription string) (int, error) {
	var pos int
	err := s.db.QueryRow(ctx, `
		INSERT INTO vocabulary_items (list_id, position, phrase, grammar, transcription)
		VALUES ($1, coalesce((SELECT max(position) + 1 FROM vocabulary_items WHERE list_id = $1), 0), $2, $3, $4)
		RETURNING position`,
		listID, phrase, nullIfEmpty(grammar), nullIfEmpty(transcription)).Scan(&pos)
	return pos, err
}

// AddItemTx is AddItem's logic run against an already-open transaction tx
// (see Begin's doc comment) instead of this store's own pool.
func (s *Store) AddItemTx(ctx context.Context, tx pgx.Tx, listID int64, phrase, grammar, transcription string) (int, error) {
	var pos int
	err := tx.QueryRow(ctx, `
		INSERT INTO vocabulary_items (list_id, position, phrase, grammar, transcription)
		VALUES ($1, coalesce((SELECT max(position) + 1 FROM vocabulary_items WHERE list_id = $1), 0), $2, $3, $4)
		RETURNING position`,
		listID, phrase, nullIfEmpty(grammar), nullIfEmpty(transcription)).Scan(&pos)
	return pos, err
}

// UpdateItem overwrites the common fields of the item at position. Returns
// ErrItemNotFound if position doesn't exist.
func (s *Store) UpdateItem(ctx context.Context, listID int64, position int, phrase, grammar, transcription string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE vocabulary_items SET phrase = $3, grammar = $4, transcription = $5
		WHERE list_id = $1 AND position = $2`,
		listID, position, phrase, nullIfEmpty(grammar), nullIfEmpty(transcription))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	return nil
}

// SetItemTranscription overwrites only the item at (listID, position)'s
// transcription — same out-of-order-safety rationale as
// texts.Store.SetTranscription (a background job's completion must never
// clobber a field it didn't touch); never touches phrase/grammar or any
// other item field. Guarded by phrase (see B3 in
// specs/features/phraseforge-export-import.md): a background job is
// generated against a specific (listID, position, phrase) triple, and by the
// time it runs the item at that position may have moved (deleted/reordered)
// or been replaced entirely — writing its result onto whatever now happens
// to sit at that bare position would silently corrupt the wrong item. If
// phrase no longer matches what's stored (or position no longer exists at
// all), this is ErrItemNotFound, not a silent no-op, so the caller (ai.go's
// writeback) fails the job instead of reporting false success.
func (s *Store) SetItemTranscription(ctx context.Context, listID int64, position int, phrase, transcription string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE vocabulary_items SET transcription = $1 WHERE list_id = $2 AND position = $3 AND phrase = $4`,
		nullIfEmpty(transcription), listID, position, phrase)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	return nil
}

// SetItemTranslationGuarded upserts one item's translation/notes at (listID,
// position), but only if the item still has the given phrase — same
// stale-target rationale as SetItemTranscription's own doc comment,
// extended to translation writeback per B3. Runs the phrase check and the
// upsert/delete inside one transaction (with FOR UPDATE on the phrase read)
// so a concurrent edit can never slip between the check and the write.
// Returns ErrItemNotFound if phrase no longer matches (or position is gone).
func (s *Store) SetItemTranslationGuarded(ctx context.Context, listID int64, position int, phrase, locale, translation, notes string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	var storedPhrase string
	err = tx.QueryRow(ctx, `SELECT phrase FROM vocabulary_items WHERE list_id = $1 AND position = $2 FOR UPDATE`, listID, position).Scan(&storedPhrase)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrItemNotFound
		}
		return err
	}
	if storedPhrase != phrase {
		return ErrItemNotFound
	}

	if translation == "" && notes == "" {
		if _, err := tx.Exec(ctx, `DELETE FROM vocabulary_item_translation WHERE list_id = $1 AND position = $2 AND locale = $3`, listID, position, locale); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vocabulary_item_translation (list_id, position, locale, translation, notes)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (list_id, position, locale) DO UPDATE SET
			translation = EXCLUDED.translation, notes = EXCLUDED.notes, updated_at = now()`,
		listID, position, locale, nullIfEmpty(translation), nullIfEmpty(notes)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteItem removes the item at position and shifts every later item (and
// its own per-locale translations, across every locale — not just the
// editing user's) down by one position. Shifting is done one row at a time
// in ascending order (never a single bulk UPDATE) so the target position is
// always free at that moment — a bulk update's row-processing order isn't
// guaranteed, and could transiently collide with the composite primary key.
// This keeps each item's translations attached to that item as it moves,
// rather than picking up whatever used to sit at the position below it.
// Returns ErrItemNotFound if position doesn't exist.
func (s *Store) DeleteItem(ctx context.Context, listID int64, position int) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	if err := s.DeleteItemTx(ctx, tx, listID, position); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteItemTx is DeleteItem's logic run against an already-open transaction
// tx (see Begin's doc comment) instead of this method beginning/committing
// its own — lets a caller (e.g. a wholesale item replacement) fold several
// items' deletes into one outer transaction. Returns ErrItemNotFound if
// position doesn't exist; the caller commits/rolls back tx itself.
func (s *Store) DeleteItemTx(ctx context.Context, tx pgx.Tx, listID int64, position int) error {
	tag, err := tx.Exec(ctx, `DELETE FROM vocabulary_items WHERE list_id = $1 AND position = $2`, listID, position)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM vocabulary_item_translation WHERE list_id = $1 AND position = $2`, listID, position); err != nil {
		return err
	}

	var maxPos int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(position), -1) FROM vocabulary_items WHERE list_id = $1`, listID).Scan(&maxPos); err != nil {
		return err
	}
	for p := position + 1; p <= maxPos; p++ {
		if _, err := tx.Exec(ctx, `UPDATE vocabulary_items SET position = $3 WHERE list_id = $1 AND position = $2`, listID, p, p-1); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE vocabulary_item_translation SET position = $3 WHERE list_id = $1 AND position = $2`, listID, p, p-1); err != nil {
			return err
		}
	}
	return nil
}

// Translations returns listID's translations for locale, keyed by position.
// A position with no row yet (not translated in this locale) simply isn't
// in the map — callers show it blank, not as an error.
func (s *Store) Translations(ctx context.Context, listID int64, locale string) (map[int]ItemTranslation, error) {
	rows, err := s.db.Query(ctx,
		`SELECT position, coalesce(translation, ''), coalesce(notes, '')
		   FROM vocabulary_item_translation WHERE list_id = $1 AND locale = $2`, listID, locale)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int]ItemTranslation{}
	for rows.Next() {
		var t ItemTranslation
		if err := rows.Scan(&t.Position, &t.Translation, &t.Notes); err != nil {
			return nil, err
		}
		out[t.Position] = t
	}
	return out, rows.Err()
}

// SetTranslations upserts listID's translations for locale. A translation
// for a position beyond validPositions (e.g. one that's since been deleted
// from vocabulary_items) is silently skipped — the FK would reject it
// anyway, and a caller passing a stale position shouldn't get a hard error.
func (s *Store) SetTranslations(ctx context.Context, listID int64, locale string, translations []ItemTranslation, validPositions int) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	if err := s.SetTranslationsTx(ctx, tx, listID, locale, translations, validPositions); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetTranslationsTx is SetTranslations' logic run against an already-open
// transaction tx (see Begin's doc comment) instead of this method
// beginning/committing its own — lets a caller fold several items'
// translation upserts into one outer transaction alongside their own
// delete/add steps (e.g. a wholesale item replacement).
func (s *Store) SetTranslationsTx(ctx context.Context, tx pgx.Tx, listID int64, locale string, translations []ItemTranslation, validPositions int) error {
	for _, t := range translations {
		if t.Position >= validPositions {
			continue
		}
		if t.Translation == "" && t.Notes == "" {
			if _, err := tx.Exec(ctx,
				`DELETE FROM vocabulary_item_translation WHERE list_id = $1 AND position = $2 AND locale = $3`,
				listID, t.Position, locale); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO vocabulary_item_translation (list_id, position, locale, translation, notes)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (list_id, position, locale) DO UPDATE SET
				translation = EXCLUDED.translation, notes = EXCLUDED.notes, updated_at = now()`,
			listID, t.Position, locale, nullIfEmpty(t.Translation), nullIfEmpty(t.Notes)); err != nil {
			return err
		}
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// pgxRows is the subset of pgx.Rows this package needs.
type pgxRows interface {
	Next() bool
	Scan(...any) error
	Close()
	Err() error
}
