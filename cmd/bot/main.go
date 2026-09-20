package main

import (
	"log"
	"os"
	"time"

	"bro-bot/internal/adapters/agy"
	"bro-bot/internal/adapters/claude"
	"bro-bot/internal/adapters/telegram"
	"bro-bot/internal/agents"
	"bro-bot/internal/config"
	"bro-bot/internal/handlers"
	"bro-bot/internal/mcp"
	"bro-bot/internal/ports"
)

// buildTransport выбирает и инициализирует адаптер мессенджера. Новый мессенджер
// подключается добавлением одного case сюда, без изменений в internal/handlers.
//
// Токен берётся прямо здесь и нигде не сохраняется: в снимке конфигурации ему не место.
func buildTransport(messenger string) ports.Transport {
	switch messenger {
	case "", "telegram":
		botToken, err := config.BotToken()
		if err != nil {
			log.Fatal(err)
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
		log.Fatalf("unknown messenger in MESSENGER: %s", messenger)
		return nil
	}
}

// buildAgentRegistry собирает реестр агентов. Это единственное место, где бот знает
// о конкретных адаптерах: первый зарегистрированный агент становится агентом по
// умолчанию, новый агент подключается одной строкой.
func buildAgentRegistry() *agents.Registry {
	reg := agents.NewRegistry()
	reg.Register(agy.Spec())
	reg.Register(claude.Spec())
	return reg
}

// main — корень композиции и единственное место, где бот завершает процесс: всё
// остальное возвращает ошибку наверх.
func main() {
	if len(os.Args) > 1 && os.Args[1] == "mcp-serve" {
		if err := mcp.RunStdioServer(); err != nil {
			log.Fatalf("mcp server error: %v", err)
		}
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	if err := handlers.Start(buildTransport(cfg.Messenger), buildAgentRegistry(), cfg); err != nil {
		log.Fatal(err)
	}
}
