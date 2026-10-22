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

// SchemaSQL is the idempotent DDL Migrate applies, for tests that need the tables
// in a throwaway schema.
func SchemaSQL() string { return schema }

const schema = `
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY,
    email TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sessions (
    token TEXT PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);
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
);
CREATE TABLE IF NOT EXISTS jobs (
    id UUID PRIMARY KEY,
    kind TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running', 'done', 'failed')),
    step TEXT NOT NULL DEFAULT '',
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS jobs_one_running_idx ON jobs (kind) WHERE status = 'running';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS source_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS source_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS source_text TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS source_tags TEXT[] NOT NULL DEFAULT '{}';
CREATE TABLE IF NOT EXISTS knowledge_drafts (
    id UUID PRIMARY KEY,
    job_id UUID REFERENCES jobs(id) ON DELETE SET NULL,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('url', 'text')),
    source_ref TEXT NOT NULL,
    chunk_index INT NOT NULL,
    title TEXT NOT NULL,
    summary TEXT NOT NULL,
    body TEXT NOT NULL,
    tags TEXT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS knowledge_drafts_job_id_idx ON knowledge_drafts (job_id);
CREATE INDEX IF NOT EXISTS knowledge_drafts_created_at_idx ON knowledge_drafts (created_at DESC);
CREATE TABLE IF NOT EXISTS operations (
    id UUID PRIMARY KEY,
    kind TEXT NOT NULL,
    priority TEXT NOT NULL CHECK (priority IN ('interactive', 'background')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'done', 'failed')),
    payload JSONB NOT NULL DEFAULT '{}',
    result JSONB,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS operations_one_running_idx ON operations ((true)) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS operations_claim_idx ON operations (created_at ASC) WHERE status = 'pending';
-- One row per attempt to call a model, embeddings included (see internal/llmcalls).
-- A row never holds a prompt, a reply, a text or a vector. operation_id is the queue
-- operation the call ran in (every chat, title, summary, translate and ingest call
-- runs in one) and is NULL for a call made outside the queue, such as the embedding
-- of a search query; it has no foreign key, so purging operations never touches it.
CREATE TABLE IF NOT EXISTS llm_calls (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    operation_id  UUID,
    kind          TEXT NOT NULL DEFAULT '',
    model_id      TEXT NOT NULL,
    provider_id   TEXT NOT NULL,
    model_name    TEXT NOT NULL,
    tags          TEXT[] NOT NULL DEFAULT '{}',
    input_tokens  INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    cost_usd      NUMERIC(14, 8) NOT NULL DEFAULT 0 CHECK (cost_usd >= 0),
    started_at    TIMESTAMPTZ NOT NULL,
    duration_ms   INTEGER NOT NULL CHECK (duration_ms >= 0),
    outcome       TEXT NOT NULL CHECK (outcome IN ('ok', 'error', 'timeout', 'rate_limited', 'canceled')),
    error         TEXT,
    http_status   INTEGER,
    request_id    TEXT
);
CREATE INDEX IF NOT EXISTS llm_calls_started_idx ON llm_calls (started_at);
CREATE INDEX IF NOT EXISTS llm_calls_model_idx ON llm_calls (model_id, started_at);
CREATE INDEX IF NOT EXISTS llm_calls_operation_idx ON llm_calls (operation_id);`
