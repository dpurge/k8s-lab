use std::net::SocketAddr;
use std::time::Duration;

use sqlx::postgres::PgConnectOptions;

/// Application configuration assembled from environment variables.
pub struct AppConfig {
    pub server: ServerConfig,
    pub database: DatabaseConfig,
    pub log_filter: String,
}

/// HTTP server configuration.
pub struct ServerConfig {
    pub bind: SocketAddr,
}

/// Postgres connection configuration. Custom Debug redacts the password (FR-DB-003).
pub struct DatabaseConfig {
    pub host: String,
    pub port: u16,
    pub database: String,
    pub user: String,
    pub password: String,
    pub max_connections: u32,
    pub acquire_timeout: Duration,
}

impl std::fmt::Debug for DatabaseConfig {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("DatabaseConfig")
            .field("host", &self.host)
            .field("port", &self.port)
            .field("database", &self.database)
            .field("user", &self.user)
            .field("password", &"[REDACTED]")
            .field("max_connections", &self.max_connections)
            .field("acquire_timeout", &self.acquire_timeout)
            .finish()
    }
}

/// Errors from loading configuration.
#[derive(Debug, thiserror::Error)]
pub enum ConfigError {
    #[error("missing required environment variable: {0}")]
    MissingEnv(&'static str),

    #[error("invalid value for {0}: {1}")]
    InvalidValue(&'static str, String),
}

impl AppConfig {
    /// Load configuration from environment variables.
    /// Returns a `ConfigError` with the variable name on the first missing required var.
    pub fn from_env() -> Result<Self, ConfigError> {
        let host = std::env::var("PGHOST")
            .map_err(|_| ConfigError::MissingEnv("PGHOST"))?;

        let port = std::env::var("PGPORT")
            .unwrap_or_else(|_| "5432".to_string())
            .parse::<u16>()
            .map_err(|e| ConfigError::InvalidValue("PGPORT", e.to_string()))?;

        let database = std::env::var("PGDATABASE")
            .map_err(|_| ConfigError::MissingEnv("PGDATABASE"))?;

        let user = std::env::var("PGUSER")
            .map_err(|_| ConfigError::MissingEnv("PGUSER"))?;

        let password = std::env::var("PGPASSWORD")
            .map_err(|_| ConfigError::MissingEnv("PGPASSWORD"))?;

        let max_connections = std::env::var("DB_MAX_CONNECTIONS")
            .unwrap_or_else(|_| "5".to_string())
            .parse::<u32>()
            .map_err(|e| ConfigError::InvalidValue("DB_MAX_CONNECTIONS", e.to_string()))?;

        let bind = std::env::var("BIND_ADDR")
            .unwrap_or_else(|_| "0.0.0.0:8080".to_string())
            .parse::<SocketAddr>()
            .map_err(|e| ConfigError::InvalidValue("BIND_ADDR", e.to_string()))?;

        let log_filter = std::env::var("RUST_LOG").unwrap_or_else(|_| "info".to_string());

        Ok(AppConfig {
            server: ServerConfig { bind },
            database: DatabaseConfig {
                host,
                port,
                database,
                user,
                password,
                max_connections,
                // 3 s: fast enough to surface unreachable Postgres as 503 (FR-API-006)
                acquire_timeout: Duration::from_secs(3),
            },
            log_filter,
        })
    }
}

/// Build `PgConnectOptions` field-wise — no password in a URL string (FR-DB-003).
impl From<&DatabaseConfig> for PgConnectOptions {
    fn from(cfg: &DatabaseConfig) -> Self {
        PgConnectOptions::new()
            .host(&cfg.host)
            .port(cfg.port)
            .database(&cfg.database)
            .username(&cfg.user)
            .password(&cfg.password)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn debug_redacts_password() {
        let cfg = DatabaseConfig {
            host: "localhost".to_string(),
            port: 5432,
            database: "phraseforge".to_string(),
            user: "phraseforge".to_string(),
            password: "super_secret_pw".to_string(),
            max_connections: 5,
            acquire_timeout: Duration::from_secs(3),
        };
        let debug_str = format!("{:?}", cfg);
        assert!(!debug_str.contains("super_secret_pw"), "password must not appear in Debug output");
        assert!(debug_str.contains("REDACTED"), "REDACTED sentinel must appear");
    }
}
