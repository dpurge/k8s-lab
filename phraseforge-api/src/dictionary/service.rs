use std::collections::HashSet;

use sqlx::{PgConnection, PgPool, Postgres, Transaction};

use crate::{
    dictionary::{
        dto::{
            CreateEntryRequest, DeleteEntryByTextQuery, EntryIdPath, EntryListResponse,
            EntryResponse, FieldError, LookupQuery, LookupResponse, TranslationResponse,
            TranslationWithTagsResponse, UpdateEntryByIdRequest, UpdateEntryByTextRequest,
            PhraseResponse,
        },
        language::Language,
        model::{TranslationId, TranslationText, PhraseId},
        repository,
        tag::GrammarTag,
    },
    error::ApiError,
};

// ── Reference validation helper ───────────────────────────────────────────────

/// Validate that all supplied language-scripts and tags exist in their reference tables.
///
/// Called inside the same transaction as the writes — before any write — so:
/// 1. An `unknown_reference` error can name exactly which field is at fault.
/// 2. No row is written before validation fails (FR-API-001 criteria 4/5, T-4.1 watch-for).
///
/// Two round trips: one for language-scripts, one for tags.
/// DATA-DICT-004, DATA-DICT-007, FR-API-001 criteria 4/5.
async fn validate_references(
    conn: &mut PgConnection,
    source: &Language,
    target: &Language,
    tags: &[GrammarTag],
) -> Result<(), ApiError> {
    // Deduplicate codes before querying to avoid duplicate dictionaries in the query.
    let mut ls_codes = vec![source.clone(), target.clone()];
    ls_codes.dedup(); // source == target is common; dedup removes consecutive duplicates

    let present_ls = repository::language_scripts_present(conn, &ls_codes).await?;

    let mut errors: Vec<FieldError> = Vec::new();

    if !present_ls.contains(source) {
        errors.push(FieldError {
            field: "source".to_string(),
            message: format!(
                "'{}' is not in the language-script reference table",
                source.as_str()
            ),
        });
    }
    if !present_ls.contains(target) {
        errors.push(FieldError {
            field: "target".to_string(),
            message: format!(
                "'{}' is not in the language-script reference table",
                target.as_str()
            ),
        });
    }

    if !tags.is_empty() {
        let present_tags = repository::tags_present(conn, tags).await?;
        for tag in tags {
            if !present_tags.contains(tag) {
                errors.push(FieldError {
                    field: "tags".to_string(),
                    message: format!(
                        "'{}' is not in the grammatical-tag reference table",
                        tag.as_str()
                    ),
                });
            }
        }
    }

    if !errors.is_empty() {
        return Err(ApiError::UnknownReference(errors));
    }

    Ok(())
}

// ── Shared delete helper ──────────────────────────────────────────────────────

/// Core delete logic: delete the Dictionary then sweep an orphaned Translation.
///
/// Returns 404 when the dictionary row does not exist (rows_affected == 0).
/// The ON DELETE CASCADE on dictionary_tag means the dictionary's tags disappear automatically;
/// no explicit tag cleanup is needed (DATA-DICT-008, T-3.3 AC).
///
/// DATA-DICT-003, FR-API-004, FR-API-009.
async fn do_delete(
    conn: &mut PgConnection,
    phrase_id: PhraseId,
    translation_id: TranslationId,
) -> Result<(), ApiError> {
    let rows = repository::delete_dictionary(conn, phrase_id, translation_id).await?;
    if rows == 0 {
        return Err(ApiError::NotFound);
    }

    // Orphan sweep (DATA-DICT-003): lock the Translation, then delete if no referrers remain.
    // Lock must precede the NOT EXISTS check to serialize against concurrent linkers (SPECS §8).
    // translation_id is guaranteed to exist here: dictionary.translation_id carries a foreign key with
    // ON DELETE RESTRICT, so None can only occur if that invariant is violated.
    let _locked = repository::lock_translation(conn, translation_id).await?;
    repository::delete_translation_if_orphan(conn, translation_id).await?;

    Ok(())
}

// ── Shared relink helper ──────────────────────────────────────────────────────

