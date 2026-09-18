package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
	"bufio"
	"context"
	"fmt"
	"html"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// chatSuggestionTTL — сколько живёт карточка «ответить/план/задача» (идентификаторы кнопок
	// ограничены 64 байтами, поэтому текст запроса храним в памяти).
	chatSuggestionTTL = time.Hour
	// chatAnswerInlineLimit — до какой длины ответ помещается прямо в статусное сообщение.
	chatAnswerInlineLimit = 3500
	// chatAnswerDocumentLimit — начиная с какой длины ответ отправляется файлом.
	chatAnswerDocumentLimit = 12000
	// chatBootstrapTurns / chatBootstrapChars — размер контекста, подмешиваемого новому CLI-агенту.
	chatBootstrapTurns = 12
	chatBootstrapChars = 6000
)

// chatSystemPreamble — инструкция диалогового режима: агент изучает код и отвечает, но не меняет его.
func chatSystemPreamble() string {
	return "РЕЖИМ ДИАЛОГА. Ты отвечаешь на вопросы пользователя о проекте в чате.\n" +
		"Разрешено: читать файлы, искать по коду, выполнять безопасные read-only команды (git log, git status, ls, grep), объяснять архитектуру и предлагать решения словами.\n" +
		"ЗАПРЕЩЕНО: создавать git-ветки, изменять, создавать и удалять файлы, делать commit и push, открывать Pull Request, запускать миграции и деплой.\n" +
		"Если для ответа нужна правка кода — не выполняй её, а коротко опиши, что именно нужно сделать: пользователь оформит это отдельной задачей.\n" +
		"Отвечай кратко, по делу и на русском языке."
}

// chatShortReminder — короткое напоминание о режиме для последующих ходов CLI-агента.
const chatShortReminder = "(режим диалога: только чтение и ответ, без изменений в файлах и git)"

// buildChatPrompt собирает промпт хода диалога.
//
// В api-режиме преамбула уходит отдельным системным полем, а история — отдельным списком реплик,
// поэтому в промпте остаётся только текст пользователя. В cli-режиме у агента своя память сессии:
// при первом ходе новой пары агент/режим подмешиваем преамбулу и краткий контекст прошлых реплик,
// дальше достаточно короткого напоминания.
func buildChatPrompt(session *domain.ChatSession, agent, mode, userText string) string {
	if strings.EqualFold(mode, "api") {
		return userText
	}

	firstTurn := session.ConversationIDFor(agent, mode) == ""
	if !firstTurn {
		return fmt.Sprintf("%s\n\n%s", chatShortReminder, userText)
	}

	var bldr strings.Builder
	bldr.WriteString(chatSystemPreamble())

	if session.NeedsContextBootstrap(agent, mode) {
		if prev := formatChatContext(session.HistoryForPrompt(chatBootstrapTurns, chatBootstrapChars)); prev != "" {
			bldr.WriteString("\n\nКОНТЕКСТ ПРЕДЫДУЩЕГО РАЗГОВОРА (перенесён с другого агента или режима):\n")
			bldr.WriteString(prev)
		}
	}

	bldr.WriteString("\n\nВОПРОС ПОЛЬЗОВАТЕЛЯ:\n")
	bldr.WriteString(userText)
	return bldr.String()
}

// formatChatContext оформляет реплики разговора в текстовый блок для промпта.
func formatChatContext(turns []domain.ChatTurn) string {
	var bldr strings.Builder
	for _, turn := range turns {
		speaker := "Пользователь"
		if turn.Role == domain.ChatRoleAssistant {
			speaker = "Ты"
		}
		bldr.WriteString(fmt.Sprintf("%s: %s\n", speaker, turn.Content))
	}
	return strings.TrimSpace(bldr.String())
}

// chatHistoryForAgent возвращает историю диалога в формате порта.
// В cli-режиме история не передаётся: агент восстанавливает её сам по идентификатору сессии.
func chatHistoryForAgent(session *domain.ChatSession, mode string) []ports.ChatMessage {
	if !strings.EqualFold(mode, "api") {
		return nil
	}
	turns := session.HistoryForPrompt(domain.MaxChatHistoryTurns, domain.MaxChatHistoryChars)
	if len(turns) == 0 {
		return nil
	}
	history := make([]ports.ChatMessage, 0, len(turns))
	for _, turn := range turns {
		history = append(history, ports.ChatMessage{Role: string(turn.Role), Content: turn.Content})
	}
	return history
}

