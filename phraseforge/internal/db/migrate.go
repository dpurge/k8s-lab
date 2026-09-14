package db

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5"
	sharedpg "k8s-lab/shared/postgres"

	"phraseforge/internal/config"
)

//go:embed schema.sql
var schemaSQL string

// Migrate creates cfg.PGDatabase on the shared Postgres server if it doesn't
// already exist (connecting via the always-present "postgres" maintenance DB),
// then applies schema.sql against it. Safe to run repeatedly.
func Migrate(ctx context.Context, cfg config.Config) error {
	created, err := sharedpg.EnsureDatabase(ctx, SharedConfig(cfg))
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("Created database %s\n", cfg.PGDatabase)
	} else {
		fmt.Printf("Database %s already exists\n", cfg.PGDatabase)
	}

	app, err := pgx.Connect(ctx, sharedpg.DSN(SharedConfig(cfg), cfg.PGDatabase))
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
