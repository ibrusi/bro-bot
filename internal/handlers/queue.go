package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
	"html"
	"strconv"
	"strings"
)

// Очередь сообщений, пока агент отвечает, и список сессий, которые бот сам сохранил.
// Команды общие для agy, claude и grok: они не меняют текст промпта и не прерывают
// текущий ход. Чат забирает очередь в runChatLoop, задача — между шагами пайплайна.

const (
	queuePreviewItems  = 15
	queuePreviewRunes  = 80
	sessionTaskLimit   = 10
	sessionPromptRunes = 48
	sessionIDRunes     = 24
	sessionButtonRunes = 24
)

// handleQueue показывает очередь чата и дополнения задачи текущего проекта.
func handleQueue(s ports.Session) error {
	lang := uiLang()
	project, _ := currentProjectPair()
	if project == "" {
		return s.Send(i18n.T(lang, "projects.select_first"), nil)
	}
	return s.Send(formatQueue(project, lang), ports.Rich())
}

// handleClearQueue очищает обе очереди и не останавливает текущий ответ.
func handleClearQueue(s ports.Session) error {
	lang := uiLang()
	project, _ := currentProjectPair()
	if project == "" {
		return s.Send(i18n.T(lang, "projects.select_first"), nil)
	}

	chatN := 0
	if session := domain.GlobalChatManager.Get(project); session != nil {
		chatN = len(session.ClearPending())
	}

	taskN := 0
	if task := taskOfProject(project); task != nil {
		view := task.Snapshot()
		if !taskFinished(view.Status) {
			taskN = len(view.PendingFollowups)
			if taskN > 0 {
				task.ClearPendingFollowups()
			}
		}
	}

	return s.Send(i18n.Tf(lang, "queue.cleared", chatN, taskN), ports.Rich())
}

// handleFlush отдаёт очередь на ближайшей границе хода и не обрывает текущий ответ.
// Простаивающий чат с остатком очереди запускается сразу. Задача на паузе не возобновляется.
func handleFlush(s ports.Session) error {
	lang := uiLang()
	project, _ := currentProjectPair()
	if project == "" {
		return s.Send(i18n.T(lang, "projects.select_first"), nil)
	}

	var notes []string
	var drained []string
	chatText := ""

	if session := domain.GlobalChatManager.Get(project); session != nil {
		pending := session.PendingTexts()
		switch {
		case session.IsRunning() && len(pending) > 0:
			notes = append(notes, i18n.Tf(lang, "queue.flush_chat", len(pending)))
		case !session.IsRunning() && len(pending) > 0:
			drained = session.DrainPending()
			chatText = strings.TrimSpace(strings.Join(drained, "\n"))
			if chatText == "" {
				drained = nil
			} else {
				notes = append(notes, i18n.Tf(lang, "queue.flush_now", len(drained)))
			}
		}
	}

	if task := taskOfProject(project); task != nil {
		view := task.Snapshot()
		n := len(view.PendingFollowups)
		if n > 0 && !taskFinished(view.Status) {
			notes = append(notes, flushTaskNote(lang, view, n))
		}
	}

	if len(notes) == 0 {
		return s.Send(i18n.T(lang, "queue.flush_idle"), ports.Rich())
	}
	if err := s.Send(strings.Join(notes, "\n"), ports.Rich()); err != nil {
		restoreDrainedChat(project, drained, chatText)
		return err
	}
	if chatText == "" {
		return nil
	}
	if err := startChatTurn(s.Messenger(), s.Chat(), project, ActiveAgentName(), chatText, ""); err != nil {
		restoreDrainedChat(project, drained, chatText)
		return err
	}
	return nil
}

// handleBtw ставит реплику в начало очереди того, кто сейчас работает.
// Если никто не работает, сообщение идёт обычным маршрутом.
func handleBtw(s ports.Session) error {
	lang := uiLang()
	text := strings.TrimSpace(strings.Join(s.Args(), " "))
	if text == "" {
		return s.Send(i18n.T(lang, "queue.btw_usage"), ports.Rich())
	}
	project, _ := currentProjectPair()
	if project == "" {
		return s.Send(i18n.T(lang, "projects.select_first"), nil)
	}

	if session := domain.GlobalChatManager.Get(project); session != nil && session.IsRunning() {
		n := session.PrependPending(text)
		return s.Send(i18n.Tf(lang, "queue.btw_chat", n), ports.Rich())
	}

	if task, kind := workingTask(project); task != nil {
		id := task.Snapshot().ID
		if kind == "answer" {
			return handleAddFollowupToTask(s, id, text)
		}
		n, err := domain.GlobalTaskManager.PrependFollowup(id, text)
		if err != nil {
			return s.Send(i18n.Tf(lang, "task.followup_failed", id, ErrorText(err, lang)), ports.Rich())
		}
		return s.Send(i18n.Tf(lang, "queue.btw_task", id, n), ports.Rich())
	}

	return handleTextWithContent(s, text)
}

