package handlers

import (
	"bro-bot/internal/agents"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
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
		return fmt.Errorf("не удалось инициализировать SQLite базу данных: %w", err)
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
			log.Printf("Сохранённый проект %q отклонён: %v", savedProj, pathErr)
		} else if fi, err := os.Stat(projPath); err == nil && fi.IsDir() {
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
	if savedMode, err := sqliteStorage.GetSetting(ctx, "execution_mode"); err == nil && savedMode != "" {
		config.ProjectState.SetExecutionMode(savedMode)
		log.Printf("Восстановлен режим выполнения из SQLite: %s", savedMode)
	}
	if savedInteraction, err := sqliteStorage.GetSetting(ctx, "interaction_mode"); err == nil && savedInteraction != "" {
		config.ProjectState.SetInteractionMode(savedInteraction)
		log.Printf("Восстановлен режим взаимодействия из SQLite: %s", savedInteraction)
	}

	if savedLang, err := sqliteStorage.GetSetting(ctx, "bot_language"); err == nil && savedLang != "" {
		config.ProjectState.SetLanguage(savedLang)
		log.Printf("Восстановлен язык интерфейса из SQLite: %s", savedLang)
	}

	savedAgent, _ := sqliteStorage.GetSetting(ctx, "current_agent")
	initActiveAgent(savedAgent)

	if err := t.SetCommands(context.Background(), getDefaultCommands("en"), ""); err != nil {
		log.Printf("Предупреждение: не удалось зарегистрировать команды: %v", err)
	}
	if err := t.SetCommands(context.Background(), getDefaultCommands("ru"), "ru"); err != nil {
		log.Printf("Предупреждение: не удалось зарегистрировать команды для ru: %v", err)
	}

	t.Use(authMiddleware(config.AdminID))

	t.OnCommand("start", handleStart)
	t.OnCommand("tasks", handleTasks)
	t.OnCallback("task_sel", onTaskSel)
	t.OnCommand("status", handleStatus)
	t.OnCommand("tokens", handleTokens)
	t.OnCommand("stats", handleStats)
	t.OnCommand("context", handleContext)
	t.OnCommand("models", handleModels)
	t.OnCommand("model", handleModel)
	t.OnCommand("agent", handleAgent)
	t.OnCommand("mode", handleMode)
	t.OnCommand("usage", handleUsage)
	t.OnCommand("limits", handleUsage)
	t.OnCommand("projects", handleProjects)
	t.OnCommand("use", handleUse)
	t.OnCommand("clone", handleCloneCommand)
	t.OnCommand("task", handleTask)
	t.OnCommand("add", handleAdd)
	t.OnCommand("new", handleNew)
	t.OnCommand("plan", handlePlan)
	t.OnCommand("planmode", handlePlanMode)
	t.OnCommand("chat", handleChat)
	t.OnCommand("chatmode", handleChatMode)
	t.OnCallback("chat_mode_toggle", onChatModeToggle)
	t.OnCommand("language", handleLanguage)
	t.OnCallback("lang_sel", onLanguageSel)
	t.OnCallback("chat_answer", onChatAnswer)
	t.OnCallback("chat_plan", onChatPlan)
	t.OnCallback("chat_task", onChatTask)
	t.OnCommand("planfile", handlePlanFile)
	t.OnCommand("history", handleHistory)
	t.OnCommand("approve", handleApprove)
	t.OnCommand("confirm", handleConfirm)
	t.OnCallback("plan_approve", onPlanApprove)
	t.OnCallback("plan_cancel", onPlanCancel)
	t.OnCallback("plan_mode_toggle", onPlanModeToggle)
	t.OnCommand("cancel", handleCancel)
	t.OnCallback("plan_appr_var", onPlanApproveVariant)
	t.OnCallback("plan_doc", onPlanDoc)
	t.OnCallback("q_choice", onQuestionChoice)
	t.OnCallback("q_pause", onQuestionPause)
	t.OnCallback("q_resume", onQuestionResume)
	t.OnCallback("task_agent_restart", onTaskAgentRestart)
	t.OnCallback("task_agent_switch", onTaskAgentSwitch)
	t.OnCommand("pause", handlePause)
	t.OnCommand("resume", handleResume)
	t.OnCommand("retry", handleRetry)
	t.OnCommand("restart", handleRestart(t))
	t.OnCommand("rebuild", handleRebuild(t))
	t.OnCommand("build", handleBuild(t))
	t.OnCommand("top", handleResources)
	t.OnCommand("ps", handleResources)
	t.OnCommand("resources", handleResources)
	t.OnCommand("res", handleResources)
	t.OnCommand("script", handleScript)
	t.OnText(handleText)

	// Каталог передаётся значением, а не читается из config внутри горутины: она
	// просыпается через полторы секунды, и в тестах к этому моменту следующий Start
	// уже переписывает снимок — детектор гонок это заметит.
	go system.CheckAndNotifyRestart(t, cfg.AdminID, cfg.BotDir)

	log.Println("Мультипроектный агент-бот запущен...")
	return t.Start(context.Background())
}

