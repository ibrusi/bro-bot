package config

import (
	"bro-bot/internal/domain"
	"time"
)

var (
	Session         domain.AgentSession
	ProjectState    domain.ProjectState
	AdminID         int64
	ProjectsRoot    string
	QuestionTimeout time.Duration
	StepTimeout     time.Duration
	BotDir          string
	DBPath          string
	ScriptsDir      string
)

