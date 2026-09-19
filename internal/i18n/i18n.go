package i18n

import (
	"strings"
)

type Language string

const (
	LangEN Language = "en"
	LangRU Language = "ru"
)

func (l Language) String() string {
	return string(l)
}

var messages = map[Language]map[string]string{
	LangRU: {
		"StatusQueued":          "⏳ В очереди",
		"StatusPlanning":        "📝 Составление плана",
		"StatusWaitingApproval": "📋 Ожидает утверждения плана",
		"StatusRunning":         "⚙️ Выполняется",
		"StatusWaitingInput":    "❓ Ждёт вашего ответа",
		"StatusPaused":          "⏸ Приостановлена",
		"StatusCompleted":       "✅ Завершена",
		"StatusCancelled":       "🛑 Отменена",
		"StatusFailed":          "❌ Ошибка",
		"ChatSystemPreamble":    "РЕЖИМ ДИАЛОГА. Ты отвечаешь на вопросы пользователя о проекте в чате.\nРазрешено: читать файлы, искать по коду, выполнять безопасные read-only команды (git log, git status, ls, grep), объяснять архитектуру и предлагать решения словами.\nЗАПРЕЩЕНО: создавать git-ветки, изменять, создавать и удалять файлы, делать commit и push, открывать Pull Request, запускать миграции и деплой.\nЕсли для ответа нужна правка кода — не выполняй её, а коротко опиши, что именно нужно сделать: пользователь оформит это отдельной задачей.\nОтвечай кратко, по делу и на русском языке.",
		"ChatShortReminder":     "(режим диалога: только чтение и ответ, без изменений в файлах и git)",
		"CmdLanguage":           "Выбор языка / Language selection",
		"CmdLanguageDesc":       "Выберите язык интерфейса:",
		"LangSetToRU":           "🇷🇺 Язык интерфейса изменен на русский.",
		"LangSetToEN":           "🇬🇧 Interface language changed to English.",
		"BtnApprovePlan":        "✅ Утвердить план",
		"BtnCancel":             "❌ Отменить",
		"BtnResume":             "▶️ Возобновить",
		"BtnPause":              "⏸ Приостановить",
		"BtnDownloadPlan":       "📄 Скачать план (.md)",
		"BtnApproveAndStart":    "✅ Утвердить и начать",
	},
	LangEN: {
		"StatusQueued":          "⏳ Queued",
		"StatusPlanning":        "📝 Planning",
		"StatusWaitingApproval": "📋 Waiting for plan approval",
		"StatusRunning":         "⚙️ Running",
		"StatusWaitingInput":    "❓ Waiting for your input",
		"StatusPaused":          "⏸ Paused",
		"StatusCompleted":       "✅ Completed",
		"StatusCancelled":       "🛑 Cancelled",
		"StatusFailed":          "❌ Failed",
		"ChatSystemPreamble":    "CHAT MODE. You answer the user's questions about the project in chat.\nAllowed: read files, search code, execute safe read-only commands (git log, git status, ls, grep), explain architecture, and propose solutions in words.\nFORBIDDEN: create git branches, modify, create, and delete files, commit and push, open Pull Requests, run migrations, and deploy.\nIf code modification is needed to answer - do not execute it, but briefly describe what needs to be done: the user will format it as a separate task.\nAnswer briefly, to the point, and in English.",
		"ChatShortReminder":     "(chat mode: read-only and answer, no changes to files and git)",
		"CmdLanguage":           "Language selection / Выбор языка",
		"CmdLanguageDesc":       "Choose interface language:",
		"LangSetToRU":           "🇷🇺 Язык интерфейса изменен на русский.",
		"LangSetToEN":           "🇬🇧 Interface language changed to English.",
		"BtnApprovePlan":        "✅ Approve plan",
		"BtnCancel":             "❌ Cancel",
		"BtnResume":             "▶️ Resume",
		"BtnPause":              "⏸ Pause",
		"BtnDownloadPlan":       "📄 Download plan (.md)",
		"BtnApproveAndStart":    "✅ Approve and start",
	},
}

func T(lang string, key string) string {
	l := Language(strings.ToLower(lang))
	if m, ok := messages[l]; ok {
		if val, ok := m[key]; ok {
			return val
		}
	}
	// Fallback to English
	if m, ok := messages[LangEN]; ok {
		if val, ok := m[key]; ok {
			return val
		}
	}
	return key
}

func TaskStatusTitle(status string, lang string) string {
	switch status {
	case "queued":
		return T(lang, "StatusQueued")
	case "planning":
		return T(lang, "StatusPlanning")
	case "waiting_approval":
		return T(lang, "StatusWaitingApproval")
	case "running":
		return T(lang, "StatusRunning")
	case "waiting_input":
		return T(lang, "StatusWaitingInput")
	case "paused":
		return T(lang, "StatusPaused")
	case "completed":
		return T(lang, "StatusCompleted")
	case "cancelled":
		return T(lang, "StatusCancelled")
	case "failed":
		return T(lang, "StatusFailed")
	default:
		return status
	}
}
