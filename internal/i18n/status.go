package i18n

// taskStatusKeys связывает значения статуса задачи из домена с ключами каталога.
// Домен и i18n не зависят друг от друга по типам: статус приходит строкой,
// поэтому новый статус нужно добавить и сюда, и в каталоги всех языков.
var taskStatusKeys = map[string]string{
	"queued":           "status.queued",
	"planning":         "status.planning",
	"waiting_approval": "status.waiting_approval",
	"running":          "status.running",
	"waiting_input":    "status.waiting_input",
	"paused":           "status.paused",
	"completed":        "status.completed",
	"cancelled":        "status.cancelled",
	"failed":           "status.failed",
}

// TaskStatusTitle возвращает человекочитаемое название статуса задачи.
// Незнакомый статус возвращается как есть: подменять его выдуманным текстом хуже,
// чем показать сырое значение.
func TaskStatusTitle(status string, lang string) string {
	key, ok := taskStatusKeys[status]
	if !ok {
		return status
	}
	return T(lang, key)
}

// TaskStatusKeys возвращает ключи каталога для всех известных статусов задачи.
func TaskStatusKeys() []string {
	keys := make([]string, 0, len(taskStatusKeys))
	for _, key := range taskStatusKeys {
		keys = append(keys, key)
	}
	return keys
}
