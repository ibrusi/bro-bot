package handlers

import (
	"bro-bot/internal/adapters/cliproc"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
	"bufio"
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Пайплайн выполнения задачи: запуск шагов агента, разбор потока событий,
// таймауты и ошибки шага.

func runAgentPipeline(m ports.Messenger, chat ports.ChatID, workDir, projectName, initialPrompt string) {
	config.ProjectState.RLock()
	modelName := config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent(projectName, modelName, ActiveAgentName(), initialPrompt, chat, false)
	task.Update(func(t *domain.TaskSession) {
		t.Status = domain.TaskStatusRunning
		t.StartedAt = time.Now()
	})

	syncLegacySession(task)
	runAgentTaskPipeline(m, chat, task, workDir, config.ProjectsRoot)
}

// runAgentTaskPipeline ведёт задачу от первого шага до отчёта. Корень проектов приходит
// параметром: пайплайн живёт в фоновой горутине минутами, а config.ProjectsRoot —
// снимок конфигурации, который переписывается при старте бота. Читать его по ходу
// работы значит гонку, которую видит детектор гонок в тестах, перезапускающих бота.
func runAgentTaskPipeline(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession, workDir, projectsRoot string) {
	// Язык интерфейса снимаем один раз на весь прогон: пайплайн живёт минутами,
	// и сообщения одной задачи не должны оказаться на разных языках, если
	// пользователь переключит язык по ходу работы.
	lang := i18n.Active()

	// Имя активного агента читаем до снимка задачи: activeAgentMu остаётся листовым
	// мьютексом и никогда не берётся внутри чужих блокировок.
	fallbackAgent := ActiveAgentName()

	view := task.Snapshot()
	projectName := view.Project
	taskID := view.ID
	trackModel := view.Model
	trackPrompt := view.CurrentPrompt
	if trackPrompt == "" {
		trackPrompt = view.InitialPrompt
	}
	taskAgent := view.Agent
	if taskAgent == "" {
		taskAgent = fallbackAgent
	}
	domain.GlobalTokenTracker.StartTaskIfNotActiveWithAgent(projectName, trackModel, trackPrompt, taskAgent)

	// ЭТАП 1: Планирование (если требуется и ещё не утверждён)
	view2 := task.Snapshot()
	needsPlanning := view2.RequiresPlan && !view2.PlanApproved

	if needsPlanning {
		// Проверка отмены и перевод в планирование — одним куском: иначе задачу можно
		// отменить между ними и запустить уже отменённый шаг.
		var (
			cancelled        bool
			existingPlan     string
			curPrompt        string
			pendingFollowups []string
		)
		task.Update(func(t *domain.TaskSession) {
			if t.Status == domain.TaskStatusCancelled {
				cancelled = true
				return
			}
			t.Status = domain.TaskStatusPlanning
			existingPlan = t.Plan
			curPrompt = t.CurrentPrompt
			pendingFollowups = append([]string(nil), t.PendingFollowups...)
		})
		if cancelled {
			return
		}

		syncLegacySession(task)

		pendingSection := ""
		if len(pendingFollowups) > 0 {
			var pbldr strings.Builder
			pbldr.WriteString(i18n.T(lang, "pipeline.plan_pending_section"))
			for i, pf := range pendingFollowups {
				pbldr.WriteString(fmt.Sprintf("%d. %s\n", i+1, pf))
			}
			pbldr.WriteString(i18n.T(lang, "pipeline.plan_pending_footer"))
			pendingSection = pbldr.String()
		}

		var planningPrompt string
		if existingPlan == "" {
			if view.ConversationID != "" && curPrompt != "" && curPrompt != view.InitialPrompt {
				planningPrompt = curPrompt
			} else {
				planningPrompt = i18n.Tf(lang, "pipeline.plan_prompt", view.InitialPrompt, pendingSection)
			}
		} else {
			feedback := curPrompt
			if feedback == "" {
				feedback = view.InitialPrompt
			}
			planningPrompt = i18n.Tf(lang, "pipeline.plan_revise_prompt",
				view.InitialPrompt, existingPlan, feedback, pendingSection)
		}

		for {
			var (
				cancelled   bool
				activeModel string
			)
			task.Update(func(t *domain.TaskSession) {
				if t.Status == domain.TaskStatusCancelled {
					cancelled = true
					return
				}
				t.Status = domain.TaskStatusPlanning
				activeModel = t.Model
			})
			if cancelled {
				checkAndStartQueuedTask(m, projectName, projectsRoot)
				return
			}

			syncLegacySession(task)

			res := executeStepForTask(m, chat, task, workDir, planningPrompt, activeModel, lang)

			stepView := task.Snapshot()
			if stepView.Status == domain.TaskStatusCancelled || res.Outcome == StepOutcomeCancelled {
				checkAndStartQueuedTask(m, projectName, projectsRoot)
				return
			}
			st := stepView.Status

			if res.Outcome == StepOutcomeWaitingInput || st == domain.TaskStatusWaitingInput {
				answer, ok := waitForTaskInput(m, chat, task, projectName, projectsRoot, taskID, lang)
				if !ok {
					return
				}
				planningPrompt = answer
				continue
			}

			if res.Outcome == StepOutcomeTimeout {
				handleTaskStepTimeout(m, chat, task, projectName, projectsRoot, taskID, true, lang)
				return
			}

			if res.Outcome == StepOutcomeError {
				handleTaskStepError(m, chat, task, projectName, projectsRoot, taskID, res.Error, lang)
				return
			}

			break
		}

		planText := strings.TrimSpace(task.Snapshot().Output)
		if isLikelyErrorMessage(planText) {
			handleTaskStepError(m, chat, task, projectName, projectsRoot, taskID, errors.New(planText), lang)
			return
		}
		if planText == "" {
			planText = i18n.T(lang, "plan.empty_fallback")
		}
		task.Update(func(t *domain.TaskSession) {
			t.Plan = planText
			t.Status = domain.TaskStatusWaitingApproval
			t.ResetOutputLocked()
			t.RecentLogs = nil
		})

		syncLegacySession(task)

		sendPlanForApproval(m, chat, task, lang)
		return
	}

	view3 := task.Snapshot()
	currentPrompt := view3.InitialPrompt
	if view3.CurrentPrompt != "" {
		currentPrompt = view3.CurrentPrompt
	}

	for {
		var (
			cancelled   bool
			activeModel string
		)
		task.Update(func(t *domain.TaskSession) {
			if t.Status == domain.TaskStatusCancelled {
				cancelled = true
				return
			}
			activeModel = t.Model
			t.CurrentPrompt = currentPrompt
		})
		if cancelled {
			return
		}

		syncLegacySession(task)

		res := executeStepForTask(m, chat, task, workDir, currentPrompt, activeModel, lang)

		stepView := task.Snapshot()
		if stepView.Status == domain.TaskStatusCancelled || res.Outcome == StepOutcomeCancelled {
			checkAndStartQueuedTask(m, projectName, projectsRoot)
			return
		}
		st := stepView.Status

		if res.Outcome == StepOutcomeWaitingInput || st == domain.TaskStatusWaitingInput {
			answer, ok := waitForTaskInput(m, chat, task, projectName, projectsRoot, taskID, lang)
			if !ok {
				return
			}
			currentPrompt = answer
			continue
		}

		if res.Outcome == StepOutcomeTimeout {
			handleTaskStepTimeout(m, chat, task, projectName, projectsRoot, taskID, false, lang)
			return
		}

		if res.Outcome == StepOutcomeError {
			handleTaskStepError(m, chat, task, projectName, projectsRoot, taskID, res.Error, lang)
			return
		}

		// Раньше этот блок держал лок задачи от проверки очереди дополнений до конца
		// ветки с дополнениями и вызывал ClearPendingFollowups, который берёт тот же
		// мьютекс — задача с непустой очередью намертво вешала пайплайн. Снимок и
		// Update разводят захваты: наружу лок больше не торчит.
		var (
			completed     bool
			prURL         string
			finalReport   string
			initialPrompt string
			hasPlan       bool
			followups     []string
		)
		task.Update(func(t *domain.TaskSession) {
			if len(t.PendingFollowups) > 0 {
				followups = append([]string(nil), t.PendingFollowups...)
				return
			}
			completed = true
			prURL = t.LastPRURL
			finalReport = t.FullOutput.String()
			initialPrompt = t.InitialPrompt
			hasPlan = t.Plan != ""
			t.Status = domain.TaskStatusCompleted
			t.FinishedAt = time.Now()
		})

		if completed {
			syncLegacySession(task)

			metrics := domain.GlobalTokenTracker.FinishTask(prURL)
			task.Update(func(t *domain.TaskSession) {
				t.TokenMetrics = &metrics
			})
			domain.GlobalTaskManager.SaveTaskMetrics(taskID, &metrics)
			domain.GlobalTaskManager.SaveTask(task)
			statsSummary := metrics.FormatCompletionSummary(lang)

			var compBldr strings.Builder
			if prURL != "" {
				compBldr.WriteString(i18n.Tf(lang, "pipeline.completed_with_pr", taskID, html.EscapeString(projectName), html.EscapeString(prURL)))
			} else {
				compBldr.WriteString(i18n.Tf(lang, "pipeline.completed", taskID, html.EscapeString(projectName)))
			}

			if initialPrompt != "" {
				compBldr.WriteString(i18n.Tf(lang, "pipeline.completed_prompt",
					html.EscapeString(utils.TruncateString(initialPrompt, 200))))
			}

			if hasPlan {
				compBldr.WriteString(i18n.Tf(lang, "pipeline.completed_plan_link", taskID))
			}

			compBldr.WriteString("\n" + statsSummary)

			var compMenu *ports.Keyboard
			var actButtons []ports.Button
			if prURL != "" {
				actButtons = append(actButtons, ports.Button{Text: i18n.T(lang, "btn.open_pr"), URL: prURL})
			}
			if hasPlan {
				actButtons = append(actButtons, ports.Button{Text: i18n.T(lang, "btn.download_plan"), Action: "plan_doc", Payload: strconv.Itoa(taskID)})
			}
			if len(actButtons) > 0 {
				compMenu = &ports.Keyboard{Rows: [][]ports.Button{actButtons}}
			}

			compRef, _ := m.Send(context.Background(), chat, compBldr.String(), ports.RichWith(compMenu))
			if compRef.ID != "" {
				domain.GlobalTaskManager.RegisterMessageTask(compRef, taskID)
			}

			finalReport = strings.TrimSpace(finalReport)
			if finalReport != "" {
				reportRunes := []rune(finalReport)
				if len(reportRunes) > 1500 {
					summary := utils.ExtractPlanSummary(finalReport, 1200)
					summaryHTML := utils.MarkdownToTelegramHTML(summary)
					_, _ = m.Send(context.Background(), chat, i18n.Tf(lang, "pipeline.report_summary", taskID, summaryHTML, len(reportRunes)), ports.Rich())

					doc := ports.Document{
						FileName: fmt.Sprintf("report_task_%d.md", taskID),
						MIME:     "text/markdown",
						Caption:  i18n.Tf(lang, "pipeline.report_doc_caption", taskID, projectName),
						Content:  []byte(finalReport),
					}
					docRef, docErr := m.SendDocument(context.Background(), chat, doc)
					if docErr != nil {
						sendLongMarkdown(m, chat, finalReport)
					} else if docRef.ID != "" {
						domain.GlobalTaskManager.RegisterMessageTask(docRef, taskID)
					}
				} else {
					sendLongMarkdown(m, chat, finalReport)
				}
			}

			// Запускаем следующую задачу из очереди для этого проекта, если есть
			checkAndStartQueuedTask(m, projectName, projectsRoot)
			return
		}

		task.ClearPendingFollowups()

		var bldr strings.Builder
		bldr.WriteString(i18n.T(lang, "pipeline.followup_prompt_header"))
		for i, f := range followups {
			bldr.WriteString(fmt.Sprintf("%d. %s\n", i+1, f))
		}
		bldr.WriteString(i18n.T(lang, "pipeline.followup_prompt_footer"))

		currentPrompt = bldr.String()
		task.Update(func(t *domain.TaskSession) {
			t.CurrentPrompt = strings.Join(followups, "; ")
			t.StartedAt = time.Now()
			t.RecentLogs = nil
		})

		domain.GlobalTokenTracker.StartNextStep(activeModel)

		_, _ = m.Send(context.Background(), chat, i18n.Tf(lang, "pipeline.followups_taken", taskID, len(followups)), ports.Rich())
	}
}

// isLikelyErrorMessage определяет, является ли полученный текст сырой ошибкой CLI или API, а не планом задачи.
func isLikelyErrorMessage(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	isErrPrefix := strings.HasPrefix(lower, "error:") || strings.HasPrefix(lower, "fatal:") ||
		strings.HasPrefix(lower, "panic:") || strings.Contains(lower, "eligibility check failed") ||
		strings.Contains(lower, "operation not permitted")
	if isErrPrefix {
		// Если в тексте нет структуры плана (заголовков markdown '#' или этапов/шагов)
		if !strings.Contains(trimmed, "#") && !strings.Contains(lower, "план") && !strings.Contains(lower, "архитектур") {
			return true
		}
	}
	return false
}

// extractStepErrorMessage извлекает содержательный текст ошибки из результата agy, логов шага или вывода процесса.
func extractStepErrorMessage(task *domain.TaskSession, resultError string, waitErr error) string {
	if strings.TrimSpace(resultError) != "" {
		return strings.TrimSpace(resultError)
	}

	if task != nil {
		view4 := task.Snapshot()
		recentLogs := append([]string(nil), view4.RecentLogs...)
		fullOutput := view4.Output

		// 1. Поиск смысловой строки ошибки в RecentLogs с конца
		for i := len(recentLogs) - 1; i >= 0; i-- {
			line := strings.TrimSpace(recentLogs[i])
			lower := strings.ToLower(line)
			if strings.HasPrefix(lower, "error:") || strings.HasPrefix(lower, "fatal:") ||
				strings.Contains(lower, "eligibility check failed") || strings.Contains(lower, "operation not permitted") ||
				strings.Contains(lower, "invalid model selection") {
				return line
			}
		}

		// 2. Поиск в FullOutput построчно с конца
		lines := strings.Split(fullOutput, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimSpace(lines[i])
			lower := strings.ToLower(line)
			if strings.HasPrefix(lower, "error:") || strings.HasPrefix(lower, "fatal:") ||
				strings.Contains(lower, "eligibility check failed") || strings.Contains(lower, "operation not permitted") ||
				strings.Contains(lower, "invalid model selection") {
				return line
			}
		}
	}

	if waitErr != nil {
		return waitErr.Error()
	}
	return i18n.T(i18n.Active(), "err.step_unknown")
}

// isAgyPrintTimeoutLine проверяет, является ли строка системным терминальным сообщением CLI agy о таймауте print mode,
// исключая JSON-события стрима (где эта фраза может встретиться в просматриваемом коде, дифах или ответах).
func isAgyPrintTimeoutLine(rawLine string) bool {
	trimmed := strings.TrimSpace(rawLine)
	if trimmed == "" {
		return false
	}
	// JSON-строки стрима гарантированно не являются системным баннером CLI agy о таймауте
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		return false
	}
	// Сообщение CLI agy при превышении --print-timeout имеет строгий префикс:
	// "[agy] print timeout after" или "Print mode: print timeout after"
	return strings.HasPrefix(trimmed, "[agy] print timeout after") ||
		strings.HasPrefix(trimmed, "Print mode: print timeout after")
}

