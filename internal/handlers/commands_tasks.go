package handlers

import (
	"bro-bot/internal/adapters/transcript"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/ports"
	"bro-bot/internal/system"
	"bro-bot/internal/utils"
	"bro-bot/internal/i18n"
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Команды и кнопки управления задачами: создание, статус, дополнение,
// ответы на вопросы агента, пауза, возобновление и отмена.

// handleTasks — обработчик команды /tasks.
func handleTasks(s ports.Session) error {
	msg, menu := domain.FormatTasksList(domain.GlobalTaskManager, config.ProjectState.GetLanguage())
	return s.Send(msg, ports.RichWith(menu))
}

// onTaskSel — обработчик кнопки task_sel.
func onTaskSel(s ports.Session) error {
	idStr := s.Callback().Payload
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return s.Respond("Некорректный номер задачи")
	}
	task, err := domain.GlobalTaskManager.SetActiveTask(id)
	if err != nil {
		return s.Respond(err.Error())
	}
	syncLegacySession(task)
	_ = s.Respond(fmt.Sprintf("Выбрана задача #%d", id))

	details := domain.FormatTaskDetails(task, true, config.ProjectState.GetLanguage())
	markup := domain.BuildTaskDetailsMarkup(task, config.ProjectState.GetLanguage())
	conflictNote := ""
	if HasAgentConflict(task, ActiveAgentName()) {
		view := task.Snapshot()
		tAgent := view.Agent
		if tAgent == "" {
			tAgent = "agy"
		}
		conflictNote = fmt.Sprintf("\n\n⚠️ <i>Внимание: задача использует сессию агента <b>%s</b>, а активен <b>%s</b>.</i>", html.EscapeString(tAgent), html.EscapeString(ActiveAgentName()))
	}
	return s.Send(fmt.Sprintf("🎯 <b>Фокус переключен на задачу #%d!</b>\n\n%s%s", id, details, conflictNote), ports.RichWith(markup))
}

// handleStatus — обработчик команды /status.
func handleStatus(s ports.Session) error {
	args := s.Args()
	var target *domain.TaskSession
	if len(args) > 0 {
		first := strings.TrimPrefix(args[0], "#")
		if id, err := strconv.Atoi(first); err == nil {
			target = domain.GlobalTaskManager.GetTask(id)
			if target == nil {
				return s.Send(fmt.Sprintf("❌ Задача #%d не найдена. Список задач: /tasks", id), ports.Rich())
			}
		}
	}

	if target == nil {
		target = domain.GlobalTaskManager.GetActiveTask()
	}

	if target == nil || (!target.IsActive() && len(args) == 0) {
		idleMsg := "💤 <b>Сейчас нет активных задач. Агент простаивает.</b>"
		lastSnippet := domain.GlobalTokenTracker.GetLastTaskStatusBlock()
		if lastSnippet != "" {
			idleMsg += "\n\n" + lastSnippet
		}
		report := system.CollectResourceReport(false)
		resSnippet := system.FormatCompactResourceSnippet(report)
		if resSnippet != "" {
			idleMsg += "\n\n" + resSnippet
		}
		idleMsg += "\n\n💡 <i>Отправьте задачу сообщением в чат, /tasks для списка или /new для новой задачи.</i>"
		return s.Send(idleMsg, ports.Rich())
	}

	targetID := target.Snapshot().ID
	activeTask := domain.GlobalTaskManager.GetActiveTask()
	isActiveFocus := (activeTask != nil && activeTask.Snapshot().ID == targetID)
	msg := domain.FormatTaskDetails(target, isActiveFocus, config.ProjectState.GetLanguage())

	tokenBlock := domain.GlobalTokenTracker.GetCurrentTaskStatusBlock()
	if tokenBlock != "" {
		msg += "\n\n" + tokenBlock
	}
	report := system.CollectResourceReport(false)
	resSnippet := system.FormatCompactResourceSnippet(report)
	if resSnippet != "" {
		msg += "\n\n" + resSnippet
	}

	otherTasks := domain.GlobalTaskManager.GetActiveOrQueuedTasks()
	if len(otherTasks) > 1 {
		var otherParts []string
		for _, ot := range otherTasks {
			if ot.ID != targetID {
				otView := ot.Snapshot()
				otherParts = append(otherParts, fmt.Sprintf("<b>#%d</b> (%s <code>%s</code>)", otView.ID, otView.Status.Emoji(), html.EscapeString(otView.Project)))
			}
		}
		if len(otherParts) > 0 {
			msg += fmt.Sprintf("\n\n📌 <b>Другие задачи:</b> %s\n<i>(Переключить: /task &lt;id&gt; или /tasks)</i>", strings.Join(otherParts, " | "))
		}
	}

	statusMarkup := domain.BuildTaskDetailsMarkup(target, config.ProjectState.GetLanguage())
	return s.Send(msg, ports.RichWith(statusMarkup))
}

// handleTask — обработчик команды /task.
func handleTask(s ports.Session) error {
	args := s.Args()
	if len(args) == 0 {
		active := domain.GlobalTaskManager.GetActiveTask()
		if active == nil {
			return s.Send("💤 Нет активных задач. Создать: <code>/new &lt;текст&gt;</code>", ports.Rich())
		}
		details := domain.FormatTaskDetails(active, true, config.ProjectState.GetLanguage())
		markup := domain.BuildTaskDetailsMarkup(active, config.ProjectState.GetLanguage())
		conflictNote := ""
		if HasAgentConflict(active, ActiveAgentName()) {
			activeView := active.Snapshot()
			tAgent := activeView.Agent
			if tAgent == "" {
				tAgent = "agy"
			}
			conflictNote = fmt.Sprintf("\n\n⚠️ <i>Внимание: задача использует сессию агента <b>%s</b>, а активен <b>%s</b>.</i>", html.EscapeString(tAgent), html.EscapeString(ActiveAgentName()))
		}
		return s.Send(details+conflictNote, ports.RichWith(markup))
	}

	first := strings.TrimPrefix(args[0], "#")
	id, err := strconv.Atoi(first)
	if err != nil {
		if strings.ToLower(first) == "new" && len(args) > 1 {
			prompt := strings.Join(args[1:], " ")
			return handleCreateNewTask(s, prompt)
		}
		if strings.ToLower(first) == "plan" && len(args) > 1 {
			prompt := strings.Join(args[1:], " ")
			return handleCreatePlanTask(s, prompt)
		}
		return s.Send("Использование:\n• <code>/task &lt;id&gt;</code> — переключить активную задачу\n• <code>/task &lt;id&gt; &lt;текст&gt;</code> — дополнить задачу", ports.Rich())
	}

	// Если передан текст дополнения: /task 2 сделай ещё это
	if len(args) > 1 {
		followupText := strings.TrimSpace(strings.Join(args[1:], " "))
		return handleAddFollowupToTask(s, id, followupText)
	}

	task, err := domain.GlobalTaskManager.SetActiveTask(id)
	if err != nil {
		return s.Send(fmt.Sprintf("❌ %s", err.Error()), ports.Rich())
	}
	syncLegacySession(task)

	details := domain.FormatTaskDetails(task, true, config.ProjectState.GetLanguage())
	markup := domain.BuildTaskDetailsMarkup(task, config.ProjectState.GetLanguage())
	conflictNote := ""
	if HasAgentConflict(task, ActiveAgentName()) {
		view2 := task.Snapshot()
		tAgent := view2.Agent
		if tAgent == "" {
			tAgent = "agy"
		}
		conflictNote = fmt.Sprintf("\n\n⚠️ <i>Внимание: задача использует сессию агента <b>%s</b>, а активен <b>%s</b>.</i>", html.EscapeString(tAgent), html.EscapeString(ActiveAgentName()))
	}
	return s.Send(fmt.Sprintf("🎯 <b>Фокус переключен на задачу #%d!</b>\n\n%s%s", id, details, conflictNote), ports.RichWith(markup))
}

