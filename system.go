package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"
)

const (
	restartMarkerFilename = ".restart_notify.json"
	defaultBotDir         = "/home/deploy/tg-agent-bot"
	defaultServiceName    = "tg-bot.service"
)

var (
	systemActionLock sync.Mutex
	isSystemAction   bool
)

type RestartMarker struct {
	ChatID      int64     `json:"chat_id"`
	MessageID   int       `json:"message_id,omitempty"`
	Action      string    `json:"action"` // "restart" or "rebuild"
	TriggeredAt time.Time `json:"triggered_at"`
	GitCommit   string    `json:"git_commit,omitempty"`
	GitBranch   string    `json:"git_branch,omitempty"`
}

type SystemFlags struct {
	Pull  bool
	Force bool
}

func getBotDir() string {
	if d := os.Getenv("BOT_DIR"); d != "" {
		return d
	}
	exe, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(exeDir, "go.mod")); err == nil {
			return exeDir
		}
	}
	return defaultBotDir
}

func getGoBinary() string {
	if bin, err := exec.LookPath("go"); err == nil {
		return bin
	}
	candidates := []string{
		"/usr/local/go/bin/go",
		"/home/deploy/go/bin/go",
		"/usr/bin/go",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "go"
}

func getGitBranch(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func getGitCommit(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "log", "-1", "--format=%h (%s)").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func saveRestartMarker(botDir string, marker RestartMarker) error {
	filePath := filepath.Join(botDir, restartMarkerFilename)
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filePath, data, 0644)
}

func loadAndClearRestartMarker(botDir string) (*RestartMarker, error) {
	filePath := filepath.Join(botDir, restartMarkerFilename)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	_ = os.Remove(filePath)

	var marker RestartMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, err
	}
	return &marker, nil
}

func checkAndNotifyRestart(b *tele.Bot, defaultAdminID int64) {
	time.Sleep(1500 * time.Millisecond)

	botDir := getBotDir()
	marker, err := loadAndClearRestartMarker(botDir)
	if err != nil {
		log.Printf("Ошибка чтения маркера перезапуска: %v", err)
		return
	}
	if marker == nil {
		return
	}

	if time.Since(marker.TriggeredAt) > 10*time.Minute {
		log.Printf("Маркер перезапуска устарел (%v), пропускаем", marker.TriggeredAt)
		return
	}

	targetChatID := marker.ChatID
	if targetChatID == 0 {
		targetChatID = defaultAdminID
	}
	if targetChatID == 0 {
		return
	}

	branch := getGitBranch(botDir)
	commit := getGitCommit(botDir)
	nowStr := time.Now().Format("02.01.2006 15:04:05 MST")

	var title string
	if marker.Action == "rebuild" {
		title = "🚀 <b>Бот успешно пересобран и перезапущен!</b>"
	} else {
		title = "🚀 <b>Бот успешно перезапущен!</b>"
	}

	msg := fmt.Sprintf(
		"%s\n\n"+
			"🌿 <b>Ветка:</b> <code>%s</code>\n"+
			"🔖 <b>Коммит:</b> <code>%s</code>\n"+
			"⏱ <b>Время запуска:</b> <code>%s</code>\n\n"+
			"✅ <i>Все системы активны и готовы к приёму задач.</i>",
		title,
		html.EscapeString(branch),
		html.EscapeString(commit),
		html.EscapeString(nowStr),
	)

	_, sendErr := b.Send(tele.ChatID(targetChatID), msg, tele.ModeHTML)
	if sendErr != nil {
		log.Printf("Не удалось отправить уведомление о перезапуске: %v", sendErr)
	}
}

func parseSystemFlags(args []string) SystemFlags {
	var f SystemFlags
	for _, a := range args {
		lower := strings.ToLower(strings.TrimSpace(a))
		switch lower {
		case "pull", "-p", "--pull":
			f.Pull = true
		case "force", "-f", "--force":
			f.Force = true
		}
	}
	return f
}

