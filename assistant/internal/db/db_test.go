package db_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"assistant/internal/db"
	"assistant/internal/dbtest"
)

type column struct {
	name, typ  string
	nullable   bool
	hasDefault bool
}

func columnsOf(t *testing.T, pool *pgxpool.Pool, table string) []column {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT column_name, data_type, is_nullable = 'YES', column_default IS NOT NULL
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1
		ORDER BY ordinal_position`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.name, &c.typ, &c.nullable, &c.hasDefault); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func TestUsersTableHasTheAgreedColumns(t *testing.T) {
	pool := dbtest.Pool(t)
	want := []column{
		{"id", "uuid", false, true},
		{"telegram_id", "bigint", true, false},
		{"display_name", "text", false, false},
		{"created_at", "timestamp with time zone", false, true},
	}
	got := columnsOf(t, pool, "users")
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("users columns:\n got %v\nwant %v", got, want)
	}
}

func TestTasksTableHasTheAgreedColumns(t *testing.T) {
	pool := dbtest.Pool(t)
	want := []column{
		{"id", "uuid", false, true},
		{"user_id", "uuid", false, false},
		{"workflow_id", "text", false, false},
		{"source", "text", false, false},
		{"status", "text", false, true},
		{"payload", "jsonb", false, true},
		{"result", "jsonb", true, false},
		{"error", "text", true, false},
		{"created_at", "timestamp with time zone", false, true},
		{"started_at", "timestamp with time zone", true, false},
		{"finished_at", "timestamp with time zone", true, false},
		{"run", "jsonb", true, false},
		{"ask", "jsonb", true, false},
		{"resume_message", "jsonb", true, false},
	}
	got := columnsOf(t, pool, "tasks")
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("tasks columns:\n got %v\nwant %v", got, want)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	pool := dbtest.Pool(t) // already migrated once
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO users (display_name) VALUES ('Ada')`); err != nil {
		t.Fatal(err)
	}
	before := columnsOf(t, pool, "tasks")
	for i := 0; i < 2; i++ {
		if err := db.Migrate(ctx, pool); err != nil {
			t.Fatalf("migrate again (%d): %v", i+1, err)
		}
	}
	if fmt.Sprint(columnsOf(t, pool, "tasks")) != fmt.Sprint(before) {
		t.Errorf("a second migration changed the tasks table")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil || n != 1 {
		t.Errorf("a second migration must keep the data: %d rows, %v", n, err)
	}
}

func mustPgError(t *testing.T, err error, code string) *pgconn.PgError {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("want PostgreSQL error %s, got %v", code, err)
	}
	return pgErr
}

func newUser(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `INSERT INTO users (display_name) VALUES ('Ada') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestConstraintsRejectBadRows(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	user := newUser(t, pool)

	tests := []struct {
		name, sql  string
		args       []any
		code       string
		constraint string
	}{
		{"blank display name", `INSERT INTO users (display_name) VALUES ('')`, nil, "23514", "users_display_name_check"},
		{"missing display name", `INSERT INTO users (telegram_id) VALUES (1)`, nil, "23502", ""},
		{"bad source", `INSERT INTO tasks (user_id, workflow_id, source) VALUES ($1, 'noop', 'carrier-pigeon')`, []any{user}, "23514", "tasks_source_check"},
		{"bad status", `INSERT INTO tasks (user_id, workflow_id, source, status) VALUES ($1, 'noop', 'api', 'done')`, []any{user}, "23514", "tasks_status_check"},
		{"blank workflow", `INSERT INTO tasks (user_id, workflow_id, source) VALUES ($1, '', 'api')`, []any{user}, "23514", "tasks_workflow_id_check"},
		{"unknown user", `INSERT INTO tasks (user_id, workflow_id, source) VALUES (gen_random_uuid(), 'noop', 'api')`, nil, "23503", "tasks_user_id_fkey"},
		{"missing user", `INSERT INTO tasks (workflow_id, source) VALUES ('noop', 'api')`, nil, "23502", ""},
		{"payload not valid jsonb", `INSERT INTO tasks (user_id, workflow_id, source, payload) VALUES ($1, 'noop', 'api', 'not json')`, []any{user}, "22P02", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, tc.sql, tc.args...)
			pgErr := mustPgError(t, err, tc.code)
			if tc.constraint != "" && pgErr.ConstraintName != tc.constraint {
				t.Errorf("constraint = %q, want %q", pgErr.ConstraintName, tc.constraint)
			}
		})
	}
}

func TestEverySourceAndStatusIsAccepted(t *testing.T) {
	pool := dbtest.Pool(t)
	user := newUser(t, pool)
	ctx := context.Background()
	for _, source := range []string{"telegram", "nats", "scheduler", "api"} {
		if _, err := pool.Exec(ctx, `INSERT INTO tasks (user_id, workflow_id, source) VALUES ($1, 'noop', $2)`, user, source); err != nil {
			t.Errorf("source %s: %v", source, err)
		}
	}
	// Four statuses other than running, so the one-running rule is not in play.
	for _, status := range []string{"pending", "succeeded", "failed", "cancelled"} {
		if _, err := pool.Exec(ctx, `INSERT INTO tasks (user_id, workflow_id, source, status) VALUES ($1, 'noop', 'api', $2)`, user, status); err != nil {
			t.Errorf("status %s: %v", status, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tasks (user_id, workflow_id, source, status) VALUES ($1, 'noop', 'api', 'running')`, user); err != nil {
		t.Errorf("status running: %v", err)
	}
}