// handleAdd — обработчик команды /add.
func handleAdd(s ports.Session) error {
	args := s.Args()
	if len(args) == 0 {
		return s.Send("Использование:\n• <code>/add &lt;id&gt; &lt;текст&gt;</code> — дополнить задачу #id\n• <code>/add &lt;текст&gt;</code> — дополнить активную задачу", ports.Rich())
	}

	first := strings.TrimPrefix(args[0], "#")
	if id, err := strconv.Atoi(first); err == nil && len(args) > 1 {
		followupText := strings.TrimSpace(strings.Join(args[1:], " "))
		return handleAddFollowupToTask(s, id, followupText)
	}

	active := domain.GlobalTaskManager.GetActiveTask()
	if active == nil {
		return s.Send("❌ Нет активной задачи. Укажите ID: <code>/add &lt;id&gt; &lt;текст&gt;</code>", ports.Rich())
	}
	followupText := strings.TrimSpace(strings.Join(args, " "))
	return handleAddFollowupToTask(s, active.Snapshot().ID, followupText)
}

// handleNew — обработчик команды /new.
func handleNew(s ports.Session) error {
	args := s.Args()
	if len(args) == 0 {
		return s.Send("Использование: <code>/new &lt;описание задачи&gt;</code>\n(или <code>/new [проект] [агент] &lt;описание&gt;</code>)", ports.Rich())
	}
	text := strings.TrimSpace(strings.Join(args, " "))
	return handleCreateNewTask(s, text)
}

// handleHistory — обработчик команды /history.
func handleHistory(s ports.Session) error {
	args := s.Args()
	var target *domain.TaskSession
	if len(args) > 0 {
		first := strings.TrimPrefix(args[0], "#")
		if id, err := strconv.Atoi(first); err == nil {
			target = domain.GlobalTaskManager.GetTask(id)
			if target == nil {
				return s.Send(fmt.Sprintf("❌ Задача #%d не найдена. Список задач: /tasks", id), ports.Rich())
			}
		}
	}

	if target == nil {
		target = domain.GlobalTaskManager.GetActiveTask()
	}

	if target == nil {
		return s.Send("❌ Нет активных задач. Список задач: /tasks", ports.Rich())
	}

	return sendTaskHistory(s, target)
}

// handleCancel — обработчик команды /cancel.
func handleCancel(s ports.Session) error {
	args := s.Args()
	var targetID int
	if len(args) > 0 {
		idStr := strings.TrimPrefix(args[0], "#")
		var err error
		targetID, err = strconv.Atoi(idStr)
		if err != nil {
			return s.Send("Укажите номер задачи. Пример: <code>/cancel 2</code>", ports.Rich())
		}
	} else {
		active := domain.GlobalTaskManager.GetActiveTask()
		if active == nil || !active.IsActive() {
			return s.Send("Сейчас нет активных задач.", nil)
		}
		targetID = active.Snapshot().ID
	}

	task, err := domain.GlobalTaskManager.CancelTask(targetID)
	if err != nil {
		return s.Send(fmt.Sprintf("❌ %s", err.Error()), ports.Rich())
	}

	syncLegacySession(domain.GlobalTaskManager.GetActiveTask())
	domain.GlobalTokenTracker.CancelTask()

	view := task.Snapshot()
	checkAndStartQueuedTask(s.Messenger(), view.Project, config.ProjectsRoot)

	return s.Send(fmt.Sprintf("🛑 Задача <b>#%d</b> (<code>%s</code>) остановлена.", view.ID, html.EscapeString(view.Project)), ports.Rich())
}

// onQuestionChoice — обработчик кнопки q_choice.
func onQuestionChoice(s ports.Session) error {
	parts := strings.Split(s.Callback().Payload, ":")
	if len(parts) < 2 {
		return s.Respond("Некорректные данные кнопки")
	}
	taskID, err1 := strconv.Atoi(parts[0])
	optIdx, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return s.Respond("Некорректные параметры")
	}

	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Respond("Задача не найдена")
	}

	view3 := task.Snapshot()
	status := view3.Status
	var chosenText string
	if optIdx >= 0 && optIdx < len(view3.QuestionOptions) {
		chosenText = view3.QuestionOptions[optIdx]
	}
	cmdIsNil := !task.HasLiveProcess()

	if chosenText == "" {
		return s.Respond("Вариант не найден")
	}

	_ = s.Respond(fmt.Sprintf("Выбрано: %s", utils.TruncateString(chosenText, 25)))

	if cb := s.Callback(); cb != nil && cb.MessageText != "" {
		_ = s.Edit(fmt.Sprintf("%s\n\n✅ <b>Выбран вариант %d:</b> <i>«%s»</i>",
			cb.MessageText, optIdx+1, html.EscapeString(chosenText)), ports.Rich())
	}

	if status == domain.TaskStatusWaitingInput {
		if !cmdIsNil {
			task.DeliverAnswer(chosenText)
			return s.Send(fmt.Sprintf("💬 <b>Выбран вариант %d для задачи #%d:</b>\n<i>«%s»</i>", optIdx+1, taskID, html.EscapeString(chosenText)), ports.Rich())
		}
		if HasAgentConflict(task, ActiveAgentName()) {
			task.Update(func(t *domain.TaskSession) {
				t.CurrentPrompt = chosenText
			})
			domain.GlobalTaskManager.SaveTask(task)
			return sendAgentConflictDialog(s, task)
		}
		resumedTask, err := domain.GlobalTaskManager.ResumeTask(taskID, chosenText)
		if err != nil {
			return s.Send(fmt.Sprintf("❌ Ошибка возобновления задачи #%d: %s", taskID, err.Error()), ports.Rich())
		}
		syncLegacySession(resumedTask)
		resumedView := resumedTask.Snapshot()
		resumedStatus := resumedView.Status
		projectName := resumedView.Project

		if resumedStatus == domain.TaskStatusQueued {
			return s.Send(fmt.Sprintf("⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code> с ответом:\n<i>«%s»</i>",
				taskID, html.EscapeString(projectName), html.EscapeString(chosenText)), ports.Rich())
		}
		workDir := filepath.Join(config.ProjectsRoot, projectName)
		go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir, config.ProjectsRoot)
		return s.Send(fmt.Sprintf("▶️ <b>Задача #%d возобновлена в <code>%s</code> с ответом:</b>\n<i>«%s»</i>",
			taskID, html.EscapeString(projectName), html.EscapeString(chosenText)), ports.Rich())
	} else if status == domain.TaskStatusPaused {
		if HasAgentConflict(task, ActiveAgentName()) {
			task.Update(func(t *domain.TaskSession) {
				t.CurrentPrompt = chosenText
			})
			domain.GlobalTaskManager.SaveTask(task)
			return sendAgentConflictDialog(s, task)
		}
		resumedTask, err := domain.GlobalTaskManager.ResumeTask(taskID, chosenText)
		if err != nil {
			return s.Send(fmt.Sprintf("❌ Ошибка возобновления задачи #%d: %s", taskID, err.Error()), ports.Rich())
		}
		syncLegacySession(resumedTask)
		resumedView2 := resumedTask.Snapshot()
		resumedStatus := resumedView2.Status
		projectName := resumedView2.Project

		if resumedStatus == domain.TaskStatusQueued {
			return s.Send(fmt.Sprintf("⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code> с ответом:\n<i>«%s»</i>",
				taskID, html.EscapeString(projectName), html.EscapeString(chosenText)), ports.Rich())
		}
		workDir := filepath.Join(config.ProjectsRoot, projectName)
		go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir, config.ProjectsRoot)
		return s.Send(fmt.Sprintf("▶️ <b>Задача #%d возобновлена в <code>%s</code> с ответом:</b>\n<i>«%s»</i>",
			taskID, html.EscapeString(projectName), html.EscapeString(chosenText)), ports.Rich())
	}

	lang := config.ProjectState.GetLanguage()
	return s.Send(fmt.Sprintf("ℹ️ Задача #%d сейчас не ожидает ответа (текущий статус: %s).", taskID, i18n.TaskStatusTitle(string(status), lang)), nil)
}

