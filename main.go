package main

import (
	"bufio"
	"fmt"
	"html"
	"io"
	"log"
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
}

var (
	session      AgentSession
	projectState ProjectState
	adminID      int64
	prUrlRegexp  = regexp.MustCompile(`PR_URL:\s*(https?://[^\s]+)`)
	ansiRegex    = regexp.MustCompile(`\x1b(\[[0-9;?><=$]*[a-zA-Z~]|\][0-9;]*\x07|\([B0-9])|\r`)
	tokensRegexp = regexp.MustCompile(`(?i)tokens?:\s*([0-9,kKmM\s/]+)`)
	modelRegexp  = regexp.MustCompile(`(?i)model:\s*([a-zA-Z0-9.\-_]+)`)

	availableModels = map[string]string{
		"gemini-3.8-flash":  "⚡ По умолчанию: максимальная скорость и свежая база",
		"gemini-3.7-flash":  "⚡ Предыдущая быстрая версия",
		"gemini-3.6-flash":  "⚡ Базовая быстрая модель",
		"gemini-3.1-pro":    "🧠 Флагман: глубокий рефакторинг, архитектура, сложные алгоритмы",
		"claude-sonnet-4.6": "🎯 Claude Sonnet 4.6 (Thinking): сильный агентный кодинг с пошаговым рассуждением",
		"claude-opus-4.6":   "👑 Claude Opus 4.6 (Thinking): максимальный уровень рассуждений для сложных багов",
		"gpt-oss-120b":      "🌐 GPT-OSS 120B (Medium): открытая весовая архитектура",
	}
)