func TestTaskDefaults(t *testing.T) {
	pool := dbtest.Pool(t)
	user := newUser(t, pool)
	var status string
	var payload string
	var hasResult, hasStarted bool
	err := pool.QueryRow(context.Background(),
		`INSERT INTO tasks (user_id, workflow_id, source) VALUES ($1, 'noop', 'api')
		 RETURNING status, payload::text, result IS NOT NULL, started_at IS NOT NULL`, user).Scan(&status, &payload, &hasResult, &hasStarted)
	if err != nil {
		t.Fatal(err)
	}
	if status != "pending" || payload != "{}" || hasResult || hasStarted {
		t.Errorf("defaults: status=%q payload=%q result=%v started=%v", status, payload, hasResult, hasStarted)
	}
}

func TestTelegramIDIsUniqueButMayBeAbsentTwice(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(ctx, `INSERT INTO users (display_name) VALUES ('no telegram')`); err != nil {
			t.Fatalf("two users without a telegram id must be allowed: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (display_name, telegram_id) VALUES ('a', 42)`); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO users (display_name, telegram_id) VALUES ('b', 42)`)
	mustPgError(t, err, "23505")
}

func TestOnlyOneTaskMayBeRunning(t *testing.T) {
	pool := dbtest.Pool(t)
	user := newUser(t, pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO tasks (user_id, workflow_id, source, status) VALUES ($1, 'a', 'api', 'running')`, user); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO tasks (user_id, workflow_id, source, status) VALUES ($1, 'b', 'api', 'running')`, user)
	pgErr := mustPgError(t, err, "23505")
	if !strings.Contains(pgErr.ConstraintName, "one_running") {
		t.Errorf("constraint = %q", pgErr.ConstraintName)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tasks (user_id, workflow_id, source, status) VALUES ($1, 'c', 'api', 'pending')`, user); err != nil {
		t.Errorf("a pending task beside a running one is fine: %v", err)
	}
}

func TestSchemaFileIsTheOnlyDDL(t *testing.T) {
	// The schema creates exactly these tables and indexes, so a table added
	// elsewhere (or an index forgotten here) shows up as a failing test.
	pool := dbtest.Pool(t)
	rows, err := pool.Query(context.Background(),
		`SELECT tablename FROM pg_tables WHERE schemaname = current_schema() ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	if fmt.Sprint(tables) != "[conversations llm_calls messages task_steps tasks users]" {
		t.Errorf("tables = %v", tables)
	}
}

func TestLLMCallsTableHasTheAgreedColumns(t *testing.T) {
	pool := dbtest.Pool(t)
	want := []column{
		{"id", "uuid", false, true},
		{"task_id", "uuid", true, false},
		{"user_id", "uuid", false, false},
		{"model_id", "text", false, false},
		{"provider_id", "text", false, false},
		{"model_name", "text", false, false},
		{"tags", "ARRAY", false, true},
		{"input_tokens", "integer", false, true},
		{"output_tokens", "integer", false, true},
		{"cost_usd", "numeric", false, true},
		{"started_at", "timestamp with time zone", false, false},
		{"duration_ms", "integer", false, false},
		{"outcome", "text", false, false},
		{"error", "text", true, false},
	}
	got := columnsOf(t, pool, "llm_calls")
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("llm_calls columns:\n got %v\nwant %v", got, want)
	}
}

func TestLLMCallConstraintsRejectBadRows(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	user := newUser(t, pool)
	insert := `INSERT INTO llm_calls (user_id, model_id, provider_id, model_name, started_at, duration_ms, outcome%s) VALUES (%s, 'm', 'p', 'n', now(), 5, %s%s)`
	row := func(userExpr, outcome, extraCols, extraVals string) string {
		return fmt.Sprintf(insert, extraCols, userExpr, outcome, extraVals)
	}
	good := "'" + user + "'"
	tests := []struct {
		name, sql, code, constraint string
	}{
		{"unknown outcome", row(good, "'exploded'", "", ""), "23514", "llm_calls_outcome_check"},
		{"negative input tokens", row(good, "'ok'", ", input_tokens", ", -1"), "23514", "llm_calls_input_tokens_check"},
		{"negative output tokens", row(good, "'ok'", ", output_tokens", ", -1"), "23514", "llm_calls_output_tokens_check"},
		{"negative cost", row(good, "'ok'", ", cost_usd", ", -0.01"), "23514", "llm_calls_cost_usd_check"},
		{"negative duration", strings.Replace(row(good, "'ok'", "", ""), ", 5,", ", -5,", 1), "23514", "llm_calls_duration_ms_check"},
		{"unknown user", row("gen_random_uuid()", "'ok'", "", ""), "23503", "llm_calls_user_id_fkey"},
		{"unknown task", row(good, "'ok'", ", task_id", ", gen_random_uuid()"), "23503", "llm_calls_task_id_fkey"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, tc.sql)
			pgErr := mustPgError(t, err, tc.code)
			if pgErr.ConstraintName != tc.constraint {
				t.Errorf("constraint = %q, want %q", pgErr.ConstraintName, tc.constraint)
			}
		})
	}
	if _, err := pool.Exec(ctx, row(good, "'ok'", "", "")); err != nil {
		t.Errorf("a call without a task is allowed: %v", err)
	}
	for _, outcome := range []string{"ok", "error", "timeout", "rate_limited", "canceled"} {
		if _, err := pool.Exec(ctx, row(good, "'"+outcome+"'", "", "")); err != nil {
			t.Errorf("outcome %s: %v", outcome, err)
		}
	}
}

