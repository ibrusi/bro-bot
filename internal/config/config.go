package config

import (
	"bro-bot/internal/domain"
	"bro-bot/internal/ports"
	"sync"
	"time"
)

// Состояние времени выполнения: меняется командами пользователя, у каждого свой мьютекс.
var (
	Session      domain.AgentSession
	ProjectState domain.ProjectState
)

// Снимок конфигурации. Эти переменные пишет только Apply — один раз при старте,
// до того как поднимутся обработчики и фоновые горутины. Читать их можно откуда угодно,
// а вот окружение напрямую — нельзя: за этим следит envguard_test.
var (
	AdminID         ports.ChatID
	ProjectsRoot    string
	DefaultProject  string
	DefaultModel    string
	QuestionTimeout time.Duration
	// ChatTimeout — таймаут одного хода диалогового режима.
	ChatTimeout time.Duration
	StepTimeout time.Duration
	// AutoContinueMax — сколько раз подряд пайплайн сам продолжает шаг, оборванный
	// выходом агента с прерыванием его фоновых задач. 0 отключает автопродолжение.
	AutoContinueMax     int
	BotDir              string
	ServiceName         string
	DBPath              string
	ScriptsDir          string
	MCPSocketPath       string
	ChatWritePaths      []string
	ChatApprovalTimeout time.Duration

	// Whisper (распознавание голосовых сообщений)
	WhisperServerURL   string
	WhisperAPIKey      string
	WhisperModel       string
	WhisperLanguage    string
	WhisperPrompt      string
	WhisperTemperature float64
	WhisperLoudnorm    bool
	WhisperTimeout     time.Duration

	// Debug включает режим отладки (расширенные метрики в чат).
	Debug bool

	writePolicyMu sync.RWMutex
	writePolicy   WritePolicyConfig
)

// WritePolicyConfig — потокобезопасный снимок настроек политики записи для фоновых горутин.
type WritePolicyConfig struct {
	ScriptsDir          string
	ChatWritePaths      []string
	ChatApprovalTimeout time.Duration
}

// WritePolicySnapshot возвращает копию актуальной конфигурации политики записи.
func WritePolicySnapshot() WritePolicyConfig {
	writePolicyMu.RLock()
	defer writePolicyMu.RUnlock()
	out := writePolicy
	if len(writePolicy.ChatWritePaths) > 0 {
		out.ChatWritePaths = append([]string(nil), writePolicy.ChatWritePaths...)
	}
	return out
}

// SetWritePolicy потокобезопасно обновляет снимок политики записи (используется при инициализации и в тестах).
func SetWritePolicy(p WritePolicyConfig) {
	var paths []string
	if len(p.ChatWritePaths) > 0 {
		paths = append([]string(nil), p.ChatWritePaths...)
	}
	writePolicyMu.Lock()
	writePolicy = WritePolicyConfig{
		ScriptsDir:          p.ScriptsDir,
		ChatWritePaths:      paths,
		ChatApprovalTimeout: p.ChatApprovalTimeout,
	}
	writePolicyMu.Unlock()
}