func main() {
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	adminIDStr := os.Getenv("TELEGRAM_ADMIN_ID")
	projectsRoot := os.Getenv("PROJECTS_ROOT")
	if projectsRoot == "" {
		projectsRoot = "/home/deploy/projects"
	}

	var err error
	adminID, err = strconv.ParseInt(adminIDStr, 10, 64)
	if err != nil || adminID == 0 {
		log.Fatal("Некорректный или отсутствующий TELEGRAM_ADMIN_ID")
	}

	initDefaultProject(projectsRoot)
	projectState.Lock()
	projectState.currentModel = "gemini-3.8-flash"
	projectState.Unlock()

	b, err := tele.NewBot(tele.Settings{
		Token:  botToken,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		log.Fatal(err)
	}

	commands := []tele.Command{
		{Text: "status", Description: "Статус задачи, лог и очередь"},
		{Text: "limits", Description: "Остаток квот и лимиты моделей"},
		{Text: "models", Description: "Список доступных моделей"},
		{Text: "model", Description: "Выбрать модель: /model <имя>"},
		{Text: "projects", Description: "Список доступных проектов"},
		{Text: "use", Description: "Переключить проект: /use <имя>"},
		{Text: "cancel", Description: "Принудительно остановить процесс"},
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
				"📁 Текущий проект: <code>%s</code>\n"+
				"🧠 Активная модель: <code>%s</code>\n\n"+
				"<b>Команды:</b>\n"+
				"• /status — статус, лог и очередь уточнений\n"+
				"• /limits — статистика токенов и лимиты\n"+
				"• /models — список моделей и переключение\n"+
				"• /model &lt;имя&gt; — переключить модель\n"+
				"• /projects — список проектов\n"+
				"• /use &lt;имя&gt; — переключить проект\n"+
				"• /cancel — остановить задачу и сбросить очередь\n\n"+
				"Отправьте задачу сообщением в чат. Дополнения можно отправлять прямо в процессе выполнения.",
			html.EscapeString(curProj),
			html.EscapeString(curMod),
		)
		return c.Send(msg, tele.ModeHTML)
	})

	b.Handle("/status", func(c tele.Context) error {
		session.Lock()
		running := session.isRunning
		waiting := session.waiting
		prompt := session.currentPrompt
		project := session.currentProject
		started := session.startedAt
		logs := append([]string(nil), session.recentLogs...)
		followups := append([]string(nil), session.pendingFollowups...)
		session.Unlock()

		if !running {
			return c.Send("💤 Сейчас нет активных задач. Агент простаивает.")
		}

		duration := time.Since(started).Round(time.Second)
		stateStr := "⚙️ Выполняется"
		if waiting {
			stateStr = "❓ Ждёт вашего ответа"
		}

		rawTail := strings.Join(logs, "\n")
		if len(rawTail) > 1800 {
			rawTail = rawTail[len(rawTail)-1800:]
		}
		if strings.TrimSpace(rawTail) == "" {
			rawTail = "Инициализация шага или вывод пока формируется..."
		}

		var followupsSection string
		if len(followups) > 0 {
			followupsSection = fmt.Sprintf("\n📥 <b>В очереди доработок (%d):</b>\n", len(followups))
			for i, item := range followups {
				followupsSection += fmt.Sprintf("%d. <i>«%s»</i>\n", i+1, html.EscapeString(item))
			}
		}

		msg := fmt.Sprintf(
			"📊 <b>Статус задачи:</b>\n"+
				"• Проект: <code>%s</code>\n"+
				"• Состояние: <b>%s</b>\n"+
				"• Время текущего шага: <code>%s</code>\n"+
				"• Задача: <i>%s</i>%s\n\n"+
				"📜 <b>Лог выполнения:</b>\n<pre>%s</pre>",
			html.EscapeString(project),
			html.EscapeString(stateStr),
			duration,
			html.EscapeString(prompt),
			followupsSection,
			html.EscapeString(rawTail),
		)

		return c.Send(msg, tele.ModeHTML)
	})

	b.Handle("/models", func(c tele.Context) error {
		projectState.RLock()
		curModel := projectState.currentModel
		projectState.RUnlock()

		var bldr strings.Builder
		bldr.WriteString("🤖 <b>Доступные модели:</b>\n\n")

		for m, desc := range availableModels {
			if m == curModel {
				bldr.WriteString(fmt.Sprintf("👉 <b>%s</b> <i>(активна)</i>\n%s\n\n", m, desc))
			} else {
				bldr.WriteString(fmt.Sprintf("• <code>%s</code>\n%s\n<i>Переключить:</i> <code>/model %s</code>\n\n", m, desc, m))
			}
		}

		bldr.WriteString("💡 <i>Короткие алиасы:</i>\n")
		bldr.WriteString("• <code>/model flash</code> — Gemini 3.8 Flash\n")
		bldr.WriteString("• <code>/model pro</code> — Gemini 3.1 Pro\n")
		bldr.WriteString("• <code>/model sonnet</code> — Claude Sonnet 4.6 Thinking\n")
		bldr.WriteString("• <code>/model opus</code> — Claude Opus 4.6 Thinking\n")
		bldr.WriteString("• <code>/model oss</code> — GPT-OSS 120B")

		return c.Send(bldr.String(), tele.ModeHTML)
	})

	b.Handle("/model", func(c tele.Context) error {
		args := c.Args()
		if len(args) == 0 {
			projectState.RLock()
			cur := projectState.currentModel
			projectState.RUnlock()
			return c.Send(fmt.Sprintf("Текущая модель: <code>%s</code>\nИспользование: <code>/model &lt;имя&gt;</code> (например, <code>/model sonnet</code>)", cur), tele.ModeHTML)
		}

		target := strings.ToLower(strings.TrimSpace(args[0]))

		switch target {
		case "flash", "3.8", "3.8-flash":
			target = "gemini-3.8-flash"
		case "3.7", "3.7-flash":
			target = "gemini-3.7-flash"
		case "3.6", "3.6-flash":
			target = "gemini-3.6-flash"
		case "pro", "3.1", "3.1-pro":
			target = "gemini-3.1-pro"
		case "sonnet", "claude-sonnet", "sonnet-thinking":
			target = "claude-sonnet-4.6"
		case "opus", "claude-opus", "opus-thinking":
			target = "claude-opus-4.6"
		case "oss", "gpt-oss", "120b":
			target = "gpt-oss-120b"
		}

		if _, exists := availableModels[target]; !exists {
			return c.Send(fmt.Sprintf("❌ Неизвестная модель: <code>%s</code>. Список: /models", html.EscapeString(target)), tele.ModeHTML)
		}

		projectState.Lock()
		projectState.currentModel = target
		projectState.Unlock()

		return c.Send(fmt.Sprintf("✅ Модель переключена на: <code>%s</code>", target), tele.ModeHTML)
	})

	b.Handle("/limits", func(c tele.Context) error {
		out, err := exec.Command("agy", "quota").CombinedOutput()
		cliQuotaOutput := ""
		if err == nil && len(out) > 0 {
			cleanOut := ansiRegex.ReplaceAllString(string(out), "")
			cliQuotaOutput = strings.TrimSpace(cleanOut)
		}

		session.Lock()
		lastModel := session.lastModelUsed
		lastTokens := session.lastTokensUsed
		session.Unlock()

		projectState.RLock()
		activeModel := projectState.currentModel
		projectState.RUnlock()

		if lastModel == "" {
			lastModel = activeModel
		}
		if lastTokens == "" {
			lastTokens = "нет данных (запустите хотя бы одну задачу)"
		}

		var bldr strings.Builder
		bldr.WriteString("📊 <b>Лимиты и квоты аккаунта</b>\n\n")

		if cliQuotaOutput != "" {
			bldr.WriteString("<b>Ответ CLI:</b>\n<pre>")
			bldr.WriteString(html.EscapeString(cliQuotaOutput))
			bldr.WriteString("</pre>\n\n")
		}

		bldr.WriteString("📈 <b>Статистика последней операции:</b>\n")
		bldr.WriteString(fmt.Sprintf("• Выбранная модель: <code>%s</code>\n", html.EscapeString(activeModel)))
		bldr.WriteString(fmt.Sprintf("• Модель в сессии: <code>%s</code>\n", html.EscapeString(lastModel)))
		bldr.WriteString(fmt.Sprintf("• Использовано токенов: <code>%s</code>\n\n", html.EscapeString(lastTokens)))

		bldr.WriteString("📋 <b>Справочная информация по квотам:</b>\n")
		bldr.WriteString("• <b>Gemini Flash (3.8/3.7):</b> высокая скорость, высокий дневной лимит запросов\n")
		bldr.WriteString("• <b>Gemini Pro (3.1):</b> окно контекста 2M токенов, скользящий лимит запросов\n")
		bldr.WriteString("• <b>Claude Thinking (Sonnet/Opus):</b> расширенное рассуждение, расход квот сложного инференса\n")

		return c.Send(bldr.String(), tele.ModeHTML)
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

	b.Handle("/cancel", func(c tele.Context) error {
		session.Lock()
		defer session.Unlock()
		if session.isRunning && session.cmd != nil && session.cmd.Process != nil {
			_ = session.cmd.Process.Kill()
			session.isRunning = false
			session.waiting = false
			session.pendingFollowups = nil
			session.fullOutput.Reset()
			return c.Send("🛑 Процесс остановлен. Очередь задач очищена.")
		}
		return c.Send("Сейчас нет активных задач.")
	})

	b.Handle(tele.OnText, func(c tele.Context) error {
		userText := strings.TrimSpace(c.Text())
		if strings.HasPrefix(userText, "/") {
			return nil
		}

		session.Lock()
		if session.isRunning && session.waiting {
			session.waiting = false
			_, err := io.WriteString(session.stdin, userText+"\n")
			session.Unlock()

			if err != nil {
				return c.Send(fmt.Sprintf("❌ Ошибка отправки ответа: %v", err))
			}
			return c.Send("💬 Ответ передан агенту...")
		}

		if session.isRunning {
			session.pendingFollowups = append(session.pendingFollowups, userText)
			count := len(session.pendingFollowups)
			session.Unlock()

			msg := fmt.Sprintf(
				"📥 <b>Дополнение сохранено в очередь (#%d)</b>\n\n"+
					"<i>«%s»</i>\n\n"+
					"Агент завершит текущий шаг и сразу применит эти правки в текущую ветку.",
				count, html.EscapeString(userText),
			)
			return c.Send(msg, tele.ModeHTML)
		}

		projectState.RLock()
		cur := projectState.currentProject
		projectState.RUnlock()

		if cur == "" {
			session.Unlock()
			return c.Send("❌ Сначала выберите проект: /projects")
		}

		session.isRunning = true
		session.waiting = false
		session.startedAt = time.Now()
		session.currentPrompt = userText
		session.currentProject = cur
		session.recentLogs = nil
		session.pendingFollowups = nil
		session.lastPRURL = ""
		session.fullOutput.Reset()
		session.Unlock()

		workDir := filepath.Join(projectsRoot, cur)
		go runAgentPipeline(b, c.Recipient(), workDir, cur, userText)

		return nil
	})

	log.Println("Мультипроектный агент-бот запущен...")
	b.Start()
}

func initDefaultProject(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			projectState.Lock()
			projectState.currentProject = e.Name()
			projectState.Unlock()
			break
		}
	}
}

