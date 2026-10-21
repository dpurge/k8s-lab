-- The assistant's database schema. Idempotent: `assistant migrate` runs it on
-- every deployment, and running it twice changes nothing. This is the only
-- source of DDL; tables are never altered anywhere else.

-- ── Users ────────────────────────────────────────────────────────────────────
-- Everyone a task can be run for. Cost is attributed to a user.
CREATE TABLE IF NOT EXISTS users (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    telegram_id  bigint UNIQUE,
    display_name text NOT NULL CHECK (display_name <> ''),
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- ── Tasks ────────────────────────────────────────────────────────────────────
-- The queue. A task names the workflow to run and the user it runs for.
-- Queue time is started_at - created_at and execution time is
-- finished_at - started_at; neither is stored. started_at is the first start:
-- a task that waited for the user and was resumed keeps it.
CREATE TABLE IF NOT EXISTS tasks (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users (id),
    workflow_id text NOT NULL CHECK (workflow_id <> ''),
    source      text NOT NULL CHECK (source IN ('telegram', 'nats', 'scheduler', 'api')),
    status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'waiting', 'succeeded', 'failed', 'cancelled')),
    payload     jsonb NOT NULL DEFAULT '{}',
    result      jsonb,
    error       text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    started_at  timestamptz,
    finished_at timestamptz,
    -- A workflow run, as the engine's JSON snapshot: kept while the task waits
    -- for the user, and as the final state of a finished run.
    run         jsonb,
    -- What a waiting run asked the user (the ask_user payload).
    ask         jsonb,
    -- The user's answer to a waiting run, until the worker has delivered it.
    resume_message jsonb
);

-- Tables created before the engine was connected to the worker lack the run
-- columns and the waiting status.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS run jsonb;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS ask jsonb;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS resume_message jsonb;
ALTER TABLE tasks DROP CONSTRAINT IF EXISTS tasks_status_check;
ALTER TABLE tasks ADD CONSTRAINT tasks_status_check CHECK (status IN ('pending', 'running', 'waiting', 'succeeded', 'failed', 'cancelled'));

