use axum::{
    extract::{rejection::{JsonRejection, PathRejection, QueryRejection}, Path, Query, State},
    http::StatusCode,
    response::IntoResponse,
    routing::{get, post, put},
    Json, Router,
};

use crate::{
    dictionary::{
        dto::{
            CreateEntryRequest, DeleteEntryByTextQuery, EntryIdPath, LookupQuery,
            UpdateEntryByIdRequest, UpdateEntryByTextRequest,
        },
        service,
    },
    error::ApiError,
    state::AppState,
};

// ── Router ────────────────────────────────────────────────────────────────────

/// Build the `/api/v1/dictionary` sub-router.
///
/// All 7 endpoints per SPECS §7 table are registered here.
/// State type is `AppState` (inferred from handlers using `State<AppState>`).
///
/// R-5 route precedence: `/dictionaries` vs `/dictionaries/{phrase_id}/{translation_id}` —
/// these have different path-segment counts (1 vs 3) and are unambiguous to axum.
pub fn router() -> Router<AppState> {
    Router::new()
        // Entries (natural-key endpoints: body or query carry identity)
        .route(
            "/dictionaries",
            post(create_entry)
                .get(list_dictionary)
                .put(relink_by_text)
                .delete(delete_by_text),
        )
        // Surrogate-key endpoints: UUIDs in the path
        .route(
            "/dictionaries/{phrase_id}/{translation_id}",
            put(relink_by_ids).delete(delete_by_ids),
        )
        // Lookup (GET /translations)
        .route("/translations", get(lookup_translations))
}

// ── Handlers ──────────────────────────────────────────────────────────────────

/// POST /api/v1/dictionary/dictionaries → 201 EntryResponse
///
/// Creates a new dictionary dictionary (phrase → translation link).
/// Returns 201 on success. FR-API-001, DATA-DICT-008, DATA-DICT-009.
/// 400 validation_failed — body malformed or newtype invariant violated.
/// 400 unknown_reference — well-formed language-script or tag absent from reference table.
/// 409 conflict — this (phrase, translation) pair already exists.
/// 503 service_unavailable — Postgres unreachable.
async fn create_entry(
    State(state): State<AppState>,
    body: Result<Json<CreateEntryRequest>, JsonRejection>,
) -> Result<impl IntoResponse, ApiError> {
    let Json(req) = body.map_err(ApiError::from)?;
    let dictionary = service::create_entry(&state.pool, req).await?;
    Ok((StatusCode::CREATED, Json(dictionary)))
}

/// GET /api/v1/dictionary/translations?phrase=…&source=…&target=…
/// → 200 LookupResponse
///
/// Returns the Phrase (with transcription) + all Translations into the target language-script.
/// Empty `translations` array when the phrase exists but has no dictionaries in that language (E-03).
/// FR-API-002, DATA-DICT-008, DATA-DICT-009. 400/404/503.
async fn lookup_translations(
    State(state): State<AppState>,
    query: Result<Query<LookupQuery>, QueryRejection>,
) -> Result<impl IntoResponse, ApiError> {
    let Query(q) = query.map_err(ApiError::from)?;
    let response = service::lookup_translations(&state.pool, q).await?;
    Ok(Json(response))
}

/// GET /api/v1/dictionary/dictionaries → 200 EntryListResponse
///
/// Lists all dictionaries. Never a bare array (NFR-EXT-002). FR-API-005. 503.
async fn list_dictionary(
    State(state): State<AppState>,
) -> Result<impl IntoResponse, ApiError> {
    let response = service::list_dictionary(&state.pool).await?;
    Ok(Json(response))
}

/// PUT /api/v1/dictionary/dictionaries → 200 EntryResponse
///
/// Relinks an Dictionary to a new Translation by natural key (texts). FR-API-003.
/// The new Translation is created/reused in the same language-script as the old one (HITL-4).
/// Tags carry over to the relinked Dictionary (HITL-8, E-18).
/// 400/404/409/503.
async fn relink_by_text(
    State(state): State<AppState>,
    body: Result<Json<UpdateEntryByTextRequest>, JsonRejection>,
) -> Result<impl IntoResponse, ApiError> {
    let Json(req) = body.map_err(ApiError::from)?;
    let dictionary = service::relink_entry_by_natural_key(&state.pool, req).await?;
    Ok(Json(dictionary))
}

/// DELETE /api/v1/dictionary/dictionaries?phrase=…&source=…&target=…&translation=…
/// → 204 (no body)
///
/// Deletes an Dictionary by natural key. Orphaned Translation is also deleted (DATA-DICT-003).
/// FR-API-004. 400/404/503.
async fn delete_by_text(
    State(state): State<AppState>,
    query: Result<Query<DeleteEntryByTextQuery>, QueryRejection>,
) -> Result<impl IntoResponse, ApiError> {
    let Query(q) = query.map_err(ApiError::from)?;
    service::delete_dictionary_by_natural_key(&state.pool, q).await?;
    Ok(StatusCode::NO_CONTENT)
}

/// PUT /api/v1/dictionary/dictionaries/{phrase_id}/{translation_id} → 200 EntryResponse
///
/// Relinks an Dictionary to a new Translation by surrogate key (UUIDs in path). FR-API-008.
/// Language-script is inherited from the replaced Translation (HITL-4, E-09).
/// Tags carry over (HITL-8, E-18). 400/404/409/503.
async fn relink_by_ids(
    State(state): State<AppState>,
    path: Result<Path<EntryIdPath>, PathRejection>,
    body: Result<Json<UpdateEntryByIdRequest>, JsonRejection>,
) -> Result<impl IntoResponse, ApiError> {
    let Path(p) = path.map_err(ApiError::from)?;
    let Json(req) = body.map_err(ApiError::from)?;
    let dictionary = service::relink_entry_by_ids(&state.pool, p, req).await?;
    Ok(Json(dictionary))
}

/// DELETE /api/v1/dictionary/dictionaries/{phrase_id}/{translation_id} → 204 (no body)
///
/// Deletes an Dictionary by surrogate key. Orphaned Translation also deleted (DATA-DICT-003).
/// FR-API-009. 400/404/503.
async fn delete_by_ids(
    State(state): State<AppState>,
    path: Result<Path<EntryIdPath>, PathRejection>,
) -> Result<impl IntoResponse, ApiError> {
    let Path(p) = path.map_err(ApiError::from)?;
    service::delete_dictionary_by_ids(&state.pool, p).await?;
    Ok(StatusCode::NO_CONTENT)
}
