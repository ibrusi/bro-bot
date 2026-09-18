package system

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	restartMarkerFilename = ".restart_notify.json"
	defaultBotDir         = "/home/deploy/bro-bot"
	defaultServiceName    = "tg-bot.service"
)

var (
	systemActionLock sync.Mutex
	isSystemAction   bool
)

type RestartMarker struct {
	ChatID      string    `json:"chat_id"`
	MessageID   string    `json:"message_id,omitempty"`
	Action      string    `json:"action"` // "restart" or "rebuild"
	TriggeredAt time.Time `json:"triggered_at"`
	GitCommit   string    `json:"git_commit,omitempty"`
	GitBranch   string    `json:"git_branch,omitempty"`
}

// scalarToString конвертирует JSON-скаляр (число или строку) в строку. Нужен, чтобы
// UnmarshalJSON понимал как новый формат маркера (строковые ID), так и старый,
// сохранённый ещё telegram-специфичным кодом с числовыми chat_id/message_id.
func scalarToString(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// UnmarshalJSON поддерживает и текущий формат (строковые ChatID/MessageID), и старый
// маркер, сохранённый до введения абстракции мессенджера (числовые значения).
func (m *RestartMarker) UnmarshalJSON(data []byte) error {
	type alias RestartMarker
	aux := &struct {
		ChatID    interface{} `json:"chat_id"`
		MessageID interface{} `json:"message_id,omitempty"`
		*alias
	}{alias: (*alias)(m)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	m.ChatID = scalarToString(aux.ChatID)
	m.MessageID = scalarToString(aux.MessageID)
	return nil
}

type SystemFlags struct {
	Pull   bool
	Force  bool
	Branch string
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
	log.Fatal("ОБЯЗАТЕЛЬНЫЙ параметр BOT_DIR не задан и не удалось определить его автоматически")
	return ""
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

func CheckAndNotifyRestart(m ports.Messenger, defaultAdminChat ports.ChatID) {
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

	targetChat := ports.ChatID(marker.ChatID)
	if targetChat == "" {
		targetChat = defaultAdminChat
	}
	if targetChat == "" {
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

	_, sendErr := m.Send(context.Background(), targetChat, msg, ports.Rich())
	if sendErr != nil {
		log.Printf("Не удалось отправить уведомление о перезапуске: %v", sendErr)
	}
}

func parseSystemFlags(args []string) SystemFlags {
	var f SystemFlags
	for i := 0; i < len(args); i++ {
		a := args[i]
		lower := strings.ToLower(strings.TrimSpace(a))
		if strings.HasPrefix(lower, "branch=") || strings.HasPrefix(lower, "--branch=") {
			parts := strings.SplitN(a, "=", 2)
			if len(parts) == 2 {
				f.Branch = strings.TrimSpace(parts[1])
			}
			continue
		}

		switch lower {
		case "pull", "-p", "--pull":
			f.Pull = true
		case "force", "-f", "--force":
			f.Force = true
		case "branch", "-b", "--branch":
			if i+1 < len(args) {
				f.Branch = strings.TrimSpace(args[i+1])
				i++
			}
		}
	}
	return f
}

// validBranchNameRegex — подмножество допустимых имён веток git: буква или цифра в
// начале, дальше буквы, цифры, дефис, подчёркивание, точка и слэш.
var validBranchNameRegex = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// validateBranchName проверяет имя ветки перед передачей его в git.
//
// Имя приходит из аргументов команды /rebuild и попадает в позиционные аргументы
// git checkout / fetch / pull. Значение, начинающееся с дефиса, git разобрал бы как
// опцию (например --upload-pack=...), поэтому имя должно пройти белый список.
func validateBranchName(branch string) error {
	name := strings.TrimSpace(branch)

	switch {
	case name == "":
		return fmt.Errorf("имя ветки не может быть пустым")
	case len(name) > 255:
		return fmt.Errorf("имя ветки слишком длинное")
	case !validBranchNameRegex.MatchString(name):
		return fmt.Errorf("имя ветки содержит недопустимые символы. Разрешены буквы, цифры, дефис, подчёркивание, точка и слэш; начинаться имя должно с буквы или цифры")
	case strings.Contains(name, ".."), strings.Contains(name, "//"), strings.Contains(name, "@{"):
		return fmt.Errorf("имя ветки содержит недопустимую последовательность")
	case strings.HasSuffix(name, "/"), strings.HasSuffix(name, "."), strings.HasSuffix(name, ".lock"):
		return fmt.Errorf("недопустимое окончание имени ветки")
	}
	return nil
}

func performGitCheckout(ctx context.Context, dir, branch string, force bool) (string, error) {
	// Сначала выполняем git fetch origin, чтобы подтянуть метаданные веток с сервера,
	// если ветка существует только на origin и мы хотим переключиться на нее локально
	fetchCmd := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "origin")
	_, _ = fetchCmd.CombinedOutput()

	args := []string{"-C", dir, "checkout"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, branch)
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func performGitPull(ctx context.Context, dir, branch string, force bool) (string, error) {
	// Сначала выполняем git fetch origin [branch], чтобы подтянуть метаданные ветки с сервера
	fetchCmd := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "origin", branch)
	fetchOut, fetchErr := fetchCmd.CombinedOutput()
	if fetchErr != nil {
		// Пробуем общий git fetch origin, если специфичный fetch возвращает ошибку
		fetchCmdAll := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "origin")
		_, _ = fetchCmdAll.CombinedOutput()
	}

	cmd := exec.CommandContext(ctx, "git", "-C", dir, "pull", "origin", branch)
	out, err := cmd.CombinedOutput()
	if err != nil && force {
		resetCmd := exec.CommandContext(ctx, "git", "-C", dir, "reset", "--hard", "origin/"+branch)
		resetOut, resetErr := resetCmd.CombinedOutput()
		if resetErr == nil {
			return strings.TrimSpace(string(resetOut)), nil
		}
		if fetchErr != nil {
			return strings.TrimSpace(string(fetchOut)), fetchErr
		}
	}
	return strings.TrimSpace(string(out)), err
}

func performBuild(ctx context.Context, botDir string) (string, error) {
	goBin := getGoBinary()
	tmpBinary := filepath.Join(botDir, "bot.tmp")
	targetBinary := filepath.Join(botDir, "bot")

	_ = os.Remove(tmpBinary)

	cmd := exec.CommandContext(ctx, goBin, "build", "-o", tmpBinary, "./cmd/bot")
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

func executeRestart(t ports.Transport) {
	serviceName := os.Getenv("BOT_SERVICE_NAME")
	if serviceName == "" {
		log.Fatal("ОБЯЗАТЕЛЬНЫЙ параметр BOT_SERVICE_NAME не задан")
	}

	log.Printf("Инициирован перезапуск бота (сервис: %s)...", serviceName)

	// Небольшая задержка, чтобы сообщение гарантированно ушло получателю
	time.Sleep(800 * time.Millisecond)

	// Останавливаем получение апдейтов
	t.Stop()

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

func normalizeGitURL(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, "/")
	raw = strings.TrimSuffix(raw, ".git")
	raw = strings.TrimRight(raw, "/")
	if idx := strings.Index(raw, "://"); idx != -1 {
		raw = raw[idx+3:]
	}
	if idx := strings.Index(raw, "@"); idx != -1 {
		raw = raw[idx+1:]
	}
	raw = strings.ReplaceAll(raw, ":", "/")
	raw = strings.Trim(raw, "/")
	return strings.ToLower(raw)
}

func isBotProject(projectName, botDir, projectsRoot string) bool {
	cleanProj := strings.TrimSpace(projectName)
	if cleanProj == "" {
		return false
	}
	cleanProj = filepath.Clean(cleanProj)
	baseProj := filepath.Base(cleanProj)

	// Проверка по DEFAULT_PROJECT
	if defProj := os.Getenv("DEFAULT_PROJECT"); defProj != "" {
		if strings.EqualFold(cleanProj, defProj) || strings.EqualFold(baseProj, defProj) {
			return true
		}
	}

	// Прямое совпадение с известными именами репозитория бота
	for _, name := range []string{"bro-bot", "bro-bot"} {
		if strings.EqualFold(cleanProj, name) || strings.EqualFold(baseProj, name) {
			return true
		}
	}

	// Совпадение с basename директории бота (например, bro-bot)
	if botDir != "" {
		botBase := filepath.Base(botDir)
		if strings.EqualFold(cleanProj, botBase) || strings.EqualFold(baseProj, botBase) {
			return true
		}
	}

	// Совпадение по нормализованному Git Remote URL
	if botDir != "" {
		botRemote := normalizeGitURL(getGitRemoteURL(botDir))
		if botRemote != "" {
			var projDir string
			if filepath.IsAbs(cleanProj) {
				projDir = cleanProj
			} else if config.ProjectsRoot != "" {
				projDir = filepath.Join(config.ProjectsRoot, cleanProj)
			}
			if projDir != "" {
				projRemote := normalizeGitURL(getGitRemoteURL(projDir))
				if projRemote != "" && botRemote == projRemote {
					return true
				}
			}
		}
	}

	return false
}

func checkActiveTasksForSystemAction(cmdName string, flags SystemFlags, botDir, projectsRoot string) (string, bool) {
	activeTasks := domain.GlobalTaskManager.GetActiveOrQueuedTasks()

	// 1. Ищем активные задачи на проекте самого бота
	var botTasks []*domain.TaskSession
	var otherTasks []*domain.TaskSession
	for _, t := range activeTasks {
		if isBotProject(t.Project, botDir, config.ProjectsRoot) {
			botTasks = append(botTasks, t)
		} else {
			otherTasks = append(otherTasks, t)
		}
	}

	// 2. Если есть задачи на проекте бота и нет флага Force
	if len(botTasks) > 0 && !flags.Force {
		firstTask := botTasks[0]
		firstTask.Lock()
		tID := firstTask.ID
		tProj := firstTask.Project
		tPrompt := firstTask.CurrentPrompt
		tStatus := firstTask.Status.RussianTitle()
		firstTask.Unlock()

		countStr := ""
		if len(botTasks) > 1 {
			countStr = fmt.Sprintf(" (%d шт.)", len(botTasks))
		}

		actionDesc := "Сборка и перезапуск"
		if strings.Contains(cmdName, "pull") {
			actionDesc = "Смена ветки на main, git pull и сборка"
		} else if strings.Contains(cmdName, "restart") {
			actionDesc = "Перезапуск бота"
		}

		return fmt.Sprintf(
			"⚠️ <b>На проекте бота выполняется активная задача%s!</b>\n\n"+
				"• Проект: <code>%s</code>\n"+
				"• Задача #%d: <i>%s</i>\n"+
				"• Статус: %s\n\n"+
				"%s могут повлиять на рабочий репозиторий и прервут выполнение.\n"+
				"Чтобы принудительно остановить задачу и выполнить команду:\n"+
				"<code>%s force</code>\n\n"+
				"Или отмените текущую задачу командой: <code>/cancel %d</code>.",
			countStr,
			html.EscapeString(tProj),
			tID,
			html.EscapeString(utils.TruncateString(tPrompt, 100)),
			html.EscapeString(tStatus),
			actionDesc,
			cmdName,
			tID,
		), true
	}

	// 3. Если есть любые другие активные задачи в боте и нет флага Force
	if len(otherTasks) > 0 && !flags.Force {
		firstTask := otherTasks[0]
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
			len(otherTasks),
			tID,
			html.EscapeString(tProj),
			html.EscapeString(utils.TruncateString(tPrompt, 100)),
			html.EscapeString(tStatus),
			cmdName,
		), true
	}

	// 4. Проверка устаревшей сессии (session) на случай фоллбэка
	config.Session.Lock()
	legacyRunning := config.Session.IsRunning
	legacyPrompt := config.Session.CurrentPrompt
	legacyProj := config.Session.CurrentProject
	config.Session.Unlock()

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
			html.EscapeString(utils.TruncateString(legacyPrompt, 100)),
			cmdName,
		), true
	}

	// 5. Если передан флаг Force — останавливаем все задачи. CancelTask сам гасит
	// процесс шага (сигналом группе у CLI, отменой контекста у API).
	if flags.Force {
		for _, t := range activeTasks {
			_, _ = domain.GlobalTaskManager.CancelTask(t.ID)
		}

		config.Session.Lock()
		config.Session.IsRunning = false
		config.Session.Waiting = false
		config.Session.PendingFollowups = nil
		config.Session.FullOutput.Reset()
		domain.GlobalTokenTracker.CancelTask()
		config.Session.Unlock()
	}

	return "", false
}

