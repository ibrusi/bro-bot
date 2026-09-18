package claude

import (
	"strings"

	"bro-bot/internal/agents"
	"bro-bot/internal/ports"
)

// Spec описывает агента claude для реестра: CLI Claude Code либо Claude API напрямую.
func Spec() agents.Spec {
	return agents.Spec{
		Name:         "claude",
		CLITitle:     "Claude Code",
		APITitle:     "Claude API",
		APIKeyEnv:    []string{"ANTHROPIC_API_KEY", "CLAUDE_API_KEY"},
		DefaultModel: func() string { return "sonnet" },
		// Модели Gemini и GPT claude не запускает: при переключении на него
		// такая модель заменяется на модель по умолчанию.
		RejectsModel: func(model string) bool {
			m := strings.ToLower(model)
			return strings.Contains(m, "gemini") || strings.Contains(m, "gpt")
		},
		NewCLI: func() ports.AgentFramework { return NewClaudeAdapter() },
		NewAPI: func() ports.AgentFramework { return NewClaudeAPIAdapter() },
	}
}