func performGitCheckout(ctx context.Context, dir, branch string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "checkout", branch)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func performGitPull(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "pull")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func performBuild(ctx context.Context, botDir string) (string, error) {
	goBin := getGoBinary()
	tmpBinary := filepath.Join(botDir, "bot.tmp")
	targetBinary := filepath.Join(botDir, "bot")

	_ = os.Remove(tmpBinary)

	cmd := exec.CommandContext(ctx, goBin, "build", "-o", tmpBinary, ".")
	cmd.Dir = botDir
	out, err := cmd.CombinedOutput()
	outStr := strings.TrimSpace(string(out))
	if err != nil {
		_ = os.Remove(tmpBinary)
		return outStr, fmt.Errorf("ошибка компиляции: %w\n%s", err, outStr)
	}

	if err := os.Chmod(tmpBinary, 0755); err != nil {
		_ = os.Remove(tmpBinary)
		return "", fmt.Errorf("не удалось выставить права на бинарник: %w", err)
	}

	// Атомарно заменяем рабочий бинарник
	if err := os.Rename(tmpBinary, targetBinary); err != nil {
		_ = os.Remove(tmpBinary)
		return "", fmt.Errorf("не удалось заменить бинарник: %w", err)
	}

	return outStr, nil
}

func executeRestart(b *tele.Bot, recipient tele.Recipient) {
	serviceName := os.Getenv("BOT_SERVICE_NAME")
	if serviceName == "" {
		serviceName = defaultServiceName
	}

	log.Printf("Инициирован перезапуск бота (сервис: %s)...", serviceName)

	// Небольшая задержка, чтобы сообщение в Telegram гарантированно ушло
	time.Sleep(800 * time.Millisecond)

	// Останавливаем polling обновлений Telegram
	b.Stop()

	// 1. Пробуем безопасный неблокирующий перезапуск через systemctl
	cmd := exec.Command("sudo", "systemctl", "restart", "--no-block", serviceName)
	if err := cmd.Run(); err == nil {
		log.Printf("sudo systemctl restart --no-block %s успешно вызван", serviceName)
		// Ждём штатного сигнала от systemd, если не пришёл за 3 сек — выходим чисто
		time.Sleep(3 * time.Second)
		os.Exit(0)
		return
	} else {
		log.Printf("Предупреждение: sudo systemctl restart завершился с ошибкой: %v", err)
	}

	// 2. Мягкий фоллбек: чистый выход процесса.
	// Так как в systemd настроено Restart=always, сервис автоматически перезапустится!
	log.Println("Запуск мягкого завершения процесса (Restart=always перезапустит сервис)...")
	time.Sleep(500 * time.Millisecond)
	os.Exit(0)
}

