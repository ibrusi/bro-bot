package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
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
	args := s.Args()
	if len(args) == 0 {
		config.ProjectState.RLock()
		mode := config.ProjectState.PlanMode
		config.ProjectState.RUnlock()

		statusStr := "❌ выключен"
		if mode {
			statusStr = "✅ включен"
		}

		msg := fmt.Sprintf(
			"📝 <b>Режим планирования задач:</b>\n\n"+
				"В этом режиме агент сначала подробно исследует кодовую базу, формирует пошаговый план, отправляет его вам на утверждение, и <b>только после вашего одобрения</b> приступает к автономной реализации (создание ветки, код, тесты, PR).\n\n"+
				"• Обязательный план для всех задач: <b>%s</b>\n\n"+
				"<b>Команды:</b>\n"+
				"• <code>/plan &lt;описание задачи&gt;</code> — создать задачу с обязательным планом\n"+
				"• <code>/plan &lt;проект&gt; &lt;описание&gt;</code> — создать задачу с планом для проекта\n"+
				"• <code>/planmode [on|off]</code> — включить/выключить обязательный план для всех задач\n"+
				"• <code>/approve [id]</code> — утвердить план задачи и начать реализацию\n\n"+
				"💡 <i>Вы можете переключить режим кнопкой ниже:</i>",
			statusStr,
		)

		menu := &ports.Keyboard{Rows: [][]ports.Button{
			{{Text: "🔄 Переключить Plan Mode", Action: "plan_mode_toggle"}},
		}}
		return s.Send(msg, ports.RichWith(menu))
	}
	text := strings.TrimSpace(strings.Join(args, " "))
	return handleCreatePlanTask(s, text)
}

// handlePlanMode — обработчик команды /planmode.
func handlePlanMode(s ports.Session) error {
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
			return s.Send("Использование: <code>/planmode [on|off|toggle]</code>", ports.Rich())
		}
	}
	newMode := config.ProjectState.PlanMode
	config.ProjectState.Unlock()

	if st := domain.GlobalTaskManager.Storage(); st != nil {
		_ = st.SetSetting(context.Background(), "plan_mode", strconv.FormatBool(newMode))
	}

	if newMode {
		return s.Send("✅ <b>Режим обязательного планирования ВКЛЮЧЕН.</b>\nВсе новые задачи будут сначала составлять план и ожидать вашего утверждения.", ports.Rich())
	}

	return s.Send("ℹ️ <b>Режим обязательного планирования ВЫКЛЮЧЕН.</b>\nНовые задачи будут сразу приступать к реализации (для плана используйте <code>/plan &lt;задача&gt;</code>).", ports.Rich())
}

// handlePlanFile — обработчик команды /planfile.
func handlePlanFile(s ports.Session) error {
	args := s.Args()
	var target *domain.TaskSession
	if len(args) > 0 {
		first := strings.TrimPrefix(args[0], "#")
		first = strings.TrimPrefix(first, "_")
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

	return sendTaskPlanDocument(s, target)
}

// handleApprove — обработчик команды /approve.
func handleApprove(s ports.Session) error {
	args := s.Args()
	var targetID int
	if len(args) > 0 {
		first := strings.TrimPrefix(args[0], "#")
		var err error
		targetID, err = strconv.Atoi(first)
		if err != nil {
			return s.Send("Укажите номер задачи. Пример: <code>/approve 1</code>", ports.Rich())
		}
	} else {
		active := domain.GlobalTaskManager.GetActiveTask()
		if active == nil {
			return s.Send("Нет активных задач для утверждения.", nil)
		}
		targetID = active.Snapshot().ID
	}
	return handleApprovePlan(s.Messenger(), s.Chat(), targetID)
}

// handleConfirm — обработчик команды /confirm.
func handleConfirm(s ports.Session) error {
	args := s.Args()
	var targetID int
	if len(args) > 0 {
		first := strings.TrimPrefix(args[0], "#")
		var err error
		targetID, err = strconv.Atoi(first)
		if err != nil {
			return s.Send("Укажите номер задачи. Пример: <code>/confirm 1</code>", ports.Rich())
		}
	} else {
		active := domain.GlobalTaskManager.GetActiveTask()
		if active == nil {
			return s.Send("Нет активных задач для утверждения.", nil)
		}
		targetID = active.Snapshot().ID
	}
	return handleApprovePlan(s.Messenger(), s.Chat(), targetID)
}

// onPlanApprove — обработчик кнопки plan_approve.
func onPlanApprove(s ports.Session) error {
	idStr := s.Callback().Payload
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return s.Respond("Некорректный номер задачи")
	}
	_ = s.Respond(fmt.Sprintf("План #%d утверждён", id))
	return handleApprovePlan(s.Messenger(), s.Chat(), id)
}