/// Core relink logic executed within an already-open transaction.
///
/// The new Translation inherits the old Translation's language-script (HITL-4, E-09).
/// Tags are captured BEFORE the old Dictionary row is deleted — the ON DELETE CASCADE would
/// otherwise drop them silently, losing user data with no error. (T-4.5 watch-for)
/// Tag carry-over is HITL-8/E-18.
///
/// Identical target → no-op 200 (SPECS §7).
/// Already-linked target → 409 via 23505 unique_violation (E-08, HITL-3).
///
/// Callers (public dictionary points) begin and commit the transaction; they resolve
/// phrase_id and old_translation_id from either natural keys or surrogate IDs before
/// calling this function.
async fn do_relink_in_tx(
    tx: &mut Transaction<'_, Postgres>,
    phrase_id: PhraseId,
    old_translation_id: TranslationId,
    new_translation_text: TranslationText,
) -> Result<EntryResponse, ApiError> {
    // Resolve old Translation to obtain its language-script (inherited by new Translation).
    let old_translation = repository::find_translation_by_id(&mut **tx, old_translation_id)
        .await?
        .ok_or(ApiError::NotFound)?;

    // Verify the Dictionary linking phrase ↔ old_translation exists.
    repository::find_dictionary(&mut **tx, phrase_id, old_translation_id)
        .await?
        .ok_or(ApiError::NotFound)?;

    // *** READ TAGS BEFORE DELETING OLD ENTRY (T-4.5 watch-for) ***
    // The DELETE below cascades dictionary_tag rows. Reading after the delete yields
    // an empty set and silently loses user-supplied tags. Must happen here.
    let saved_tags =
        repository::list_tags_for_entry(&mut **tx, phrase_id, old_translation_id).await?;

    // Find-or-create the new Translation in the SAME language-script as the old one.
    // HITL-4 / E-09: relinking never changes the target language-script.
    let new_translation = repository::upsert_translation(
        &mut **tx,
        &old_translation.language_script,
        &new_translation_text,
    )
    .await?;

    // No-op path: identical Translation (same ID).
    // Return the current Dictionary state unchanged; SPECS §7 "no-op returning 200".
    if new_translation.id == old_translation_id {
        let phrase = repository::find_phrase_by_id(&mut **tx, phrase_id)
            .await?
            .ok_or(ApiError::NotFound)?;
        // Tags read from the database via list_tags_for_entry cannot contain duplicates due to
        // dictionary_tag_pkey, so saved_tags and any deduped version of it are identical here.
        return Ok(EntryResponse {
            phrase: PhraseResponse::from(phrase),
            translation: TranslationResponse {
                id: old_translation.id,
                language_script: old_translation.language_script,
                text: old_translation.text,
            },
            tags: saved_tags,
        });
    }

    // Delete old Dictionary (cascades its dictionary_tag rows — tags already captured above).
    repository::delete_dictionary(&mut **tx, phrase_id, old_translation_id).await?;

    // Insert new Dictionary — 23505 (unique_violation) → Conflict (E-08):
    // the Phrase is already linked to the target Translation.
    repository::insert_dictionary(&mut **tx, phrase_id, new_translation.id).await?;

    // Re-insert saved tags against new Dictionary (E-18, HITL-8: tags carry over).
    // Tags from DB are unique by PK; dedup defensively to avoid dictionary_tag_pkey violation.
    let deduped_tags: Vec<GrammarTag> = {
        let mut seen: HashSet<GrammarTag> = HashSet::new();
        saved_tags.into_iter().filter(|t| seen.insert(t.clone())).collect()
    };
    repository::insert_dictionary_tags(&mut **tx, phrase_id, new_translation.id, &deduped_tags).await?;

    // Orphan sweep on old Translation (DATA-DICT-003).
    repository::lock_translation(&mut **tx, old_translation_id).await?;
    repository::delete_translation_if_orphan(&mut **tx, old_translation_id).await?;

    // Fetch the Phrase for the response (unchanged by relink).
    let phrase = repository::find_phrase_by_id(&mut **tx, phrase_id)
        .await?
        .ok_or(ApiError::NotFound)?;

    Ok(EntryResponse {
        phrase: PhraseResponse::from(phrase),
        translation: TranslationResponse {
            id: new_translation.id,
            language_script: new_translation.language_script,
            text: new_translation.text,
        },
        // Tags read from the database via list_tags_for_entry cannot contain duplicates due to
        // dictionary_tag_pkey, so deduped_tags and saved_tags are identical here.
        tags: deduped_tags,
    })
}

