use serde::{Deserialize, Serialize};

/// Error returned when a string value fails newtype format validation.
#[derive(Debug)]
pub struct ValidationError {
    pub field: &'static str,
    pub message: String,
}

impl std::fmt::Display for ValidationError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}: {}", self.field, self.message)
    }
}

impl std::error::Error for ValidationError {}

// ── ScriptCode ────────────────────────────────────────────────────────────────
// Invariant: exactly 4 lowercase ASCII letters (ISO 15924). DATA-DICT-005.

#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize, sqlx::Type)]
#[serde(try_from = "String", into = "String")]
#[sqlx(transparent)]
pub struct ScriptCode(String);

impl ScriptCode {
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl TryFrom<String> for ScriptCode {
    type Error = ValidationError;

    fn try_from(s: String) -> Result<Self, Self::Error> {
        let lower = s.to_lowercase();
        if lower.len() == 4 && lower.chars().all(|c| c.is_ascii_lowercase()) {
            Ok(ScriptCode(lower))
        } else {
            Err(ValidationError {
                field: "script_code",
                message: format!(
                    "'{}' is not a valid 4-letter ISO 15924 script code (expected ^[a-z]{{4}}$)",
                    s
                ),
            })
        }
    }
}

impl From<ScriptCode> for String {
    fn from(s: ScriptCode) -> Self {
        s.0
    }
}

// ── LanguageCode ─────────────────────────────────────────────────────────────
// Invariant: exactly 3 lowercase ASCII letters (ISO 639-3). DATA-DICT-006.

#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize, sqlx::Type)]
#[serde(try_from = "String", into = "String")]
#[sqlx(transparent)]
pub struct LanguageCode(String);

impl LanguageCode {
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl TryFrom<String> for LanguageCode {
    type Error = ValidationError;

    fn try_from(s: String) -> Result<Self, Self::Error> {
        let lower = s.to_lowercase();
        if lower.len() == 3 && lower.chars().all(|c| c.is_ascii_lowercase()) {
            Ok(LanguageCode(lower))
        } else {
            Err(ValidationError {
                field: "language_code",
                message: format!(
                    "'{}' is not a valid 3-letter ISO 639 language code (expected ^[a-z]{{3}}$)",
                    s
                ),
            })
        }
    }
}

impl From<LanguageCode> for String {
    fn from(s: LanguageCode) -> Self {
        s.0
    }
}

// ── Language ──────────────────────────────────────────────────────────────────
// Invariant: "{3-letter lang}-{4-letter script}", all lowercase — e.g. "cmn-hans".
// DATA-DICT-004, DATA-DICT-006.
//
// Format validation only. Membership in the reference table is layer 2, enforced
// by the service layer (adding it here would violate NFR-EXT-001).

#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize, sqlx::Type)]
#[serde(try_from = "String", into = "String")]
#[sqlx(transparent)]
pub struct Language(String);

impl Language {
    /// The ISO 639 language part of the code (e.g. `"cmn"` from `"cmn-hans"`).
    pub fn language(&self) -> LanguageCode {
        // Safe: invariant guarantees exactly one '-' at position 3.
        let lang = &self.0[..3];
        LanguageCode(lang.to_string())
    }

    /// The ISO 15924 script part of the code (e.g. `"hans"` from `"cmn-hans"`).
    pub fn script(&self) -> ScriptCode {
        // Safe: invariant guarantees '-' at position 3 and 4 chars after.
        let script = &self.0[4..];
        ScriptCode(script.to_string())
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl TryFrom<String> for Language {
    type Error = ValidationError;

    fn try_from(s: String) -> Result<Self, Self::Error> {
        let lower = s.to_lowercase();
        // Must match ^[a-z]{3}-[a-z]{4}$
        let valid = lower.len() == 8
            && lower.as_bytes()[3] == b'-'
            && lower[..3].chars().all(|c| c.is_ascii_lowercase())
            && lower[4..].chars().all(|c| c.is_ascii_lowercase());

        if valid {
            Ok(Language(lower))
        } else {
            Err(ValidationError {
                field: "language_script",
                message: format!(
                    "'{}' is not a valid language-script code (expected ^[a-z]{{3}}-[a-z]{{4}}$, e.g. \"eng-latn\")",
                    s
                ),
            })
        }
    }
}

impl From<Language> for String {
    fn from(ls: Language) -> Self {
        ls.0
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────
// Unit tests per SPECS §10 and T-6.1 acceptance criteria.

#[cfg(test)]
mod tests {
    use super::*;

    // Language: format acceptance
    #[test]
    fn language_accepts_lowercase() {
        let ls = Language::try_from("eng-latn".to_string()).unwrap();
        assert_eq!(ls.as_str(), "eng-latn");
    }

    #[test]
    fn language_normalises_uppercase() {
        let ls = Language::try_from("ENG-LATN".to_string()).unwrap();
        assert_eq!(ls.as_str(), "eng-latn");
    }

    #[test]
    fn language_language_accessor() {
        let ls = Language::try_from("cmn-hans".to_string()).unwrap();
        assert_eq!(ls.language().as_str(), "cmn");
    }

    #[test]
    fn language_script_accessor() {
        let ls = Language::try_from("cmn-hans".to_string()).unwrap();
        assert_eq!(ls.script().as_str(), "hans");
    }

    // Language: format rejection
    #[test]
    fn language_rejects_bare_lang_code() {
        assert!(Language::try_from("eng".to_string()).is_err());
    }

    #[test]
    fn language_rejects_short_script() {
        assert!(Language::try_from("eng-lat".to_string()).is_err());
    }

    #[test]
    fn language_rejects_underscore_separator() {
        assert!(Language::try_from("eng_latn".to_string()).is_err());
    }

    #[test]
    fn language_rejects_two_letter_lang() {
        assert!(Language::try_from("en-latn".to_string()).is_err());
    }

    #[test]
    fn language_rejects_empty() {
        assert!(Language::try_from(String::new()).is_err());
    }

    // ScriptCode: length validation
    #[test]
    fn script_code_accepts_four_letters() {
        assert!(ScriptCode::try_from("latn".to_string()).is_ok());
    }

    #[test]
    fn script_code_rejects_three_letters() {
        assert!(ScriptCode::try_from("lat".to_string()).is_err());
    }

    #[test]
    fn script_code_rejects_five_letters() {
        assert!(ScriptCode::try_from("latin".to_string()).is_err());
    }

    // LanguageCode: length validation
    #[test]
    fn language_code_accepts_three_letters() {
        assert!(LanguageCode::try_from("eng".to_string()).is_ok());
    }

    #[test]
    fn language_code_rejects_two_letters() {
        assert!(LanguageCode::try_from("en".to_string()).is_err());
    }

    #[test]
    fn language_code_normalises_uppercase() {
        let lc = LanguageCode::try_from("ENG".to_string()).unwrap();
        assert_eq!(lc.as_str(), "eng");
    }
}
