use sqlx::{postgres::PgPoolOptions, PgPool};

use crate::config::DatabaseConfig;
use crate::error::ApiError;

/// Build a lazy Postgres connection pool from the typed database configuration.
///
/// `connect_lazy_with` never fails (it defers actual TCP connections to first
/// pool use), so the Result<> exists only for the shared function signature and
/// future validation use-cases.
pub fn build_pool(cfg: &DatabaseConfig) -> Result<PgPool, ApiError> {
    let pool = PgPoolOptions::new()
        .max_connections(cfg.max_connections)
        .acquire_timeout(cfg.acquire_timeout)
        .connect_lazy_with(cfg.into());
    Ok(pool)
}
