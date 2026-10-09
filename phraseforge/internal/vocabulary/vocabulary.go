package vocabulary

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phraseforge/internal/pagination"
	"phraseforge/internal/textnorm"
)

// ErrItemNotFound is returned by UpdateItem/DeleteItem when position doesn't
// name an existing item in the list.
var ErrItemNotFound = errors.New("vocabulary item not found")

// ErrBlankPhrase is returned when an item's phrase is empty once trimmed.
var ErrBlankPhrase = errors.New("phrase must not be blank")

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
	PhraseID      int64 // the shared phrase this item links to; the id an export carries
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

// ListAllPage mirrors texts.Store.ListPage exactly, for vocabulary_lists —
// see that method's doc comment.
func (s *Store) ListAllPage(ctx context.Context, languages []string, all bool, tagIDs []int64, cursor *pagination.Cursor, limit int) (items []List, hasMore bool, err error) {
	if !all && len(languages) == 0 {
		return nil, false, nil
	}
	var cursorCreatedAt any
	var cursorID any
	if cursor != nil {
		cursorCreatedAt, cursorID = cursor.CreatedAt, cursor.ID
	}
	var rows pgxRows
	if all {
		rows, err = s.db.Query(ctx, `
			SELECT `+listCols+` FROM vocabulary_lists
			WHERE ($1::bigint[] IS NULL OR id = ANY($1))
			  AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3))
			ORDER BY created_at DESC, id DESC
			LIMIT $4`, tagIDs, cursorCreatedAt, cursorID, limit+1)
	} else {
		rows, err = s.db.Query(ctx, `
			SELECT `+listCols+` FROM vocabulary_lists
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

	var out []List
	for rows.Next() {
		var l List
		if err := scanList(rows, &l); err != nil {
			return nil, false, err
		}
		out = append(out, l)
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

// ListOffset is ListPage's numbered-page sibling (see specs/features/
// phraseforge-hash-navigation.md): same language/all and tagIDs semantics
// and the same (created_at, id) DESC order, but skips offset rows instead
// of seeking past a cursor, so any page can be fetched directly.
func (s *Store) ListOffset(ctx context.Context, languages []string, all bool, tagIDs []int64, offset, limit int) ([]List, error) {
	if !all && len(languages) == 0 {
		return nil, nil
	}
	var rows pgxRows
	var err error
	if all {
		rows, err = s.db.Query(ctx, `
			SELECT `+listCols+` FROM vocabulary_lists
			WHERE ($1::bigint[] IS NULL OR id = ANY($1))
			ORDER BY created_at DESC, id DESC
			LIMIT $2 OFFSET $3`, tagIDs, limit, offset)
	} else {
		rows, err = s.db.Query(ctx, `
			SELECT `+listCols+` FROM vocabulary_lists
			WHERE language = ANY($1)
			  AND ($2::bigint[] IS NULL OR id = ANY($2))
			ORDER BY created_at DESC, id DESC
			LIMIT $3 OFFSET $4`, languages, tagIDs, limit, offset)
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

// Count returns how many vocabulary lists match the same filters as ListOffset, so
// a page count can be computed.
func (s *Store) Count(ctx context.Context, languages []string, all bool, tagIDs []int64) (int, error) {
	if !all && len(languages) == 0 {
		return 0, nil
	}
	var n int
	var err error
	if all {
		err = s.db.QueryRow(ctx, `
			SELECT count(*) FROM vocabulary_lists
			WHERE ($1::bigint[] IS NULL OR id = ANY($1))`, tagIDs).Scan(&n)
	} else {
		err = s.db.QueryRow(ctx, `
			SELECT count(*) FROM vocabulary_lists
			WHERE language = ANY($1)
			  AND ($2::bigint[] IS NULL OR id = ANY($2))`, languages, tagIDs).Scan(&n)
	}
	return n, err
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
// instead finds the linked list via GetBySourceTextID and syncs
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

// CreateFromDialog mirrors CreateFromText — see that method's doc comment
// (dialog-vocabulary-models-generation extends generation to Dialogs).
func (s *Store) CreateFromDialog(ctx context.Context, userID int64, title, language, script string, sourceDialogID int64) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx,
		`INSERT INTO vocabulary_lists (user_id, title, language, script, source_dialog_id) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		userID, title, language, script, sourceDialogID).Scan(&id)
	return id, err
}

// GetBySourceDialogID mirrors GetBySourceTextID — see that method's doc
// comment.
func (s *Store) GetBySourceDialogID(ctx context.Context, dialogID int64) (id int64, found bool, err error) {
	err = s.db.QueryRow(ctx, `SELECT id FROM vocabulary_lists WHERE source_dialog_id = $1`, dialogID).Scan(&id)
	switch {
	case err == nil:
		return id, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return 0, false, nil
	default:
		return 0, false, err
	}
}

// UpdateMeta overwrites a list's own title/language/script. A phrase belongs to
// one language and script, so when either changes the list's items are
// re-linked to the phrases with the same text in the new one (see
// relinkItemsToListLanguage) in the same transaction — otherwise the list
// would link phrases of a language it no longer has, and could edit them for
// every list still in that language.
func (s *Store) UpdateMeta(ctx context.Context, id int64, title, language, script string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	var oldLanguage, oldScript string
	err = tx.QueryRow(ctx, `SELECT language, script FROM vocabulary_lists WHERE id = $1 FOR UPDATE`, id).Scan(&oldLanguage, &oldScript)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE vocabulary_lists SET title = $1, language = $2, script = $3, updated_at = now() WHERE id = $4`,
		title, language, script, id); err != nil {
		return err
	}
	if language != oldLanguage || script != oldScript {
		if err := relinkItemsToListLanguage(ctx, tx, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// relinkItemsToListLanguage points each of listID's items at the phrase with
// the same text in the list's (just changed) language and script, creating it
// where no list has it and copying the old phrase's translations (a
// translation the new phrase already has is kept). The old phrases other lists
// still link are left alone; those no list links any more are deleted here
// explicitly, because delete_orphan_phrases only fires on deleted links, not
// on re-pointed ones.
func relinkItemsToListLanguage(ctx context.Context, tx pgx.Tx, listID int64) error {
	var oldIDs []int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(DISTINCT phrase_id), '{}') FROM vocabulary_items WHERE list_id = $1`, listID).Scan(&oldIDs); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO phrases (language, script, phrase, grammar, transcription)
		SELECT DISTINCT l.language, l.script, op.phrase, op.grammar, op.transcription
		FROM vocabulary_items i JOIN vocabulary_lists l ON l.id = i.list_id JOIN phrases op ON op.id = i.phrase_id
		WHERE i.list_id = $1
		ON CONFLICT DO NOTHING`, listID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO phrase_translation (phrase_id, locale, translation, notes, updated_at)
		SELECT np.id, t.locale, t.translation, t.notes, t.updated_at
		FROM vocabulary_items i JOIN vocabulary_lists l ON l.id = i.list_id JOIN phrases op ON op.id = i.phrase_id
		JOIN phrases np ON np.language = l.language AND np.script = l.script
			AND np.phrase = op.phrase AND np.grammar = op.grammar AND np.transcription = op.transcription
		JOIN phrase_translation t ON t.phrase_id = op.id
		WHERE i.list_id = $1
		ON CONFLICT DO NOTHING`, listID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE vocabulary_items i SET phrase_id = np.id
		FROM vocabulary_lists l, phrases op, phrases np
		WHERE i.list_id = $1 AND l.id = i.list_id AND op.id = i.phrase_id
		  AND np.language = l.language AND np.script = l.script
		  AND np.phrase = op.phrase AND np.grammar = op.grammar AND np.transcription = op.transcription`, listID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		DELETE FROM phrases p WHERE p.id = ANY($1)
		  AND NOT EXISTS (SELECT 1 FROM vocabulary_items i WHERE i.phrase_id = p.id)`, oldIDs)
	return err
}

