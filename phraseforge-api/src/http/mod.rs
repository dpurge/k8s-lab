pub mod health;

use axum::{routing::get, Router};
use tower_http::trace::TraceLayer;

use crate::{dictionary::routes as dict_routes, state::AppState};

/// Build the fully-configured application router with state attached.
///
/// Nests `/api/v1/dictionary` sub-router (7 endpoints, SPECS §7 table).
/// Mounts `/healthz` (liveness, no DB) and `/readyz` (readiness, SELECT 1).
/// Wraps the whole stack in TraceLayer for request-level logging.
/// No authentication middleware anywhere — FR-API-010.
///
/// The returned `Router<()>` (= `Router` with unit state) is ready to pass to `axum::serve`.
pub fn router(state: AppState) -> Router {
    Router::new()
        .nest("/api/v1/dictionary", dict_routes::router())
        .route("/healthz", get(health::healthz))
        .route("/readyz", get(health::readyz))
        .layer(TraceLayer::new_for_http())
        .with_state(state)
}