func TestLLMCallDefaults(t *testing.T) {
	pool := dbtest.Pool(t)
	user := newUser(t, pool)
	var tags string
	var in, out int
	var cost string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO llm_calls (user_id, model_id, provider_id, model_name, started_at, duration_ms, outcome)
		 VALUES ($1, 'm', 'p', 'n', now(), 1, 'ok') RETURNING tags::text, input_tokens, output_tokens, cost_usd::text`, user).Scan(&tags, &in, &out, &cost)
	if err != nil {
		t.Fatal(err)
	}
	if tags != "{}" || in != 0 || out != 0 || cost != "0.00000000" {
		t.Errorf("defaults: tags=%q in=%d out=%d cost=%s", tags, in, out, cost)
	}
}

func TestTaskStepsTableHasTheAgreedColumns(t *testing.T) {
	pool := dbtest.Pool(t)
	want := []column{
		{"id", "bigint", false, true},
		{"task_id", "uuid", false, false},
		{"step", "integer", false, false},
		{"attempt", "integer", false, false},
		{"state", "text", false, false},
		{"outcome", "text", false, false},
		{"error", "text", true, false},
		{"action", "jsonb", true, false},
		{"patch", "jsonb", true, false},
		{"tool", "text", true, false},
		{"tool_result", "text", true, false},
		{"reply", "text", true, false},
		{"input_tokens", "integer", false, true},
		{"output_tokens", "integer", false, true},
		{"duration_ms", "integer", false, true},
		{"at", "timestamp with time zone", false, false},
		{"workflow_id", "text", true, false},
		{"depth", "integer", false, true},
	}
	got := columnsOf(t, pool, "task_steps")
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("task_steps columns:\n got %v\nwant %v", got, want)
	}
}

func TestATaskCanBeWaitingButNotAnythingElseUnknown(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (display_name) VALUES ('Ada') RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	insert := func(status string) error {
		_, err := pool.Exec(ctx, `INSERT INTO tasks (user_id, workflow_id, source, status) VALUES ($1::uuid, 'w', 'api', $2)`, userID, status)
		return err
	}
	if err := insert("waiting"); err != nil {
		t.Errorf("waiting must be a status: %v", err)
	}
	if err := insert("paused"); err == nil {
		t.Error("an unknown status must be refused")
	}
}

// A database created before the engine was connected to the worker has a tasks
// table without the run columns and without the waiting status; the migration
// must bring it up to date and keep its rows.
func TestMigrateUpgradesATasksTableFromBeforeTheEngine(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`ALTER TABLE tasks DROP COLUMN run, DROP COLUMN ask, DROP COLUMN resume_message`,
		`ALTER TABLE tasks DROP CONSTRAINT tasks_status_check`,
		`ALTER TABLE tasks ADD CONSTRAINT tasks_status_check CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled'))`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	var userID string
	pool.QueryRow(ctx, `INSERT INTO users (display_name) VALUES ('Ada') RETURNING id::text`).Scan(&userID)
	if _, err := pool.Exec(ctx, `INSERT INTO tasks (user_id, workflow_id, source) VALUES ($1::uuid, 'old', 'api')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tasks SET status = 'waiting'`); err == nil {
		t.Fatal("the old table must refuse waiting, or this test proves nothing")
	}

	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tasks SET status = 'waiting', run = '{}', ask = '{}', resume_message = '1'`); err != nil {
		t.Errorf("the upgraded table must hold a waiting run: %v", err)
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE workflow_id = 'old'`).Scan(&n)
	if n != 1 {
		t.Errorf("the migration must keep the rows: %d", n)
	}
}

