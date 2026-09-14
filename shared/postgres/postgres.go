package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Config struct{ Host, Port, Database, User, Password string }

func DSN(c Config, database string) string {
	return fmt.Sprintf("host=%s port=%s dbname=%s user=%s password=%s sslmode=disable", c.Host, c.Port, database, c.User, c.Password)
}

func EnsureDatabase(ctx context.Context, c Config) (bool, error) {
	admin, err := pgx.Connect(ctx, DSN(c, "postgres"))
	if err != nil {
		return false, fmt.Errorf("connect to maintenance db: %w", err)
	}
	defer admin.Close(ctx)
	_, err = admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s OWNER %s", pgx.Identifier{c.Database}.Sanitize(), pgx.Identifier{c.User}.Sanitize()))
	if err == nil {
		return true, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P04" {
		return false, nil
	}
	return false, fmt.Errorf("create database %s: %w", c.Database, err)
}
