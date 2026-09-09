use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::dictionary::{
    language::{Language, ValidationError},
    tag::GrammarTag,
};

// ── Surrogate-key newtypes ────────────────────────────────────────────────────
// DATA-DICT-002: dictionary PK = (phrase_id, translation_id) — the surrogate-key
// scheme for FR-API-008/009.

/// Surrogate key for a Phrase row. Wraps UUID; transparent to serde and sqlx.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize, sqlx::Type)]
#[serde(transparent)]
#[sqlx(transparent)]
pub struct PhraseId(pub Uuid);

/// Surrogate key for a Translation row.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize, sqlx::Type)]
#[serde(transparent)]
#[sqlx(transparent)]
pub struct TranslationId(pub Uuid);

// ── Text-value newtypes ───────────────────────────────────────────────────────
// DATA-DICT-001, DATA-DICT-009. Invariant: trimmed and non-empty.

/// The text of a dictionary phrase. Invariant: trimmed, non-empty. DATA-DICT-001.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize, sqlx::Type)]
#[serde(try_from = "String", into = "String")]
#[sqlx(transparent)]
pub struct PhraseText(String);

impl PhraseText {
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl TryFrom<String> for PhraseText {
    type Error = ValidationError;

    fn try_from(s: String) -> Result<Self, Self::Error> {
        let trimmed = s.trim();
        if trimmed.is_empty() {
            Err(ValidationError {
                field: "phrase",
                message: "phrase text must not be blank or whitespace-only".to_string(),
            })
        } else {
            Ok(PhraseText(trimmed.to_string()))
        }
    }
}

impl From<PhraseText> for String {
    fn from(t: PhraseText) -> Self {
        t.0
    }
}

/// The text of a translation. Invariant: trimmed, non-empty. DATA-DICT-001.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize, sqlx::Type)]
#[serde(try_from = "String", into = "String")]
#[sqlx(transparent)]
pub struct TranslationText(String);

impl TranslationText {
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl TryFrom<String> for TranslationText {
    type Error = ValidationError;

    fn try_from(s: String) -> Result<Self, Self::Error> {
        let trimmed = s.trim();
        if trimmed.is_empty() {
            Err(ValidationError {
                field: "translation",
                message: "translation text must not be blank or whitespace-only".to_string(),
            })
        } else {
            Ok(TranslationText(trimmed.to_string()))
        }
    }
}

impl From<TranslationText> for String {
    fn from(t: TranslationText) -> Self {
        t.0
    }
}

/// Optional phonetic transcription stored on a Phrase (e.g. pinyin for Chinese).
/// Invariant: trimmed, non-empty — always used as Option<Transcription>. DATA-DICT-009.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize, sqlx::Type)]
#[serde(try_from = "String", into = "String")]
#[sqlx(transparent)]
pub struct Transcription(String);

impl Transcription {
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl TryFrom<String> for Transcription {
    type Error = ValidationError;

    fn try_from(s: String) -> Result<Self, Self::Error> {
        let trimmed = s.trim();
        if trimmed.is_empty() {
            Err(ValidationError {
                field: "transcription",
                message: "transcription must not be blank or whitespace-only".to_string(),
            })
        } else {
            Ok(Transcription(trimmed.to_string()))
        }
    }
}

impl From<Transcription> for String {
    fn from(t: Transcription) -> Self {
        t.0
    }
}

// ── Reference-table entities ──────────────────────────────────────────────────
// DATA-DICT-005, DATA-DICT-006, DATA-DICT-007

/// A seeded writing script (e.g. `latn`, `hans`). DATA-DICT-005.
#[derive(Debug, Clone, sqlx::FromRow)]
pub struct Script {
    pub code: crate::dictionary::language::ScriptCode,
}

/// A seeded language-script combination (e.g. `cmn-hans`). DATA-DICT-006.
#[derive(Debug, Clone, sqlx::FromRow)]
pub struct LanguageRow {
    pub code: Language,
    pub language_code: crate::dictionary::language::LanguageCode,
    pub script_code: crate::dictionary::language::ScriptCode,
}

/// A seeded grammatical tag (e.g. `noun`). DATA-DICT-007.
#[derive(Debug, Clone, sqlx::FromRow)]
pub struct GrammarTagRow {
    pub code: GrammarTag,
}

// ── Content-table entities ────────────────────────────────────────────────────
// DATA-DICT-001, DATA-DICT-002, DATA-DICT-008, DATA-DICT-009

/// A dictionary phrase, uniquely identified by (language_script, text). DATA-DICT-001.
/// `transcription` is nullable and excluded from the unique key (DATA-DICT-009).
#[derive(Debug, Clone, sqlx::FromRow)]
pub struct Phrase {
    pub id: PhraseId,
    pub language_script: Language,
    pub text: PhraseText,
    pub transcription: Option<Transcription>, // DATA-DICT-009: nullable, not in dedup key
    pub created_at: DateTime<Utc>,
}

/// A translation, uniquely identified by (language_script, text). DATA-DICT-001.
#[derive(Debug, Clone, sqlx::FromRow)]
pub struct Translation {
    pub id: TranslationId,
    pub language_script: Language,
    pub text: TranslationText,
    pub created_at: DateTime<Utc>,
}

/// An Dictionary linking a Phrase to a Translation. PK = (phrase_id, translation_id). DATA-DICT-002.
#[derive(Debug, Clone, sqlx::FromRow)]
pub struct Dictionary {
    pub phrase_id: PhraseId,
    pub translation_id: TranslationId,
    pub created_at: DateTime<Utc>,
}

/// An association between an Dictionary and a grammatical tag. DATA-DICT-008.
#[derive(Debug, Clone, sqlx::FromRow)]
pub struct DictionaryTag {
    pub phrase_id: PhraseId,
    pub translation_id: TranslationId,
    pub tag_code: GrammarTag,
}

/// Join projection used by list_dictionary (FR-API-005) and lookup (FR-API-002).
/// `tags` is decoded from a Postgres `text[]` produced by
/// `COALESCE(array_agg(tag_code) FILTER (WHERE tag_code IS NOT NULL), '{}')` —
/// an untagged Dictionary yields an empty Vec, never NULL (DATA-DICT-008 second criterion).
///
/// `Vec<GrammarTag>` decodes from `text[]` via the PgHasArrayType impl that
/// sqlx 0.9 auto-generates for `GrammarTag`. Do NOT add a manual impl. (T-1.2 w-f 3)
#[derive(Debug, Clone, sqlx::FromRow)]
pub struct DictionaryJoinRow {
    pub phrase_id: PhraseId,
    pub word_language_script: Language,
    pub phrase_text: PhraseText,
    pub word_transcription: Option<Transcription>,    // DATA-DICT-009
    pub translation_id: TranslationId,
    pub translation_language_script: Language,
    pub translation_text: TranslationText,
    pub tags: Vec<GrammarTag>,                   // DATA-DICT-008: [] when none
}

/// Repository result type for the lookup query (FR-API-002).
/// Returned by list_translations_with_tags_for_word; converted to DTO in service/routes.
#[derive(Debug, Clone, sqlx::FromRow)]
pub struct TranslationWithTags {
    pub id: TranslationId,
    pub language_script: Language,
    pub text: TranslationText,
    pub tags: Vec<GrammarTag>,
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn phrase_text_rejects_blank() {
        assert!(PhraseText::try_from("   ".to_string()).is_err());
    }

    #[test]
    fn phrase_text_rejects_empty() {
        assert!(PhraseText::try_from(String::new()).is_err());
    }

    #[test]
    fn phrase_text_accepts_non_blank() {
        let t = PhraseText::try_from("house".to_string()).unwrap();
        assert_eq!(t.as_str(), "house");
    }

    #[test]
    fn phrase_text_trims() {
        let t = PhraseText::try_from("  house  ".to_string()).unwrap();
        assert_eq!(t.as_str(), "house");
    }

    #[test]
    fn transcription_rejects_blank() {
        assert!(Transcription::try_from("   ".to_string()).is_err());
    }

    #[test]
    fn transcription_accepts_non_blank() {
        let t = Transcription::try_from("fángzi".to_string()).unwrap();
        assert_eq!(t.as_str(), "fángzi");
    }

    #[test]
    fn translation_text_rejects_empty() {
        assert!(TranslationText::try_from(String::new()).is_err());
    }

    #[test]
    fn translation_text_roundtrip() {
        let t = TranslationText::try_from("maison".to_string()).unwrap();
        let s: String = t.into();
        assert_eq!(s, "maison");
    }
}