// Delete removes a list (and its items/translations, via ON DELETE CASCADE).
func (s *Store) Delete(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM vocabulary_lists WHERE id = $1`, id)
	return err
}

// Begin starts a transaction against this store's own pool — exported so a
// caller (e.g. server.syncVocabItemsTx's import, or generate's item sync) can
// run a whole SyncItemsTx for one list in a single transaction: a failure or
// client disconnect partway through must never leave a list half-changed (see
// specs/features/phraseforge-export-import.md's B2 fix). The caller commits or
// rolls back tx itself once every step it wants atomic with the others has
// succeeded.
func (s *Store) Begin(ctx context.Context) (pgx.Tx, error) {
	return s.db.Begin(ctx)
}

// Items returns listID's common fields, in position order.
func (s *Store) Items(ctx context.Context, listID int64) ([]Item, error) {
	rows, err := s.db.Query(ctx,
		`SELECT i.position, p.id, p.phrase, p.grammar, p.transcription
		   FROM vocabulary_items i JOIN phrases p ON p.id = i.phrase_id
		  WHERE i.list_id = $1 ORDER BY i.position`, listID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.Position, &it.PhraseID, &it.Phrase, &it.Grammar, &it.Transcription); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// AddItem appends a new item at the next free position and returns it. The
// item links to the shared phrase with these exact fields, which is created
// only if no list has it yet — so an already-translated phrase arrives with
// its translations.
func (s *Store) AddItem(ctx context.Context, listID int64, phrase, grammar, transcription string) (int, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	pos, err := s.AddItemTx(ctx, tx, listID, phrase, grammar, transcription)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return pos, nil
}

// AddItemTx is AddItem's logic run against an already-open transaction tx
// (see Begin's doc comment) instead of this store's own pool.
func (s *Store) AddItemTx(ctx context.Context, tx pgx.Tx, listID int64, phrase, grammar, transcription string) (int, error) {
	phraseID, err := findOrCreatePhrase(ctx, tx, listID, phrase, grammar, transcription)
	if err != nil {
		return 0, err
	}

	var pos int
	err = tx.QueryRow(ctx, `
		INSERT INTO vocabulary_items (list_id, position, phrase_id)
		VALUES ($1, coalesce((SELECT max(position) + 1 FROM vocabulary_items WHERE list_id = $1), 0), $2)
		RETURNING position`,
		listID, phraseID).Scan(&pos)
	return pos, err
}

// findOrCreatePhrase returns the id of the phrase with these fields in
// listID's language and script, creating it if no list has it yet. The fields
// are normalised first (see textnorm.Phrase), so text that only differs by
// surrounding whitespace or Unicode form is the same phrase.
func findOrCreatePhrase(ctx context.Context, tx pgx.Tx, listID int64, phrase, grammar, transcription string) (int64, error) {
	phrase, grammar, transcription = textnorm.Phrase(phrase), textnorm.Phrase(grammar), textnorm.Phrase(transcription)
	if phrase == "" {
		return 0, ErrBlankPhrase
	}
	// The no-op DO UPDATE makes RETURNING yield the id on conflict too, in one
	// race-free statement (DO NOTHING returns no row for an existing phrase).
	var phraseID int64
	err := tx.QueryRow(ctx, `
		INSERT INTO phrases (language, script, phrase, grammar, transcription)
		SELECT l.language, l.script, $2, $3, $4 FROM vocabulary_lists l WHERE l.id = $1
		ON CONFLICT (language, script, phrase, grammar, transcription) DO UPDATE SET phrase = EXCLUDED.phrase
		RETURNING id`,
		listID, phrase, grammar, transcription).Scan(&phraseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("vocabulary list %d not found", listID)
	}
	return phraseID, err
}

// UpdateItem changes the common fields of the item at position — for every
// list that links the same phrase, not only this one (phrases are shared). If
// another phrase already has exactly these fields the two are merged, see
// retargetPhrase. Returns ErrItemNotFound if position doesn't exist.
func (s *Store) UpdateItem(ctx context.Context, listID int64, position int, phrase, grammar, transcription string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	var phraseID int64
	err = tx.QueryRow(ctx, `SELECT phrase_id FROM vocabulary_items WHERE list_id = $1 AND position = $2 FOR UPDATE`, listID, position).Scan(&phraseID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrItemNotFound
		}
		return err
	}
	if _, err := retargetPhrase(ctx, tx, phraseID, phrase, grammar, transcription); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// retargetPhrase gives phrase id new phrase/grammar/transcription values for
// every list that links it. When another phrase (same language and script)
// already has exactly those values, id is merged into it instead: its
// translations are copied over (the surviving phrase's own non-blank
// translation wins, a blank one is filled), every list link is re-pointed,
// and id is deleted. Returns the id the phrase has afterwards: id itself, or
// the survivor's after a merge. The new values are normalised first, like
// findOrCreatePhrase's.
func retargetPhrase(ctx context.Context, tx pgx.Tx, id int64, phrase, grammar, transcription string) (int64, error) {
	phrase, grammar, transcription = textnorm.Phrase(phrase), textnorm.Phrase(grammar), textnorm.Phrase(transcription)
	if phrase == "" {
		return 0, ErrBlankPhrase
	}
	var language, script string
	if err := tx.QueryRow(ctx, `SELECT language, script FROM phrases WHERE id = $1 FOR UPDATE`, id).Scan(&language, &script); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrItemNotFound
		}
		return 0, err
	}

	var survivorID int64
	err := tx.QueryRow(ctx, `
		SELECT id FROM phrases
		WHERE language = $1 AND script = $2 AND phrase = $3 AND grammar = $4 AND transcription = $5 AND id <> $6
		FOR UPDATE`, language, script, phrase, grammar, transcription, id).Scan(&survivorID)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `UPDATE phrases SET phrase = $2, grammar = $3, transcription = $4 WHERE id = $1`, id, phrase, grammar, transcription)
		return id, err
	}
	if err != nil {
		return 0, err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO phrase_translation (phrase_id, locale, translation, notes, updated_at)
		SELECT $2, locale, translation, notes, updated_at FROM phrase_translation WHERE phrase_id = $1
		ON CONFLICT (phrase_id, locale) DO UPDATE SET
			translation = EXCLUDED.translation, notes = EXCLUDED.notes, updated_at = EXCLUDED.updated_at
			WHERE btrim(coalesce(phrase_translation.translation, '')) = ''`, id, survivorID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE vocabulary_items SET phrase_id = $2 WHERE phrase_id = $1`, id, survivorID); err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `DELETE FROM phrases WHERE id = $1`, id)
	return survivorID, err
}

// SetItemTranscriptionIfBlank writes transcription at (listID, position),
// but only if the item still has the given phrase (stale-target guard, same
// rationale as SetItemTranslationIfAbsent's own doc comment) AND its
// transcription is still blank (background-generate-title-transcription-
// translation's universal blank-check rule). Runs the phrase check, the
// blank check, and the write inside one transaction (FOR UPDATE on the
// read) so a concurrent edit can never slip in between. Returns
// ErrItemNotFound if phrase no longer matches (or position is gone);
// applied is false (not an error) when the transcription was already
// non-blank.
func (s *Store) SetItemTranscriptionIfBlank(ctx context.Context, listID int64, position int, phrase, transcription string) (applied bool, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	cur, err := lockItemPhraseIfStill(ctx, tx, listID, position, phrase)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(cur.transcription) != "" {
		return false, nil
	}
	if _, err := retargetPhrase(ctx, tx, cur.id, cur.phrase, cur.grammar, transcription); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// itemPhrase is the shared phrase row an item links to.
type itemPhrase struct {
	id                             int64
	phrase, grammar, transcription string
}

// lockItemPhraseIfStill is lockItemPhrase plus the stale-target guard (see B3
// in specs/features/phraseforge-export-import.md): a background job is made
// for a specific (listID, position, phrase) triple, and by the time it runs the
// item at that position may have moved, been deleted or been replaced, so
// writing its result to whatever sits there now would corrupt the wrong item.
// If the stored phrase is no longer phrase this is ErrItemNotFound, not a
// silent no-op, so the caller fails the job instead of reporting success.
func lockItemPhraseIfStill(ctx context.Context, tx pgx.Tx, listID int64, position int, phrase string) (itemPhrase, error) {
	cur, err := lockItemPhrase(ctx, tx, listID, position)
	if err != nil {
		return itemPhrase{}, err
	}
	if cur.phrase != phrase {
		return itemPhrase{}, ErrItemNotFound
	}
	return cur, nil
}

// lockItemPhrase locks the item at (listID, position) and the phrase it links
// to, and returns that phrase. Returns ErrItemNotFound if position doesn't
// exist. The phrase is resolved here, inside the caller's transaction, so a
// merge that re-pointed the item since a job was queued is followed rather
// than missed.
func lockItemPhrase(ctx context.Context, tx pgx.Tx, listID int64, position int) (itemPhrase, error) {
	var cur itemPhrase
	err := tx.QueryRow(ctx, `
		SELECT p.id, p.phrase, p.grammar, p.transcription
		FROM vocabulary_items i JOIN phrases p ON p.id = i.phrase_id
		WHERE i.list_id = $1 AND i.position = $2
		FOR UPDATE OF i, p`, listID, position).Scan(&cur.id, &cur.phrase, &cur.grammar, &cur.transcription)
	if errors.Is(err, pgx.ErrNoRows) {
		return itemPhrase{}, ErrItemNotFound
	}
	return cur, err
}

// SetItemGrammarIfBlank sets one item's grammar tags at (listID,
// position), with exactly SetItemTranscriptionIfBlank's guards: the item
// must still have phrase (else ErrItemNotFound), and an already non-blank
// grammar is left alone (applied false, not an error).
func (s *Store) SetItemGrammarIfBlank(ctx context.Context, listID int64, position int, phrase, grammar string) (applied bool, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	cur, err := lockItemPhraseIfStill(ctx, tx, listID, position, phrase)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(cur.grammar) != "" {
		return false, nil
	}
	if _, err := retargetPhrase(ctx, tx, cur.id, cur.phrase, grammar, cur.transcription); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// SetItemTranslationIfAbsent upserts one item's translation/notes at
// (listID, position), but only if the item still has the given phrase
// (stale-target guard, B3 fix) AND no translation already exists for this
// locale (background-generate-title-transcription-translation's universal
// blank-check rule — notes is passed through unconditionally by the caller,
// see main.go's itemTranslationWriteback adapter, so it is not itself part
// of the "absent" check). Runs the phrase check, the absence check, and the
// upsert/delete inside one transaction (with FOR UPDATE on both reads) so a
// concurrent edit can never slip between the checks and the write. Returns
// ErrItemNotFound if phrase no longer matches (or position is gone);
// applied is false (not an error) when a translation for this locale
// already existed.
func (s *Store) SetItemTranslationIfAbsent(ctx context.Context, listID int64, position int, phrase, locale, translation, notes string) (applied bool, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	cur, err := lockItemPhraseIfStill(ctx, tx, listID, position, phrase)
	if err != nil {
		return false, err
	}

	var existingTranslation string
	err = tx.QueryRow(ctx, `SELECT coalesce(translation, '') FROM phrase_translation WHERE phrase_id = $1 AND locale = $2 FOR UPDATE`, cur.id, locale).Scan(&existingTranslation)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if strings.TrimSpace(existingTranslation) != "" {
		return false, nil
	}

	if translation == "" && notes == "" {
		if _, err := tx.Exec(ctx, `DELETE FROM phrase_translation WHERE phrase_id = $1 AND locale = $2`, cur.id, locale); err != nil {
			return false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return true, nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO phrase_translation (phrase_id, locale, translation, notes)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (phrase_id, locale) DO UPDATE SET
			translation = EXCLUDED.translation, notes = EXCLUDED.notes, updated_at = now()`,
		cur.id, locale, nullIfEmpty(translation), nullIfEmpty(notes)); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// DeleteItem removes the item at position and shifts every later item in the
