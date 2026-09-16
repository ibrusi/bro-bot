package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStorage реализует интерфейс Storage на базе SQLite.
type SQLiteStorage struct {
	db       *sql.DB
	writeMu  sync.Mutex
	isMemory bool
}

// NewSQLiteStorage открывает или создаёт базу данных SQLite по указанному пути,
// применяет системные PRAGMA и накатывает миграции.
func NewSQLiteStorage(dbPath string) (*SQLiteStorage, error) {
	isMemory := dbPath == ":memory:" || strings.HasPrefix(dbPath, "file::memory:")

	if !isMemory {
		dir := filepath.Dir(dbPath)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create database directory %s: %w", dir, err)
			}
		}
	}

	dsn := dbPath
	if !isMemory && !strings.Contains(dsn, "?") {
		dsn += "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	if isMemory {
		// Для in-memory базы нужно удерживать ровно 1 соединение, иначе при закрытии БД сбрасывается
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(10)
		db.SetMaxIdleConns(5)
		db.SetConnMaxLifetime(time.Hour)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Применяем базовые прагмы явно
	pragmas := []string{
		"PRAGMA foreign_keys = ON;",
		"PRAGMA busy_timeout = 5000;",
	}
	if !isMemory {
		pragmas = append(pragmas, "PRAGMA journal_mode = WAL;", "PRAGMA synchronous = NORMAL;")
	}

	for _, p := range pragmas {
		if _, err := db.ExecContext(ctx, p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("failed to execute pragma %s: %w", p, err)
		}
	}

	if err := runMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to run database migrations: %w", err)
	}

	return &SQLiteStorage{
		db:       db,
		isMemory: isMemory,
	}, nil
}

// Close закрывает соединение с БД.
func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}

// CreateTask вставляет новую задачу в БД.
func (s *SQLiteStorage) CreateTask(ctx context.Context, task *TaskRecord) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	optsJSON, err := json.Marshal(task.QuestionOptions)
	if err != nil {
		optsJSON = []byte("[]")
	}

	query := `
		INSERT INTO tasks (
			project, model, initial_prompt, current_prompt, status,
			requires_plan, plan, plan_approved, started_at, finished_at,
			last_pr_url, conversation_id, last_question, question_options,
			question_asked_at, recipient_id, last_model_used, last_tokens_used
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`

	var startedAt, finishedAt, questionAskedAt interface{}
	if !task.StartedAt.IsZero() {
		startedAt = task.StartedAt
	}
	if !task.FinishedAt.IsZero() {
		finishedAt = task.FinishedAt
	}
	if !task.QuestionAskedAt.IsZero() {
		questionAskedAt = task.QuestionAskedAt
	}

	res, err := s.db.ExecContext(ctx, query,
		task.Project,
		task.Model,
		task.InitialPrompt,
		task.CurrentPrompt,
		task.Status,
		task.RequiresPlan,
		task.Plan,
		task.PlanApproved,
		startedAt,
		finishedAt,
		task.LastPRURL,
		task.ConversationID,
		task.LastQuestion,
		string(optsJSON),
		questionAskedAt,
		task.RecipientID,
		task.LastModelUsed,
		task.LastTokensUsed,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert task: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get last insert id: %w", err)
	}
	task.ID = int(id)
	return int(id), nil
}

