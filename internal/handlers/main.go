package handlers

import (
	"bro-bot/internal/agents"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/models"
	"bro-bot/internal/ports"
	"bro-bot/internal/storage"
	"bro-bot/internal/system"
	"bro-bot/internal/utils"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// activeAgentMu защищает активного агента бота: его меняют команды /agent, /mode и /new <агент>,
// а читают фоновые горутины пайплайна задач. Адаптер и имя лежат под одним мьютексом и читаются
// парой: код сравнивает их между собой, и увидеть новое имя со старым адаптером нельзя.
var (
	activeAgentMu   sync.RWMutex
	activeAgent     ports.AgentFramework
	activeAgentName = "agy"
)

// ActiveAgent возвращает согласованную пару «адаптер + имя активного агента».
func ActiveAgent() (ports.AgentFramework, string) {
	activeAgentMu.RLock()
	defer activeAgentMu.RUnlock()
	return activeAgent, activeAgentName
}

// ActiveAgentName возвращает имя активного агента.
func ActiveAgentName() string {
	activeAgentMu.RLock()
	defer activeAgentMu.RUnlock()
	return activeAgentName
}

// SetActiveAgent атомарно задаёт активного агента и его имя.
// Пустое имя означает агента по умолчанию из реестра. Реестр читаем до захвата
// activeAgentMu: он остаётся листовым мьютексом и не берётся внутри чужих блокировок.
func SetActiveAgent(framework ports.AgentFramework, name string) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		name = registry().Default()
	}

	activeAgentMu.Lock()
	defer activeAgentMu.Unlock()
	activeAgent = framework
	activeAgentName = name
}