// list down by one position. Translations belong to the shared phrase, so
// they don't move; a phrase no list links to any more is deleted by the
// delete_orphan_phrases trigger. Shifting is done one row at a time in
// ascending order (never a single bulk UPDATE) so the target position is
// always free at that moment — a bulk update's row-processing order isn't
// guaranteed, and could transiently collide with the composite primary key.
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
// its own — lets a caller fold the delete into a larger transaction. Returns
// ErrItemNotFound if position doesn't exist; the caller commits/rolls back tx
// itself.
func (s *Store) DeleteItemTx(ctx context.Context, tx pgx.Tx, listID int64, position int) error {
	tag, err := tx.Exec(ctx, `DELETE FROM vocabulary_items WHERE list_id = $1 AND position = $2`, listID, position)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrItemNotFound
	}

	var maxPos int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(position), -1) FROM vocabulary_items WHERE list_id = $1`, listID).Scan(&maxPos); err != nil {
		return err
	}
	for p := position + 1; p <= maxPos; p++ {
		if _, err := tx.Exec(ctx, `UPDATE vocabulary_items SET position = $3 WHERE list_id = $1 AND position = $2`, listID, p, p-1); err != nil {
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
		`SELECT i.position, coalesce(t.translation, ''), coalesce(t.notes, '')
		   FROM vocabulary_items i JOIN phrase_translation t ON t.phrase_id = i.phrase_id
		  WHERE i.list_id = $1 AND t.locale = $2`, listID, locale)
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

// SetTranslations upserts the translations for locale of the phrases listID's
// items link to — so it is visible in every list that links the same phrase.
// A translation for a position beyond validPositions (e.g. one that's since
// been deleted from vocabulary_items) is silently skipped; a caller passing a
// stale position shouldn't get a hard error.
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
// translation upserts into one outer transaction.
func (s *Store) SetTranslationsTx(ctx context.Context, tx pgx.Tx, listID int64, locale string, translations []ItemTranslation, validPositions int) error {
	for _, t := range translations {
		if t.Position >= validPositions {
			continue
		}
		if t.Translation == "" && t.Notes == "" {
			if _, err := tx.Exec(ctx,
				`DELETE FROM phrase_translation WHERE locale = $3
				  AND phrase_id = (SELECT phrase_id FROM vocabulary_items WHERE list_id = $1 AND position = $2)`,
				listID, t.Position, locale); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO phrase_translation (phrase_id, locale, translation, notes)
			SELECT phrase_id, $3, $4, $5 FROM vocabulary_items WHERE list_id = $1 AND position = $2
			ON CONFLICT (phrase_id, locale) DO UPDATE SET
				translation = EXCLUDED.translation, notes = EXCLUDED.notes, updated_at = now()`,
			listID, t.Position, locale, nullIfEmpty(t.Translation), nullIfEmpty(t.Notes)); err != nil {
			return err
		}
	}
	return nil
}

