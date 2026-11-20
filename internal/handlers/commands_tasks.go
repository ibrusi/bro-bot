package handlers

import (
	"bro-bot/internal/adapters/transcript"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"bro-bot/internal/system"
	"bro-bot/internal/utils"
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Команды и кнопки управления задачами: создание, статус, дополнение,
// ответы на вопросы агента, пауза, возобновление и отмена.

// handleTasks — обработчик команды /tasks.
func handleTasks(s ports.Session) error {
	msg, menu := domain.FormatTasksList(domain.GlobalTaskManager, uiLang())
	return s.Send(msg, ports.RichWith(menu))
}

// onTaskSel — обработчик кнопки task_sel.
func onTaskSel(s ports.Session) error {
	lang := uiLang()

	id, err := strconv.Atoi(s.Callback().Payload)
	if err != nil {
		return s.Respond(i18n.T(lang, "task.toast_bad_id"))
	}
	task, err := domain.GlobalTaskManager.SetActiveTask(id)
	if err != nil {
		return s.Respond(ErrorText(err, lang))
	}
	syncLegacySession(task)
	_ = s.Respond(i18n.Tf(lang, "task.toast_selected", id))

	details := domain.FormatTaskDetails(task, true, lang)
	markup := domain.BuildTaskDetailsMarkup(task, lang)
	return s.Send(i18n.Tf(lang, "task.focus_switched", id, details, agentConflictNote(task, lang)), ports.RichWith(markup))
}

// agentConflictNote — приписка к карточке задачи, чью сессию вёл другой агент.
// Пустая строка, если конфликта нет.
func agentConflictNote(task *domain.TaskSession, lang string) string {
	if !HasAgentConflict(task, ActiveAgentName()) {
		return ""
	}
	taskAgent := task.Snapshot().Agent
	if taskAgent == "" {
		taskAgent = "agy"
	}
	return i18n.Tf(lang, "task.agent_conflict_note", html.EscapeString(taskAgent), html.EscapeString(ActiveAgentName()))
}

// handleStatus — обработчик команды /status.
func handleStatus(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	var target *domain.TaskSession
	if len(args) > 0 {
		first := strings.TrimPrefix(args[0], "#")
		if id, err := strconv.Atoi(first); err == nil {
			target = domain.GlobalTaskManager.GetTask(id)
			if target == nil {
				return s.Send(i18n.Tf(lang, "task.not_found", id), ports.Rich())
			}
		}
	}

	if target == nil {
		target = domain.GlobalTaskManager.GetActiveTask()
	}

	if target == nil || (!target.IsActive() && len(args) == 0) {
		idleMsg := i18n.T(lang, "task.idle")
		if lastSnippet := domain.GlobalTokenTracker.GetLastTaskStatusBlock(lang); lastSnippet != "" {
			idleMsg += "\n\n" + lastSnippet
		}
		report := system.CollectResourceReport(false)
		if resSnippet := system.FormatCompactResourceSnippet(report, lang); resSnippet != "" {
			idleMsg += "\n\n" + resSnippet
		}
		idleMsg += i18n.T(lang, "task.idle_hint")
		return s.Send(idleMsg, ports.Rich())
	}

	targetID := target.Snapshot().ID
	activeTask := domain.GlobalTaskManager.GetActiveTask()
	isActiveFocus := (activeTask != nil && activeTask.Snapshot().ID == targetID)
	msg := domain.FormatTaskDetails(target, isActiveFocus, lang)

	if tokenBlock := domain.GlobalTokenTracker.GetCurrentTaskStatusBlock(lang); tokenBlock != "" {
		msg += "\n\n" + tokenBlock
	}
	report := system.CollectResourceReport(false)
	if resSnippet := system.FormatCompactResourceSnippet(report, lang); resSnippet != "" {
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
			msg += i18n.Tf(lang, "task.other_tasks", strings.Join(otherParts, " | "))
		}
	}

	statusMarkup := domain.BuildTaskDetailsMarkup(target, lang)
	return s.Send(msg, ports.RichWith(statusMarkup))
}

// handleTask — обработчик команды /task.
func handleTask(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	if len(args) == 0 {
		active := domain.GlobalTaskManager.GetActiveTask()
		if active == nil {
			return s.Send(i18n.T(lang, "task.no_active_create"), ports.Rich())
		}
		details := domain.FormatTaskDetails(active, true, lang)
		markup := domain.BuildTaskDetailsMarkup(active, lang)
		return s.Send(details+agentConflictNote(active, lang), ports.RichWith(markup))
	}

	payload := commandPayload(s)
	first := strings.TrimPrefix(args[0], "#")
	id, err := strconv.Atoi(first)
	if err != nil {
		if strings.ToLower(first) == "new" && len(args) > 1 {
			prompt := stripLeadingWords(payload, 1)
			return handleCreateNewTask(s, prompt)
		}
		if strings.ToLower(first) == "plan" && len(args) > 1 {
			prompt := stripLeadingWords(payload, 1)
			return handleCreatePlanTask(s, prompt)
		}
		return s.Send(i18n.T(lang, "task.usage_task"), ports.Rich())
	}

	// Если передан текст дополнения: /task 2 сделай ещё это
	if len(args) > 1 {
		followupText := stripLeadingWords(payload, 1)
		if followupText != "" {
			return handleAddFollowupToTask(s, id, followupText)
		}
	}

	task, err := domain.GlobalTaskManager.SetActiveTask(id)
	if err != nil {
		return s.Send("❌ "+ErrorText(err, lang), ports.Rich())
	}
	syncLegacySession(task)

	details := domain.FormatTaskDetails(task, true, lang)
	markup := domain.BuildTaskDetailsMarkup(task, lang)
	return s.Send(i18n.Tf(lang, "task.focus_switched", id, details, agentConflictNote(task, lang)), ports.RichWith(markup))
}

// handleAdd — обработчик команды /add.
func handleAdd(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	if len(args) == 0 {
		return s.Send(i18n.T(lang, "task.usage_add"), ports.Rich())
	}

	payload := commandPayload(s)
	first := strings.TrimPrefix(args[0], "#")
	if id, err := strconv.Atoi(first); err == nil && len(args) > 1 {
		followupText := stripLeadingWords(payload, 1)
		return handleAddFollowupToTask(s, id, followupText)
	}

	active := domain.GlobalTaskManager.GetActiveTask()
	if active == nil {
		return s.Send(i18n.T(lang, "task.no_active_for_add"), ports.Rich())
	}
	return handleAddFollowupToTask(s, active.Snapshot().ID, payload)
}

// handleNew — обработчик команды /new.
func handleNew(s ports.Session) error {
	text := commandPayload(s)
	if text == "" {
		return s.Send(i18n.T(uiLang(), "task.usage_new"), ports.Rich())
	}
	return handleCreateNewTask(s, text)
}

