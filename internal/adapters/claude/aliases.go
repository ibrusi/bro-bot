package claude

import "bro-bot/internal/models"

// modelAliases — псевдонимы моделей claude. Действуют во всех режимах: cli, mcp и api.
// ID — имена Claude Code и Claude API (claude-opus-4-6, а не claude-opus-4-6-thinking
// из каталога agy). Точные ID тоже перечислены: без ключа API каталог claude состоит
// только из псевдонимов CLI, и иначе /model claude-opus-5-5 не распознался бы.
var modelAliases = models.NewAliases(map[string]string{
	"default": "claude-sonnet-5-5",

	// Claude Sonnet
	"sonnet":            "claude-sonnet-5-5",
	"claude-sonnet":     "claude-sonnet-5-5",
	"sonnet-5.5":        "claude-sonnet-5-5",
	"claude-sonnet-5.5": "claude-sonnet-5-5",
	"claude-sonnet-5-5": "claude-sonnet-5-5",
	"sonnet-5":          "claude-sonnet-5",
	"claude-sonnet-5":   "claude-sonnet-5",
	"claude-5":          "claude-sonnet-5",
	"claude-sonnet-4.6": "claude-sonnet-4-6",
	"claude-sonnet-4-6": "claude-sonnet-4-6",

	// Claude Opus
	"opus":            "claude-opus-5-5",
	"claude-opus":     "claude-opus-5-5",
	"opus-5.5":        "claude-opus-5-5",
	"claude-opus-5.5": "claude-opus-5-5",
	"claude-opus-5-5": "claude-opus-5-5",
	"claude-opus-4.6": "claude-opus-4-6",
	"claude-opus-4-6": "claude-opus-4-6",

	// Claude Haiku
	"haiku":            "claude-haiku-5-5",
	"claude-haiku":     "claude-haiku-5-5",
	"haiku-5.5":        "claude-haiku-5-5",
	"claude-haiku-5.5": "claude-haiku-5-5",
	"claude-haiku-5-5": "claude-haiku-5-5",
	"claude-haiku-4.5": "claude-haiku-4-5",
	"claude-haiku-4-5": "claude-haiku-4-5",
},
	models.AliasHint{Alias: "sonnet", Title: "Claude Sonnet 5.5"},
	models.AliasHint{Alias: "opus", Title: "Claude Opus 5.5"},
	models.AliasHint{Alias: "haiku", Title: "Claude Haiku 5.5"},
	models.AliasHint{Alias: "sonnet-5", Title: "Claude Sonnet 5"},
)