// onPlanCancel — обработчик кнопки plan_cancel.
func onPlanCancel(s ports.Session) error {
	idStr := s.Callback().Payload
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return s.Respond("Некорректный номер задачи")
	}
	_ = s.Respond(fmt.Sprintf("Задача #%d отменена", id))
	task, err := domain.GlobalTaskManager.CancelTask(id)
	if err != nil {
		return s.Send(fmt.Sprintf("❌ %s", err.Error()), ports.Rich())
	}
	syncLegacySession(domain.GlobalTaskManager.GetActiveTask())
	domain.GlobalTokenTracker.CancelTask()
	project := task.Snapshot().Project
	checkAndStartQueuedTask(s.Messenger(), project, config.ProjectsRoot)
	return s.Send(fmt.Sprintf("🛑 <b>Задача #%d (<code>%s</code>) остановлена.</b>", id, html.EscapeString(project)), ports.Rich())
}

// onPlanModeToggle — обработчик кнопки plan_mode_toggle.
func onPlanModeToggle(s ports.Session) error {
	config.ProjectState.Lock()
	config.ProjectState.PlanMode = !config.ProjectState.PlanMode
	newMode := config.ProjectState.PlanMode
	config.ProjectState.Unlock()

	if st := domain.GlobalTaskManager.Storage(); st != nil {
		_ = st.SetSetting(context.Background(), "plan_mode", strconv.FormatBool(newMode))
	}

	if newMode {
		_ = s.Respond("Режим планирования включен")
		return s.Send("✅ <b>Режим обязательного планирования ВКЛЮЧЕН.</b>\nВсе новые задачи будут сначала формировать план и ожидать вашего утверждения.", ports.Rich())
	}
	_ = s.Respond("Режим планирования выключен")
	return s.Send("ℹ️ <b>Режим обязательного планирования ВЫКЛЮЧЕН.</b>\nДля создания задач с планом используйте <code>/plan &lt;задача&gt;</code>.", ports.Rich())
}

// onPlanApproveVariant — обработчик кнопки plan_appr_var.
func onPlanApproveVariant(s ports.Session) error {
	parts := strings.Split(s.Callback().Payload, ":")
	if len(parts) < 2 {
		return s.Respond("Некорректные параметры")
	}
	id, err1 := strconv.Atoi(parts[0])
	varIdx, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return s.Respond("Некорректный номер задачи или варианта")
	}
	task := domain.GlobalTaskManager.GetTask(id)
	if task == nil {
		return s.Respond("Задача не найдена")
	}
	view := task.Snapshot()
	planText := view.Plan
	variants := utils.ExtractPlanVariantOptions(planText)
	chosenVar := ""
	if varIdx >= 0 && varIdx < len(variants) {
		chosenVar = variants[varIdx]
	}
	_ = s.Respond(fmt.Sprintf("Утверждён вариант: %s", utils.TruncateString(chosenVar, 20)))
	return handleApprovePlanWithVariant(s.Messenger(), s.Chat(), id, chosenVar)
}

// onPlanDoc — обработчик кнопки plan_doc.
func onPlanDoc(s ports.Session) error {
	idStr := s.Callback().Payload
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return s.Respond("Некорректный номер задачи")
	}
	task := domain.GlobalTaskManager.GetTask(id)
	if task == nil {
		return s.Respond("Задача не найдена")
	}
	view2 := task.Snapshot()
	planText := strings.TrimSpace(view2.Plan)

	if planText == "" {
		return s.Respond("У задачи нет сформированного плана")
	}

	_ = s.Respond("Отправляю файл плана...")
	return sendTaskPlanDocument(s, task)
}

