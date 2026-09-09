use axum::{
    extract::rejection::{JsonRejection, PathRejection, QueryRejection},
    http::StatusCode,
    response::{IntoResponse, Response},
    Json,
};
use thiserror::Error;

use crate::dictionary::dto::{ErrorBody, ErrorCode, ErrorResponse, FieldError};

/// All error cases the API can produce.
#[derive(Debug, Error)]
pub enum ApiError {
    /// Body or query deserialisation failed, or a newtype invariant was violated.
    #[error("validation failed")]
    ValidationFailed(Vec<FieldError>),

    /// A well-formed value (language-script or tag) is absent from its reference table.
    /// DATA-DICT-004, FR-API-001 criteria 4/5.
    #[error("unknown reference")]
    UnknownReference(Vec<FieldError>),

    /// The requested resource does not exist.
    #[error("not found")]
    NotFound,

    /// The operation would violate a uniqueness constraint.
    #[error("conflict")]
    Conflict,

    /// Postgres is unreachable or failing. FR-API-006.
    #[error("service unavailable")]
    ServiceUnavailable,

    /// Unexpected internal failure.
    #[error("internal error: {0}")]
    Internal(String),
}

// ── HTTP response mapping ─────────────────────────────────────────────────────

impl IntoResponse for ApiError {
    fn into_response(self) -> Response {
        let (status, code, message, details) = match self {
            ApiError::ValidationFailed(d) => (
                StatusCode::BAD_REQUEST,
                ErrorCode::ValidationFailed,
                "validation failed".to_string(),
                Some(d),
            ),
            ApiError::UnknownReference(d) => (
                StatusCode::BAD_REQUEST,
                ErrorCode::UnknownReference,
                "unknown reference value".to_string(),
                Some(d),
            ),
            ApiError::NotFound => (
                StatusCode::NOT_FOUND,
                ErrorCode::NotFound,
                "not found".to_string(),
                None,
            ),
            ApiError::Conflict => (
                StatusCode::CONFLICT,
                ErrorCode::Conflict,
                "conflict".to_string(),
                None,
            ),
            ApiError::ServiceUnavailable => (
                StatusCode::SERVICE_UNAVAILABLE,
                ErrorCode::ServiceUnavailable,
                "service unavailable".to_string(),
                None,
            ),
            ApiError::Internal(msg) => (
                StatusCode::INTERNAL_SERVER_ERROR,
                ErrorCode::Internal,
                msg,
                None,
            ),
        };

        let body = ErrorResponse {
            error: ErrorBody { code, message, details },
        };

        (status, Json(body)).into_response()
    }
}

// ── From<sqlx::Error> ────────────────────────────────────────────────────────
// Maps sqlx errors to ApiError per SPECS §7.1 status table.

impl From<sqlx::Error> for ApiError {
    fn from(e: sqlx::Error) -> Self {
        use sqlx::Error as SE;

        match &e {
            SE::RowNotFound => ApiError::NotFound,

            SE::PoolTimedOut => ApiError::ServiceUnavailable,
            SE::Io(_) => ApiError::ServiceUnavailable,

            SE::Database(db_err) => {
                let code = db_err.code().unwrap_or_default();
                // SQLSTATE class 08 = connection failures; 57P0x = admin shutdown, crash recovery
                if code.starts_with("08") || code.starts_with("57P0") {
                    ApiError::ServiceUnavailable
                } else if code == "23505" {
                    // unique_violation
                    ApiError::Conflict
                } else if code == "23503" {
                    // foreign_key_violation — race backstop for reference validation (DATA-DICT-004)
                    ApiError::UnknownReference(vec![FieldError {
                        field: "unknown".to_string(),
                        message: db_err.message().to_string(),
                    }])
                } else {
                    ApiError::Internal(db_err.message().to_string())
                }
            }

            _ => ApiError::Internal(e.to_string()),
        }
    }
}

// ── From rejection types ──────────────────────────────────────────────────────

impl From<JsonRejection> for ApiError {
    fn from(r: JsonRejection) -> Self {
        ApiError::ValidationFailed(vec![FieldError {
            field: "body".to_string(),
            message: r.to_string(),
        }])
    }
}

impl From<QueryRejection> for ApiError {
    fn from(r: QueryRejection) -> Self {
        ApiError::ValidationFailed(vec![FieldError {
            field: "query".to_string(),
            message: r.to_string(),
        }])
    }
}

/// Maps a path extraction failure (e.g. malformed UUID) to 400 validation_failed.
/// SPECS §7.1: "malformed UUID path segment ⇒ 400 validation_failed". FR-API-008/009.
impl From<PathRejection> for ApiError {
    fn from(r: PathRejection) -> Self {
        ApiError::ValidationFailed(vec![FieldError {
            field: "path".to_string(),
            message: r.to_string(),
        }])
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────
// T-6.1: one assertion per arm of the status table (SPECS §7.1).

#[cfg(test)]
mod tests {
    use super::*;

    fn status_of(e: ApiError) -> StatusCode {
        let resp = e.into_response();
        resp.status()
    }

    #[test]
    fn validation_failed_is_400() {
        assert_eq!(status_of(ApiError::ValidationFailed(vec![])), StatusCode::BAD_REQUEST);
    }

    #[test]
    fn unknown_reference_is_400() {
        assert_eq!(status_of(ApiError::UnknownReference(vec![])), StatusCode::BAD_REQUEST);
    }

    #[test]
    fn not_found_is_404() {
        assert_eq!(status_of(ApiError::NotFound), StatusCode::NOT_FOUND);
    }

    #[test]
    fn conflict_is_409() {
        assert_eq!(status_of(ApiError::Conflict), StatusCode::CONFLICT);
    }

    #[test]
    fn service_unavailable_is_503() {
        assert_eq!(
            status_of(ApiError::ServiceUnavailable),
            StatusCode::SERVICE_UNAVAILABLE
        );
    }

    #[test]
    fn internal_is_500() {
        assert_eq!(
            status_of(ApiError::Internal("oops".to_string())),
            StatusCode::INTERNAL_SERVER_ERROR
        );
    }
}
