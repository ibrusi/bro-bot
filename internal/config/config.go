package config
import "tg-agent-bot/internal/domain"

var (
	Session      domain.AgentSession
	ProjectState domain.ProjectState
	AdminID      int64
	ProjectsRoot = "/home/deploy/projects"
)