// handleHistory — обработчик команды /history.
func handleHistory(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	var target *domain.TaskSession
	if len(args) > 0 {
		first := strings.TrimPrefix(args[0], "#")
		if id, err := strconv.Atoi(first); err == nil {
			target = domain.GlobalTaskManager.GetTask(id)
			if target == nil {
				return s.Send(i18n.Tf(lang, "task.not_found", id), ports.Rich())
			}
		}
	}

	if target == nil {
		target = domain.GlobalTaskManager.GetActiveTask()
	}

	if target == nil {
		return s.Send(i18n.T(lang, "task.no_active"), ports.Rich())
	}

	return sendTaskHistory(s, target, lang)
}

// handleCancel — обработчик команды /cancel.
func handleCancel(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	var targetID int
	if len(args) > 0 {
		idStr := strings.TrimPrefix(args[0], "#")
		var err error
		targetID, err = strconv.Atoi(idStr)
		if err != nil {
			return s.Send(i18n.T(lang, "task.usage_cancel"), ports.Rich())
		}
	} else {
		active := domain.GlobalTaskManager.GetActiveTask()
		if active == nil || !active.IsActive() {
			return s.Send(i18n.T(lang, "task.no_active_short"), nil)
		}
		targetID = active.Snapshot().ID
	}

	task, err := domain.GlobalTaskManager.CancelTask(targetID)
	if err != nil {
		return s.Send("❌ "+ErrorText(err, lang), ports.Rich())
	}

	syncLegacySession(domain.GlobalTaskManager.GetActiveTask())
	domain.GlobalTokenTracker.CancelTask()

	view := task.Snapshot()
	checkAndStartQueuedTask(s.Messenger(), view.Project, config.ProjectsRoot)

	return s.Send(i18n.Tf(lang, "task.cancelled", view.ID, html.EscapeString(view.Project)), ports.Rich())
}

// onQuestionChoice — обработчик кнопки q_choice.
func onQuestionChoice(s ports.Session) error {
	lang := uiLang()

	parts := strings.Split(s.Callback().Payload, ":")
	if len(parts) < 2 {
		return s.Respond(i18n.T(lang, "task.toast_bad_button"))
	}
	var (
		taskID int
		qIdx   = -1
		optIdx int
		err1   error
		err2   error
		err3   error
	)
	taskID, err1 = strconv.Atoi(parts[0])
	if len(parts) >= 3 {
		qIdx, err2 = strconv.Atoi(parts[1])
		optIdx, err3 = strconv.Atoi(parts[2])
	} else {
		optIdx, err2 = strconv.Atoi(parts[1])
	}
	if err1 != nil || err2 != nil || err3 != nil {
		return s.Respond(i18n.T(lang, "task.toast_bad_params"))
	}

	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Respond(i18n.T(lang, "task.toast_not_found"))
	}

	view3 := task.Snapshot()
	status := view3.Status
	isPlanning := view3.RequiresPlan && !view3.PlanApproved
	var chosenText string
	totalQ := len(view3.PendingQuestions)
	effectiveQ := qIdx
	if totalQ > 0 {
		if effectiveQ < 0 || effectiveQ >= totalQ {
			effectiveQ = view3.CurrentQuestionIndex
			if effectiveQ < 0 || effectiveQ >= totalQ {
				effectiveQ = 0
			}
		}
		q := view3.PendingQuestions[effectiveQ]
		if optIdx >= 0 && optIdx < len(q.Options) {
			chosenText = q.Options[optIdx]
		}
	} else if optIdx >= 0 && optIdx < len(view3.QuestionOptions) {
		chosenText = view3.QuestionOptions[optIdx]
	}

	if chosenText == "" {
		return s.Respond(i18n.T(lang, "task.toast_option_missing"))
	}

	_ = s.Respond(i18n.Tf(lang, "task.toast_chosen", utils.TruncateString(chosenText, 25)))

	if totalQ > 1 {
		done, combined := recordSingleQuestionAnswerAndAdvance(task, effectiveQ, chosenText, lang)
		if !done {
			domain.GlobalTaskManager.SaveTask(task)
			updatedView := task.Snapshot()
			nextMsg := formatTaskQuestionMessage(updatedView, isPlanning, lang)
			nextMenu := buildQuestionMarkup(task, lang)
			_ = s.Edit(nextMsg, ports.RichWith(nextMenu))
			return nil
		}
		if cb := s.Callback(); cb != nil && cb.MessageText != "" {
			_ = s.Edit(i18n.Tf(lang, "task.option_chosen_edit",
				cb.MessageText, optIdx+1, html.EscapeString(chosenText)), ports.Rich())
		}
		return deliverTaskAnswer(s, task, combined, lang, -1)
	}

	if status == domain.TaskStatusWaitingInput || status == domain.TaskStatusPaused {
		if cb := s.Callback(); cb != nil && cb.MessageText != "" {
			_ = s.Edit(i18n.Tf(lang, "task.option_chosen_edit",
				cb.MessageText, optIdx+1, html.EscapeString(chosenText)), ports.Rich())
		}
		return deliverTaskAnswer(s, task, chosenText, lang, optIdx)
	}

	return s.Send(i18n.Tf(lang, "task.not_waiting_status", taskID, i18n.TaskStatusTitle(string(status), lang)), nil)
}

// onQuestionToggle — обработчик кнопки q_toggle (переключает вариант в мультивыборе).
func onQuestionToggle(s ports.Session) error {
	lang := uiLang()
	parts := strings.Split(s.Callback().Payload, ":")
	if len(parts) < 3 {
		return s.Respond(i18n.T(lang, "task.toast_bad_button"))
	}
	taskID, err1 := strconv.Atoi(parts[0])
	qIdx, err2 := strconv.Atoi(parts[1])
	optIdx, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return s.Respond(i18n.T(lang, "task.toast_bad_params"))
	}

	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Respond(i18n.T(lang, "task.toast_not_found"))
	}

	var toggled bool
	task.Update(func(t *domain.TaskSession) {
		if qIdx >= 0 && qIdx < len(t.PendingQuestions) && optIdx >= 0 && optIdx < len(t.PendingQuestions[qIdx].Options) {
			t.CurrentQuestionIndex = qIdx
			domain.ToggleQuestionOption(&t.PendingQuestions[qIdx], optIdx)
			toggled = true
		}
	})
	if !toggled {
		return s.Respond(i18n.T(lang, "task.toast_option_missing"))
	}

	domain.GlobalTaskManager.SaveTask(task)
	_ = s.Respond(i18n.T(lang, "task.toast_selection_updated"))

	updatedView := task.Snapshot()
	isPlanning := updatedView.RequiresPlan && !updatedView.PlanApproved
	msgText := formatTaskQuestionMessage(updatedView, isPlanning, lang)
	menu := buildQuestionMarkup(task, lang)
	return s.Edit(msgText, ports.RichWith(menu))
}

