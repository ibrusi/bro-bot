package domain

import (
	"bro-bot/internal/effort"
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
	ExecutionMode  string // "mcp" или "api"
	PlanMode       bool
	// InteractionMode задаёт, что делать с обычным сообщением без активной задачи:
	// "chat" — отвечать в диалоге, "task" — сразу создавать задачу.
	InteractionMode string
	// ChatWriteMode управляет правами записи в /chat ("deny", "ask", "allow"; по умолчанию "ask").
	ChatWriteMode string
	// PlanWriteMode управляет правами записи на этапе /plan ("deny", "ask", "allow"; по умолчанию "deny").
	PlanWriteMode string
	// Effort — уровень усилия рассуждений агента, общий для всех агентов.
	Effort effort.Level
	// Language — зеркало выбранного языка для отладки и снимков состояния.
	// Источник истины — i18n.Active(): см. GetLanguage/SetLanguage.
	Language string
}

// Политики записи на диск для режимов диалога (/chat) и планирования (/plan).
const (
	WriteModeDeny  = "deny"  // строгий read-only (чтение, аналитика, systemctl status)
	WriteModeAsk   = "ask"   // подтверждение кнопкой + авто-запись в CHAT_WRITE_PATHS (кроме SCRIPTS_DIR)
	WriteModeAllow = "allow" // запись разрешена сразу (включая SCRIPTS_DIR); запуск скриптов из SCRIPTS_DIR — по кнопке
)

// NormalizeWriteMode нормализует ввод (включая синонимы on/off/ro/approve/auto) к каноническому значению.
func NormalizeWriteMode(raw, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case WriteModeDeny, "off", "ro", "readonly", "disable", "false", "0", "запретить", "выкл", "нет":
		return WriteModeDeny
	case WriteModeAsk, "confirm", "спрашивать", "подтверждать":
		return WriteModeAsk
	case WriteModeAllow, "approve", "auto", "on", "enable", "true", "1", "разрешить", "вкл", "да":
		return WriteModeAllow
	default:
		return fallback
	}
}

// GetChatWriteMode возвращает текущую политику записи в режиме диалога (/chat, по умолчанию "ask").
func (ps *ProjectState) GetChatWriteMode() string {
	ps.RLock()
	defer ps.RUnlock()
	return NormalizeWriteMode(ps.ChatWriteMode, WriteModeAsk)
}

// SetChatWriteMode обновляет политику записи в режиме диалога (/chat).
func (ps *ProjectState) SetChatWriteMode(mode string) {
	ps.Lock()
	defer ps.Unlock()
	ps.ChatWriteMode = NormalizeWriteMode(mode, WriteModeAsk)
}

// GetPlanWriteMode возвращает текущую политику записи на этапе планирования (/plan, по умолчанию "deny").
func (ps *ProjectState) GetPlanWriteMode() string {
	ps.RLock()
	defer ps.RUnlock()
	return NormalizeWriteMode(ps.PlanWriteMode, WriteModeDeny)
}

// SetPlanWriteMode обновляет политику записи на этапе планирования (/plan).
func (ps *ProjectState) SetPlanWriteMode(mode string) {
	ps.Lock()
	defer ps.Unlock()
	ps.PlanWriteMode = NormalizeWriteMode(mode, WriteModeDeny)
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

// GetExecutionMode возвращает текущий режим выполнения ("mcp", "api" или "acp", по умолчанию "mcp").
func (ps *ProjectState) GetExecutionMode() string {
	ps.RLock()
	defer ps.RUnlock()
	if ps.ExecutionMode == "" {
		return "mcp"
	}
	return ps.ExecutionMode
}

// SetExecutionMode обновляет режим выполнения.
// Устаревшее имя cli сохраняется как mcp.
func (ps *ProjectState) SetExecutionMode(mode string) {
	ps.Lock()
	defer ps.Unlock()
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "api":
		ps.ExecutionMode = "api"
	case "acp":
		ps.ExecutionMode = "acp"
	default:
		ps.ExecutionMode = "mcp"
	}
}

// GetEffort возвращает текущий уровень усилия рассуждений (по умолчанию auto).
func (ps *ProjectState) GetEffort() effort.Level {
	ps.RLock()
	defer ps.RUnlock()
	return ps.Effort
}

// SetEffort обновляет уровень усилия рассуждений.
func (ps *ProjectState) SetEffort(level effort.Level) {
	ps.Lock()
	defer ps.Unlock()
	ps.Effort = level
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