// evaluateStepCompletion determines the outcome and question state of a completed task step.
func evaluateStepCompletion(
	isPlanning bool,
	hasAskQuestionToolCall bool,
	pendingQuestionText string,
	pendingQuestionOptions []string,
	lastPR string,
	fullResp string,
) (outcome StepOutcome, isQuestion bool, questionText string, questionOptions []string) {
	// 1. В режиме составления плана (isPlanning):
	// Весь сгенерированный агентом текст является планом реализации.
	// Обычный текст со знаками '?' НИКОГДА не перехватывается как вопрос.
	// Исключение: только если агент явно вызвал инструмент ask_question.
	if isPlanning {
		if hasAskQuestionToolCall && pendingQuestionText != "" {
			return StepOutcomeWaitingInput, true, pendingQuestionText, pendingQuestionOptions
		}
		return StepOutcomeSuccess, false, "", nil
	}

	// 2. В режиме выполнения (Execution phase):
	// Если создан PR — задача успешно выполнена, вопросов нет!
	if lastPR != "" {
		return StepOutcomeSuccess, false, "", nil
	}

	// Если PR нет, проверяем, был ли задан вопрос (инструментом ask_question или в завершении ответа)
	if hasAskQuestionToolCall && pendingQuestionText != "" {
		return StepOutcomeWaitingInput, true, pendingQuestionText, pendingQuestionOptions
	} else if utils.IsFinalResponseAQuestion(fullResp) {
		return StepOutcomeWaitingInput, true, utils.ExtractQuestionFromResponse(fullResp), nil
	}

	return StepOutcomeSuccess, false, "", nil
}

