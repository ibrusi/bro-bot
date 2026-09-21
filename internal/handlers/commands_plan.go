package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
	"context"
	"fmt"
	"html"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Команды и кнопки режима планирования: составление, утверждение и правка плана.

// handlePlan — обработчик команды /plan.
func handlePlan(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	if len(args) == 0 {
		config.ProjectState.RLock()
		mode := config.ProjectState.PlanMode
		config.ProjectState.RUnlock()

		statusStr := i18n.T(lang, "plan.mode_off_label")
		if mode {
			statusStr = i18n.T(lang, "plan.mode_on_label")
		}

		menu := &ports.Keyboard{Rows: [][]ports.Button{
			{{Text: i18n.T(lang, "btn.toggle_plan_mode"), Action: "plan_mode_toggle"}},
		}}
		return s.Send(i18n.Tf(lang, "plan.overview", statusStr), ports.RichWith(menu))
	}
	text := strings.TrimSpace(strings.Join(args, " "))
	return handleCreatePlanTask(s, text)
}

// handlePlanMode — обработчик команды /planmode.
func handlePlanMode(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	config.ProjectState.Lock()
	if len(args) == 0 {
		config.ProjectState.PlanMode = !config.ProjectState.PlanMode
	} else {
		arg := strings.ToLower(strings.TrimSpace(args[0]))
		switch arg {
		case "on", "enable", "true", "1", "вкл", "да":
			config.ProjectState.PlanMode = true
		case "off", "disable", "false", "0", "выкл", "нет":
			config.ProjectState.PlanMode = false
		case "toggle":
			config.ProjectState.PlanMode = !config.ProjectState.PlanMode
		default:
			config.ProjectState.Unlock()
			return s.Send(i18n.T(lang, "plan.mode_usage"), ports.Rich())
		}
	}
	newMode := config.ProjectState.PlanMode
	config.ProjectState.Unlock()

	if st := domain.GlobalTaskManager.Storage(); st != nil {
		_ = st.SetSetting(context.Background(), "plan_mode", strconv.FormatBool(newMode))
	}

	if newMode {
		return s.Send(i18n.T(lang, "plan.mode_enabled"), ports.Rich())
	}

	return s.Send(i18n.T(lang, "plan.mode_disabled"), ports.Rich())
}

