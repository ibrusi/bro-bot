package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"bro-bot/internal/system"
	"bro-bot/internal/utils"
	"context"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Системные команды: приветствие, проекты, перезапуск, сборка, ресурсы и скрипты.

// handleStart — обработчик команды /start.
func handleStart(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	if len(args) > 0 {
		payload := strings.TrimSpace(args[0])
		if strings.HasPrefix(payload, "plan_") || strings.HasPrefix(payload, "planfile_") {
			rawID := strings.TrimPrefix(payload, "planfile_")
			rawID = strings.TrimPrefix(rawID, "plan_")
			if id, err := strconv.Atoi(rawID); err == nil {
				target := domain.GlobalTaskManager.GetTask(id)
				if target != nil {
					return sendTaskPlanDocument(s, target, lang)
				}
				return s.Send(i18n.Tf(lang, "task.not_found", id), ports.Rich())
			}
		}
	}

	config.ProjectState.RLock()
	curProj := config.ProjectState.CurrentProject
	curMod := config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()

	msg := i18n.Tf(lang, "start.greeting",
		html.EscapeString(curProj),
		html.EscapeString(curMod),
	)
	return s.Send(msg, ports.Rich())
}

// handleProjects — обработчик команды /projects.
func handleProjects(s ports.Session) error {
	lang := uiLang()

	entries, err := os.ReadDir(config.ProjectsRoot)
	if err != nil {
		return s.Send(i18n.Tf(lang, "projects.read_error", err), nil)
	}

	config.ProjectState.RLock()
	cur := config.ProjectState.CurrentProject
	config.ProjectState.RUnlock()

	var bldr strings.Builder
	bldr.WriteString(i18n.T(lang, "projects.header"))

	found := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		gitPath := filepath.Join(config.ProjectsRoot, e.Name(), ".git")
		if _, err := os.Stat(gitPath); err == nil {
			found = true
			if e.Name() == cur {
				bldr.WriteString(i18n.Tf(lang, "projects.active_item", html.EscapeString(e.Name())))
			} else {
				bldr.WriteString(i18n.Tf(lang, "projects.item", html.EscapeString(e.Name()), html.EscapeString(e.Name())))
			}
		}
	}

	if !found {
		return s.Send(i18n.T(lang, "projects.empty"), ports.Rich())
	}

	bldr.WriteString(i18n.T(lang, "projects.footer"))
	return s.Send(bldr.String(), ports.Rich())
}

// handleUse — обработчик команды /use.
func handleUse(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	if len(args) == 0 {
		return s.Send(i18n.T(lang, "projects.use_usage"), ports.Rich())
	}

	target := strings.TrimSpace(args[0])

	// Без проверки имени "/use ../../etc" сделал бы рабочим каталогом агента
	// произвольную директорию хоста.
	targetPath, err := utils.SafeJoinSegment(config.ProjectsRoot, target)
	if err != nil {
		return s.Send(i18n.Tf(lang, "projects.use_invalid", html.EscapeString(ErrorText(err, lang))), ports.Rich())
	}

	if fi, err := os.Stat(targetPath); err != nil || !fi.IsDir() {
		return s.Send(i18n.Tf(lang, "projects.use_not_found", html.EscapeString(target)), ports.Rich())
	}

	config.ProjectState.Lock()
	config.ProjectState.CurrentProject = target
	config.ProjectState.Unlock()

	if st := domain.GlobalTaskManager.Storage(); st != nil {
		_ = st.SetSetting(context.Background(), "current_project", target)
	}

	return s.Send(i18n.Tf(lang, "projects.use_switched", html.EscapeString(target)), ports.Rich())
}

// handleRestart — обработчик команды /restart; транспорт нужен системным действиям.
func handleRestart(t ports.Transport) ports.Handler {
	return func(s ports.Session) error {
		return system.HandleRestart(t, s)
	}
}

// handleRebuild — обработчик команды /rebuild; транспорт нужен системным действиям.
func handleRebuild(t ports.Transport) ports.Handler {
	return func(s ports.Session) error {
		return system.HandleRebuild(t, s)
	}
}

// handleBuild — обработчик команды /build; транспорт нужен системным действиям.
func handleBuild(t ports.Transport) ports.Handler {
	return func(s ports.Session) error {
		return system.HandleRebuild(t, s)
	}
}

// handleResources — общий обработчик нескольких команд.
func handleResources(s ports.Session) error {
	report := system.CollectResourceReport(true)
	return s.Send(system.FormatResourcesMessage(report, uiLang()), ports.Rich())
}

// handleScript — обработчик команды /script.
func handleScript(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	if len(args) == 0 {
		return s.Send(i18n.T(lang, "script.usage"), nil)
	}
	scriptName := args[0]
	if config.ScriptsDir == "" {
		return s.Send(i18n.T(lang, "script.dir_missing"), nil)
	}

	// Проверка префикса без разделителя пропускала каталог-сосед:
	// "../scripts-evil/x" при корне "/opt/scripts" давал "/opt/scripts-evil/x".
	scriptPath, err := utils.SafeJoin(config.ScriptsDir, scriptName)
	if err != nil {
		return s.Send(i18n.Tf(lang, "script.bad_name", ErrorText(err, lang)), nil)
	}
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return s.Send(i18n.Tf(lang, "script.not_found", scriptName, config.ScriptsDir), nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), scriptTimeout)
	defer cancel()

	out, runErr := exec.CommandContext(ctx, scriptPath).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return s.Send(i18n.Tf(lang, "script.timeout", scriptName, scriptTimeout), nil)
	}

	msg := i18n.Tf(lang, "script.result", scriptName, utils.TruncateString(string(out), scriptOutputLimit))
	if runErr != nil {
		msg += i18n.Tf(lang, "script.error", runErr)
	}
	return s.Send(msg, nil)
}

const (
	// scriptTimeout ограничивает время работы скрипта из /script: без него зависший
	// скрипт держал бы обработчик команды бесконечно.
	scriptTimeout = 2 * time.Minute
	// scriptOutputLimit — сколько символов вывода скрипта уходит в чат.
	scriptOutputLimit = 3500
)
