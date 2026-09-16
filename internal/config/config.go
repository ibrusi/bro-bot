package config

import (
	"bro-bot/internal/domain"
	"bro-bot/internal/ports"
	"time"
)

var (
	Session         domain.AgentSession
	ProjectState    domain.ProjectState
	AdminID         ports.ChatID
	ProjectsRoot    string
	QuestionTimeout time.Duration
	StepTimeout     time.Duration
	BotDir          string
	DBPath          string
	ScriptsDir      string
)
