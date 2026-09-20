package handlers

import (
	"log"

	"bro-bot/internal/ports"
)

// authMiddleware пропускает апдейты только от администратора и только из его личного чата.
//
// Проверка отправителя без проверки чата недостаточна: если бота добавить в группу,
// сообщения администратора там пройдут, а ответы уйдут в общий чат — вместе с выводом
// агента, ссылками на PR и публичным SSH-ключом сервера из /clone. Бот рассчитан
// на личную переписку: уведомления о перезапуске и так отправляются на TELEGRAM_ADMIN_ID
// как на идентификатор чата.
//
// Отклонённый апдейт остаётся без ответа: отвечать посторонним «доступ запрещён» —
// значит подтверждать им, что бот жив.
func authMiddleware(admin ports.ChatID) func(ports.Handler) ports.Handler {
	return func(next ports.Handler) ports.Handler {
		return func(s ports.Session) error {
			if !isAdminSession(s, admin) {
				log.Printf("update rejected: sender %q, chat %q", s.SenderID(), s.Chat())
				return nil
			}
			return next(s)
		}
	}
}

// isAdminSession сообщает, принадлежит ли апдейт администратору в его личном чате.
func isAdminSession(s ports.Session, admin ports.ChatID) bool {
	if admin == "" || s == nil {
		return false
	}
	return s.SenderID() == string(admin) && s.Chat() == admin
}
