package config

import (
	"bro-bot/internal/domain"
	"bro-bot/internal/ports"
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
	BotDir      string
	ServiceName string
	DBPath      string
	ScriptsDir  string

	// Whisper (распознавание голосовых сообщений)
	WhisperServerURL string
	WhisperAPIKey    string
	WhisperModel     string
	WhisperLanguage  string
	WhisperTimeout   time.Duration

	// Debug включает режим отладки (расширенные метрики в чат).
	Debug bool
)