// UpdateTask обновляет все изменяемые поля задачи в БД.
func (s *SQLiteStorage) UpdateTask(ctx context.Context, task *TaskRecord) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	optsJSON, err := json.Marshal(task.QuestionOptions)
	if err != nil {
		optsJSON = []byte("[]")
	}

	query := `
		UPDATE tasks SET
			project = ?,
			model = ?,
			initial_prompt = ?,
			current_prompt = ?,
			status = ?,
			requires_plan = ?,
			plan = ?,
			plan_approved = ?,
			started_at = ?,
			finished_at = ?,
			last_pr_url = ?,
			conversation_id = ?,
			last_question = ?,
			question_options = ?,
			question_asked_at = ?,
			recipient_id = ?,
			last_model_used = ?,
			last_tokens_used = ?
		WHERE id = ?;
	`

	var startedAt, finishedAt, questionAskedAt interface{}
	if !task.StartedAt.IsZero() {
		startedAt = task.StartedAt
	}
	if !task.FinishedAt.IsZero() {
		finishedAt = task.FinishedAt
	}
	if !task.QuestionAskedAt.IsZero() {
		questionAskedAt = task.QuestionAskedAt
	}

	_, err = s.db.ExecContext(ctx, query,
		task.Project,
		task.Model,
		task.InitialPrompt,
		task.CurrentPrompt,
		task.Status,
		task.RequiresPlan,
		task.Plan,
		task.PlanApproved,
		startedAt,
		finishedAt,
		task.LastPRURL,
		task.ConversationID,
		task.LastQuestion,
		string(optsJSON),
		questionAskedAt,
		task.RecipientID,
		task.LastModelUsed,
		task.LastTokensUsed,
		task.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update task #%d: %w", task.ID, err)
	}
	return nil
}

// UpdateTaskStatus атомарно обновляет статус задачи.
func (s *SQLiteStorage) UpdateTaskStatus(ctx context.Context, id int, status string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = ? WHERE id = ?`, status, id)
	return err
}

// UpdateTaskPlan обновляет план задачи и признак утверждения.
func (s *SQLiteStorage) UpdateTaskPlan(ctx context.Context, id int, plan string, approved bool) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET plan = ?, plan_approved = ? WHERE id = ?`, plan, approved, id)
	return err
}

// UpdateTaskFinished фиксирует завершение задачи.
func (s *SQLiteStorage) UpdateTaskFinished(ctx context.Context, id int, status string, finishedAt time.Time, prURL string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = ?, finished_at = ?, last_pr_url = ? WHERE id = ?`,
		status, finishedAt, prURL, id)
	return err
}

// UpdateTaskConversationID немедленно сохраняет идентификатор сессии agy для задачи.
func (s *SQLiteStorage) UpdateTaskConversationID(ctx context.Context, id int, conversationID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET conversation_id = ? WHERE id = ?`, conversationID, id)
	return err
}

func scanTaskRow(scanner interface {
	Scan(dest ...interface{}) error
}) (*TaskRecord, error) {
	var (
		task            TaskRecord
		startedAt       sql.NullTime
		finishedAt      sql.NullTime
		questionAskedAt sql.NullTime
		optsJSON        string
	)

	err := scanner.Scan(
		&task.ID,
		&task.Project,
		&task.Model,
		&task.InitialPrompt,
		&task.CurrentPrompt,
		&task.Status,
		&task.RequiresPlan,
		&task.Plan,
		&task.PlanApproved,
		&startedAt,
		&finishedAt,
		&task.CreatedAt,
		&task.LastPRURL,
		&task.ConversationID,
		&task.LastQuestion,
		&optsJSON,
		&questionAskedAt,
		&task.RecipientID,
		&task.LastModelUsed,
		&task.LastTokensUsed,
	)
	if err != nil {
		return nil, err
	}

	if startedAt.Valid {
		task.StartedAt = startedAt.Time
	}
	if finishedAt.Valid {
		task.FinishedAt = finishedAt.Time
	}
	if questionAskedAt.Valid {
		task.QuestionAskedAt = questionAskedAt.Time
	}
	if optsJSON != "" {
		_ = json.Unmarshal([]byte(optsJSON), &task.QuestionOptions)
	}

	return &task, nil
}

const taskSelectFields = `
	id, project, model, initial_prompt, current_prompt, status,
	requires_plan, plan, plan_approved, started_at, finished_at,
	created_at, last_pr_url, conversation_id, last_question,
	question_options, question_asked_at, recipient_id, last_model_used,
	last_tokens_used
`

// GetTask возвращает задачу по ID.
func (s *SQLiteStorage) GetTask(ctx context.Context, id int) (*TaskRecord, error) {
	query := fmt.Sprintf(`SELECT %s FROM tasks WHERE id = ?`, taskSelectFields)
	row := s.db.QueryRowContext(ctx, query, id)
	task, err := scanTaskRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get task #%d: %w", id, err)
	}
	return task, nil
}

