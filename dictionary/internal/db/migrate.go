package db

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"dictionary/internal/config"
)

//go:embed schema.sql
var schemaSQL string

// pgErrCode returns the SQLSTATE code of err, or "" if err isn't a *pgconn.PgError.
func pgErrCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// Migrate creates cfg.PGDatabase on the shared Postgres server if it doesn't
// already exist (connecting via the always-present "postgres" maintenance DB),
// then applies schema.sql against it. Safe to run repeatedly.
func Migrate(ctx context.Context, cfg config.Config) error {
	admin, err := pgx.Connect(ctx, dsn(cfg, "postgres"))
	if err != nil {
		return fmt.Errorf("connect to maintenance db: %w", err)
	}
	defer admin.Close(ctx)

	_, err = admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s OWNER %s", pgx.Identifier{cfg.PGDatabase}.Sanitize(), pgx.Identifier{cfg.PGUser}.Sanitize()))
	if err != nil && pgErrCode(err) != "42P04" { // 42P04 = duplicate_database, i.e. already exists
		return fmt.Errorf("create database %s: %w", cfg.PGDatabase, err)
	}
	if err == nil {
		fmt.Printf("Created database %s\n", cfg.PGDatabase)
	} else {
		fmt.Printf("Database %s already exists\n", cfg.PGDatabase)
	}

	app, err := pgx.Connect(ctx, dsn(cfg, cfg.PGDatabase))
	if err != nil {
		return fmt.Errorf("connect to %s: %w", cfg.PGDatabase, err)
	}
	defer app.Close(ctx)

	if _, err := app.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	fmt.Println("Schema applied.")
	return nil
}