func TestConversationsAndMessagesTablesHaveTheAgreedColumns(t *testing.T) {
	pool := dbtest.Pool(t)
	wantConversations := []column{
		{"id", "uuid", false, true},
		{"user_id", "uuid", false, false},
		{"workflow_id", "text", false, false},
		{"title", "text", false, true},
		{"task_id", "uuid", true, false},
		{"telegram_chat_id", "bigint", true, false},
		{"created_at", "timestamp with time zone", false, true},
	}
	if got := columnsOf(t, pool, "conversations"); fmt.Sprint(got) != fmt.Sprint(wantConversations) {
		t.Errorf("conversations columns:\n got %v\nwant %v", got, wantConversations)
	}
	wantMessages := []column{
		{"id", "bigint", false, true},
		{"conversation_id", "uuid", false, false},
		{"role", "text", false, false},
		{"kind", "text", false, true},
		{"text", "text", false, false},
		{"task_id", "uuid", true, false},
		{"ui", "jsonb", true, false},
		{"created_at", "timestamp with time zone", false, true},
	}
	if got := columnsOf(t, pool, "messages"); fmt.Sprint(got) != fmt.Sprint(wantMessages) {
		t.Errorf("messages columns:\n got %v\nwant %v", got, wantMessages)
	}
}

func TestRemovingATaskOnlyClearsTheConversationsRunAndRemovingAConversationTakesItsMessages(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	var userID, taskID, convID string
	pool.QueryRow(ctx, `INSERT INTO users (display_name) VALUES ('Ada') RETURNING id::text`).Scan(&userID)
	pool.QueryRow(ctx, `INSERT INTO tasks (user_id, workflow_id, source) VALUES ($1::uuid, 'w', 'api') RETURNING id::text`, userID).Scan(&taskID)
	pool.QueryRow(ctx, `INSERT INTO conversations (user_id, workflow_id, task_id) VALUES ($1::uuid, 'w', $2::uuid) RETURNING id::text`, userID, taskID).Scan(&convID)
	pool.Exec(ctx, `INSERT INTO messages (conversation_id, role, text, task_id) VALUES ($1::uuid, 'user', 'hi', $2::uuid)`, convID, taskID)

	if _, err := pool.Exec(ctx, `DELETE FROM tasks WHERE id = $1::uuid`, taskID); err != nil {
		t.Fatalf("a task in a conversation must be removable: %v", err)
	}
	var task *string
	pool.QueryRow(ctx, `SELECT task_id::text FROM conversations WHERE id = $1::uuid`, convID).Scan(&task)
	var messages int
	pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE conversation_id = $1::uuid`, convID).Scan(&messages)
	if task != nil || messages != 1 {
		t.Errorf("the run is cleared (%v) and the history stays (%d messages)", task, messages)
	}
	pool.Exec(ctx, `DELETE FROM conversations WHERE id = $1::uuid`, convID)
	pool.QueryRow(ctx, `SELECT count(*) FROM messages`).Scan(&messages)
	if messages != 0 {
		t.Errorf("a conversation's messages go with it: %d", messages)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO messages (conversation_id, role, text) VALUES (gen_random_uuid(), 'user', 'x')`); err == nil {
		t.Error("a message needs a conversation")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO conversations (user_id, workflow_id) VALUES ($1::uuid, 'w')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO messages (conversation_id, role, text) SELECT id, 'robot', 'x' FROM conversations`); err == nil {
		t.Error("only user and assistant may speak")
	}
}