// ── T-4.1: create_entry ───────────────────────────────────────────────────────

/// Create a new dictionary Dictionary (Phrase → Translation link). FR-API-001.
///
/// Flow: validate references → find-or-create Phrase (with transcription) →
/// find-or-create Translation → INSERT dictionary (23505 → 409) →
/// INSERT dictionary_tags → COMMIT.
///
/// Reference validation runs BEFORE any write so the error message names the
/// offending field and no partial rows remain on failure (E-13, E-14).
pub async fn create_entry(
    pool: &PgPool,
    req: CreateEntryRequest,
) -> Result<EntryResponse, ApiError> {
    // De-duplicate request tags before any DB interaction (dictionary_tag_pkey must not fire).
    let deduped_tags: Vec<GrammarTag> = {
        let mut seen: HashSet<GrammarTag> = HashSet::new();
        req.tags.into_iter().filter(|t| seen.insert(t.clone())).collect()
    };

    let mut tx = pool.begin().await.map_err(ApiError::from)?;

    // Layer 2 reference validation (layer 1 = newtype format already done at deserialization).
    validate_references(
        &mut *tx,
        &req.source,
        &req.target,
        &deduped_tags,
    )
    .await?;

    // Find-or-create Phrase; transcription overwrites stored value when supplied (HITL-9, E-19).
    let phrase = repository::upsert_phrase(
        &mut *tx,
        &req.source,
        &req.phrase,
        req.transcription.as_ref(),
    )
    .await?;

    // Find-or-create Translation (DATA-DICT-001).
    let translation =
        repository::upsert_translation(&mut *tx, &req.target, &req.translation)
            .await?;

    // Insert Dictionary — 23505 (unique_violation) → Conflict (E-01, DATA-DICT-002).
    repository::insert_dictionary(&mut *tx, phrase.id, translation.id).await?;

    // Insert grammatical tags for this Dictionary (DATA-DICT-008, E-16).
    repository::insert_dictionary_tags(&mut *tx, phrase.id, translation.id, &deduped_tags).await?;

    tx.commit().await.map_err(ApiError::from)?;

    // Build response from in-memory data — no extra SELECT needed (E-17, E-16).
    Ok(EntryResponse {
        phrase: PhraseResponse::from(phrase),
        translation: TranslationResponse {
            id: translation.id,
            language_script: translation.language_script,
            text: translation.text,
        },
        tags: deduped_tags,
    })
}

// ── T-4.2: lookup_translations ────────────────────────────────────────────────

/// Look up all Translations of a Phrase into a target language-script. FR-API-002.
///
/// Phrase miss → 404 (E-04). Phrase hit → 200 with possibly-empty translations list (E-03).
/// Transcription is present in both the populated and the empty-list case (E-17).
/// Each Translation carries its own tag list (E-15, DATA-DICT-008).
pub async fn lookup_translations(
    pool: &PgPool,
    query: LookupQuery,
) -> Result<LookupResponse, ApiError> {
    let mut conn = pool.acquire().await.map_err(ApiError::from)?;

    // Find the Phrase by natural key; absent → 404 (E-04, FR-API-002 criterion 1).
    let phrase =
        repository::find_phrase(&mut *conn, &query.source, &query.phrase)
            .await?
            .ok_or(ApiError::NotFound)?;

    // Possibly-empty list (E-03, FR-API-002 criterion 3).
    // LEFT JOIN + COALESCE in the query guarantees an untagged Dictionary appears with [] (E-15).
    let translations = repository::list_translations_with_tags_for_word(
        &mut *conn,
        phrase.id,
        &query.target,
    )
    .await?;

    Ok(LookupResponse {
        phrase: PhraseResponse::from(phrase),
        target: query.target,
        translations: translations.into_iter().map(TranslationWithTagsResponse::from).collect(),
    })
}

// ── T-4.3: list_dictionary ───────────────────────────────────────────────────────

