use sqlx::PgConnection;

use crate::{
    dictionary::{
        language::Language,
        model::{
            Dictionary, DictionaryJoinRow, Translation, TranslationId, TranslationText, TranslationWithTags,
            Phrase, PhraseId, PhraseText,
        },
        tag::GrammarTag,
        model::Transcription,
    },
    error::ApiError,
};

// ── T-3.1: find-or-create ──────────────────────────────────────────────────────

/// Insert or reuse a Phrase, atomically.
///
/// `ON CONFLICT DO UPDATE` is required (over `DO NOTHING`) so the statement
/// can `RETURNING *` the pre-existing row — `DO NOTHING` cannot do that. (T-3.1 w-f 1)
///
/// `COALESCE(EXCLUDED.transcription, phrase.transcription)` — supplied-value wins,
/// existing-value only as fallback when the incoming transcription is absent.
/// Argument order is deliberate (HITL-9, T-3.1 w-f 2): reversed args would silently
/// prevent a client from ever correcting a wrong transcription.
pub async fn upsert_phrase(
    conn: &mut PgConnection,
    language_script: &Language,
    text: &PhraseText,
    transcription: Option<&Transcription>,
) -> Result<Phrase, ApiError> {
    // DATA-DICT-001, DATA-DICT-009
    static SQL: &str = "\
        INSERT INTO phrase (language_script, text, transcription) \
        VALUES ($1, $2, $3) \
        ON CONFLICT (language_script, text) DO UPDATE \
          SET transcription = COALESCE(EXCLUDED.transcription, phrase.transcription) \
        RETURNING *";

    sqlx::query_as::<_, Phrase>(SQL)
        .bind(language_script.as_str())
        .bind(text.as_str())
        .bind(transcription.map(Transcription::as_str))
        .fetch_one(conn)
        .await
        .map_err(ApiError::from)
}

/// Insert or reuse a Translation, atomically.
/// `DO UPDATE SET text = EXCLUDED.text` is a no-op that enables `RETURNING *`
/// for the pre-existing row. DATA-DICT-001.
pub async fn upsert_translation(
    conn: &mut PgConnection,
    language_script: &Language,
    text: &TranslationText,
) -> Result<Translation, ApiError> {
    // DATA-DICT-001
    static SQL: &str = "\
        INSERT INTO translation (language_script, text) \
        VALUES ($1, $2) \
        ON CONFLICT (language_script, text) DO UPDATE \
          SET text = EXCLUDED.text \
        RETURNING *";

    sqlx::query_as::<_, Translation>(SQL)
        .bind(language_script.as_str())
        .bind(text.as_str())
        .fetch_one(conn)
        .await
        .map_err(ApiError::from)
}

// ── T-3.2: reads ──────────────────────────────────────────────────────────────

/// Look up a Phrase by its surrogate key (UUID).
/// Used by the relink service path to build the response after relinking by ID,
/// where only phrase_id is available (no natural key in scope). FR-API-008, FR-API-009.
pub async fn find_phrase_by_id(
    conn: &mut PgConnection,
    id: PhraseId,
) -> Result<Option<Phrase>, ApiError> {
    sqlx::query_as::<_, Phrase>(
        "SELECT * FROM phrase WHERE id = $1",
    )
    .bind(id)
    .fetch_optional(conn)
    .await
    .map_err(ApiError::from)
}

/// Look up a Phrase by its natural key (language_script, text). FR-API-002.
pub async fn find_phrase(
    conn: &mut PgConnection,
    language_script: &Language,
    text: &PhraseText,
) -> Result<Option<Phrase>, ApiError> {
    sqlx::query_as::<_, Phrase>(
        "SELECT * FROM phrase WHERE language_script = $1 AND text = $2",
    )
    .bind(language_script.as_str())
    .bind(text.as_str())
    .fetch_optional(conn)
    .await
    .map_err(ApiError::from)
}

/// Look up a Translation by its natural key (language_script, text).
pub async fn find_translation(
    conn: &mut PgConnection,
    language_script: &Language,
    text: &TranslationText,
) -> Result<Option<Translation>, ApiError> {
    sqlx::query_as::<_, Translation>(
        "SELECT * FROM translation WHERE language_script = $1 AND text = $2",
    )
    .bind(language_script.as_str())
    .bind(text.as_str())
    .fetch_optional(conn)
    .await
    .map_err(ApiError::from)
}

/// Look up a Translation by its surrogate key.
pub async fn find_translation_by_id(
    conn: &mut PgConnection,
    id: TranslationId,
) -> Result<Option<Translation>, ApiError> {
    sqlx::query_as::<_, Translation>(
        "SELECT * FROM translation WHERE id = $1",
    )
    .bind(id)
    .fetch_optional(conn)
    .await
    .map_err(ApiError::from)
}