func handleApprovePlan(m ports.Messenger, chat ports.ChatID, taskID int) error {
	return handleApprovePlanWithVariant(m, chat, taskID, "")
}

// buildImplementationPrompt собирает промпт реализации по утверждённому плану.
func buildImplementationPrompt(initialPrompt, planText, variantInstruction string) string {
	return fmt.Sprintf(
		"Задача пользователя: %s\n\n"+
			"УТВЕРЖДЁННЫЙ ПЛАН РЕАЛИЗАЦИИ:\n%s%s\n\n"+
			"Приступай к полной автономной реализации задачи в точности по утверждённому плану и инструкциям в AGENT.md:\n"+
			"1. Создай ветку от актуального main: feat/... или fix/...\n"+
			"2. Реализуй все пункты плана.\n"+
			"3. Проверь код тестами и линтерами.\n"+
			"4. Закоммить изменения (Conventional Commits) и запушь ветку.\n"+
			"5. Открой Pull Request через GitHub CLI (gh pr create --fill).\n"+
			"6. В самом конце ответа обязательно выведи строчку строго в формате:\n"+
			"PR_URL: <полная web-ссылка на созданный PR>",
		initialPrompt,
		planText,
		variantInstruction,
	)
}

func handleApprovePlanWithVariant(m ports.Messenger, chat ports.ChatID, taskID int, variant string) error {
	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		_, err := m.Send(context.Background(), chat, fmt.Sprintf("❌ Задача #%d не найдена.", taskID), nil)
		return err
	}

	variantInstruction := ""
	if variant != "" {
		variantInstruction = fmt.Sprintf("\n\nПОЛЬЗОВАТЕЛЬ ВЫБРАЛ И УТВЕРДИЛ ВАРИАНТ:\n%s\nРеализуй задачу строго в соответствии с этим выбранным вариантом плана.", variant)
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
			statusTitle = t.Status.RussianTitle()
			return
		}

		t.PlanApproved = true
		t.Status = domain.TaskStatusRunning
		t.StartedAt = time.Now()
		t.RecentLogs = nil

		projectName = t.Project
		modelName = t.Model
		initialPrompt = t.InitialPrompt
		t.CurrentPrompt = buildImplementationPrompt(t.InitialPrompt, t.Plan, variantInstruction)
	})

	if wrongStatus {
		_, err := m.Send(context.Background(), chat, fmt.Sprintf("ℹ️ Задача #%d не ожидает утверждения плана (текущий статус: %s).", taskID, statusTitle), nil)
		return err
	}

	syncLegacySession(task)
	_, _ = domain.GlobalTaskManager.SetActiveTask(taskID)

	if HasAgentConflict(task, ActiveAgentName()) {
		domain.GlobalTaskManager.SaveTask(task)
		return sendAgentConflictDialogWithMessenger(m, chat, task)
	}

	_, _ = m.Send(context.Background(), chat, fmt.Sprintf("🚀 <b>План задачи #%d утверждён!</b>\nПриступаю к автономной реализации в <code>%s</code>...", taskID, html.EscapeString(projectName)), ports.Rich())

	domain.GlobalTokenTracker.StartTaskWithAgent(projectName, modelName, initialPrompt, ActiveAgentName())
	workDir := filepath.Join(config.ProjectsRoot, projectName)
	go runAgentTaskPipeline(m, chat, task, workDir, config.ProjectsRoot)

	return nil
}

func handleRevisePlan(m ports.Messenger, chat ports.ChatID, taskID int, feedback string) error {
	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return fmt.Errorf("задача #%d не найдена", taskID)
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

	_, _ = m.Send(context.Background(), chat, fmt.Sprintf("📝 <b>Задача #%d: Обновляю план с учётом замечаний...</b>\n<i>«%s»</i>", taskID, html.EscapeString(utils.TruncateString(feedback, 100))), ports.Rich())

	workDir := filepath.Join(config.ProjectsRoot, projectName)
	go runAgentTaskPipeline(m, chat, task, workDir, config.ProjectsRoot)

	return nil
}