// onQuestionPause — обработчик кнопки q_pause.
func onQuestionPause(s ports.Session) error {
	idStr := s.Callback().Payload
	taskID, err := strconv.Atoi(idStr)
	if err != nil {
		return s.Respond("Некорректный номер задачи")
	}

	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Respond("Задача не найдена")
	}

	if !task.PauseTask() {
		return s.Respond("Задача не ожидает ответа")
	}

	_ = s.Respond(fmt.Sprintf("Задача #%d приостановлена", taskID))
	syncLegacySession(task)

	if cb := s.Callback(); cb != nil && cb.MessageText != "" {
		_ = s.Edit(fmt.Sprintf("%s\n\n⏸ <i>Задача приостановлена пользователем.</i>", cb.MessageText), ports.Rich())
	}

	view4 := task.Snapshot()
	projectName := view4.Project

	checkAndStartQueuedTask(s.Messenger(), projectName, config.ProjectsRoot)

	resumeMenu := buildResumeMarkup(taskID)
	msg := fmt.Sprintf("⏸ <b>Задача #%d (<code>%s</code>) приостановлена.</b>\nОчередь проекта освобождена.\nНажмите кнопку ниже или используйте <code>/resume %d &lt;ответ&gt;</code>, чтобы продолжить.", taskID, html.EscapeString(projectName), taskID)
	return s.Send(msg, ports.RichWith(resumeMenu))
}

// onQuestionResume — обработчик кнопки q_resume.
func onQuestionResume(s ports.Session) error {
	idStr := s.Callback().Payload
	taskID, err := strconv.Atoi(idStr)
	if err != nil {
		return s.Respond("Некорректный номер задачи")
	}

	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Respond("Задача не найдена")
	}

	view5 := task.Snapshot()
	status := view5.Status
	lastQ := view5.LastQuestion
	opts := append([]string(nil), view5.QuestionOptions...)
	proj := view5.Project

	_ = s.Respond("")

	if status != domain.TaskStatusPaused && status != domain.TaskStatusWaitingInput {
		lang := config.ProjectState.GetLanguage()
		return s.Send(fmt.Sprintf("ℹ️ Задача #%d сейчас не ожидает ответа (текущий статус: %s).", taskID, i18n.TaskStatusTitle(string(status), lang)), nil)
	}

	if HasAgentConflict(task, ActiveAgentName()) {
		return sendAgentConflictDialog(s, task)
	}

	if lastQ != "" && len(opts) > 0 {
		menu := buildQuestionMarkup(task)
		promptMsg := fmt.Sprintf("❓ <b>Вопрос по задаче #%d (<code>%s</code>):</b>\n\n%s\n\n<i>Выберите вариант кнопкой или ответьте сообщением в чат:</i>",
			taskID, html.EscapeString(proj), utils.MarkdownToTelegramHTML(lastQ))
		return s.Send(promptMsg, ports.RichWith(menu))
	}

	if lastQ != "" {
		return s.Send(fmt.Sprintf("💡 Задача #%d ждёт ответа на вопрос:\n\n<i>«%s»</i>\n\nОтправьте ответ сообщением в чат или <code>/resume %d &lt;ответ&gt;</code>.",
			taskID, html.EscapeString(lastQ), taskID), ports.Rich())
	}

	// Если вопроса не было (например, пауза по таймауту выполнения шага) — возобновляем выполнение
	resumedTask, err := domain.GlobalTaskManager.ResumeTask(taskID, "")
	if err != nil {
		return s.Send(fmt.Sprintf("❌ Не удалось возобновить задачу #%d: %v", taskID, err), ports.Rich())
	}
	syncLegacySession(resumedTask)

	resumedView3 := resumedTask.Snapshot()
	resStatus := resumedView3.Status

	if resStatus == domain.TaskStatusQueued {
		return s.Send(fmt.Sprintf("⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code>.\nОна запустится автоматически, как только проект освободится.", taskID, html.EscapeString(proj)), ports.Rich())
	}

	view6 := task.Snapshot()
	tAgent := view6.Agent
	if tAgent == "" {
		tAgent = "agy"
	}

	workDir := filepath.Join(config.ProjectsRoot, proj)
	go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir, config.ProjectsRoot)
	return s.Send(fmt.Sprintf("▶️ <b>Задача #%d (<code>%s</code>) возобновлена с сохранённой сессии %s!</b>", taskID, html.EscapeString(proj), html.EscapeString(tAgent)), ports.Rich())
}