// maxPendingAnswerAge — сколько времени приостановленная задача продолжает считаться ждущей ответа.
const maxPendingAnswerAge = 6 * time.Hour

// isAwaitingAnswer сообщает, что задача остановилась на неотвеченном вопросе агента.
func isAwaitingAnswer(task *domain.TaskSession) bool {
	if task == nil {
		return false
	}
	task.Lock()
	defer task.Unlock()
	return task.Status == domain.TaskStatusPaused && strings.TrimSpace(task.LastQuestion) != ""
}

// hasFreshPendingQuestion сообщает, что задача ждёт ответа на недавно заданный вопрос:
// только в этом случае обычное сообщение уходит в задачу, а не в диалог.
func hasFreshPendingQuestion(task *domain.TaskSession) bool {
	if !isAwaitingAnswer(task) {
		return false
	}
	task.Lock()
	askedAt := task.QuestionAskedAt
	task.Unlock()

	if askedAt.IsZero() {
		return false
	}
	return time.Since(askedAt) <= maxPendingAnswerAge
}

// handleTextInChatMode обрабатывает обычное сообщение в диалоговом режиме:
// вопросы уходят агенту, запросы на изменение кода — в карточку выбора.
func handleTextInChatMode(s ports.Session, text string) error {
	return handleTextInChatModeWithNote(s, text, "")
}

// handleTextInChatModeWithNote дополнительно добавляет к ответу строку-подсказку
// (например, напоминание о задаче, которая всё ещё ждёт ответа).
func handleTextInChatModeWithNote(s ports.Session, text, note string) error {
	config.ProjectState.RLock()
	curProj := config.ProjectState.CurrentProject
	config.ProjectState.RUnlock()

	targetProj, targetAgent, prompt := parseNewTaskInput(text, curProj, ActiveAgentName())
	if strings.TrimSpace(prompt) == "" {
		return nil
	}
	if targetProj == "" {
		return s.Send("❌ Сначала выберите проект: /projects", nil)
	}

	intent, reason := domain.ClassifyMessageReason(prompt)
	if intent == domain.IntentWork {
		log.Printf("Диалог: сообщение распознано как запрос на изменение кода (правило %s)", reason)
		return sendWorkSuggestion(s, targetProj, targetAgent, prompt)
	}

	return startChatTurn(s.Messenger(), s.Chat(), targetProj, targetAgent, prompt, note)
}

// handleChatMessage запускает ход диалога, минуя классификатор (команда /chat и кнопка «Ответить в чате»).
func handleChatMessage(s ports.Session, text string) error {
	config.ProjectState.RLock()
	curProj := config.ProjectState.CurrentProject
	config.ProjectState.RUnlock()

	targetProj, targetAgent, prompt := parseNewTaskInput(text, curProj, ActiveAgentName())
	if strings.TrimSpace(prompt) == "" {
		return s.Send("Использование: <code>/chat &lt;вопрос&gt;</code>", ports.Rich())
	}
	if targetProj == "" {
		return s.Send("❌ Сначала выберите проект: /projects", nil)
	}
	return startChatTurn(s.Messenger(), s.Chat(), targetProj, targetAgent, prompt, "")
}

// chatTurnSetup — параметры хода диалога, снятые в момент запуска.
// Агент, режим и адаптер фиксируются здесь, на горутине обработчика: фоновый ход
// не читает глобальное состояние, которое пользователь может поменять командой /agent или /mode.
type chatTurnSetup struct {
	Project   string
	Agent     string
	Mode      string
	Model     string
	Framework ports.AgentFramework
}

// startChatTurn ставит ход диалога в работу либо откладывает сообщение, если ход уже идёт.
func startChatTurn(m ports.Messenger, chat ports.ChatID, project, agent, text, note string) error {
	config.ProjectState.RLock()
	model := config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()

	setup := chatTurnSetup{
		Project: project,
		Agent:   normalizeAgentName(agent),
		Mode:    config.ProjectState.GetExecutionMode(),
		Model:   model,
	}

	framework, err := agentFrameworkFor(setup.Agent)
	if err != nil {
		return sendPlain(m, chat, fmt.Sprintf("❌ %s", err.Error()))
	}
	setup.Framework = framework

	session := domain.GlobalChatManager.GetOrCreate(project, model)

	ctx, cancel := context.WithTimeout(context.Background(), chatTimeout())
	if !session.BeginTurn(cancel) {
		cancel()
		queued := session.EnqueuePending(text)
		return sendPlain(m, chat, fmt.Sprintf("📥 <b>Учту после текущего ответа</b> (в очереди: %d)", queued))
	}

	go runChatLoop(ctx, cancel, m, chat, session, setup, text, note)
	return nil
}

