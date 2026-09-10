// Package db handles dictionary's Postgres connection and schema migration.
// It owns its own database ("dictionary" by default) on the shared Postgres
// server, created and migrated the same way phraseforge's Go app manages
// "phraseforge_app": a self-contained CREATE DATABASE + schema.sql step, run
// via the "migrate" subcommand.
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"dictionary/internal/config"
)

func dsn(cfg config.Config, database string) string {
	return fmt.Sprintf("host=%s port=%s dbname=%s user=%s password=%s sslmode=disable",
		cfg.PGHost, cfg.PGPort, database, cfg.PGUser, cfg.PGPassword)
}

func Connect(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, dsn(cfg, cfg.PGDatabase))
}
