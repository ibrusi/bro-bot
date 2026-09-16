package storage

import (
	"context"
	"time"
)

// TaskRecord представляет запись задачи в базе данных SQLite.
type TaskRecord struct {
	ID              int
	Project         string
	Model           string
	InitialPrompt   string
	CurrentPrompt   string
	Status          string
	RequiresPlan    bool
	Plan            string
	PlanApproved    bool
	StartedAt       time.Time
	FinishedAt      time.Time
	CreatedAt       time.Time
	LastPRURL       string
	ConversationID  string
	LastQuestion    string
	QuestionOptions []string
	QuestionAskedAt time.Time
	RecipientID     string
	LastModelUsed   string
	LastTokensUsed  string
}

// TokenMetricsRecord представляет сохраненные метрики токенов задачи.
type TokenMetricsRecord struct {
	TaskID          int
	InputTokens     int64
	OutputTokens    int64
	ThinkingTokens  int64
	CacheReadTokens int64
	TotalTokens     int64
	DurationSeconds float64
	Turns           int
	ToolCallsCount  int
	Model           string
	PRURL           string
	ConversationID  string
	CreatedAt       time.Time

	LastStepInputTokens     int64
	LastStepOutputTokens    int64
	LastStepThinkingTokens  int64
	LastStepCacheReadTokens int64
	LastStepTotalTokens     int64
}

// AggregateMetrics содержит суммарную статистику всех исторических задач.
type AggregateMetrics struct {
	TotalTasks      int
	TotalTokens     int64
	InputTokens     int64
	OutputTokens    int64
	ThinkingTokens  int64
	CacheReadTokens int64
	TotalDuration   float64
}

// Storage определяет интерфейс персистентного хранилища данных бота.
type Storage interface {
	// Tasks
	CreateTask(ctx context.Context, task *TaskRecord) (int, error)
	UpdateTask(ctx context.Context, task *TaskRecord) error
	GetTask(ctx context.Context, id int) (*TaskRecord, error)
	ListTasks(ctx context.Context) ([]*TaskRecord, error)
	UpdateTaskStatus(ctx context.Context, id int, status string) error
	UpdateTaskPlan(ctx context.Context, id int, plan string, approved bool) error
	UpdateTaskFinished(ctx context.Context, id int, status string, finishedAt time.Time, prURL string) error
	UpdateTaskConversationID(ctx context.Context, id int, conversationID string) error

	// Followups
	AddFollowup(ctx context.Context, taskID int, text string, orderIndex int) error
	GetFollowups(ctx context.Context, taskID int) ([]string, error)
	ClearFollowups(ctx context.Context, taskID int) error

	// Logs
	AppendLog(ctx context.Context, taskID int, line string) error
	GetRecentLogs(ctx context.Context, taskID int, limit int) ([]string, error)

	// Metrics
	SaveMetrics(ctx context.Context, m *TokenMetricsRecord) error
	GetMetrics(ctx context.Context, taskID int) (*TokenMetricsRecord, error)
	GetAggregateMetrics(ctx context.Context) (*AggregateMetrics, error)

	// Messenger message mapping
	RegisterMessageTask(ctx context.Context, chatID, messageID string, taskID int) error
	GetTaskIDByMessage(ctx context.Context, messageID string) (int, error)
	ListAllMessageTasks(ctx context.Context) (map[string]int, error)

	// Settings
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error

	// Recovery
	RecoverInterruptedTasks(ctx context.Context) ([]int, error)

	Close() error
}