// runChatLoop выполняет ход диалога и разбирает сообщения, пришедшие во время ответа.
func runChatLoop(ctx context.Context, cancel context.CancelFunc, m ports.Messenger, chat ports.ChatID,
	session *domain.ChatSession, setup chatTurnSetup, text, note string) {
	defer session.EndTurn()

	for {
		runChatTurn(ctx, m, chat, session, setup, text, note)
		cancel()

		pending := session.DrainPending()
		if len(pending) == 0 {
			return
		}
		text = strings.Join(pending, "\n")
		note = ""
		ctx, cancel = context.WithTimeout(context.Background(), chatTimeout())
		session.SetTurnCancel(cancel)
	}
}

// chatResult — результат одного хода диалога.
type chatResult struct {
	Answer         string
	ConversationID string
	Usage          *domain.UsageStats
	Duration       float64
	Status         string
	Err            error
}

// runChatTurn выполняет один ход диалога: запускает агента, показывает прогресс и отправляет ответ.
// Функция не зависит от агента и режима — различия спрятаны в адаптерах и в сборке аргументов.
func runChatTurn(ctx context.Context, m ports.Messenger, chat ports.ChatID, session *domain.ChatSession,
	setup chatTurnSetup, userText, note string) {
	project, agent, mode, model := setup.Project, setup.Agent, setup.Mode, setup.Model

	statusText := fmt.Sprintf("💭 <b>Думаю…</b> <code>%s</code> [<code>%s</code>] · %s/%s",
		html.EscapeString(project), html.EscapeString(model), html.EscapeString(agent), html.EscapeString(mode))
	if domain.GlobalTaskManager.HasRunningTaskInProject(project) {
		statusText += "\n<i>⚠️ В проекте выполняется задача — отвечаю, не трогая файлы.</i>"
	}
	// Статусное сообщение диалога намеренно не регистрируется как сообщение задачи:
	// таблица сообщений ссылается на tasks(id), а у разговора строки задачи нет.
	statusRef, _ := m.Send(context.Background(), chat, statusText, ports.Rich())

	workDir := filepath.Join(config.ProjectsRoot, project)
	args := ports.ExecuteArgs{
		ConversationID: session.ConversationIDFor(agent, mode),
		ModelName:      model,
		Prompt:         buildChatPrompt(session, agent, mode, userText),
		WorkDir:        workDir,
		History:        chatHistoryForAgent(session, mode),
	}
	if strings.EqualFold(mode, "api") {
		args.SystemPrompt = chatSystemPreamble()
	}

	startedAt := time.Now()
	res := streamChatAnswer(ctx, m, chat, statusRef, setup.Framework, args, setup)

	if res.ConversationID != "" {
		session.SetConversationID(agent, mode, res.ConversationID)
	}

	if res.Err != nil || res.Status == "ERROR" {
		message := "❌ <b>Не удалось получить ответ</b>"
		if ctx.Err() == context.Canceled {
			// Пользователь сам остановил ответ командой /chat stop.
			editOrSend(m, chat, statusRef, "🛑 <b>Ответ остановлен.</b>")
			return
		}
		if ctx.Err() == context.DeadlineExceeded {
			message = fmt.Sprintf("⌛️ <b>Агент не ответил за %s.</b>\nПопробуйте переспросить или начать новый разговор: <code>/chat new</code>",
				domain.FormatDurationHuman(chatTimeout()))
		} else if res.Err != nil {
			message = fmt.Sprintf("❌ <b>Не удалось получить ответ:</b> %s", html.EscapeString(res.Err.Error()))
		}
		editOrSend(m, chat, statusRef, message)
		return
	}

	answer := strings.TrimSpace(res.Answer)
	if answer == "" {
		editOrSend(m, chat, statusRef, "🤔 Агент не прислал ответ. Попробуйте переформулировать вопрос.")
		return
	}

	session.AppendTurn(domain.ChatRoleUser, userText)
	session.AppendTurn(domain.ChatRoleAssistant, answer)

	duration := res.Duration
	if duration <= 0 {
		duration = time.Since(startedAt).Seconds()
	}
	usage := domain.UsageStats{}
	if res.Usage != nil {
		usage = *res.Usage
	}
	domain.GlobalTokenTracker.RecordChatUsage(model, usage, duration)

	sendChatAnswer(m, chat, statusRef, project, answer, note)
}

