package config

import (
	"tg-agent-bot/internal/domain"
	"time"
)

var (
	Session         domain.AgentSession
	ProjectState    domain.ProjectState
	AdminID         int64
	ProjectsRoot    string
	QuestionTimeout time.Duration
	BotDir          string
	DBPath          string
)

