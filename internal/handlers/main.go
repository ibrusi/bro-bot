package handlers

import (
	"bro-bot/internal/adapters/agy"
	"bro-bot/internal/adapters/claude"
	"bro-bot/internal/adapters/transcript"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/models"
	"bro-bot/internal/ports"
	"bro-bot/internal/storage"
	"bro-bot/internal/system"
	"bro-bot/internal/utils"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	Agent           ports.AgentFramework
	ActiveAgentName = "agy"
)

type AgyQuotaResponse struct {
	Status   string `json:"status"`
	Response string `json:"response"`
	Command  struct {
		Name string `json:"name"`
		Data struct {
			Description string          `json:"description"`
			Groups      []AgyQuotaGroup `json:"groups"`
		} `json:"data"`
	} `json:"command"`
}

type AgyQuotaGroup struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Buckets     []AgyQuotaBucket `json:"buckets"`
}

type AgyQuotaBucket struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Window            string   `json:"window"`
	RemainingFraction *float64 `json:"remaining_fraction"`
	ResetTime         string   `json:"reset_time"`
}

type AgyCreditsResponse struct {
	Command struct {
		Name string `json:"name"`
		Data struct {
			RemainingCredits float64 `json:"remaining_credits"`
			UpgradeURI       string  `json:"upgrade_uri"`
		} `json:"data"`
	} `json:"command"`
}

var (
	prUrlRegexp  = regexp.MustCompile(`PR_URL:\s*(https?://[^\s]+)`)
	tokensRegexp = regexp.MustCompile(`(?i)tokens?:\s*([0-9,kKmM\s/]+)`)
	modelRegexp  = regexp.MustCompile(`(?i)model:\s*([a-zA-Z0-9.\-_]+)`)
)

