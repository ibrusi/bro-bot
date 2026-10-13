package agy

import (
	"strings"

	"bro-bot/internal/agents"
	"bro-bot/internal/config"
	"bro-bot/internal/ports"
)

// apiKeyEnv — переменные, любая из которых даёт ключ для режима api. Список один
// на весь адаптер: и для реестра, и для самих запросов.
var apiKeyEnv = []string{"GEMINI_API_KEY"}

// fallbackDefaultModel — на случай, если модель по умолчанию ещё не разрешена
// (например, адаптер собрали в тесте без запуска Start).
const fallbackDefaultModel = "gemini-3.1-pro-high"

// Spec описывает агента agy для реестра: Gemini API либо режим Google Antigravity (MCP).
func Spec() agents.Spec {
	return agents.Spec{
		Name:         "agy",
		APITitle:     "Gemini API",
		MCPTitle:     "Google Antigravity (MCP)",
		APIKeyEnv:    apiKeyEnv,
		DefaultModel: defaultModel,
		// Модели Claude вне каталога agy (haiku, sonnet-5) при переключении на него
		// заменяются на модель по умолчанию.
		RejectsModel: rejectsModel,
		ModelAliases: modelAliases,
		NewAPI:       func() ports.AgentFramework { return NewAgyAPIAdapter() },
		NewMCP:       func() ports.AgentFramework { return NewAgyMCPAdapter() },
	}
}

// defaultModel возвращает модель бота по умолчанию. Значение приходит из снимка
// конфигурации, уже разрешённое через реестр моделей: раньше DEFAULT_MODEL читали
// три места с разными запасными значениями, и псевдоним flash давал в них разные
// модели.
func defaultModel() string {
	if model := strings.TrimSpace(config.DefaultModel); model != "" {
		return model
	}
	return fallbackDefaultModel
}
