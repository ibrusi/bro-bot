package grok

import "bro-bot/internal/models"

// modelAliases — псевдонимы моделей grok для команды /model.
// Живой список по-прежнему берётся из `grok models`; здесь только короткие имена.
var modelAliases = models.NewAliases(map[string]string{
	"default": defaultModel,
	"grok":    defaultModel,
	"4.7":     defaultModel,
	"fast":    "grok-4.7-build-fast",
	"4.6":     "grok-4.6",
	"4.5":     "grok-4.5",
},
	models.AliasHint{Alias: "grok", Title: "Grok 4.7"},
	models.AliasHint{Alias: "fast", Title: "Grok 4.7 Fast"},
)
