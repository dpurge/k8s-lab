package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"phraseforge/internal/config"
)

// dsn builds a libpq connection string for the given database name.
func dsn(cfg config.Config, database string) string {
	return fmt.Sprintf("host=%s port=%s dbname=%s user=%s password=%s sslmode=disable",
		cfg.PGHost, cfg.PGPort, database, cfg.PGUser, cfg.PGPassword)
}

// Connect opens a pool against cfg.PGDatabase (the app's own database, separate
// from phraseforge-api's "phraseforge" database on the same Postgres server).
func Connect(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, dsn(cfg, cfg.PGDatabase))
}
