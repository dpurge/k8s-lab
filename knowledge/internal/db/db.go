package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	sharedpg "k8s-lab/shared/postgres"
	"knowledge/internal/config"
)

func sharedConfig(cfg config.Config) sharedpg.Config {
	return sharedpg.Config{Host: cfg.PGHost, Port: cfg.PGPort, Database: cfg.PGDatabase, User: cfg.PGUser, Password: cfg.PGPassword}
}

func Connect(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, sharedpg.DSN(sharedConfig(cfg), cfg.PGDatabase))
}

func Migrate(ctx context.Context, cfg config.Config) error {
	if _, err := sharedpg.EnsureDatabase(ctx, sharedConfig(cfg)); err != nil {
		return err
	}
	pool, err := Connect(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, schema)
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS chats (
    id UUID PRIMARY KEY,
    title TEXT NOT NULL,
    tags TEXT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS chat_messages (
    id UUID PRIMARY KEY,
    chat_id UUID NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS chat_message_sources (
    message_id UUID NOT NULL REFERENCES chat_messages(id) ON DELETE CASCADE,
    knowledge_id TEXT NOT NULL,
    title TEXT NOT NULL,
    summary TEXT NOT NULL,
    tags TEXT[] NOT NULL DEFAULT '{}',
    score DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (message_id, knowledge_id)
);`