// onQuestionSubmit — обработчик кнопки q_submit для подтверждения мультивыбора.
func onQuestionSubmit(s ports.Session) error {
	lang := uiLang()
	parts := strings.Split(s.Callback().Payload, ":")
	if len(parts) < 2 {
		return s.Respond(i18n.T(lang, "task.toast_bad_button"))
	}
	taskID, err1 := strconv.Atoi(parts[0])
	qIdx, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return s.Respond(i18n.T(lang, "task.toast_bad_params"))
	}

	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Respond(i18n.T(lang, "task.toast_not_found"))
	}

	var selected string
	var valid bool
	task.Update(func(t *domain.TaskSession) {
		if qIdx >= 0 && qIdx < len(t.PendingQuestions) {
			valid = true
			selected = domain.FormatSelectedOptions(t.PendingQuestions[qIdx])
		}
	})
	if !valid {
		return s.Respond(i18n.T(lang, "task.toast_option_missing"))
	}
	if strings.TrimSpace(selected) == "" {
		return s.Respond(i18n.T(lang, "task.toast_nothing_selected"))
	}

	_ = s.Respond(i18n.Tf(lang, "task.toast_chosen", utils.TruncateString(selected, 25)))

	view := task.Snapshot()
	totalQ := len(view.PendingQuestions)
	isPlanning := view.RequiresPlan && !view.PlanApproved

	if totalQ > 1 {
		done, combined := recordSingleQuestionAnswerAndAdvance(task, qIdx, selected, lang)
		if !done {
			domain.GlobalTaskManager.SaveTask(task)
			updatedView := task.Snapshot()
			nextMsg := formatTaskQuestionMessage(updatedView, isPlanning, lang)
			nextMenu := buildQuestionMarkup(task, lang)
			_ = s.Edit(nextMsg, ports.RichWith(nextMenu))
			return nil
		}
		if cb := s.Callback(); cb != nil && cb.MessageText != "" {
			_ = s.Edit(i18n.Tf(lang, "task.option_chosen_edit",
				cb.MessageText, 1, html.EscapeString(selected)), ports.Rich())
		}
		return deliverTaskAnswer(s, task, combined, lang, -1)
	}

	if cb := s.Callback(); cb != nil && cb.MessageText != "" {
		_ = s.Edit(i18n.Tf(lang, "task.option_chosen_edit",
			cb.MessageText, 1, html.EscapeString(selected)), ports.Rich())
	}
	return deliverTaskAnswer(s, task, selected, lang, -1)
}

// onQuestionBack — возврат к предыдущему вопросу в интерактивном опроснике.
func onQuestionBack(s ports.Session) error {
	lang := uiLang()
	taskID, err := strconv.Atoi(s.Callback().Payload)
	if err != nil {
		return s.Respond(i18n.T(lang, "task.toast_bad_id"))
	}
	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Respond(i18n.T(lang, "task.toast_not_found"))
	}

	var curIdx int
	task.Update(func(t *domain.TaskSession) {
		if t.QuestionAcceptAllTxt {
			t.QuestionAcceptAllTxt = false
		} else if t.CurrentQuestionIndex > 0 {
			t.CurrentQuestionIndex--
		}
		if t.CurrentQuestionIndex >= len(t.PendingQuestions) && len(t.PendingQuestions) > 0 {
			t.CurrentQuestionIndex = len(t.PendingQuestions) - 1
		}
		curIdx = t.CurrentQuestionIndex
	})

	domain.GlobalTaskManager.SaveTask(task)
	_ = s.Respond(i18n.Tf(lang, "task.toast_question_switched", curIdx+1))

	updatedView := task.Snapshot()
	isPlanning := updatedView.RequiresPlan && !updatedView.PlanApproved
	return s.Edit(formatTaskQuestionMessage(updatedView, isPlanning, lang), ports.RichWith(buildQuestionMarkup(task, lang)))
}

// onQuestionAllText — переключение опросника в режим ответа на все вопросы одним сообщением.
func onQuestionAllText(s ports.Session) error {
	lang := uiLang()
	taskID, err := strconv.Atoi(s.Callback().Payload)
	if err != nil {
		return s.Respond(i18n.T(lang, "task.toast_bad_id"))
	}
	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Respond(i18n.T(lang, "task.toast_not_found"))
	}

	task.Update(func(t *domain.TaskSession) {
		t.QuestionAcceptAllTxt = true
	})
	_ = s.Respond(i18n.T(lang, "task.toast_send_all_text"))

	updatedView := task.Snapshot()
	isPlanning := updatedView.RequiresPlan && !updatedView.PlanApproved
	return s.Edit(formatTaskQuestionMessage(updatedView, isPlanning, lang), ports.RichWith(buildQuestionMarkup(task, lang)))
}

func recordSingleQuestionAnswerAndAdvance(task *domain.TaskSession, qIdx int, answer, lang string) (done bool, combined string) {
	task.Update(func(t *domain.TaskSession) {
		n := len(t.PendingQuestions)
		if n <= 1 {
			if n == 1 {
				t.PendingQuestions[0].Answer = answer
			}
			done = true
			combined = answer
			return
		}
		if qIdx < 0 || qIdx >= n {
			qIdx = t.CurrentQuestionIndex
			if qIdx < 0 || qIdx >= n {
				qIdx = 0
			}
		}
		t.PendingQuestions[qIdx].Answer = answer
		t.QuestionAcceptAllTxt = false

		nextIdx := -1
		for i := qIdx + 1; i < n; i++ {
			if strings.TrimSpace(t.PendingQuestions[i].Answer) == "" {
				nextIdx = i
				break
			}
		}
		if nextIdx == -1 {
			for i := 0; i < qIdx; i++ {
				if strings.TrimSpace(t.PendingQuestions[i].Answer) == "" {
					nextIdx = i
					break
				}
			}
		}
		if nextIdx == -1 {
			done = true
			combined = domain.FormatCombinedAnswers(t.PendingQuestions, lang)
			return
		}
		t.CurrentQuestionIndex = nextIdx
		t.LastQuestion = t.PendingQuestions[nextIdx].Question
		t.QuestionOptions = append([]string(nil), t.PendingQuestions[nextIdx].Options...)
		t.QuestionAskedAt = time.Now()
	})
	return done, combined
}