// handlePlanFile — обработчик команды /planfile.
func handlePlanFile(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	var target *domain.TaskSession
	if len(args) > 0 {
		first := strings.TrimPrefix(args[0], "#")
		first = strings.TrimPrefix(first, "_")
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

	return sendTaskPlanDocument(s, target, lang)
}

// handleApprove — обработчик команды /approve.
func handleApprove(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	var targetID int
	if len(args) > 0 {
		first := strings.TrimPrefix(args[0], "#")
		var err error
		targetID, err = strconv.Atoi(first)
		if err != nil {
			return s.Send(i18n.T(lang, "plan.approve_usage"), ports.Rich())
		}
	} else {
		active := domain.GlobalTaskManager.GetActiveTask()
		if active == nil {
			return s.Send(i18n.T(lang, "plan.no_active_to_approve"), nil)
		}
		targetID = active.Snapshot().ID
	}
	return handleApprovePlan(s.Messenger(), s.Chat(), targetID)
}

// handleConfirm — обработчик команды /confirm.
func handleConfirm(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	var targetID int
	if len(args) > 0 {
		first := strings.TrimPrefix(args[0], "#")
		var err error
		targetID, err = strconv.Atoi(first)
		if err != nil {
			return s.Send(i18n.T(lang, "plan.confirm_usage"), ports.Rich())
		}
	} else {
		active := domain.GlobalTaskManager.GetActiveTask()
		if active == nil {
			return s.Send(i18n.T(lang, "plan.no_active_to_approve"), nil)
		}
		targetID = active.Snapshot().ID
	}
	return handleApprovePlan(s.Messenger(), s.Chat(), targetID)
}

// onPlanApprove — обработчик кнопки plan_approve.
func onPlanApprove(s ports.Session) error {
	lang := uiLang()

	id, err := strconv.Atoi(s.Callback().Payload)
	if err != nil {
		return s.Respond(i18n.T(lang, "task.toast_bad_id"))
	}
	_ = s.Respond(i18n.Tf(lang, "task.toast_plan_approved", id))
	return handleApprovePlan(s.Messenger(), s.Chat(), id)
}

// onPlanCancel — обработчик кнопки plan_cancel.
func onPlanCancel(s ports.Session) error {
	lang := uiLang()

	id, err := strconv.Atoi(s.Callback().Payload)
	if err != nil {
		return s.Respond(i18n.T(lang, "task.toast_bad_id"))
	}
	_ = s.Respond(i18n.Tf(lang, "task.toast_cancelled", id))
	task, err := domain.GlobalTaskManager.CancelTask(id)
	if err != nil {
		return s.Send("❌ "+ErrorText(err, lang), ports.Rich())
	}
	syncLegacySession(domain.GlobalTaskManager.GetActiveTask())
	domain.GlobalTokenTracker.CancelTask()
	project := task.Snapshot().Project
	checkAndStartQueuedTask(s.Messenger(), project, config.ProjectsRoot)
	return s.Send(i18n.Tf(lang, "task.cancelled_rich", id, html.EscapeString(project)), ports.Rich())
}

// onPlanModeToggle — обработчик кнопки plan_mode_toggle.
func onPlanModeToggle(s ports.Session) error {
	lang := uiLang()

	config.ProjectState.Lock()
	config.ProjectState.PlanMode = !config.ProjectState.PlanMode
	newMode := config.ProjectState.PlanMode
	config.ProjectState.Unlock()

	if st := domain.GlobalTaskManager.Storage(); st != nil {
		_ = st.SetSetting(context.Background(), "plan_mode", strconv.FormatBool(newMode))
	}

	if newMode {
		_ = s.Respond(i18n.T(lang, "plan.toast_mode_on"))
		return s.Send(i18n.T(lang, "plan.mode_enabled"), ports.Rich())
	}
	_ = s.Respond(i18n.T(lang, "plan.toast_mode_off"))
	return s.Send(i18n.T(lang, "plan.mode_disabled_button"), ports.Rich())
}

// onPlanApproveVariant — обработчик кнопки plan_appr_var.
func onPlanApproveVariant(s ports.Session) error {
	lang := uiLang()

	parts := strings.Split(s.Callback().Payload, ":")
	if len(parts) < 2 {
		return s.Respond(i18n.T(lang, "task.toast_bad_params"))
	}
	id, err1 := strconv.Atoi(parts[0])
	varIdx, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return s.Respond(i18n.T(lang, "task.toast_bad_params"))
	}
	task := domain.GlobalTaskManager.GetTask(id)
	if task == nil {
		return s.Respond(i18n.T(lang, "task.toast_not_found"))
	}
	view := task.Snapshot()
	planText := view.Plan
	variants := utils.ExtractPlanVariantOptions(planText)
	chosenVar := ""
	if varIdx >= 0 && varIdx < len(variants) {
		chosenVar = variants[varIdx]
	}
	_ = s.Respond(i18n.Tf(lang, "task.toast_variant_approved", utils.TruncateString(chosenVar, 20)))
	return handleApprovePlanWithVariant(s.Messenger(), s.Chat(), id, chosenVar)
}

// onPlanDoc — обработчик кнопки plan_doc.
func onPlanDoc(s ports.Session) error {
	lang := uiLang()

	id, err := strconv.Atoi(s.Callback().Payload)
	if err != nil {
		return s.Respond(i18n.T(lang, "task.toast_bad_id"))
	}
	task := domain.GlobalTaskManager.GetTask(id)
	if task == nil {
		return s.Respond(i18n.T(lang, "task.toast_not_found"))
	}

	if strings.TrimSpace(task.Snapshot().Plan) == "" {
		return s.Respond(i18n.T(lang, "task.toast_no_plan"))
	}

	_ = s.Respond(i18n.T(lang, "task.toast_sending_plan"))
	return sendTaskPlanDocument(s, task, lang)
}