/// Look up an Dictionary by its composite PK. FR-API-008, FR-API-009.
pub async fn find_dictionary(
    conn: &mut PgConnection,
    phrase_id: PhraseId,
    translation_id: TranslationId,
) -> Result<Option<Dictionary>, ApiError> {
    sqlx::query_as::<_, Dictionary>(
        "SELECT * FROM dictionary WHERE phrase_id = $1 AND translation_id = $2",
    )
    .bind(phrase_id)
    .bind(translation_id)
    .fetch_optional(conn)
    .await
    .map_err(ApiError::from)
}

/// List all translations (+ per-dictionary tags) for a phrase into a target language-script.
///
/// FR-API-002, DATA-DICT-008, DATA-DICT-009.
///
/// LEFT JOIN dictionary_tag (not INNER) + COALESCE + FILTER ensures an untagged Dictionary
/// returns `[]` rather than `[null]` or being dropped from the result. (T-3.2 w-f 2)
///
/// `Vec<GrammarTag>` is decoded from the Postgres `text[]` produced by
/// `COALESCE(array_agg(…) FILTER (…), '{}')`. (T-3.2 w-f 3)
pub async fn list_translations_with_tags_for_word(
    conn: &mut PgConnection,
    phrase_id: PhraseId,
    target_language_script: &Language,
) -> Result<Vec<TranslationWithTags>, ApiError> {
    // DATA-DICT-008, FR-API-002
    static SQL: &str = "\
        SELECT \
            t.id, \
            t.language_script, \
            t.text, \
            COALESCE(array_agg(et.tag_code) FILTER (WHERE et.tag_code IS NOT NULL), \
                     ARRAY[]::text[]) AS tags \
        FROM dictionary e \
        JOIN  translation t  ON t.id = e.translation_id \
        LEFT JOIN dictionary_tag et \
            ON et.phrase_id = e.phrase_id AND et.translation_id = e.translation_id \
        WHERE e.phrase_id = $1 AND t.language_script = $2 \
        GROUP BY t.id, t.language_script, t.text";

    sqlx::query_as::<_, TranslationWithTags>(SQL)
        .bind(phrase_id)
        .bind(target_language_script.as_str())
        .fetch_all(conn)
        .await
        .map_err(ApiError::from)
}

/// List all dictionaries in the dictionary (phrase + translation + tags). FR-API-005.
///
/// Single query with GROUP BY — no N+1 per dictionary (FR-API-005 AC).
/// LEFT JOIN + COALESCE + FILTER: same correctness argument as list_translations_with_tags.
pub async fn list_dictionary(
    conn: &mut PgConnection,
) -> Result<Vec<DictionaryJoinRow>, ApiError> {
    // FR-API-005, DATA-DICT-008, DATA-DICT-009
    static SQL: &str = "\
        SELECT \
            w.id   AS phrase_id, \
            w.language_script AS word_language_script, \
            w.text AS phrase_text, \
            w.transcription   AS word_transcription, \
            t.id   AS translation_id, \
            t.language_script AS translation_language_script, \
            t.text AS translation_text, \
            COALESCE(array_agg(et.tag_code) FILTER (WHERE et.tag_code IS NOT NULL), \
                     ARRAY[]::text[]) AS tags \
        FROM dictionary e \
        JOIN phrase        w  ON w.id = e.phrase_id \
        JOIN translation t  ON t.id = e.translation_id \
        LEFT JOIN dictionary_tag et \
            ON et.phrase_id = e.phrase_id AND et.translation_id = e.translation_id \
        GROUP BY \
            w.id, w.language_script, w.text, w.transcription, \
            t.id, t.language_script, t.text";

    sqlx::query_as::<_, DictionaryJoinRow>(SQL)
        .fetch_all(conn)
        .await
        .map_err(ApiError::from)
}

/// Fetch all tags attached to a specific dictionary. Used by the relink path (T-4.5)
/// to capture the tag set BEFORE deleting the old dictionary row (cascade would drop them).
pub async fn list_tags_for_entry(
    conn: &mut PgConnection,
    phrase_id: PhraseId,
    translation_id: TranslationId,
) -> Result<Vec<GrammarTag>, ApiError> {
    // DATA-DICT-008, FR-API-003, FR-API-008
    sqlx::query_scalar::<_, GrammarTag>(
        "SELECT tag_code FROM dictionary_tag WHERE phrase_id = $1 AND translation_id = $2",
    )
    .bind(phrase_id)
    .bind(translation_id)
    .fetch_all(conn)
    .await
    .map_err(ApiError::from)
}

/// Check which of the supplied language-script codes exist in the reference table.
/// Returns the subset that are present (used for reference validation, DATA-DICT-004).
/// One round trip regardless of how many codes are supplied. FR-API-001 criteria 4/5.
pub async fn language_scripts_present(
    conn: &mut PgConnection,
    codes: &[Language],
) -> Result<Vec<Language>, ApiError> {
    if codes.is_empty() {
        return Ok(vec![]);
    }
    let codes_str: Vec<String> = codes.iter().map(|c| c.as_str().to_owned()).collect();
    sqlx::query_scalar::<_, Language>(
        "SELECT code FROM language WHERE code = ANY($1)",
    )
    .bind(codes_str)
    .fetch_all(conn)
    .await
    .map_err(ApiError::from)
}

