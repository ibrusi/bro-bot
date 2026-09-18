package main

import (
	"log"
	"os"
	"time"

	"bro-bot/internal/adapters/agy"
	"bro-bot/internal/adapters/telegram"
	"bro-bot/internal/handlers"
	"bro-bot/internal/models"
	"bro-bot/internal/ports"
)

// buildTransport выбирает и инициализирует адаптер мессенджера по переменной окружения
// MESSENGER (по умолчанию — telegram). Новый мессенджер подключается добавлением одного
// case сюда, без изменений в internal/handlers.
func buildTransport() ports.Transport {
	switch messenger := os.Getenv("MESSENGER"); messenger {
	case "", "telegram":
		botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
		if botToken == "" {
			log.Fatal("ОБЯЗАТЕЛЬНЫЙ параметр TELEGRAM_BOT_TOKEN не задан")
		}
		t, err := telegram.New(telegram.Config{
			Token:       botToken,
			PollTimeout: 10 * time.Second,
		})
		if err != nil {
			log.Fatal(err)
		}
		return t
	default:
		log.Fatalf("неизвестный мессенджер в MESSENGER: %s", messenger)
		return nil
	}
}

func main() {
	adapter := agy.NewAgyAdapter()
	handlers.SetActiveAgent(adapter, "agy")
	models.SetAgent(adapter)

	handlers.Start(buildTransport())
}