// onTaskAgentRestart — обработчик кнопки task_agent_restart.
func onTaskAgentRestart(s ports.Session) error {
	idStr := s.Callback().Payload
	taskID, err := strconv.Atoi(idStr)
	if err != nil {
		return s.Respond("Некорректный номер задачи")
	}

	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Respond("Задача не найдена")
	}

	_ = s.Respond(fmt.Sprintf("Перезапуск с агентом %s...", ActiveAgentName()))

	domain.GlobalTaskManager.ClearTaskConversationID(taskID)
	domain.GlobalTaskManager.SetTaskAgent(taskID, ActiveAgentName())

	var proj string
	task.Update(func(t *domain.TaskSession) {
		proj = t.Project
		t.ConversationID = ""
		t.Agent = ActiveAgentName()
		if t.RequiresPlan && !t.PlanApproved {
			t.Status = domain.TaskStatusPlanning
		} else {
			t.Status = domain.TaskStatusRunning
		}
		if t.CurrentPrompt == "" {
			t.CurrentPrompt = t.InitialPrompt
		}
		t.LastPRURL = ""
		t.LastQuestion = ""
		t.QuestionOptions = nil
		t.StartedAt = time.Now()
		t.RecentLogs = nil
		t.ResetOutputLocked()
	})

	syncLegacySession(task)
	domain.GlobalTaskManager.SaveTask(task)

	if cb := s.Callback(); cb != nil && cb.MessageText != "" {
		_ = s.Edit(fmt.Sprintf("%s\n\n🔄 <b>Выбрано: Начать заново с агентом %s.</b>", cb.MessageText, html.EscapeString(ActiveAgentName())), ports.Rich())
	}

	workDir := filepath.Join(config.ProjectsRoot, proj)
	go runAgentTaskPipeline(s.Messenger(), s.Chat(), task, workDir, config.ProjectsRoot)
	return s.Send(fmt.Sprintf("🔄 <b>Задача #%d перезапущена с агентом %s</b> (новая сессия в <code>%s</code>).", taskID, html.EscapeString(ActiveAgentName()), html.EscapeString(proj)), ports.Rich())
}

// onTaskAgentSwitch — обработчик кнопки task_agent_switch.
func onTaskAgentSwitch(s ports.Session) error {
	idStr := s.Callback().Payload
	taskID, err := strconv.Atoi(idStr)
	if err != nil {
		return s.Respond("Некорректный номер задачи")
	}

	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Respond("Задача не найдена")
	}

	view7 := task.Snapshot()
	targetAgent := view7.Agent
	if targetAgent == "" {
		targetAgent = "agy"
	}
	prompt := view7.CurrentPrompt
	proj := view7.Project

	_ = s.Respond(fmt.Sprintf("Переключение на %s...", targetAgent))

	switchMsg, err := SwitchActiveAgent(targetAgent)
	if err != nil {
		return s.Send(fmt.Sprintf("❌ Ошибка переключения агента: %v", err), ports.Rich())
	}

	resumedTask, err := domain.GlobalTaskManager.ResumeTask(taskID, prompt)
	if err != nil {
		return s.Send(fmt.Sprintf("❌ Ошибка возобновления задачи #%d: %s", taskID, err.Error()), ports.Rich())
	}
	syncLegacySession(resumedTask)

	if cb := s.Callback(); cb != nil && cb.MessageText != "" {
		_ = s.Edit(fmt.Sprintf("%s\n\n🔀 <b>Выбрано: Переключиться на %s.</b>", cb.MessageText, html.EscapeString(targetAgent)), ports.Rich())
	}

	resumedView4 := resumedTask.Snapshot()
	resStatus := resumedView4.Status

	if resStatus == domain.TaskStatusQueued {
		return s.Send(fmt.Sprintf("%s\n\n⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code>.", switchMsg, taskID, html.EscapeString(proj)), ports.Rich())
	}

	workDir := filepath.Join(config.ProjectsRoot, proj)
	go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir, config.ProjectsRoot)
	return s.Send(fmt.Sprintf("%s\n\n▶️ <b>Задача #%d (<code>%s</code>) возобновлена с агентом %s!</b>", switchMsg, taskID, html.EscapeString(proj), html.EscapeString(targetAgent)), ports.Rich())
}

// handlePause — обработчик команды /pause.
func handlePause(s ports.Session) error {
	args := s.Args()
	var targetID int
	if len(args) > 0 {
		idStr := strings.TrimPrefix(args[0], "#")
		var err error
		targetID, err = strconv.Atoi(idStr)
		if err != nil {
			return s.Send("Укажите номер задачи. Пример: <code>/pause 2</code>", ports.Rich())
		}
	} else {
		active := domain.GlobalTaskManager.GetActiveTask()
		if active == nil {
			return s.Send("Сейчас нет активных задач.", nil)
		}
		targetID = active.Snapshot().ID
	}

	task := domain.GlobalTaskManager.GetTask(targetID)
	if task == nil {
		return s.Send(fmt.Sprintf("❌ Задача #%d не найдена.", targetID), ports.Rich())
	}

	if !task.PauseTask() {
		view8 := task.Snapshot()
		st := view8.Status
		lang := config.ProjectState.GetLanguage()
		return s.Send(fmt.Sprintf("ℹ️ Задачу #%d нельзя приостановить (текущий статус: %s). Пауза доступна при ожидании ответа.", targetID, i18n.TaskStatusTitle(string(st), lang)), ports.Rich())
	}

	syncLegacySession(task)
	view9 := task.Snapshot()
	projectName := view9.Project

	checkAndStartQueuedTask(s.Messenger(), projectName, config.ProjectsRoot)

	resumeMenu := buildResumeMarkup(targetID)
	msg := fmt.Sprintf("⏸ <b>Задача #%d (<code>%s</code>) приостановлена.</b>\nОчередь проекта освобождена.\nЧтобы возобновить, используйте <code>/resume %d &lt;ответ&gt;</code> или кнопку ниже.", targetID, html.EscapeString(projectName), targetID)
	return s.Send(msg, ports.RichWith(resumeMenu))
}