func sendTaskPlanDocument(s ports.Session, task *domain.TaskSession) error {
	if task == nil {
		return s.Send("❌ Задача не найдена. Список задач: /tasks", ports.Rich())
	}

	view3 := task.Snapshot()
	planText := strings.TrimSpace(view3.Plan)
	id := view3.ID
	proj := view3.Project

	if planText == "" {
		return s.Send(fmt.Sprintf("ℹ️ У задачи #%d нет сформированного плана.", id), ports.Rich())
	}

	docName := fmt.Sprintf("plan_task_%d.md", id)
	doc := ports.Document{
		FileName: docName,
		MIME:     "text/markdown",
		Caption:  fmt.Sprintf("📄 Полный план реализации задачи #%d (%s)", id, proj),
		Content:  []byte(planText),
	}
	return s.SendDocument(doc)
}

func sendPlanForApproval(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession) {
	view4 := task.Snapshot()
	taskID := view4.ID
	projectName := view4.Project
	initialPrompt := view4.InitialPrompt
	planText := strings.TrimSpace(view4.Plan)

	var rows [][]ports.Button

	// Проверяем наличие альтернативных вариантов в плане
	variants := utils.ExtractPlanVariantOptions(planText)
	if len(variants) > 0 {
		for i, v := range variants {
			btnText := fmt.Sprintf("Утвердить: %s", utils.TruncateString(v, 24))
			rows = append(rows, []ports.Button{{Text: btnText, Action: "plan_appr_var", Payload: fmt.Sprintf("%d:%d", taskID, i)}})
		}
	}

	rows = append(rows, []ports.Button{
		{Text: "✅ Утвердить и начать", Action: "plan_approve", Payload: strconv.Itoa(taskID)},
		{Text: "❌ Отменить", Action: "plan_cancel", Payload: strconv.Itoa(taskID)},
	})
	rows = append(rows, []ports.Button{{Text: "📄 Скачать план (.md)", Action: "plan_doc", Payload: strconv.Itoa(taskID)}})
	planMenu := &ports.Keyboard{Rows: rows}

	planRunes := []rune(planText)
	isLongPlan := len(planRunes) > maxInlinePlanRunes

	var planDisplayHTML string
	var planNote string
	if isLongPlan {
		summary := utils.ExtractPlanSummary(planText, maxInlinePlanRunes)
		planDisplayHTML = utils.MarkdownToTelegramHTML(summary)
		planNote = fmt.Sprintf("\n\n📄 <i>Полный детальный план (%d знаков):</i> /planfile_%d (или кнопка ниже)", len(planRunes), taskID)
	} else {
		planDisplayHTML = utils.MarkdownToTelegramHTML(planText)
		planNote = fmt.Sprintf("\n\n📄 <i>Полный план:</i> /planfile_%d", taskID)
	}

	promptSnippet := utils.TruncateString(initialPrompt, 250)

	msgText := fmt.Sprintf(
		"📋 <b>План реализации задачи #%d</b> (<code>%s</code>):\n\n"+
			"📝 <b>Задача:</b> <i>«%s»</i>\n\n"+
			"%s%s\n\n"+
			"👆 <b>План ожидает вашего утверждения:</b>\n"+
			"• Нажмите <b>«✅ Утвердить и начать»</b> (или выберите вариант) либо <code>/approve %d</code>.\n"+
			"• Для правок отправьте <code>/add %d &lt;замечания&gt;</code> или ответьте на сообщение.\n"+
			"• Для отмены: <b>«❌ Отменить»</b> или <code>/cancel %d</code>.",
		taskID, html.EscapeString(projectName),
		html.EscapeString(promptSnippet),
		planDisplayHTML, planNote,
		taskID, taskID, taskID,
	)

	ctlRef, _ := m.Send(context.Background(), chat, msgText, ports.RichWith(planMenu))
	if ctlRef.ID != "" {
		domain.GlobalTaskManager.RegisterMessageTask(ctlRef, taskID)
	}
}

const maxInlinePlanRunes = 1200