func HandleRebuild(t ports.Transport, s ports.Session) error {
	systemActionLock.Lock()
	if isSystemAction {
		systemActionLock.Unlock()
		return s.Send("⚠️ Операция сборки или перезапуска уже выполняется, подождите...", nil)
	}
	isSystemAction = true
	systemActionLock.Unlock()

	defer func() {
		systemActionLock.Lock()
		isSystemAction = false
		systemActionLock.Unlock()
	}()

	flags := parseSystemFlags(s.Args())
	botDir := getBotDir()

	cmdName := "/rebuild"
	if flags.Pull {
		cmdName = "/rebuild pull"
	}

	if warnMsg, blocked := checkActiveTasksForSystemAction(cmdName, flags, botDir, config.ProjectsRoot); blocked {
		return s.Send(warnMsg, ports.Rich())
	}

	ctx := context.Background()
	chat := s.Chat()
	statusRef, _ := t.Send(ctx, chat, "🔨 <b>Инициализация сборки бота...</b>", ports.Rich())

	updateStatus := func(text string) {
		if statusRef.ID != "" {
			if err := t.Edit(ctx, statusRef, text, ports.Rich()); err == nil {
				return
			}
		}
		statusRef, _ = t.Send(ctx, chat, text, ports.Rich())
	}

	// Опциональный git checkout и git pull
	if flags.Pull || flags.Branch != "" {
		targetBranch := "main"
		if flags.Branch != "" {
			targetBranch = flags.Branch
		}

		// Имя проверяем до первого обращения к git: иначе значение вроде "-f" или
		// "--upload-pack=..." ушло бы в команду как опция, а не как ветка.
		if err := validateBranchName(targetBranch); err != nil {
			updateStatus(fmt.Sprintf(
				"❌ <b>Недопустимое имя ветки:</b> %s\n<i>Сборка отменена, бот продолжает работу на текущей ветке.</i>",
				html.EscapeString(err.Error()),
			))
			return nil
		}

		updateStatus(fmt.Sprintf("🌿 <b>Переключаюсь на ветку %s...</b>", html.EscapeString(targetBranch)))
		ctxCheckout, cancelCheckout := context.WithTimeout(context.Background(), 30*time.Second)
		checkoutOut, checkoutErr := performGitCheckout(ctxCheckout, botDir, targetBranch, flags.Force)
		cancelCheckout()
		if checkoutErr != nil {
			errMsg := checkoutOut
			if errMsg == "" {
				errMsg = checkoutErr.Error()
			}
			updateStatus(fmt.Sprintf(
				"❌ <b>Ошибка при переключении на ветку %s:</b>\n<pre>%s</pre>\n<i>Сборка отменена, бот продолжает работу на текущей ветке.</i>",
				html.EscapeString(targetBranch),
				html.EscapeString(errMsg),
			))
			return nil
		}

		if flags.Pull {
			updateStatus("📥 <b>Выполняю git pull origin...</b>")
			ctxPull, cancelPull := context.WithTimeout(context.Background(), 30*time.Second)
			pullOut, pullErr := performGitPull(ctxPull, botDir, targetBranch, flags.Force)
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
			html.EscapeString(utils.TruncateString(errDetails, 3500)),
		))
		return nil
	}

	// Сохраняем маркер для отправки подтверждения после перезапуска
	_ = saveRestartMarker(botDir, RestartMarker{
		ChatID:      string(chat),
		MessageID:   string(statusRef.ID),
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

	go executeRestart(t)
	return nil
}

func HandleRestart(t ports.Transport, s ports.Session) error {
	systemActionLock.Lock()
	if isSystemAction {
		systemActionLock.Unlock()
		return s.Send("⚠️ Операция сборки или перезапуска уже выполняется, подождите...", nil)
	}
	isSystemAction = true
	systemActionLock.Unlock()

	defer func() {
		systemActionLock.Lock()
		isSystemAction = false
		systemActionLock.Unlock()
	}()

	flags := parseSystemFlags(s.Args())
	botDir := getBotDir()

	if warnMsg, blocked := checkActiveTasksForSystemAction("/restart", flags, botDir, config.ProjectsRoot); blocked {
		return s.Send(warnMsg, ports.Rich())
	}

	branch := getGitBranch(botDir)
	commit := getGitCommit(botDir)

	ctx := context.Background()
	chat := s.Chat()
	statusRef, _ := t.Send(ctx, chat, "🔄 <b>Инициирован перезапуск бота...</b>", ports.Rich())

	_ = saveRestartMarker(botDir, RestartMarker{
		ChatID:      string(chat),
		MessageID:   string(statusRef.ID),
		Action:      "restart",
		TriggeredAt: time.Now(),
		GitCommit:   commit,
		GitBranch:   branch,
	})

	go executeRestart(t)
	return nil
}
