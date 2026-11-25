package ports

import (
	"context"
	"io"
	"time"

	"bro-bot/internal/effort"
)

// AgentFramework определяет контракт для работы с любым CLI агентов
type AgentFramework interface {
	// ExecuteTask запускает процесс выполнения задачи агентом
	ExecuteTask(ctx context.Context, args ExecuteArgs) (AgentProcess, error)
	// GetModels возвращает сырой JSON или строку со списком моделей
	GetModels(ctx context.Context) ([]byte, error)
	// GetQuota возвращает сырой JSON для квоты. lang — язык интерфейса: API-адаптеры
	// собирают сводку сами, и её текст должен быть на языке пользователя; CLI-адаптеры
	// показывают ответ своего инструмента и параметр игноруют.
	GetQuota(ctx context.Context, lang string) ([]byte, error)
	// GetQuotaText возвращает текстовое представление квоты на языке lang.
	GetQuotaText(ctx context.Context, lang string) ([]byte, error)
	// GetCredits возвращает сырой JSON для кредитов
	GetCredits(ctx context.Context) ([]byte, error)
}

// ModeAwareFramework позволяет адаптеру явно сообщить свой режим выполнения ("mcp", "api" или "acp").
// Адаптеры, реализующие этот интерфейс, защищены от переиспользования в несовместимом режиме.
type ModeAwareFramework interface {
	ExecutionMode() string
}

// EffortAwareFramework позволяет адаптеру сообщить, применяет ли он уровень усилия
// рассуждений (ExecuteArgs.Effort). Адаптеры, не реализующие интерфейс, считаются
// поддерживающими уровень.
type EffortAwareFramework interface {
	SupportsEffort() bool
}

// ChatMessage — одна реплика диалога, передаваемая агенту для восстановления контекста.
type ChatMessage struct {
	Role    string // "user" or "assistant"
	Content string
}

// BotToolResult — результат вызова инструмента бота (через API-адаптер).
type BotToolResult struct {
	Text    string
	IsError bool
	// EndTurn сигнализирует API-адаптеру завершить tool-loop после текущего хода
	// (например, когда агент вызвал ask_user и ждёт ответа человека).
	EndTurn bool
}

// BotToolHandler выполняет инструменты бота напрямую в памяти процесса бота
// (используется API-адаптерами, у которых нет дочернего процесса mcp-serve).
type BotToolHandler interface {
	CallBotTool(ctx context.Context, name string, args map[string]interface{}) BotToolResult
}

// PermissionDecision — исход проверки права на выполнение инструмента агента.
type PermissionDecision int

const (
	PermissionDeny PermissionDecision = iota
	PermissionAllowOnce
	PermissionAllowAlways // одобрение на текущий ход/шаг вне SCRIPTS_DIR (в ACP всё равно уходит allow_once)
)

// PermissionRequest описывает вызов инструмента, требующий проверки политики доступа.
type PermissionRequest struct {
	ToolName string
	Kind     string
	Title    string
	Command  string
	Paths    []string
	WorkDir  string
	RawInput map[string]any
}

// PermissionHandler проверяет допустимость вызова инструмента (в /chat и /plan при режимах ask/allow).
type PermissionHandler interface {
	DecidePermission(ctx context.Context, req PermissionRequest) (PermissionDecision, string)
}

// ExecuteArgs содержит аргументы для запуска агента
type ExecuteArgs struct {
	ConversationID string
	ModelName      string
	Prompt         string
	WorkDir        string
	// History — предыдущие реплики диалога. Используется только API-адаптерами:
	// CLI-агенты хранят историю сами (agy --conversation, claude --resume) и это поле игноруют.
	History []ChatMessage
	// SystemPrompt — системная инструкция (правила проекта AGENTS.md или преамбула диалогового режима).
	SystemPrompt string
	// BotInstructions — системные инструкции самого бота по его инструментам и
	// правилам взаимодействия (вопросы, файлы, прогресс). Передаются агенту
	// на первом шаге независимо от наличия локального AGENTS.md в WorkDir.
	BotInstructions string
	// ReadOnly указывает, что агенту разрешены только операции чтения файлов (диалоговый режим).
	ReadOnly bool
	// ScriptsDir — каталог пользовательских скриптов (/script, SCRIPTS_DIR).
	// Запуск скриптов из него не проходит тихий read-only и всегда требует подтверждения;
	// запись в него требует подтверждения в режиме ask и разрешена сразу в allow.
	ScriptsDir string
	// PermissionHandler задаёт динамический контроль прав на вызовы инструментов.
	// Когда nil — ReadOnly=true означает строгий read-only (режим deny).
	PermissionHandler PermissionHandler
	// Effort — уровень усилия рассуждений (команда /effort). Нулевое значение
	// effort.Auto означает, что уровень агенту не передаётся.
	Effort effort.Level
	// Timeout — ограничение времени на один ход диалога или шаг задачи.
	// Адаптеры CLI с собственным флагом таймаута (например, agy --print-timeout)
	// передают его процессу; при значении <= 0 адаптер использует значение по умолчанию.
	Timeout time.Duration
	// ChannelToken — одноразовый токен шага для привязки дочернего mcp-serve
	// к каналу задачи/диалога (резерв к определению по дереву PID).
	ChannelToken string
	// BotTools — прямой мост инструментов бота для api-режима (nil для CLI/MCP/ACP).
	BotTools BotToolHandler
}

// AgentProcess инкапсулирует запущенный процесс агента.
//
// Домен работает только с этим интерфейсом: как именно останавливается агент — сигналом
// группе процессов у CLI или закрытием pipe и отменой контекста у API — решает адаптер.
type AgentProcess interface {
	Stdout() io.Reader
	Stdin() io.WriteCloser
	Wait() error
	// Kill останавливает агента вместе с его дочерними процессами.
	Kill() error
	Close() error
	// PID возвращает идентификатор процесса в ОС или 0, если процесса нет (api-режим).
	// Нужен только для отчёта о ресурсах.
	PID() int
}