// ListTasks возвращает все задачи в хронологическом порядке.
func (s *SQLiteStorage) ListTasks(ctx context.Context) ([]*TaskRecord, error) {
	query := fmt.Sprintf(`SELECT %s FROM tasks ORDER BY id ASC`, taskSelectFields)
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*TaskRecord
	for rows.Next() {
		task, err := scanTaskRow(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan task: %w", err)
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// AddFollowup добавляет правку к задаче.
func (s *SQLiteStorage) AddFollowup(ctx context.Context, taskID int, text string, orderIndex int) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO task_followups (task_id, text, order_index) VALUES (?, ?, ?)`,
		taskID, text, orderIndex)
	return err
}

// GetFollowups возвращает список правок задачи в порядке добавления.
func (s *SQLiteStorage) GetFollowups(ctx context.Context, taskID int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT text FROM task_followups WHERE task_id = ? ORDER BY order_index ASC, id ASC`,
		taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var followups []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, err
		}
		followups = append(followups, text)
	}
	return followups, rows.Err()
}

// ClearFollowups удаляет все правки для задачи.
func (s *SQLiteStorage) ClearFollowups(ctx context.Context, taskID int) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.ExecContext(ctx, `DELETE FROM task_followups WHERE task_id = ?`, taskID)
	return err
}

// AppendLog добавляет строку лога для задачи.
func (s *SQLiteStorage) AppendLog(ctx context.Context, taskID int, line string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.ExecContext(ctx, `INSERT INTO task_logs (task_id, log_line) VALUES (?, ?)`, taskID, line)
	return err
}

// GetRecentLogs возвращает последние N строк лога задачи.
func (s *SQLiteStorage) GetRecentLogs(ctx context.Context, taskID int, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 20
	}

	query := `
		SELECT log_line FROM (
			SELECT id, log_line FROM task_logs WHERE task_id = ? ORDER BY id DESC LIMIT ?
		) ORDER BY id ASC;
	`
	rows, err := s.db.QueryContext(ctx, query, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		logs = append(logs, line)
	}
	return logs, rows.Err()
}

// SaveMetrics сохраняет метрики токенов задачи.
func (s *SQLiteStorage) SaveMetrics(ctx context.Context, m *TokenMetricsRecord) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	query := `
		INSERT INTO task_metrics (
			task_id, input_tokens, output_tokens, thinking_tokens, cache_read_tokens,
			total_tokens, duration_seconds, turns, tool_calls_count, model, pr_url, conversation_id,
			last_step_input_tokens, last_step_output_tokens, last_step_thinking_tokens,
			last_step_cache_read_tokens, last_step_total_tokens
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET
			input_tokens = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			thinking_tokens = excluded.thinking_tokens,
			cache_read_tokens = excluded.cache_read_tokens,
			total_tokens = excluded.total_tokens,
			duration_seconds = excluded.duration_seconds,
			turns = excluded.turns,
			tool_calls_count = excluded.tool_calls_count,
			model = excluded.model,
			pr_url = excluded.pr_url,
			conversation_id = excluded.conversation_id,
			last_step_input_tokens = excluded.last_step_input_tokens,
			last_step_output_tokens = excluded.last_step_output_tokens,
			last_step_thinking_tokens = excluded.last_step_thinking_tokens,
			last_step_cache_read_tokens = excluded.last_step_cache_read_tokens,
			last_step_total_tokens = excluded.last_step_total_tokens;
	`

	_, err := s.db.ExecContext(ctx, query,
		m.TaskID,
		m.InputTokens,
		m.OutputTokens,
		m.ThinkingTokens,
		m.CacheReadTokens,
		m.TotalTokens,
		m.DurationSeconds,
		m.Turns,
		m.ToolCallsCount,
		m.Model,
		m.PRURL,
		m.ConversationID,
		m.LastStepInputTokens,
		m.LastStepOutputTokens,
		m.LastStepThinkingTokens,
		m.LastStepCacheReadTokens,
		m.LastStepTotalTokens,
	)
	return err
}

