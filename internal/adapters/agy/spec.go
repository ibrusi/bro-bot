package agy

import (
	"bro-bot/internal/agents"
	"bro-bot/internal/ports"
)

// apiKeyEnv — переменные, любая из которых даёт ключ для режима api. Список один
// на весь адаптер: и для реестра, и для самих запросов.
var apiKeyEnv = []string{"GEMINI_API_KEY"}

// defaultModel — модель по умолчанию при переключении на agy (соответствует алиасу orchestrator).
const defaultModel = "gemini-pro-orchestrator-neon"

// Spec описывает агента agy для реестра: по умолчанию режим ACP (Google Antigravity),
// альтернативно Gemini API.
func Spec() agents.Spec {
	return agents.Spec{
		Name:         "agy",
		APITitle:     "Gemini API",
		ACPTitle:     "Google Antigravity (ACP)",
		DefaultMode:  agents.ModeACP,
		APIKeyEnv:    apiKeyEnv,
		DefaultModel: func() string { return defaultModel },
		// Модели других семейств (Claude и Grok) при переключении на agy
		// заменяются на модель по умолчанию (orchestrator).
		RejectsModel: rejectsModel,
		ModelAliases: modelAliases,
		NewAPI:       func() ports.AgentFramework { return NewAgyAPIAdapter() },
		NewACP:       func() ports.AgentFramework { return NewAgyACPAdapter() },
	}
}
