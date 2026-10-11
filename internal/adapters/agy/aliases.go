package agy

import (
	"strings"

	"bro-bot/internal/models"
)

// claudeFamilies — модели Claude из каталога agy (`agy models` 1.3.3). Каждая доступна
// вариантами по уровню рассуждений (claude-opus-5-5-high), базовое имя agy дополняет
// уровнем сам (models.BuildAgyModelArgs). Haiku в каталоге agy нет.
var claudeFamilies = []string{"claude-opus-5-5", "claude-sonnet-5-5"}

// modelAliases — псевдонимы моделей agy. Действуют во всех режимах: cli, mcp и api.
// Базовые имена семейств Claude перечислены явно: в каталоге agy есть только их
// варианты по уровню.
var modelAliases = models.NewAliases(map[string]string{
	"default": "gemini-3.1-pro-high",

	// Flash 3.8
	"flash":            "gemini-3.8-flash-medium",
	"3.8":              "gemini-3.8-flash-medium",
	"3.8-flash":        "gemini-3.8-flash-medium",
	"gemini-3.8-flash": "gemini-3.8-flash-medium",
	"flash-high":       "gemini-3.8-flash-high",
	"3.8-high":         "gemini-3.8-flash-high",
	"flash-low":        "gemini-3.8-flash-low",
	"3.8-low":          "gemini-3.8-flash-low",

	// Flash 3.7
	"3.7":              "gemini-3.7-flash-medium",
	"3.7-flash":        "gemini-3.7-flash-medium",
	"gemini-3.7-flash": "gemini-3.7-flash-medium",
	"3.7-high":         "gemini-3.7-flash-high",
	"3.7-low":          "gemini-3.7-flash-low",

	// Flash 3.6
	"3.6":              "gemini-3.6-flash-medium",
	"3.6-flash":        "gemini-3.6-flash-medium",
	"gemini-3.6-flash": "gemini-3.6-flash-medium",
	"3.6-high":         "gemini-3.6-flash-high",
	"3.6-low":          "gemini-3.6-flash-low",

	// Pro 3.1
	"pro":            "gemini-3.1-pro-high",
	"3.1":            "gemini-3.1-pro-high",
	"3.1-pro":        "gemini-3.1-pro-high",
	"gemini-3.1-pro": "gemini-3.1-pro-high",
	"pro-low":        "gemini-3.1-pro-low",
	"3.1-low":        "gemini-3.1-pro-low",

	// Claude Sonnet 5.5: уровень рассуждений задаёт /effort
	"sonnet":            "claude-sonnet-5-5",
	"claude-sonnet":     "claude-sonnet-5-5",
	"sonnet-5.5":        "claude-sonnet-5-5",
	"claude-sonnet-5.5": "claude-sonnet-5-5",
	"claude-sonnet-5-5": "claude-sonnet-5-5",

	// Claude Opus 5.5
	"opus":            "claude-opus-5-5",
	"claude-opus":     "claude-opus-5-5",
	"opus-5.5":        "claude-opus-5-5",
	"claude-opus-5.5": "claude-opus-5-5",
	"claude-opus-5-5": "claude-opus-5-5",

	// GPT-OSS
	"oss":                 "gpt-oss-120b-medium",
	"gpt-oss":             "gpt-oss-120b-medium",
	"120b":                "gpt-oss-120b-medium",
	"gpt-oss-120b":        "gpt-oss-120b-medium",
	"gpt-oss-120b-medium": "gpt-oss-120b-medium",
},
	models.AliasHint{Alias: "flash", Title: "Gemini 3.8 Flash"},
	models.AliasHint{Alias: "pro", Title: "Gemini 3.1 Pro"},
	models.AliasHint{Alias: "sonnet", Title: "Claude Sonnet 5.5"},
	models.AliasHint{Alias: "opus", Title: "Claude Opus 5.5"},
	models.AliasHint{Alias: "oss", Title: "GPT-OSS 120B"},
)

// rejectsModel сообщает, что модель agy не запустит: это модель Claude, которой нет
// в каталоге agy (например, claude-haiku-5-5 или claude-sonnet-5). Псевдоним сначала
// разрешается в наборе agy, поэтому sonnet и opus допустимы, а haiku — нет.
func rejectsModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if resolved, ok := modelAliases.Resolve(m); ok {
		m = resolved
	}
	if !isClaudeModel(m) {
		return false
	}
	for _, family := range claudeFamilies {
		if m == family || strings.HasPrefix(m, family+"-") {
			return false
		}
	}
	return true
}

func isClaudeModel(m string) bool {
	return strings.Contains(m, "claude") || strings.Contains(m, "sonnet") ||
		strings.Contains(m, "opus") || strings.Contains(m, "haiku")
}
