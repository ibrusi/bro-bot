package agy

import (
	"os"
	"strings"

	"bro-bot/internal/agents"
	"bro-bot/internal/ports"
)

// Spec описывает агента agy для реестра: CLI Google Antigravity либо Gemini API напрямую.
func Spec() agents.Spec {
	return agents.Spec{
		Name:      "agy",
		CLITitle:  "Google Antigravity",
		APITitle:  "Gemini API",
		APIKeyEnv: []string{"GEMINI_API_KEY"},
		DefaultModel: func() string {
			if model := strings.TrimSpace(os.Getenv("DEFAULT_MODEL")); model != "" {
				return model
			}
			return "gemini-3.1-pro-high"
		},
		// Модели семейства Claude agy не запускает: при переключении на него
		// такая модель заменяется на модель по умолчанию.
		RejectsModel: func(model string) bool {
			m := strings.ToLower(model)
			return strings.Contains(m, "claude") || strings.Contains(m, "sonnet") ||
				strings.Contains(m, "opus") || strings.Contains(m, "haiku")
		},
		NewCLI: func() ports.AgentFramework { return NewAgyAdapter() },
		NewAPI: func() ports.AgentFramework { return NewAgyAPIAdapter() },
	}
}
