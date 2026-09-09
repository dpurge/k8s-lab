use phraseforge_api::{config::AppConfig, db::build_pool};

#[tokio::main]
async fn main() {
    // Initialise structured logging from RUST_LOG.
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "info".into()),
        )
        .init();

    let config = AppConfig::from_env().unwrap_or_else(|e| {
        tracing::error!("Configuration error: {}", e);
        std::process::exit(1);
    });

    let pool = build_pool(&config.database).unwrap_or_else(|e| {
        tracing::error!("Failed to build connection pool: {}", e);
        std::process::exit(1);
    });

    tracing::info!("Running pending migrations…");

    // sqlx::migrate!() embeds $CARGO_MANIFEST_DIR/migrations at compile time.
    // Idempotent: if no migrations are pending, this is a no-op (E-11, FR-MIG-001).
    match sqlx::migrate!().run(&pool).await {
        Ok(()) => {
            tracing::info!("Migrations applied successfully.");
        }
        Err(e) => {
            tracing::error!("Migration failed: {}", e);
            std::process::exit(1);
        }
    }
}
