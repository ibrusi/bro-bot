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
}

type ProjectState struct {
	sync.RWMutex
	currentProject string
}

var (
	session      AgentSession
	projectState ProjectState
	adminID      int64
	prUrlRegexp  = regexp.MustCompile(`PR_URL:\s*(https?://[^\s]+)`)
	ansiRegex    = regexp.MustCompile(`\x1b(\[[0-9;?><=$]*[a-zA-Z~]|\][0-9;]*\x07|\([B0-9])|\r`)
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

	b, err := tele.NewBot(tele.Settings{
		Token:  botToken,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Регистрация меню команд в интерфейсе Telegram
	commands := []tele.Command{
		{Text: "status", Description: "Статус задачи, лог и очередь"},
		{Text: "projects", Description: "Список доступных проектов"},
		{Text: "use", Description: "Переключить проект: /use <имя>"},
		{Text: "cancel", Description: "Принудительно остановить процесс"},
		{Text: "start", Description: "Справка и текущий активный проект"},
	}
	if err := b.SetCommands(commands); err != nil {
		log.Printf("Предупреждение: не удалось зарегистрировать команды: %v", err)
	}

	// Фильтр: пускать только владельца по ID
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
		cur := projectState.currentProject
		projectState.RUnlock()

		msg := fmt.Sprintf(
			"🤖 <b>Агент-воркер готов к работе!</b>\n\n"+
				"📁 Текущий проект: <code>%s</code>\n\n"+
				"<b>Команды:</b>\n"+
				"• /status — статус, лог и очередь уточнений\n"+
				"• /projects — список проектов\n"+
				"• /use &lt;имя&gt; — переключить проект\n"+
				"• /cancel — остановить задачу и сбросить очередь\n\n"+
				"Отправьте задачу сообщением в чат. Дополнения можно присылать прямо во время выполнения.",
			html.EscapeString(cur),
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
		// Ответ на уточняющий вопрос агента
		if session.isRunning && session.waiting {
			session.waiting = false
			_, err := io.WriteString(session.stdin, userText+"\n")
			session.Unlock()

			if err != nil {
				return c.Send(fmt.Sprintf("❌ Ошибка отправки ответа: %v", err))
			}
			return c.Send("💬 Ответ передан агенту...")
		}

		// Агент занят — добавляем сообщение в очередь доработок
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

		// Запуск новой задачи
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
		session.Unlock()

		workDir := filepath.Join(projectsRoot, cur)
		go runAgentPipeline(b, c.Recipient(), workDir, cur, userText)

		return nil
	})

	log.Println("Мультипроектный агент-бот с поддержкой очереди запущен...")
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
		executeStep(b, recipient, workDir, projectName, currentPrompt)

		session.Lock()
		if !session.isRunning {
			session.Unlock()
			return
		}

		if len(session.pendingFollowups) == 0 {
			prURL := session.lastPRURL
			session.isRunning = false
			session.Unlock()

			if prURL != "" {
				b.Send(recipient, fmt.Sprintf("🎉 <b>Все задачи и дополнения выполнены!</b> (<code>%s</code>)\n\n🔗 <b>Pull Request:</b>\n%s", html.EscapeString(projectName), prURL), tele.ModeHTML)
			} else {
				session.Lock()
				recentTail := strings.Join(session.recentLogs, "\n")
				if len(recentTail) > 1800 {
					recentTail = recentTail[len(recentTail)-1800:]
				}
				session.Unlock()
				b.Send(recipient, fmt.Sprintf("✅ Все задачи завершены (<code>%s</code>).\n\nИтоговый лог:\n<pre>%s</pre>", html.EscapeString(projectName), html.EscapeString(recentTail)), tele.ModeHTML)
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

func executeStep(b *tele.Bot, recipient tele.Recipient, workDir, projectName, prompt string) {
	statusMsg, _ := b.Send(recipient, fmt.Sprintf("🚀 <b>Шаг в работе:</b> <code>%s</code>\n<i>Инициализация сессии агента...</i>", html.EscapeString(projectName)), tele.ModeHTML)

	// Запуск с авто-одобрением системных команд и стримингом без TUI
	cmd := exec.Command("agy", "--dangerously-skip-permissions", "-p", prompt)
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
						"⏳ <b>В работе:</b> <code>%s</code> (<code>%s</code>%s)\n\n📍 <b>Действие:</b>\n<code>%s</code>\n\n<i>(Логи: /status | Стоп: /cancel)</i>",
						html.EscapeString(projectName),
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
			if matches := prUrlRegexp.FindStringSubmatch(cleanLine); len(matches) > 1 {
				session.lastPRURL = matches[1]
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

func truncateString(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