func executeStepForTask(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession, workDir, prompt, modelName, lang string) StepResult {
	view := task.Snapshot()
	projectName := view.Project
	taskID := view.ID

	if view.Status == domain.TaskStatusCancelled {
		return StepResult{Outcome: StepOutcomeCancelled}
	}
	isPlanning := view.Status == domain.TaskStatusPlanning

	statusKey := "pipeline.step_running_status"
	if isPlanning {
		statusKey = "pipeline.step_planning_status"
	}
	statusMsgText := i18n.Tf(lang, statusKey, taskID, html.EscapeString(projectName), html.EscapeString(modelName))

	statusRef, _ := m.Send(context.Background(), chat, statusMsgText, ports.Rich())
	if statusRef.ID != "" {
		domain.GlobalTaskManager.RegisterMessageTask(statusRef, taskID)
	}

	task.Update(func(t *domain.TaskSession) {
		t.LastModelUsed = modelName
		t.LiveMsg = &statusRef
	})

	syncLegacySession(task)

	view5 := task.Snapshot()
	convID := view5.ConversationID
	taskAgent := view5.Agent
	if taskAgent == "" {
		taskAgent = "agy"
	}

	// Имя активного агента читаем один раз: проверка конфликта и выбор адаптера ниже
	// должны видеть одно и то же состояние, даже если пользователь переключает агента.
	currentAgentName := ActiveAgentName()

	if convID != "" && !strings.EqualFold(taskAgent, currentAgentName) {
		err := fmt.Errorf("pipeline: agent conflict: the task session belongs to %s while the current agent is %s", taskAgent, currentAgentName)
		_, _ = m.Send(context.Background(), chat, i18n.Tf(lang, "pipeline.agent_start_failed", taskID,
			i18n.Tf(lang, "err.agent_conflict", taskAgent, currentAgentName)), nil)
		task.Update(func(t *domain.TaskSession) {
			t.Status = domain.TaskStatusPaused
		})
		syncLegacySession(task)
		_ = sendAgentConflictDialogWithMessenger(m, chat, task)
		return StepResult{Outcome: StepOutcomeError, Error: err}
	}

	args := ports.ExecuteArgs{
		ConversationID: convID,
		ModelName:      modelName,
		Prompt:         prompt,
		WorkDir:        workDir,
	}

	// Передаём правила проекта AGENTS.md как системный промпт ТОЛЬКО для новой сессии (convID == "").
	// При возобновлении сессии (convID != "") контекст уже сохранён агентом,
	// и повторная отправка исключена для экономии токенов.
	if convID == "" {
		if rules := utils.LoadProjectAgentsRules(workDir, config.ProjectsRoot); rules != "" {
			args.SystemPrompt = rules
		}
	}

	stepTimeout := config.StepTimeout
	if stepTimeout <= 0 {
		stepTimeout = 30 * time.Minute
	}
	stepCtx, stepCancel := context.WithTimeout(context.Background(), stepTimeout+2*time.Minute)
	defer stepCancel()

	framework, frameworkErr := agentFrameworkFor(taskAgent)
	if frameworkErr != nil {
		_, _ = m.Send(context.Background(), chat, i18n.Tf(lang, "pipeline.agent_start_failed", taskID, ErrorText(frameworkErr, lang)), ports.Rich())
		task.Update(func(t *domain.TaskSession) {
			t.Status = domain.TaskStatusFailed
		})
		syncLegacySession(task)
		return StepResult{Outcome: StepOutcomeError, Error: frameworkErr}
	}
	agentProcess, err := framework.ExecuteTask(stepCtx, args)
	if err != nil {
		_, _ = m.Send(context.Background(), chat, i18n.Tf(lang, "pipeline.agent_start_failed", taskID, ErrorText(err, lang)), nil)
		task.Update(func(t *domain.TaskSession) {
			t.Status = domain.TaskStatusFailed
		})
		syncLegacySession(task)
		return StepResult{Outcome: StepOutcomeError, Error: err}
	}
	defer func() { _ = agentProcess.Close() }()

	// Привязываем процесс к задаче: с этого момента /cancel, /pause и /resume
	// останавливают его сами, одинаково для CLI и API. Если /cancel успел прийти
	// раньше привязки, гасим процесс здесь, иначе он доработал бы до конца.
	if !task.AttachProcess(agentProcess, stepCancel) {
		_ = agentProcess.Kill()
		return StepResult{Outcome: StepOutcomeCancelled}
	}
	syncLegacySession(task)

	scanner := bufio.NewScanner(agentProcess.Stdout())
	scanBuf := make([]byte, 64*1024)
	scanner.Buffer(scanBuf, 10*1024*1024)
	done := make(chan struct{})

	stopLiveUpdate := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-stopLiveUpdate:
				return
			case <-ticker.C:
				liveView := task.Snapshot()
				if liveView.Status != domain.TaskStatusRunning && liveView.Status != domain.TaskStatusWaitingInput && liveView.Status != domain.TaskStatusPlanning {
					return
				}
				var lastLine string
				if len(liveView.RecentLogs) > 0 {
					lastLine = liveView.RecentLogs[len(liveView.RecentLogs)-1]
				}
				followupsCount := len(liveView.PendingFollowups)
				taskStatus := liveView.Status
				dur := liveView.Duration

				tokenSnippet := domain.GlobalTokenTracker.GetLiveStatusSnippet(lang)

				if statusRef.ID != "" {
					queueInfo := ""
					if followupsCount > 0 {
						queueInfo = i18n.Tf(lang, "pipeline.live_queue", followupsCount)
					}
					statusPrefix := i18n.T(lang, "pipeline.live_prefix_running")
					if taskStatus == domain.TaskStatusPlanning {
						statusPrefix = i18n.T(lang, "pipeline.live_prefix_planning")
					}
					var bldr strings.Builder
					bldr.WriteString(i18n.Tf(lang, "pipeline.live_header",
						statusPrefix,
						taskID,
						html.EscapeString(projectName),
						html.EscapeString(modelName),
						domain.FormatDuration(dur, lang),
						queueInfo,
					))
					if lastLine != "" {
						bldr.WriteString(i18n.Tf(lang, "pipeline.live_action", html.EscapeString(utils.TruncateString(lastLine, 80))))
					} else {
						bldr.WriteString(i18n.T(lang, "pipeline.live_action_init"))
					}
					if tokenSnippet != "" {
						bldr.WriteString(tokenSnippet + "\n\n")
					}
					bldr.WriteString(i18n.Tf(lang, "pipeline.live_footer", taskID, taskID, taskID))

					_ = m.Edit(context.Background(), statusRef, bldr.String(), ports.Rich())
				}
			}
		}
	}()

	var stepTimedOut bool
	var hasResult bool
	var resultStatus string
	var resultError string
	var hasAskQuestionToolCall bool
	var pendingQuestionText string
	var pendingQuestionOptions []string

	go func() {
		for scanner.Scan() {
			rawLine := scanner.Text()
			cleanLine := utils.AnsiRegex.ReplaceAllString(rawLine, "")
			cleanLine = strings.TrimSpace(cleanLine)
			if cleanLine == "" {
				continue
			}

			evt, err := domain.ParseStreamEvent(cleanLine)
			if err == nil && evt != nil {
				convID := evt.ConversationID
				if convID == "" && evt.StepUpdate != nil {
					convID = evt.StepUpdate.ConversationID
				}
				if convID == "" && evt.Result != nil {
					convID = evt.Result.ConversationID
				}
				if convID != "" {
					domain.GlobalTaskManager.SetTaskConversationID(taskID, convID)
					domain.GlobalTaskManager.SetTaskAgent(taskID, taskAgent)
					domain.GlobalTokenTracker.SetConversationID(convID)
				}

				if evt.StepUpdate != nil {
					u := evt.StepUpdate
					if u.Usage != nil {
						domain.GlobalTokenTracker.RecordStepUsage(u.StepIndex, *u.Usage)
					}
					if u.StepType == "tool" && u.State == "ACTIVE" {
						domain.GlobalTokenTracker.RecordToolCall()
						desc := domain.FormatToolAction(u.ToolName, u.ToolInfo)
						task.AppendLog(desc)
					} else if u.StepType == "agent_response" && u.TextDelta != "" {
						task.Update(func(t *domain.TaskSession) {
							t.AppendOutputLocked(u.TextDelta)
							if matches := prUrlRegexp.FindStringSubmatch(u.TextDelta); len(matches) > 1 {
								t.LastPRURL = matches[1]
							}
						})
					}

					// Фиксация вызова инструмента ask_question (без преждевременного прерывания процесса)
					if u.ToolName == "ask_question" || (u.ToolInfo != nil && u.ToolInfo.Name == "ask_question") {
						hasAskQuestionToolCall = true
						if u.ToolInfo != nil && u.ToolInfo.Parameters != nil {
							qText := utils.FormatAskQuestionParams(u.ToolInfo.Parameters, lang)
							qOpts := utils.ExtractAskQuestionOptions(u.ToolInfo.Parameters)
							if qText != "" {
								pendingQuestionText = qText
							}
							if len(qOpts) > 0 {
								pendingQuestionOptions = qOpts
							}
						}
						if pendingQuestionText == "" && u.TextDelta != "" {
							pendingQuestionText = u.TextDelta
						}
					}
				}

				if evt.Result != nil {
					res := evt.Result
					hasResult = true
					resultStatus = res.Status
					if res.Error != "" {
						resultError = res.Error
					}
					if res.Usage != nil {
						domain.GlobalTokenTracker.RecordResultUsage(*res.Usage, res.DurationSeconds, res.NumTurns)
					}
					if res.Response != "" {
						task.Update(func(t *domain.TaskSession) {
							if t.FullOutput.Len() == 0 {
								t.AppendOutputLocked(res.Response)
							}
							if matches := prUrlRegexp.FindStringSubmatch(res.Response); len(matches) > 1 {
								t.LastPRURL = matches[1]
							}
						})
					}
				}
				continue
			}

			// Проверка на системный таймаут agy ТОЛЬКО для не-JSON строк терминального вывода
			if isAgyPrintTimeoutLine(cleanLine) {
				stepTimedOut = true
			}

			// Fallback для текстового вывода или не-JSON строк
			task.AppendLog(cleanLine)
			task.Update(func(t *domain.TaskSession) {
				t.AppendOutputLocked(cleanLine + "\n")
				if matches := prUrlRegexp.FindStringSubmatch(cleanLine); len(matches) > 1 {
					t.LastPRURL = matches[1]
				}
				if m := modelRegexp.FindStringSubmatch(cleanLine); len(m) > 1 {
					t.LastModelUsed = strings.TrimSpace(m[1])
				}
				if tokens := tokensRegexp.FindStringSubmatch(cleanLine); len(tokens) > 1 {
					t.LastTokensUsed = strings.TrimSpace(tokens[1])
				}
			})
		}
		if scanErr := scanner.Err(); scanErr != nil && !cliproc.IsPTYEOF(scanErr) {
			log.Printf("warning: output scanner error for task #%d: %v", taskID, scanErr)
		}
		close(done)
	}()

	<-done
	close(stopLiveUpdate)
	waitErr := agentProcess.Wait()
	task.DetachProcess()

	view6 := task.Snapshot()
	isCancelled := (view6.Status == domain.TaskStatusCancelled)
	lastPR := view6.LastPRURL
	fullResp := strings.TrimSpace(view6.Output)
	syncLegacySession(task)

	if isCancelled {
		return StepResult{Outcome: StepOutcomeCancelled, PRURL: lastPR}
	}
	// Если PR уже успешно создан в git и получен URL, шаг считается успешно завершённым
	if lastPR != "" {
		return StepResult{Outcome: StepOutcomeSuccess, PRURL: lastPR, HasResult: hasResult, ResultStatus: resultStatus}
	}
	if stepTimedOut || stepCtx.Err() == context.DeadlineExceeded {
		return StepResult{Outcome: StepOutcomeTimeout, Error: waitErr, PRURL: lastPR}
	}

	hasError := (hasResult && (strings.EqualFold(resultStatus, "ERROR") || strings.TrimSpace(resultError) != "")) || waitErr != nil
	if hasError {
		errText := extractStepErrorMessage(task, resultError, waitErr)
		return StepResult{
			Outcome:      StepOutcomeError,
			Error:        errors.New(errText),
			HasResult:    hasResult,
			ResultStatus: resultStatus,
			PRURL:        lastPR,
		}
	}

	// Оцениваем результат шага и детекцию вопросов
	outcome, isQuestion, qText, qOpts := evaluateStepCompletion(
		isPlanning,
		hasAskQuestionToolCall,
		pendingQuestionText,
		pendingQuestionOptions,
		lastPR,
		fullResp,
	)

	if isQuestion {
		task.Update(func(t *domain.TaskSession) {
			t.Status = domain.TaskStatusWaitingInput
			t.LastQuestion = qText
			t.QuestionOptions = qOpts
			t.QuestionAskedAt = time.Now()
		})
		syncLegacySession(task)

		menu := buildQuestionMarkup(task, lang)
		formattedQ := utils.MarkdownToTelegramHTML(qText)
		headerKey := "pipeline.question_header"
		if isPlanning {
			headerKey = "pipeline.question_header_planning"
		}
		msgText := i18n.Tf(lang, headerKey, taskID, html.EscapeString(projectName), formattedQ)
		qRef, _ := m.Send(context.Background(), chat, msgText, ports.RichWith(menu))
		if qRef.ID != "" {
			domain.GlobalTaskManager.RegisterMessageTask(qRef, taskID)
		}

		return StepResult{
			Outcome:      StepOutcomeWaitingInput,
			PRURL:        lastPR,
			HasResult:    hasResult,
			ResultStatus: resultStatus,
		}
	}

	return StepResult{
		Outcome:      outcome,
		HasResult:    hasResult,
		ResultStatus: resultStatus,
		PRURL:        lastPR,
	}
}

