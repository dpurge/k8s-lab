package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	sharedpg "k8s-lab/shared/postgres"

	"phraseforge/internal/config"
)

func SharedConfig(cfg config.Config) sharedpg.Config {
	return sharedpg.Config{Host: cfg.PGHost, Port: cfg.PGPort, Database: cfg.PGDatabase, User: cfg.PGUser, Password: cfg.PGPassword}
}

// Connect opens a pool against cfg.PGDatabase, this app's own database on the
// shared Postgres server.
func Connect(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, sharedpg.DSN(SharedConfig(cfg), cfg.PGDatabase))
}
