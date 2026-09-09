use axum::{
    extract::State,
    http::StatusCode,
    response::IntoResponse,
    Json,
};

use crate::{
    dictionary::dto::{HealthResponse, HealthStatus},
    error::ApiError,
    state::AppState,
};

/// GET /healthz — liveness probe. No database interaction.
///
/// Returns 200 unconditionally so a Postgres outage never causes the pod to be
/// restarted. FR-API-006 (liveness half), SPECS §10, T-5.3 AC.
pub async fn healthz() -> impl IntoResponse {
    Json(HealthResponse { status: HealthStatus::Ok })
}

/// GET /readyz — readiness probe. Confirms Postgres connectivity with SELECT 1.
///
/// Returns 200 when Postgres is reachable, 503 otherwise.
/// FR-API-006 (readiness half), E-07, T-5.3 AC.
pub async fn readyz(State(state): State<AppState>) -> axum::response::Response {
    match sqlx::query("SELECT 1").execute(&state.pool).await {
        Ok(_) => {
            (StatusCode::OK, Json(HealthResponse { status: HealthStatus::Ok })).into_response()
        }
        Err(_) => ApiError::ServiceUnavailable.into_response(),
    }
}