// deliverTaskAnswer доставляет готовый ответ задаче, которая ждёт ввода или стоит на паузе.
func deliverTaskAnswer(s ports.Session, task *domain.TaskSession, answer, lang string, singleOptIdx int) error {
	view := task.Snapshot()
	taskID := view.ID
	status := view.Status
	proj := view.Project
	hasPipeline := view.HasActivePipeline

	if status == domain.TaskStatusWaitingInput {
		if hasPipeline {
			task.Update(func(t *domain.TaskSession) {
				t.ClearPendingQuestionLocked()
			})
			domain.GlobalTaskManager.SaveTask(task)
			task.DeliverAnswer(answer)
			if singleOptIdx >= 0 {
				return s.Send(i18n.Tf(lang, "task.option_delivered", singleOptIdx+1, taskID, html.EscapeString(answer)), ports.Rich())
			}
			return s.Send(i18n.Tf(lang, "task.answer_delivered", taskID, html.EscapeString(proj)), ports.Rich())
		}
		if HasAgentConflict(task, ActiveAgentName()) {
			task.Update(func(t *domain.TaskSession) {
				t.CurrentPrompt = answer
			})
			domain.GlobalTaskManager.SaveTask(task)
			return sendAgentConflictDialog(s, task)
		}
		resumedTask, err := domain.GlobalTaskManager.ResumeTask(taskID, answer)
		if err != nil {
			return s.Send(i18n.Tf(lang, "task.resume_failed", taskID, ErrorText(err, lang)), ports.Rich())
		}
		syncLegacySession(resumedTask)
		resumedView := resumedTask.Snapshot()
		if resumedView.Status == domain.TaskStatusQueued {
			return s.Send(i18n.Tf(lang, "task.queued_with_answer",
				taskID, html.EscapeString(resumedView.Project), html.EscapeString(utils.TruncateString(answer, 250))), ports.Rich())
		}
		workDir := filepath.Join(config.ProjectsRoot, resumedView.Project)
		go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir, config.ProjectsRoot)
		return s.Send(i18n.Tf(lang, "task.resumed_with_answer",
			taskID, html.EscapeString(resumedView.Project), html.EscapeString(utils.TruncateString(answer, 250))), ports.Rich())
	}

	if status == domain.TaskStatusPaused {
		if HasAgentConflict(task, ActiveAgentName()) {
			task.Update(func(t *domain.TaskSession) {
				t.CurrentPrompt = answer
			})
			domain.GlobalTaskManager.SaveTask(task)
			return sendAgentConflictDialog(s, task)
		}
		resumedTask, err := domain.GlobalTaskManager.ResumeTask(taskID, answer)
		if err != nil {
			return s.Send(i18n.Tf(lang, "task.resume_failed", taskID, ErrorText(err, lang)), ports.Rich())
		}
		syncLegacySession(resumedTask)
		resumedView := resumedTask.Snapshot()
		if resumedView.Status == domain.TaskStatusQueued {
			return s.Send(i18n.Tf(lang, "task.queued_with_answer",
				taskID, html.EscapeString(resumedView.Project), html.EscapeString(utils.TruncateString(answer, 250))), ports.Rich())
		}
		workDir := filepath.Join(config.ProjectsRoot, resumedView.Project)
		go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir, config.ProjectsRoot)
		return s.Send(i18n.Tf(lang, "task.resumed_with_answer",
			taskID, html.EscapeString(resumedView.Project), html.EscapeString(utils.TruncateString(answer, 250))), ports.Rich())
	}

	return s.Send(i18n.Tf(lang, "task.not_waiting_status", taskID, i18n.TaskStatusTitle(string(status), lang)), nil)
}

// onQuestionPause — обработчик кнопки q_pause.
func onQuestionPause(s ports.Session) error {
	lang := uiLang()

	taskID, err := strconv.Atoi(s.Callback().Payload)
	if err != nil {
		return s.Respond(i18n.T(lang, "task.toast_bad_id"))
	}

	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Respond(i18n.T(lang, "task.toast_not_found"))
	}

	if !task.PauseTask() {
		return s.Respond(i18n.T(lang, "task.toast_not_waiting"))
	}

	_ = s.Respond(i18n.Tf(lang, "task.toast_paused", taskID))
	syncLegacySession(task)

	if cb := s.Callback(); cb != nil && cb.MessageText != "" {
		_ = s.Edit(i18n.Tf(lang, "task.paused_edit", cb.MessageText), ports.Rich())
	}

	view4 := task.Snapshot()
	projectName := view4.Project

	checkAndStartQueuedTask(s.Messenger(), projectName, config.ProjectsRoot)

	resumeMenu := buildResumeMarkup(taskID, lang)
	msg := i18n.Tf(lang, "task.paused_button", taskID, html.EscapeString(projectName), taskID)
	return s.Send(msg, ports.RichWith(resumeMenu))
}

// onQuestionResume — обработчик кнопки q_resume.
func onQuestionResume(s ports.Session) error {
	lang := uiLang()

	taskID, err := strconv.Atoi(s.Callback().Payload)
	if err != nil {
		return s.Respond(i18n.T(lang, "task.toast_bad_id"))
	}

	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Respond(i18n.T(lang, "task.toast_not_found"))
	}

	view5 := task.Snapshot()
	status := view5.Status
	lastQ := view5.LastQuestion
	opts := append([]string(nil), view5.QuestionOptions...)
	proj := view5.Project

	_ = s.Respond("")

	if status != domain.TaskStatusPaused && status != domain.TaskStatusWaitingInput {
		return s.Send(i18n.Tf(lang, "task.not_waiting_status", taskID, i18n.TaskStatusTitle(string(status), lang)), nil)
	}

	if HasAgentConflict(task, ActiveAgentName()) {
		return sendAgentConflictDialog(s, task)
	}

	if len(view5.PendingQuestions) > 0 || (lastQ != "" && len(opts) > 0) {
		menu := buildQuestionMarkup(task, lang)
		isPlan := view5.RequiresPlan && !view5.PlanApproved
		promptMsg := formatTaskQuestionMessage(view5, isPlan, lang)
		return s.Send(promptMsg, ports.RichWith(menu))
	}

	if lastQ != "" {
		return s.Send(i18n.Tf(lang, "task.question_pending",
			taskID, html.EscapeString(lastQ), taskID), ports.Rich())
	}

	// Если вопроса не было (например, пауза по таймауту выполнения шага) — возобновляем выполнение
	resumedTask, err := domain.GlobalTaskManager.ResumeTask(taskID, "")
	if err != nil {
		return s.Send(i18n.Tf(lang, "task.resume_failed_short", taskID, ErrorText(err, lang)), ports.Rich())
	}
	syncLegacySession(resumedTask)

	resumedView3 := resumedTask.Snapshot()
	resStatus := resumedView3.Status

	if resStatus == domain.TaskStatusQueued {
		return s.Send(i18n.Tf(lang, "task.queued_project", taskID, html.EscapeString(proj)), ports.Rich())
	}

	view6 := task.Snapshot()
	tAgent := view6.Agent
	if tAgent == "" {
		tAgent = "agy"
	}

	workDir := filepath.Join(config.ProjectsRoot, proj)
	go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir, config.ProjectsRoot)
	return s.Send(i18n.Tf(lang, "task.resumed_from_session", taskID, html.EscapeString(proj), html.EscapeString(tAgent)), ports.Rich())
}

