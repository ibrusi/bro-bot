package ports

import (
	"context"
	"io"
)

// AgentFramework определяет контракт для работы с любым CLI агентов
type AgentFramework interface {
	// ExecuteTask запускает процесс выполнения задачи агентом
	ExecuteTask(ctx context.Context, args ExecuteArgs) (AgentProcess, error)
	// GetModels возвращает сырой JSON или строку со списком моделей
	GetModels(ctx context.Context) ([]byte, error)
	// GetQuota возвращает сырой JSON для квоты
	GetQuota(ctx context.Context) ([]byte, error)
	// GetQuotaText возвращает текстовое представление квоты
	GetQuotaText(ctx context.Context) ([]byte, error)
	// GetCredits возвращает сырой JSON для кредитов
	GetCredits(ctx context.Context) ([]byte, error)
}

// ChatMessage — одна реплика диалога, передаваемая агенту для восстановления контекста.
type ChatMessage struct {
	Role    string // "user" или "assistant"
	Content string
}

// ExecuteArgs содержит аргументы для запуска агента
type ExecuteArgs struct {
	ConversationID string
	ModelName      string
	Prompt         string
	WorkDir        string
	// History — предыдущие реплики диалога. Используется только API-адаптерами:
	// CLI-агенты хранят историю сами (agy --conversation, claude --resume) и это поле игнорируют.
	History []ChatMessage
	// SystemPrompt — системная инструкция (например, преамбула диалогового режима).
	// Также только для API-адаптеров: у CLI-агентов преамбула подмешивается в текст промпта.
	SystemPrompt string
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
