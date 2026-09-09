use phraseforge_api::{config::AppConfig, db::build_pool, http, state::AppState};
use tokio::net::TcpListener;

#[tokio::main]
async fn main() {
    // Init tracing from RUST_LOG (defaults to "info" when absent).
    let log_filter =
        std::env::var("RUST_LOG").unwrap_or_else(|_| "info".to_string());
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::new(&log_filter))
        .init();

    // Load typed configuration; exit non-zero on any missing required var.
    let config = AppConfig::from_env().unwrap_or_else(|e| {
        tracing::error!("Configuration error: {}", e);
        std::process::exit(1);
    });

    tracing::info!(bind = %config.server.bind, "starting phraseforge-api");

    // Build lazy pool — connect_lazy_with defers the first TCP connection to the
    // first pool use, so startup never fails due to Postgres being unreachable.
    // A failing request maps to ApiError::ServiceUnavailable (503) at request time.
    let pool = build_pool(&config.database).unwrap_or_else(|e| {
        tracing::error!("Failed to build connection pool: {}", e);
        std::process::exit(1);
    });

    let state = AppState { pool };
    let app = http::router(state);

    let listener = TcpListener::bind(config.server.bind).await.unwrap_or_else(|e| {
        tracing::error!("Failed to bind to {}: {}", config.server.bind, e);
        std::process::exit(1);
    });

    tracing::info!("listening on {}", listener.local_addr().unwrap_or(config.server.bind));

    axum::serve(listener, app)
        .with_graceful_shutdown(shutdown_signal())
        .await
        .unwrap_or_else(|e| {
            tracing::error!("Server error: {}", e);
            std::process::exit(1);
        });
}

/// Wait for SIGTERM (Unix) or Ctrl-C (all platforms) and return.
///
/// Used as the graceful-shutdown future passed to `axum::serve`.
/// `tokio::select!` returns on whichever signal fires first.
async fn shutdown_signal() {
    let ctrl_c = async {
        tokio::signal::ctrl_c()
            .await
            .unwrap_or_else(|e| {
                tracing::error!("Failed to install Ctrl+C handler: {}", e);
                std::process::exit(1);
            });
    };

    #[cfg(unix)]
    let terminate = async {
        tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
            .unwrap_or_else(|e| {
                tracing::error!("Failed to install SIGTERM handler: {}", e);
                std::process::exit(1);
            })
            .recv()
            .await;
    };

    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        _ = ctrl_c => { tracing::info!("received Ctrl-C, shutting down"); }
        _ = terminate => { tracing::info!("received SIGTERM, shutting down"); }
    }
}
