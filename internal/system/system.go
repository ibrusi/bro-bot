package system

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
	"context"
	"encoding/json"
	"errors"
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
	// botRepoName — имя каталога репозитория самого бота: по нему команды /rebuild
	// и /restart отличают проект бота от прочих проектов.
	botRepoName = "bro-bot"
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

// CheckAndNotifyRestart досылает отчёт о перезапуске. Каталог бота приходит
// параметром, а не из config: горутина просыпается через полторы секунды, а снимок
// конфигурации к этому времени может переписывать следующий Start — и чтение против
// записи поймает детектор гонок.
func CheckAndNotifyRestart(m ports.Messenger, defaultAdminChat ports.ChatID, botDir string) {
	time.Sleep(1500 * time.Millisecond)

	if botDir == "" {
		log.Printf("bot directory is unknown, skipping the restart marker check")
		return
	}
	marker, err := loadAndClearRestartMarker(botDir)
	if err != nil {
		log.Printf("cannot read the restart marker: %v", err)
		return
	}
	if marker == nil {
		return
	}

	if time.Since(marker.TriggeredAt) > 10*time.Minute {
		log.Printf("the restart marker is stale (%v), skipping", marker.TriggeredAt)
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

	lang := config.ProjectState.GetLanguage()
	title := i18n.T(lang, "system.restart_title")
	if marker.Action == "rebuild" {
		title = i18n.T(lang, "system.rebuild_title")
	}

	msg := i18n.Tf(lang, "system.restart_report",
		title,
		html.EscapeString(branch),
		html.EscapeString(commit),
		html.EscapeString(nowStr),
	)

	_, sendErr := m.Send(context.Background(), targetChat, msg, ports.Rich())
	if sendErr != nil {
		log.Printf("cannot send the restart notification: %v", sendErr)
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

// Ошибки проверки имени ветки. Текст служебный: пользователю показывается перевод,
// который подбирает BranchErrorText.
var (
	ErrBranchEmpty    = errors.New("system: branch name must not be empty")
	ErrBranchTooLong  = errors.New("system: branch name is too long")
	ErrBranchCharset  = errors.New("system: branch name contains characters outside the allowed set")
	ErrBranchSequence = errors.New("system: branch name contains a forbidden sequence")
	ErrBranchSuffix   = errors.New("system: branch name has a forbidden ending")
)

// BranchErrorText переводит ошибку validateBranchName на язык интерфейса.
func BranchErrorText(err error, lang string) string {
	switch {
	case errors.Is(err, ErrBranchEmpty):
		return i18n.T(lang, "err.branch_empty")
	case errors.Is(err, ErrBranchTooLong):
		return i18n.T(lang, "err.branch_too_long")
	case errors.Is(err, ErrBranchCharset):
		return i18n.T(lang, "err.branch_charset")
	case errors.Is(err, ErrBranchSequence):
		return i18n.T(lang, "err.branch_sequence")
	case errors.Is(err, ErrBranchSuffix):
		return i18n.T(lang, "err.branch_suffix")
	default:
		return err.Error()
	}
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
		return ErrBranchEmpty
	case len(name) > 255:
		return ErrBranchTooLong
	case !validBranchNameRegex.MatchString(name):
		return ErrBranchCharset
	case strings.Contains(name, ".."), strings.Contains(name, "//"), strings.Contains(name, "@{"):
		return ErrBranchSequence
	case strings.HasSuffix(name, "/"), strings.HasSuffix(name, "."), strings.HasSuffix(name, ".lock"):
		return ErrBranchSuffix
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
		return outStr, fmt.Errorf("system: build failed: %w\n%s", err, outStr)
	}

	if err := os.Chmod(tmpBinary, 0755); err != nil {
		_ = os.Remove(tmpBinary)
		return "", fmt.Errorf("system: chmod the new binary: %w", err)
	}

	// Атомарно заменяем рабочий бинарник
	if err := os.Rename(tmpBinary, targetBinary); err != nil {
		_ = os.Remove(tmpBinary)
		return "", fmt.Errorf("system: replace the binary: %w", err)
	}

	return outStr, nil
}

func executeRestart(t ports.Transport) {
	serviceName := config.ServiceName
	if serviceName == "" {
		// До этого места с пустым именем не добраться: конфигурация проверена на
		// старте. Но если добрались — выходим чисто и даём systemd поднять юнит,
		// вместо того чтобы убивать бота посреди пользовательской команды.
		log.Printf("service name is not set, skipping systemctl: exiting and relying on Restart=always")
		t.Stop()
		os.Exit(0)
	}

	log.Printf("bot restart initiated (service: %s)...", serviceName)

	// Небольшая задержка, чтобы сообщение гарантированно ушло получателю
	time.Sleep(800 * time.Millisecond)

	// Останавливаем получение апдейтов
	t.Stop()

	// 1. Пробуем безопасный неблокирующий перезапуск через systemctl
	cmd := exec.Command("sudo", "systemctl", "restart", "--no-block", serviceName)
	if err := cmd.Run(); err == nil {
		log.Printf("sudo systemctl restart --no-block %s succeeded", serviceName)
		// Ждём штатного сигнала от systemd, если не пришёл за 3 сек — выходим чисто
		time.Sleep(3 * time.Second)
		os.Exit(0)
		return
	} else {
		log.Printf("warning: sudo systemctl restart failed: %v", err)
	}

	// 2. Мягкий фоллбек: чистый выход процесса.
	// Так как в systemd настроено Restart=always, сервис автоматически перезапустится!
	log.Println("falling back to a clean process exit (Restart=always will bring the service back)...")
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

	// Проверка по проекту по умолчанию
	if defProj := config.DefaultProject; defProj != "" {
		if strings.EqualFold(cleanProj, defProj) || strings.EqualFold(baseProj, defProj) {
			return true
		}
	}

	// Прямое совпадение с именем репозитория бота
	if strings.EqualFold(cleanProj, botRepoName) || strings.EqualFold(baseProj, botRepoName) {
		return true
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

func checkActiveTasksForSystemAction(cmdName string, flags SystemFlags, botDir, projectsRoot, lang string) (string, bool) {
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
		firstView := firstTask.Snapshot()
		tID := firstView.ID
		tProj := firstView.Project
		tPrompt := firstView.CurrentPrompt
		tStatus := i18n.TaskStatusTitle(string(firstView.Status), lang)

		countStr := ""
		if len(botTasks) > 1 {
			countStr = i18n.Tf(lang, "system.busy_count", len(botTasks))
		}

		actionDesc := i18n.T(lang, "system.action_rebuild")
		if strings.Contains(cmdName, "pull") {
			actionDesc = i18n.T(lang, "system.action_pull")
		} else if strings.Contains(cmdName, "restart") {
			actionDesc = i18n.T(lang, "system.action_restart")
		}

		return i18n.Tf(lang, "system.busy_bot_task",
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
		firstView2 := firstTask.Snapshot()
		tID := firstView2.ID
		tProj := firstView2.Project
		tPrompt := firstView2.CurrentPrompt
		tStatus := i18n.TaskStatusTitle(string(firstView2.Status), lang)

		return i18n.Tf(lang, "system.busy_other_tasks",
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
		return i18n.Tf(lang, "system.busy_legacy_task",
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
		return s.Send(i18n.T(config.ProjectState.GetLanguage(), "system.busy"), nil)
	}
	isSystemAction = true
	systemActionLock.Unlock()

	defer func() {
		systemActionLock.Lock()
		isSystemAction = false
		systemActionLock.Unlock()
	}()

	lang := config.ProjectState.GetLanguage()
	flags := parseSystemFlags(s.Args())
	botDir := config.BotDir
	if botDir == "" {
		return s.Send(i18n.T(lang, "system.bot_dir_missing"), ports.Rich())
	}

	cmdName := "/rebuild"
	if flags.Pull {
		cmdName = "/rebuild pull"
	}

	if warnMsg, blocked := checkActiveTasksForSystemAction(cmdName, flags, botDir, config.ProjectsRoot, lang); blocked {
		return s.Send(warnMsg, ports.Rich())
	}

	ctx := context.Background()
	chat := s.Chat()
	statusRef, _ := t.Send(ctx, chat, i18n.T(lang, "system.build_init"), ports.Rich())

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
			updateStatus(i18n.Tf(lang, "system.branch_invalid", html.EscapeString(BranchErrorText(err, lang))))
			return nil
		}

		updateStatus(i18n.Tf(lang, "system.branch_switching", html.EscapeString(targetBranch)))
		ctxCheckout, cancelCheckout := context.WithTimeout(context.Background(), 30*time.Second)
		checkoutOut, checkoutErr := performGitCheckout(ctxCheckout, botDir, targetBranch, flags.Force)
		cancelCheckout()
		if checkoutErr != nil {
			errMsg := checkoutOut
			if errMsg == "" {
				errMsg = checkoutErr.Error()
			}
			updateStatus(i18n.Tf(lang, "system.branch_failed",
				html.EscapeString(targetBranch),
				html.EscapeString(errMsg),
			))
			return nil
		}

		if flags.Pull {
			updateStatus(i18n.T(lang, "system.pulling"))
			ctxPull, cancelPull := context.WithTimeout(context.Background(), 30*time.Second)
			pullOut, pullErr := performGitPull(ctxPull, botDir, targetBranch, flags.Force)
			cancelPull()
			if pullErr != nil {
				errMsg := pullOut
				if errMsg == "" {
					errMsg = pullErr.Error()
				}
				updateStatus(i18n.Tf(lang, "system.pull_failed", html.EscapeString(errMsg)))
				return nil
			}
		}
	}

	branch := getGitBranch(botDir)
	commit := getGitCommit(botDir)

	updateStatus(i18n.Tf(lang, "system.compiling",
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
		updateStatus(i18n.Tf(lang, "system.build_failed",
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

	updateStatus(i18n.Tf(lang, "system.build_ok",
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
		return s.Send(i18n.T(config.ProjectState.GetLanguage(), "system.busy"), nil)
	}
	isSystemAction = true
	systemActionLock.Unlock()

	defer func() {
		systemActionLock.Lock()
		isSystemAction = false
		systemActionLock.Unlock()
	}()

	lang := config.ProjectState.GetLanguage()
	flags := parseSystemFlags(s.Args())
	botDir := config.BotDir
	if botDir == "" {
		return s.Send(i18n.T(lang, "system.bot_dir_missing"), ports.Rich())
	}

	if warnMsg, blocked := checkActiveTasksForSystemAction("/restart", flags, botDir, config.ProjectsRoot, lang); blocked {
		return s.Send(warnMsg, ports.Rich())
	}

	branch := getGitBranch(botDir)
	commit := getGitCommit(botDir)

	ctx := context.Background()
	chat := s.Chat()
	statusRef, _ := t.Send(ctx, chat, i18n.T(lang, "system.restart_init"), ports.Rich())

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