func handleApprovePlan(m ports.Messenger, chat ports.ChatID, taskID int) error {
	return handleApprovePlanWithVariant(m, chat, taskID, "")
}

// buildImplementationPrompt собирает промпт реализации по утверждённому плану.
// Промпт тоже переводится: на его языке агент пишет отчёт, который бот пересылает
// пользователю в чат.
func buildImplementationPrompt(initialPrompt, planText, variantInstruction, lang string) string {
	return i18n.Tf(lang, "pipeline.implementation_prompt",
		initialPrompt,
		planText,
		variantInstruction,
	)
}

func handleApprovePlanWithVariant(m ports.Messenger, chat ports.ChatID, taskID int, variant string) error {
	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		_, err := m.Send(context.Background(), chat, i18n.Tf(uiLang(), "task.not_found_short", taskID), nil)
		return err
	}

	lang := uiLang()

	variantInstruction := ""
	if variant != "" {
		variantInstruction = i18n.Tf(lang, "pipeline.implementation_variant", variant)
	}

	// Проверка статуса, утверждение плана и запись промпта — одним куском: иначе план
	// можно утвердить дважды или запустить задачу со старым промптом.
	var (
		wrongStatus   bool
		statusTitle   string
		projectName   string
		modelName     string
		initialPrompt string
	)
	task.Update(func(t *domain.TaskSession) {
		if t.Status != domain.TaskStatusWaitingApproval {
			wrongStatus = true
			statusTitle = i18n.TaskStatusTitle(string(t.Status), lang)
			return
		}

		t.PlanApproved = true
		t.Status = domain.TaskStatusRunning
		t.StartedAt = time.Now()
		t.RecentLogs = nil

		projectName = t.Project
		modelName = t.Model
		initialPrompt = t.InitialPrompt
		t.CurrentPrompt = buildImplementationPrompt(t.InitialPrompt, utils.SanitizePlanText(t.Plan), variantInstruction, lang)
	})

	if wrongStatus {
		_, err := m.Send(context.Background(), chat, i18n.Tf(lang, "plan.not_waiting_approval", taskID, statusTitle), nil)
		return err
	}

	syncLegacySession(task)
	_, _ = domain.GlobalTaskManager.SetActiveTask(taskID)

	if HasAgentConflict(task, ActiveAgentName()) {
		domain.GlobalTaskManager.SaveTask(task)
		return sendAgentConflictDialogWithMessenger(m, chat, task)
	}

	_, _ = m.Send(context.Background(), chat, i18n.Tf(lang, "plan.approved", taskID, html.EscapeString(projectName)), ports.Rich())

	domain.GlobalTokenTracker.StartTaskWithAgent(projectName, modelName, initialPrompt, ActiveAgentName())
	workDir := filepath.Join(config.ProjectsRoot, projectName)
	go runAgentTaskPipeline(m, chat, task, workDir, config.ProjectsRoot)

	return nil
}

func handleRevisePlan(m ports.Messenger, chat ports.ChatID, taskID int, feedback string) error {
	lang := uiLang()

	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return &domain.TaskNotFoundError{ID: taskID}
	}

	var projectName string
	task.Update(func(t *domain.TaskSession) {
		t.Status = domain.TaskStatusPlanning
		t.CurrentPrompt = feedback
		t.StartedAt = time.Now()
		t.RecentLogs = nil
		projectName = t.Project
	})

	syncLegacySession(task)
	_, _ = domain.GlobalTaskManager.SetActiveTask(taskID)

	if HasAgentConflict(task, ActiveAgentName()) {
		domain.GlobalTaskManager.SaveTask(task)
		return sendAgentConflictDialogWithMessenger(m, chat, task)
	}

	_, _ = m.Send(context.Background(), chat, i18n.Tf(lang, "plan.revising", taskID, html.EscapeString(utils.TruncateString(feedback, 100))), ports.Rich())

	workDir := filepath.Join(config.ProjectsRoot, projectName)
	go runAgentTaskPipeline(m, chat, task, workDir, config.ProjectsRoot)

	return nil
}