// onTaskAgentRestart — обработчик кнопки task_agent_restart.
func onTaskAgentRestart(s ports.Session) error {
	lang := uiLang()

	taskID, err := strconv.Atoi(s.Callback().Payload)
	if err != nil {
		return s.Respond(i18n.T(lang, "task.toast_bad_id"))
	}

	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Respond(i18n.T(lang, "task.toast_not_found"))
	}

	_ = s.Respond(i18n.Tf(lang, "task.toast_restarting_with", ActiveAgentName()))

	domain.GlobalTaskManager.ClearTaskConversationID(taskID)
	domain.GlobalTaskManager.SetTaskAgent(taskID, ActiveAgentName())

	var proj string
	task.Update(func(t *domain.TaskSession) {
		proj = t.Project
		t.ConversationID = ""
		t.Agent = ActiveAgentName()
		if t.RequiresPlan {
			t.PlanApproved = false
			t.Status = domain.TaskStatusPlanning
		} else {
			t.Status = domain.TaskStatusRunning
		}
		if t.CurrentPrompt == "" {
			t.CurrentPrompt = t.InitialPrompt
		}
		t.LastPRURL = ""
		t.ClearPendingQuestionLocked()
		t.StartedAt = time.Now()
		t.RecentLogs = nil
		t.ResetOutputLocked()
	})

	syncLegacySession(task)
	domain.GlobalTaskManager.SaveTask(task)

	if cb := s.Callback(); cb != nil && cb.MessageText != "" {
		_ = s.Edit(i18n.Tf(lang, "task.restart_edit", cb.MessageText, html.EscapeString(ActiveAgentName())), ports.Rich())
	}

	workDir := filepath.Join(config.ProjectsRoot, proj)
	go runAgentTaskPipeline(s.Messenger(), s.Chat(), task, workDir, config.ProjectsRoot)
	return s.Send(i18n.Tf(lang, "task.restarted_with_agent", taskID, html.EscapeString(ActiveAgentName()), html.EscapeString(proj)), ports.Rich())
}

// onTaskAgentSwitch — обработчик кнопки task_agent_switch.
func onTaskAgentSwitch(s ports.Session) error {
	lang := uiLang()

	taskID, err := strconv.Atoi(s.Callback().Payload)
	if err != nil {
		return s.Respond(i18n.T(lang, "task.toast_bad_id"))
	}

	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Respond(i18n.T(lang, "task.toast_not_found"))
	}

	view7 := task.Snapshot()
	targetAgent := view7.Agent
	if targetAgent == "" {
		targetAgent = "agy"
	}
	prompt := view7.CurrentPrompt
	proj := view7.Project

	_ = s.Respond(i18n.Tf(lang, "task.toast_switching_to", targetAgent))

	switchResult, err := SwitchActiveAgent(targetAgent)
	if err != nil {
		return s.Send(i18n.Tf(lang, "task.agent_switch_failed", ErrorText(err, lang)), ports.Rich())
	}
	switchMsg := formatAgentSwitch(switchResult, lang)

	resumedTask, err := domain.GlobalTaskManager.ResumeTask(taskID, prompt)
	if err != nil {
		return s.Send(i18n.Tf(lang, "task.resume_failed", taskID, ErrorText(err, lang)), ports.Rich())
	}
	syncLegacySession(resumedTask)

	if cb := s.Callback(); cb != nil && cb.MessageText != "" {
		_ = s.Edit(i18n.Tf(lang, "task.switch_edit", cb.MessageText, html.EscapeString(targetAgent)), ports.Rich())
	}

	resumedView4 := resumedTask.Snapshot()
	resStatus := resumedView4.Status

	if resStatus == domain.TaskStatusQueued {
		return s.Send(i18n.Tf(lang, "task.queued_after_switch", switchMsg, taskID, html.EscapeString(proj)), ports.Rich())
	}

	workDir := filepath.Join(config.ProjectsRoot, proj)
	go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir, config.ProjectsRoot)
	return s.Send(i18n.Tf(lang, "task.resumed_with_agent", switchMsg, taskID, html.EscapeString(proj), html.EscapeString(targetAgent)), ports.Rich())
}

// handlePause — обработчик команды /pause.
func handlePause(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	var targetID int
	if len(args) > 0 {
		idStr := strings.TrimPrefix(args[0], "#")
		var err error
		targetID, err = strconv.Atoi(idStr)
		if err != nil {
			return s.Send(i18n.T(lang, "task.usage_pause"), ports.Rich())
		}
	} else {
		active := domain.GlobalTaskManager.GetActiveTask()
		if active == nil {
			return s.Send(i18n.T(lang, "task.no_active_short"), nil)
		}
		targetID = active.Snapshot().ID
	}

	task := domain.GlobalTaskManager.GetTask(targetID)
	if task == nil {
		return s.Send(i18n.Tf(lang, "task.not_found_short", targetID), ports.Rich())
	}

	if !task.PauseTask() {
		st := task.Snapshot().Status
		return s.Send(i18n.Tf(lang, "task.cannot_pause", targetID, i18n.TaskStatusTitle(string(st), lang)), ports.Rich())
	}

	syncLegacySession(task)
	view9 := task.Snapshot()
	projectName := view9.Project

	checkAndStartQueuedTask(s.Messenger(), projectName, config.ProjectsRoot)

	resumeMenu := buildResumeMarkup(targetID, lang)
	msg := i18n.Tf(lang, "task.paused_command", targetID, html.EscapeString(projectName), targetID)
	return s.Send(msg, ports.RichWith(resumeMenu))
}

// handleResume — обработчик команды /resume.
func handleResume(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	payload := commandPayload(s)
	var targetID int
	var answer string

	if len(args) > 0 {
		if id, err := strconv.Atoi(strings.TrimPrefix(args[0], "#")); err == nil {
			targetID = id
			if len(args) > 1 {
				answer = stripLeadingWords(payload, 1)
			}
		} else {
			answer = payload
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
		return s.Send(i18n.T(lang, "task.usage_resume"), ports.Rich())
	}

	task := domain.GlobalTaskManager.GetTask(targetID)
	if task == nil {
		return s.Send(i18n.Tf(lang, "task.not_found_short", targetID), ports.Rich())
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
			return handleAddFollowupToTask(s, targetID, answer)
		}
		menu := buildQuestionMarkup(task, lang)
		if len(view10.PendingQuestions) > 0 {
			isPlan := view10.RequiresPlan && !view10.PlanApproved
			return s.Send(formatTaskQuestionMessage(view10, isPlan, lang), ports.RichWith(menu))
		}
		return s.Send(i18n.Tf(lang, "task.question_waiting_short", targetID, targetID), ports.RichWith(menu))
	}

	if status == domain.TaskStatusPaused && answer == "" && len(view10.PendingQuestions) > 0 {
		menu := buildQuestionMarkup(task, lang)
		isPlan := view10.RequiresPlan && !view10.PlanApproved
		return s.Send(formatTaskQuestionMessage(view10, isPlan, lang), ports.RichWith(menu))
	}

	resumedTask, err := domain.GlobalTaskManager.ResumeTask(targetID, answer)
	if err != nil {
		return s.Send(i18n.Tf(lang, "task.resume_failed", targetID, ErrorText(err, lang)), ports.Rich())
	}
	syncLegacySession(resumedTask)

	resumedView6 := resumedTask.Snapshot()
	resStatus := resumedView6.Status
	proj := resumedView6.Project

	if resStatus == domain.TaskStatusQueued {
		return s.Send(i18n.Tf(lang, "task.queued_after_current", targetID, html.EscapeString(proj)), ports.Rich())
	}

	workDir := filepath.Join(config.ProjectsRoot, proj)
	go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir, config.ProjectsRoot)
	return s.Send(i18n.Tf(lang, "task.resumed", targetID, html.EscapeString(proj)), ports.Rich())
}

