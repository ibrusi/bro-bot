package main

import (
	"tg-agent-bot/internal/adapters/agy"
	"tg-agent-bot/internal/handlers"
	"tg-agent-bot/internal/models"
)

func main() {
	adapter := agy.NewAgyAdapter()
	handlers.Agent = adapter
	models.Agent = adapter

	handlers.Start()
}