func handleTaskStepTimeout(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession, projectName, projectsRoot string, taskID int, isPlanning bool, lang string) {
	var convID string
	var tAgent string
	task.Update(func(t *domain.TaskSession) {
		t.Status = domain.TaskStatusPaused
		convID = t.ConversationID
		tAgent = t.Agent
		if tAgent == "" {
			tAgent = "agy"
		}
	})

	syncLegacySession(task)

	stepTimeout := config.StepTimeout
	if stepTimeout <= 0 {
		stepTimeout = 30 * time.Minute
	}

	phaseName := i18n.T(lang, "pipeline.phase_running")
	if isPlanning {
		phaseName = i18n.T(lang, "pipeline.phase_planning")
	}

	task.AppendLog(i18n.Tf(lang, "pipeline.timeout_log", phaseName, stepTimeout, convID))

	resumeMenu := buildResumeMarkup(taskID, lang)
	timeoutMsg := i18n.Tf(lang, "pipeline.timeout_message",
		taskID, html.EscapeString(projectName), phaseName, stepTimeout,
		html.EscapeString(tAgent), html.EscapeString(convID), i18n.T(lang, "btn.resume"), taskID,
	)

	if m != nil && chat != "" {
		defer func() { _ = recover() }()
		tRef, _ := m.Send(context.Background(), chat, timeoutMsg, ports.RichWith(resumeMenu))
		if tRef.ID != "" {
			domain.GlobalTaskManager.RegisterMessageTask(tRef, taskID)
		}
	}

	checkAndStartQueuedTask(m, projectName, projectsRoot)
}

