package handlers

import (
	"bro-bot/internal/adapters/cliproc"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
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
func chatSystemPreamble(lang string) string {
	return i18n.T(lang, "chat.system_preamble")
}

// chatShortReminder — короткое напоминание о режиме для последующих ходов CLI-агента.
func chatShortReminder(lang string) string {
	return i18n.T(lang, "chat.short_reminder")
}

// buildChatPrompt собирает промпт хода диалога.
//
// В api-режиме преамбула уходит отдельным системным полем, а история — отдельным списком реплик,
// поэтому в промпте остаётся только текст пользователя. В cli-режиме у агента своя память сессии:
// при первом ходе новой пары агент/режим подмешиваем преамбулу и краткий контекст прошлых реплик,
// дальше достаточно короткого напоминания.
func buildChatPrompt(session *domain.ChatSession, agent, mode, userText, lang, rules string) string {
	if strings.EqualFold(mode, "api") {
		return userText
	}

	firstTurn := session.ConversationIDFor(agent, mode) == ""
	if !firstTurn {
		return fmt.Sprintf("%s\n\n%s", chatShortReminder(lang), userText)
	}

	var bldr strings.Builder
	bldr.WriteString(chatSystemPreamble(lang))
	if rules != "" {
		bldr.WriteString("\n\n")
		bldr.WriteString(rules)
	}

	if session.NeedsContextBootstrap(agent, mode) {
		if prev := formatChatContext(session.HistoryForPrompt(chatBootstrapTurns, chatBootstrapChars), lang); prev != "" {
			bldr.WriteString(i18n.T(lang, "chat.prompt_previous_context"))
			bldr.WriteString(prev)
		}
	}

	bldr.WriteString(i18n.T(lang, "chat.prompt_user_question"))
	bldr.WriteString(userText)
	return bldr.String()
}

// formatChatContext оформляет реплики разговора в текстовый блок для промпта.
func formatChatContext(turns []domain.ChatTurn, lang string) string {
	var bldr strings.Builder
	for _, turn := range turns {
		speaker := i18n.T(lang, "chat.prompt_speaker_user")
		if turn.Role == domain.ChatRoleAssistant {
			speaker = i18n.T(lang, "chat.prompt_speaker_assistant")
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
	view := task.Snapshot()
	return view.Status == domain.TaskStatusPaused && strings.TrimSpace(view.LastQuestion) != ""
}

// hasFreshPendingQuestion сообщает, что задача ждёт ответа на недавно заданный вопрос:
// только в этом случае обычное сообщение уходит в задачу, а не в диалог.
func hasFreshPendingQuestion(task *domain.TaskSession) bool {
	if !isAwaitingAnswer(task) {
		return false
	}
	view := task.Snapshot()
	askedAt := view.QuestionAskedAt

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
		return s.Send(i18n.T(uiLang(), "projects.select_first"), nil)
	}

	intent, reason := domain.ClassifyMessageReason(prompt)
	if intent == domain.IntentWork {
		log.Printf("chat: the message was classified as a code-change request (rule %s)", reason)
		return sendWorkSuggestion(s, targetProj, targetAgent, prompt)
	}

	return startChatTurn(s.Messenger(), s.Chat(), targetProj, targetAgent, prompt, note)
}

// handleChatMessage запускает ход диалога, минуя классификатор (команда /chat и кнопка «Ответить в чате»).
func handleChatMessage(s ports.Session, text string) error {
	config.ProjectState.RLock()
	curProj := config.ProjectState.CurrentProject
	config.ProjectState.RUnlock()

	lang := uiLang()

	targetProj, targetAgent, prompt := parseNewTaskInput(text, curProj, ActiveAgentName())
	if strings.TrimSpace(prompt) == "" {
		return s.Send(i18n.T(lang, "chat.usage"), ports.Rich())
	}
	if targetProj == "" {
		return s.Send(i18n.T(lang, "projects.select_first"), nil)
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

	lang := i18n.Active()

	framework, err := agentFrameworkFor(setup.Agent)
	if err != nil {
		return sendPlain(m, chat, "❌ "+ErrorText(err, lang))
	}
	setup.Framework = framework

	session := domain.GlobalChatManager.GetOrCreate(project, model)

	ctx, cancel := context.WithTimeout(context.Background(), chatTimeout())
	if !session.BeginTurn(cancel) {
		cancel()
		queued := session.EnqueuePending(text)
		return sendPlain(m, chat, i18n.Tf(lang, "chat.queued", queued))
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

	lang := i18n.Active()

	statusText := i18n.Tf(lang, "chat.thinking",
		html.EscapeString(project), html.EscapeString(model), html.EscapeString(agent), html.EscapeString(mode))
	if domain.GlobalTaskManager.HasRunningTaskInProject(project) {
		statusText += i18n.T(lang, "chat.thinking_task_note")
	}
	// Статусное сообщение диалога намеренно не регистрируется как сообщение задачи:
	// таблица сообщений ссылается на tasks(id), а у разговора строки задачи нет.
	statusRef, _ := m.Send(context.Background(), chat, statusText, ports.Rich())

	workDir := filepath.Join(config.ProjectsRoot, project)
	isNewTurn := session.ConversationIDFor(agent, mode) == ""

	var rules string
	if isNewTurn {
		rules = utils.LoadProjectAgentsRules(workDir, config.ProjectsRoot)
	}

	args := ports.ExecuteArgs{
		ConversationID: session.ConversationIDFor(agent, mode),
		ModelName:      model,
		Prompt:         buildChatPrompt(session, agent, mode, userText, lang, rules),
		WorkDir:        workDir,
		History:        chatHistoryForAgent(session, mode),
		ReadOnly:       true,
	}
	if strings.EqualFold(mode, "api") {
		args.SystemPrompt = chatSystemPreamble(lang)
		if rules != "" {
			args.SystemPrompt = fmt.Sprintf("%s\n\n%s", args.SystemPrompt, rules)
		}
	} else if isNewTurn && rules != "" {
		args.SystemPrompt = rules
	}

	startedAt := time.Now()
	res := streamChatAnswer(ctx, m, chat, statusRef, setup.Framework, args, setup, lang)

	if res.ConversationID != "" {
		session.SetConversationID(agent, mode, res.ConversationID)
	}

	if res.Err != nil || res.Status == "ERROR" {
		message := i18n.T(lang, "chat.answer_failed")
		if ctx.Err() == context.Canceled {
			// Пользователь сам остановил ответ командой /chat stop.
			editOrSend(m, chat, statusRef, i18n.T(lang, "chat.answer_stopped"))
			return
		}
		if ctx.Err() == context.DeadlineExceeded {
			message = i18n.Tf(lang, "chat.answer_timeout", domain.FormatDuration(chatTimeout(), lang))
		} else if res.Err != nil {
			message = i18n.Tf(lang, "chat.answer_failed_reason", html.EscapeString(ErrorText(res.Err, lang)))
		}
		editOrSend(m, chat, statusRef, message)
		return
	}

	answer := strings.TrimSpace(res.Answer)
	if answer == "" {
		editOrSend(m, chat, statusRef, i18n.T(lang, "chat.answer_empty"))
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

	sendChatAnswer(m, chat, statusRef, project, answer, note, lang)
}

// streamChatAnswer запускает агента и разбирает поток событий stream-json.
func streamChatAnswer(ctx context.Context, m ports.Messenger, chat ports.ChatID, statusRef ports.MessageRef,
	framework ports.AgentFramework, args ports.ExecuteArgs, setup chatTurnSetup, lang string) chatResult {
	var res chatResult

	agentProcess, err := framework.ExecuteTask(ctx, args)
	if err != nil {
		res.Err = err
		return res
	}
	defer func() { _ = agentProcess.Close() }()

	var mu sync.Mutex
	lastAction := i18n.T(lang, "chat.init_action")
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

				text := i18n.Tf(lang, "chat.thinking_progress",
					html.EscapeString(setup.Project), html.EscapeString(setup.Model),
					html.EscapeString(setup.Agent), html.EscapeString(setup.Mode),
					domain.FormatDuration(time.Since(startedAt), lang),
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
	if scanErr := scanner.Err(); scanErr != nil && res.Err == nil && !cliproc.IsPTYEOF(scanErr) {
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
func sendChatAnswer(m ports.Messenger, chat ports.ChatID, statusRef ports.MessageRef, project, answer, note, lang string) {
	if note != "" {
		answer = answer + "\n\n" + note
	}

	runes := []rune(answer)
	switch {
	case len(runes) <= chatAnswerInlineLimit:
		editOrSend(m, chat, statusRef, utils.MarkdownToTelegramHTML(answer))

	case len(runes) <= chatAnswerDocumentLimit:
		editOrSend(m, chat, statusRef, i18n.T(lang, "chat.answer_header"))
		sendLongMarkdown(m, chat, answer)

	default:
		summary := utils.MarkdownToTelegramHTML(utils.ExtractPlanSummary(answer, 1200))
		editOrSend(m, chat, statusRef, i18n.Tf(lang, "chat.answer_summary", summary, len(runes)))

		doc := ports.Document{
			FileName: fmt.Sprintf("chat_%s_%d.md", project, time.Now().Unix()),
			MIME:     "text/markdown",
			Caption:  i18n.Tf(lang, "chat.answer_doc_caption", project),
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
	lang := uiLang()
	msg := i18n.Tf(lang, "chat.work_suggestion",
		html.EscapeString(utils.TruncateString(text, 250)), html.EscapeString(project))

	m := s.Messenger()
	ref, err := m.Send(context.Background(), s.Chat(), msg, ports.RichWith(buildWorkSuggestionMarkup(lang)))
	if err != nil {
		return err
	}
	rememberSuggestion(ref, workSuggestion{Text: text, Project: project, Agent: agent, CreatedAt: time.Now()})
	return nil
}

// buildWorkSuggestionMarkup собирает кнопки карточки выбора.
func buildWorkSuggestionMarkup(lang string) *ports.Keyboard {
	return &ports.Keyboard{
		Rows: [][]ports.Button{
			{
				{Text: i18n.T(lang, "btn.chat_answer"), Action: "chat_answer"},
				{Text: i18n.T(lang, "btn.chat_plan"), Action: "chat_plan"},
			},
			{
				{Text: i18n.T(lang, "btn.chat_task"), Action: "chat_task"},
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
func buildChatModeMarkup(lang string) *ports.Keyboard {
	return &ports.Keyboard{
		Rows: [][]ports.Button{
			{{Text: i18n.T(lang, "btn.toggle_message_mode"), Action: "chat_mode_toggle"}},
		},
	}
}

// applyInteractionMode переключает режим обработки обычных сообщений и сохраняет выбор.
func applyInteractionMode(s ports.Session, target string, fromButton bool) error {
	lang := uiLang()

	config.ProjectState.SetInteractionMode(target)
	actual := config.ProjectState.GetInteractionMode()

	if st := domain.GlobalTaskManager.Storage(); st != nil {
		_ = st.SetSetting(context.Background(), "interaction_mode", actual)
	}

	if actual == domain.InteractionModeChat {
		if fromButton {
			_ = s.Respond(i18n.T(lang, "chat.toast_mode_on"))
		}
		return s.Send(i18n.T(lang, "chat.mode_on"), ports.Rich())
	}

	if fromButton {
		_ = s.Respond(i18n.T(lang, "chat.toast_mode_off"))
	}
	return s.Send(i18n.T(lang, "chat.mode_off"), ports.Rich())
}

// formatChatStatus описывает текущий разговор: проект, агент, режим и вид памяти.
func formatChatStatus(project, lang string) string {
	mode := config.ProjectState.GetExecutionMode()
	agent := ActiveAgentName()
	if agent == "" {
		agent = "agy"
	}
	interaction := config.ProjectState.GetInteractionMode()

	var bldr strings.Builder
	bldr.WriteString(i18n.T(lang, "chat.status_header"))
	if interaction == domain.InteractionModeChat {
		bldr.WriteString(i18n.T(lang, "chat.status_messages_chat"))
	} else {
		bldr.WriteString(i18n.T(lang, "chat.status_messages_task"))
	}
	bldr.WriteString(i18n.Tf(lang, "chat.status_project", html.EscapeString(project)))
	bldr.WriteString(i18n.Tf(lang, "chat.status_agent", html.EscapeString(agent), html.EscapeString(mode)))

	session := domain.GlobalChatManager.Get(project)
	if session == nil {
		bldr.WriteString(i18n.T(lang, "chat.status_not_started"))
		return bldr.String()
	}

	bldr.WriteString(i18n.Tf(lang, "chat.status_turns", session.TurnsCount()))
	if convID := session.ConversationIDFor(agent, mode); convID != "" {
		bldr.WriteString(i18n.Tf(lang, "chat.status_session", html.EscapeString(utils.TruncateString(convID, 40))))
	}
	if strings.EqualFold(mode, "api") {
		bldr.WriteString(i18n.T(lang, "chat.status_memory_api"))
	} else {
		bldr.WriteString(i18n.T(lang, "chat.status_memory_cli"))
	}
	if session.IsRunning() {
		bldr.WriteString(i18n.T(lang, "chat.status_running"))
	} else {
		bldr.WriteString(i18n.T(lang, "chat.status_idle"))
	}
	return bldr.String()
}

// handleChat — обработчик команды /chat.
func handleChat(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	config.ProjectState.RLock()
	curProj := config.ProjectState.CurrentProject
	curModel := config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()

	if len(args) == 0 {
		if curProj == "" {
			return s.Send(i18n.T(lang, "projects.select_first"), nil)
		}
		return s.Send(formatChatStatus(curProj, lang), ports.RichWith(buildChatModeMarkup(lang)))
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "new", "reset", "новый", "сброс":
		if curProj == "" {
			return s.Send(i18n.T(lang, "projects.select_first"), nil)
		}
		if session := domain.GlobalChatManager.Get(curProj); session != nil {
			session.Cancel()
		}
		domain.GlobalChatManager.Reset(curProj, curModel)
		return s.Send(i18n.Tf(lang, "chat.reset", html.EscapeString(curProj)), ports.Rich())

	case "stop", "стоп", "отмена":
		if curProj == "" {
			return s.Send(i18n.T(lang, "projects.select_first"), nil)
		}
		session := domain.GlobalChatManager.Get(curProj)
		if session == nil || !session.Cancel() {
			return s.Send(i18n.T(lang, "chat.no_active_answer"), ports.Rich())
		}
		return s.Send(i18n.T(lang, "chat.stopped"), ports.Rich())
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
			return s.Send(i18n.T(uiLang(), "chat.mode_usage"), ports.Rich())
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
	lang := uiLang()

	sug, ok := takeChatSuggestion(s)
	if !ok {
		return respondCardExpired(s, lang)
	}
	_ = s.Respond(i18n.T(lang, "chat.toast_answering"))
	return startChatTurn(s.Messenger(), s.Chat(), sug.Project, sug.Agent, sug.Text, "")
}

// respondCardExpired отвечает на нажатие кнопки карточки, которую бот уже забыл.
func respondCardExpired(s ports.Session, lang string) error {
	_ = s.Respond(i18n.T(lang, "chat.toast_card_expired"))
	return s.Send(i18n.T(lang, "chat.card_expired"), ports.Rich())
}

// onChatPlan — обработчик кнопки chat_plan.
func onChatPlan(s ports.Session) error {
	lang := uiLang()

	sug, ok := takeChatSuggestion(s)
	if !ok {
		return respondCardExpired(s, lang)
	}
	_ = s.Respond(i18n.T(lang, "chat.toast_planning"))
	return handleCreateNewTaskWithOptions(s, sug.Text, true)
}

// onChatTask — обработчик кнопки chat_task.
func onChatTask(s ports.Session) error {
	lang := uiLang()

	sug, ok := takeChatSuggestion(s)
	if !ok {
		return respondCardExpired(s, lang)
	}
	_ = s.Respond(i18n.T(lang, "chat.toast_creating_task"))
	return handleCreateNewTaskWithOptions(s, sug.Text, false)
}
