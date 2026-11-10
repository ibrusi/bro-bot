package grok

import (
	"strings"

	"bro-bot/internal/agents"
	"bro-bot/internal/ports"
)

// defaultModel — модель Grok, если текущая агенту не подходит.
const defaultModel = "grok-4.7"

// Spec описывает агента grok. Он говорит с ботом только через ACP
// (`grok agent stdio`): режимов mcp и api у него нет.
func Spec() agents.Spec {
	return agents.Spec{
		Name:         "grok",
		ACPTitle:     "Grok (ACP)",
		DefaultModel: func() string { return defaultModel },
		// Каталоги agy и claude grok не запускает: при переключении на него
		// такая модель заменяется на модель по умолчанию.
		RejectsModel: rejectsModel,
		ModelAliases: modelAliases,
		NewACP:       func() ports.AgentFramework { return NewAdapter() },
	}
}

// rejectsModel сообщает, что модель принадлежит другому семейству.
func rejectsModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" || strings.HasPrefix(m, "grok") {
		return false
	}
	return strings.Contains(m, "gemini") ||
		strings.Contains(m, "claude") ||
		strings.Contains(m, "gpt") ||
		strings.Contains(m, "sonnet") ||
		strings.Contains(m, "opus") ||
		strings.Contains(m, "haiku") ||
		strings.Contains(m, "flash")
}