func handleTaskStepError(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession, projectName, projectsRoot string, taskID int, err error, lang string) {
	var convID string
	task.Update(func(t *domain.TaskSession) {
		t.Status = domain.TaskStatusFailed
		t.FinishedAt = time.Now()
		convID = t.ConversationID
	})

	syncLegacySession(task)

	errText := i18n.T(lang, "pipeline.error_unknown")
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		errText = strings.TrimSpace(ErrorText(err, lang))
	}
	if len([]rune(errText)) > 1200 {
		errText = string([]rune(errText)[:1200]) + "..."
	}

	task.AppendLog(i18n.Tf(lang, "pipeline.error_log", errText))

	retryMenu := buildResumeMarkup(taskID, lang)
	msg := i18n.Tf(lang, "pipeline.error_message",
		taskID, html.EscapeString(projectName), html.EscapeString(errText),
		html.EscapeString(convID), taskID, taskID,
	)

	if m != nil && chat != "" {
		defer func() { _ = recover() }()
		eRef, _ := m.Send(context.Background(), chat, msg, ports.RichWith(retryMenu))
		if eRef.ID != "" {
			domain.GlobalTaskManager.RegisterMessageTask(eRef, taskID)
		}
	}

	checkAndStartQueuedTask(m, projectName, projectsRoot)
}