-- The worker takes the oldest pending task.
CREATE INDEX IF NOT EXISTS tasks_claim_idx ON tasks (created_at, id) WHERE status = 'pending';
-- Listing, newest first, with or without a status filter.
CREATE INDEX IF NOT EXISTS tasks_list_idx ON tasks (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS tasks_status_list_idx ON tasks (status, created_at DESC, id DESC);
-- At most one task runs at a time, app-wide: one worker, one task.
CREATE UNIQUE INDEX IF NOT EXISTS tasks_one_running_idx ON tasks ((true)) WHERE status = 'running';

-- ── Model calls ──────────────────────────────────────────────────────────────
-- One row per attempt to call a model, so a fallback leaves a failed row and a
-- successful one. Cost is attributed to the user the task runs for. A prompt or
-- a reply is never stored here: only who, which model, how much and how long.
CREATE TABLE IF NOT EXISTS llm_calls (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id       uuid REFERENCES tasks (id),
    user_id       uuid NOT NULL REFERENCES users (id),
    model_id      text NOT NULL,
    provider_id   text NOT NULL,
    model_name    text NOT NULL,
    tags          text[] NOT NULL DEFAULT '{}',
    input_tokens  integer NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens integer NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    cost_usd      numeric(14, 8) NOT NULL DEFAULT 0 CHECK (cost_usd >= 0),
    started_at    timestamptz NOT NULL,
    duration_ms   integer NOT NULL CHECK (duration_ms >= 0),
    outcome       text NOT NULL CHECK (outcome IN ('ok', 'error', 'timeout', 'rate_limited', 'canceled')),
    error         text
);

-- A task's attempts, oldest first.
CREATE INDEX IF NOT EXISTS llm_calls_task_idx ON llm_calls (task_id, started_at);
-- Usage per user over time.
CREATE INDEX IF NOT EXISTS llm_calls_user_idx ON llm_calls (user_id, started_at);

-- ── Workflow trace ───────────────────────────────────────────────────────────
-- One row per engine step attempt of a task's run: what the model answered, what
-- the engine made of it and what it cost. Unlike llm_calls it holds the model's
-- raw reply (cut at 8 KB): it is the debugging trace of the engine, kept in the
-- database only and never logged.
CREATE TABLE IF NOT EXISTS task_steps (
    id            bigserial PRIMARY KEY,
    task_id       uuid NOT NULL REFERENCES tasks (id),
    step          integer NOT NULL CHECK (step >= 0),
    attempt       integer NOT NULL CHECK (attempt >= 0),
    state         text NOT NULL,
    outcome       text NOT NULL CHECK (outcome IN ('ok', 'retry', 'rejected', 'failed')),
    error         text,
    action        jsonb,
    patch         jsonb,
    tool          text,
    tool_result   text,
    reply         text,
    input_tokens  integer NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens integer NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    duration_ms   integer NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    at            timestamptz NOT NULL,
    -- The workflow the step ran in and how deep that run was: a request handed
    -- to another workflow (delegation) puts its steps in the same trace, one
    -- level down. NULL and 0 for the steps of a run that delegated to no one.
    workflow_id   text,
    depth         integer NOT NULL DEFAULT 0 CHECK (depth >= 0)
);
ALTER TABLE task_steps ADD COLUMN IF NOT EXISTS workflow_id text;
ALTER TABLE task_steps ADD COLUMN IF NOT EXISTS depth integer NOT NULL DEFAULT 0 CHECK (depth >= 0);

-- A task's trace, in the order it happened.
CREATE INDEX IF NOT EXISTS task_steps_task_idx ON task_steps (task_id, id);

-- ── Conversations ────────────────────────────────────────────────────────────
-- A chat between a user and the assistant: a series of messages that drive one
-- workflow run at a time. A user can have several. A first message starts a task
-- for the conversation's workflow; while that task waits for an answer, the next
-- message is the answer; once it is finished, the next message starts a new run.
CREATE TABLE IF NOT EXISTS conversations (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid NOT NULL REFERENCES users (id),
    workflow_id      text NOT NULL CHECK (workflow_id <> ''),
    title            text NOT NULL DEFAULT '',
    -- The run the conversation is in; cleared if that task is removed.
    task_id          uuid REFERENCES tasks (id) ON DELETE SET NULL,
    -- The Telegram chat this conversation mirrors, once the real channel exists.
    telegram_chat_id bigint,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS conversations_user_idx ON conversations (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS conversations_task_idx ON conversations (task_id);

-- What was said, in order. task_id says which run an assistant message came
-- from; it has no reference, so removing a task leaves the history readable.
CREATE TABLE IF NOT EXISTS messages (
    id              bigserial PRIMARY KEY,
    conversation_id uuid NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    role            text NOT NULL CHECK (role IN ('user', 'assistant')),
    kind            text NOT NULL DEFAULT 'text' CHECK (kind IN ('text', 'question', 'result', 'error', 'info', 'ui', 'ui_action')),
    text            text NOT NULL,
    task_id         uuid,
    -- A2UI: for kind 'ui' the array of A2UI messages the assistant sent to the
    -- page (already validated); for 'ui_action' the one message the page sent
    -- back (an action, an error, a function call or its answer). NULL for the
    -- other kinds.
    ui              jsonb,
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- Databases made before A2UI have neither the column nor the wider kinds.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS ui jsonb;
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_kind_check;
ALTER TABLE messages ADD CONSTRAINT messages_kind_check CHECK (kind IN ('text', 'question', 'result', 'error', 'info', 'ui', 'ui_action'));
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_ui_check;
ALTER TABLE messages ADD CONSTRAINT messages_ui_check CHECK ((kind IN ('ui', 'ui_action')) = (ui IS NOT NULL));

CREATE INDEX IF NOT EXISTS messages_conversation_idx ON messages (conversation_id, id);