// streamChatAnswer запускает агента и разбирает поток событий stream-json.
func streamChatAnswer(ctx context.Context, m ports.Messenger, chat ports.ChatID, statusRef ports.MessageRef,
	framework ports.AgentFramework, args ports.ExecuteArgs, setup chatTurnSetup) chatResult {
	var res chatResult

	agentProcess, err := framework.ExecuteTask(ctx, args)
	if err != nil {
		res.Err = err
		return res
	}
	defer func() { _ = agentProcess.Close() }()

	var mu sync.Mutex
	lastAction := "Инициализация сессии агента..."
	startedAt := time.Now()

	stopTicker := make(chan struct{})
	tickerDone := make(chan struct{})
	go func() {
		defer close(tickerDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-stopTicker:
				return
			case <-ticker.C:
				mu.Lock()
				action := lastAction
				mu.Unlock()

				text := fmt.Sprintf("💭 <b>Думаю…</b> <code>%s</code> [<code>%s</code>] · %s/%s\n⏱ %s\n<i>%s</i>\n\n<i>Остановить: /chat stop</i>",
					html.EscapeString(setup.Project), html.EscapeString(setup.Model),
					html.EscapeString(setup.Agent), html.EscapeString(setup.Mode),
					domain.FormatDurationHuman(time.Since(startedAt)),
					html.EscapeString(utils.TruncateString(action, 80)))
				if statusRef.ID != "" {
					_ = m.Edit(context.Background(), statusRef, text, ports.Rich())
				}
			}
		}
	}()

	var answer strings.Builder
	scanner := bufio.NewScanner(agentProcess.Stdout())
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(utils.AnsiRegex.ReplaceAllString(scanner.Text(), ""))
		if line == "" {
			continue
		}

		evt, parseErr := domain.ParseStreamEvent(line)
		if parseErr != nil || evt == nil {
			// Не-JSON строки терминала показываем как прогресс, но в ответ не включаем.
			mu.Lock()
			lastAction = line
			mu.Unlock()
			continue
		}

		if convID := streamEventConversationID(evt); convID != "" {
			res.ConversationID = convID
		}

		if u := evt.StepUpdate; u != nil {
			switch {
			case u.StepType == "tool" && u.State == "ACTIVE":
				desc := domain.FormatToolAction(u.ToolName, u.ToolInfo)
				mu.Lock()
				lastAction = desc
				mu.Unlock()
			case u.StepType == "agent_response" && u.TextDelta != "":
				answer.WriteString(u.TextDelta)
			}
		}

		if r := evt.Result; r != nil {
			res.Status = r.Status
			res.Duration = r.DurationSeconds
			if r.Usage != nil {
				res.Usage = r.Usage
			}
			if r.Error != "" {
				res.Err = fmt.Errorf("%s", r.Error)
			}
			if r.Response != "" && answer.Len() == 0 {
				answer.WriteString(r.Response)
			}
		}
	}

	close(stopTicker)
	<-tickerDone

	if waitErr := agentProcess.Wait(); waitErr != nil && res.Err == nil {
		res.Err = waitErr
	}
	if scanErr := scanner.Err(); scanErr != nil && res.Err == nil {
		res.Err = scanErr
	}
	if ctx.Err() != nil && res.Err == nil && answer.Len() == 0 {
		res.Err = ctx.Err()
	}

	res.Answer = answer.String()
	return res
}

// streamEventConversationID достаёт идентификатор сессии агента из любого типа события.
func streamEventConversationID(evt *domain.StreamEvent) string {
	if evt.ConversationID != "" {
		return evt.ConversationID
	}
	if evt.StepUpdate != nil && evt.StepUpdate.ConversationID != "" {
		return evt.StepUpdate.ConversationID
	}
	if evt.Result != nil {
		return evt.Result.ConversationID
	}
	return ""
}