// handleSessions показывает сессии чата и задачи, которые бот сохранил для проекта.
func handleSessions(s ports.Session) error {
	lang := uiLang()
	project, _ := currentProjectPair()
	if project == "" {
		return s.Send(i18n.T(lang, "projects.select_first"), nil)
	}
	text, kb := formatSessions(project, lang)
	return s.Send(text, ports.RichWith(kb))
}

// onChatNew повторяет /chat new: обрывает ответ и начинает разговор заново.
func onChatNew(s ports.Session) error {
	lang := uiLang()
	project, model := currentProjectPair()
	if project == "" {
		_ = s.Respond(i18n.T(lang, "projects.select_first"))
		return s.Send(i18n.T(lang, "projects.select_first"), nil)
	}
	resetProjectChat(project, model)
	_ = s.Respond(i18n.T(lang, "sessions.toast_new"))
	return s.Send(i18n.Tf(lang, "chat.reset", html.EscapeString(project)), ports.Rich())
}

func currentProjectPair() (string, string) {
	config.ProjectState.RLock()
	defer config.ProjectState.RUnlock()
	return config.ProjectState.CurrentProject, config.ProjectState.CurrentModel
}

func formatQueue(project, lang string) string {
	var chatItems []string
	if session := domain.GlobalChatManager.Get(project); session != nil {
		chatItems = session.PendingTexts()
	}

	var taskView *domain.TaskView
	if task := taskOfProject(project); task != nil {
		view := task.Snapshot()
		if len(view.PendingFollowups) > 0 {
			taskView = &view
		}
	}

	if len(chatItems) == 0 && taskView == nil {
		return i18n.T(lang, "queue.header") + i18n.T(lang, "queue.empty")
	}

	var b strings.Builder
	b.WriteString(i18n.T(lang, "queue.header"))
	if len(chatItems) > 0 {
		b.WriteString(i18n.T(lang, "queue.chat_header"))
		writeQueueItems(&b, lang, "queue.chat_item", chatItems)
	}
	if taskView != nil {
		b.WriteString(i18n.Tf(lang, "queue.task_header", taskView.ID))
		writeQueueItems(&b, lang, "queue.task_item", taskView.PendingFollowups)
	}
	return b.String()
}

func writeQueueItems(b *strings.Builder, lang, itemKey string, items []string) {
	shown := items
	extra := 0
	if len(shown) > queuePreviewItems {
		extra = len(shown) - queuePreviewItems
		shown = shown[:queuePreviewItems]
	}
	for i, item := range shown {
		text := html.EscapeString(utils.TruncateString(strings.TrimSpace(item), queuePreviewRunes))
		b.WriteString(i18n.Tf(lang, itemKey, i+1, text))
	}
	if extra > 0 {
		b.WriteString(i18n.Tf(lang, "queue.more", extra))
	}
}

func formatSessions(project, lang string) (string, *ports.Keyboard) {
	var b strings.Builder
	b.WriteString(i18n.Tf(lang, "sessions.header", html.EscapeString(project)))
	b.WriteString(i18n.T(lang, "sessions.chat_header"))

	var sessions []domain.ChatAgentSession
	if session := domain.GlobalChatManager.Get(project); session != nil {
		sessions = session.AgentSessions()
	}
	if len(sessions) == 0 {
		b.WriteString(i18n.T(lang, "sessions.chat_empty"))
	}
	for _, item := range sessions {
		b.WriteString(i18n.Tf(lang, "sessions.chat_item",
			html.EscapeString(item.Agent),
			html.EscapeString(item.Mode),
			html.EscapeString(utils.TruncateString(item.ConversationID, sessionIDRunes)),
		))
	}

	tasks := projectTasks(project, sessionTaskLimit)
	b.WriteString(i18n.T(lang, "sessions.task_header"))
	if len(tasks) == 0 {
		b.WriteString(i18n.T(lang, "sessions.task_empty"))
	}

	kb := &ports.Keyboard{
		Rows: [][]ports.Button{{
			{Text: i18n.T(lang, "btn.session_new_chat"), Action: "chat_new"},
		}},
	}
	for _, task := range tasks {
		view := task.Snapshot()
		prompt := strings.Join(strings.Fields(view.InitialPrompt), " ")
		agent := view.Agent
		if agent == "" {
			agent = "agy"
		}
		b.WriteString(i18n.Tf(lang, "sessions.task_item",
			view.ID,
			html.EscapeString(i18n.TaskStatusTitle(string(view.Status), lang)),
			html.EscapeString(agent),
			html.EscapeString(utils.TruncateString(prompt, sessionPromptRunes)),
		))
		if view.ConversationID != "" {
			b.WriteString(i18n.Tf(lang, "sessions.task_session",
				html.EscapeString(utils.TruncateString(view.ConversationID, sessionIDRunes)),
			))
		}
		kb.Rows = append(kb.Rows, []ports.Button{{
			Text:    i18n.Tf(lang, "btn.session_task", view.ID, utils.TruncateString(prompt, sessionButtonRunes)),
			Action:  "task_sel",
			Payload: strconv.Itoa(view.ID),
		}})
	}
	return b.String(), kb
}