// GetMetrics возвращает сохранённые метрики для задачи.
func (s *SQLiteStorage) GetMetrics(ctx context.Context, taskID int) (*TokenMetricsRecord, error) {
	query := `
		SELECT task_id, input_tokens, output_tokens, thinking_tokens, cache_read_tokens,
		       total_tokens, duration_seconds, turns, tool_calls_count, model, pr_url,
		       conversation_id, created_at,
		       last_step_input_tokens, last_step_output_tokens, last_step_thinking_tokens,
		       last_step_cache_read_tokens, last_step_total_tokens
		FROM task_metrics WHERE task_id = ?;
	`
	row := s.db.QueryRowContext(ctx, query, taskID)

	var m TokenMetricsRecord
	err := row.Scan(
		&m.TaskID,
		&m.InputTokens,
		&m.OutputTokens,
		&m.ThinkingTokens,
		&m.CacheReadTokens,
		&m.TotalTokens,
		&m.DurationSeconds,
		&m.Turns,
		&m.ToolCallsCount,
		&m.Model,
		&m.PRURL,
		&m.ConversationID,
		&m.CreatedAt,
		&m.LastStepInputTokens,
		&m.LastStepOutputTokens,
		&m.LastStepThinkingTokens,
		&m.LastStepCacheReadTokens,
		&m.LastStepTotalTokens,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &m, nil
}

// ExtractConversationLastStepUsage читает метрики последнего шага агента напрямую из БД agy.
func ExtractConversationLastStepUsage(convID string) (inputTokens, outputTokens, thinkingTokens, cacheReadTokens, totalTokens int64, ok bool) {
	if convID == "" {
		return 0, 0, 0, 0, 0, false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, 0, 0, 0, 0, false
	}
	dbPath := filepath.Join(home, ".gemini", "antigravity-cli", "conversations", convID+".db")
	if _, err := os.Stat(dbPath); err != nil {
		return 0, 0, 0, 0, 0, false
	}

	convDB, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return 0, 0, 0, 0, 0, false
	}
	defer convDB.Close()

	var meta []byte
	// Шаг агента (step_type = 15) с метаданными
	err = convDB.QueryRow("SELECT metadata FROM steps WHERE step_type = 15 AND metadata IS NOT NULL ORDER BY idx DESC LIMIT 1").Scan(&meta)
	if err != nil || len(meta) == 0 {
		return 0, 0, 0, 0, 0, false
	}

	return parseUsageFromStepMetadata(meta)
}

func parseUsageFromStepMetadata(meta []byte) (inputTokens, outputTokens, thinkingTokens, cacheReadTokens, totalTokens int64, ok bool) {
	i := 0
	for i < len(meta) {
		key, next, err := readVarint(meta, i)
		if err != nil {
			break
		}
		i = next
		fn := key >> 3
		wt := key & 7
		if wt == 0 {
			_, next, err := readVarint(meta, i)
			if err != nil {
				break
			}
			i = next
		} else if wt == 2 {
			length, next, err := readVarint(meta, i)
			if err != nil {
				break
			}
			i = next
			if i+int(length) > len(meta) {
				break
			}
			data := meta[i : i+int(length)]
			i += int(length)

			// field 9 is the UsageMetadata message
			if fn == 9 {
				j := 0
				for j < len(data) {
					k, nextJ, err := readVarint(data, j)
					if err != nil {
						break
					}
					j = nextJ
					subFn := k >> 3
					subWt := k & 7
					if subWt == 0 {
						v, nextJ, err := readVarint(data, j)
						if err != nil {
							break
						}
						j = nextJ
						switch subFn {
						case 2:
							inputTokens = int64(v)
						case 3:
							outputTokens = int64(v)
						case 5:
							cacheReadTokens = int64(v)
						case 10:
							thinkingTokens = int64(v)
						}
					} else if subWt == 2 {
						subLen, nextJ, err := readVarint(data, j)
						if err != nil {
							break
						}
						j = nextJ + int(subLen)
					} else if subWt == 1 {
						j += 8
					} else if subWt == 5 {
						j += 4
					} else {
						break
					}
				}
				totalTokens = inputTokens + outputTokens
				return inputTokens, outputTokens, thinkingTokens, cacheReadTokens, totalTokens, true
			}
		} else if wt == 1 {
			i += 8
		} else if wt == 5 {
			i += 4
		} else {
			break
		}
	}
	return 0, 0, 0, 0, 0, false
}