// sendChatAnswer отправляет ответ агента: коротким сообщением, несколькими частями или файлом.
func sendChatAnswer(m ports.Messenger, chat ports.ChatID, statusRef ports.MessageRef, project, answer, note string) {
	if note != "" {
		answer = answer + "\n\n" + note
	}

	runes := []rune(answer)
	switch {
	case len(runes) <= chatAnswerInlineLimit:
		editOrSend(m, chat, statusRef, utils.MarkdownToTelegramHTML(answer))

	case len(runes) <= chatAnswerDocumentLimit:
		editOrSend(m, chat, statusRef, "💬 <b>Ответ агента:</b>")
		sendLongMarkdown(m, chat, answer)

	default:
		summary := utils.MarkdownToTelegramHTML(utils.ExtractPlanSummary(answer, 1200))
		editOrSend(m, chat, statusRef, fmt.Sprintf("💬 <b>Ответ агента:</b>\n\n%s\n\n📄 <i>Полный ответ (%d знаков) прикреплён файлом.</i>", summary, len(runes)))

		doc := ports.Document{
			FileName: fmt.Sprintf("chat_%s_%d.md", project, time.Now().Unix()),
			MIME:     "text/markdown",
			Caption:  fmt.Sprintf("💬 Полный ответ агента (%s)", project),
			Content:  []byte(answer),
		}
		if _, err := m.SendDocument(context.Background(), chat, doc); err != nil {
			sendLongMarkdown(m, chat, answer)
		}
	}
}

// editOrSend правит статусное сообщение, а если это не удалось — отправляет новое.
func editOrSend(m ports.Messenger, chat ports.ChatID, ref ports.MessageRef, text string) {
	if ref.ID != "" {
		if err := m.Edit(context.Background(), ref, text, ports.Rich()); err == nil {
			return
		}
	}
	_, _ = m.Send(context.Background(), chat, text, ports.Rich())
}

func sendPlain(m ports.Messenger, chat ports.ChatID, text string) error {
	_, err := m.Send(context.Background(), chat, text, ports.Rich())
	return err
}

// chatTimeout возвращает таймаут одного хода диалога.
func chatTimeout() time.Duration {
	if config.ChatTimeout > 0 {
		return config.ChatTimeout
	}
	return 5 * time.Minute
}

// workSuggestion — запомненный запрос на изменение кода, ожидающий выбора пользователя.
type workSuggestion struct {
	Text      string
	Project   string
	Agent     string
	CreatedAt time.Time
}

var (
	suggestionsMu sync.Mutex
	suggestions   = make(map[ports.MessageID]workSuggestion)
)

// sendWorkSuggestion предлагает выбор: ответить в чате, составить план или создать задачу.
func sendWorkSuggestion(s ports.Session, project, agent, text string) error {
	msg := fmt.Sprintf(
		"🤔 <b>Похоже на запрос на изменение кода:</b>\n<i>«%s»</i>\n\n"+
			"Проект: <code>%s</code>\nЧто сделать?",
		html.EscapeString(utils.TruncateString(text, 250)), html.EscapeString(project))

	m := s.Messenger()
	ref, err := m.Send(context.Background(), s.Chat(), msg, ports.RichWith(buildWorkSuggestionMarkup()))
	if err != nil {
		return err
	}
	rememberSuggestion(ref, workSuggestion{Text: text, Project: project, Agent: agent, CreatedAt: time.Now()})
	return nil
}

// buildWorkSuggestionMarkup собирает кнопки карточки выбора.
func buildWorkSuggestionMarkup() *ports.Keyboard {
	return &ports.Keyboard{
		Rows: [][]ports.Button{
			{
				{Text: "💬 Ответить в чате", Action: "chat_answer"},
				{Text: "📝 Составить план", Action: "chat_plan"},
			},
			{
				{Text: "🚀 Создать задачу", Action: "chat_task"},
			},
		},
	}
}

// rememberSuggestion запоминает текст запроса, попутно вычищая устаревшие карточки.
func rememberSuggestion(ref ports.MessageRef, sug workSuggestion) {
	if ref.ID == "" {
		return
	}
	suggestionsMu.Lock()
	defer suggestionsMu.Unlock()

	for id, item := range suggestions {
		if time.Since(item.CreatedAt) > chatSuggestionTTL {
			delete(suggestions, id)
		}
	}
	suggestions[ref.ID] = sug
}