func waitForTaskInput(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession, projectName, projectsRoot string, taskID int, lang string) (string, bool) {
	timeout := config.QuestionTimeout

	select {
	case answer := <-task.AnswerChannel():
		task.Update(func(t *domain.TaskSession) {
			if t.RequiresPlan && !t.PlanApproved {
				t.Status = domain.TaskStatusPlanning
			} else {
				t.Status = domain.TaskStatusRunning
			}
			t.CurrentPrompt = answer
			t.LastQuestion = ""
			t.QuestionOptions = nil
			t.StartedAt = time.Now()
		})
		syncLegacySession(task)

		_, _ = m.Send(context.Background(), chat, i18n.Tf(lang, "pipeline.answer_received", taskID), ports.Rich())
		return answer, true

	case <-task.PauseChannel():
		var isCancelled bool
		task.Update(func(t *domain.TaskSession) {
			isCancelled = (t.Status == domain.TaskStatusCancelled)
			if !isCancelled {
				t.Status = domain.TaskStatusPaused
			}
		})
		syncLegacySession(task)

		if !isCancelled {
			checkAndStartQueuedTask(m, projectName, projectsRoot)
		}
		return "", false

	case <-time.After(timeout):
		task.Update(func(t *domain.TaskSession) {
			if t.Status == domain.TaskStatusWaitingInput {
				t.Status = domain.TaskStatusPaused
			}
		})
		syncLegacySession(task)

		resumeMenu := buildResumeMarkup(taskID, lang)
		timeoutMsg := i18n.Tf(lang, "pipeline.answer_timeout_message",
			taskID, html.EscapeString(projectName), timeout, i18n.T(lang, "btn.resume"), taskID,
		)
		tRef, _ := m.Send(context.Background(), chat, timeoutMsg, ports.RichWith(resumeMenu))
		if tRef.ID != "" {
			domain.GlobalTaskManager.RegisterMessageTask(tRef, taskID)
		}

		checkAndStartQueuedTask(m, projectName, projectsRoot)
		return "", false
	}
}

var (
	prUrlRegexp  = regexp.MustCompile(`PR_URL:\s*(https?://[^\s]+)`)
	tokensRegexp = regexp.MustCompile(`(?i)tokens?:\s*([0-9,kKmM\s/]+)`)
	modelRegexp  = regexp.MustCompile(`(?i)model:\s*([a-zA-Z0-9.\-_]+)`)
)

type StepOutcome int

const (
	StepOutcomeSuccess StepOutcome = iota
	StepOutcomeWaitingInput
	StepOutcomeTimeout
	StepOutcomeCancelled
	StepOutcomeError
)

type StepResult struct {
	Outcome      StepOutcome
	Error        error
	HasResult    bool
	ResultStatus string
	PRURL        string
}