// SyncTranslation is one locale's translation and notes for a SyncItem.
type SyncTranslation struct {
	Translation string
	Notes       string
}

// SyncItem is one line of the item sequence SyncItemsTx makes a list have.
type SyncItem struct {
	// PhraseID, when non-zero and linked by the list being synced, names the
	// shared phrase this line edits. Zero (or an id the list doesn't link)
	// makes the line a new one.
	PhraseID      int64
	Phrase        string
	Grammar       string
	Transcription string
	// Translations is partial: only the locales present are written, a
	// present locale with both fields blank clears it, an absent one is left
	// alone.
	Translations map[string]SyncTranslation
}

// syncMoveOffset moves a list's old links out of the way of the new ones; it
// only needs to exceed any position a list can reach.
const syncMoveOffset = 1 << 30

// SyncItemsTx makes listID's items exactly items, in that order (position i =
// items[i]), inside tx (see Begin's doc comment). The caller commits.
//
// Each line resolves to one shared phrase:
//   - a line whose PhraseID the list links edits that phrase for every list
//     that links it, as UpdateItem does — but only if allowEdit is true (an
//     import that changes the list's language passes false, since a phrase
//     can't change language) and no earlier line already resolved to it;
//   - any other line links the phrase with exactly its fields, creating it
//     only if no list has it.
//
// A phrase is "claimed" once a line has resolved to it, by either route, and
// no later line may edit a claimed phrase: otherwise a later edit would
// silently rewrite what an earlier line shows, or a merge would delete a
// phrase an earlier line points at. So the same PhraseID on two lines, or a
// PhraseID equal to a phrase an earlier line linked, makes the later line a
// new one. Each line's translations then overwrite the phrase's. Links the
// file doesn't mention are removed; a phrase no list links any more goes with
// its last link.
func (s *Store) SyncItemsTx(ctx context.Context, tx pgx.Tx, listID int64, items []SyncItem, allowEdit bool) error {
	linked, err := linkedPhraseIDs(ctx, tx, listID)
	if err != nil {
		return err
	}

	claimed := map[int64]bool{}
	resolved := make([]int64, len(items))
	for i, it := range items {
		mayEdit := allowEdit && linked[it.PhraseID] && !claimed[it.PhraseID]
		id, err := resolveSyncLine(ctx, tx, listID, it, mayEdit)
		if err != nil {
			return err
		}
		claimed[it.PhraseID] = true
		claimed[id] = true
		resolved[i] = id
		if err := writeSyncTranslations(ctx, tx, id, it.Translations); err != nil {
			return err
		}
	}
	return relinkItems(ctx, tx, listID, resolved)
}