// Start настраивает бота и регистрирует обработчики. Реестр агентов приходит из корня
// композиции: обработчики не зависят от конкретных адаптеров. Конфигурация приходит
// оттуда же готовым снимком — окружение здесь уже не читается.
//
// Ошибки возвращаются наверх: решение завершить процесс принимает корень композиции,
// а не библиотечный пакет.
func Start(t ports.Transport, reg *agents.Registry, cfg config.Config) error {
	setAgentRegistry(reg)
	config.Apply(cfg)

	sqliteStorage, err := storage.NewSQLiteStorage(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("handlers: cannot initialize the SQLite database: %w", err)
	}
	domain.GlobalTaskManager.InitWithStorage(sqliteStorage)
	domain.GlobalChatManager.InitWithStorage(sqliteStorage)

	models.GlobalModelRegistry = models.NewModelRegistry(10 * time.Minute)

	initDefaultProject(cfg.ProjectsRoot, cfg.DefaultProject)
	initDefaultModel(cfg.DefaultModel)

	// Восстанавливаем сохраненные настройки из базы данных
	ctx := context.Background()
	if savedProj, err := sqliteStorage.GetSetting(ctx, "current_project"); err == nil && savedProj != "" {
		// Значение приходит из базы, но проверяем его так же, как ввод пользователя:
		// в базу оно когда-то попало из команды /use.
		if projPath, pathErr := utils.SafeJoinSegment(config.ProjectsRoot, savedProj); pathErr != nil {
			log.Printf("stored project %q rejected: %v", savedProj, pathErr)
		} else if fi, err := os.Stat(projPath); err == nil && fi.IsDir() {
			config.ProjectState.Lock()
			config.ProjectState.CurrentProject = savedProj
			config.ProjectState.Unlock()
			log.Printf("restored the active project from SQLite: %s", savedProj)
		}
	}
	if savedModel, err := sqliteStorage.GetSetting(ctx, "current_model"); err == nil && savedModel != "" {
		config.ProjectState.Lock()
		config.ProjectState.CurrentModel = savedModel
		config.ProjectState.Unlock()
		log.Printf("restored the active model from SQLite: %s", savedModel)
	}
	if savedPlanMode, err := sqliteStorage.GetSetting(ctx, "plan_mode"); err == nil && savedPlanMode != "" {
		if pm, err := strconv.ParseBool(savedPlanMode); err == nil {
			config.ProjectState.Lock()
			config.ProjectState.PlanMode = pm
			config.ProjectState.Unlock()
			log.Printf("restored PlanMode from SQLite: %v", pm)
		}
	}
	if savedMode, err := sqliteStorage.GetSetting(ctx, "execution_mode"); err == nil && savedMode != "" {
		config.ProjectState.SetExecutionMode(savedMode)
		log.Printf("restored the execution mode from SQLite: %s", savedMode)
	} else {
		config.ProjectState.SetExecutionMode(agents.ModeMCP)
	}
	if savedInteraction, err := sqliteStorage.GetSetting(ctx, "interaction_mode"); err == nil && savedInteraction != "" {
		config.ProjectState.SetInteractionMode(savedInteraction)
		log.Printf("restored the interaction mode from SQLite: %s", savedInteraction)
	}

	if savedLang, err := sqliteStorage.GetSetting(ctx, "bot_language"); err == nil && savedLang != "" {
		config.ProjectState.SetLanguage(savedLang)
		log.Printf("restored the interface language from SQLite: %s", savedLang)
	}

	savedAgent, _ := sqliteStorage.GetSetting(ctx, "current_agent")
	initActiveAgent(savedAgent)

	registerBotCommands(t)

	t.Use(authMiddleware(config.AdminID))

	onCmd := func(name string, h ports.Handler) {
		registerCommandHandler(name, h)
		t.OnCommand(name, h)
	}

	onCmd("start", handleStart)
	onCmd("tasks", handleTasks)
	t.OnCallback("task_sel", onTaskSel)
	onCmd("status", handleStatus)
	onCmd("tokens", handleTokens)
	onCmd("stats", handleStats)
	onCmd("context", handleContext)
	onCmd("models", handleModels)
	onCmd("model", handleModel)
	onCmd("agent", handleAgent)
	onCmd("mode", handleMode)
	onCmd("usage", handleUsage)
	onCmd("limits", handleUsage)
	onCmd("projects", handleProjects)
	onCmd("use", handleUse)
	onCmd("clone", handleCloneCommand)
	onCmd("task", handleTask)
	onCmd("add", handleAdd)
	onCmd("new", handleNew)
	onCmd("plan", handlePlan)
	onCmd("planmode", handlePlanMode)
	onCmd("chat", handleChat)
	onCmd("chatmode", handleChatMode)
	t.OnCallback("chat_mode_toggle", onChatModeToggle)
	onCmd("language", handleLanguage)
	t.OnCallback("lang_sel", onLanguageSel)
	t.OnCallback("chat_answer", onChatAnswer)
	t.OnCallback("chat_plan", onChatPlan)
	t.OnCallback("chat_task", onChatTask)
	onCmd("planfile", handlePlanFile)
	onCmd("history", handleHistory)
	onCmd("approve", handleApprove)
	onCmd("confirm", handleConfirm)
	t.OnCallback("plan_approve", onPlanApprove)
	t.OnCallback("plan_cancel", onPlanCancel)
	t.OnCallback("plan_mode_toggle", onPlanModeToggle)
	onCmd("cancel", handleCancel)
	t.OnCallback("plan_appr_var", onPlanApproveVariant)
	t.OnCallback("plan_doc", onPlanDoc)
	t.OnCallback("q_choice", onQuestionChoice)
	t.OnCallback("q_pause", onQuestionPause)
	t.OnCallback("q_resume", onQuestionResume)
	t.OnCallback("task_agent_restart", onTaskAgentRestart)
	t.OnCallback("task_agent_switch", onTaskAgentSwitch)
	onCmd("pause", handlePause)
	onCmd("resume", handleResume)
	onCmd("retry", handleRetry)
	onCmd("restart", handleRestart(t))
	onCmd("rebuild", handleRebuild(t))
	onCmd("build", handleBuild(t))
	onCmd("top", handleResources)
	onCmd("ps", handleResources)
	onCmd("resources", handleResources)
	onCmd("res", handleResources)
	onCmd("script", handleScript)
	t.OnText(handleText)
	t.OnVoice(handleVoice)

	// Каталог передаётся значением, а не читается из config внутри горутины: она
	// просыпается через полторы секунды, и в тестах к этому моменту следующий Start
	// уже переписывает снимок — детектор гонок это заметит.
	go system.CheckAndNotifyRestart(t, cfg.AdminID, cfg.BotDir)

	log.Println("multi-project agent bot started...")
	return t.Start(context.Background())
}

