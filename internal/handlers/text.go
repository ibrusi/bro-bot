package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
)

// Маршрутизация обычного текстового сообщения: ответ на вопрос агента,
// дополнение задачи или диалог с агентом.

// handleText — обработчик обычного текстового сообщения.
func handleText(s ports.Session) error {
	return handleTextWithContent(s, s.Text())
}

// resolveImplicitTaskTarget определяет задачу, которой адресовано сообщение/файл без явного Reply:
// ожидающая ответа задача (в том числе из другого проекта) имеет приоритет над выполняющейся задачей в фокусе.
func resolveImplicitTaskTarget() (target *domain.TaskSession, otherWaiting []*domain.TaskSession, staleNote string) {
	active := domain.GlobalTaskManager.GetActiveTask()
	waiting := domain.GlobalTaskManager.GetWaitingInputTasks()

	if len(waiting) > 1 {
		type waitEntry struct {
			task *domain.TaskSession
			view domain.TaskView
		}
		entries := make([]waitEntry, 0, len(waiting))
		for _, w := range waiting {
			entries = append(entries, waitEntry{task: w, view: w.Snapshot()})
		}
		sort.Slice(entries, func(i, j int) bool {
			if !entries[i].view.QuestionAskedAt.Equal(entries[j].view.QuestionAskedAt) {
				return entries[i].view.QuestionAskedAt.After(entries[j].view.QuestionAskedAt)
			}
			return entries[i].view.ID > entries[j].view.ID
		})
		for i, e := range entries {
			waiting[i] = e.task
		}
	}

	if active != nil {
		activeView := active.Snapshot()
		if activeView.Status == domain.TaskStatusWaitingInput || hasFreshPendingQuestion(active) {
			for _, w := range waiting {
				if w.Snapshot().ID != activeView.ID {
					otherWaiting = append(otherWaiting, w)
				}
			}
			return active, otherWaiting, ""
		}
	}

	if len(waiting) > 0 {
		return waiting[0], waiting[1:], ""
	}

	if active != nil {
		if active.IsActive() {
			return active, nil, ""
		}
		if isAwaitingAnswer(active) {
			activeID := active.Snapshot().ID
			return nil, nil, i18n.Tf(uiLang(), "chat.task_still_waiting", activeID, activeID)
		}
	}

	return nil, nil, ""
}

func sendOtherWaitingHint(s ports.Session, otherWaiting []*domain.TaskSession) {
	if len(otherWaiting) == 0 {
		return
	}
	var items []string
	for _, w := range otherWaiting {
		v := w.Snapshot()
		items = append(items, fmt.Sprintf("#%d (%s)", v.ID, html.EscapeString(v.Project)))
	}
	_ = s.Send(i18n.Tf(uiLang(), "task.other_waiting_tasks_hint", strings.Join(items, ", ")), ports.Rich())
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

	// 2. Проверяем ожидающую ответа (в том числе в другом проекте) или активную задачу в фокусе
	target, otherWaiting, note := resolveImplicitTaskTarget()
	if target != nil {
		err := handleAddFollowupToTask(s, target.Snapshot().ID, userText)
		if err == nil {
			sendOtherWaitingHint(s, otherWaiting)
		}
		return err
	}

	// 3. Нет активных задач — диалоговый режим или создание задачи
	if config.ProjectState.GetInteractionMode() == domain.InteractionModeTask {
		return handleCreateNewTask(s, userText)
	}
	return handleTextInChatModeWithNote(s, userText, note)
}