func sendTaskPlanDocument(s ports.Session, task *domain.TaskSession, lang string) error {
	if task == nil {
		return s.Send(i18n.T(lang, "task.not_found_plain"), ports.Rich())
	}

	view3 := task.Snapshot()
	planText := strings.TrimSpace(view3.Plan)
	id := view3.ID
	proj := view3.Project

	if planText == "" {
		return s.Send(i18n.Tf(lang, "plan.none", id), ports.Rich())
	}

	doc := ports.Document{
		FileName: fmt.Sprintf("plan_task_%d.md", id),
		MIME:     "text/markdown",
		Caption:  i18n.Tf(lang, "plan.doc_caption", id, proj),
		Content:  []byte(planText),
	}
	return s.SendDocument(doc)
}

func sendPlanForApproval(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession, lang string) {
	view4 := task.Snapshot()
	taskID := view4.ID
	projectName := view4.Project
	initialPrompt := view4.InitialPrompt
	planText := strings.TrimSpace(utils.SanitizePlanText(view4.Plan))
	if planText == "" {
		planText = i18n.T(lang, "plan.empty_fallback")
	}

	var rows [][]ports.Button

	// Проверяем наличие альтернативных вариантов в плане
	variants := utils.ExtractPlanVariantOptions(planText)
	if len(variants) > 0 {
		for i, v := range variants {
			btnText := i18n.Tf(lang, "btn.approve_variant", utils.TruncateString(v, 24))
			rows = append(rows, []ports.Button{{Text: btnText, Action: "plan_appr_var", Payload: fmt.Sprintf("%d:%d", taskID, i)}})
		}
	}

	approveLabel := i18n.T(lang, "btn.approve_and_start")
	cancelLabel := i18n.T(lang, "btn.cancel")
	rows = append(rows, []ports.Button{
		{Text: approveLabel, Action: "plan_approve", Payload: strconv.Itoa(taskID)},
		{Text: cancelLabel, Action: "plan_cancel", Payload: strconv.Itoa(taskID)},
	})
	rows = append(rows, []ports.Button{{Text: i18n.T(lang, "btn.download_plan"), Action: "plan_doc", Payload: strconv.Itoa(taskID)}})
	planMenu := &ports.Keyboard{Rows: rows}

	planRunes := []rune(planText)
	isLongPlan := len(planRunes) > maxInlinePlanRunes

	var planDisplayHTML string
	var planNote string
	if isLongPlan {
		summary := utils.ExtractPlanSummary(planText, maxInlinePlanRunes)
		planDisplayHTML = utils.MarkdownToTelegramHTML(summary)
		planNote = i18n.Tf(lang, "plan.note_long", len(planRunes), taskID)
	} else {
		planDisplayHTML = utils.MarkdownToTelegramHTML(planText)
		planNote = i18n.Tf(lang, "plan.note_short", taskID)
	}

	promptSnippet := utils.TruncateString(initialPrompt, 250)

	msgText := i18n.Tf(lang, "plan.approval_request",
		taskID, html.EscapeString(projectName),
		html.EscapeString(promptSnippet),
		planDisplayHTML, planNote,
		approveLabel, taskID, taskID,
		cancelLabel, taskID,
	)

	ctlRef, _ := m.Send(context.Background(), chat, msgText, ports.RichWith(planMenu))
	if ctlRef.ID != "" {
		domain.GlobalTaskManager.RegisterMessageTask(ctlRef, taskID)
	}
}

const maxInlinePlanRunes = 1200