// handleRetryTaskByID выполняет сброс сессии и перезапуск задачи по её ID.
func handleRetryTaskByID(s ports.Session, targetID int) error {
	lang := uiLang()

	task := domain.GlobalTaskManager.GetTask(targetID)
	if task == nil {
		return s.Send(i18n.Tf(lang, "task.not_found_short", targetID), ports.Rich())
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
		if t.RequiresPlan {
			t.PlanApproved = false
			t.Status = domain.TaskStatusPlanning
		}
		t.CurrentPrompt = t.InitialPrompt
		t.LastPRURL = ""
		t.ClearPendingQuestionLocked()
		t.StartedAt = time.Now()
		t.RecentLogs = nil
		t.ResetOutputLocked()
	})
	if running {
		return s.Send(i18n.Tf(lang, "task.already_running", targetID, targetID), ports.Rich())
	}

	domain.GlobalTaskManager.ClearTaskConversationID(targetID)
	domain.GlobalTaskManager.SetTaskAgent(targetID, ActiveAgentName())
	syncLegacySession(task)
	domain.GlobalTaskManager.SaveTask(task)

	workDir := filepath.Join(config.ProjectsRoot, proj)
	go runAgentTaskPipeline(s.Messenger(), s.Chat(), task, workDir, config.ProjectsRoot)
	return s.Send(i18n.Tf(lang, "task.retried", targetID, html.EscapeString(ActiveAgentName()), html.EscapeString(proj)), ports.Rich())
}

// onTaskRetry — обработчик инлайн-кнопки task_retry.
func onTaskRetry(s ports.Session) error {
	lang := uiLang()

	taskID, err := strconv.Atoi(s.Callback().Payload)
	if err != nil {
		return s.Respond(i18n.T(lang, "task.toast_bad_id"))
	}
	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Respond(i18n.T(lang, "task.toast_not_found"))
	}
	_ = s.Respond(i18n.Tf(lang, "task.toast_restarting_with", ActiveAgentName()))
	return handleRetryTaskByID(s, taskID)
}

// handleRetry — обработчик команды /retry.
func handleRetry(s ports.Session) error {
	lang := uiLang()

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
			if st == domain.TaskStatusPaused || st == domain.TaskStatusFailed || st == domain.TaskStatusCompleted {
				targetID = t.ID
				break
			}
		}
	}
	if targetID == 0 {
		return s.Send(i18n.T(lang, "task.usage_retry"), ports.Rich())
	}

	return handleRetryTaskByID(s, targetID)
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

// commandPayload возвращает полный текст аргументов команды (включая переносы строк),
// либо fallback из s.Args() для моковых сессий без сырого Text().
func commandPayload(s ports.Session) string {
	if s == nil {
		return ""
	}
	if payload, ok := utils.ExtractCommandPayload(s.Text()); ok {
		return payload
	}
	return strings.TrimSpace(strings.Join(s.Args(), " "))
}

