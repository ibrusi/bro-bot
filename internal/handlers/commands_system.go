package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/ports"
	"bro-bot/internal/system"
	"bro-bot/internal/utils"
	"context"
	"fmt"
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
			"<b>Диалог:</b>\n"+
			"• Обычное сообщение — вопрос агенту по проекту, контекст разговора сохраняется\n"+
			"• /chat [вопрос|new|stop] — статус разговора, новый разговор или остановка ответа\n"+
			"• /chatmode [on|off] — отвечать в чате или сразу создавать задачу\n\n"+
			"<b>Задачи:</b>\n"+
			"• /tasks — список задач и быстрое переключение\n"+
			"• /task &lt;id&gt; [текст] — переключить фокус на задачу или дополнить её\n"+
			"• /plan [проект] &lt;текст&gt; — составить план и утвердить перед реализацией\n"+
			"• /planmode [on|off] — включить обязательный план для всех задач\n"+
			"• /approve [id] — утвердить план задачи и начать реализацию\n"+
			"• /add [id] &lt;текст&gt; — отправить дополнение конкретной задаче\n"+
			"• /new [проект] [агент] &lt;текст&gt; — создать новую задачу (с выбором проекта и агента)\n"+
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
			"• /restart, /rebuild [branch=имя] [pull] [force] — управление процессом и пересборка бота\n\n"+
			"💡 <i>Обычное сообщение — это разговор с агентом. Если бот увидит запрос на изменение кода, он предложит составить план или создать задачу. Прямые команды: /plan &lt;задача&gt; и /new &lt;задача&gt;, дополнения — через /add [id] &lt;текст&gt; или ответом на сообщения бота.</i>",
		html.EscapeString(curProj),
		html.EscapeString(curMod),
	)
	return s.Send(msg, ports.Rich())
}

// handleProjects — обработчик команды /projects.
func handleProjects(s ports.Session) error {
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
}

// handleUse — обработчик команды /use.
func handleUse(s ports.Session) error {
	args := s.Args()
	if len(args) == 0 {
		return s.Send("Укажите имя проекта. Пример: <code>/use my-repo</code>", ports.Rich())
	}

	target := strings.TrimSpace(args[0])

	// Без проверки имени "/use ../../etc" сделал бы рабочим каталогом агента
	// произвольную директорию хоста.
	targetPath, err := utils.SafeJoinSegment(config.ProjectsRoot, target)
	if err != nil {
		return s.Send(fmt.Sprintf("❌ Недопустимое имя проекта: %s", html.EscapeString(err.Error())), ports.Rich())
	}

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
	msg := system.FormatResourcesMessage(report)
	return s.Send(msg, ports.Rich())
}

// handleScript — обработчик команды /script.
func handleScript(s ports.Session) error {
	args := s.Args()
	if len(args) == 0 {
		return s.Send("Пожалуйста, укажите название скрипта: /script <name>", nil)
	}
	scriptName := args[0]
	if config.ScriptsDir == "" {
		return s.Send("Директория скриптов не настроена (SCRIPTS_DIR)", nil)
	}

	// Проверка префикса без разделителя пропускала каталог-сосед:
	// "../scripts-evil/x" при корне "/opt/scripts" давал "/opt/scripts-evil/x".
	scriptPath, err := utils.SafeJoin(config.ScriptsDir, scriptName)
	if err != nil {
		return s.Send(fmt.Sprintf("Недопустимое имя скрипта: %s", err.Error()), nil)
	}
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return s.Send(fmt.Sprintf("Скрипт %s не найден в %s", scriptName, config.ScriptsDir), nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), scriptTimeout)
	defer cancel()

	out, runErr := exec.CommandContext(ctx, scriptPath).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return s.Send(fmt.Sprintf("Скрипт %s прерван по таймауту (%s)", scriptName, scriptTimeout), nil)
	}

	msg := fmt.Sprintf("Результат выполнения %s:\n\n%s", scriptName, utils.TruncateString(string(out), scriptOutputLimit))
	if runErr != nil {
		msg += fmt.Sprintf("\nОшибка: %v", runErr)
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
