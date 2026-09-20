package domain

import (
	"bro-bot/internal/i18n"
	"strings"
	"sync"
	"time"
)

// AgentSession — устаревшее зеркало состояния активной задачи для отчётов и /status.
// Процесс агента здесь не хранится: им владеет TaskSession.
type AgentSession struct {
	sync.Mutex
	IsRunning        bool
	Waiting          bool
	StartedAt        time.Time
	CurrentPrompt    string
	CurrentProject   string
	RecentLogs       []string
	PendingFollowups []string
	LastPRURL        string
	FullOutput       strings.Builder
	LastModelUsed    string
	LastTokensUsed   string
}

type ProjectState struct {
	sync.RWMutex
	CurrentProject string
	CurrentModel   string
	CurrentAgent   string
	ExecutionMode  string // "cli" или "api"
	PlanMode       bool
	// InteractionMode задаёт, что делать с обычным сообщением без активной задачи:
	// "chat" — отвечать в диалоге, "task" — сразу создавать задачу.
	InteractionMode string
	// Language — зеркало выбранного языка для отладки и снимков состояния.
	// Источник истины — i18n.Active(): см. GetLanguage/SetLanguage.
	Language string
}

// GetCurrentAgent возвращает имя активного агента (по умолчанию agy).
func (ps *ProjectState) GetCurrentAgent() string {
	ps.RLock()
	defer ps.RUnlock()
	if ps.CurrentAgent == "" {
		return "agy"
	}
	return ps.CurrentAgent
}

// SetCurrentAgent обновляет имя активного агента.
func (ps *ProjectState) SetCurrentAgent(agent string) {
	ps.Lock()
	defer ps.Unlock()
	ps.CurrentAgent = agent
}

// GetExecutionMode возвращает текущий режим выполнения ("mcp", "cli" или "api", по умолчанию "mcp").
func (ps *ProjectState) GetExecutionMode() string {
	ps.RLock()
	defer ps.RUnlock()
	if ps.ExecutionMode == "" {
		return "mcp"
	}
	return ps.ExecutionMode
}

// SetExecutionMode обновляет режим выполнения.
func (ps *ProjectState) SetExecutionMode(mode string) {
	ps.Lock()
	defer ps.Unlock()
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "api":
		ps.ExecutionMode = "api"
	case "cli":
		ps.ExecutionMode = "cli"
	default:
		ps.ExecutionMode = "mcp"
	}
}

// Режимы взаимодействия с обычным текстовым сообщением.
const (
	InteractionModeChat = "chat"
	InteractionModeTask = "task"
)

// GetInteractionMode возвращает текущий режим взаимодействия (по умолчанию "chat").
func (ps *ProjectState) GetInteractionMode() string {
	ps.RLock()
	defer ps.RUnlock()
	if ps.InteractionMode == "" {
		return InteractionModeChat
	}
	return ps.InteractionMode
}

// SetInteractionMode обновляет режим взаимодействия.
func (ps *ProjectState) SetInteractionMode(mode string) {
	ps.Lock()
	defer ps.Unlock()
	if strings.ToLower(strings.TrimSpace(mode)) == InteractionModeTask {
		ps.InteractionMode = InteractionModeTask
	} else {
		ps.InteractionMode = InteractionModeChat
	}
}

// GetLanguage возвращает текущий язык интерфейса (по умолчанию английский).
//
// Значение живёт в пакете i18n: его должен видеть и домен, которому нельзя
// зависеть от config. Здесь остаётся привычная точка доступа для обработчиков.
func (ps *ProjectState) GetLanguage() string {
	return i18n.Active()
}

// SetLanguage обновляет язык интерфейса и возвращает применённый код.
// Незнакомый код заменяется языком по умолчанию, поэтому значение из базы
// можно передавать сюда без дополнительной проверки.
func (ps *ProjectState) SetLanguage(lang string) string {
	applied := i18n.SetActive(lang)
	ps.Lock()
	defer ps.Unlock()
	ps.Language = applied
	return applied
}