/// Check which of the supplied tag codes exist in the reference table.
/// Returns the subset that are present. DATA-DICT-007/008, FR-API-001 criterion 5.
pub async fn tags_present(
    conn: &mut PgConnection,
    tags: &[GrammarTag],
) -> Result<Vec<GrammarTag>, ApiError> {
    if tags.is_empty() {
        return Ok(vec![]);
    }
    let tag_strings: Vec<String> = tags.iter().map(|t| t.as_str().to_owned()).collect();
    sqlx::query_scalar::<_, GrammarTag>(
        "SELECT code FROM grammar_tag WHERE code = ANY($1)",
    )
    .bind(tag_strings)
    .fetch_all(conn)
    .await
    .map_err(ApiError::from)
}

// ── T-3.3: writes and orphan sweep ────────────────────────────────────────────

/// Insert an Dictionary row. Callers handle the 23505 unique_violation → Conflict mapping.
pub async fn insert_dictionary(
    conn: &mut PgConnection,
    phrase_id: PhraseId,
    translation_id: TranslationId,
) -> Result<Dictionary, ApiError> {
    // DATA-DICT-002, FR-API-001
    sqlx::query_as::<_, Dictionary>(
        "INSERT INTO dictionary (phrase_id, translation_id) VALUES ($1, $2) RETURNING *",
    )
    .bind(phrase_id)
    .bind(translation_id)
    .fetch_one(conn)
    .await
    .map_err(ApiError::from)
}

/// Insert all supplied tags for an dictionary in a single statement.
///
/// Caller MUST de-duplicate the slice before calling so `dictionary_tag_pkey` cannot fire.
/// (FR-API-001: "duplicates in the request are de-duplicated first".)
/// An empty slice is a no-op. DATA-DICT-008.
pub async fn insert_dictionary_tags(
    conn: &mut PgConnection,
    phrase_id: PhraseId,
    translation_id: TranslationId,
    tags: &[GrammarTag],
) -> Result<(), ApiError> {
    if tags.is_empty() {
        return Ok(());
    }

    // DATA-DICT-008
    // UNNEST($3::text[]) expands the array into rows, one INSERT per array element.
    // This is a single round-trip regardless of the number of tags.
    let tag_strings: Vec<String> = tags.iter().map(|t| t.as_str().to_owned()).collect();

    sqlx::query(
        "INSERT INTO dictionary_tag (phrase_id, translation_id, tag_code) \
         SELECT $1, $2, unnest($3::text[])",
    )
    .bind(phrase_id)
    .bind(translation_id)
    .bind(tag_strings)
    .execute(conn)
    .await
    .map(|_| ())
    .map_err(ApiError::from)
}

/// Delete an Dictionary row. Returns the number of rows affected (0 or 1).
/// The ON DELETE CASCADE on dictionary_tag means the dictionary's tags are removed automatically;
/// no explicit tag cleanup is needed here (DATA-DICT-008 + T-3.3 AC).
pub async fn delete_dictionary(
    conn: &mut PgConnection,
    phrase_id: PhraseId,
    translation_id: TranslationId,
) -> Result<u64, ApiError> {
    // FR-API-004, FR-API-009, DATA-DICT-008 (cascade)
    sqlx::query(
        "DELETE FROM dictionary WHERE phrase_id = $1 AND translation_id = $2",
    )
    .bind(phrase_id)
    .bind(translation_id)
    .execute(conn)
    .await
    .map(|r| r.rows_affected())
    .map_err(ApiError::from)
}

/// Lock a Translation row FOR UPDATE to serialize against concurrent linkers.
///
/// This ensures the NOT EXISTS check in `delete_translation_if_orphan` is not
/// subject to a race where a concurrent INSERT INTO dictionary completes between the
/// check and the DELETE. (SPECS §8, DATA-DICT-003.)
pub async fn lock_translation(
    conn: &mut PgConnection,
    id: TranslationId,
) -> Result<Option<Translation>, ApiError> {
    // DATA-DICT-003
    sqlx::query_as::<_, Translation>(
        "SELECT * FROM translation WHERE id = $1 FOR UPDATE",
    )
    .bind(id)
    .fetch_optional(conn)
    .await
    .map_err(ApiError::from)
}

/// Delete the Translation identified by `id` if and only if no Dictionary references it.
///
/// Returns `true` when the row was deleted (zero referrers), `false` when it was
/// retained (≥1 referrer). DATA-DICT-003.
///
/// Must be called after `lock_translation` within the same transaction.
/// The `ON DELETE RESTRICT` FK on dictionary.translation_id is the final backstop.
pub async fn delete_translation_if_orphan(
    conn: &mut PgConnection,
    id: TranslationId,
) -> Result<bool, ApiError> {
    // DATA-DICT-003
    let result = sqlx::query(
        "DELETE FROM translation WHERE id = $1 \
         AND NOT EXISTS (SELECT 1 FROM dictionary WHERE translation_id = $1)",
    )
    .bind(id)
    .execute(conn)
    .await
    .map_err(ApiError::from)?;

    Ok(result.rows_affected() == 1)
}
