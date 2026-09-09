use serde::{Deserialize, Serialize};

use crate::dictionary::{
    language::Language,
    model::{
        DictionaryJoinRow, TranslationId, TranslationText, TranslationWithTags, Transcription,
        Phrase, PhraseId, PhraseText,
    },
    tag::GrammarTag,
};

// ── Shared response fragments ─────────────────────────────────────────────────

/// Phrase fragment included in every response that surfaces a phrase. DATA-DICT-009.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PhraseResponse {
    pub id: PhraseId,
    pub language_script: Language,
    pub text: PhraseText,
    /// Omitted from JSON when absent — never `null` (DATA-DICT-009 second criterion).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub transcription: Option<Transcription>,
}

impl From<Phrase> for PhraseResponse {
    fn from(w: Phrase) -> Self {
        PhraseResponse {
            id: w.id,
            language_script: w.language_script,
            text: w.text,
            transcription: w.transcription,
        }
    }
}

/// Translation fragment used in dictionary responses.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TranslationResponse {
    pub id: TranslationId,
    pub language_script: Language,
    pub text: TranslationText,
}

/// Translation + its dictionary-level grammatical tags, used in lookup responses. DATA-DICT-008.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TranslationWithTagsResponse {
    pub id: TranslationId,
    pub language_script: Language,
    pub text: TranslationText,
    /// Empty slice serialises as `[]`, never `null` (DATA-DICT-008 second criterion).
    pub tags: Vec<GrammarTag>,
}

impl From<TranslationWithTags> for TranslationWithTagsResponse {
    fn from(t: TranslationWithTags) -> Self {
        TranslationWithTagsResponse {
            id: t.id,
            language_script: t.language_script,
            text: t.text,
            tags: t.tags,
        }
    }
}

/// Full dictionary response: phrase + translation + tags. DATA-DICT-008.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EntryResponse {
    pub phrase: PhraseResponse,
    pub translation: TranslationResponse,
    /// Empty slice serialises as `[]`, never `null` (DATA-DICT-008 second criterion).
    pub tags: Vec<GrammarTag>,
}

impl From<DictionaryJoinRow> for EntryResponse {
    fn from(row: DictionaryJoinRow) -> Self {
        EntryResponse {
            phrase: PhraseResponse {
                id: row.phrase_id,
                language_script: row.word_language_script,
                text: row.phrase_text,
                transcription: row.word_transcription,
            },
            translation: TranslationResponse {
                id: row.translation_id,
                language_script: row.translation_language_script,
                text: row.translation_text,
            },
            tags: row.tags,
        }
    }
}

// ── FR-API-001: create ────────────────────────────────────────────────────────

/// Body for `POST /api/v1/dictionary/dictionaries`. FR-API-001.
#[derive(Debug, Clone, Deserialize)]
pub struct CreateEntryRequest {
    pub phrase: PhraseText,
    pub source: Language,
    pub translation: TranslationText,
    pub target: Language,
    /// Optional phrase-level transcription (DATA-DICT-009).
    /// `#[serde(default)]` so an omitted field is None, not a deserialisation error.
    #[serde(default)]
    pub transcription: Option<Transcription>,
    /// Optional grammatical tags for the Dictionary (DATA-DICT-008).
    /// `#[serde(default)]` so an omitted field is an empty Vec, not a deserialisation error.
    #[serde(default)]
    pub tags: Vec<GrammarTag>,
}

// ── FR-API-002: lookup ────────────────────────────────────────────────────────

/// Query parameters for `GET /api/v1/dictionary/translations`. FR-API-002.
#[derive(Debug, Clone, Deserialize)]
pub struct LookupQuery {
    pub phrase: PhraseText,
    pub source: Language,
    pub target: Language,
}

/// Response for the lookup endpoint. FR-API-002.
#[derive(Debug, Clone, Serialize)]
pub struct LookupResponse {
    /// Always present; carries transcription when stored (FR-API-002 criteria 1 and 3).
    pub phrase: PhraseResponse,
    pub target: Language,
    /// May be empty (FR-API-002 criterion 3 — phrase known, no dictionaries in target language).
    pub translations: Vec<TranslationWithTagsResponse>,
}

// ── FR-API-005: list ─────────────────────────────────────────────────────────

/// Response for `GET /api/v1/dictionary/dictionaries`. Object-wrapped — never a bare array.
/// Object wrapper satisfies NFR-EXT-002 (pagination fields can be added additively).
#[derive(Debug, Clone, Serialize)]
pub struct EntryListResponse {
    pub dictionaries: Vec<EntryResponse>,
    pub count: u64,
}

// ── FR-API-003: update by natural key ────────────────────────────────────────

/// Body for `PUT /api/v1/dictionary/dictionaries` (natural-key relink). FR-API-003.
#[derive(Debug, Clone, Deserialize)]
pub struct UpdateEntryByTextRequest {
    pub phrase: PhraseText,
    pub source: Language,
    pub target: Language,
    pub current_translation: TranslationText,
    pub new_translation: TranslationText,
}

// ── FR-API-004: delete by natural key ────────────────────────────────────────

/// Query parameters for `DELETE /api/v1/dictionary/dictionaries` (natural-key delete). FR-API-004.
#[derive(Debug, Clone, Deserialize)]
pub struct DeleteEntryByTextQuery {
    pub phrase: PhraseText,
    pub source: Language,
    pub target: Language,
    pub translation: TranslationText,
}

// ── FR-API-008/009: surrogate-key variants ────────────────────────────────────

/// Path segments for surrogate-key endpoints. FR-API-008, FR-API-009.
#[derive(Debug, Clone, Deserialize)]
pub struct EntryIdPath {
    pub phrase_id: PhraseId,
    pub translation_id: TranslationId,
}

/// Body for `PUT /api/v1/dictionary/dictionaries/{phrase_id}/{translation_id}`. FR-API-008.
#[derive(Debug, Clone, Deserialize)]
pub struct UpdateEntryByIdRequest {
    pub new_translation: TranslationText,
}

// ── Error DTOs ────────────────────────────────────────────────────────────────
// FR-API-006, FR-API-001, DATA-DICT-004, NFR-EXT-003

/// Machine-readable error codes used in ErrorBody. Serialised snake_case. SPECS §6.3.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ErrorCode {
    ValidationFailed,
    /// Well-formed value absent from a reference table (language-script or tag). DATA-DICT-004.
    UnknownReference,
    NotFound,
    Conflict,
    ServiceUnavailable,
    Internal,
}

/// A field-level validation detail. Included in ValidationFailed and UnknownReference bodies.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FieldError {
    pub field: String,
    pub message: String,
}

/// Inner error body (nested under `{"error": ...}`).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ErrorBody {
    pub code: ErrorCode,
    pub message: String,
    /// Populated for `validation_failed` and `unknown_reference`; omitted otherwise.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub details: Option<Vec<FieldError>>,
}

/// Top-level error response wrapper. SPECS §7.1.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ErrorResponse {
    pub error: ErrorBody,
}

// ── Health DTOs ───────────────────────────────────────────────────────────────

/// Status values for health/readiness responses.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum HealthStatus {
    Ok,
    Unavailable,
}

/// Body for `/healthz` and `/readyz`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HealthResponse {
    pub status: HealthStatus,
}

