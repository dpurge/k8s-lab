// Package db applies the assistant's database schema.
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"assistant/schemas"
)

// Migrate applies schemas/db/schema.sql, which is idempotent: running it again
// leaves the database as it was.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, schemas.DB); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}