// handleResume — обработчик команды /resume.
func handleResume(s ports.Session) error {
	args := s.Args()
	var targetID int
	var answer string

	if len(args) > 0 {
		if id, err := strconv.Atoi(args[0]); err == nil {
			targetID = id
			if len(args) > 1 {
				answer = strings.Join(args[1:], " ")
			}
		} else {
			answer = strings.Join(args, " ")
		}
	}

	if targetID == 0 {
		all := domain.GlobalTaskManager.ListTasks()
		for i := len(all) - 1; i >= 0; i-- {
			t := all[i]
			st := t.Snapshot().Status
			if st == domain.TaskStatusPaused || st == domain.TaskStatusWaitingInput || st == domain.TaskStatusFailed || st == domain.TaskStatusCancelled {
				targetID = t.ID
				break
			}
		}
	}

	if targetID == 0 {
		active := domain.GlobalTaskManager.GetActiveTask()
		if active != nil {
			targetID = active.Snapshot().ID
		}
	}

	if targetID == 0 {
		return s.Send("❌ Не указана задача для возобновления. Использование: <code>/resume &lt;id&gt; [ответ]</code>", ports.Rich())
	}

	task := domain.GlobalTaskManager.GetTask(targetID)
	if task == nil {
		return s.Send(fmt.Sprintf("❌ Задача #%d не найдена.", targetID), ports.Rich())
	}

	if HasAgentConflict(task, ActiveAgentName()) {
		if answer != "" {
			task.Update(func(t *domain.TaskSession) {
				t.CurrentPrompt = answer
			})
			domain.GlobalTaskManager.SaveTask(task)
		}
		return sendAgentConflictDialog(s, task)
	}

	view10 := task.Snapshot()
	status := view10.Status

	if status == domain.TaskStatusWaitingInput {
		if answer != "" {
			view11 := task.Snapshot()
			proj := view11.Project
			cmdIsNil := !task.HasLiveProcess()

			if !cmdIsNil {
				task.DeliverAnswer(answer)
				return s.Send(fmt.Sprintf("💬 <b>Ответ передан задаче #%d</b> (<code>%s</code>)...", targetID, html.EscapeString(proj)), ports.Rich())
			}

			resumedTask, err := domain.GlobalTaskManager.ResumeTask(targetID, answer)
			if err != nil {
				return s.Send(fmt.Sprintf("❌ Ошибка возобновления задачи #%d: %s", targetID, err.Error()), ports.Rich())
			}
			syncLegacySession(resumedTask)
			resumedView5 := resumedTask.Snapshot()
			resStatus := resumedView5.Status

			if resStatus == domain.TaskStatusQueued {
				return s.Send(fmt.Sprintf("⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code> с ответом:\n<i>«%s»</i>", targetID, html.EscapeString(proj), html.EscapeString(answer)), ports.Rich())
			}

			workDir := filepath.Join(config.ProjectsRoot, proj)
			go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir, config.ProjectsRoot)
			return s.Send(fmt.Sprintf("▶️ <b>Задача #%d возобновлена в <code>%s</code> с ответом:</b>\n<i>«%s»</i>", targetID, html.EscapeString(proj), html.EscapeString(answer)), ports.Rich())
		}
		menu := buildQuestionMarkup(task)
		return s.Send(fmt.Sprintf("❓ Задача #%d ждёт ответа. Выберите вариант или отправьте: <code>/resume %d &lt;ответ&gt;</code>", targetID, targetID), ports.RichWith(menu))
	}

	resumedTask, err := domain.GlobalTaskManager.ResumeTask(targetID, answer)
	if err != nil {
		return s.Send(fmt.Sprintf("❌ Ошибка возобновления задачи #%d: %s", targetID, err.Error()), ports.Rich())
	}
	syncLegacySession(resumedTask)

	resumedView6 := resumedTask.Snapshot()
	resStatus := resumedView6.Status
	proj := resumedView6.Project

	if resStatus == domain.TaskStatusQueued {
		return s.Send(fmt.Sprintf("⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code>.\nОна запустится автоматически, как только текущая задача завершится.", targetID, html.EscapeString(proj)), ports.Rich())
	}

	workDir := filepath.Join(config.ProjectsRoot, proj)
	go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir, config.ProjectsRoot)
	return s.Send(fmt.Sprintf("▶️ <b>Задача #%d возобновлена в <code>%s</code>!</b>", targetID, html.EscapeString(proj)), ports.Rich())
}

// handleRetry — обработчик команды /retry.
func handleRetry(s ports.Session) error {
	args := s.Args()
	var targetID int
	if len(args) > 0 {
		idStr := strings.TrimPrefix(args[0], "#")
		targetID, _ = strconv.Atoi(idStr)
	}
	if targetID == 0 {
		active := domain.GlobalTaskManager.GetActiveTask()
		if active != nil {
			targetID = active.Snapshot().ID
		}
	}
	if targetID == 0 {
		all := domain.GlobalTaskManager.ListTasks()
		for i := len(all) - 1; i >= 0; i-- {
			t := all[i]
			st := t.Snapshot().Status
			if st == domain.TaskStatusPaused || st == domain.TaskStatusFailed {
				targetID = t.ID
				break
			}
		}
	}
	if targetID == 0 {
		return s.Send("❌ Укажите номер задачи. Пример: <code>/retry 2</code>", ports.Rich())
	}

	task := domain.GlobalTaskManager.GetTask(targetID)
	if task == nil {
		return s.Send(fmt.Sprintf("❌ Задача #%d не найдена.", targetID), ports.Rich())
	}

	// Проверка статуса и сброс задачи — одним куском: между ними задачу нельзя
	// успеть запустить заново.
	var (
		running  bool
		proj     string
		newAgent = ActiveAgentName()
	)
	task.Update(func(t *domain.TaskSession) {
		if t.Status == domain.TaskStatusRunning || t.Status == domain.TaskStatusPlanning {
			running = true
			return
		}
		proj = t.Project
		t.ConversationID = ""
		t.Agent = newAgent
		t.Status = domain.TaskStatusRunning
		if t.RequiresPlan && !t.PlanApproved {
			t.Status = domain.TaskStatusPlanning
		}
		t.CurrentPrompt = t.InitialPrompt
		t.LastPRURL = ""
		t.LastQuestion = ""
		t.QuestionOptions = nil
		t.StartedAt = time.Now()
		t.RecentLogs = nil
		t.ResetOutputLocked()
	})
	if running {
		return s.Send(fmt.Sprintf("ℹ️ Задача #%d сейчас выполняется. Сначала остановите её: <code>/cancel %d</code>", targetID, targetID), ports.Rich())
	}

	domain.GlobalTaskManager.ClearTaskConversationID(targetID)
	domain.GlobalTaskManager.SetTaskAgent(targetID, ActiveAgentName())
	syncLegacySession(task)

	workDir := filepath.Join(config.ProjectsRoot, proj)
	go runAgentTaskPipeline(s.Messenger(), s.Chat(), task, workDir, config.ProjectsRoot)
	return s.Send(fmt.Sprintf("🔄 <b>Задача #%d перезапущена с чистого листа</b> (новая сессия %s в <code>%s</code>).", targetID, html.EscapeString(ActiveAgentName()), html.EscapeString(proj)), ports.Rich())
}

func handleCreateNewTask(s ports.Session, text string) error {
	config.ProjectState.RLock()
	requiresPlan := config.ProjectState.PlanMode
	config.ProjectState.RUnlock()
	return handleCreateNewTaskWithOptions(s, text, requiresPlan)
}

func handleCreatePlanTask(s ports.Session, text string) error {
	return handleCreateNewTaskWithOptions(s, text, true)
}

// isProjectDir проверяет, существует ли директория проекта с таким именем в ProjectsRoot.
func isProjectDir(name string) bool {
	if strings.HasPrefix(strings.TrimSpace(name), ".") {
		return false
	}
	path, err := utils.SafeJoinSegment(config.ProjectsRoot, name)
	if err != nil {
		return false
	}
	fi, statErr := os.Stat(path)
	return statErr == nil && fi.IsDir()
}

