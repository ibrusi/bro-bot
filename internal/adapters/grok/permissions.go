package grok

import (
	"os"
	"strings"
)

// yoloFromEnv сообщает, подтверждать ли инструменты grok без вопроса в задачах.
// Пустое значение и любое слово, кроме явного выключения, означает да:
// так же агенты mcp запускаются с --dangerously-skip-permissions.
// Выключение: GROK_ACP_YOLO=false|0|no|off.
// В диалоге (/chat) подтверждение всё равно выключено: правки и команды отклоняются.
func yoloFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GROK_ACP_YOLO"))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
