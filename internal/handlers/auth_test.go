package handlers

import (
	"testing"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/ports"
)

const testAdminID ports.ChatID = "12345"

func TestAuthMiddleware(t *testing.T) {
	cases := []struct {
		name     string
		sender   string
		chat     ports.ChatID
		wantPass bool
	}{
		{"админ в личке", "12345", testAdminID, true},
		// Главный случай, ради которого добавлена проверка чата: в группе ответы
		// агента увидели бы посторонние.
		{"админ в группе", "12345", "-1001234567890", false},
		{"чужой отправитель в личке админа", "99999", testAdminID, false},
		{"чужой отправитель в своей личке", "99999", "99999", false},
		{"пустой отправитель", "", testAdminID, false},
		{"пустой чат", "12345", "", false},
		{"оба пустые", "", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler := authMiddleware(testAdminID)(func(ports.Session) error {
				called = true
				return nil
			})

			messenger := mock.New()
			sess := &mock.Session{M: messenger, Sender: tc.sender, ChatID: tc.chat}
			if err := handler(sess); err != nil {
				t.Fatalf("middleware вернул ошибку: %v", err)
			}

			if called != tc.wantPass {
				t.Errorf("обработчик вызван = %v, ожидали %v", called, tc.wantPass)
			}
			// Постороннему не отвечаем вовсе: ответ подтвердил бы, что бот жив.
			if !tc.wantPass && len(messenger.AllTexts()) != 0 {
				t.Errorf("отклонённому апдейту не должно уходить ответа")
			}
		})
	}
}

// TestAuthMiddlewareWithoutAdmin — если TELEGRAM_ADMIN_ID не задан, не проходит никто.
func TestAuthMiddlewareWithoutAdmin(t *testing.T) {
	called := false
	handler := authMiddleware("")(func(ports.Session) error {
		called = true
		return nil
	})

	if err := handler(&mock.Session{M: mock.New(), Sender: "12345", ChatID: "12345"}); err != nil {
		t.Fatalf("middleware вернул ошибку: %v", err)
	}
	if called {
		t.Error("без заданного администратора апдейты пропускать нельзя")
	}
}

// TestCommandsGoThroughAuthMiddleware — сквозная проверка: команда, зарегистрированная
// через Start, действительно проходит через middleware транспорта. Тест сторожит и сам
// мок-транспорт: пока его Use был пустышкой, проверка доступа в тестах не выполнялась.
func TestCommandsGoThroughAuthMiddleware(t *testing.T) {
	mt := setupTestApp(t)

	handler, ok := mt.commands["projects"]
	if !ok {
		t.Fatal("команда /projects не зарегистрирована")
	}

	stranger := &mock.Session{M: mt.Messenger, Sender: "99999", ChatID: "99999"}
	if err := handler(stranger); err != nil {
		t.Fatalf("обработчик вернул ошибку: %v", err)
	}
	if texts := mt.AllTexts(); len(texts) != 0 {
		t.Fatalf("команда от постороннего не должна выполняться, но бот ответил: %v", texts)
	}

	// Тот же обработчик для администратора отрабатывает — значит блокировка выше
	// исходит от middleware, а не от неработающей команды.
	if err := handler(adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("обработчик вернул ошибку для администратора: %v", err)
	}
	if len(mt.AllTexts()) == 0 {
		t.Error("администратору команда /projects должна отвечать")
	}
}

// TestGroupChatIsRejectedEndToEnd — сообщения администратора из группы не обслуживаются:
// вывод агента, ссылки на PR и SSH-ключ из /clone не должны попадать в общий чат.
func TestGroupChatIsRejectedEndToEnd(t *testing.T) {
	mt := setupTestApp(t)

	handler, ok := mt.commands["projects"]
	if !ok {
		t.Fatal("команда /projects не зарегистрирована")
	}

	group := &mock.Session{M: mt.Messenger, Sender: string(testChatID), ChatID: "-1001234567890"}
	if err := handler(group); err != nil {
		t.Fatalf("обработчик вернул ошибку: %v", err)
	}
	if texts := mt.AllTexts(); len(texts) != 0 {
		t.Fatalf("в группе бот отвечать не должен, но ответил: %v", texts)
	}
}