func readVarint(data []byte, offset int) (uint64, int, error) {
	var val uint64
	var shift uint
	for i := offset; i < len(data); i++ {
		b := data[i]
		val |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return val, i + 1, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, 0, fmt.Errorf("varint overflow")
		}
	}
	return 0, 0, fmt.Errorf("unexpected EOF reading varint")
}

// GetAggregateMetrics возвращает суммарные метрики по всем историческим задачам.
func (s *SQLiteStorage) GetAggregateMetrics(ctx context.Context) (*AggregateMetrics, error) {
	query := `
		SELECT
			COUNT(*),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(thinking_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(duration_seconds), 0)
		FROM task_metrics;
	`
	row := s.db.QueryRowContext(ctx, query)

	var agg AggregateMetrics
	err := row.Scan(
		&agg.TotalTasks,
		&agg.TotalTokens,
		&agg.InputTokens,
		&agg.OutputTokens,
		&agg.ThinkingTokens,
		&agg.CacheReadTokens,
		&agg.TotalDuration,
	)
	if err != nil {
		return nil, err
	}
	return &agg, nil
}

// RegisterMessageTask сохраняет соответствие сообщения мессенджера и ID задачи.
func (s *SQLiteStorage) RegisterMessageTask(ctx context.Context, chatID, messageID string, taskID int) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO messenger_messages (chat_id, message_id, task_id) VALUES (?, ?, ?)`,
		chatID, messageID, taskID)
	return err
}

// GetTaskIDByMessage возвращает ID задачи по ID сообщения.
func (s *SQLiteStorage) GetTaskIDByMessage(ctx context.Context, messageID string) (int, error) {
	var taskID int
	err := s.db.QueryRowContext(ctx, `SELECT task_id FROM messenger_messages WHERE message_id = ?`, messageID).Scan(&taskID)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	return taskID, nil
}

// ListAllMessageTasks возвращает всю карту соответствий message_id -> task_id.
func (s *SQLiteStorage) ListAllMessageTasks(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT message_id, task_id FROM messenger_messages`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := make(map[string]int)
	for rows.Next() {
		var msgID string
		var taskID int
		if err := rows.Scan(&msgID, &taskID); err != nil {
			return nil, err
		}
		res[msgID] = taskID
	}
	return res, rows.Err()
}

// GetSetting возвращает значение параметра бота.
func (s *SQLiteStorage) GetSetting(ctx context.Context, key string) (string, error) {
	var val string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM bot_settings WHERE key = ?`, key).Scan(&val)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return val, nil
}

// SetSetting сохраняет или обновляет значение параметра бота.
func (s *SQLiteStorage) SetSetting(ctx context.Context, key, value string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO bot_settings (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		key, value)
	return err
}

// RecoverInterruptedTasks находит задачи, оставшиеся в статусе running или planning после завершения
// предыдущего процесса, переводит их в статус paused и логирует предупреждение.
func (s *SQLiteStorage) RecoverInterruptedTasks(ctx context.Context) ([]int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	rows, err := s.db.QueryContext(ctx, `SELECT id FROM tasks WHERE status IN ('running', 'planning', 'waiting_input')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var recoveredIDs []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		recoveredIDs = append(recoveredIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, id := range recoveredIDs {
		_, _ = s.db.ExecContext(ctx, `UPDATE tasks SET status = 'paused' WHERE id = ?`, id)
		_, _ = s.db.ExecContext(ctx, `INSERT INTO task_logs (task_id, log_line) VALUES (?, ?)`,
			id, "⚠️ Выполнение прервано перезапуском бота. Задача переведена на паузу. Возобновить: /resume")
	}

	return recoveredIDs, nil
}