func getGitRemoteURL(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

func isBotProject(projectName, botDir, projectsRoot string) bool {
	cleanProj := strings.TrimSpace(projectName)
	if cleanProj == "" {
		return false
	}
	// Прямое совпадение с известными именами репозитория бота
	if cleanProj == "tg-bot-agent" || cleanProj == "tg-agent-bot" {
		return true
	}
	if botDir != "" && strings.EqualFold(cleanProj, filepath.Base(botDir)) {
		return true
	}
	if projectsRoot != "" && botDir != "" {
		projDir := filepath.Join(projectsRoot, cleanProj)
		botRemote := getGitRemoteURL(botDir)
		projRemote := getGitRemoteURL(projDir)
		if botRemote != "" && projRemote != "" && botRemote == projRemote {
			return true
		}
	}
	return false
}

func checkActiveTasksForSystemAction(cmdName string, flags SystemFlags, botDir, projectsRoot string) (string, bool) {
	activeTasks := taskManager.GetActiveOrQueuedTasks()

	// 1. Ищем активную задачу на проекте самого бота
	var botTask *TaskSession
	for _, t := range activeTasks {
		if isBotProject(t.Project, botDir, projectsRoot) {
			botTask = t
			break
		}
	}

	// 2. Если есть задача на проекте бота и нет флага Force
	if botTask != nil && !flags.Force {
		botTask.Lock()
		tID := botTask.ID
		tProj := botTask.Project
		tPrompt := botTask.CurrentPrompt
		tStatus := botTask.Status.RussianTitle()
		botTask.Unlock()

		return fmt.Sprintf(
			"⚠️ <b>На проекте бота выполняется активная задача!</b>\n\n"+
				"• Проект: <code>%s</code>\n"+
				"• Задача #%d: <i>%s</i>\n"+
				"• Статус: %s\n\n"+
				"Смена ветки, git pull и сборка могут повлиять на рабочий репозиторий и прервут выполнение.\n"+
				"Чтобы принудительно остановить задачу и выполнить команду:\n"+
				"<code>%s force</code>\n\n"+
				"Или отмените текущую задачу командой: <code>/cancel %d</code>.",
			html.EscapeString(tProj),
			tID,
			html.EscapeString(truncateString(tPrompt, 100)),
			html.EscapeString(tStatus),
			cmdName,
			tID,
		), true
	}

	// 3. Если есть любые другие активные задачи в боте и нет флага Force
	if len(activeTasks) > 0 && !flags.Force {
		firstTask := activeTasks[0]
		firstTask.Lock()
		tID := firstTask.ID
		tProj := firstTask.Project
		tPrompt := firstTask.CurrentPrompt
		tStatus := firstTask.Status.RussianTitle()
		firstTask.Unlock()

		return fmt.Sprintf(
			"⚠️ <b>Выполняются активные задачи (%d шт.)!</b>\n\n"+
				"• Задача #%d (проект: <code>%s</code>): <i>%s</i> [%s]\n\n"+
				"Перезапуск бота прервёт выполнение активных процессов.\n"+
				"Чтобы принудительно остановить задачи и выполнить операцию:\n"+
				"<code>%s force</code>\n\n"+
				"Или дождитесь их завершения.",
			len(activeTasks),
			tID,
			html.EscapeString(tProj),
			html.EscapeString(truncateString(tPrompt, 100)),
			html.EscapeString(tStatus),
			cmdName,
		), true
	}

	// 4. Проверка устаревшей сессии (session) на случай фоллбэка
	session.Lock()
	legacyRunning := session.isRunning
	legacyPrompt := session.currentPrompt
	legacyProj := session.currentProject
	session.Unlock()

	if legacyRunning && !flags.Force {
		return fmt.Sprintf(
			"⚠️ <b>Выполняется активная задача!</b>\n\n"+
				"• Проект: <code>%s</code>\n"+
				"• Задача: <i>%s</i>\n\n"+
				"Сборка и перезапуск прервут её выполнение.\n"+
				"Чтобы принудительно перезапустить:\n"+
				"<code>%s force</code>\n\n"+
				"Или отмените текущую задачу командой /cancel.",
			html.EscapeString(legacyProj),
			html.EscapeString(truncateString(legacyPrompt, 100)),
			cmdName,
		), true
	}

	// 5. Если передан флаг Force — останавливаем все задачи
	if flags.Force {
		for _, t := range activeTasks {
			_, _ = taskManager.CancelTask(t.ID)
		}

		session.Lock()
		if session.cmd != nil && session.cmd.Process != nil {
			_ = session.cmd.Process.Kill()
		}
		session.isRunning = false
		session.waiting = false
		session.pendingFollowups = nil
		session.fullOutput.Reset()
		tokenTracker.CancelTask()
		session.Unlock()
	}

	return "", false
}

func handleRebuild(b *tele.Bot, c tele.Context) error {
	systemActionLock.Lock()
	if isSystemAction {
		systemActionLock.Unlock()
		return c.Send("⚠️ Операция сборки или перезапуска уже выполняется, подождите...")
	}
	isSystemAction = true
	systemActionLock.Unlock()

	defer func() {
		systemActionLock.Lock()
		isSystemAction = false
		systemActionLock.Unlock()
	}()

	flags := parseSystemFlags(c.Args())
	botDir := getBotDir()

	cmdName := "/rebuild"
	if flags.Pull {
		cmdName = "/rebuild pull"
	}

	if warnMsg, blocked := checkActiveTasksForSystemAction(cmdName, flags, botDir, projectsRoot); blocked {
		return c.Send(warnMsg, tele.ModeHTML)
	}

	statusMsg, _ := b.Send(c.Recipient(), "🔨 <b>Инициализация сборки бота...</b>", tele.ModeHTML)

	updateStatus := func(text string) {
		if statusMsg != nil {
			if _, err := b.Edit(statusMsg, text, tele.ModeHTML); err == nil {
				return
			}
		}
		statusMsg, _ = b.Send(c.Recipient(), text, tele.ModeHTML)
	}

	// Опциональный git checkout main и git pull
	if flags.Pull {
		updateStatus("🌿 <b>Переключаюсь на ветку main...</b>")
		ctxCheckout, cancelCheckout := context.WithTimeout(context.Background(), 30*time.Second)
		checkoutOut, checkoutErr := performGitCheckout(ctxCheckout, botDir, "main")
		cancelCheckout()
		if checkoutErr != nil {
			errMsg := checkoutOut
			if errMsg == "" {
				errMsg = checkoutErr.Error()
			}
			updateStatus(fmt.Sprintf(
				"❌ <b>Ошибка при переключении на ветку main:</b>\n<pre>%s</pre>\n<i>Сборка отменена, бот продолжает работу на текущей ветке.</i>",
				html.EscapeString(errMsg),
			))
			return nil
		}

		updateStatus("📥 <b>Выполняю git pull origin...</b>")
		ctxPull, cancelPull := context.WithTimeout(context.Background(), 30*time.Second)
		pullOut, pullErr := performGitPull(ctxPull, botDir)
		cancelPull()
		if pullErr != nil {
			errMsg := pullOut
			if errMsg == "" {
				errMsg = pullErr.Error()
			}
			updateStatus(fmt.Sprintf(
				"❌ <b>Ошибка при git pull:</b>\n<pre>%s</pre>\n<i>Сборка отменена, бот продолжает работу.</i>",
				html.EscapeString(errMsg),
			))
			return nil
		}
	}

	branch := getGitBranch(botDir)
	commit := getGitCommit(botDir)

	updateStatus(fmt.Sprintf(
		"🔨 <b>Компиляция нового билда...</b>\n"+
			"🌿 Ветка: <code>%s</code>\n"+
			"🔖 Коммит: <code>%s</code>",
		html.EscapeString(branch),
		html.EscapeString(commit),
	))

	ctxBuild, cancelBuild := context.WithTimeout(context.Background(), 2*time.Minute)
	buildOut, buildErr := performBuild(ctxBuild, botDir)
	cancelBuild()

	if buildErr != nil {
		errDetails := buildOut
		if errDetails == "" {
			errDetails = buildErr.Error()
		}
		updateStatus(fmt.Sprintf(
			"❌ <b>Ошибка компиляции (билд отклонён):</b>\n<pre>%s</pre>\n\n"+
				"🛡 <i>Текущий бинарник сохранён без изменений, бот продолжает работу.</i>",
			html.EscapeString(truncateString(errDetails, 3500)),
		))
		return nil
	}

	// Сохраняем маркер для отправки подтверждения после перезапуска
	var msgID int
	if statusMsg != nil {
		msgID = statusMsg.ID
	}
	_ = saveRestartMarker(botDir, RestartMarker{
		ChatID:      c.Sender().ID,
		MessageID:   msgID,
		Action:      "rebuild",
		TriggeredAt: time.Now(),
		GitCommit:   commit,
		GitBranch:   branch,
	})

	updateStatus(fmt.Sprintf(
		"✅ <b>Билд успешно собран!</b>\n"+
			"🌿 Ветка: <code>%s</code>\n"+
			"🔖 Коммит: <code>%s</code>\n\n"+
			"🔄 <b>Перезапускаю сервис...</b>",
		html.EscapeString(branch),
		html.EscapeString(commit),
	))

	go executeRestart(b, c.Recipient())
	return nil
}

func handleRestart(b *tele.Bot, c tele.Context) error {
	systemActionLock.Lock()
	if isSystemAction {
		systemActionLock.Unlock()
		return c.Send("⚠️ Операция сборки или перезапуска уже выполняется, подождите...")
	}
	isSystemAction = true
	systemActionLock.Unlock()

	defer func() {
		systemActionLock.Lock()
		isSystemAction = false
		systemActionLock.Unlock()
	}()

	flags := parseSystemFlags(c.Args())
	botDir := getBotDir()

	if warnMsg, blocked := checkActiveTasksForSystemAction("/restart", flags, botDir, projectsRoot); blocked {
		return c.Send(warnMsg, tele.ModeHTML)
	}

	branch := getGitBranch(botDir)
	commit := getGitCommit(botDir)

	statusMsg, _ := b.Send(c.Recipient(), "🔄 <b>Инициирован перезапуск бота...</b>", tele.ModeHTML)

	var msgID int
	if statusMsg != nil {
		msgID = statusMsg.ID
	}
	_ = saveRestartMarker(botDir, RestartMarker{
		ChatID:      c.Sender().ID,
		MessageID:   msgID,
		Action:      "restart",
		TriggeredAt: time.Now(),
		GitCommit:   commit,
		GitBranch:   branch,
	})

	go executeRestart(b, c.Recipient())
	return nil
}