// stripLeadingWords удаляет count первых слов из строки text, сохраняя форматирование остатка.
func stripLeadingWords(text string, count int) string {
	idx := 0
	for i := 0; i < count; i++ {
		for idx < len(text) && (text[idx] == ' ' || text[idx] == '\t' || text[idx] == '\n' || text[idx] == '\r') {
			idx++
		}
		for idx < len(text) && !(text[idx] == ' ' || text[idx] == '\t' || text[idx] == '\n' || text[idx] == '\r') {
			idx++
		}
	}
	return strings.TrimSpace(text[idx:])
}

// parseNewTaskInput разбирает аргументы команды /new или /plan,
// извлекая опциональные [проект] и [агент] (в любом порядке) и текст описания задачи.
func parseNewTaskInput(text string, defaultProj, defaultAgent string) (targetProj, targetAgent, prompt string) {
	targetProj = defaultProj
	targetAgent = defaultAgent
	words := strings.Fields(text)
	if len(words) == 0 {
		return targetProj, targetAgent, ""
	}

	w0 := words[0]
	w0Agent := isKnownAgent(w0)
	w0Proj := isProjectDir(w0)

	if len(words) >= 2 {
		w1 := words[1]
		w1Agent := isKnownAgent(w1)
		w1Proj := isProjectDir(w1)

		if w0Agent && w1Proj {
			targetAgent = strings.ToLower(w0)
			targetProj = w1
			prompt = stripLeadingWords(text, 2)
			return targetProj, targetAgent, prompt
		}
		if w0Proj && w1Agent {
			targetProj = w0
			targetAgent = strings.ToLower(w1)
			prompt = stripLeadingWords(text, 2)
			return targetProj, targetAgent, prompt
		}
	}

	if w0Agent {
		targetAgent = strings.ToLower(w0)
		prompt = stripLeadingWords(text, 1)
		return targetProj, targetAgent, prompt
	}

	if w0Proj {
		targetProj = w0
		prompt = stripLeadingWords(text, 1)
		return targetProj, targetAgent, prompt
	}

	prompt = strings.TrimSpace(text)
	return targetProj, targetAgent, prompt
}

func handleCreateNewTaskWithOptions(s ports.Session, text string, requiresPlan bool) error {
	curAgent := ActiveAgentName()

	config.ProjectState.RLock()
	curProj := config.ProjectState.CurrentProject
	curMod := config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()

	targetProj, targetAgent, prompt := parseNewTaskInput(text, curProj, curAgent)
	if prompt == "" {
		return s.Send("Использование: <code>/new &lt;описание задачи&gt;</code>\n(или <code>/new [проект] [агент] &lt;описание&gt;</code>)", ports.Rich())
	}

	if targetProj == "" {
		return s.Send("❌ Сначала выберите проект: /projects", nil)
	}

	if targetAgent != "" && !strings.EqualFold(targetAgent, curAgent) {
		if _, err := SwitchActiveAgent(targetAgent); err != nil {
			return s.Send(fmt.Sprintf("❌ Ошибка переключения на агента <b>%s</b>: %v", html.EscapeString(targetAgent), err), ports.Rich())
		}
	}

	config.ProjectState.RLock()
	curMod = config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent(targetProj, curMod, targetAgent, prompt, s.Chat(), requiresPlan)
	syncLegacySession(task)

	taskID := task.Snapshot().ID
	if domain.GlobalTaskManager.HasRunningTaskInProject(targetProj) {
		planNote := ""
		if requiresPlan {
			planNote = " Сначала будет составлен подробный план."
		}
		msg := fmt.Sprintf(
			"⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code>:\n\n"+
				"<i>«%s»</i>\n\n"+
				"💡 В этом проекте уже выполняется задача. Задача #%d начнется автоматически после ее завершения.%s",
			taskID, html.EscapeString(targetProj), html.EscapeString(utils.TruncateString(prompt, 250)), taskID, planNote,
		)
		return s.Send(msg, ports.Rich())
	}

	task.Update(func(t *domain.TaskSession) {
		if requiresPlan {
			t.Status = domain.TaskStatusPlanning
		} else {
			t.Status = domain.TaskStatusRunning
		}
		t.StartedAt = time.Now()
	})
	syncLegacySession(task)
	_, _ = domain.GlobalTaskManager.SetActiveTask(taskID)

	domain.GlobalTokenTracker.StartTaskWithAgent(targetProj, curMod, prompt, targetAgent)
	workDir := filepath.Join(config.ProjectsRoot, targetProj)

	go runAgentTaskPipeline(s.Messenger(), s.Chat(), task, workDir, config.ProjectsRoot)

	return nil
}

func handleAddFollowupToTask(s ports.Session, taskID int, text string) error {
	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Send(fmt.Sprintf("❌ Задача #%d не найдена.", taskID), ports.Rich())
	}

	view12 := task.Snapshot()
	status := view12.Status
	cmdIsNil := !task.HasLiveProcess()

	if status == domain.TaskStatusWaitingApproval {
		if isConfirmationText(text) {
			return handleApprovePlan(s.Messenger(), s.Chat(), taskID)
		}
		return handleRevisePlan(s.Messenger(), s.Chat(), taskID, text)
	}

	if (status == domain.TaskStatusPaused || status == domain.TaskStatusCancelled || (status == domain.TaskStatusWaitingInput && cmdIsNil)) && HasAgentConflict(task, ActiveAgentName()) {
		task.Update(func(t *domain.TaskSession) {
			t.CurrentPrompt = text
		})
		domain.GlobalTaskManager.SaveTask(task)
		return sendAgentConflictDialog(s, task)
	}

	task, qLen, isAnswer, err := domain.GlobalTaskManager.AddFollowup(taskID, text)
	if err != nil {
		return s.Send(fmt.Sprintf("❌ Не удалось отправить дополнение к задаче #%d: %s", taskID, err.Error()), ports.Rich())
	}

	syncLegacySession(task)

	if isAnswer {
		view13 := task.Snapshot()
		curStatus := view13.Status
		proj := view13.Project
		cmdIsNil = !task.HasLiveProcess()

		if curStatus == domain.TaskStatusQueued {
			return s.Send(fmt.Sprintf("⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code> с ответом:\n<i>«%s»</i>",
				taskID, html.EscapeString(proj), html.EscapeString(utils.TruncateString(text, 250))), ports.Rich())
		} else if (curStatus == domain.TaskStatusRunning || curStatus == domain.TaskStatusWaitingInput) && cmdIsNil {
			if HasAgentConflict(task, ActiveAgentName()) {
				task.Update(func(t *domain.TaskSession) {
					t.CurrentPrompt = text
				})
				domain.GlobalTaskManager.SaveTask(task)
				return sendAgentConflictDialog(s, task)
			}
			task.Update(func(t *domain.TaskSession) {
				if t.RequiresPlan && !t.PlanApproved {
					t.Status = domain.TaskStatusPlanning
				} else {
					t.Status = domain.TaskStatusRunning
				}
				t.StartedAt = time.Now()
				t.CurrentPrompt = text
				t.LastQuestion = ""
				t.QuestionOptions = nil
			})
			syncLegacySession(task)

			workDir := filepath.Join(config.ProjectsRoot, proj)
			go runAgentTaskPipeline(s.Messenger(), s.Chat(), task, workDir, config.ProjectsRoot)
			return s.Send(fmt.Sprintf("▶️ <b>Задача #%d (<code>%s</code>) возобновлена с ответом:</b>\n<i>«%s»</i>",
				taskID, html.EscapeString(proj), html.EscapeString(utils.TruncateString(text, 250))), ports.Rich())
		}

		return s.Send(fmt.Sprintf("💬 <b>Ответ передан задаче #%d</b> (<code>%s</code>)...", taskID, html.EscapeString(task.Snapshot().Project)), ports.Rich())
	}

	msg := fmt.Sprintf(
		"📥 <b>Дополнение сохранено в задачу #%d</b> (<code>%s</code>) [#%d в очереди]:\n\n"+
			"<i>«%s»</i>\n\n"+
			"Агент завершит текущий шаг и применит эти правки в ветку задачи #%d.",
		taskID, html.EscapeString(task.Snapshot().Project), qLen, html.EscapeString(utils.TruncateString(text, 250)), taskID,
	)
	return s.Send(msg, ports.Rich())
}

