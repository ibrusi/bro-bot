package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"strconv"
	"strings"
)

// Маршрутизация обычного текстового сообщения: ответ на вопрос агента,
// дополнение задачи или диалог с агентом.

// handleText — обработчик обычного текстового сообщения.
func handleText(s ports.Session) error {
	return handleTextWithContent(s, s.Text())
}

// handleTextWithContent содержит логику обработки пользовательского текста (набранного вручную или распознанного из голоса).
func handleTextWithContent(s ports.Session, rawText string) error {
	userText := strings.TrimSpace(rawText)
	if strings.HasPrefix(userText, "/planfile_") || strings.HasPrefix(userText, "/plan_") {
		rawID := strings.TrimPrefix(userText, "/planfile_")
		rawID = strings.TrimPrefix(rawID, "/plan_")
		if atIdx := strings.Index(rawID, "@"); atIdx != -1 {
			rawID = rawID[:atIdx]
		}
		if id, err := strconv.Atoi(strings.TrimSpace(rawID)); err == nil {
			target := domain.GlobalTaskManager.GetTask(id)
			if target != nil {
				return sendTaskPlanDocument(s, target, uiLang())
			}
			return s.Send(i18n.Tf(uiLang(), "task.not_found", id), ports.Rich())
		}
	}

	if strings.HasPrefix(userText, "/") {
		return nil
	}

	// 1. Проверяем, является ли сообщение ответом (Reply) на статус/вопрос конкретной задачи
	if msg := s.Message(); msg != nil && msg.ReplyTo != nil {
		if task := domain.GlobalTaskManager.GetTaskByMessageID(msg.ReplyTo.ID); task != nil {
			return handleAddFollowupToTask(s, task.Snapshot().ID, userText)
		}
	}

	// 2. Проверяем активную задачу в фокусе
	note := ""
	active := domain.GlobalTaskManager.GetActiveTask()
	if active != nil {
		if active.IsActive() {
			return handleAddFollowupToTask(s, active.Snapshot().ID, userText)
		}
		// Приостановленная задача забирает сообщение, только если она действительно ждёт ответа
		// на свежий вопрос. Иначе задача, зависшая после перезапуска бота, навсегда
		// перехватывала бы весь диалог.
		if hasFreshPendingQuestion(active) {
			return handleAddFollowupToTask(s, active.Snapshot().ID, userText)
		}
		if isAwaitingAnswer(active) {
			activeID := active.Snapshot().ID
			note = i18n.Tf(uiLang(), "chat.task_still_waiting", activeID, activeID)
		}
	}

	// 3. Нет активных задач — диалоговый режим или создание задачи
	if config.ProjectState.GetInteractionMode() == domain.InteractionModeTask {
		return handleCreateNewTask(s, userText)
	}
	return handleTextInChatModeWithNote(s, userText, note)
}