func flushTaskNote(lang string, view domain.TaskView, n int) string {
	switch view.Status {
	case domain.TaskStatusRunning, domain.TaskStatusPlanning:
		return i18n.Tf(lang, "queue.flush_task", view.ID, n)
	case domain.TaskStatusWaitingInput:
		if view.HasActivePipeline {
			return i18n.Tf(lang, "queue.flush_task", view.ID, n)
		}
		return i18n.Tf(lang, "queue.flush_paused", view.ID, n, view.ID)
	case domain.TaskStatusPaused:
		return i18n.Tf(lang, "queue.flush_paused", view.ID, n, view.ID)
	default:
		return i18n.Tf(lang, "queue.flush_saved", view.ID, n)
	}
}

func restoreDrainedChat(project string, drained []string, chatText string) {
	if chatText == "" || len(drained) == 0 {
		return
	}
	session := domain.GlobalChatManager.Get(project)
	if session == nil {
		return
	}
	for _, item := range session.PendingTexts() {
		if item == chatText {
			return
		}
	}
	for i := len(drained) - 1; i >= 0; i-- {
		session.PrependPending(drained[i])
	}
}

// taskOfProject возвращает задачу текущего проекта: сфокусированную, иначе активную,
// иначе последнюю. Задачи других проектов не попадают в очередь.
func taskOfProject(project string) *domain.TaskSession {
	if project == "" || domain.GlobalTaskManager == nil {
		return nil
	}
	if active := domain.GlobalTaskManager.GetActiveTask(); active != nil && active.Snapshot().Project == project {
		return active
	}
	var latest *domain.TaskSession
	for _, task := range domain.GlobalTaskManager.ListTasks() {
		view := task.Snapshot()
		if view.Project != project {
			continue
		}
		latest = task
		if view.IsActive {
			return task
		}
	}
	return latest
}

// workingTask ищет в текущем проекте задачу, которая прямо сейчас работает.
// "prepend" — ход идёт, дополнение встаёт в очередь. "answer" — задача ждёт ответ.
func workingTask(project string) (*domain.TaskSession, string) {
	if project == "" || domain.GlobalTaskManager == nil {
		return nil, ""
	}
	var working, waiting *domain.TaskSession
	focused := domain.GlobalTaskManager.GetActiveTask()
	for _, task := range domain.GlobalTaskManager.ListTasks() {
		view := task.Snapshot()
		if view.Project != project {
			continue
		}
		switch view.Status {
		case domain.TaskStatusRunning, domain.TaskStatusPlanning:
			if working == nil || task == focused {
				working = task
			}
		case domain.TaskStatusWaitingInput:
			if waiting == nil || task == focused {
				waiting = task
			}
		}
	}
	if working != nil {
		return working, "prepend"
	}
	if waiting != nil {
		return waiting, "answer"
	}
	return nil, ""
}

func projectTasks(project string, limit int) []*domain.TaskSession {
	var tasks []*domain.TaskSession
	for _, task := range domain.GlobalTaskManager.ListTasks() {
		if task.Snapshot().Project == project {
			tasks = append(tasks, task)
		}
	}
	if limit > 0 && len(tasks) > limit {
		tasks = tasks[len(tasks)-limit:]
	}
	return tasks
}

func taskFinished(status domain.TaskStatus) bool {
	switch status {
	case domain.TaskStatusCompleted, domain.TaskStatusCancelled, domain.TaskStatusFailed:
		return true
	default:
		return false
	}
}