func Start(t ports.Transport) {
	adminIDStr := os.Getenv("TELEGRAM_ADMIN_ID")
	adminIDNum, err := strconv.ParseInt(adminIDStr, 10, 64)
	if err != nil || adminIDNum == 0 {
		log.Fatal("ОБЯЗАТЕЛЬНЫЙ параметр TELEGRAM_ADMIN_ID не задан или некорректен")
	}
	config.AdminID = ports.ChatID(strconv.FormatInt(adminIDNum, 10))

	envProjectsRoot := os.Getenv("PROJECTS_ROOT")
	if envProjectsRoot == "" {
		log.Fatal("ОБЯЗАТЕЛЬНЫЙ параметр PROJECTS_ROOT не задан")
	}
	config.ProjectsRoot = envProjectsRoot

	envTimeout := os.Getenv("QUESTION_TIMEOUT")
	if envTimeout == "" {
		log.Fatal("ОБЯЗАТЕЛЬНЫЙ параметр QUESTION_TIMEOUT не задан")
	}
	if d, err := time.ParseDuration(envTimeout); err == nil && d > 0 {
		config.QuestionTimeout = d
	} else if sec, err := strconv.Atoi(envTimeout); err == nil && sec > 0 {
		config.QuestionTimeout = time.Duration(sec) * time.Second
	} else {
		log.Fatal("Некорректный формат QUESTION_TIMEOUT")
	}

	config.StepTimeout = 30 * time.Minute
	if envStepTimeout := os.Getenv("STEP_TIMEOUT"); envStepTimeout != "" {
		if d, err := time.ParseDuration(envStepTimeout); err == nil && d > 0 {
			config.StepTimeout = d
		} else if sec, err := strconv.Atoi(envStepTimeout); err == nil && sec > 0 {
			config.StepTimeout = time.Duration(sec) * time.Second
		} else {
			log.Printf("Предупреждение: некорректный формат STEP_TIMEOUT, используется значение по умолчанию %v", config.StepTimeout)
		}
	}

	if os.Getenv("BOT_SERVICE_NAME") == "" {
		log.Fatal("ОБЯЗАТЕЛЬНЫЙ параметр BOT_SERVICE_NAME не задан")
	}

	botDir := os.Getenv("BOT_DIR")
	if botDir == "" {
		botDir = "/home/deploy/bro-bot"
	}
	config.BotDir = botDir

	dbPath := os.Getenv("SQLITE_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(botDir, "data", "bot.db")
	}
	config.DBPath = dbPath

	scriptsDir := os.Getenv("SCRIPTS_DIR")
	if scriptsDir == "" {
		scriptsDir = filepath.Join(botDir, "scripts")
	}
	config.ScriptsDir = scriptsDir

	sqliteStorage, err := storage.NewSQLiteStorage(dbPath)
	if err != nil {
		log.Fatalf("Не удалось инициализировать SQLite базу данных: %v", err)
	}
	domain.GlobalTaskManager.InitWithStorage(sqliteStorage)

	models.GlobalModelRegistry = models.NewModelRegistry(10 * time.Minute)

	initDefaultProject(config.ProjectsRoot)
	initDefaultModel()

	// Восстанавливаем сохраненные настройки из базы данных
	ctx := context.Background()
	if savedProj, err := sqliteStorage.GetSetting(ctx, "current_project"); err == nil && savedProj != "" {
		projPath := filepath.Join(config.ProjectsRoot, savedProj)
		if fi, err := os.Stat(projPath); err == nil && fi.IsDir() {
			config.ProjectState.Lock()
			config.ProjectState.CurrentProject = savedProj
			config.ProjectState.Unlock()
			log.Printf("Восстановлен активный проект из SQLite: %s", savedProj)
		}
	}
	if savedModel, err := sqliteStorage.GetSetting(ctx, "current_model"); err == nil && savedModel != "" {
		config.ProjectState.Lock()
		config.ProjectState.CurrentModel = savedModel
		config.ProjectState.Unlock()
		log.Printf("Восстановлена активная модель из SQLite: %s", savedModel)
	}
	if savedPlanMode, err := sqliteStorage.GetSetting(ctx, "plan_mode"); err == nil && savedPlanMode != "" {
		if pm, err := strconv.ParseBool(savedPlanMode); err == nil {
			config.ProjectState.Lock()
			config.ProjectState.PlanMode = pm
			config.ProjectState.Unlock()
			log.Printf("Восстановлен PlanMode из SQLite: %v", pm)
		}
	}
	if savedAgent, err := sqliteStorage.GetSetting(ctx, "current_agent"); err == nil && savedAgent != "" {
		if _, err := SwitchActiveAgent(savedAgent); err == nil {
			log.Printf("Восстановлен активный агент из SQLite: %s", savedAgent)
		}
	} else {
		config.ProjectState.SetCurrentAgent(ActiveAgentName)
	}

	if err := t.SetCommands(context.Background(), getDefaultCommands()); err != nil {
		log.Printf("Предупреждение: не удалось зарегистрировать команды: %v", err)
	}

	t.Use(func(next ports.Handler) ports.Handler {
		return func(s ports.Session) error {
			if s.SenderID() != string(config.AdminID) {
				return nil
			}
			return next(s)
		}
	})

	t.OnCommand("start", func(s ports.Session) error {
		args := s.Args()
		if len(args) > 0 {
			payload := strings.TrimSpace(args[0])
			if strings.HasPrefix(payload, "plan_") || strings.HasPrefix(payload, "planfile_") {
				rawID := strings.TrimPrefix(payload, "planfile_")
				rawID = strings.TrimPrefix(rawID, "plan_")
				if id, err := strconv.Atoi(rawID); err == nil {
					target := domain.GlobalTaskManager.GetTask(id)
					if target != nil {
						return sendTaskPlanDocument(s, target)
					}
					return s.Send(fmt.Sprintf("❌ Задача #%d не найдена. Список задач: /tasks", id), ports.Rich())
				}
			}
		}

		config.ProjectState.RLock()
		curProj := config.ProjectState.CurrentProject
		curMod := config.ProjectState.CurrentModel
		config.ProjectState.RUnlock()

		msg := fmt.Sprintf(
			"🤖 <b>Агент-воркер готов к работе!</b>\n\n"+
				"📁 Выбранный проект: <code>%s</code>\n"+
				"🧠 Активная модель: <code>%s</code>\n\n"+
				"<b>Задачи:</b>\n"+
				"• /tasks — список задач и быстрое переключение\n"+
				"• /task &lt;id&gt; [текст] — переключить фокус на задачу или дополнить её\n"+
				"• /plan [проект] &lt;текст&gt; — составить план и утвердить перед реализацией\n"+
				"• /planmode [on|off] — включить обязательный план для всех задач\n"+
				"• /approve [id] — утвердить план задачи и начать реализацию\n"+
				"• /add [id] &lt;текст&gt; — отправить дополнение конкретной задаче\n"+
				"• /new [проект] [агент] &lt;текст&gt; — создать новую задачу (с выбором проекта и агента)\n" +
				"• /resume [id] [ответ] — возобновить задачу или передать ответ\n"+
				"• /retry [id] — перезапустить задачу с чистой сессией agy\n"+
				"• /pause [id] — приостановить задачу\n"+
				"• /status [id] — подробный статус, логи и очередь правок\n"+
				"• /cancel [id] — остановить задачу\n\n"+
				"<b>Система и мониторинг:</b>\n"+
				"• /top (или /ps) — потребление CPU и памяти\n"+
				"• /context [id] — распределение окна контекста\n"+
				"• /tokens (или /stats) — статистика токенов, скорости и кэша\n"+
				"• /usage (или /limits) — статистика токенов и лимиты\n"+
				"• /models — список доступных моделей\n"+
				"• /model [имя] — переключить активную модель\n"+
				"• /projects — список доступных проектов\n"+
				"• /use &lt;имя&gt; — переключить активный проект\n"+
				"• /clone &lt;url&gt; [имя] — клонировать репозиторий\n"+
				"• /restart, /rebuild — управление процессом бота\n\n"+
				"💡 <i>Отправьте задачу сообщением в чат. Для предварительного плана используйте /plan &lt;задача&gt;. Дополнения можно отправлять через /add [id] &lt;текст&gt; или ответом на сообщения бота.</i>",
			html.EscapeString(curProj),
			html.EscapeString(curMod),
		)
		return s.Send(msg, ports.Rich())
	})

	t.OnCommand("tasks", func(s ports.Session) error {
		msg, menu := domain.FormatTasksList(domain.GlobalTaskManager)
		return s.Send(msg, ports.RichWith(menu))
	})

	t.OnCallback("task_sel", func(s ports.Session) error {
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

		details := domain.FormatTaskDetails(task, true)
		markup := domain.BuildTaskDetailsMarkup(task)
		conflictNote := ""
		if HasAgentConflict(task, ActiveAgentName) {
			task.Lock()
			tAgent := task.Agent
			if tAgent == "" {
				tAgent = "agy"
			}
			task.Unlock()
			conflictNote = fmt.Sprintf("\n\n⚠️ <i>Внимание: задача использует сессию агента <b>%s</b>, а активен <b>%s</b>.</i>", html.EscapeString(tAgent), html.EscapeString(ActiveAgentName))
		}
		return s.Send(fmt.Sprintf("🎯 <b>Фокус переключен на задачу #%d!</b>\n\n%s%s", id, details, conflictNote), ports.RichWith(markup))
	})

	t.OnCommand("status", func(s ports.Session) error {
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

		activeTask := domain.GlobalTaskManager.GetActiveTask()
		isActiveFocus := (activeTask != nil && activeTask.ID == target.ID)
		msg := domain.FormatTaskDetails(target, isActiveFocus)

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
				if ot.ID != target.ID {
					ot.Lock()
					otherParts = append(otherParts, fmt.Sprintf("<b>#%d</b> (%s <code>%s</code>)", ot.ID, ot.Status.Emoji(), html.EscapeString(ot.Project)))
					ot.Unlock()
				}
			}
			if len(otherParts) > 0 {
				msg += fmt.Sprintf("\n\n📌 <b>Другие задачи:</b> %s\n<i>(Переключить: /task &lt;id&gt; или /tasks)</i>", strings.Join(otherParts, " | "))
			}
		}

		statusMarkup := domain.BuildTaskDetailsMarkup(target)
		return s.Send(msg, ports.RichWith(statusMarkup))
	})

	t.OnCommand("tokens", func(s ports.Session) error {
		msg := domain.GlobalTokenTracker.GetTokensCommandMessage()
		return s.Send(msg, ports.Rich())
	})

	t.OnCommand("stats", func(s ports.Session) error {
		msg := domain.GlobalTokenTracker.GetTokensCommandMessage()
		return s.Send(msg, ports.Rich())
	})

	t.OnCommand("context", func(s ports.Session) error {
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

		config.ProjectState.RLock()
		curProj := config.ProjectState.CurrentProject
		curMod := config.ProjectState.CurrentModel
		config.ProjectState.RUnlock()

		msg := domain.GlobalTokenTracker.GetContextCommandMessage(target, curProj, curMod)
		return s.Send(msg, ports.Rich())
	})

	t.OnCommand("models", func(s ports.Session) error {
		args := s.Args()
		forceRefresh := len(args) > 0 && (args[0] == "refresh" || args[0] == "update")
		if forceRefresh {
			if _, err := models.GlobalModelRegistry.RefreshModels(true); err != nil {
				return s.Send(fmt.Sprintf("⚠️ Ошибка синхронизации с %s: %v\nПоказан кэшированный список.", ActiveAgentName, err), nil)
			}
		}

		config.ProjectState.RLock()
		curModel := config.ProjectState.CurrentModel
		config.ProjectState.RUnlock()

		msg := models.GlobalModelRegistry.FormatModelsMessage(curModel)
		return s.Send(msg, ports.Rich())
	})

	t.OnCommand("model", func(s ports.Session) error {
		args := s.Args()
		if len(args) == 0 {
			config.ProjectState.RLock()
			cur := config.ProjectState.CurrentModel
			config.ProjectState.RUnlock()
			return s.Send(fmt.Sprintf("Текущая модель: <code>%s</code>\nИспользование: <code>/model &lt;имя&gt;</code> (например, <code>/model sonnet</code>)", html.EscapeString(cur)), ports.Rich())
		}

		target := strings.TrimSpace(args[0])
		resolved, ok := models.GlobalModelRegistry.ResolveModel(target)
		if !ok {
			return s.Send(fmt.Sprintf("❌ Неизвестная модель: <code>%s</code>. Список: /models", html.EscapeString(target)), ports.Rich())
		}

		config.ProjectState.Lock()
		config.ProjectState.CurrentModel = resolved
		config.ProjectState.Unlock()

		if st := domain.GlobalTaskManager.Storage(); st != nil {
			_ = st.SetSetting(context.Background(), "current_model", resolved)
		}

		return s.Send(fmt.Sprintf("✅ Модель переключена на: <code>%s</code>", html.EscapeString(resolved)), ports.Rich())
	})

	t.OnCommand("agent", func(s ports.Session) error {
		args := s.Args()
		if len(args) == 0 {
			return s.Send(fmt.Sprintf("🤖 Текущий CLI агент: <code>%s</code>\nДоступны: <b>agy</b>, <b>claude</b>", html.EscapeString(ActiveAgentName)), ports.Rich())
		}

		name := strings.ToLower(strings.TrimSpace(args[0]))
		msg, err := SwitchActiveAgent(name)
		if err != nil {
			return s.Send(fmt.Sprintf("❌ %s", err.Error()), ports.Rich())
		}
		return s.Send(msg, ports.Rich())
	})

	handleUsage := func(s ports.Session) error {
		m := s.Messenger()
		chat := s.Chat()
		loadingAgent := ActiveAgentName
		if loadingAgent == "" {
			loadingAgent = "агента"
		}
		statusRef, _ := m.Send(context.Background(), chat, fmt.Sprintf("⏳ <i>Запрашиваю актуальные лимиты и квоты из %s...</i>", html.EscapeString(loadingAgent)), ports.Rich())

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		var (
			quotaResp   AgyQuotaResponse
			creditsResp AgyCreditsResponse
			quotaRaw    string
			quotaErr    error
			creditsErr  error
			wg          sync.WaitGroup
		)

		wg.Add(2)
		go func() {
			defer wg.Done()
			out, err := Agent.GetQuota(ctx)
			cleanOut := utils.AnsiRegex.ReplaceAllString(string(out), "")
			quotaRaw = strings.TrimSpace(cleanOut)
			if err != nil {
				textOut, textErr := Agent.GetQuotaText(ctx)
				if textErr == nil && len(textOut) > 0 {
					quotaRaw = strings.TrimSpace(utils.AnsiRegex.ReplaceAllString(string(textOut), ""))
				}
				quotaErr = err
				return
			}
			if err := json.Unmarshal([]byte(cleanOut), &quotaResp); err != nil {
				quotaErr = err
			}
		}()

		go func() {
			defer wg.Done()
			out, err := Agent.GetCredits(ctx)
			cleanOut := utils.AnsiRegex.ReplaceAllString(string(out), "")
			if err != nil {
				creditsErr = err
				return
			}
			_ = json.Unmarshal([]byte(cleanOut), &creditsResp)
		}()

		wg.Wait()

		config.Session.Lock()
		lastModel := config.Session.LastModelUsed
		config.Session.Unlock()

		config.ProjectState.RLock()
		activeModel := config.ProjectState.CurrentModel
		config.ProjectState.RUnlock()

		if lastModel == "" {
			lastModel = activeModel
		}

		lastTokens := domain.GlobalTokenTracker.FormatShortLastTask()
		if lastTokens == "" {
			config.Session.Lock()
			lastTokens = config.Session.LastTokensUsed
			config.Session.Unlock()
			if lastTokens == "" {
				lastTokens = "нет данных (запустите хотя бы одну задачу)"
			}
		}

		var bldr strings.Builder
		headerTitle := "📊 <b>Лимиты и квоты аккаунта (Google Antigravity)</b>\n\n"
		if ActiveAgentName == "claude" {
			headerTitle = "📊 <b>Лимиты и квоты аккаунта (Claude Code)</b>\n\n"
		}
		bldr.WriteString(headerTitle)

		if quotaErr == nil && len(quotaResp.Command.Data.Groups) > 0 {
			for _, g := range quotaResp.Command.Data.Groups {
				bldr.WriteString(fmt.Sprintf("🔹 <b>%s</b>\n", html.EscapeString(g.Name)))
				if g.Description != "" {
					bldr.WriteString(fmt.Sprintf("<i>%s</i>\n", html.EscapeString(g.Description)))
				}
				for _, b := range g.Buckets {
					bucketLabel := formatBucketName(b.Name, b.Window)
					if b.RemainingFraction != nil {
						frac := *b.RemainingFraction
						pct := frac * 100
						bar := renderProgressBar(frac, 10)
						emoji := quotaStatusEmoji(frac)
						bldr.WriteString(fmt.Sprintf("• <b>%s:</b> %.1f%% %s\n", html.EscapeString(bucketLabel), pct, emoji))
						bldr.WriteString(fmt.Sprintf("  <code>[%s]</code> %.1f%%\n", bar, pct))
					} else {
						bldr.WriteString(fmt.Sprintf("• <b>%s:</b> <i>доступно</i>\n", html.EscapeString(bucketLabel)))
					}
					if b.ResetTime != "" {
						resetInfo := formatResetDuration(b.ResetTime)
						bldr.WriteString(fmt.Sprintf("  ⏳ <i>Сброс: %s</i>\n", html.EscapeString(resetInfo)))
					}
				}
				bldr.WriteString("\n")
			}
		} else if quotaRaw != "" {
			agentTitle := "Ответ агента"
			if ActiveAgentName == "claude" {
				agentTitle = "Ответ Claude"
			} else if ActiveAgentName == "agy" {
				agentTitle = "Ответ agy"
			}
			bldr.WriteString(fmt.Sprintf("<b>%s:</b>\n<pre>%s</pre>\n\n", agentTitle, html.EscapeString(quotaRaw)))
		} else if quotaErr != nil {
			agentTitle := ActiveAgentName
			if agentTitle == "" {
				agentTitle = "агента"
			}
			bldr.WriteString(fmt.Sprintf("⚠️ <i>Не удалось получить актуальные лимиты из %s: %s</i>\n\n", html.EscapeString(agentTitle), html.EscapeString(quotaErr.Error())))
		}

		if creditsErr == nil && creditsResp.Command.Name == "credits" {
			bldr.WriteString(fmt.Sprintf("💳 <b>Дополнительные кредиты:</b> <code>%.0f</code>\n\n", creditsResp.Command.Data.RemainingCredits))
		}

		bldr.WriteString("⚙️ <b>Сессия и модель:</b>\n")
		bldr.WriteString(fmt.Sprintf("• Выбранная модель: <code>%s</code>\n", html.EscapeString(activeModel)))
		bldr.WriteString(fmt.Sprintf("• Модель в сессии: <code>%s</code>\n", html.EscapeString(lastModel)))
		bldr.WriteString(fmt.Sprintf("• Использовано токенов: <code>%s</code>\n\n", html.EscapeString(lastTokens)))

		bldr.WriteString("💡 <i>Лимиты 5-часового окна и недели сглаживают общую нагрузку и обновляются автоматически.</i>")

		resultMsg := bldr.String()
		if statusRef.ID != "" {
			if editErr := m.Edit(context.Background(), statusRef, resultMsg, ports.Rich()); editErr == nil {
				return nil
			}
		}
		return s.Send(resultMsg, ports.Rich())
	}
	t.OnCommand("usage", handleUsage)
	t.OnCommand("limits", handleUsage)

	t.OnCommand("projects", func(s ports.Session) error {
		entries, err := os.ReadDir(config.ProjectsRoot)
		if err != nil {
			return s.Send(fmt.Sprintf("❌ Ошибка чтения директории: %v", err), nil)
		}

		config.ProjectState.RLock()
		cur := config.ProjectState.CurrentProject
		config.ProjectState.RUnlock()

		var bldr strings.Builder
		bldr.WriteString("📁 <b>Доступные проекты:</b>\n\n")

		found := false
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			gitPath := filepath.Join(config.ProjectsRoot, e.Name(), ".git")
			if _, err := os.Stat(gitPath); err == nil {
				found = true
				if e.Name() == cur {
					bldr.WriteString(fmt.Sprintf("👉 <b>%s</b> <i>(активен)</i>\n", html.EscapeString(e.Name())))
				} else {
					bldr.WriteString(fmt.Sprintf("• <code>%s</code> (переключить: <code>/use %s</code>)\n", html.EscapeString(e.Name()), html.EscapeString(e.Name())))
				}
			}
		}

		if !found {
			return s.Send("В каталоге проектов пока нет склонированных репозиториев.\n\n💡 Клонировать: <code>/clone &lt;url&gt; [имя]</code>", ports.Rich())
		}

		bldr.WriteString("\n💡 Клонировать новый: <code>/clone &lt;url&gt; [имя]</code>")
		return s.Send(bldr.String(), ports.Rich())
	})

	t.OnCommand("use", func(s ports.Session) error {
		args := s.Args()
		if len(args) == 0 {
			return s.Send("Укажите имя проекта. Пример: <code>/use my-repo</code>", ports.Rich())
		}

		target := strings.TrimSpace(args[0])
		targetPath := filepath.Join(config.ProjectsRoot, target)

		if fi, err := os.Stat(targetPath); err != nil || !fi.IsDir() {
			return s.Send(fmt.Sprintf("❌ Проект <code>%s</code> не найден.", html.EscapeString(target)), ports.Rich())
		}

		config.ProjectState.Lock()
		config.ProjectState.CurrentProject = target
		config.ProjectState.Unlock()

		if st := domain.GlobalTaskManager.Storage(); st != nil {
			_ = st.SetSetting(context.Background(), "current_project", target)
		}

		return s.Send(fmt.Sprintf("✅ Проект переключен на: <code>%s</code>", html.EscapeString(target)), ports.Rich())
	})

	t.OnCommand("clone", func(s ports.Session) error {
		return handleCloneCommand(s)
	})

	t.OnCommand("task", func(s ports.Session) error {
		args := s.Args()
		if len(args) == 0 {
			active := domain.GlobalTaskManager.GetActiveTask()
			if active == nil {
				return s.Send("💤 Нет активных задач. Создать: <code>/new &lt;текст&gt;</code>", ports.Rich())
			}
			details := domain.FormatTaskDetails(active, true)
			markup := domain.BuildTaskDetailsMarkup(active)
			conflictNote := ""
			if HasAgentConflict(active, ActiveAgentName) {
				active.Lock()
				tAgent := active.Agent
				if tAgent == "" {
					tAgent = "agy"
				}
				active.Unlock()
				conflictNote = fmt.Sprintf("\n\n⚠️ <i>Внимание: задача использует сессию агента <b>%s</b>, а активен <b>%s</b>.</i>", html.EscapeString(tAgent), html.EscapeString(ActiveAgentName))
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

		details := domain.FormatTaskDetails(task, true)
		markup := domain.BuildTaskDetailsMarkup(task)
		conflictNote := ""
		if HasAgentConflict(task, ActiveAgentName) {
			task.Lock()
			tAgent := task.Agent
			if tAgent == "" {
				tAgent = "agy"
			}
			task.Unlock()
			conflictNote = fmt.Sprintf("\n\n⚠️ <i>Внимание: задача использует сессию агента <b>%s</b>, а активен <b>%s</b>.</i>", html.EscapeString(tAgent), html.EscapeString(ActiveAgentName))
		}
		return s.Send(fmt.Sprintf("🎯 <b>Фокус переключен на задачу #%d!</b>\n\n%s%s", id, details, conflictNote), ports.RichWith(markup))
	})

	t.OnCommand("add", func(s ports.Session) error {
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
		return handleAddFollowupToTask(s, active.ID, followupText)
	})

	t.OnCommand("new", func(s ports.Session) error {
		args := s.Args()
		if len(args) == 0 {
			return s.Send("Использование: <code>/new &lt;описание задачи&gt;</code>\n(или <code>/new [проект] [агент] &lt;описание&gt;</code>)", ports.Rich())
		}
		text := strings.TrimSpace(strings.Join(args, " "))
		return handleCreateNewTask(s, text)
	})

	t.OnCommand("plan", func(s ports.Session) error {
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
	})

	t.OnCommand("planmode", func(s ports.Session) error {
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
	})

	t.OnCommand("planfile", func(s ports.Session) error {
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
	})

	t.OnCommand("history", func(s ports.Session) error {
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
	})

	t.OnCommand("approve", func(s ports.Session) error {
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
			targetID = active.ID
		}
		return handleApprovePlan(s.Messenger(), s.Chat(), targetID)
	})

	t.OnCommand("confirm", func(s ports.Session) error {
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
			targetID = active.ID
		}
		return handleApprovePlan(s.Messenger(), s.Chat(), targetID)
	})

	t.OnCallback("plan_approve", func(s ports.Session) error {
		idStr := s.Callback().Payload
		id, err := strconv.Atoi(idStr)
		if err != nil {
			return s.Respond("Некорректный номер задачи")
		}
		_ = s.Respond(fmt.Sprintf("План #%d утверждён", id))
		return handleApprovePlan(s.Messenger(), s.Chat(), id)
	})

	t.OnCallback("plan_cancel", func(s ports.Session) error {
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
		checkAndStartQueuedTask(s.Messenger(), task.Project, config.ProjectsRoot)
		return s.Send(fmt.Sprintf("🛑 <b>Задача #%d (<code>%s</code>) остановлена.</b>", id, html.EscapeString(task.Project)), ports.Rich())
	})

	t.OnCallback("plan_mode_toggle", func(s ports.Session) error {
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
	})

	t.OnCommand("cancel", func(s ports.Session) error {
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
			targetID = active.ID
		}

		task, err := domain.GlobalTaskManager.CancelTask(targetID)
		if err != nil {
			return s.Send(fmt.Sprintf("❌ %s", err.Error()), ports.Rich())
		}

		syncLegacySession(domain.GlobalTaskManager.GetActiveTask())
		domain.GlobalTokenTracker.CancelTask()

		checkAndStartQueuedTask(s.Messenger(), task.Project, config.ProjectsRoot)

		return s.Send(fmt.Sprintf("🛑 Задача <b>#%d</b> (<code>%s</code>) остановлена.", task.ID, html.EscapeString(task.Project)), ports.Rich())
	})

	t.OnCallback("plan_appr_var", func(s ports.Session) error {
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
		task.Lock()
		planText := task.Plan
		task.Unlock()
		variants := utils.ExtractPlanVariantOptions(planText)
		chosenVar := ""
		if varIdx >= 0 && varIdx < len(variants) {
			chosenVar = variants[varIdx]
		}
		_ = s.Respond(fmt.Sprintf("Утверждён вариант: %s", truncateString(chosenVar, 20)))
		return handleApprovePlanWithVariant(s.Messenger(), s.Chat(), id, chosenVar)
	})

	t.OnCallback("plan_doc", func(s ports.Session) error {
		idStr := s.Callback().Payload
		id, err := strconv.Atoi(idStr)
		if err != nil {
			return s.Respond("Некорректный номер задачи")
		}
		task := domain.GlobalTaskManager.GetTask(id)
		if task == nil {
			return s.Respond("Задача не найдена")
		}
		task.Lock()
		planText := strings.TrimSpace(task.Plan)
		task.Unlock()

		if planText == "" {
			return s.Respond("У задачи нет сформированного плана")
		}

		_ = s.Respond("Отправляю файл плана...")
		return sendTaskPlanDocument(s, task)
	})

	t.OnCallback("q_choice", func(s ports.Session) error {
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

		task.Lock()
		status := task.Status
		var chosenText string
		if optIdx >= 0 && optIdx < len(task.QuestionOptions) {
			chosenText = task.QuestionOptions[optIdx]
		}
		cmdIsNil := (task.Cmd == nil || task.Cmd.Process == nil)
		task.Unlock()

		if chosenText == "" {
			return s.Respond("Вариант не найден")
		}

		_ = s.Respond(fmt.Sprintf("Выбрано: %s", truncateString(chosenText, 25)))

		if cb := s.Callback(); cb != nil && cb.MessageText != "" {
			_ = s.Edit(fmt.Sprintf("%s\n\n✅ <b>Выбран вариант %d:</b> <i>«%s»</i>",
				cb.MessageText, optIdx+1, html.EscapeString(chosenText)), ports.Rich())
		}

		if status == domain.TaskStatusWaitingInput {
			if !cmdIsNil {
				task.DeliverAnswer(chosenText)
				return s.Send(fmt.Sprintf("💬 <b>Выбран вариант %d для задачи #%d:</b>\n<i>«%s»</i>", optIdx+1, taskID, html.EscapeString(chosenText)), ports.Rich())
			}
			if HasAgentConflict(task, ActiveAgentName) {
				task.Lock()
				task.CurrentPrompt = chosenText
				task.Unlock()
				domain.GlobalTaskManager.SaveTask(task)
				return sendAgentConflictDialog(s, task)
			}
			resumedTask, err := domain.GlobalTaskManager.ResumeTask(taskID, chosenText)
			if err != nil {
				return s.Send(fmt.Sprintf("❌ Ошибка возобновления задачи #%d: %s", taskID, err.Error()), ports.Rich())
			}
			syncLegacySession(resumedTask)
			resumedTask.Lock()
			resumedStatus := resumedTask.Status
			projectName := resumedTask.Project
			resumedTask.Unlock()

			if resumedStatus == domain.TaskStatusQueued {
				return s.Send(fmt.Sprintf("⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code> с ответом:\n<i>«%s»</i>",
					taskID, html.EscapeString(projectName), html.EscapeString(chosenText)), ports.Rich())
			}
			workDir := filepath.Join(config.ProjectsRoot, projectName)
			go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir)
			return s.Send(fmt.Sprintf("▶️ <b>Задача #%d возобновлена в <code>%s</code> с ответом:</b>\n<i>«%s»</i>",
				taskID, html.EscapeString(projectName), html.EscapeString(chosenText)), ports.Rich())
		} else if status == domain.TaskStatusPaused {
			if HasAgentConflict(task, ActiveAgentName) {
				task.Lock()
				task.CurrentPrompt = chosenText
				task.Unlock()
				domain.GlobalTaskManager.SaveTask(task)
				return sendAgentConflictDialog(s, task)
			}
			resumedTask, err := domain.GlobalTaskManager.ResumeTask(taskID, chosenText)
			if err != nil {
				return s.Send(fmt.Sprintf("❌ Ошибка возобновления задачи #%d: %s", taskID, err.Error()), ports.Rich())
			}
			syncLegacySession(resumedTask)
			resumedTask.Lock()
			resumedStatus := resumedTask.Status
			projectName := resumedTask.Project
			resumedTask.Unlock()

			if resumedStatus == domain.TaskStatusQueued {
				return s.Send(fmt.Sprintf("⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code> с ответом:\n<i>«%s»</i>",
					taskID, html.EscapeString(projectName), html.EscapeString(chosenText)), ports.Rich())
			}
			workDir := filepath.Join(config.ProjectsRoot, projectName)
			go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir)
			return s.Send(fmt.Sprintf("▶️ <b>Задача #%d возобновлена в <code>%s</code> с ответом:</b>\n<i>«%s»</i>",
				taskID, html.EscapeString(projectName), html.EscapeString(chosenText)), ports.Rich())
		}

		return s.Send(fmt.Sprintf("ℹ️ Задача #%d сейчас не ожидает ответа (текущий статус: %s).", taskID, status.RussianTitle()), nil)
	})

	t.OnCallback("q_pause", func(s ports.Session) error {
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

		task.Lock()
		projectName := task.Project
		task.Unlock()

		checkAndStartQueuedTask(s.Messenger(), projectName, config.ProjectsRoot)

		resumeMenu := buildResumeMarkup(taskID)
		msg := fmt.Sprintf("⏸ <b>Задача #%d (<code>%s</code>) приостановлена.</b>\nОчередь проекта освобождена.\nНажмите кнопку ниже или используйте <code>/resume %d &lt;ответ&gt;</code>, чтобы продолжить.", taskID, html.EscapeString(projectName), taskID)
		return s.Send(msg, ports.RichWith(resumeMenu))
	})

	t.OnCallback("q_resume", func(s ports.Session) error {
		idStr := s.Callback().Payload
		taskID, err := strconv.Atoi(idStr)
		if err != nil {
			return s.Respond("Некорректный номер задачи")
		}

		task := domain.GlobalTaskManager.GetTask(taskID)
		if task == nil {
			return s.Respond("Задача не найдена")
		}

		task.Lock()
		status := task.Status
		lastQ := task.LastQuestion
		opts := append([]string(nil), task.QuestionOptions...)
		proj := task.Project
		task.Unlock()

		_ = s.Respond("")

		if status != domain.TaskStatusPaused && status != domain.TaskStatusWaitingInput {
			return s.Send(fmt.Sprintf("ℹ️ Задача #%d не находится на паузе (текущий статус: %s).", taskID, status.RussianTitle()), nil)
		}

		if HasAgentConflict(task, ActiveAgentName) {
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

		resumedTask.Lock()
		resStatus := resumedTask.Status
		resumedTask.Unlock()

		if resStatus == domain.TaskStatusQueued {
			return s.Send(fmt.Sprintf("⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code>.\nОна запустится автоматически, как только проект освободится.", taskID, html.EscapeString(proj)), ports.Rich())
		}

		task.Lock()
		tAgent := task.Agent
		if tAgent == "" {
			tAgent = "agy"
		}
		task.Unlock()

		workDir := filepath.Join(config.ProjectsRoot, proj)
		go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir)
		return s.Send(fmt.Sprintf("▶️ <b>Задача #%d (<code>%s</code>) возобновлена с сохранённой сессии %s!</b>", taskID, html.EscapeString(proj), html.EscapeString(tAgent)), ports.Rich())
	})

	t.OnCallback("task_agent_restart", func(s ports.Session) error {
		idStr := s.Callback().Payload
		taskID, err := strconv.Atoi(idStr)
		if err != nil {
			return s.Respond("Некорректный номер задачи")
		}

		task := domain.GlobalTaskManager.GetTask(taskID)
		if task == nil {
			return s.Respond("Задача не найдена")
		}

		_ = s.Respond(fmt.Sprintf("Перезапуск с агентом %s...", ActiveAgentName))

		domain.GlobalTaskManager.ClearTaskConversationID(taskID)
		domain.GlobalTaskManager.SetTaskAgent(taskID, ActiveAgentName)

		task.Lock()
		proj := task.Project
		task.ConversationID = ""
		task.Agent = ActiveAgentName
		if task.RequiresPlan && !task.PlanApproved {
			task.Status = domain.TaskStatusPlanning
		} else {
			task.Status = domain.TaskStatusRunning
		}
		if task.CurrentPrompt == "" {
			task.CurrentPrompt = task.InitialPrompt
		}
		task.LastPRURL = ""
		task.LastQuestion = ""
		task.QuestionOptions = nil
		task.StartedAt = time.Now()
		task.RecentLogs = nil
		task.FullOutput.Reset()
		task.Unlock()

		syncLegacySession(task)
		domain.GlobalTaskManager.SaveTask(task)

		if cb := s.Callback(); cb != nil && cb.MessageText != "" {
			_ = s.Edit(fmt.Sprintf("%s\n\n🔄 <b>Выбрано: Начать заново с агентом %s.</b>", cb.MessageText, html.EscapeString(ActiveAgentName)), ports.Rich())
		}

		workDir := filepath.Join(config.ProjectsRoot, proj)
		go runAgentTaskPipeline(s.Messenger(), s.Chat(), task, workDir)
		return s.Send(fmt.Sprintf("🔄 <b>Задача #%d перезапущена с агентом %s</b> (новая сессия в <code>%s</code>).", taskID, html.EscapeString(ActiveAgentName), html.EscapeString(proj)), ports.Rich())
	})

	t.OnCallback("task_agent_switch", func(s ports.Session) error {
		idStr := s.Callback().Payload
		taskID, err := strconv.Atoi(idStr)
		if err != nil {
			return s.Respond("Некорректный номер задачи")
		}

		task := domain.GlobalTaskManager.GetTask(taskID)
		if task == nil {
			return s.Respond("Задача не найдена")
		}

		task.Lock()
		targetAgent := task.Agent
		if targetAgent == "" {
			targetAgent = "agy"
		}
		prompt := task.CurrentPrompt
		proj := task.Project
		task.Unlock()

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

		resumedTask.Lock()
		resStatus := resumedTask.Status
		resumedTask.Unlock()

		if resStatus == domain.TaskStatusQueued {
			return s.Send(fmt.Sprintf("%s\n\n⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code>.", switchMsg, taskID, html.EscapeString(proj)), ports.Rich())
		}

		workDir := filepath.Join(config.ProjectsRoot, proj)
		go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir)
		return s.Send(fmt.Sprintf("%s\n\n▶️ <b>Задача #%d (<code>%s</code>) возобновлена с агентом %s!</b>", switchMsg, taskID, html.EscapeString(proj), html.EscapeString(targetAgent)), ports.Rich())
	})

	t.OnCommand("pause", func(s ports.Session) error {
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
			targetID = active.ID
		}

		task := domain.GlobalTaskManager.GetTask(targetID)
		if task == nil {
			return s.Send(fmt.Sprintf("❌ Задача #%d не найдена.", targetID), ports.Rich())
		}

		if !task.PauseTask() {
			task.Lock()
			st := task.Status
			task.Unlock()
			return s.Send(fmt.Sprintf("ℹ️ Задачу #%d нельзя приостановить (текущий статус: %s). Пауза доступна при ожидании ответа.", targetID, st.RussianTitle()), ports.Rich())
		}

		syncLegacySession(task)
		task.Lock()
		projectName := task.Project
		task.Unlock()

		checkAndStartQueuedTask(s.Messenger(), projectName, config.ProjectsRoot)

		resumeMenu := buildResumeMarkup(targetID)
		msg := fmt.Sprintf("⏸ <b>Задача #%d (<code>%s</code>) приостановлена.</b>\nОчередь проекта освобождена.\nЧтобы возобновить, используйте <code>/resume %d &lt;ответ&gt;</code> или кнопку ниже.", targetID, html.EscapeString(projectName), targetID)
		return s.Send(msg, ports.RichWith(resumeMenu))
	})

	t.OnCommand("resume", func(s ports.Session) error {
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
				t.Lock()
				st := t.Status
				t.Unlock()
				if st == domain.TaskStatusPaused || st == domain.TaskStatusWaitingInput || st == domain.TaskStatusFailed || st == domain.TaskStatusCancelled {
					targetID = t.ID
					break
				}
			}
		}

		if targetID == 0 {
			active := domain.GlobalTaskManager.GetActiveTask()
			if active != nil {
				targetID = active.ID
			}
		}

		if targetID == 0 {
			return s.Send("❌ Не указана задача для возобновления. Использование: <code>/resume &lt;id&gt; [ответ]</code>", ports.Rich())
		}

		task := domain.GlobalTaskManager.GetTask(targetID)
		if task == nil {
			return s.Send(fmt.Sprintf("❌ Задача #%d не найдена.", targetID), ports.Rich())
		}

		if HasAgentConflict(task, ActiveAgentName) {
			if answer != "" {
				task.Lock()
				task.CurrentPrompt = answer
				task.Unlock()
				domain.GlobalTaskManager.SaveTask(task)
			}
			return sendAgentConflictDialog(s, task)
		}

		task.Lock()
		status := task.Status
		task.Unlock()

		if status == domain.TaskStatusWaitingInput {
			if answer != "" {
				task.Lock()
				cmdIsNil := (task.Cmd == nil || task.Cmd.Process == nil)
				proj := task.Project
				task.Unlock()

				if !cmdIsNil {
					task.DeliverAnswer(answer)
					return s.Send(fmt.Sprintf("💬 <b>Ответ передан задаче #%d</b> (<code>%s</code>)...", targetID, html.EscapeString(proj)), ports.Rich())
				}

				resumedTask, err := domain.GlobalTaskManager.ResumeTask(targetID, answer)
				if err != nil {
					return s.Send(fmt.Sprintf("❌ Ошибка возобновления задачи #%d: %s", targetID, err.Error()), ports.Rich())
				}
				syncLegacySession(resumedTask)
				resumedTask.Lock()
				resStatus := resumedTask.Status
				resumedTask.Unlock()

				if resStatus == domain.TaskStatusQueued {
					return s.Send(fmt.Sprintf("⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code> с ответом:\n<i>«%s»</i>", targetID, html.EscapeString(proj), html.EscapeString(answer)), ports.Rich())
				}

				workDir := filepath.Join(config.ProjectsRoot, proj)
				go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir)
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

		resumedTask.Lock()
		resStatus := resumedTask.Status
		proj := resumedTask.Project
		resumedTask.Unlock()

		if resStatus == domain.TaskStatusQueued {
			return s.Send(fmt.Sprintf("⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code>.\nОна запустится автоматически, как только текущая задача завершится.", targetID, html.EscapeString(proj)), ports.Rich())
		}

		workDir := filepath.Join(config.ProjectsRoot, proj)
		go runAgentTaskPipeline(s.Messenger(), s.Chat(), resumedTask, workDir)
		return s.Send(fmt.Sprintf("▶️ <b>Задача #%d возобновлена в <code>%s</code>!</b>", targetID, html.EscapeString(proj)), ports.Rich())
	})

	t.OnCommand("retry", func(s ports.Session) error {
		args := s.Args()
		var targetID int
		if len(args) > 0 {
			idStr := strings.TrimPrefix(args[0], "#")
			targetID, _ = strconv.Atoi(idStr)
		}
		if targetID == 0 {
			active := domain.GlobalTaskManager.GetActiveTask()
			if active != nil {
				targetID = active.ID
			}
		}
		if targetID == 0 {
			all := domain.GlobalTaskManager.ListTasks()
			for i := len(all) - 1; i >= 0; i-- {
				t := all[i]
				t.Lock()
				st := t.Status
				t.Unlock()
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

		task.Lock()
		if task.Status == domain.TaskStatusRunning || task.Status == domain.TaskStatusPlanning {
			task.Unlock()
			return s.Send(fmt.Sprintf("ℹ️ Задача #%d сейчас выполняется. Сначала остановите её: <code>/cancel %d</code>", targetID, targetID), ports.Rich())
		}
		proj := task.Project
		task.ConversationID = ""
		task.Agent = ActiveAgentName
		task.Status = domain.TaskStatusRunning
		if task.RequiresPlan && !task.PlanApproved {
			task.Status = domain.TaskStatusPlanning
		}
		task.CurrentPrompt = task.InitialPrompt
		task.LastPRURL = ""
		task.LastQuestion = ""
		task.QuestionOptions = nil
		task.StartedAt = time.Now()
		task.RecentLogs = nil
		task.FullOutput.Reset()
		task.Unlock()

		domain.GlobalTaskManager.ClearTaskConversationID(targetID)
		domain.GlobalTaskManager.SetTaskAgent(targetID, ActiveAgentName)
		syncLegacySession(task)

		workDir := filepath.Join(config.ProjectsRoot, proj)
		go runAgentTaskPipeline(s.Messenger(), s.Chat(), task, workDir)
		return s.Send(fmt.Sprintf("🔄 <b>Задача #%d перезапущена с чистого листа</b> (новая сессия %s в <code>%s</code>).", targetID, html.EscapeString(ActiveAgentName), html.EscapeString(proj)), ports.Rich())
	})

	t.OnCommand("restart", func(s ports.Session) error {
		return system.HandleRestart(t, s)
	})

	t.OnCommand("rebuild", func(s ports.Session) error {
		return system.HandleRebuild(t, s)
	})

	t.OnCommand("build", func(s ports.Session) error {
		return system.HandleRebuild(t, s)
	})

	handleResources := func(s ports.Session) error {
		report := system.CollectResourceReport(true)
		msg := system.FormatResourcesMessage(report)
		return s.Send(msg, ports.Rich())
	}

	t.OnCommand("top", handleResources)
	t.OnCommand("ps", handleResources)
	t.OnCommand("resources", handleResources)
	t.OnCommand("res", handleResources)

	t.OnCommand("script", func(s ports.Session) error {
		args := s.Args()
		if len(args) == 0 {
			return s.Send("Пожалуйста, укажите название скрипта: /script <name>", nil)
		}
		scriptName := args[0]
		if config.ScriptsDir == "" {
			return s.Send("Директория скриптов не настроена (SCRIPTS_DIR)", nil)
		}
		scriptPath := filepath.Join(config.ScriptsDir, scriptName)
		if !strings.HasPrefix(filepath.Clean(scriptPath), filepath.Clean(config.ScriptsDir)) {
			return s.Send("Недопустимое имя скрипта", nil)
		}
		if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
			return s.Send(fmt.Sprintf("Скрипт %s не найден в %s", scriptName, config.ScriptsDir), nil)
		}

		cmd := exec.Command(scriptPath)
		out, err := cmd.CombinedOutput()
		msg := fmt.Sprintf("Результат выполнения %s:\n\n%s", scriptName, string(out))
		if err != nil {
			msg += fmt.Sprintf("\nОшибка: %v", err)
		}
		return s.Send(msg, nil)
	})

	t.OnText(func(s ports.Session) error {
		userText := strings.TrimSpace(s.Text())
		if strings.HasPrefix(userText, "/planfile_") || strings.HasPrefix(userText, "/plan_") {
			rawID := strings.TrimPrefix(userText, "/planfile_")
			rawID = strings.TrimPrefix(rawID, "/plan_")
			if atIdx := strings.Index(rawID, "@"); atIdx != -1 {
				rawID = rawID[:atIdx]
			}
			if id, err := strconv.Atoi(strings.TrimSpace(rawID)); err == nil {
				target := domain.GlobalTaskManager.GetTask(id)
				if target != nil {
					return sendTaskPlanDocument(s, target)
				}
				return s.Send(fmt.Sprintf("❌ Задача #%d не найдена. Список задач: /tasks", id), ports.Rich())
			}
		}

		if strings.HasPrefix(userText, "/") {
			return nil
		}

		// 1. Проверяем, является ли сообщение ответом (Reply) на статус/вопрос конкретной задачи
		if msg := s.Message(); msg != nil && msg.ReplyTo != nil {
			if task := domain.GlobalTaskManager.GetTaskByMessageID(msg.ReplyTo.ID); task != nil {
				return handleAddFollowupToTask(s, task.ID, userText)
			}
		}

		// 2. Проверяем активную задачу в фокусе
		active := domain.GlobalTaskManager.GetActiveTask()
		if active != nil {
			active.Lock()
			st := active.Status
			active.Unlock()
			if active.IsActive() || st == domain.TaskStatusPaused {
				return handleAddFollowupToTask(s, active.ID, userText)
			}
		}

		// 3. Нет активных задач — запускаем новую задачу
		return handleCreateNewTask(s, userText)
	})

	go system.CheckAndNotifyRestart(t, config.AdminID)

	log.Println("Мультипроектный агент-бот запущен...")
	if err := t.Start(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func initDefaultProject(root string) {
	defaultProject := os.Getenv("DEFAULT_PROJECT")
	if defaultProject != "" {
		targetDir := filepath.Join(root, defaultProject)
		if info, err := os.Stat(targetDir); err == nil && info.IsDir() {
			config.ProjectState.Lock()
			config.ProjectState.CurrentProject = defaultProject
			config.ProjectState.Unlock()
			log.Printf("Инициализирован проект по умолчанию: %s", defaultProject)
			return
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		log.Printf("Предупреждение: не удалось прочитать директорию проектов %s: %v", root, err)
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			config.ProjectState.Lock()
			config.ProjectState.CurrentProject = e.Name()
			config.ProjectState.Unlock()
			log.Printf("Инициализирован первый найденный проект: %s", e.Name())
			return
		}
	}
}

func initDefaultModel() {
	defaultModel := os.Getenv("DEFAULT_MODEL")
	if defaultModel == "" {
		log.Fatal("ОБЯЗАТЕЛЬНЫЙ параметр DEFAULT_MODEL не задан")
	}
	if models.GlobalModelRegistry != nil {
		if resolved, ok := models.GlobalModelRegistry.ResolveModel(defaultModel); ok {
			defaultModel = resolved
		}
	}
	config.ProjectState.Lock()
	config.ProjectState.CurrentModel = defaultModel
	config.ProjectState.Unlock()
	log.Printf("Инициализирована модель по умолчанию: %s", defaultModel)
}

func runAgentPipeline(m ports.Messenger, chat ports.ChatID, workDir, projectName, initialPrompt string) {
	config.ProjectState.RLock()
	modelName := config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent(projectName, modelName, ActiveAgentName, initialPrompt, chat, false)
	task.Lock()
	task.Status = domain.TaskStatusRunning
	task.StartedAt = time.Now()
	task.Unlock()

	syncLegacySession(task)
	runAgentTaskPipeline(m, chat, task, workDir)
}

func runAgentTaskPipeline(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession, workDir string) {
	projectName := task.Project
	taskID := task.ID

	task.Lock()
	trackModel := task.Model
	trackPrompt := task.CurrentPrompt
	if trackPrompt == "" {
		trackPrompt = task.InitialPrompt
	}
	taskAgent := task.Agent
	if taskAgent == "" {
		taskAgent = ActiveAgentName
	}
	task.Unlock()
	domain.GlobalTokenTracker.StartTaskIfNotActiveWithAgent(projectName, trackModel, trackPrompt, taskAgent)

	// ЭТАП 1: Планирование (если требуется и ещё не утверждён)
	task.Lock()
	needsPlanning := task.RequiresPlan && !task.PlanApproved
	task.Unlock()

	if needsPlanning {
		task.Lock()
		if task.Status == domain.TaskStatusCancelled {
			task.Unlock()
			return
		}
		task.Status = domain.TaskStatusPlanning
		existingPlan := task.Plan
		curPrompt := task.CurrentPrompt
		pendingFollowups := append([]string(nil), task.PendingFollowups...)
		task.Unlock()

		syncLegacySession(task)

		pendingSection := ""
		if len(pendingFollowups) > 0 {
			var pbldr strings.Builder
			pbldr.WriteString("\n\nДОПОЛНИТЕЛЬНЫЕ ТРЕБОВАНИЯ И ПРАВКИ ИЗ ОЧЕРЕДИ:\n")
			for i, pf := range pendingFollowups {
				pbldr.WriteString(fmt.Sprintf("%d. %s\n", i+1, pf))
			}
			pbldr.WriteString("Обязательно включи эти требования в план реализации.")
			pendingSection = pbldr.String()
		}

		var planningPrompt string
		if existingPlan == "" {
			if task.ConversationID != "" && curPrompt != "" && curPrompt != task.InitialPrompt {
				planningPrompt = curPrompt
			} else {
				planningPrompt = fmt.Sprintf(
					"Задача пользователя: %s%s\n\n"+
						"ВНИМАНИЕ: Сейчас выполняется ЭТАП ПЛАНИРОВАНИЯ.\n"+
						"НЕ создавай git-ветку, НЕ модифицируй файлы проекта, НЕ делай git commit, НЕ делай git push и НЕ открывай PR.\n"+
						"Твоя цель сейчас:\n"+
						"1. Тщательно исследуй кодовую базу и архитектуру проекта.\n"+
						"2. Сформируй чёткий, пошаговый и структурированный план реализации задачи.\n"+
						"3. Опиши:\n"+
						"   - Какие файлы и компоненты будут созданы или изменены.\n"+
						"   - Ключевые архитектурные решения и интерфейсы.\n"+
						"   - План тестирования и проверки работоспособности.\n"+
						"   - Возможные риски, краевые случаи и пути их решения.\n"+
						"4. Выведи итоговый план в понятном и структурированном виде для пользователя.",
					task.InitialPrompt, pendingSection,
				)
			}
		} else {
			feedback := curPrompt
			if feedback == "" {
				feedback = task.InitialPrompt
			}
			planningPrompt = fmt.Sprintf(
				"Задача пользователя: %s\n\n"+
					"ПРЕДЫДУЩИЙ ПЛАН РЕАЛИЗАЦИИ:\n%s\n\n"+
					"ЗАМЕЧАНИЯ И ДОПОЛНЕНИЯ ПОЛЬЗОВАТЕЛЯ К ПЛАНУ:\n%s%s\n\n"+
					"ВНИМАНИЕ: Это этап планирования. НЕ вноси изменения в файлы проекта, НЕ делай commit и НЕ создавай PR.\n"+
					"Обнови и скорректируй план реализации с учётом всех замечаний пользователя и выведи обновлённый план.",
				task.InitialPrompt, existingPlan, feedback, pendingSection,
			)
		}

		for {
			task.Lock()
			if task.Status == domain.TaskStatusCancelled {
				task.Unlock()
				checkAndStartQueuedTask(m, projectName, config.ProjectsRoot)
				return
			}
			task.Status = domain.TaskStatusPlanning
			activeModel := task.Model
			task.Unlock()

			syncLegacySession(task)

			res := executeStepForTask(m, chat, task, workDir, planningPrompt, activeModel)

			task.Lock()
			if task.Status == domain.TaskStatusCancelled || res.Outcome == StepOutcomeCancelled {
				task.Unlock()
				checkAndStartQueuedTask(m, projectName, config.ProjectsRoot)
				return
			}
			st := task.Status
			task.Unlock()

			if res.Outcome == StepOutcomeWaitingInput || st == domain.TaskStatusWaitingInput {
				answer, ok := waitForTaskInput(m, chat, task, projectName, taskID)
				if !ok {
					return
				}
				planningPrompt = answer
				continue
			}

			if res.Outcome == StepOutcomeTimeout {
				handleTaskStepTimeout(m, chat, task, projectName, taskID, true)
				return
			}

			if res.Outcome == StepOutcomeError {
				handleTaskStepError(m, chat, task, projectName, taskID, res.Error)
				return
			}

			break
		}

		planText := strings.TrimSpace(task.FullOutput.String())
		if isLikelyErrorMessage(planText) {
			handleTaskStepError(m, chat, task, projectName, taskID, errors.New(planText))
			return
		}
		if planText == "" {
			planText = "Агент не сформировал подробный план. Вы можете дополнить задачу замечаниями или утвердить её."
		}
		task.Lock()
		task.Plan = planText
		task.Status = domain.TaskStatusWaitingApproval
		task.FullOutput.Reset()
		task.RecentLogs = nil
		task.Unlock()

		syncLegacySession(task)

		sendPlanForApproval(m, chat, task)
		return
	}

	task.Lock()
	currentPrompt := task.InitialPrompt
	if task.CurrentPrompt != "" {
		currentPrompt = task.CurrentPrompt
	}
	task.Unlock()

	for {
		task.Lock()
		if task.Status == domain.TaskStatusCancelled {
			task.Unlock()
			return
		}
		activeModel := task.Model
		task.CurrentPrompt = currentPrompt
		task.Unlock()

		syncLegacySession(task)

		res := executeStepForTask(m, chat, task, workDir, currentPrompt, activeModel)

		task.Lock()
		if task.Status == domain.TaskStatusCancelled || res.Outcome == StepOutcomeCancelled {
			task.Unlock()
			checkAndStartQueuedTask(m, projectName, config.ProjectsRoot)
			return
		}
		st := task.Status
		task.Unlock()

		if res.Outcome == StepOutcomeWaitingInput || st == domain.TaskStatusWaitingInput {
			answer, ok := waitForTaskInput(m, chat, task, projectName, taskID)
			if !ok {
				return
			}
			currentPrompt = answer
			continue
		}

		if res.Outcome == StepOutcomeTimeout {
			handleTaskStepTimeout(m, chat, task, projectName, taskID, false)
			return
		}

		if res.Outcome == StepOutcomeError {
			handleTaskStepError(m, chat, task, projectName, taskID, res.Error)
			return
		}

		task.Lock()
		if len(task.PendingFollowups) == 0 {
			prURL := task.LastPRURL
			finalReport := task.FullOutput.String()
			initialPrompt := task.InitialPrompt
			hasPlan := task.Plan != ""
			task.Status = domain.TaskStatusCompleted
			task.FinishedAt = time.Now()
			task.Unlock()

			syncLegacySession(task)

			metrics := domain.GlobalTokenTracker.FinishTask(prURL)
			task.Lock()
			task.TokenMetrics = &metrics
			task.Unlock()
			domain.GlobalTaskManager.SaveTaskMetrics(taskID, &metrics)
			domain.GlobalTaskManager.SaveTask(task)
			statsSummary := metrics.FormatCompletionSummary()

			var compBldr strings.Builder
			if prURL != "" {
				compBldr.WriteString(fmt.Sprintf("🎉 <b>Задача #%d выполнена!</b>\n📁 Проект: <code>%s</code>\n🔗 <a href=\"%s\">Открыть Pull Request</a>\n", taskID, html.EscapeString(projectName), html.EscapeString(prURL)))
			} else {
				compBldr.WriteString(fmt.Sprintf("✅ <b>Задача #%d завершена!</b> (<code>%s</code>)\n", taskID, html.EscapeString(projectName)))
			}

			if initialPrompt != "" {
				compBldr.WriteString(fmt.Sprintf("📝 <b>Задача:</b> <i>«%s»</i>\n",
					html.EscapeString(utils.TruncateString(initialPrompt, 200))))
			}

			if hasPlan {
				compBldr.WriteString(fmt.Sprintf("📄 <b>План реализации:</b> /planfile_%d\n", taskID))
			}

			compBldr.WriteString("\n" + statsSummary)

			var compMenu *ports.Keyboard
			var actButtons []ports.Button
			if prURL != "" {
				actButtons = append(actButtons, ports.Button{Text: "🔗 Открыть PR", URL: prURL})
			}
			if hasPlan {
				actButtons = append(actButtons, ports.Button{Text: "📄 Скачать план (.md)", Action: "plan_doc", Payload: strconv.Itoa(taskID)})
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
					_, _ = m.Send(context.Background(), chat, fmt.Sprintf("📑 <b>Отчет о выполнении задачи #%d:</b>\n\n%s\n\n📄 <i>Полный отчет (%d знаков) прикреплен файлом.</i>", taskID, summaryHTML, len(reportRunes)), ports.Rich())

					docName := fmt.Sprintf("report_task_%d.md", taskID)
					doc := ports.Document{
						FileName: docName,
						MIME:     "text/markdown",
						Caption:  fmt.Sprintf("📄 Полный отчет выполнения задачи #%d (%s)", taskID, projectName),
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
			checkAndStartQueuedTask(m, projectName, config.ProjectsRoot)
			return
		}

		followups := task.PendingFollowups
		task.ClearPendingFollowups()

		var bldr strings.Builder
		bldr.WriteString("ВНИМАНИЕ: Продолжай работу в ТЕКУЩЕЙ ветке git (НЕ создавай новую ветку, НЕ делай checkout в main). ")
		bldr.WriteString("Пользователь прислал следующие дополнения к задаче:\n")
		for i, f := range followups {
			bldr.WriteString(fmt.Sprintf("%d. %s\n", i+1, f))
		}
		bldr.WriteString("Внеси необходимые изменения, запусти тесты/линтеры, закоммить изменения и запушь в текущую ветку. Если PR уже открыт, обнови его.")

		currentPrompt = bldr.String()
		task.CurrentPrompt = strings.Join(followups, "; ")
		task.StartedAt = time.Now()
		task.RecentLogs = nil
		task.Unlock()

		domain.GlobalTokenTracker.StartNextStep(activeModel)

		_, _ = m.Send(context.Background(), chat, fmt.Sprintf("🔄 <b>Задача #%d: Беру в работу дополнения (%d шт.)...</b>", taskID, len(followups)), ports.Rich())
	}
}

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
		task.Lock()
		recentLogs := append([]string(nil), task.RecentLogs...)
		fullOutput := task.FullOutput.String()
		task.Unlock()

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
	return "неизвестная ошибка выполнения"
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

func executeStepForTask(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession, workDir, prompt, modelName string) StepResult {
	projectName := task.Project
	taskID := task.ID

	task.Lock()
	if task.Status == domain.TaskStatusCancelled {
		task.Unlock()
		return StepResult{Outcome: StepOutcomeCancelled}
	}
	isPlanning := task.Status == domain.TaskStatusPlanning
	task.Unlock()

	var statusMsgText string
	if isPlanning {
		statusMsgText = fmt.Sprintf("📝 <b>Составление плана задачи #%d:</b> <code>%s</code> [<code>%s</code>]\n<i>Исследование репозитория и формирование плана...</i>", taskID, html.EscapeString(projectName), html.EscapeString(modelName))
	} else {
		statusMsgText = fmt.Sprintf("🚀 <b>Шаг задачи #%d в работе:</b> <code>%s</code> [<code>%s</code>]\n<i>Инициализация сессии агента...</i>", taskID, html.EscapeString(projectName), html.EscapeString(modelName))
	}

	statusRef, _ := m.Send(context.Background(), chat, statusMsgText, ports.Rich())
	if statusRef.ID != "" {
		domain.GlobalTaskManager.RegisterMessageTask(statusRef, taskID)
	}

	task.Lock()
	task.LastModelUsed = modelName
	task.LiveMsg = &statusRef
	task.Unlock()

	syncLegacySession(task)

	task.Lock()
	convID := task.ConversationID
	taskAgent := task.Agent
	if taskAgent == "" {
		taskAgent = "agy"
	}
	task.Unlock()

	if convID != "" && !strings.EqualFold(taskAgent, ActiveAgentName) {
		err := fmt.Errorf("конфликт агентов: сессия задачи принадлежит %s, а текущий агент %s", taskAgent, ActiveAgentName)
		_, _ = m.Send(context.Background(), chat, fmt.Sprintf("❌ Ошибка запуска агента для задачи #%d: %v", taskID, err), nil)
		task.Lock()
		task.Status = domain.TaskStatusPaused
		task.Unlock()
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

	stepTimeout := config.StepTimeout
	if stepTimeout <= 0 {
		stepTimeout = 30 * time.Minute
	}
	stepCtx, stepCancel := context.WithTimeout(context.Background(), stepTimeout+2*time.Minute)
	defer stepCancel()

	framework := Agent
	if framework == nil || !strings.EqualFold(taskAgent, ActiveAgentName) {
		if strings.EqualFold(taskAgent, "claude") {
			framework = claude.NewClaudeAdapter()
		} else {
			framework = agy.NewAgyAdapter()
		}
	}
	agentProcess, err := framework.ExecuteTask(stepCtx, args)
	if err != nil {
		_, _ = m.Send(context.Background(), chat, fmt.Sprintf("❌ Ошибка запуска агента для задачи #%d: %v", taskID, err), nil)
		task.Lock()
		task.Status = domain.TaskStatusFailed
		task.Unlock()
		syncLegacySession(task)
		return StepResult{Outcome: StepOutcomeError, Error: err}
	}
	defer func() { _ = agentProcess.Close() }()

	task.Lock()
	task.Cmd = agentProcess.GetCmd()
	task.Stdin = agentProcess.Stdin()
	task.Unlock()
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
				task.Lock()
				if task.Status != domain.TaskStatusRunning && task.Status != domain.TaskStatusWaitingInput && task.Status != domain.TaskStatusPlanning {
					task.Unlock()
					return
				}
				var lastLine string
				if len(task.RecentLogs) > 0 {
					lastLine = task.RecentLogs[len(task.RecentLogs)-1]
				}
				followupsCount := len(task.PendingFollowups)
				taskStatus := task.Status
				task.Unlock()
				dur := task.Duration()

				tokenSnippet := domain.GlobalTokenTracker.GetLiveStatusSnippet()

				if statusRef.ID != "" {
					queueInfo := ""
					if followupsCount > 0 {
						queueInfo = fmt.Sprintf(" | Правок в очереди: %d", followupsCount)
					}
					statusPrefix := "⏳ <b>Задача"
					if taskStatus == domain.TaskStatusPlanning {
						statusPrefix = "📝 <b>Планирование задачи"
					}
					var bldr strings.Builder
					bldr.WriteString(fmt.Sprintf(
						"%s #%d:</b> <code>%s</code> [<code>%s</code>] (<code>%s</code>%s)\n\n",
						statusPrefix,
						taskID,
						html.EscapeString(projectName),
						html.EscapeString(modelName),
						domain.FormatDurationHuman(dur),
						queueInfo,
					))
					if lastLine != "" {
						bldr.WriteString(fmt.Sprintf("📍 <b>Действие:</b>\n<code>%s</code>\n\n", html.EscapeString(truncateString(lastLine, 80))))
					} else {
						bldr.WriteString("📍 <b>Действие:</b>\n<code>Инициализация сессии агента...</code>\n\n")
					}
					if tokenSnippet != "" {
						bldr.WriteString(tokenSnippet + "\n\n")
					}
					bldr.WriteString(fmt.Sprintf("<i>(Лог: /status %d | Дополнить: /add %d | Стоп: /cancel %d)</i>", taskID, taskID, taskID))

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
						task.Lock()
						task.FullOutput.WriteString(u.TextDelta)
						if matches := prUrlRegexp.FindStringSubmatch(u.TextDelta); len(matches) > 1 {
							task.LastPRURL = matches[1]
						}
						task.Unlock()
					}

					// Фиксация вызова инструмента ask_question (без преждевременного прерывания процесса)
					if u.ToolName == "ask_question" || (u.ToolInfo != nil && u.ToolInfo.Name == "ask_question") {
						hasAskQuestionToolCall = true
						if u.ToolInfo != nil && u.ToolInfo.Parameters != nil {
							qText := utils.FormatAskQuestionParams(u.ToolInfo.Parameters)
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
						task.Lock()
						if task.FullOutput.Len() == 0 {
							task.FullOutput.WriteString(res.Response)
						}
						if matches := prUrlRegexp.FindStringSubmatch(res.Response); len(matches) > 1 {
							task.LastPRURL = matches[1]
						}
						task.Unlock()
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
			task.Lock()
			task.FullOutput.WriteString(cleanLine + "\n")
			if matches := prUrlRegexp.FindStringSubmatch(cleanLine); len(matches) > 1 {
				task.LastPRURL = matches[1]
			}
			if m := modelRegexp.FindStringSubmatch(cleanLine); len(m) > 1 {
				task.LastModelUsed = strings.TrimSpace(m[1])
			}
			if t := tokensRegexp.FindStringSubmatch(cleanLine); len(t) > 1 {
				task.LastTokensUsed = strings.TrimSpace(t[1])
			}
			task.Unlock()
		}
		if scanErr := scanner.Err(); scanErr != nil {
			log.Printf("Предупреждение: ошибка сканера вывода agy для задачи #%d: %v", taskID, scanErr)
		}
		close(done)
	}()

	<-done
	close(stopLiveUpdate)
	waitErr := agentProcess.Wait()

	task.Lock()
	if task.Stdin != nil {
		_ = task.Stdin.Close()
		task.Stdin = nil
	}
	isCancelled := (task.Status == domain.TaskStatusCancelled)
	lastPR := task.LastPRURL
	fullResp := strings.TrimSpace(task.FullOutput.String())
	task.Unlock()
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
		task.Lock()
		task.Status = domain.TaskStatusWaitingInput
		task.LastQuestion = qText
		task.QuestionOptions = qOpts
		task.QuestionAskedAt = time.Now()
		task.Unlock()
		syncLegacySession(task)

		menu := buildQuestionMarkup(task)
		formattedQ := utils.MarkdownToTelegramHTML(qText)
		header := "❓ <b>Вопрос по задаче #%d (<code>%s</code>):</b>\n\n%s\n\n<i>Ответьте сообщением в чат или выберите вариант кнопкой.</i>"
		if isPlanning {
			header = "❓ <b>Вопрос по плану задачи #%d (<code>%s</code>):</b>\n\n%s\n\n<i>Ответьте сообщением в чат или выберите вариант кнопкой.</i>"
		}
		msgText := fmt.Sprintf(header, taskID, html.EscapeString(projectName), formattedQ)
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

func handleTaskStepTimeout(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession, projectName string, taskID int, isPlanning bool) {
	task.Lock()
	task.Status = domain.TaskStatusPaused
	convID := task.ConversationID
	tAgent := task.Agent
	if tAgent == "" {
		tAgent = "agy"
	}
	task.Unlock()

	syncLegacySession(task)

	stepTimeout := config.StepTimeout
	if stepTimeout <= 0 {
		stepTimeout = 30 * time.Minute
	}

	phaseName := "выполнения"
	if isPlanning {
		phaseName = "планирования"
	}

	task.AppendLog(fmt.Sprintf("⏸ Превышен таймаут %s (%v). Сессия %s сохранена.", phaseName, stepTimeout, convID))

	resumeMenu := buildResumeMarkup(taskID)
	timeoutMsg := fmt.Sprintf(
		"⏸ <b>Задача #%d (<code>%s</code>) приостановлена по таймауту %s (%v).</b>\n\n"+
			"🧵 <b>Сессия %s:</b> <code>%s</code> (сохранена)\n"+
			"Очередь проекта освобождена для других задач.\n\n"+
			"Контекст не потерян! Чтобы продолжить с этого места, нажмите <b>«▶️ Возобновить задачу»</b> или введите <code>/resume %d [указания]</code>.",
		taskID, html.EscapeString(projectName), phaseName, stepTimeout,
		html.EscapeString(tAgent), html.EscapeString(convID), taskID,
	)

	if m != nil && chat != "" {
		defer func() { _ = recover() }()
		tRef, _ := m.Send(context.Background(), chat, timeoutMsg, ports.RichWith(resumeMenu))
		if tRef.ID != "" {
			domain.GlobalTaskManager.RegisterMessageTask(tRef, taskID)
		}
	}

	checkAndStartQueuedTask(m, projectName, config.ProjectsRoot)
}

func handleTaskStepError(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession, projectName string, taskID int, err error) {
	task.Lock()
	task.Status = domain.TaskStatusFailed
	task.FinishedAt = time.Now()
	convID := task.ConversationID
	task.Unlock()

	syncLegacySession(task)

	errText := "неизвестная ошибка"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		errText = strings.TrimSpace(err.Error())
	}
	if len([]rune(errText)) > 1200 {
		errText = string([]rune(errText)[:1200]) + "..."
	}

	task.AppendLog(fmt.Sprintf("❌ Ошибка выполнения шага: %s", errText))

	retryMenu := buildResumeMarkup(taskID)
	msg := fmt.Sprintf(
		"❌ <b>Ошибка выполнения задачи #%d (<code>%s</code>):</b>\n\n"+
			"<code>%s</code>\n\n"+
			"🧵 <b>Сессия agy:</b> <code>%s</code>\n\n"+
			"Попробуйте возобновить: <code>/resume %d</code> или перезапустить с чистого листа: <code>/retry %d</code>.",
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

	checkAndStartQueuedTask(m, projectName, config.ProjectsRoot)
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

// isKnownAgent проверяет, является ли переданная строка именем поддерживаемого CLI агента.
func isKnownAgent(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "agy", "claude":
		return true
	default:
		return false
	}
}

// isProjectDir проверяет, существует ли директория проекта с таким именем в ProjectsRoot.
func isProjectDir(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasPrefix(name, ".") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	path := filepath.Join(config.ProjectsRoot, name)
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
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
	config.ProjectState.RLock()
	curProj := config.ProjectState.CurrentProject
	curMod := config.ProjectState.CurrentModel
	curAgent := ActiveAgentName
	config.ProjectState.RUnlock()

	targetProj, targetAgent, prompt := parseNewTaskInput(text, curProj, curAgent)
	if prompt == "" {
		return s.Send("Использование: <code>/new &lt;описание задачи&gt;</code>\n(или <code>/new [проект] [агент] &lt;описание&gt;</code>)", ports.Rich())
	}

	if targetProj == "" {
		return s.Send("❌ Сначала выберите проект: /projects", nil)
	}

	if targetAgent != "" && !strings.EqualFold(targetAgent, ActiveAgentName) {
		if _, err := SwitchActiveAgent(targetAgent); err != nil {
			return s.Send(fmt.Sprintf("❌ Ошибка переключения на агента <b>%s</b>: %v", html.EscapeString(targetAgent), err), ports.Rich())
		}
	}

	config.ProjectState.RLock()
	curMod = config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent(targetProj, curMod, targetAgent, prompt, s.Chat(), requiresPlan)
	syncLegacySession(task)

	if domain.GlobalTaskManager.HasRunningTaskInProject(targetProj) {
		planNote := ""
		if requiresPlan {
			planNote = " Сначала будет составлен подробный план."
		}
		msg := fmt.Sprintf(
			"⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code>:\n\n"+
				"<i>«%s»</i>\n\n"+
				"💡 В этом проекте уже выполняется задача. Задача #%d начнется автоматически после ее завершения.%s",
			task.ID, html.EscapeString(targetProj), html.EscapeString(utils.TruncateString(prompt, 250)), task.ID, planNote,
		)
		return s.Send(msg, ports.Rich())
	}

	task.Lock()
	if requiresPlan {
		task.Status = domain.TaskStatusPlanning
	} else {
		task.Status = domain.TaskStatusRunning
	}
	task.StartedAt = time.Now()
	task.Unlock()
	syncLegacySession(task)
	_, _ = domain.GlobalTaskManager.SetActiveTask(task.ID)

	domain.GlobalTokenTracker.StartTaskWithAgent(targetProj, curMod, prompt, targetAgent)
	workDir := filepath.Join(config.ProjectsRoot, targetProj)

	go runAgentTaskPipeline(s.Messenger(), s.Chat(), task, workDir)

	return nil
}

func handleAddFollowupToTask(s ports.Session, taskID int, text string) error {
	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return s.Send(fmt.Sprintf("❌ Задача #%d не найдена.", taskID), ports.Rich())
	}

	task.Lock()
	status := task.Status
	cmdIsNil := (task.Cmd == nil || task.Cmd.Process == nil)
	task.Unlock()

	if status == domain.TaskStatusWaitingApproval {
		if isConfirmationText(text) {
			return handleApprovePlan(s.Messenger(), s.Chat(), taskID)
		}
		return handleRevisePlan(s.Messenger(), s.Chat(), taskID, text)
	}

	if (status == domain.TaskStatusPaused || status == domain.TaskStatusCancelled || (status == domain.TaskStatusWaitingInput && cmdIsNil)) && HasAgentConflict(task, ActiveAgentName) {
		task.Lock()
		task.CurrentPrompt = text
		task.Unlock()
		domain.GlobalTaskManager.SaveTask(task)
		return sendAgentConflictDialog(s, task)
	}

	task, qLen, isAnswer, err := domain.GlobalTaskManager.AddFollowup(taskID, text)
	if err != nil {
		return s.Send(fmt.Sprintf("❌ Не удалось отправить дополнение к задаче #%d: %s", taskID, err.Error()), ports.Rich())
	}

	syncLegacySession(task)

	if isAnswer {
		task.Lock()
		curStatus := task.Status
		proj := task.Project
		cmdIsNil = (task.Cmd == nil || task.Cmd.Process == nil)
		task.Unlock()

		if curStatus == domain.TaskStatusQueued {
			return s.Send(fmt.Sprintf("⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code> с ответом:\n<i>«%s»</i>",
				taskID, html.EscapeString(proj), html.EscapeString(utils.TruncateString(text, 250))), ports.Rich())
		} else if (curStatus == domain.TaskStatusRunning || curStatus == domain.TaskStatusWaitingInput) && cmdIsNil {
			if HasAgentConflict(task, ActiveAgentName) {
				task.Lock()
				task.CurrentPrompt = text
				task.Unlock()
				domain.GlobalTaskManager.SaveTask(task)
				return sendAgentConflictDialog(s, task)
			}
			task.Lock()
			if task.RequiresPlan && !task.PlanApproved {
				task.Status = domain.TaskStatusPlanning
			} else {
				task.Status = domain.TaskStatusRunning
			}
			task.StartedAt = time.Now()
			task.CurrentPrompt = text
			task.LastQuestion = ""
			task.QuestionOptions = nil
			task.Unlock()
			syncLegacySession(task)

			workDir := filepath.Join(config.ProjectsRoot, proj)
			go runAgentTaskPipeline(s.Messenger(), s.Chat(), task, workDir)
			return s.Send(fmt.Sprintf("▶️ <b>Задача #%d (<code>%s</code>) возобновлена с ответом:</b>\n<i>«%s»</i>",
				taskID, html.EscapeString(proj), html.EscapeString(utils.TruncateString(text, 250))), ports.Rich())
		}

		return s.Send(fmt.Sprintf("💬 <b>Ответ передан задаче #%d</b> (<code>%s</code>)...", taskID, html.EscapeString(task.Project)), ports.Rich())
	}

	msg := fmt.Sprintf(
		"📥 <b>Дополнение сохранено в задачу #%d</b> (<code>%s</code>) [#%d в очереди]:\n\n"+
			"<i>«%s»</i>\n\n"+
			"Агент завершит текущий шаг и применит эти правки в ветку задачи #%d.",
		taskID, html.EscapeString(task.Project), qLen, html.EscapeString(utils.TruncateString(text, 250)), taskID,
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

func handleApprovePlan(m ports.Messenger, chat ports.ChatID, taskID int) error {
	return handleApprovePlanWithVariant(m, chat, taskID, "")
}

func handleApprovePlanWithVariant(m ports.Messenger, chat ports.ChatID, taskID int, variant string) error {
	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		_, err := m.Send(context.Background(), chat, fmt.Sprintf("❌ Задача #%d не найдена.", taskID), nil)
		return err
	}

	task.Lock()
	if task.Status != domain.TaskStatusWaitingApproval {
		statusTitle := task.Status.RussianTitle()
		task.Unlock()
		_, err := m.Send(context.Background(), chat, fmt.Sprintf("ℹ️ Задача #%d не ожидает утверждения плана (текущий статус: %s).", taskID, statusTitle), nil)
		return err
	}

	task.PlanApproved = true
	task.Status = domain.TaskStatusRunning
	task.StartedAt = time.Now()
	task.RecentLogs = nil

	projectName := task.Project
	modelName := task.Model
	initialPrompt := task.InitialPrompt
	planText := task.Plan

	variantInstruction := ""
	if variant != "" {
		variantInstruction = fmt.Sprintf("\n\nПОЛЬЗОВАТЕЛЬ ВЫБРАЛ И УТВЕРДИЛ ВАРИАНТ:\n%s\nРеализуй задачу строго в соответствии с этим выбранным вариантом плана.", variant)
	}

	implPrompt := fmt.Sprintf(
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

	task.CurrentPrompt = implPrompt
	task.Unlock()

	syncLegacySession(task)
	_, _ = domain.GlobalTaskManager.SetActiveTask(taskID)

	if HasAgentConflict(task, ActiveAgentName) {
		domain.GlobalTaskManager.SaveTask(task)
		return sendAgentConflictDialogWithMessenger(m, chat, task)
	}

	_, _ = m.Send(context.Background(), chat, fmt.Sprintf("🚀 <b>План задачи #%d утверждён!</b>\nПриступаю к автономной реализации в <code>%s</code>...", taskID, html.EscapeString(projectName)), ports.Rich())

	domain.GlobalTokenTracker.StartTaskWithAgent(projectName, modelName, initialPrompt, ActiveAgentName)
	workDir := filepath.Join(config.ProjectsRoot, projectName)
	go runAgentTaskPipeline(m, chat, task, workDir)

	return nil
}

func handleRevisePlan(m ports.Messenger, chat ports.ChatID, taskID int, feedback string) error {
	task := domain.GlobalTaskManager.GetTask(taskID)
	if task == nil {
		return fmt.Errorf("задача #%d не найдена", taskID)
	}

	task.Lock()
	task.Status = domain.TaskStatusPlanning
	task.CurrentPrompt = feedback
	task.StartedAt = time.Now()
	task.RecentLogs = nil
	projectName := task.Project
	task.Unlock()

	syncLegacySession(task)
	_, _ = domain.GlobalTaskManager.SetActiveTask(taskID)

	if HasAgentConflict(task, ActiveAgentName) {
		domain.GlobalTaskManager.SaveTask(task)
		return sendAgentConflictDialogWithMessenger(m, chat, task)
	}

	_, _ = m.Send(context.Background(), chat, fmt.Sprintf("📝 <b>Задача #%d: Обновляю план с учётом замечаний...</b>\n<i>«%s»</i>", taskID, html.EscapeString(truncateString(feedback, 100))), ports.Rich())

	workDir := filepath.Join(config.ProjectsRoot, projectName)
	go runAgentTaskPipeline(m, chat, task, workDir)

	return nil
}

func sendTaskPlanDocument(s ports.Session, task *domain.TaskSession) error {
	if task == nil {
		return s.Send("❌ Задача не найдена. Список задач: /tasks", ports.Rich())
	}

	task.Lock()
	planText := strings.TrimSpace(task.Plan)
	id := task.ID
	proj := task.Project
	task.Unlock()

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

// sendTaskHistory читает и отправляет переписку (реплики пользователя и
// агента) для любой сессии задачи, включая уже завершённые. История читается
// заново из файла сессии CLI-агента при каждом вызове и нигде ботом не
// сохраняется — только последний общий предпросмотр показывается в чате, а
// полная версия при необходимости отправляется отдельным файлом.
func sendTaskHistory(s ports.Session, task *domain.TaskSession) error {
	if task == nil {
		return s.Send("❌ Задача не найдена. Список задач: /tasks", ports.Rich())
	}

	task.Lock()
	id := task.ID
	proj := task.Project
	convID := task.ConversationID
	agentName := task.Agent
	task.Unlock()
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

const maxInlinePlanRunes = 1200

func sendPlanForApproval(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession) {
	task.Lock()
	taskID := task.ID
	projectName := task.Project
	initialPrompt := task.InitialPrompt
	planText := strings.TrimSpace(task.Plan)
	task.Unlock()

	var rows [][]ports.Button

	// Проверяем наличие альтернативных вариантов в плане
	variants := utils.ExtractPlanVariantOptions(planText)
	if len(variants) > 0 {
		for i, v := range variants {
			btnText := fmt.Sprintf("Утвердить: %s", truncateString(v, 24))
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

func buildResumeMarkup(taskID int) *ports.Keyboard {
	return &ports.Keyboard{Rows: [][]ports.Button{
		{
			{Text: "▶️ Возобновить задачу", Action: "q_resume", Payload: strconv.Itoa(taskID)},
			{Text: "❌ Отменить", Action: "plan_cancel", Payload: strconv.Itoa(taskID)},
		},
	}}
}

func buildQuestionMarkup(task *domain.TaskSession) *ports.Keyboard {
	task.Lock()
	taskID := task.ID
	options := append([]string(nil), task.QuestionOptions...)
	task.Unlock()

	var rows [][]ports.Button

	if len(options) > 0 {
		var optButtons []ports.Button
		for i, opt := range options {
			cleanOpt := strings.TrimSpace(opt)
			btnText := fmt.Sprintf("%d. %s", i+1, truncateString(cleanOpt, 30))
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

	rows = append(rows, []ports.Button{
		{Text: "⏸ Приостановить", Action: "q_pause", Payload: strconv.Itoa(taskID)},
		{Text: "❌ Отменить", Action: "plan_cancel", Payload: strconv.Itoa(taskID)},
	})

	return &ports.Keyboard{Rows: rows}
}

func waitForTaskInput(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession, projectName string, taskID int) (string, bool) {
	timeout := config.QuestionTimeout

	select {
	case answer := <-task.AnswerChan:
		task.Lock()
		if task.RequiresPlan && !task.PlanApproved {
			task.Status = domain.TaskStatusPlanning
		} else {
			task.Status = domain.TaskStatusRunning
		}
		task.CurrentPrompt = answer
		task.LastQuestion = ""
		task.QuestionOptions = nil
		task.StartedAt = time.Now()
		task.Unlock()
		syncLegacySession(task)

		_, _ = m.Send(context.Background(), chat, fmt.Sprintf("▶️ <b>Задача #%d: Ответ получен, продолжаю выполнение...</b>", taskID), ports.Rich())
		return answer, true

	case <-task.PauseChan:
		task.Lock()
		isCancelled := (task.Status == domain.TaskStatusCancelled)
		if !isCancelled {
			task.Status = domain.TaskStatusPaused
		}
		task.Unlock()
		syncLegacySession(task)

		if !isCancelled {
			checkAndStartQueuedTask(m, projectName, config.ProjectsRoot)
		}
		return "", false

	case <-time.After(timeout):
		task.Lock()
		if task.Status == domain.TaskStatusWaitingInput {
			task.Status = domain.TaskStatusPaused
		}
		task.Unlock()
		syncLegacySession(task)

		resumeMenu := buildResumeMarkup(taskID)
		timeoutMsg := fmt.Sprintf(
			"⏸ <b>Задача #%d (<code>%s</code>) приостановлена по таймауту ожидания ответа (%v).</b>\n\n"+
				"Очередь проекта освобождена для других задач.\n"+
				"Чтобы возобновить с места вопроса, нажмите <b>«▶️ Возобновить задачу»</b> или введите <code>/resume %d &lt;ответ&gt;</code>.",
			taskID, html.EscapeString(projectName), timeout, taskID,
		)
		tRef, _ := m.Send(context.Background(), chat, timeoutMsg, ports.RichWith(resumeMenu))
		if tRef.ID != "" {
			domain.GlobalTaskManager.RegisterMessageTask(tRef, taskID)
		}

		checkAndStartQueuedTask(m, projectName, config.ProjectsRoot)
		return "", false
	}
}

func checkAndStartQueuedTask(m ports.Messenger, project, root string) {
	nextTask := domain.GlobalTaskManager.GetNextQueuedTaskForProject(project)
	if nextTask == nil {
		return
	}

	if HasAgentConflict(nextTask, ActiveAgentName) {
		nextTask.Lock()
		nextTask.Status = domain.TaskStatusPaused
		chat := nextTask.Chat
		nextTask.Unlock()
		domain.GlobalTaskManager.SaveTask(nextTask)
		_ = sendAgentConflictDialogWithMessenger(m, chat, nextTask)
		return
	}

	nextTask.Lock()
	isPlanning := nextTask.RequiresPlan && !nextTask.PlanApproved
	if isPlanning {
		nextTask.Status = domain.TaskStatusPlanning
	} else {
		nextTask.Status = domain.TaskStatusRunning
	}
	nextTask.StartedAt = time.Now()
	chat := nextTask.Chat
	nextID := nextTask.ID
	prompt := nextTask.InitialPrompt
	model := nextTask.Model
	tAgent := nextTask.Agent
	if tAgent == "" {
		tAgent = ActiveAgentName
	}
	nextTask.Unlock()

	syncLegacySession(nextTask)
	_, _ = domain.GlobalTaskManager.SetActiveTask(nextID)

	if isPlanning {
		_, _ = m.Send(context.Background(), chat, fmt.Sprintf("📝 <b>Запуск планирования задачи #%d из очереди:</b> <code>%s</code>\n<i>«%s»</i>",
			nextID, html.EscapeString(project), html.EscapeString(truncateString(prompt, 80))), ports.Rich())
	} else {
		_, _ = m.Send(context.Background(), chat, fmt.Sprintf("🚀 <b>Запуск задачи #%d из очереди:</b> <code>%s</code>\n<i>«%s»</i>",
			nextID, html.EscapeString(project), html.EscapeString(truncateString(prompt, 80))), ports.Rich())
	}

	domain.GlobalTokenTracker.StartTaskWithAgent(project, model, prompt, tAgent)
	workDir := filepath.Join(root, project)
	go runAgentTaskPipeline(m, chat, nextTask, workDir)
}

func syncLegacySession(task *domain.TaskSession) {
	if task == nil {
		config.Session.Lock()
		config.Session.IsRunning = false
		config.Session.Waiting = false
		config.Session.Cmd = nil
		config.Session.Stdin = nil
		config.Session.Unlock()
		return
	}

	task.Lock()
	config.Session.Lock()
	config.Session.IsRunning = (task.Status == domain.TaskStatusRunning || task.Status == domain.TaskStatusWaitingInput || task.Status == domain.TaskStatusPlanning)
	config.Session.Waiting = (task.Status == domain.TaskStatusWaitingInput || task.Status == domain.TaskStatusWaitingApproval)
	config.Session.StartedAt = task.StartedAt
	config.Session.CurrentPrompt = task.InitialPrompt
	config.Session.CurrentProject = task.Project
	config.Session.RecentLogs = append([]string(nil), task.RecentLogs...)
	config.Session.PendingFollowups = append([]string(nil), task.PendingFollowups...)
	config.Session.LastPRURL = task.LastPRURL
	config.Session.LastModelUsed = task.LastModelUsed
	config.Session.LastTokensUsed = task.LastTokensUsed
	config.Session.Cmd = task.Cmd
	config.Session.Stdin = task.Stdin
	config.Session.Unlock()
	task.Unlock()

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

func truncateString(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

func renderProgressBar(fraction float64, totalBlocks int) string {
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	filled := int(math.Round(fraction * float64(totalBlocks)))
	if filled > totalBlocks {
		filled = totalBlocks
	}
	empty := totalBlocks - filled
	return strings.Repeat("█", filled) + strings.Repeat("░", empty)
}

func quotaStatusEmoji(fraction float64) string {
	switch {
	case fraction >= 0.5:
		return "🟢"
	case fraction >= 0.2:
		return "🟡"
	default:
		return "🔴"
	}
}

func formatBucketName(name, window string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "week") || window == "weekly":
		return "Недельный лимит"
	case strings.Contains(lower, "five hour") || strings.Contains(lower, "5 hour") || window == "5h":
		return "5-часовой лимит"
	default:
		return name
	}
}

func formatResetDuration(resetTimeStr string) string {
	if resetTimeStr == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, resetTimeStr)
	if err != nil {
		return resetTimeStr
	}
	remaining := time.Until(t)
	formattedTime := t.UTC().Format("02.01 15:04 UTC")
	if remaining <= 0 {
		return fmt.Sprintf("сейчас (%s)", formattedTime)
	}

	var parts []string
	days := int(remaining.Hours()) / 24
	hours := int(remaining.Hours()) % 24
	mins := int(remaining.Minutes()) % 60

	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d д.", days))
	}
	if hours > 0 || (days > 0 && mins > 0) {
		parts = append(parts, fmt.Sprintf("%d ч.", hours))
	}
	if days == 0 && mins > 0 {
		parts = append(parts, fmt.Sprintf("%d мин.", mins))
	}
	if len(parts) == 0 {
		parts = append(parts, "< 1 мин.")
	}

	return fmt.Sprintf("через %s (%s)", strings.Join(parts, " "), formattedTime)
}

// getDefaultCommands возвращает список команд для регистрации в мессенджере (меню подсказок).
func getDefaultCommands() []ports.BotCommand {
	return []ports.BotCommand{
		{Name: "status", Description: "[id] Статус текущей задачи, логи и очередь"},
		{Name: "tasks", Description: "Список всех задач и переключение"},
		{Name: "task", Description: "<id> [текст] Переключить фокус на задачу или дополнить её"},
		{Name: "plan", Description: "[проект] <текст> Составить план для новой задачи"},
		{Name: "planmode", Description: "[on|off] Включить/выключить обязательный план"},
		{Name: "approve", Description: "[id] Утвердить план и начать реализацию"},
		{Name: "planfile", Description: "[id] Скачать полный план задачи в виде .md файла"},
		{Name: "history", Description: "[id] Показать переписку пользователя и агента в сессии задачи"},
		{Name: "add", Description: "[id] <текст> Дополнить задачу текстом"},
		{Name: "new", Description: "[проект] [агент] <текст> Создать новую задачу в проекте/агенте"},
		{Name: "resume", Description: "[id] [ответ] Возобновить задачу или передать ответ"},
		{Name: "retry", Description: "[id] Перезапустить задачу с чистого листа"},
		{Name: "pause", Description: "[id] Приостановить выполнение задачи"},
		{Name: "cancel", Description: "[id] Остановить задачу"},
		{Name: "tokens", Description: "Статистика токенов, скорости и кэша"},
		{Name: "context", Description: "[id] Распределение окна контекста модели"},
		{Name: "top", Description: "Мониторинг CPU и памяти бота, agy и claude"},
		{Name: "usage", Description: "Остаток квот и лимиты аккаунта"},
		{Name: "models", Description: "Список доступных моделей"},
		{Name: "model", Description: "[имя] Переключить активную модель"},
		{Name: "agent", Description: "[agy|claude] Переключить активного CLI агента"},
		{Name: "projects", Description: "Список доступных проектов"},
		{Name: "use", Description: "<имя> Переключить активный проект"},
		{Name: "clone", Description: "<url> [имя] Клонировать git-репозиторий"},
		{Name: "restart", Description: "Перезапустить бота"},
		{Name: "rebuild", Description: "Собрать и перезапустить бота"},
		{Name: "start", Description: "Перезапуск и приветственное сообщение"},
	}
}

// SwitchActiveAgent переключает глобального активного агента CLI (agy или claude) и сохраняет выбор в базе данных.
func SwitchActiveAgent(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "agy":
		adapter := agy.NewAgyAdapter()
		Agent = adapter
		models.Agent = adapter
		ActiveAgentName = "agy"
		config.ProjectState.SetCurrentAgent("agy")
		config.ProjectState.Lock()
		curModel := config.ProjectState.CurrentModel
		suggested := ""
		if strings.Contains(strings.ToLower(curModel), "claude") || strings.Contains(strings.ToLower(curModel), "sonnet") || strings.Contains(strings.ToLower(curModel), "opus") || strings.Contains(strings.ToLower(curModel), "haiku") {
			defaultModel := os.Getenv("DEFAULT_MODEL")
			if defaultModel == "" {
				defaultModel = "gemini-3.1-pro-high"
			}
			config.ProjectState.CurrentModel = defaultModel
			suggested = fmt.Sprintf("\nМодель автоматически переключена на <b>%s</b>.", html.EscapeString(defaultModel))
			if st := domain.GlobalTaskManager.Storage(); st != nil {
				_ = st.SetSetting(context.Background(), "current_model", defaultModel)
			}
		}
		config.ProjectState.Unlock()
		if st := domain.GlobalTaskManager.Storage(); st != nil {
			_ = st.SetSetting(context.Background(), "current_agent", "agy")
		}
		go func() {
			if models.GlobalModelRegistry != nil {
				_, _ = models.GlobalModelRegistry.RefreshModels(true)
			}
		}()
		return "✅ CLI агент переключен на: <b>agy</b>" + suggested, nil
	case "claude":
		adapter := claude.NewClaudeAdapter()
		Agent = adapter
		models.Agent = adapter
		ActiveAgentName = "claude"
		config.ProjectState.SetCurrentAgent("claude")
		config.ProjectState.Lock()
		curModel := config.ProjectState.CurrentModel
		suggested := ""
		if strings.Contains(strings.ToLower(curModel), "gemini") || strings.Contains(strings.ToLower(curModel), "gpt") {
			config.ProjectState.CurrentModel = "sonnet"
			suggested = "\nМодель автоматически переключена на <b>sonnet</b> (Claude Sonnet 4.6)."
			if st := domain.GlobalTaskManager.Storage(); st != nil {
				_ = st.SetSetting(context.Background(), "current_model", "sonnet")
			}
		}
		config.ProjectState.Unlock()
		if st := domain.GlobalTaskManager.Storage(); st != nil {
			_ = st.SetSetting(context.Background(), "current_agent", "claude")
		}
		go func() {
			if models.GlobalModelRegistry != nil {
				_, _ = models.GlobalModelRegistry.RefreshModels(true)
			}
		}()
		return "✅ CLI агент переключен на: <b>claude</b>" + suggested, nil
	default:
		return "", fmt.Errorf("неизвестный агент: <code>%s</code>. Доступны: <b>agy</b>, <b>claude</b>", html.EscapeString(name))
	}
}

// HasAgentConflict проверяет, есть ли несовместимость между активным агентом и агентом задачи.
// Конфликт возникает, только если задача уже имеет сессию CLI (ConversationID != "") и её агент не совпадает с активным.
func HasAgentConflict(task *domain.TaskSession, activeAgent string) bool {
	if task == nil {
		return false
	}
	task.Lock()
	defer task.Unlock()

	taskAgent := task.Agent
	if taskAgent == "" {
		taskAgent = "agy"
	}
	return task.ConversationID != "" && !strings.EqualFold(taskAgent, activeAgent)
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
	task.Lock()
	id := task.ID
	agent := task.Agent
	if agent == "" {
		agent = "agy"
	}
	convID := task.ConversationID
	task.Unlock()

	markup := buildAgentConflictMarkup(id, agent)
	msg := fmt.Sprintf(
		"⚠️ <b>Задача #%d была начата агентом %s</b> (сессия: <code>%s</code>).\n"+
			"Текущий активный агент бота: <b>%s</b>.\n\n"+
			"Сессии разных агентов несовместимы. Выберите действие:",
		id, html.EscapeString(agent), html.EscapeString(convID), html.EscapeString(ActiveAgentName),
	)
	return s.Send(msg, ports.RichWith(markup))
}

func sendAgentConflictDialogWithMessenger(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession) error {
	task.Lock()
	id := task.ID
	agent := task.Agent
	if agent == "" {
		agent = "agy"
	}
	convID := task.ConversationID
	task.Unlock()

	markup := buildAgentConflictMarkup(id, agent)
	msg := fmt.Sprintf(
		"⚠️ <b>Задача #%d была начата агентом %s</b> (сессия: <code>%s</code>).\n"+
			"Текущий активный агент бота: <b>%s</b>.\n\n"+
			"Сессии разных агентов несовместимы. Выберите действие:",
		id, html.EscapeString(agent), html.EscapeString(convID), html.EscapeString(ActiveAgentName),
	)
	_, err := m.Send(context.Background(), chat, msg, ports.RichWith(markup))
	return err
}
