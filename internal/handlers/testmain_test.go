package handlers

import (
	"os"
	"testing"
)

// TestMain задаёт BOT_DIR на весь пакет тестов.
//
// Start() поднимает фоновую проверку маркера перезапуска (system.CheckAndNotifyRestart),
// которая просыпается через полторы секунды и завершает процесс, если BOT_DIR не задан.
// Значение из t.Setenv к этому моменту уже откатывается, поэтому фиксируем переменную
// на весь запуск тестов.
func TestMain(m *testing.M) {
	if os.Getenv("BOT_DIR") == "" {
		dir, err := os.MkdirTemp("", "bro-bot-tests-")
		if err != nil {
			panic(err)
		}
		_ = os.Setenv("BOT_DIR", dir)
		defer os.RemoveAll(dir)
	}
	os.Exit(m.Run())
}