// stripLeadingWords удаляет count первых слов из строки text, сохраняя форматирование и переносы остатка.
func stripLeadingWords(text string, count int) string {
	idx := 0
	for i := 0; i < count && idx < len(text); i++ {
		for idx < len(text) {
			r, size := utf8.DecodeRuneInString(text[idx:])
			if !unicode.IsSpace(r) {
				break
			}
			idx += size
		}
		for idx < len(text) {
			r, size := utf8.DecodeRuneInString(text[idx:])
			if unicode.IsSpace(r) {
				break
			}
			idx += size
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
	config.ProjectState.RUnlock()

	targetProj, targetAgent, prompt := parseNewTaskInput(text, curProj, curAgent)
	return createTaskInProject(s, targetProj, targetAgent, prompt, requiresPlan)
}

func createTaskInProject(s ports.Session, targetProj, targetAgent, prompt string, requiresPlan bool) error {
	curAgent := ActiveAgentName()
	lang := uiLang()

	if strings.TrimSpace(prompt) == "" {
		return s.Send(i18n.T(lang, "task.usage_new"), ports.Rich())
	}

	if strings.TrimSpace(targetProj) == "" {
		config.ProjectState.RLock()
		targetProj = config.ProjectState.CurrentProject
		config.ProjectState.RUnlock()
	}
	if strings.TrimSpace(targetProj) == "" {
		return s.Send(i18n.T(lang, "projects.select_first"), nil)
	}

	if targetAgent == "" {
		targetAgent = curAgent
	}
	if targetAgent != "" && !strings.EqualFold(targetAgent, curAgent) {
		if _, err := SwitchActiveAgent(targetAgent); err != nil {
			return s.Send(i18n.Tf(lang, "task.agent_switch_failed_named", html.EscapeString(targetAgent), ErrorText(err, lang)), ports.Rich())
		}
	}

	config.ProjectState.RLock()
	curMod := config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent(targetProj, curMod, targetAgent, prompt, s.Chat(), requiresPlan)
	syncLegacySession(task)

	taskID := task.Snapshot().ID
	if domain.GlobalTaskManager.HasRunningTaskInProject(targetProj) {
		planNote := ""
		if requiresPlan {
			planNote = i18n.T(lang, "task.queued_new_plan_note")
		}
		msg := i18n.Tf(lang, "task.queued_new",
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

	domain.GlobalTokenTracker.StartTaskWithAgent(targetProj, curMod, targetAgent, prompt)
	workDir := filepath.Join(config.ProjectsRoot, targetProj)

	go runAgentTaskPipeline(s.Messenger(), s.Chat(), task, workDir, config.ProjectsRoot)

	return nil
}

var numberedMultiAnswerRx = regexp.MustCompile(`(?m)^\s*#?[1-9]\d*[.)\]:]\s+\S+`)

func hasMultipleNumberedAnswers(text string) bool {
	return len(numberedMultiAnswerRx.FindAllString(text, 2)) >= 2
}

func handleAddFollowupToTask(s ports.Session, taskID int, text string) error {
	lang := uiLang()

	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Send(i18n.Tf(lang, "task.not_found_short", taskID), ports.Rich())
	}

	view12 := task.Snapshot()
	status := view12.Status
	hadActivePipeline := view12.HasActivePipeline

	if status == domain.TaskStatusWaitingApproval {
		if isConfirmationText(text) {
			return handleApprovePlan(s.Messenger(), s.Chat(), taskID)
		}
		return handleRevisePlan(s.Messenger(), s.Chat(), taskID, text)
	}

	if (status == domain.TaskStatusPaused || status == domain.TaskStatusCancelled || (status == domain.TaskStatusWaitingInput && !hadActivePipeline)) && HasAgentConflict(task, ActiveAgentName()) {
		task.Update(func(t *domain.TaskSession) {
			t.CurrentPrompt = text
		})
		domain.GlobalTaskManager.SaveTask(task)
		return sendAgentConflictDialog(s, task)
	}

	answerText := text
	if (status == domain.TaskStatusWaitingInput || status == domain.TaskStatusPaused) && len(view12.PendingQuestions) > 1 {
		if !view12.QuestionAcceptAllTxt && !hasMultipleNumberedAnswers(text) {
			done, combined := recordSingleQuestionAnswerAndAdvance(task, view12.CurrentQuestionIndex, text, lang)
			if !done {
				domain.GlobalTaskManager.SaveTask(task)
				updatedView := task.Snapshot()
				isPlanning := updatedView.RequiresPlan && !updatedView.PlanApproved
				qRef, _ := s.Messenger().Send(context.Background(), s.Chat(),
					formatTaskQuestionMessage(updatedView, isPlanning, lang),
					ports.RichWith(buildQuestionMarkup(task, lang)))
				if qRef.ID != "" {
					domain.GlobalTaskManager.RegisterMessageTask(qRef, taskID)
				}
				return nil
			}
			answerText = combined
		}
	}

	if status == domain.TaskStatusWaitingInput || status == domain.TaskStatusPaused {
		task.Update(func(t *domain.TaskSession) {
			t.ClearPendingQuestionLocked()
		})
		domain.GlobalTaskManager.SaveTask(task)
	}

	task, qLen, isAnswer, err := domain.GlobalTaskManager.AddFollowup(taskID, answerText)
	if err != nil {
		return s.Send(i18n.Tf(lang, "task.followup_failed", taskID, ErrorText(err, lang)), ports.Rich())
	}

	syncLegacySession(task)

	if isAnswer {
		view13 := task.Snapshot()
		curStatus := view13.Status
		proj := view13.Project

		if hadActivePipeline || view13.HasActivePipeline {
			return s.Send(i18n.Tf(lang, "task.answer_delivered", taskID, html.EscapeString(proj)), ports.Rich())
		}

		if curStatus == domain.TaskStatusQueued {
			return s.Send(i18n.Tf(lang, "task.queued_with_answer",
				taskID, html.EscapeString(proj), html.EscapeString(utils.TruncateString(answerText, 250))), ports.Rich())
		}

		if HasAgentConflict(task, ActiveAgentName()) {
			task.Update(func(t *domain.TaskSession) {
				t.CurrentPrompt = answerText
			})
			domain.GlobalTaskManager.SaveTask(task)
			return sendAgentConflictDialog(s, task)
		}

		workDir := filepath.Join(config.ProjectsRoot, proj)
		go runAgentTaskPipeline(s.Messenger(), s.Chat(), task, workDir, config.ProjectsRoot)
		return s.Send(i18n.Tf(lang, "task.resumed_with_followup",
			taskID, html.EscapeString(proj), html.EscapeString(utils.TruncateString(answerText, 250))), ports.Rich())
	}

	msg := i18n.Tf(lang, "task.followup_saved",
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
func sendTaskHistory(s ports.Session, task *domain.TaskSession, lang string) error {
	if task == nil {
		return s.Send(i18n.T(lang, "task.not_found_plain"), ports.Rich())
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
		return s.Send(i18n.Tf(lang, "history.no_session", id, html.EscapeString(agentName)), ports.Rich())
	}

	workDir := filepath.Join(config.ProjectsRoot, proj)
	turns, err := transcript.ReadSession(workDir, convID)
	if err != nil {
		if errors.Is(err, transcript.ErrSessionNotFound) {
			return s.Send(i18n.Tf(lang, "history.file_missing", id), ports.Rich())
		}
		log.Printf("history: cannot read the session of task #%d: %v", id, err)
		return s.Send(i18n.Tf(lang, "history.read_failed", id), ports.Rich())
	}
	if len(turns) == 0 {
		return s.Send(i18n.Tf(lang, "history.empty", id), ports.Rich())
	}

	const previewLimit = 6
	const previewTurnRunes = 500

	previewStart := 0
	if len(turns) > previewLimit {
		previewStart = len(turns) - previewLimit
	}

	var bldr strings.Builder
	bldr.WriteString(i18n.Tf(lang, "history.header", id, html.EscapeString(proj), len(turns)))

	truncatedAny := previewStart > 0
	for _, t := range turns[previewStart:] {
		label := i18n.T(lang, "history.label_agent")
		if t.Role == "user" {
			label = i18n.T(lang, "history.label_user")
		}
		if len([]rune(t.Text)) > previewTurnRunes {
			truncatedAny = true
		}
		bldr.WriteString(fmt.Sprintf("%s:\n<i>%s</i>\n\n", label, html.EscapeString(utils.TruncateString(t.Text, previewTurnRunes))))
	}
	if previewStart > 0 {
		bldr.WriteString(i18n.Tf(lang, "history.tail_note", previewLimit, len(turns)))
	}
	if truncatedAny {
		bldr.WriteString(i18n.T(lang, "history.full_in_file"))
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
		Caption:  i18n.Tf(lang, "history.doc_caption", id, proj),
		Content:  []byte(formatHistoryDocument(turns, proj, id, lang)),
	})
}

// formatHistoryDocument форматирует полную переписку задачи в Markdown для
// отправки файлом, без каких-либо сокращений.
func formatHistoryDocument(turns []transcript.Turn, proj string, id int, lang string) string {
	var bldr strings.Builder
	bldr.WriteString(i18n.Tf(lang, "history.doc_title", id, proj))
	for _, t := range turns {
		label := i18n.T(lang, "history.doc_label_agent")
		if t.Role == "user" {
			label = i18n.T(lang, "history.doc_label_user")
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

func buildResumeMarkup(taskID int, lang string) *ports.Keyboard {
	return &ports.Keyboard{Rows: [][]ports.Button{
		{
			{Text: i18n.T(lang, "btn.resume"), Action: "q_resume", Payload: strconv.Itoa(taskID)},
			{Text: i18n.T(lang, "btn.cancel"), Action: "plan_cancel", Payload: strconv.Itoa(taskID)},
		},
	}}
}

func formatTaskQuestionMessage(view domain.TaskView, isPlanning bool, lang string) string {
	totalQ := len(view.PendingQuestions)
	projHTML := html.EscapeString(view.Project)

	if totalQ <= 1 {
		rawQ := view.LastQuestion
		var isMultiSelect bool
		if totalQ == 1 {
			rawQ = view.PendingQuestions[0].Question
			isMultiSelect = view.PendingQuestions[0].MultiSelect
		}
		body := utils.MarkdownToTelegramHTML(rawQ)
		if isMultiSelect {
			body += i18n.Tf(lang, "task.multi_select_hint", html.EscapeString(i18n.T(lang, "btn.q_submit")))
		}
		headerKey := "pipeline.question_header"
		if isPlanning {
			headerKey = "pipeline.question_header_planning"
		}
		return i18n.Tf(lang, headerKey, view.ID, projHTML, body)
	}

	if view.QuestionAcceptAllTxt {
		var listBldr strings.Builder
		for i, q := range view.PendingQuestions {
			if i > 0 {
				listBldr.WriteString("\n\n")
			}
			listBldr.WriteString(fmt.Sprintf("<b>%d.</b> %s", i+1, utils.MarkdownToTelegramHTML(q.Question)))
			if len(q.Options) > 0 {
				for _, opt := range q.Options {
					listBldr.WriteString("\n   • " + html.EscapeString(opt))
				}
			}
		}
		return i18n.Tf(lang, "task.multi_question_all_text_prompt", view.ID, projHTML, totalQ, listBldr.String())
	}

	idx := view.CurrentQuestionIndex
	if idx < 0 || idx >= totalQ {
		idx = 0
	}
	curQ := view.PendingQuestions[idx]

	var bodyBldr strings.Builder
	bodyBldr.WriteString(i18n.Tf(lang, "task.multi_question_progress", idx+1, totalQ, utils.MarkdownToTelegramHTML(curQ.Question)))
	if curQ.MultiSelect {
		bodyBldr.WriteString(i18n.Tf(lang, "task.multi_select_hint", html.EscapeString(i18n.T(lang, "btn.q_submit"))))
	}

	bodyBldr.WriteString(i18n.Tf(lang, "task.multi_question_list_header", totalQ))
	answeredMark := i18n.T(lang, "task.multi_question_answered_mark")
	currentMark := i18n.T(lang, "task.multi_question_current_mark")
	pendingMark := i18n.T(lang, "task.multi_question_pending_mark")
	for i, q := range view.PendingQuestions {
		mark := pendingMark
		if strings.TrimSpace(q.Answer) != "" {
			mark = answeredMark
		} else if i == idx {
			mark = currentMark
		}
		ansSuffix := ""
		if strings.TrimSpace(q.Answer) != "" {
			ansSuffix = i18n.Tf(lang, "task.multi_question_answer_inline", html.EscapeString(utils.TruncateString(q.Answer, 60)))
		}
		bodyBldr.WriteString(fmt.Sprintf("%s %d. %s%s\n",
			mark, i+1, html.EscapeString(utils.TruncateString(q.Question, 80)), ansSuffix))
	}

	headerKey := "task.question_header_multi"
	if isPlanning {
		headerKey = "task.question_header_planning_multi"
	}
	return i18n.Tf(lang, headerKey, view.ID, projHTML, idx+1, totalQ, strings.TrimSpace(bodyBldr.String()))
}

func buildQuestionMarkup(task *domain.TaskSession, lang string) *ports.Keyboard {
	view15 := task.Snapshot()
	taskID := view15.ID
	totalQ := len(view15.PendingQuestions)
	qIdx := view15.CurrentQuestionIndex

	var (
		options       []string
		isMultiSelect bool
		selectedSet   = make(map[int]bool)
	)
	if totalQ > 0 {
		if qIdx < 0 || qIdx >= totalQ {
			qIdx = 0
		}
		q := view15.PendingQuestions[qIdx]
		options = append([]string(nil), q.Options...)
		isMultiSelect = q.MultiSelect
		for _, sel := range q.SelectedIndices {
			selectedSet[sel] = true
		}
	} else {
		qIdx = 0
		options = append([]string(nil), view15.QuestionOptions...)
	}

	var rows [][]ports.Button

	if !view15.QuestionAcceptAllTxt && len(options) > 0 {
		var optButtons []ports.Button
		for i, opt := range options {
			cleanOpt := strings.TrimSpace(opt)
			if isMultiSelect {
				prefix := "⬜ "
				if selectedSet[i] {
					prefix = "☑️ "
				}
				btnText := prefix + utils.TruncateString(cleanOpt, 28)
				optButtons = append(optButtons, ports.Button{
					Text:    btnText,
					Action:  "q_toggle",
					Payload: fmt.Sprintf("%d:%d:%d", taskID, qIdx, i),
				})
			} else {
				btnText := fmt.Sprintf("%d. %s", i+1, utils.TruncateString(cleanOpt, 30))
				optButtons = append(optButtons, ports.Button{
					Text:    btnText,
					Action:  "q_choice",
					Payload: fmt.Sprintf("%d:%d:%d", taskID, qIdx, i),
				})
			}
		}

		allShort := true
		maxShortLen := 15
		if isMultiSelect {
			maxShortLen = 13
		}
		for _, opt := range options {
			if len([]rune(opt)) > maxShortLen {
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

		if isMultiSelect {
			rows = append(rows, []ports.Button{
				{Text: i18n.T(lang, "btn.q_submit"), Action: "q_submit", Payload: fmt.Sprintf("%d:%d", taskID, qIdx)},
			})
		}
	}

	if totalQ > 1 {
		var nav []ports.Button
		if view15.QuestionAcceptAllTxt || qIdx > 0 {
			nav = append(nav, ports.Button{Text: i18n.T(lang, "btn.q_back"), Action: "q_back", Payload: strconv.Itoa(taskID)})
		}
		if !view15.QuestionAcceptAllTxt {
			nav = append(nav, ports.Button{Text: i18n.T(lang, "btn.q_all_text"), Action: "q_all_text", Payload: strconv.Itoa(taskID)})
		}
		if len(nav) > 0 {
			rows = append(rows, nav)
		}
	}

	rows = append(rows, []ports.Button{
		{Text: i18n.T(lang, "btn.pause"), Action: "q_pause", Payload: strconv.Itoa(taskID)},
		{Text: i18n.T(lang, "btn.cancel"), Action: "plan_cancel", Payload: strconv.Itoa(taskID)},
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

	lang := i18n.Active()
	startKey := "task.queue_start_running"
	if isPlanning {
		startKey = "task.queue_start_planning"
	}
	_, _ = m.Send(context.Background(), chat, i18n.Tf(lang, startKey,
		nextID, html.EscapeString(project), html.EscapeString(utils.TruncateString(prompt, 80))), ports.Rich())

	domain.GlobalTokenTracker.StartTaskWithAgent(project, model, tAgent, prompt)
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

	fullHTML := utils.MarkdownToTelegramHTML(text)
	if utils.TelegramHTMLVisibleLen(fullHTML) <= utils.DefaultTelegramMaxChunkLen {
		_, _ = m.Send(context.Background(), chat, fullHTML, ports.Rich())
		return
	}

	chunks := utils.SplitMarkdown(text, utils.DefaultMarkdownMaxChunkLen)
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

func buildAgentConflictMarkup(taskID int, taskAgent, lang string) *ports.Keyboard {
	if taskAgent == "" {
		taskAgent = "agy"
	}
	return &ports.Keyboard{
		Rows: [][]ports.Button{
			{
				{Text: i18n.T(lang, "btn.agent_restart"), Action: "task_agent_restart", Payload: strconv.Itoa(taskID)},
				{Text: i18n.Tf(lang, "btn.agent_switch", taskAgent), Action: "task_agent_switch", Payload: strconv.Itoa(taskID)},
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

	lang := i18n.Active()
	markup := buildAgentConflictMarkup(id, agent, lang)
	msg := i18n.Tf(lang, "task.conflict_dialog",
		id, html.EscapeString(agent), html.EscapeString(convID), html.EscapeString(ActiveAgentName()),
	)
	_, err := m.Send(context.Background(), chat, msg, ports.RichWith(markup))
	return err
}
