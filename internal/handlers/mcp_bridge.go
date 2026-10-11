package handlers

import (
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/mcp"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
	"context"
	"fmt"
	"html"
	"strings"
	"sync"
)

// Сторона бота у MCP-моста: вызовы инструментов агента, пришедшие от `bot mcp-serve`,
// доходят сюда и выполняются в контексте задачи или разговора, которые запустили
// этого агента.

const (
	// maxAncestorDepth ограничивает подъём по предкам процесса: MCP-сервер — прямой
	// потомок агента или его обёртки (node, shell), глубокая цепочка не нужна.
	maxAncestorDepth = 16
	// maxQuestionOptions — сколько вариантов ответа превращается в кнопки.
	maxQuestionOptions = 10
	// maxToolTextRunes ограничивает текст, который агент отправляет пользователю.
	maxToolTextRunes = 3500
)

// agentChannel — то, что агент может сделать с пользователем через инструменты бота.
type agentChannel interface {
	SendMessage(text string) error
	ReportProgress(status string)
	AskUser(question string, options []string) error
}

// agentChannels связывает процессы запущенных агентов с их каналами к пользователю.
type agentChannels struct {
	mu        sync.Mutex
	byPID     map[int]agentChannel
	parentPID func(pid int) (int, error)
}

var mcpChannels = newAgentChannels(mcp.ParentPID)

func newAgentChannels(parentPID func(int) (int, error)) *agentChannels {
	return &agentChannels{byPID: make(map[int]agentChannel), parentPID: parentPID}
}

// bind привязывает процесс агента pid к каналу ch и возвращает отвязку. Процессы
// без PID (API-режим, моки) MCP-сервер не запускают — для них отвязка пустая.
func (c *agentChannels) bind(pid int, ch agentChannel) (unbind func()) {
	if pid <= 0 {
		return func() {}
	}
	c.mu.Lock()
	c.byPID[pid] = ch
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		if c.byPID[pid] == ch {
			delete(c.byPID, pid)
		}
		c.mu.Unlock()
	}
}

// lookup находит канал процесса pid или ближайшего его предка-агента.
func (c *agentChannels) lookup(pid int) (agentChannel, bool) {
	for depth := 0; depth < maxAncestorDepth && pid > 1; depth++ {
		c.mu.Lock()
		ch, ok := c.byPID[pid]
		c.mu.Unlock()
		if ok {
			return ch, true
		}
		parent, err := c.parentPID(pid)
		if err != nil || parent == pid {
			return nil, false
		}
		pid = parent
	}
	return nil, false
}

// handle выполняет вызов инструмента от процесса peerPID.
func (c *agentChannels) handle(_ context.Context, peerPID int, call mcp.BridgeCall) mcp.BridgeResult {
	ch, ok := c.lookup(peerPID)
	if !ok {
		return bridgeError("no bro-bot task or chat is attached to this agent process; " +
			"the tool works only for agents started by the bot")
	}

	switch call.Tool {
	case mcp.ToolSendMessage:
		message := stringArg(call.Arguments, "message")
		if message == "" {
			return bridgeError("the message argument is required")
		}
		if err := ch.SendMessage(utils.TruncateString(message, maxToolTextRunes)); err != nil {
			return bridgeError("failed to deliver the message: " + err.Error())
		}
		return mcp.BridgeResult{Text: "The message has been delivered to the user."}

	case mcp.ToolAskUser:
		question := stringArg(call.Arguments, "question")
		if question == "" {
			return bridgeError("the question argument is required")
		}
		options := stringsArg(call.Arguments, "options", maxQuestionOptions)
		if err := ch.AskUser(utils.TruncateString(question, maxToolTextRunes), options); err != nil {
			return bridgeError("failed to ask the user: " + err.Error())
		}
		return mcp.BridgeResult{Text: "The question has been shown to the user. " +
			"End your turn now without further actions: the answer will arrive as the next user message."}

	case mcp.ToolReportProgress:
		status := stringArg(call.Arguments, "status")
		if status == "" {
			return bridgeError("the status argument is required")
		}
		if percent, ok := call.Arguments["percent"].(float64); ok && percent >= 0 && percent <= 100 {
			status = fmt.Sprintf("%s (%d%%)", status, int(percent))
		}
		ch.ReportProgress(utils.TruncateString(status, 200))
		return mcp.BridgeResult{Text: "Progress recorded."}

	default:
		return bridgeError(fmt.Sprintf("unknown tool %q", call.Tool))
	}
}