func runAgentPipeline(b *tele.Bot, recipient tele.Recipient, workDir, projectName, initialPrompt string) {
	currentPrompt := initialPrompt

	for {
		projectState.RLock()
		activeModel := projectState.currentModel
		projectState.RUnlock()

		executeStep(b, recipient, workDir, projectName, currentPrompt, activeModel)

		session.Lock()
		if !session.isRunning {
			session.Unlock()
			return
		}

		if len(session.pendingFollowups) == 0 {
			prURL := session.lastPRURL
			finalReport := session.fullOutput.String()
			session.isRunning = false
			session.Unlock()

			if prURL != "" {
				b.Send(recipient, fmt.Sprintf("🎉 *Задача выполнена!*\n📁 Проект: `%s`\n🔗 [Открыть Pull Request](%s)", projectName, prURL), tele.ModeMarkdown)
			} else {
				b.Send(recipient, fmt.Sprintf("✅ *Задача завершена!* (`%s`)", projectName), tele.ModeMarkdown)
			}

			if strings.TrimSpace(finalReport) != "" {
				sendLongMarkdown(b, recipient, finalReport)
			}
			return
		}

		followups := session.pendingFollowups
		session.pendingFollowups = nil

		var bldr strings.Builder
		bldr.WriteString("ВНИМАНИЕ: Продолжай работу в ТЕКУЩЕЙ ветке git (НЕ создавай новую ветку, НЕ делай checkout в main). ")
		bldr.WriteString("Пользователь прислал следующие дополнения к задаче:\n")
		for i, f := range followups {
			bldr.WriteString(fmt.Sprintf("%d. %s\n", i+1, f))
		}
		bldr.WriteString("Внеси необходимые изменения, запусти тесты/линтеры, закоммить изменения и запушь в текущую ветку. Если PR уже открыт, обнови его.")

		currentPrompt = bldr.String()
		session.currentPrompt = strings.Join(followups, "; ")
		session.startedAt = time.Now()
		session.recentLogs = nil
		session.Unlock()

		b.Send(recipient, fmt.Sprintf("🔄 <b>Беру в работу дополнения (%d шт.)...</b>", len(followups)), tele.ModeHTML)
	}
}

