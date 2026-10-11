package handlers

import (
	"context"
	"html"
	"log"
	"strings"

	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/effort"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
)

// Команда /effort: уровень усилия рассуждений агента. Уровень общий для всех
// агентов и режимов, как и модель: адаптеры сами переводят его в флаг CLI или
// поле запроса API.

const (
	// effortSettingKey — ключ настройки в SQLite.
	effortSettingKey = "effort"
	// effortButtonsPerRow — сколько кнопок выбора уровня помещается в один ряд.
	effortButtonsPerRow = 3
	// effortCallbackAction — действие кнопки выбора уровня.
	effortCallbackAction = "effort_sel"
)

// effortChoices возвращает все варианты выбора: auto и явные уровни по возрастанию.
func effortChoices() []effort.Level {
	return append([]effort.Level{effort.Auto}, effort.Levels()...)
}

// effortNames возвращает варианты через «|» для подсказок.
func effortNames() string {
	choices := effortChoices()
	names := make([]string, len(choices))
	for i, level := range choices {
		names[i] = level.String()
	}
	return strings.Join(names, "|")
}

// buildEffortMarkup собирает клавиатуру выбора уровня; текущий отмечен галочкой.
func buildEffortMarkup(current effort.Level) *ports.Keyboard {
	choices := effortChoices()
	rows := make([][]ports.Button, 0, (len(choices)+effortButtonsPerRow-1)/effortButtonsPerRow)

	var row []ports.Button
	for _, level := range choices {
		label := level.String()
		if level == current {
			label = "✅ " + label
		}
		row = append(row, ports.Button{Text: label, Action: effortCallbackAction, Payload: level.String()})
		if len(row) == effortButtonsPerRow {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	return &ports.Keyboard{Rows: rows}
}

// handleEffort — обработчик команды /effort.
func handleEffort(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	if len(args) == 0 {
		current := config.ProjectState.GetEffort()
		msg := i18n.Tf(lang, "effort.current", html.EscapeString(current.String()), effortNames()) + effortSupportNote(lang)
		return s.Send(msg, ports.RichWith(buildEffortMarkup(current)))
	}

	level, ok := effort.Parse(args[0])
	if !ok {
		return s.Send(i18n.Tf(lang, "effort.unknown", html.EscapeString(args[0]), effortNames()), ports.Rich())
	}
	return s.Send(switchEffort(level, lang), ports.Rich())
}

// onEffortSel — обработчик кнопки effort_sel.
func onEffortSel(s ports.Session) error {
	lang := uiLang()

	level, ok := effort.Parse(s.Callback().Payload)
	if !ok {
		return s.Respond(i18n.Tf(lang, "effort.unknown_short", s.Callback().Payload))
	}

	_ = s.Respond(i18n.Tf(lang, "effort.title", level.String()))
	return s.Send(switchEffort(level, lang), ports.Rich())
}

// switchEffort применяет и сохраняет уровень и возвращает текст ответа.
// Уровень действует со следующего запуска агента: идущий шаг задачи или ход
// диалога продолжают с тем уровнем, с которым были запущены.
func switchEffort(level effort.Level, lang string) string {
	config.ProjectState.SetEffort(level)

	if st := domain.GlobalTaskManager.Storage(); st != nil {
		if err := st.SetSetting(context.Background(), effortSettingKey, level.String()); err != nil {
			log.Printf("warning: cannot save the effort level: %v", err)
		}
	}

	return i18n.Tf(lang, "effort.switched", html.EscapeString(level.String())) + effortSupportNote(lang)
}

// effortSupportNote возвращает предупреждение, если активный адаптер уровень не
// применяет (agy в режиме api), и пустую строку в остальных случаях.
func effortSupportNote(lang string) string {
	framework, err := agentFrameworkFor(ActiveAgentName())
	if err != nil {
		return ""
	}
	aware, ok := framework.(ports.EffortAwareFramework)
	if !ok || aware.SupportsEffort() {
		return ""
	}
	return i18n.Tf(lang, "effort.unsupported",
		html.EscapeString(ActiveAgentName()), html.EscapeString(config.ProjectState.GetExecutionMode()))
}

// restoreEffort восстанавливает уровень из SQLite при старте бота. Значение
// проверяется так же, как ввод пользователя: пустое или незнакомое даёт auto.
func restoreEffort(saved string) {
	level, ok := effort.Parse(saved)
	if !ok {
		if strings.TrimSpace(saved) != "" {
			log.Printf("stored effort level %q rejected, using auto", saved)
		}
		level = effort.Auto
	}
	config.ProjectState.SetEffort(level)
	if !level.IsAuto() {
		log.Printf("restored the effort level from SQLite: %s", level)
	}
}