func isConfirmationText(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "утверждаю", "утвердить", "подтверждаю", "согласовано", "ок", "ok", "approve", "lgtm", "+", "да", "yes", "погнали", "делай", "start":
		return true
	default:
		return false
	}
}

// sendTaskHistory читает и отправляет переписку (реплики пользователя и
// агента) для любой сессии задачи, включая уже завершённые. История читается
// заново из файла сессии CLI-агента при каждом вызове и нигде ботом не
// сохраняется — только последний общий предпросмотр показывается в чате, а
// полная версия при необходимости отправляется отдельным файлом.
func sendTaskHistory(s ports.Session, task *domain.TaskSession) error {
	if task == nil {
		return s.Send("❌ Задача не найдена. Список задач: /tasks", ports.Rich())
	}

	view14 := task.Snapshot()
	id := view14.ID
	proj := view14.Project
	convID := view14.ConversationID
	agentName := view14.Agent
	if agentName == "" {
		agentName = "agy"
	}

	if convID == "" {
		return s.Send(fmt.Sprintf("ℹ️ У задачи #%d ещё нет сохранённой сессии агента (<code>%s</code> ещё не запускался).", id, html.EscapeString(agentName)), ports.Rich())
	}

	workDir := filepath.Join(config.ProjectsRoot, proj)
	turns, err := transcript.ReadSession(workDir, convID)
	if err != nil {
		if errors.Is(err, transcript.ErrSessionNotFound) {
			return s.Send(fmt.Sprintf("ℹ️ Файл сессии задачи #%d не найден на диске (возможно, был удалён или сжат).", id), ports.Rich())
		}
		log.Printf("history: ошибка чтения сессии задачи #%d: %v", id, err)
		return s.Send(fmt.Sprintf("❌ Не удалось прочитать историю сессии задачи #%d.", id), ports.Rich())
	}
	if len(turns) == 0 {
		return s.Send(fmt.Sprintf("ℹ️ В сессии задачи #%d пока нет сообщений.", id), ports.Rich())
	}

	const previewLimit = 6
	const previewTurnRunes = 500

	previewStart := 0
	if len(turns) > previewLimit {
		previewStart = len(turns) - previewLimit
	}

	var bldr strings.Builder
	bldr.WriteString(fmt.Sprintf("💬 <b>Переписка задачи #%d</b> (<code>%s</code>, %d реплик):\n\n", id, html.EscapeString(proj), len(turns)))

	truncatedAny := previewStart > 0
	for _, t := range turns[previewStart:] {
		label := "🤖 <b>Агент</b>"
		if t.Role == "user" {
			label = "👤 <b>Пользователь</b>"
		}
		if len([]rune(t.Text)) > previewTurnRunes {
			truncatedAny = true
		}
		bldr.WriteString(fmt.Sprintf("%s:\n<i>%s</i>\n\n", label, html.EscapeString(utils.TruncateString(t.Text, previewTurnRunes))))
	}
	if previewStart > 0 {
		bldr.WriteString(fmt.Sprintf("<i>… показаны последние %d из %d реплик.</i>\n\n", previewLimit, len(turns)))
	}
	if truncatedAny {
		bldr.WriteString("📄 <i>Полная история без сокращений — во вложенном файле.</i>")
	}

	if err := s.Send(bldr.String(), ports.Rich()); err != nil {
		return err
	}
	if !truncatedAny {
		return nil
	}

	return s.SendDocument(ports.Document{
		FileName: fmt.Sprintf("history_task_%d.md", id),
		MIME:     "text/markdown",
		Caption:  fmt.Sprintf("💬 Полная переписка задачи #%d (%s)", id, proj),
		Content:  []byte(formatHistoryDocument(turns, proj, id)),
	})
}

// formatHistoryDocument форматирует полную переписку задачи в Markdown для
// отправки файлом, без каких-либо сокращений.
func formatHistoryDocument(turns []transcript.Turn, proj string, id int) string {
	var bldr strings.Builder
	bldr.WriteString(fmt.Sprintf("# Переписка задачи #%d (%s)\n\n", id, proj))
	for _, t := range turns {
		label := "Агент"
		if t.Role == "user" {
			label = "Пользователь"
		}
		bldr.WriteString("## ")
		bldr.WriteString(label)
		if t.Timestamp != "" {
			bldr.WriteString(" · ")
			bldr.WriteString(t.Timestamp)
		}
		bldr.WriteString("\n\n")
		bldr.WriteString(t.Text)
		bldr.WriteString("\n\n")
	}
	return bldr.String()
}

func buildResumeMarkup(taskID int) *ports.Keyboard {
	lang := config.ProjectState.GetLanguage()
	return &ports.Keyboard{Rows: [][]ports.Button{
		{
			{Text: i18n.T(lang, "BtnResume"), Action: "q_resume", Payload: strconv.Itoa(taskID)},
			{Text: i18n.T(lang, "BtnCancel"), Action: "plan_cancel", Payload: strconv.Itoa(taskID)},
		},
	}}
}

func buildQuestionMarkup(task *domain.TaskSession) *ports.Keyboard {
	view15 := task.Snapshot()
	taskID := view15.ID
	options := append([]string(nil), view15.QuestionOptions...)

	var rows [][]ports.Button

	if len(options) > 0 {
		var optButtons []ports.Button
		for i, opt := range options {
			cleanOpt := strings.TrimSpace(opt)
			btnText := fmt.Sprintf("%d. %s", i+1, utils.TruncateString(cleanOpt, 30))
			optButtons = append(optButtons, ports.Button{Text: btnText, Action: "q_choice", Payload: fmt.Sprintf("%d:%d", taskID, i)})
		}

		allShort := true
		for _, opt := range options {
			if len([]rune(opt)) > 15 {
				allShort = false
				break
			}
		}

		if allShort && len(optButtons) > 1 {
			for i := 0; i < len(optButtons); i += 2 {
				if i+1 < len(optButtons) {
					rows = append(rows, []ports.Button{optButtons[i], optButtons[i+1]})
				} else {
					rows = append(rows, []ports.Button{optButtons[i]})
				}
			}
		} else {
			for _, b := range optButtons {
				rows = append(rows, []ports.Button{b})
			}
		}
	}

	lang := config.ProjectState.GetLanguage()
	rows = append(rows, []ports.Button{
		{Text: i18n.T(lang, "BtnPause"), Action: "q_pause", Payload: strconv.Itoa(taskID)},
		{Text: i18n.T(lang, "BtnCancel"), Action: "plan_cancel", Payload: strconv.Itoa(taskID)},
	})

	return &ports.Keyboard{Rows: rows}
}