// takeSuggestion забирает запомненный запрос карточки.
func takeSuggestion(id ports.MessageID) (workSuggestion, bool) {
	suggestionsMu.Lock()
	defer suggestionsMu.Unlock()

	sug, ok := suggestions[id]
	if !ok {
		return workSuggestion{}, false
	}
	delete(suggestions, id)
	if time.Since(sug.CreatedAt) > chatSuggestionTTL {
		return workSuggestion{}, false
	}
	return sug, true
}

// takeChatSuggestion достаёт запомненный запрос по сообщению, к которому прикреплена кнопка.
func takeChatSuggestion(s ports.Session) (workSuggestion, bool) {
	cb := s.Callback()
	if cb == nil || cb.Message == nil {
		return workSuggestion{}, false
	}
	return takeSuggestion(cb.Message.ID)
}

// buildChatModeMarkup — кнопка переключения диалогового режима.
func buildChatModeMarkup() *ports.Keyboard {
	return &ports.Keyboard{
		Rows: [][]ports.Button{
			{{Text: "🔄 Переключить режим сообщений", Action: "chat_mode_toggle"}},
		},
	}
}

// applyInteractionMode переключает режим обработки обычных сообщений и сохраняет выбор.
func applyInteractionMode(s ports.Session, target string, fromButton bool) error {
	config.ProjectState.SetInteractionMode(target)
	actual := config.ProjectState.GetInteractionMode()

	if st := domain.GlobalTaskManager.Storage(); st != nil {
		_ = st.SetSetting(context.Background(), "interaction_mode", actual)
	}

	if actual == domain.InteractionModeChat {
		if fromButton {
			_ = s.Respond("Диалоговый режим включен")
		}
		return s.Send("💬 <b>Диалоговый режим ВКЛЮЧЕН.</b>\n"+
			"Обычные сообщения — это разговор с агентом по текущему проекту (контекст сохраняется, /resume не нужен).\n"+
			"Запрос на изменение кода бот предложит оформить планом или задачей.", ports.Rich())
	}

	if fromButton {
		_ = s.Respond("Диалоговый режим выключен")
	}
	return s.Send("🚀 <b>Диалоговый режим ВЫКЛЮЧЕН.</b>\n"+
		"Каждое обычное сообщение снова создаёт задачу. Разовый вопрос в чат: <code>/chat &lt;вопрос&gt;</code>.", ports.Rich())
}

// formatChatStatus описывает текущий разговор: проект, агент, режим и вид памяти.
func formatChatStatus(project string) string {
	mode := config.ProjectState.GetExecutionMode()
	agent := ActiveAgentName()
	if agent == "" {
		agent = "agy"
	}
	interaction := config.ProjectState.GetInteractionMode()

	var bldr strings.Builder
	bldr.WriteString("💬 <b>Диалоговый режим</b>\n\n")
	if interaction == domain.InteractionModeChat {
		bldr.WriteString("Обычные сообщения: <b>отвечаю в чате</b>\n")
	} else {
		bldr.WriteString("Обычные сообщения: <b>создают задачу</b> (<code>/chatmode on</code> — вернуть диалог)\n")
	}
	bldr.WriteString(fmt.Sprintf("Проект: <code>%s</code>\n", html.EscapeString(project)))
	bldr.WriteString(fmt.Sprintf("Агент: <code>%s</code>, режим: <code>%s</code>\n", html.EscapeString(agent), html.EscapeString(mode)))

	session := domain.GlobalChatManager.Get(project)
	if session == nil {
		bldr.WriteString("\nРазговор ещё не начат — просто напишите вопрос.")
		return bldr.String()
	}

	bldr.WriteString(fmt.Sprintf("Реплик в истории: <code>%d</code>\n", session.TurnsCount()))
	if convID := session.ConversationIDFor(agent, mode); convID != "" {
		bldr.WriteString(fmt.Sprintf("Сессия агента: <code>%s</code>\n", html.EscapeString(utils.TruncateString(convID, 40))))
	}
	if strings.EqualFold(mode, "api") {
		bldr.WriteString("Память: история диалога из базы бота (агент помнит только текст реплик).\n")
	} else {
		bldr.WriteString("Память: собственная сессия агента.\n")
	}
	if session.IsRunning() {
		bldr.WriteString("\n⏳ Сейчас идёт ответ. Остановить: <code>/chat stop</code>")
	} else {
		bldr.WriteString("\n🔄 Начать разговор заново: <code>/chat new</code>")
	}
	return bldr.String()
}