func executeStep(b *tele.Bot, recipient tele.Recipient, workDir, projectName, prompt, modelName string) {
	statusMsg, _ := b.Send(recipient, fmt.Sprintf("🚀 <b>Шаг в работе:</b> <code>%s</code> [<code>%s</code>]\n<i>Инициализация сессии агента...</i>", html.EscapeString(projectName), html.EscapeString(modelName)), tele.ModeHTML)

	args := []string{
	    "--dangerously-skip-permissions",
	    "--print-timeout", "30m",
	    "--model", modelName,
	}
	// Модели reasoning требуют флаг --effort
	if strings.Contains(modelName, "claude") {
	    args = append(args, "--effort", "high")
	} else if strings.Contains(modelName, "gemini-3") {
		args = append(args, "--effort", "medium")
	}
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
		b.Send(recipient, fmt.Sprintf("❌ Ошибка запуска PTY: %v", err))
		session.Lock()
		session.isRunning = false
		session.Unlock()
		return
	}
	defer func() { _ = ptmx.Close() }()

	session.Lock()
	session.cmd = cmd
	session.stdin = ptmx
	session.Unlock()

	scanner := bufio.NewScanner(ptmx)
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
				session.Lock()
				if !session.isRunning {
					session.Unlock()
					return
				}
				var lastLine string
				if len(session.recentLogs) > 0 {
					lastLine = session.recentLogs[len(session.recentLogs)-1]
				}
				dur := time.Since(session.startedAt).Round(time.Second)
				followupsCount := len(session.pendingFollowups)
				session.Unlock()

				if statusMsg != nil && lastLine != "" {
					queueInfo := ""
					if followupsCount > 0 {
						queueInfo = fmt.Sprintf(" | Очередь: %d", followupsCount)
					}
					text := fmt.Sprintf(
						"⏳ <b>В работе:</b> <code>%s</code> [<code>%s</code>] (<code>%s</code>%s)\n\n📍 <b>Действие:</b>\n<code>%s</code>\n\n<i>(Логи: /status | Стоп: /cancel)</i>",
						html.EscapeString(projectName),
						html.EscapeString(modelName),
						dur,
						queueInfo,
						html.EscapeString(truncateString(lastLine, 80)),
					)
					_, _ = b.Edit(statusMsg, text, tele.ModeHTML)
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

			session.Lock()
			session.recentLogs = append(session.recentLogs, cleanLine)
			if len(session.recentLogs) > 20 {
				session.recentLogs = session.recentLogs[1:]
			}
			session.fullOutput.WriteString(cleanLine + "\n")

			if matches := prUrlRegexp.FindStringSubmatch(cleanLine); len(matches) > 1 {
				session.lastPRURL = matches[1]
			}
			if m := modelRegexp.FindStringSubmatch(cleanLine); len(m) > 1 {
				session.lastModelUsed = strings.TrimSpace(m[1])
			}
			if t := tokensRegexp.FindStringSubmatch(cleanLine); len(t) > 1 {
				session.lastTokensUsed = strings.TrimSpace(t[1])
			}
			session.Unlock()

			lower := strings.ToLower(cleanLine)
			isQuestion := strings.HasSuffix(cleanLine, "?") ||
				strings.Contains(lower, "подтвердите") ||
				strings.Contains(lower, "как поступить") ||
				strings.Contains(lower, "do you want to")

			if isQuestion {
				session.Lock()
				session.waiting = true
				session.Unlock()

				msg := fmt.Sprintf("❓ <b>Вопрос по проекту <code>%s</code>:</b>\n\n%s\n\n<i>Ответьте сообщением в чат.</i>", html.EscapeString(projectName), html.EscapeString(cleanLine))
				b.Send(recipient, msg, tele.ModeHTML)
			}
		}
		close(done)
	}()

	<-done
	close(stopLiveUpdate)
	_ = cmd.Wait()

	session.Lock()
	if session.stdin != nil {
		_ = session.stdin.Close()
		session.stdin = nil
	}
	session.waiting = false
	session.Unlock()
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