func checkAndStartQueuedTask(m ports.Messenger, project, root string) {
	nextTask := domain.GlobalTaskManager.GetNextQueuedTaskForProject(project)
	if nextTask == nil {
		return
	}

	currentAgentName := ActiveAgentName()

	if HasAgentConflict(nextTask, currentAgentName) {
		var chat ports.ChatID
		nextTask.Update(func(t *domain.TaskSession) {
			t.Status = domain.TaskStatusPaused
			chat = t.Chat
		})
		domain.GlobalTaskManager.SaveTask(nextTask)
		_ = sendAgentConflictDialogWithMessenger(m, chat, nextTask)
		return
	}

	var isPlanning bool
	var chat ports.ChatID
	var nextID int
	var prompt string
	var model string
	var tAgent string
	nextTask.Update(func(t *domain.TaskSession) {
		isPlanning = t.RequiresPlan && !t.PlanApproved
		if isPlanning {
			t.Status = domain.TaskStatusPlanning
		} else {
			t.Status = domain.TaskStatusRunning
		}
		t.StartedAt = time.Now()
		chat = t.Chat
		nextID = t.ID
		prompt = t.InitialPrompt
		model = t.Model
		tAgent = t.Agent
		if tAgent == "" {
			tAgent = currentAgentName
		}
	})

	syncLegacySession(nextTask)
	_, _ = domain.GlobalTaskManager.SetActiveTask(nextID)

	if isPlanning {
		_, _ = m.Send(context.Background(), chat, fmt.Sprintf("📝 <b>Запуск планирования задачи #%d из очереди:</b> <code>%s</code>\n<i>«%s»</i>",
			nextID, html.EscapeString(project), html.EscapeString(utils.TruncateString(prompt, 80))), ports.Rich())
	} else {
		_, _ = m.Send(context.Background(), chat, fmt.Sprintf("🚀 <b>Запуск задачи #%d из очереди:</b> <code>%s</code>\n<i>«%s»</i>",
			nextID, html.EscapeString(project), html.EscapeString(utils.TruncateString(prompt, 80))), ports.Rich())
	}

	domain.GlobalTokenTracker.StartTaskWithAgent(project, model, prompt, tAgent)
	workDir := filepath.Join(root, project)
	go runAgentTaskPipeline(m, chat, nextTask, workDir, config.ProjectsRoot)
}

func syncLegacySession(task *domain.TaskSession) {
	if task == nil {
		config.Session.Lock()
		config.Session.IsRunning = false
		config.Session.Waiting = false
		config.Session.Unlock()
		return
	}

	view16 := task.Snapshot()
	config.Session.Lock()
	config.Session.IsRunning = (view16.Status == domain.TaskStatusRunning || view16.Status == domain.TaskStatusWaitingInput || view16.Status == domain.TaskStatusPlanning)
	config.Session.Waiting = (view16.Status == domain.TaskStatusWaitingInput || view16.Status == domain.TaskStatusWaitingApproval)
	config.Session.StartedAt = view16.StartedAt
	config.Session.CurrentPrompt = view16.InitialPrompt
	config.Session.CurrentProject = view16.Project
	config.Session.RecentLogs = append([]string(nil), view16.RecentLogs...)
	config.Session.PendingFollowups = append([]string(nil), view16.PendingFollowups...)
	config.Session.LastPRURL = view16.LastPRURL
	config.Session.LastModelUsed = view16.LastModelUsed
	config.Session.LastTokensUsed = view16.LastTokensUsed
	config.Session.Unlock()

	domain.GlobalTaskManager.SaveTask(task)
}

func isQuestionText(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	return strings.HasSuffix(s, "?") ||
		strings.Contains(lower, "подтвердите") ||
		strings.Contains(lower, "как поступить") ||
		strings.Contains(lower, "do you want to")
}

func sendLongMarkdown(m ports.Messenger, chat ports.ChatID, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	chunks := utils.SplitMarkdown(text, 3500)
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}

		htmlContent := utils.MarkdownToTelegramHTML(chunk)
		_, _ = m.Send(context.Background(), chat, htmlContent, ports.Rich())
	}
}

// HasAgentConflict проверяет, есть ли несовместимость между активным агентом и агентом задачи.
// Конфликт возникает, только если задача уже имеет сессию CLI (ConversationID != "") и её агент не совпадает с активным.
func HasAgentConflict(task *domain.TaskSession, activeAgent string) bool {
	if task == nil {
		return false
	}
	view := task.Snapshot()
	taskAgent := view.Agent
	if taskAgent == "" {
		taskAgent = "agy"
	}
	return view.ConversationID != "" && !strings.EqualFold(taskAgent, activeAgent)
}

func buildAgentConflictMarkup(taskID int, taskAgent string) *ports.Keyboard {
	if taskAgent == "" {
		taskAgent = "agy"
	}
	return &ports.Keyboard{
		Rows: [][]ports.Button{
			{
				{Text: "🔄 Начать заново", Action: "task_agent_restart", Payload: strconv.Itoa(taskID)},
				{Text: fmt.Sprintf("🔀 Переключиться на %s", taskAgent), Action: "task_agent_switch", Payload: strconv.Itoa(taskID)},
			},
		},
	}
}

func sendAgentConflictDialog(s ports.Session, task *domain.TaskSession) error {
	return sendAgentConflictDialogWithMessenger(s.Messenger(), s.Chat(), task)
}

func sendAgentConflictDialogWithMessenger(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession) error {
	view17 := task.Snapshot()
	id := view17.ID
	agent := view17.Agent
	if agent == "" {
		agent = "agy"
	}
	convID := view17.ConversationID

	markup := buildAgentConflictMarkup(id, agent)
	msg := fmt.Sprintf(
		"⚠️ <b>Задача #%d была начата агентом %s</b> (сессия: <code>%s</code>).\n"+
			"Текущий активный агент бота: <b>%s</b>.\n\n"+
			"Сессии разных агентов несовместимы. Выберите действие:",
		id, html.EscapeString(agent), html.EscapeString(convID), html.EscapeString(ActiveAgentName()),
	)
	_, err := m.Send(context.Background(), chat, msg, ports.RichWith(markup))
	return err
}
