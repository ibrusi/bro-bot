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

// Spec описывает агента agy для реестра: CLI Google Antigravity либо Gemini API напрямую.
func Spec() agents.Spec {
	return agents.Spec{
		Name:         "agy",
		CLITitle:     "Google Antigravity",
		APITitle:     "Gemini API",
		APIKeyEnv:    apiKeyEnv,
		DefaultModel: defaultModel,
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
