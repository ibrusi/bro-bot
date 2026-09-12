package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
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

	"github.com/creack/pty"
	tele "gopkg.in/telebot.v3"
)

type AgyQuotaResponse struct {
	Status   string `json:"status"`
	Response string `json:"response"`
	Command  struct {
		Name string `json:"name"`
		Data struct {
			Description string           `json:"description"`
			Groups      []AgyQuotaGroup  `json:"groups"`
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

type AgentSession struct {
	sync.Mutex
	cmd              *exec.Cmd
	stdin            io.WriteCloser
	isRunning        bool
	waiting          bool
	startedAt        time.Time
	currentPrompt    string
	currentProject   string
	recentLogs       []string
	pendingFollowups []string
	lastPRURL        string
	fullOutput       strings.Builder
	lastModelUsed    string
	lastTokensUsed   string
}

type ProjectState struct {
	sync.RWMutex
	currentProject string
	currentModel   string
	planMode       bool
}

var (
	session      AgentSession
	projectState ProjectState
	adminID      int64
	projectsRoot = "/home/deploy/projects"
	prUrlRegexp  = regexp.MustCompile(`PR_URL:\s*(https?://[^\s]+)`)
	ansiRegex    = regexp.MustCompile(`\x1b(\[[0-9;?><=$]*[a-zA-Z~]|\][0-9;]*\x07|\([B0-9])|\r`)
	tokensRegexp = regexp.MustCompile(`(?i)tokens?:\s*([0-9,kKmM\s/]+)`)
	modelRegexp  = regexp.MustCompile(`(?i)model:\s*([a-zA-Z0-9.\-_]+)`)
)

func main() {
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	adminIDStr := os.Getenv("TELEGRAM_ADMIN_ID")
	envProjectsRoot := os.Getenv("PROJECTS_ROOT")
	if envProjectsRoot != "" {
		projectsRoot = envProjectsRoot
	}

	var err error
	adminID, err = strconv.ParseInt(adminIDStr, 10, 64)
	if err != nil || adminID == 0 {
		log.Fatal("Некорректный или отсутствующий TELEGRAM_ADMIN_ID")
	}

	modelRegistry = NewModelRegistry(10 * time.Minute)

	initDefaultProject(projectsRoot)
	projectState.Lock()
	projectState.currentModel = "gemini-3.8-flash-medium"
	projectState.Unlock()

	b, err := tele.NewBot(tele.Settings{
		Token:  botToken,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		log.Fatal(err)
	}

	commands := []tele.Command{
		{Text: "status", Description: "Статус текущей задачи, лог и очередь"},
		{Text: "tasks", Description: "Список всех задач и переключение"},
		{Text: "task", Description: "Переключить задачу: /task <id> [текст]"},
		{Text: "add", Description: "Дополнить задачу: /add <id> <текст>"},
		{Text: "new", Description: "Создать новую задачу: /new <текст>"},
		{Text: "cancel", Description: "Остановить задачу: /cancel [id]"},
		{Text: "tokens", Description: "Статистика токенов, скорости и кэша"},
		{Text: "top", Description: "CPU и память бота и agy"},
		{Text: "limits", Description: "Остаток квот и лимиты моделей"},
		{Text: "models", Description: "Список доступных моделей"},
		{Text: "model", Description: "Выбрать модель: /model <имя>"},
		{Text: "projects", Description: "Список доступных проектов"},
		{Text: "use", Description: "Переключить проект: /use <имя>"},
		{Text: "restart", Description: "Перезапустить бота"},
		{Text: "rebuild", Description: "Собрать билд и перезапустить"},
		{Text: "start", Description: "Справка и активный проект"},
	}
	if err := b.SetCommands(commands); err != nil {
		log.Printf("Предупреждение: не удалось зарегистрировать команды: %v", err)
	}

	b.Use(func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			if c.Sender().ID != adminID {
				return nil
			}
			return next(c)
		}
	})

	b.Handle("/start", func(c tele.Context) error {
		projectState.RLock()
		curProj := projectState.currentProject
		curMod := projectState.currentModel
		projectState.RUnlock()

		msg := fmt.Sprintf(
			"🤖 <b>Агент-воркер готов к работе!</b>\n\n"+
				"📁 Выбранный проект: <code>%s</code>\n"+
				"🧠 Активная модель: <code>%s</code>\n\n"+
				"<b>Задачи:</b>\n"+
				"• /tasks — список задач и быстрое переключение\n"+
				"• /task &lt;id&gt; [текст] — переключить фокус на задачу или дополнить её\n"+
				"• /plan &lt;текст&gt; — составить план и утвердить перед реализацией\n"+
				"• /planmode [on|off] — включить обязательный план для всех задач\n"+
				"• /approve [id] — утвердить план задачи и начать реализацию\n"+
				"• /add &lt;id&gt; &lt;текст&gt; — отправить дополнение конкретной задаче\n"+
				"• /new &lt;текст&gt; — создать новую задачу в текущем проекте\n"+
				"• /status [id] — подробный статус, логи и очередь правок\n"+
				"• /cancel [id] — остановить задачу\n\n"+
				"<b>Система и мониторинг:</b>\n"+
				"• /top (или /ps) — потребление CPU и памяти бота и agy\n"+
				"• /tokens — статистика токенов, скорости и кэша\n"+
				"• /limits — статистика токенов и лимиты\n"+
				"• /models — список моделей и переключение (/model)\n"+
				"• /projects — список проектов и переключение (/use)\n"+
				"• /restart, /rebuild — управление процессом бота\n\n"+
				"💡 <i>Отправьте задачу сообщением в чат. Для предварительного плана используйте /plan &lt;задача&gt;. Дополнения можно отправлять через /add &lt;id&gt; &lt;текст&gt; или ответом на сообщения бота.</i>",
			html.EscapeString(curProj),
			html.EscapeString(curMod),
		)
		return c.Send(msg, tele.ModeHTML)
	})

	b.Handle("/tasks", func(c tele.Context) error {
		msg, menu := FormatTasksList(taskManager)
		if menu != nil {
			return c.Send(msg, menu, tele.ModeHTML)
		}
		return c.Send(msg, tele.ModeHTML)
	})

	btnTaskSel := tele.Btn{Unique: "task_sel"}
	b.Handle(&btnTaskSel, func(c tele.Context) error {
		idStr := strings.TrimSpace(c.Data())
		id, err := strconv.Atoi(idStr)
		if err != nil {
			return c.Respond(&tele.CallbackResponse{Text: "Некорректный номер задачи"})
		}
		task, err := taskManager.SetActiveTask(id)
		if err != nil {
			return c.Respond(&tele.CallbackResponse{Text: err.Error()})
		}
		syncLegacySession(task)
		_ = c.Respond(&tele.CallbackResponse{Text: fmt.Sprintf("Выбрана задача #%d", id)})

		details := FormatTaskDetails(task, true)
		return c.Send(fmt.Sprintf("🎯 <b>Фокус переключен на задачу #%d!</b>\n\n%s", id, details), tele.ModeHTML)
	})

	b.Handle("/status", func(c tele.Context) error {
		args := c.Args()
		var target *TaskSession
		if len(args) > 0 {
			first := strings.TrimPrefix(args[0], "#")
			if id, err := strconv.Atoi(first); err == nil {
				target = taskManager.GetTask(id)
				if target == nil {
					return c.Send(fmt.Sprintf("❌ Задача #%d не найдена. Список задач: /tasks", id), tele.ModeHTML)
				}
			}
		}

		if target == nil {
			target = taskManager.GetActiveTask()
		}

		if target == nil || (!target.IsActive() && len(args) == 0) {
			idleMsg := "💤 <b>Сейчас нет активных задач. Агент простаивает.</b>"
			lastSnippet := tokenTracker.GetLastTaskStatusBlock()
			if lastSnippet != "" {
				idleMsg += "\n\n" + lastSnippet
			}
			report := CollectResourceReport(false)
			resSnippet := FormatCompactResourceSnippet(report)
			if resSnippet != "" {
				idleMsg += "\n\n" + resSnippet
			}
			idleMsg += "\n\n💡 <i>Отправьте задачу сообщением в чат, /tasks для списка или /new для новой задачи.</i>"
			return c.Send(idleMsg, tele.ModeHTML)
		}

		activeTask := taskManager.GetActiveTask()
		isActiveFocus := (activeTask != nil && activeTask.ID == target.ID)
		msg := FormatTaskDetails(target, isActiveFocus)

		tokenBlock := tokenTracker.GetCurrentTaskStatusBlock()
		if tokenBlock != "" {
			msg += "\n\n" + tokenBlock
		}
		report := CollectResourceReport(false)
		resSnippet := FormatCompactResourceSnippet(report)
		if resSnippet != "" {
			msg += "\n\n" + resSnippet
		}

		otherTasks := taskManager.GetActiveOrQueuedTasks()
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

		return c.Send(msg, tele.ModeHTML)
	})

	b.Handle("/tokens", func(c tele.Context) error {
		msg := tokenTracker.GetTokensCommandMessage()
		return c.Send(msg, tele.ModeHTML)
	})

	b.Handle("/stats", func(c tele.Context) error {
		msg := tokenTracker.GetTokensCommandMessage()
		return c.Send(msg, tele.ModeHTML)
	})

	b.Handle("/models", func(c tele.Context) error {
		args := c.Args()
		forceRefresh := len(args) > 0 && (args[0] == "refresh" || args[0] == "update")
		if forceRefresh {
			_ = c.Notify(tele.Typing)
			if _, err := modelRegistry.RefreshModels(true); err != nil {
				return c.Send(fmt.Sprintf("⚠️ Ошибка синхронизации с agy: %v\nПоказан кэшированный список.", err))
			}
		}

		projectState.RLock()
		curModel := projectState.currentModel
		projectState.RUnlock()

		msg := modelRegistry.FormatModelsMessage(curModel)
		return c.Send(msg, tele.ModeHTML)
	})

	b.Handle("/model", func(c tele.Context) error {
		args := c.Args()
		if len(args) == 0 {
			projectState.RLock()
			cur := projectState.currentModel
			projectState.RUnlock()
			return c.Send(fmt.Sprintf("Текущая модель: <code>%s</code>\nИспользование: <code>/model &lt;имя&gt;</code> (например, <code>/model sonnet</code>)", html.EscapeString(cur)), tele.ModeHTML)
		}

		target := strings.TrimSpace(args[0])
		resolved, ok := modelRegistry.ResolveModel(target)
		if !ok {
			return c.Send(fmt.Sprintf("❌ Неизвестная модель: <code>%s</code>. Список: /models", html.EscapeString(target)), tele.ModeHTML)
		}

		projectState.Lock()
		projectState.currentModel = resolved
		projectState.Unlock()

		return c.Send(fmt.Sprintf("✅ Модель переключена на: <code>%s</code>", html.EscapeString(resolved)), tele.ModeHTML)
	})

	b.Handle("/limits", func(c tele.Context) error {
		_ = c.Notify(tele.Typing)
		statusMsg, _ := b.Send(c.Recipient(), "⏳ <i>Запрашиваю актуальные лимиты и квоты из agy...</i>", tele.ModeHTML)

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
			out, err := exec.CommandContext(ctx, "agy", "-p", "/quota", "--output-format", "json").CombinedOutput()
			cleanOut := ansiRegex.ReplaceAllString(string(out), "")
			quotaRaw = strings.TrimSpace(cleanOut)
			if err != nil {
				textOut, textErr := exec.CommandContext(ctx, "agy", "-p", "/quota").CombinedOutput()
				if textErr == nil && len(textOut) > 0 {
					quotaRaw = strings.TrimSpace(ansiRegex.ReplaceAllString(string(textOut), ""))
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
			out, err := exec.CommandContext(ctx, "agy", "-p", "/credits", "--output-format", "json").CombinedOutput()
			cleanOut := ansiRegex.ReplaceAllString(string(out), "")
			if err != nil {
				creditsErr = err
				return
			}
			_ = json.Unmarshal([]byte(cleanOut), &creditsResp)
		}()

		wg.Wait()

		session.Lock()
		lastModel := session.lastModelUsed
		session.Unlock()

		projectState.RLock()
		activeModel := projectState.currentModel
		projectState.RUnlock()

		if lastModel == "" {
			lastModel = activeModel
		}

		lastTokens := tokenTracker.FormatShortLastTask()
		if lastTokens == "" {
			session.Lock()
			lastTokens = session.lastTokensUsed
			session.Unlock()
			if lastTokens == "" {
				lastTokens = "нет данных (запустите хотя бы одну задачу)"
			}
		}

		var bldr strings.Builder
		bldr.WriteString("📊 <b>Лимиты и квоты аккаунта (Google Antigravity)</b>\n\n")

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
			bldr.WriteString("<b>Ответ agy:</b>\n<pre>")
			bldr.WriteString(html.EscapeString(quotaRaw))
			bldr.WriteString("</pre>\n\n")
		} else if quotaErr != nil {
			bldr.WriteString(fmt.Sprintf("⚠️ <i>Не удалось получить актуальные лимиты из agy: %s</i>\n\n", html.EscapeString(quotaErr.Error())))
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
		if statusMsg != nil {
			if _, editErr := b.Edit(statusMsg, resultMsg, tele.ModeHTML); editErr == nil {
				return nil
			}
		}
		return c.Send(resultMsg, tele.ModeHTML)
	})

	b.Handle("/projects", func(c tele.Context) error {
		entries, err := os.ReadDir(projectsRoot)
		if err != nil {
			return c.Send(fmt.Sprintf("❌ Ошибка чтения директории: %v", err))
		}

		projectState.RLock()
		cur := projectState.currentProject
		projectState.RUnlock()

		var bldr strings.Builder
		bldr.WriteString("📁 <b>Доступные проекты:</b>\n\n")

		found := false
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			gitPath := filepath.Join(projectsRoot, e.Name(), ".git")
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
			return c.Send("В каталоге проектов пока нет склонированных репозиториев.")
		}

		return c.Send(bldr.String(), tele.ModeHTML)
	})

	b.Handle("/use", func(c tele.Context) error {
		args := c.Args()
		if len(args) == 0 {
			return c.Send("Укажите имя проекта. Пример: <code>/use my-repo</code>", tele.ModeHTML)
		}

		target := strings.TrimSpace(args[0])
		targetPath := filepath.Join(projectsRoot, target)

		if fi, err := os.Stat(targetPath); err != nil || !fi.IsDir() {
			return c.Send(fmt.Sprintf("❌ Проект <code>%s</code> не найден.", html.EscapeString(target)), tele.ModeHTML)
		}

		projectState.Lock()
		projectState.currentProject = target
		projectState.Unlock()

		return c.Send(fmt.Sprintf("✅ Проект переключен на: <code>%s</code>", html.EscapeString(target)), tele.ModeHTML)
	})

	b.Handle("/task", func(c tele.Context) error {
		args := c.Args()
		if len(args) == 0 {
			active := taskManager.GetActiveTask()
			if active == nil {
				return c.Send("💤 Нет активных задач. Создать: <code>/new &lt;текст&gt;</code>", tele.ModeHTML)
			}
			details := FormatTaskDetails(active, true)
			return c.Send(details, tele.ModeHTML)
		}

		first := strings.TrimPrefix(args[0], "#")
		id, err := strconv.Atoi(first)
		if err != nil {
			if strings.ToLower(first) == "new" && len(args) > 1 {
				prompt := strings.Join(args[1:], " ")
				return handleCreateNewTask(b, c, prompt)
			}
			if strings.ToLower(first) == "plan" && len(args) > 1 {
				prompt := strings.Join(args[1:], " ")
				return handleCreatePlanTask(b, c, prompt)
			}
			return c.Send("Использование:\n• <code>/task &lt;id&gt;</code> — переключить активную задачу\n• <code>/task &lt;id&gt; &lt;текст&gt;</code> — дополнить задачу", tele.ModeHTML)
		}

		// Если передан текст дополнения: /task 2 сделай ещё это
		if len(args) > 1 {
			followupText := strings.TrimSpace(strings.Join(args[1:], " "))
			return handleAddFollowupToTask(b, c, id, followupText)
		}

		task, err := taskManager.SetActiveTask(id)
		if err != nil {
			return c.Send(fmt.Sprintf("❌ %s", err.Error()), tele.ModeHTML)
		}
		syncLegacySession(task)

		details := FormatTaskDetails(task, true)
		return c.Send(fmt.Sprintf("🎯 <b>Фокус переключен на задачу #%d!</b>\n\n%s", id, details), tele.ModeHTML)
	})

	b.Handle("/add", func(c tele.Context) error {
		args := c.Args()
		if len(args) == 0 {
			return c.Send("Использование:\n• <code>/add &lt;id&gt; &lt;текст&gt;</code> — дополнить задачу #id\n• <code>/add &lt;текст&gt;</code> — дополнить активную задачу", tele.ModeHTML)
		}

		first := strings.TrimPrefix(args[0], "#")
		if id, err := strconv.Atoi(first); err == nil && len(args) > 1 {
			followupText := strings.TrimSpace(strings.Join(args[1:], " "))
			return handleAddFollowupToTask(b, c, id, followupText)
		}

		active := taskManager.GetActiveTask()
		if active == nil {
			return c.Send("❌ Нет активной задачи. Укажите ID: <code>/add &lt;id&gt; &lt;текст&gt;</code>", tele.ModeHTML)
		}
		followupText := strings.TrimSpace(strings.Join(args, " "))
		return handleAddFollowupToTask(b, c, active.ID, followupText)
	})

	b.Handle("/new", func(c tele.Context) error {
		args := c.Args()
		if len(args) == 0 {
			return c.Send("Использование: <code>/new &lt;описание задачи&gt;</code>\n(или <code>/new &lt;проект&gt; &lt;описание&gt;</code>)", tele.ModeHTML)
		}
		text := strings.TrimSpace(strings.Join(args, " "))
		return handleCreateNewTask(b, c, text)
	})

	b.Handle("/plan", func(c tele.Context) error {
		args := c.Args()
		if len(args) == 0 {
			projectState.RLock()
			mode := projectState.planMode
			projectState.RUnlock()

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

			menu := &tele.ReplyMarkup{}
			btnToggle := menu.Data("🔄 Переключить Plan Mode", "plan_mode_toggle")
			menu.Inline(menu.Row(btnToggle))
			return c.Send(msg, menu, tele.ModeHTML)
		}
		text := strings.TrimSpace(strings.Join(args, " "))
		return handleCreatePlanTask(b, c, text)
	})

	b.Handle("/planmode", func(c tele.Context) error {
		args := c.Args()
		projectState.Lock()
		if len(args) == 0 {
			projectState.planMode = !projectState.planMode
		} else {
			arg := strings.ToLower(strings.TrimSpace(args[0]))
			switch arg {
			case "on", "enable", "true", "1", "вкл", "да":
				projectState.planMode = true
			case "off", "disable", "false", "0", "выкл", "нет":
				projectState.planMode = false
			case "toggle":
				projectState.planMode = !projectState.planMode
			default:
				projectState.Unlock()
				return c.Send("Использование: <code>/planmode [on|off|toggle]</code>", tele.ModeHTML)
			}
		}
		newMode := projectState.planMode
		projectState.Unlock()

		if newMode {
			return c.Send("✅ <b>Режим обязательного планирования ВКЛЮЧЕН.</b>\nВсе новые задачи будут сначала составлять план и ожидать вашего утверждения.", tele.ModeHTML)
		}
		return c.Send("ℹ️ <b>Режим обязательного планирования ВЫКЛЮЧЕН.</b>\nНовые задачи будут сразу приступать к реализации (для плана используйте <code>/plan &lt;задача&gt;</code>).", tele.ModeHTML)
	})

	b.Handle("/approve", func(c tele.Context) error {
		args := c.Args()
		var targetID int
		if len(args) > 0 {
			first := strings.TrimPrefix(args[0], "#")
			var err error
			targetID, err = strconv.Atoi(first)
			if err != nil {
				return c.Send("Укажите номер задачи. Пример: <code>/approve 1</code>", tele.ModeHTML)
			}
		} else {
			active := taskManager.GetActiveTask()
			if active == nil {
				return c.Send("Нет активных задач для утверждения.")
			}
			targetID = active.ID
		}
		return handleApprovePlan(b, c.Recipient(), targetID)
	})

	b.Handle("/confirm", func(c tele.Context) error {
		args := c.Args()
		var targetID int
		if len(args) > 0 {
			first := strings.TrimPrefix(args[0], "#")
			var err error
			targetID, err = strconv.Atoi(first)
			if err != nil {
				return c.Send("Укажите номер задачи. Пример: <code>/confirm 1</code>", tele.ModeHTML)
			}
		} else {
			active := taskManager.GetActiveTask()
			if active == nil {
				return c.Send("Нет активных задач для утверждения.")
			}
			targetID = active.ID
		}
		return handleApprovePlan(b, c.Recipient(), targetID)
	})

	btnPlanApprove := tele.Btn{Unique: "plan_approve"}
	b.Handle(&btnPlanApprove, func(c tele.Context) error {
		idStr := strings.TrimSpace(c.Data())
		id, err := strconv.Atoi(idStr)
		if err != nil {
			return c.Respond(&tele.CallbackResponse{Text: "Некорректный номер задачи"})
		}
		_ = c.Respond(&tele.CallbackResponse{Text: fmt.Sprintf("План #%d утверждён", id)})
		return handleApprovePlan(b, c.Recipient(), id)
	})

	btnPlanCancel := tele.Btn{Unique: "plan_cancel"}
	b.Handle(&btnPlanCancel, func(c tele.Context) error {
		idStr := strings.TrimSpace(c.Data())
		id, err := strconv.Atoi(idStr)
		if err != nil {
			return c.Respond(&tele.CallbackResponse{Text: "Некорректный номер задачи"})
		}
		_ = c.Respond(&tele.CallbackResponse{Text: fmt.Sprintf("Задача #%d отменена", id)})
		task, err := taskManager.CancelTask(id)
		if err != nil {
			return c.Send(fmt.Sprintf("❌ %s", err.Error()), tele.ModeHTML)
		}
		syncLegacySession(taskManager.GetActiveTask())
		tokenTracker.CancelTask()
		checkAndStartQueuedTask(b, task.Project, projectsRoot)
		return c.Send(fmt.Sprintf("🛑 <b>Задача #%d (<code>%s</code>) остановлена.</b>", id, html.EscapeString(task.Project)), tele.ModeHTML)
	})

	btnPlanModeToggle := tele.Btn{Unique: "plan_mode_toggle"}
	b.Handle(&btnPlanModeToggle, func(c tele.Context) error {
		projectState.Lock()
		projectState.planMode = !projectState.planMode
		newMode := projectState.planMode
		projectState.Unlock()

		if newMode {
			_ = c.Respond(&tele.CallbackResponse{Text: "Режим планирования включен"})
			return c.Send("✅ <b>Режим обязательного планирования ВКЛЮЧЕН.</b>\nВсе новые задачи будут сначала формировать план и ожидать вашего утверждения.", tele.ModeHTML)
		}
		_ = c.Respond(&tele.CallbackResponse{Text: "Режим планирования выключен"})
		return c.Send("ℹ️ <b>Режим обязательного планирования ВЫКЛЮЧЕН.</b>\nДля создания задач с планом используйте <code>/plan &lt;задача&gt;</code>.", tele.ModeHTML)
	})

	b.Handle("/cancel", func(c tele.Context) error {
		args := c.Args()
		var targetID int
		if len(args) > 0 {
			idStr := strings.TrimPrefix(args[0], "#")
			var err error
			targetID, err = strconv.Atoi(idStr)
			if err != nil {
				return c.Send("Укажите номер задачи. Пример: <code>/cancel 2</code>", tele.ModeHTML)
			}
		} else {
			active := taskManager.GetActiveTask()
			if active == nil || !active.IsActive() {
				return c.Send("Сейчас нет активных задач.")
			}
			targetID = active.ID
		}

		task, err := taskManager.CancelTask(targetID)
		if err != nil {
			return c.Send(fmt.Sprintf("❌ %s", err.Error()), tele.ModeHTML)
		}

		syncLegacySession(taskManager.GetActiveTask())
		tokenTracker.CancelTask()

		checkAndStartQueuedTask(b, task.Project, projectsRoot)

		return c.Send(fmt.Sprintf("🛑 Задача <b>#%d</b> (<code>%s</code>) остановлена.", task.ID, html.EscapeString(task.Project)), tele.ModeHTML)
	})

	b.Handle("/restart", func(c tele.Context) error {
		return handleRestart(b, c)
	})

	b.Handle("/rebuild", func(c tele.Context) error {
		return handleRebuild(b, c)
	})

	b.Handle("/build", func(c tele.Context) error {
		return handleRebuild(b, c)
	})

	handleResources := func(c tele.Context) error {
		_ = c.Notify(tele.Typing)
		report := CollectResourceReport(true)
		msg := FormatResourcesMessage(report)
		return c.Send(msg, tele.ModeHTML)
	}

	b.Handle("/top", handleResources)
	b.Handle("/ps", handleResources)
	b.Handle("/resources", handleResources)
	b.Handle("/res", handleResources)

	b.Handle(tele.OnText, func(c tele.Context) error {
		userText := strings.TrimSpace(c.Text())
		if strings.HasPrefix(userText, "/") {
			return nil
		}

		// 1. Проверяем, является ли сообщение ответом (Reply) на статус/вопрос конкретной задачи
		if c.Message().ReplyTo != nil {
			repliedMsgID := c.Message().ReplyTo.ID
			if task := taskManager.GetTaskByMessageID(repliedMsgID); task != nil {
				return handleAddFollowupToTask(b, c, task.ID, userText)
			}
		}

		// 2. Проверяем активную задачу в фокусе
		active := taskManager.GetActiveTask()
		if active != nil && active.IsActive() {
			return handleAddFollowupToTask(b, c, active.ID, userText)
		}

		// 3. Нет активных задач — запускаем новую задачу
		return handleCreateNewTask(b, c, userText)
	})

	go checkAndNotifyRestart(b, adminID)

	log.Println("Мультипроектный агент-бот запущен...")
	b.Start()
}

func initDefaultProject(root string) {
	defaultProject := os.Getenv("DEFAULT_PROJECT")
	if defaultProject == "" {
		defaultProject = "tg-bot-agent"
	}

	targetDir := filepath.Join(root, defaultProject)
	if info, err := os.Stat(targetDir); err == nil && info.IsDir() {
		projectState.Lock()
		projectState.currentProject = defaultProject
		projectState.Unlock()
		log.Printf("Инициализирован проект по умолчанию: %s", defaultProject)
		return
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		log.Printf("Предупреждение: не удалось прочитать директорию проектов %s: %v", root, err)
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			projectState.Lock()
			projectState.currentProject = e.Name()
			projectState.Unlock()
			log.Printf("Инициализирован первый найденный проект: %s", e.Name())
			break
		}
	}
}


func runAgentPipeline(b *tele.Bot, recipient tele.Recipient, workDir, projectName, initialPrompt string) {
	projectState.RLock()
	modelName := projectState.currentModel
	projectState.RUnlock()

	task := taskManager.CreateTask(projectName, modelName, initialPrompt, recipient)
	task.Lock()
	task.Status = TaskStatusRunning
	task.StartedAt = time.Now()
	task.Unlock()

	syncLegacySession(task)
	runAgentTaskPipeline(b, recipient, task, workDir)
}

func runAgentTaskPipeline(b *tele.Bot, recipient tele.Recipient, task *TaskSession, workDir string) {
	projectName := task.Project
	taskID := task.ID

	// ЭТАП 1: Планирование (если требуется и ещё не утверждён)
	task.Lock()
	needsPlanning := task.RequiresPlan && !task.PlanApproved
	task.Unlock()

	if needsPlanning {
		task.Lock()
		if task.Status == TaskStatusCancelled {
			task.Unlock()
			return
		}
		task.Status = TaskStatusPlanning
		activeModel := task.Model
		existingPlan := task.Plan
		curPrompt := task.CurrentPrompt
		task.Unlock()

		syncLegacySession(task)

		var planningPrompt string
		if existingPlan == "" {
			planningPrompt = fmt.Sprintf(
				"Задача пользователя: %s\n\n"+
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
				task.InitialPrompt,
			)
		} else {
			feedback := curPrompt
			if feedback == "" {
				feedback = task.InitialPrompt
			}
			planningPrompt = fmt.Sprintf(
				"Задача пользователя: %s\n\n"+
					"ПРЕДЫДУЩИЙ ПЛАН РЕАЛИЗАЦИИ:\n%s\n\n"+
					"ЗАМЕЧАНИЯ И ДОПОЛНЕНИЯ ПОЛЬЗОВАТЕЛЯ К ПЛАНУ:\n%s\n\n"+
					"ВНИМАНИЕ: Это этап планирования. НЕ вноси изменения в файлы проекта, НЕ делай commit и НЕ создавай PR.\n"+
					"Обнови и скорректируй план реализации с учётом всех замечаний пользователя и выведи обновлённый план.",
				task.InitialPrompt, existingPlan, feedback,
			)
		}

		executeStepForTask(b, recipient, task, workDir, planningPrompt, activeModel)

		task.Lock()
		if task.Status == TaskStatusCancelled {
			task.Unlock()
			checkAndStartQueuedTask(b, projectName, projectsRoot)
			return
		}

		planText := strings.TrimSpace(task.FullOutput.String())
		if planText == "" {
			planText = "Агент не сформировал подробный план. Вы можете дополнить задачу замечаниями или утвердить её."
		}
		task.Plan = planText
		task.Status = TaskStatusWaitingApproval
		task.FullOutput.Reset()
		task.RecentLogs = nil
		task.Unlock()

		syncLegacySession(task)

		sendPlanForApproval(b, recipient, task)
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
		if task.Status == TaskStatusCancelled {
			task.Unlock()
			return
		}
		activeModel := task.Model
		task.CurrentPrompt = currentPrompt
		task.Unlock()

		syncLegacySession(task)

		executeStepForTask(b, recipient, task, workDir, currentPrompt, activeModel)

		task.Lock()
		if task.Status == TaskStatusCancelled {
			task.Unlock()
			checkAndStartQueuedTask(b, projectName, projectsRoot)
			return
		}

		if len(task.PendingFollowups) == 0 {
			prURL := task.LastPRURL
			finalReport := task.FullOutput.String()
			task.Status = TaskStatusCompleted
			task.FinishedAt = time.Now()
			task.Unlock()

			syncLegacySession(task)

			metrics := tokenTracker.FinishTask(prURL)
			statsSummary := metrics.FormatCompletionSummary()

			var compMsg *tele.Message
			if prURL != "" {
				compMsg, _ = b.Send(recipient, fmt.Sprintf("🎉 <b>Задача #%d выполнена!</b>\n📁 Проект: <code>%s</code>\n🔗 <a href=\"%s\">Открыть Pull Request</a>\n\n%s", taskID, html.EscapeString(projectName), html.EscapeString(prURL), statsSummary), tele.ModeHTML)
			} else {
				compMsg, _ = b.Send(recipient, fmt.Sprintf("✅ <b>Задача #%d завершена!</b> (<code>%s</code>)\n\n%s", taskID, html.EscapeString(projectName), statsSummary), tele.ModeHTML)
			}
			if compMsg != nil {
				taskManager.RegisterMessageTask(compMsg.ID, taskID)
			}

			if strings.TrimSpace(finalReport) != "" {
				sendLongMarkdown(b, recipient, finalReport)
			}

			// Запускаем следующую задачу из очереди для этого проекта, если есть
			checkAndStartQueuedTask(b, projectName, projectsRoot)
			return
		}

		followups := task.PendingFollowups
		task.PendingFollowups = nil

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

		tokenTracker.StartNextStep(activeModel)

		b.Send(recipient, fmt.Sprintf("🔄 <b>Задача #%d: Беру в работу дополнения (%d шт.)...</b>", taskID, len(followups)), tele.ModeHTML)
	}
}

func executeStepForTask(b *tele.Bot, recipient tele.Recipient, task *TaskSession, workDir, prompt, modelName string) {
	projectName := task.Project
	taskID := task.ID

	task.Lock()
	isPlanning := task.Status == TaskStatusPlanning
	task.Unlock()

	var statusMsgText string
	if isPlanning {
		statusMsgText = fmt.Sprintf("📝 <b>Составление плана задачи #%d:</b> <code>%s</code> [<code>%s</code>]\n<i>Исследование репозитория и формирование плана...</i>", taskID, html.EscapeString(projectName), html.EscapeString(modelName))
	} else {
		statusMsgText = fmt.Sprintf("🚀 <b>Шаг задачи #%d в работе:</b> <code>%s</code> [<code>%s</code>]\n<i>Инициализация сессии агента...</i>", taskID, html.EscapeString(projectName), html.EscapeString(modelName))
	}

	statusMsg, _ := b.Send(recipient, statusMsgText, tele.ModeHTML)
	if statusMsg != nil {
		taskManager.RegisterMessageTask(statusMsg.ID, taskID)
	}

	task.Lock()
	task.LastModelUsed = modelName
	task.LiveMsg = statusMsg
	task.Unlock()

	syncLegacySession(task)

	args := []string{
		"--dangerously-skip-permissions",
		"--print-timeout", "30m",
		"--output-format", "stream-json",
	}
	args = append(args, buildAgyModelArgs(modelName)...)
	args = append(args, "-p", prompt)
	cmd := exec.Command("agy", args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"TERM=dumb",
		"NO_COLOR=1",
		"CI=true",
	)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		b.Send(recipient, fmt.Sprintf("❌ Ошибка запуска PTY для задачи #%d: %v", taskID, err))
		task.Lock()
		task.Status = TaskStatusFailed
		task.Unlock()
		syncLegacySession(task)
		return
	}
	defer func() { _ = ptmx.Close() }()

	task.Lock()
	task.Cmd = cmd
	task.Stdin = ptmx
	task.Unlock()
	syncLegacySession(task)

	scanner := bufio.NewScanner(ptmx)
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
				if task.Status != TaskStatusRunning && task.Status != TaskStatusWaitingInput && task.Status != TaskStatusPlanning {
					task.Unlock()
					return
				}
				var lastLine string
				if len(task.RecentLogs) > 0 {
					lastLine = task.RecentLogs[len(task.RecentLogs)-1]
				}
				dur := task.Duration()
				followupsCount := len(task.PendingFollowups)
				taskStatus := task.Status
				task.Unlock()

				tokenSnippet := tokenTracker.GetLiveStatusSnippet()

				if statusMsg != nil && (lastLine != "" || tokenSnippet != "") {
					queueInfo := ""
					if followupsCount > 0 {
						queueInfo = fmt.Sprintf(" | Правок в очереди: %d", followupsCount)
					}
					statusPrefix := "⏳ <b>Задача"
					if taskStatus == TaskStatusPlanning {
						statusPrefix = "📝 <b>Планирование задачи"
					}
					var bldr strings.Builder
					bldr.WriteString(fmt.Sprintf(
						"%s #%d:</b> <code>%s</code> [<code>%s</code>] (<code>%s</code>%s)\n\n",
						statusPrefix,
						taskID,
						html.EscapeString(projectName),
						html.EscapeString(modelName),
						formatDurationHuman(dur),
						queueInfo,
					))
					if lastLine != "" {
						bldr.WriteString(fmt.Sprintf("📍 <b>Действие:</b>\n<code>%s</code>\n\n", html.EscapeString(truncateString(lastLine, 80))))
					}
					if tokenSnippet != "" {
						bldr.WriteString(tokenSnippet + "\n\n")
					}
					bldr.WriteString(fmt.Sprintf("<i>(Лог: /status %d | Дополнить: /add %d | Стоп: /cancel %d)</i>", taskID, taskID, taskID))

					_, _ = b.Edit(statusMsg, bldr.String(), tele.ModeHTML)
				}
			}
		}
	}()

	go func() {
		for scanner.Scan() {
			rawLine := scanner.Text()
			cleanLine := ansiRegex.ReplaceAllString(rawLine, "")
			cleanLine = strings.TrimSpace(cleanLine)
			if cleanLine == "" {
				continue
			}

			evt, err := ParseStreamEvent(cleanLine)
			if err == nil && evt != nil {
				if evt.StepUpdate != nil {
					u := evt.StepUpdate
					if u.Usage != nil {
						tokenTracker.RecordStepUsage(u.StepIndex, *u.Usage)
					}
					if u.StepType == "tool" && u.State == "ACTIVE" {
						desc := formatToolAction(u.ToolName, u.ToolInfo)
						task.AppendLog(desc)
					} else if u.StepType == "agent_response" && u.TextDelta != "" {
						task.Lock()
						task.FullOutput.WriteString(u.TextDelta)
						if matches := prUrlRegexp.FindStringSubmatch(u.TextDelta); len(matches) > 1 {
							task.LastPRURL = matches[1]
						}
						task.Unlock()
					}

					// Проверка на вопрос пользователю
					if u.ToolName == "ask_question" ||
						(u.StepType == "agent_response" && u.State == "DONE" && isQuestionText(u.TextDelta)) {
						task.Lock()
						task.Status = TaskStatusWaitingInput
						task.Unlock()
						syncLegacySession(task)

						questionText := u.TextDelta
						if u.ToolName == "ask_question" && u.ToolInfo != nil && u.ToolInfo.Parameters != nil {
							questionText = fmt.Sprintf("%v", u.ToolInfo.Parameters)
						}
						qMsg, _ := b.Send(recipient, fmt.Sprintf("❓ <b>Вопрос по задаче #%d (<code>%s</code>):</b>\n\n%s\n\n<i>Ответьте сообщением в чат или <code>/add %d &lt;ответ&gt;</code>.</i>",
							taskID, html.EscapeString(projectName), html.EscapeString(questionText), taskID), tele.ModeHTML)
						if qMsg != nil {
							taskManager.RegisterMessageTask(qMsg.ID, taskID)
						}
					}
				}

				if evt.Result != nil {
					res := evt.Result
					if res.Usage != nil {
						tokenTracker.RecordResultUsage(*res.Usage, res.DurationSeconds, res.NumTurns)
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

			if isQuestionText(cleanLine) {
				task.Lock()
				task.Status = TaskStatusWaitingInput
				task.Unlock()
				syncLegacySession(task)

				qMsg, _ := b.Send(recipient, fmt.Sprintf("❓ <b>Вопрос по задаче #%d (<code>%s</code>):</b>\n\n%s\n\n<i>Ответьте сообщением в чат или <code>/add %d &lt;ответ&gt;</code>.</i>",
					taskID, html.EscapeString(projectName), html.EscapeString(cleanLine), taskID), tele.ModeHTML)
				if qMsg != nil {
					taskManager.RegisterMessageTask(qMsg.ID, taskID)
				}
			}
		}
		close(done)
	}()

	<-done
	close(stopLiveUpdate)
	_ = cmd.Wait()

	task.Lock()
	if task.Stdin != nil {
		_ = task.Stdin.Close()
		task.Stdin = nil
	}
	if task.Status == TaskStatusWaitingInput {
		task.Status = TaskStatusRunning
	}
	task.Unlock()
	syncLegacySession(task)
}

func handleCreateNewTask(b *tele.Bot, c tele.Context, text string) error {
	projectState.RLock()
	requiresPlan := projectState.planMode
	projectState.RUnlock()
	return handleCreateNewTaskWithOptions(b, c, text, requiresPlan)
}

func handleCreatePlanTask(b *tele.Bot, c tele.Context, text string) error {
	return handleCreateNewTaskWithOptions(b, c, text, true)
}

func handleCreateNewTaskWithOptions(b *tele.Bot, c tele.Context, text string, requiresPlan bool) error {
	projectState.RLock()
	curProj := projectState.currentProject
	curMod := projectState.currentModel
	projectState.RUnlock()

	words := strings.Fields(text)
	if len(words) > 1 {
		possibleProj := words[0]
		possiblePath := filepath.Join(projectsRoot, possibleProj)
		if fi, err := os.Stat(possiblePath); err == nil && fi.IsDir() {
			curProj = possibleProj
			text = strings.TrimSpace(strings.TrimPrefix(text, possibleProj))
		}
	}

	if curProj == "" {
		return c.Send("❌ Сначала выберите проект: /projects")
	}

	task := taskManager.CreateTaskWithPlan(curProj, curMod, text, c.Recipient(), requiresPlan)
	syncLegacySession(task)

	if taskManager.HasRunningTaskInProject(curProj) {
		planNote := ""
		if requiresPlan {
			planNote = " Сначала будет составлен подробный план."
		}
		msg := fmt.Sprintf(
			"⏳ <b>Задача #%d поставлена в очередь проекта</b> <code>%s</code>:\n\n"+
				"<i>«%s»</i>\n\n"+
				"💡 В этом проекте уже выполняется задача. Задача #%d начнется автоматически после ее завершения.%s",
			task.ID, html.EscapeString(curProj), html.EscapeString(text), task.ID, planNote,
		)
		return c.Send(msg, tele.ModeHTML)
	}

	task.Lock()
	if requiresPlan {
		task.Status = TaskStatusPlanning
	} else {
		task.Status = TaskStatusRunning
	}
	task.StartedAt = time.Now()
	task.Unlock()
	syncLegacySession(task)

	tokenTracker.StartTask(curProj, curMod, text)
	workDir := filepath.Join(projectsRoot, curProj)

	go runAgentTaskPipeline(b, c.Recipient(), task, workDir)

	return nil
}

func handleAddFollowupToTask(b *tele.Bot, c tele.Context, taskID int, text string) error {
	task := taskManager.GetTask(taskID)
	if task == nil {
		return c.Send(fmt.Sprintf("❌ Задача #%d не найдена.", taskID), tele.ModeHTML)
	}

	task.Lock()
	status := task.Status
	task.Unlock()

	if status == TaskStatusWaitingApproval {
		if isConfirmationText(text) {
			return handleApprovePlan(b, c.Recipient(), taskID)
		}
		return handleRevisePlan(b, c.Recipient(), taskID, text)
	}

	task, qLen, isAnswer, err := taskManager.AddFollowup(taskID, text)
	if err != nil {
		return c.Send(fmt.Sprintf("❌ Не удалось отправить дополнение к задаче #%d: %s", taskID, err.Error()), tele.ModeHTML)
	}

	syncLegacySession(task)

	if isAnswer {
		return c.Send(fmt.Sprintf("💬 <b>Ответ передан задаче #%d</b> (<code>%s</code>)...", taskID, html.EscapeString(task.Project)), tele.ModeHTML)
	}

	msg := fmt.Sprintf(
		"📥 <b>Дополнение сохранено в задачу #%d</b> (<code>%s</code>) [#%d в очереди]:\n\n"+
			"<i>«%s»</i>\n\n"+
			"Агент завершит текущий шаг и применит эти правки в ветку задачи #%d.",
		taskID, html.EscapeString(task.Project), qLen, html.EscapeString(text), taskID,
	)
	return c.Send(msg, tele.ModeHTML)
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

func handleApprovePlan(b *tele.Bot, recipient tele.Recipient, taskID int) error {
	task := taskManager.GetTask(taskID)
	if task == nil {
		_, err := b.Send(recipient, fmt.Sprintf("❌ Задача #%d не найдена.", taskID))
		return err
	}

	task.Lock()
	if task.Status != TaskStatusWaitingApproval {
		statusTitle := task.Status.RussianTitle()
		task.Unlock()
		_, err := b.Send(recipient, fmt.Sprintf("ℹ️ Задача #%d не ожидает утверждения плана (текущий статус: %s).", taskID, statusTitle))
		return err
	}

	task.PlanApproved = true
	task.Status = TaskStatusRunning
	task.StartedAt = time.Now()
	task.RecentLogs = nil
	task.PendingFollowups = nil

	projectName := task.Project
	modelName := task.Model
	initialPrompt := task.InitialPrompt
	planText := task.Plan

	implPrompt := fmt.Sprintf(
		"Задача пользователя: %s\n\n"+
			"УТВЕРЖДЁННЫЙ ПЛАН РЕАЛИЗАЦИИ:\n%s\n\n"+
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
	)

	task.CurrentPrompt = implPrompt
	task.Unlock()

	syncLegacySession(task)

	b.Send(recipient, fmt.Sprintf("🚀 <b>План задачи #%d утверждён!</b>\nПриступаю к автономной реализации в <code>%s</code>...", taskID, html.EscapeString(projectName)), tele.ModeHTML)

	tokenTracker.StartTask(projectName, modelName, initialPrompt)
	workDir := filepath.Join(projectsRoot, projectName)
	go runAgentTaskPipeline(b, recipient, task, workDir)

	return nil
}

func handleRevisePlan(b *tele.Bot, recipient tele.Recipient, taskID int, feedback string) error {
	task := taskManager.GetTask(taskID)
	if task == nil {
		return fmt.Errorf("задача #%d не найдена", taskID)
	}

	task.Lock()
	task.Status = TaskStatusPlanning
	task.CurrentPrompt = feedback
	task.StartedAt = time.Now()
	task.RecentLogs = nil
	projectName := task.Project
	task.Unlock()

	syncLegacySession(task)

	b.Send(recipient, fmt.Sprintf("📝 <b>Задача #%d: Обновляю план с учётом замечаний...</b>\n<i>«%s»</i>", taskID, html.EscapeString(truncateString(feedback, 100))), tele.ModeHTML)

	workDir := filepath.Join(projectsRoot, projectName)
	go runAgentTaskPipeline(b, recipient, task, workDir)

	return nil
}

func sendPlanForApproval(b *tele.Bot, recipient tele.Recipient, task *TaskSession) {
	task.Lock()
	taskID := task.ID
	projectName := task.Project
	planText := task.Plan
	task.Unlock()

	header := fmt.Sprintf("📋 <b>План реализации задачи #%d</b> (<code>%s</code>):\n", taskID, html.EscapeString(projectName))
	b.Send(recipient, header, tele.ModeHTML)

	if strings.TrimSpace(planText) != "" {
		sendLongMarkdown(b, recipient, planText)
	}

	planMenu := &tele.ReplyMarkup{}
	btnApprove := planMenu.Data("✅ Утвердить и начать", "plan_approve", strconv.Itoa(taskID))
	btnCancel := planMenu.Data("❌ Отменить", "plan_cancel", strconv.Itoa(taskID))
	planMenu.Inline(planMenu.Row(btnApprove, btnCancel))

	footer := fmt.Sprintf(
		"👆 <b>План задачи #%d ожидает вашего утверждения:</b>\n\n"+
			"• Нажмите <b>«✅ Утвердить и начать»</b> или введите <code>/approve %d</code>, чтобы начать автономную реализацию.\n"+
			"• Чтобы внести правки, ответьте (Reply) на это сообщение или введите <code>/add %d &lt;замечания&gt;</code>.\n"+
			"• Для отмены нажмите <b>«❌ Отменить»</b> или <code>/cancel %d</code>.",
		taskID, taskID, taskID, taskID,
	)

	ctlMsg, _ := b.Send(recipient, footer, planMenu, tele.ModeHTML)
	if ctlMsg != nil {
		taskManager.RegisterMessageTask(ctlMsg.ID, taskID)
	}
}

func checkAndStartQueuedTask(b *tele.Bot, project, root string) {
	nextTask := taskManager.GetNextQueuedTaskForProject(project)
	if nextTask == nil {
		return
	}

	nextTask.Lock()
	isPlanning := nextTask.RequiresPlan && !nextTask.PlanApproved
	if isPlanning {
		nextTask.Status = TaskStatusPlanning
	} else {
		nextTask.Status = TaskStatusRunning
	}
	nextTask.StartedAt = time.Now()
	recipient := nextTask.Recipient
	nextID := nextTask.ID
	prompt := nextTask.InitialPrompt
	model := nextTask.Model
	nextTask.Unlock()

	syncLegacySession(nextTask)

	if isPlanning {
		b.Send(recipient, fmt.Sprintf("📝 <b>Запуск планирования задачи #%d из очереди:</b> <code>%s</code>\n<i>«%s»</i>",
			nextID, html.EscapeString(project), html.EscapeString(truncateString(prompt, 80))), tele.ModeHTML)
	} else {
		b.Send(recipient, fmt.Sprintf("🚀 <b>Запуск задачи #%d из очереди:</b> <code>%s</code>\n<i>«%s»</i>",
			nextID, html.EscapeString(project), html.EscapeString(truncateString(prompt, 80))), tele.ModeHTML)
	}

	tokenTracker.StartTask(project, model, prompt)
	workDir := filepath.Join(root, project)
	go runAgentTaskPipeline(b, recipient, nextTask, workDir)
}

func syncLegacySession(task *TaskSession) {
	if task == nil {
		session.Lock()
		session.isRunning = false
		session.waiting = false
		session.cmd = nil
		session.stdin = nil
		session.Unlock()
		return
	}

	task.Lock()
	defer task.Unlock()

	session.Lock()
	defer session.Unlock()

	session.isRunning = (task.Status == TaskStatusRunning || task.Status == TaskStatusWaitingInput || task.Status == TaskStatusPlanning)
	session.waiting = (task.Status == TaskStatusWaitingInput || task.Status == TaskStatusWaitingApproval)
	session.startedAt = task.StartedAt
	session.currentPrompt = task.InitialPrompt
	session.currentProject = task.Project
	session.recentLogs = append([]string(nil), task.RecentLogs...)
	session.pendingFollowups = append([]string(nil), task.PendingFollowups...)
	session.lastPRURL = task.LastPRURL
	session.lastModelUsed = task.LastModelUsed
	session.lastTokensUsed = task.LastTokensUsed
	session.cmd = task.Cmd
	session.stdin = task.Stdin
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

func sendLongMarkdown(b *tele.Bot, recipient tele.Recipient, text string) {
	const maxChunkSize = 3900
	text = strings.TrimSpace(text)

	if len(text) <= maxChunkSize {
		_, err := b.Send(recipient, text, tele.ModeMarkdown)
		if err != nil {
			b.Send(recipient, text)
		}
		return
	}

	for len(text) > 0 {
		chunkSize := maxChunkSize
		if len(text) < chunkSize {
			chunkSize = len(text)
		} else {
			if lastNL := strings.LastIndex(text[:chunkSize], "\n"); lastNL > 1000 {
				chunkSize = lastNL
			}
		}

		chunk := strings.TrimSpace(text[:chunkSize])
		text = strings.TrimSpace(text[chunkSize:])

		if chunk != "" {
			_, err := b.Send(recipient, chunk, tele.ModeMarkdown)
			if err != nil {
				b.Send(recipient, chunk)
			}
		}
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