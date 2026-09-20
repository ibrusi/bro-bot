package claude

import (
	"strings"

	"bro-bot/internal/agents"
	"bro-bot/internal/ports"
)

// apiKeyEnv — переменные, любая из которых даёт ключ для режима api, в порядке
// приоритета: ANTHROPIC_API_KEY, затем устаревшая CLAUDE_API_KEY. Список один на
// весь адаптер: и для реестра, и для самих запросов, чтобы они не разошлись.
var apiKeyEnv = []string{"ANTHROPIC_API_KEY", "CLAUDE_API_KEY"}

// Spec описывает агента claude для реестра: CLI Claude Code, Claude API либо режим MCP.
func Spec() agents.Spec {
	return agents.Spec{
		Name:         "claude",
		CLITitle:     "Claude Code",
		APITitle:     "Claude API",
		MCPTitle:     "Claude Code (MCP)",
		APIKeyEnv:    apiKeyEnv,
		DefaultModel: func() string { return "sonnet" },
		// Модели Gemini и GPT claude не запускает: при переключении на него
		// такая модель заменяется на модель по умолчанию.
		RejectsModel: func(model string) bool {
			m := strings.ToLower(model)
			return strings.Contains(m, "gemini") || strings.Contains(m, "gpt")
		},
		NewCLI: func() ports.AgentFramework { return NewClaudeAdapter() },
		NewAPI: func() ports.AgentFramework { return NewClaudeAPIAdapter() },
		NewMCP: func() ports.AgentFramework { return NewClaudeMCPAdapter() },
	}
}