// linkedPhraseIDs locks listID's links and returns the phrases they point to.
func linkedPhraseIDs(ctx context.Context, tx pgx.Tx, listID int64) (map[int64]bool, error) {
	rows, err := tx.Query(ctx, `SELECT phrase_id FROM vocabulary_items WHERE list_id = $1 FOR UPDATE`, listID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	linked := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		linked[id] = true
	}
	return linked, rows.Err()
}

// resolveSyncLine returns the phrase line should link to: it edits the phrase
// line names when mayEdit, otherwise finds or creates the phrase with the
// line's fields.
func resolveSyncLine(ctx context.Context, tx pgx.Tx, listID int64, line SyncItem, mayEdit bool) (int64, error) {
	if mayEdit {
		return retargetPhrase(ctx, tx, line.PhraseID, line.Phrase, line.Grammar, line.Transcription)
	}
	return findOrCreatePhrase(ctx, tx, listID, line.Phrase, line.Grammar, line.Transcription)
}

// writeSyncTranslations overwrites phraseID's translations for the locales in
// translations: a locale with blank fields is cleared, one not present is left
// alone.
func writeSyncTranslations(ctx context.Context, tx pgx.Tx, phraseID int64, translations map[string]SyncTranslation) error {
	for locale, t := range translations {
		var err error
		if t.Translation == "" && t.Notes == "" {
			_, err = tx.Exec(ctx, `DELETE FROM phrase_translation WHERE phrase_id = $1 AND locale = $2`, phraseID, locale)
		} else {
			_, err = tx.Exec(ctx, `
				INSERT INTO phrase_translation (phrase_id, locale, translation, notes)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (phrase_id, locale) DO UPDATE SET
					translation = EXCLUDED.translation, notes = EXCLUDED.notes, updated_at = now()`,
				phraseID, locale, nullIfEmpty(t.Translation), nullIfEmpty(t.Notes))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// relinkItems makes listID's links exactly phraseIDs, in order. The old links
// are moved out of the way first, the new ones added, then the moved ones
// deleted: deleting first would let delete_orphan_phrases remove a phrase that
// is about to be linked again, together with its translations. The bulk move
// is safe against the (list_id, position) primary key because every moved
// position is at least syncMoveOffset, which is above any position a list
// has, so no moved row can land on an existing one (DeleteItemTx shifts by
// one instead, where a bulk update could collide).
func relinkItems(ctx context.Context, tx pgx.Tx, listID int64, phraseIDs []int64) error {
	if _, err := tx.Exec(ctx, `UPDATE vocabulary_items SET position = position + $2 WHERE list_id = $1`, listID, syncMoveOffset); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vocabulary_items (list_id, position, phrase_id)
		SELECT $1, (t.ord - 1)::int, t.id FROM unnest($2::bigint[]) WITH ORDINALITY AS t(id, ord)`, listID, phraseIDs); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM vocabulary_items WHERE list_id = $1 AND position >= $2`, listID, syncMoveOffset)
	return err
}

// TranslatedLocales returns the locales in which the phrase at (listID,
// position) already has a non-blank translation — whichever list it was made
// through. Backfill uses it to skip translating a phrase that already is.
func (s *Store) TranslatedLocales(ctx context.Context, listID int64, position int) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, `
		SELECT t.locale FROM vocabulary_items i JOIN phrase_translation t ON t.phrase_id = i.phrase_id
		WHERE i.list_id = $1 AND i.position = $2 AND btrim(coalesce(t.translation, '')) <> ''`, listID, position)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var locale string
		if err := rows.Scan(&locale); err != nil {
			return nil, err
		}
		out[locale] = true
	}
	return out, rows.Err()
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