// handleChat — обработчик команды /chat.
func handleChat(s ports.Session) error {
	args := s.Args()
	config.ProjectState.RLock()
	curProj := config.ProjectState.CurrentProject
	curModel := config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()

	if len(args) == 0 {
		if curProj == "" {
			return s.Send("❌ Сначала выберите проект: /projects", nil)
		}
		return s.Send(formatChatStatus(curProj), ports.RichWith(buildChatModeMarkup()))
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "new", "reset", "новый", "сброс":
		if curProj == "" {
			return s.Send("❌ Сначала выберите проект: /projects", nil)
		}
		if session := domain.GlobalChatManager.Get(curProj); session != nil {
			session.Cancel()
		}
		domain.GlobalChatManager.Reset(curProj, curModel)
		return s.Send(fmt.Sprintf("🔄 <b>Начат новый разговор</b> в проекте <code>%s</code>. Прошлый контекст больше не используется.", html.EscapeString(curProj)), ports.Rich())

	case "stop", "стоп", "отмена":
		if curProj == "" {
			return s.Send("❌ Сначала выберите проект: /projects", nil)
		}
		session := domain.GlobalChatManager.Get(curProj)
		if session == nil || !session.Cancel() {
			return s.Send("ℹ️ Сейчас нет активного ответа в диалоге.", ports.Rich())
		}
		return s.Send("🛑 <b>Ответ агента остановлен.</b>", ports.Rich())
	}

	return handleChatMessage(s, strings.TrimSpace(strings.Join(args, " ")))
}

// handleChatMode — обработчик команды /chatmode.
func handleChatMode(s ports.Session) error {
	args := s.Args()
	current := config.ProjectState.GetInteractionMode()
	target := domain.InteractionModeChat

	if len(args) == 0 {
		if current == domain.InteractionModeChat {
			target = domain.InteractionModeTask
		}
	} else {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "on", "enable", "true", "1", "вкл", "да", "chat":
			target = domain.InteractionModeChat
		case "off", "disable", "false", "0", "выкл", "нет", "task":
			target = domain.InteractionModeTask
		case "toggle":
			if current == domain.InteractionModeChat {
				target = domain.InteractionModeTask
			}
		default:
			return s.Send("Использование: <code>/chatmode [on|off|toggle]</code>", ports.Rich())
		}
	}

	return applyInteractionMode(s, target, false)
}

// onChatModeToggle — обработчик кнопки chat_mode_toggle.
func onChatModeToggle(s ports.Session) error {
	target := domain.InteractionModeTask
	if config.ProjectState.GetInteractionMode() == domain.InteractionModeTask {
		target = domain.InteractionModeChat
	}
	return applyInteractionMode(s, target, true)
}

// onChatAnswer — обработчик кнопки chat_answer.
func onChatAnswer(s ports.Session) error {
	sug, ok := takeChatSuggestion(s)
	if !ok {
		_ = s.Respond("Карточка устарела")
		return s.Send("ℹ️ Карточка устарела — отправьте сообщение ещё раз.", ports.Rich())
	}
	_ = s.Respond("Отвечаю в чате")
	return startChatTurn(s.Messenger(), s.Chat(), sug.Project, sug.Agent, sug.Text, "")
}

// onChatPlan — обработчик кнопки chat_plan.
func onChatPlan(s ports.Session) error {
	sug, ok := takeChatSuggestion(s)
	if !ok {
		_ = s.Respond("Карточка устарела")
		return s.Send("ℹ️ Карточка устарела — отправьте сообщение ещё раз.", ports.Rich())
	}
	_ = s.Respond("Составляю план")
	return handleCreateNewTaskWithOptions(s, sug.Text, true)
}

// onChatTask — обработчик кнопки chat_task.
func onChatTask(s ports.Session) error {
	sug, ok := takeChatSuggestion(s)
	if !ok {
		_ = s.Respond("Карточка устарела")
		return s.Send("ℹ️ Карточка устарела — отправьте сообщение ещё раз.", ports.Rich())
	}
	_ = s.Respond("Создаю задачу")
	return handleCreateNewTaskWithOptions(s, sug.Text, false)
}