func initDefaultProject(root, defaultProject string) {
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
	log.Printf("Инициализирована модель по умолчанию: %s", defaultModel)
}

// getDefaultCommands возвращает список команд для регистрации в мессенджере (меню подсказок).
func getDefaultCommands(lang string) []ports.BotCommand {
	if lang == "ru" {
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
			{Name: "chat", Description: "[вопрос|new|stop] Разговор с агентом по текущему проекту"},
			{Name: "chatmode", Description: "[on|off] Отвечать в чате вместо создания задачи"},
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
			{Name: "agent", Description: "[" + strings.Join(registry().Names(), "|") + "] Переключить активный агент"},
			{Name: "mode", Description: "[cli|api] Переключить режим (CLI или API)"},
			{Name: "projects", Description: "Список доступных проектов"},
			{Name: "use", Description: "<имя> Переключить активный проект"},
			{Name: "clone", Description: "<url> [имя] Клонировать git-репозиторий"},
			{Name: "language", Description: "Сменить язык интерфейса бота"},
			{Name: "restart", Description: "Перезапустить бота"},
			{Name: "rebuild", Description: "[branch=имя] [pull] [force] Собрать и перезапустить бота"},
			{Name: "start", Description: "Перезапуск и приветственное сообщение"},
		}
	}
	return []ports.BotCommand{
		{Name: "status", Description: "[id] Status of current task, logs and queue"},
		{Name: "tasks", Description: "List all tasks and switch"},
		{Name: "task", Description: "<id> [text] Focus on task or append to it"},
		{Name: "plan", Description: "[project] <text> Create a plan for a new task"},
		{Name: "planmode", Description: "[on|off] Toggle mandatory planning"},
		{Name: "approve", Description: "[id] Approve plan and start execution"},
		{Name: "planfile", Description: "[id] Download full task plan as .md file"},
		{Name: "history", Description: "[id] Show conversation history of task session"},
		{Name: "add", Description: "[id] <text> Append text to a task"},
		{Name: "chat", Description: "[question|new|stop] Chat with agent in current project"},
		{Name: "chatmode", Description: "[on|off] Reply in chat instead of creating a task"},
		{Name: "new", Description: "[project] [agent] <text> Create new task"},
		{Name: "resume", Description: "[id] [reply] Resume task or send reply"},
		{Name: "retry", Description: "[id] Restart task from scratch"},
		{Name: "pause", Description: "[id] Pause task execution"},
		{Name: "cancel", Description: "[id] Cancel task"},
		{Name: "tokens", Description: "Token, speed and cache statistics"},
		{Name: "context", Description: "[id] Context window distribution"},
		{Name: "top", Description: "CPU/memory monitoring of bot, agy, and claude"},
		{Name: "usage", Description: "API usage costs and account limits"},
		{Name: "models", Description: "List available models"},
		{Name: "model", Description: "[name] Switch active model"},
		{Name: "agent", Description: "[" + strings.Join(registry().Names(), "|") + "] Switch active agent"},
		{Name: "mode", Description: "[cli|api] Switch mode (CLI or API)"},
		{Name: "projects", Description: "List available projects"},
		{Name: "use", Description: "<name> Switch active project"},
		{Name: "clone", Description: "<url> [name] Clone git repository"},
		{Name: "language", Description: "Change bot interface language"},
		{Name: "restart", Description: "Restart the bot"},
		{Name: "rebuild", Description: "[branch=name] [pull] [force] Build and restart the bot"},
		{Name: "start", Description: "Restart and welcome message"},
	}
}
