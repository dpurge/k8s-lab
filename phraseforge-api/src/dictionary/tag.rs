use serde::{Deserialize, Serialize};

/// A grammatical tag code from the controlled vocabulary.
/// Invariant: matches ^[a-z][a-z_-]{1,31}$ — all lowercase, no normalization.
/// Uppercase input is REJECTED, not silently normalized. DATA-DICT-007, SPECS §10.
///
/// Unlike `LanguageScript`/`ScriptCode`/`LanguageCode` (which normalize to lowercase),
/// `GrammarTag` treats uppercase as invalid: SPECS §10 lists `"Noun"` as an
/// error-path test case (should yield 400 validation_failed), not a normalization case.
///
/// Do NOT hand-write `impl PgHasArrayType for GrammarTag` — sqlx 0.9
/// auto-generates it via `#[derive(sqlx::Type)]` for transparent newtypes.
/// A manual impl would cause a conflicting-implementation compile error. (T-1.2 watch-for 3)
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize, sqlx::Type)]
#[serde(try_from = "String", into = "String")]
#[sqlx(transparent)]
pub struct GrammarTag(String);

impl GrammarTag {
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl TryFrom<String> for GrammarTag {
    type Error = crate::dictionary::language::ValidationError;

    fn try_from(s: String) -> Result<Self, Self::Error> {
        // SPECS §10 fix: uppercase characters are REJECTED, not normalized.
        // "Noun" must yield a validation error, not silently become "noun".
        // This is different from LanguageScript/ScriptCode/LanguageCode which normalize.
        if s.chars().any(|c: char| c.is_uppercase()) {
            return Err(crate::dictionary::language::ValidationError {
                field: "grammar_tag",
                message: format!(
                    "'{}' is not a valid grammatical tag code: uppercase characters are not \
                     allowed (expected ^[a-z][a-z_-]{{1,31}}$)",
                    s
                ),
            });
        }

        // All chars already confirmed not-uppercase; validate ^[a-z][a-z_-]{1,31}$
        let len = s.len();
        let valid = len >= 2
            && len <= 32
            && s.starts_with(|c: char| c.is_ascii_lowercase())
            && s[1..].chars().all(|c| c.is_ascii_lowercase() || c == '_' || c == '-');

        if valid {
            Ok(GrammarTag(s))
        } else {
            Err(crate::dictionary::language::ValidationError {
                field: "grammar_tag",
                message: format!(
                    "'{}' is not a valid grammatical tag code (expected ^[a-z][a-z_-]{{1,31}}$)",
                    s
                ),
            })
        }
    }
}

impl From<GrammarTag> for String {
    fn from(t: GrammarTag) -> Self {
        t.0
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────
// T-6.1 acceptance criteria for GrammarTag (SPECS §10).

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tag_accepts_noun() {
        assert!(GrammarTag::try_from("noun".to_string()).is_ok());
    }

    #[test]
    fn tag_accepts_multi_word_with_hyphen() {
        assert!(GrammarTag::try_from("noun-phrase".to_string()).is_ok());
    }

    #[test]
    fn tag_accepts_with_underscore() {
        assert!(GrammarTag::try_from("noun_phrase".to_string()).is_ok());
    }

    /// SPECS §10 error-path test: "Noun" (uppercase N) must be REJECTED.
    /// Unlike LanguageScript/ScriptCode which normalize uppercase to lowercase,
    /// GrammarTag rejects uppercase input as invalid.
    #[test]
    fn tag_rejects_uppercase_noun() {
        let err = GrammarTag::try_from("Noun".to_string());
        assert!(err.is_err(), "'Noun' with uppercase N must be rejected");
    }

    /// SPECS §10: "no un" (space in tag) must be rejected.
    #[test]
    fn tag_rejects_with_space() {
        assert!(GrammarTag::try_from("no un".to_string()).is_err());
    }

    #[test]
    fn tag_rejects_single_char() {
        // length < 2
        assert!(GrammarTag::try_from("n".to_string()).is_err());
    }

    #[test]
    fn tag_rejects_empty() {
        assert!(GrammarTag::try_from(String::new()).is_err());
    }

    #[test]
    fn tag_rejects_all_uppercase() {
        assert!(GrammarTag::try_from("NOUN".to_string()).is_err());
    }

    #[test]
    fn tag_rejects_mixed_case() {
        assert!(GrammarTag::try_from("nOun".to_string()).is_err());
    }

    #[test]
    fn tag_roundtrips() {
        let t = GrammarTag::try_from("adjective".to_string()).unwrap();
        let s: String = t.clone().into();
        assert_eq!(s, "adjective");
    }

    #[test]
    fn tag_accepts_verb() {
        assert!(GrammarTag::try_from("verb".to_string()).is_ok());
    }

    #[test]
    fn tag_accepts_adverb() {
        assert!(GrammarTag::try_from("adverb".to_string()).is_ok());
    }
}