func bridgeError(text string) mcp.BridgeResult {
	return mcp.BridgeResult{Text: text, IsError: true}
}

func stringArg(args map[string]interface{}, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

// stringsArg читает массив строк, пропуская пустые и нестроковые элементы.
func stringsArg(args map[string]interface{}, key string, limit int) []string {
	raw, _ := args[key].([]interface{})
	var out []string
	for _, item := range raw {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, utils.TruncateString(strings.TrimSpace(s), 64))
			if len(out) == limit {
				break
			}
		}
	}
	return out
}

// taskChannel — канал агента, выполняющего шаг задачи.
//
// Вопрос не показывается сразу: шаг заканчивается, когда агент завершает ход,
// и пайплайн задаёт его тем же путём, что и вопросы ask_question, — с кнопками,
// ожиданием ответа и возобновлением сессии с ответом.
type taskChannel struct {
	m    ports.Messenger
	chat ports.ChatID
	task *domain.TaskSession
	lang string

	mu       sync.Mutex
	question string
	options  []string
}

func newTaskChannel(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession, lang string) *taskChannel {
	return &taskChannel{m: m, chat: chat, task: task, lang: lang}
}

func (c *taskChannel) SendMessage(text string) error {
	view := c.task.Snapshot()
	msg := i18n.Tf(c.lang, "mcp.task_message", view.ID, html.EscapeString(view.Project), utils.MarkdownToTelegramHTML(text))
	ref, err := c.m.Send(context.Background(), c.chat, msg, ports.Rich())
	if err != nil {
		return err
	}
	if ref.ID != "" {
		// Ответ на это сообщение должен попасть в ту же задачу.
		domain.GlobalTaskManager.RegisterMessageTask(ref, view.ID)
	}
	return nil
}

func (c *taskChannel) ReportProgress(status string) {
	c.task.AppendLog(i18n.Tf(c.lang, "mcp.progress_log", status))
}

func (c *taskChannel) AskUser(question string, options []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.question = question
	c.options = options
	return nil
}

// pendingQuestion возвращает последний вопрос агента за шаг, если он был.
func (c *taskChannel) pendingQuestion() (string, []string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.question, c.options, c.question != ""
}

// chatChannel — канал агента, отвечающего в разговоре. Ответ на вопрос приходит
// обычным сообщением и продолжает разговор, поэтому вопрос показывается сразу,
// а варианты — списком.
type chatChannel struct {
	m          ports.Messenger
	chat       ports.ChatID
	lang       string
	onProgress func(status string)
}

func (c *chatChannel) SendMessage(text string) error {
	_, err := c.m.Send(context.Background(), c.chat, utils.MarkdownToTelegramHTML(text), ports.Rich())
	return err
}

func (c *chatChannel) ReportProgress(status string) {
	if c.onProgress != nil {
		c.onProgress(status)
	}
}

func (c *chatChannel) AskUser(question string, options []string) error {
	var bldr strings.Builder
	bldr.WriteString(i18n.Tf(c.lang, "mcp.chat_question", utils.MarkdownToTelegramHTML(question)))
	for _, option := range options {
		bldr.WriteString("\n• " + html.EscapeString(option))
	}
	_, err := c.m.Send(context.Background(), c.chat, bldr.String(), ports.Rich())
	return err
}
