package main

import (
	"bro-bot/internal/adapters/agy"
	"bro-bot/internal/handlers"
	"bro-bot/internal/models"
)

func main() {
	adapter := agy.NewAgyAdapter()
	handlers.Agent = adapter
	models.Agent = adapter

	handlers.Start()
}
