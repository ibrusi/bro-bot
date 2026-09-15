package ports

import (
	"context"
	"io"
	"os/exec"
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

// ExecuteArgs содержит аргументы для запуска агента
type ExecuteArgs struct {
	ConversationID string
	ModelName      string
	Prompt         string
	WorkDir        string
}

// AgentProcess инкапсулирует запущенный процесс агента
type AgentProcess interface {
	Stdout() io.Reader
	Stdin() io.WriteCloser
	Wait() error
	Kill() error
	Close() error
	GetCmd() *exec.Cmd // Нужен для совместимости с текущим TaskSession.Cmd
}