/// List all dictionary dictionaries. FR-API-005.
///
/// Empty database returns `{"dictionaries":[],"count":0}` — never a bare array (NFR-EXT-002).
/// One query, no N+1 per dictionary (FR-API-005 AC). Each item carries phrase text,
/// transcription (omitted when null), both language-scripts, translation text and tags.
pub async fn list_dictionary(pool: &PgPool) -> Result<EntryListResponse, ApiError> {
    let mut conn = pool.acquire().await.map_err(ApiError::from)?;
    let rows = repository::list_dictionary(&mut *conn).await?;
    let count = rows.len() as u64;
    Ok(EntryListResponse {
        dictionaries: rows.into_iter().map(EntryResponse::from).collect(),
        count,
    })
}

// ── T-4.4: delete (two public dictionary points, one shared core) ─────────────────

/// Delete an Dictionary by its natural key. FR-API-004.
///
/// Phrase or Translation absent → 404. Dictionary absent → 404.
/// Translation orphaned after delete → also deleted (DATA-DICT-003, E-05).
/// Phrase is never collected (E-06; FR-API-002 criterion 3 requires it to outlive its dictionaries).
pub async fn delete_dictionary_by_natural_key(
    pool: &PgPool,
    query: DeleteEntryByTextQuery,
) -> Result<(), ApiError> {
    let mut tx = pool.begin().await.map_err(ApiError::from)?;

    let phrase =
        repository::find_phrase(&mut *tx, &query.source, &query.phrase)
            .await?
            .ok_or(ApiError::NotFound)?;

    let translation =
        repository::find_translation(&mut *tx, &query.target, &query.translation)
            .await?
            .ok_or(ApiError::NotFound)?;

    do_delete(&mut *tx, phrase.id, translation.id).await?;

    tx.commit().await.map_err(ApiError::from)?;
    Ok(())
}

/// Delete an Dictionary by its surrogate key (phrase_id, translation_id). FR-API-009.
///
/// Dictionary absent → 404. Orphan sweep same as the natural-key variant (DATA-DICT-003).
pub async fn delete_dictionary_by_ids(pool: &PgPool, path: EntryIdPath) -> Result<(), ApiError> {
    let mut tx = pool.begin().await.map_err(ApiError::from)?;
    do_delete(&mut *tx, path.phrase_id, path.translation_id).await?;
    tx.commit().await.map_err(ApiError::from)?;
    Ok(())
}

// ── T-4.5: relink (two public dictionary points, one shared core) ─────────────────

/// Relink an Dictionary to a new Translation by natural key. FR-API-003.
///
/// Resolves phrase and current_translation from texts, then delegates to `do_relink_in_tx`
/// within the same transaction (consistent read + write against concurrent changes).
pub async fn relink_entry_by_natural_key(
    pool: &PgPool,
    req: UpdateEntryByTextRequest,
) -> Result<EntryResponse, ApiError> {
    let mut tx = pool.begin().await.map_err(ApiError::from)?;

    // Resolve phrase by natural key (inside the transaction for consistency).
    let phrase =
        repository::find_phrase(&mut *tx, &req.source, &req.phrase)
            .await?
            .ok_or(ApiError::NotFound)?;

    // Resolve current translation by natural key.
    let translation =
        repository::find_translation(&mut *tx, &req.target, &req.current_translation)
            .await?
            .ok_or(ApiError::NotFound)?;

    let response =
        do_relink_in_tx(&mut tx, phrase.id, translation.id, req.new_translation).await?;

    tx.commit().await.map_err(ApiError::from)?;
    Ok(response)
}

/// Relink an Dictionary to a new Translation by surrogate key (phrase_id, translation_id). FR-API-008.
///
/// IDs are used directly; delegates to `do_relink_in_tx`.
pub async fn relink_entry_by_ids(
    pool: &PgPool,
    path: EntryIdPath,
    req: UpdateEntryByIdRequest,
) -> Result<EntryResponse, ApiError> {
    let mut tx = pool.begin().await.map_err(ApiError::from)?;
    let response =
        do_relink_in_tx(&mut tx, path.phrase_id, path.translation_id, req.new_translation).await?;
    tx.commit().await.map_err(ApiError::from)?;
    Ok(response)
}