func initDefaultProject(root, defaultProject string) {
	if defaultProject != "" {
		targetDir := filepath.Join(root, defaultProject)
		if info, err := os.Stat(targetDir); err == nil && info.IsDir() {
			config.ProjectState.Lock()
			config.ProjectState.CurrentProject = defaultProject
			config.ProjectState.Unlock()
			log.Printf("initialized the default project: %s", defaultProject)
			return
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		log.Printf("warning: cannot read the projects directory %s: %v", root, err)
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			config.ProjectState.Lock()
			config.ProjectState.CurrentProject = e.Name()
			config.ProjectState.Unlock()
			log.Printf("initialized the first project found: %s", e.Name())
			return
		}
	}
}

// initDefaultModel разрешает псевдоним модели один раз и публикует результат в
// config.DefaultModel. Раньше DEFAULT_MODEL читали три места с разными запасными
// значениями, и псевдоним flash давал в них разные модели.
func initDefaultModel(defaultModel string) {
	if models.GlobalModelRegistry != nil {
		if resolved, ok := models.GlobalModelRegistry.ResolveModel(defaultModel); ok {
			defaultModel = resolved
		}
	}
	config.DefaultModel = defaultModel
	config.ProjectState.Lock()
	config.ProjectState.CurrentModel = defaultModel
	config.ProjectState.Unlock()
	log.Printf("initialized the default model: %s", defaultModel)
}

// getDefaultCommands возвращает список команд для меню подсказок мессенджера на
// языке lang. Описания живут в каталоге локалей, поэтому новый язык получает своё
// меню без правок здесь.
func getDefaultCommands(lang string) []ports.BotCommand {
	agentNames := strings.Join(registry().Names(), "|")

	names := []string{
		"status", "tasks", "task", "plan", "planmode", "approve", "planfile", "history",
		"add", "chat", "chatmode", "new", "resume", "retry", "pause", "cancel",
		"tokens", "context", "top", "usage", "models", "model", "agent", "mode",
		"projects", "use", "clone", "language", "restart", "rebuild", "start",
	}

	cmds := make([]ports.BotCommand, 0, len(names))
	for _, name := range names {
		description := i18n.T(lang, "cmd."+name)
		if name == "agent" {
			description = i18n.Tf(lang, "cmd.agent", agentNames)
		}
		cmds = append(cmds, ports.BotCommand{Name: name, Description: description})
	}
	return cmds
}

// registerBotCommands публикует меню подсказок для каждого загруженного языка.
// Мессенджер сам выбирает подходящее по языку клиента, а пустой код — это набор
// по умолчанию: его увидят клиенты с языком, для которого каталога нет.
func registerBotCommands(m ports.Messenger) {
	ctx := context.Background()
	if err := m.SetCommands(ctx, getDefaultCommands(i18n.Default), ""); err != nil {
		log.Printf("warning: cannot register the default command menu: %v", err)
	}
	for _, lang := range i18n.Codes() {
		if err := m.SetCommands(ctx, getDefaultCommands(lang), lang); err != nil {
			log.Printf("warning: cannot register the command menu for %q: %v", lang, err)
		}
	}
}
