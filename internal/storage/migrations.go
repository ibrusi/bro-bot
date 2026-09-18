package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log"
)

type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{
	{
		version: 1,
		name:    "initial_schema",
		sql: `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS bot_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project TEXT NOT NULL,
    model TEXT NOT NULL,
    initial_prompt TEXT NOT NULL,
    current_prompt TEXT NOT NULL,
    status TEXT NOT NULL,
    requires_plan BOOLEAN NOT NULL DEFAULT 0,
    plan TEXT NOT NULL DEFAULT '',
    plan_approved BOOLEAN NOT NULL DEFAULT 0,
    started_at DATETIME,
    finished_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_pr_url TEXT NOT NULL DEFAULT '',
    conversation_id TEXT NOT NULL DEFAULT '',
    last_question TEXT NOT NULL DEFAULT '',
    question_options TEXT NOT NULL DEFAULT '[]',
    question_asked_at DATETIME,
    recipient_id TEXT NOT NULL DEFAULT '',
    last_model_used TEXT NOT NULL DEFAULT '',
    last_tokens_used TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_tasks_project ON tasks(project);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_created_at ON tasks(created_at);

CREATE TABLE IF NOT EXISTS task_followups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    text TEXT NOT NULL,
    order_index INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_task_followups_task_id ON task_followups(task_id, order_index);

CREATE TABLE IF NOT EXISTS task_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    log_line TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_task_logs_task_id ON task_logs(task_id, id);

CREATE TABLE IF NOT EXISTS task_metrics (
    task_id INTEGER PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    thinking_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    duration_seconds REAL NOT NULL DEFAULT 0,
    turns INTEGER NOT NULL DEFAULT 0,
    tool_calls_count INTEGER NOT NULL DEFAULT 0,
    model TEXT NOT NULL DEFAULT '',
    pr_url TEXT NOT NULL DEFAULT '',
    conversation_id TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS telegram_messages (
    message_id INTEGER PRIMARY KEY,
    chat_id INTEGER NOT NULL DEFAULT 0,
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_telegram_messages_task_id ON telegram_messages(task_id);
`,
	},
	{
		version: 2,
		name:    "add_last_step_metrics_to_task_metrics",
		sql: `
ALTER TABLE task_metrics ADD COLUMN last_step_input_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE task_metrics ADD COLUMN last_step_output_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE task_metrics ADD COLUMN last_step_thinking_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE task_metrics ADD COLUMN last_step_cache_read_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE task_metrics ADD COLUMN last_step_total_tokens INTEGER NOT NULL DEFAULT 0;
`,
	},
	{
		version: 3,
		name:    "add_messenger_messages",
		sql: `
CREATE TABLE IF NOT EXISTS messenger_messages (
    chat_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (chat_id, message_id)
);

CREATE INDEX IF NOT EXISTS idx_messenger_messages_task_id ON messenger_messages(task_id);

INSERT OR IGNORE INTO messenger_messages (chat_id, message_id, task_id, created_at)
SELECT CAST(chat_id AS TEXT), CAST(message_id AS TEXT), task_id, created_at
FROM telegram_messages;
`,
	},
	{
		version: 4,
		name:    "add_agent_to_tasks",
		sql: `
ALTER TABLE tasks ADD COLUMN agent TEXT NOT NULL DEFAULT 'agy';
CREATE INDEX IF NOT EXISTS idx_tasks_agent ON tasks(agent);
`,
	},
	{
		version: 5,
		name:    "add_chat_sessions",
		sql: `
CREATE TABLE IF NOT EXISTS chat_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_sessions_active_project
    ON chat_sessions(project) WHERE active = 1;

-- Отдельный идентификатор сессии агента для каждой пары агент+режим:
-- сессии agy и claude, cli и api несовместимы между собой.
CREATE TABLE IF NOT EXISTS chat_conversations (
    session_id INTEGER NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
    agent TEXT NOT NULL,
    mode TEXT NOT NULL,
    conversation_id TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (session_id, agent, mode)
);

CREATE TABLE IF NOT EXISTS chat_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_chat_messages_session ON chat_messages(session_id, id);
`,
	},
}

func runMigrations(ctx context.Context, db *sql.DB) error {
	// Создаём таблицу schema_migrations, если ещё нет
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`)
	if err != nil {
		return fmt.Errorf("failed to ensure schema_migrations table: %w", err)
	}

	for _, m := range migrations {
		var exists int
		err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&exists)
		if err != nil {
			return fmt.Errorf("failed to check migration %d: %w", m.version, err)
		}
		if exists > 0 {
			continue
		}

		log.Printf("Applying database migration %d: %s...", m.version, m.name)

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin tx for migration %d: %w", m.version, err)
		}

		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to execute migration %d (%s): %w", m.version, m.name, err)
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name) VALUES (?, ?)`, m.version, m.name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to record migration %d: %w", m.version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit migration %d: %w", m.version, err)
		}
		log.Printf("Migration %d (%s) applied successfully", m.version, m.name)
	}

	backfillHistoricalTaskMetrics(ctx, db)

	return nil
}

func backfillHistoricalTaskMetrics(ctx context.Context, db *sql.DB) {
	rows, err := db.QueryContext(ctx, `
		SELECT task_id, conversation_id
		FROM task_metrics
		WHERE last_step_input_tokens = 0 AND last_step_output_tokens = 0 AND conversation_id != ''
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	type updateItem struct {
		taskID int
		in     int64
		out    int64
		think  int64
		cache  int64
		total  int64
	}
	var updates []updateItem

	for rows.Next() {
		var tid int
		var convID string
		if err := rows.Scan(&tid, &convID); err == nil && convID != "" {
			in, out, think, cache, total, ok := ExtractConversationLastStepUsage(convID)
			if ok {
				updates = append(updates, updateItem{
					taskID: tid,
					in:     in,
					out:    out,
					think:  think,
					cache:  cache,
					total:  total,
				})
			}
		}
	}

	for _, u := range updates {
		_, _ = db.ExecContext(ctx, `
			UPDATE task_metrics SET
				last_step_input_tokens = ?,
				last_step_output_tokens = ?,
				last_step_thinking_tokens = ?,
				last_step_cache_read_tokens = ?,
				last_step_total_tokens = ?
			WHERE task_id = ?
		`, u.in, u.out, u.think, u.cache, u.total, u.taskID)
	}
}
