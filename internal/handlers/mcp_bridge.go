package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/mcp"
	"bro-bot/internal/ports"
	"bro-bot/internal/tools"
	"bro-bot/internal/utils"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Сторона бота у MCP/API-моста: вызовы инструментов агента доходят сюда и
// выполняются в контексте задачи или разговора, которые запустили этого агента.

const (
	// maxAncestorDepth ограничивает подъём по предкам процесса: MCP-сервер — прямой
	// потомок агента или его обёртки (node, shell), глубокая цепочка не нужна.
	maxAncestorDepth = 16
	// maxQuestionOptions — сколько вариантов ответа превращается в кнопки.
	maxQuestionOptions = 10
	// maxQuestionOptionRunes ограничивает длину одного варианта ответа:
	// достаточно для развёрнутой формулировки без потери смысла и защищает хранилище/чат.
	maxQuestionOptionRunes = 500
	// maxToolTextRunes ограничивает текст, который агент отправляет за один вызов.
	maxToolTextRunes = 32000
	// maxSendFileBytes — безопасный предел отправки файла в Telegram (50 МБ).
	maxSendFileBytes = 50 * 1024 * 1024
	// maxSendFileCaptionRunes — лимит подписи к документу в Telegram (запас до 1024).
	maxSendFileCaptionRunes = 900
	// maxBridgeHeaderOverhead — запас под заголовок задачи при разбиении длинного текста.
	maxBridgeHeaderOverhead = 120
)

// agentChannel — то, что агент может сделать с пользователем через инструменты бота.
type agentChannel interface {
	SendMessage(text string, silent bool) error
	SendFile(ctx context.Context, path, caption string) error
	SendFileContent(ctx context.Context, filename, content, caption string) error
	ReportProgress(status string)
	AskQuestions(items []utils.ParsedQuestion) error
	TaskContext(taskID int) (string, error)
}

// agentChannels связывает процессы и токены запущенных агентов с их каналами к пользователю.
type agentChannels struct {
	mu        sync.Mutex
	byPID     map[int]agentChannel
	byToken   map[string]agentChannel
	parentPID func(pid int) (int, error)
}

var mcpChannels = newAgentChannels(mcp.ParentPID)

func newAgentChannels(parentPID func(int) (int, error)) *agentChannels {
	return &agentChannels{
		byPID:     make(map[int]agentChannel),
		byToken:   make(map[string]agentChannel),
		parentPID: parentPID,
	}
}

// generateChannelToken создаёт случайный одноразовый токен для привязки MCP-вызовов к шагу.
func generateChannelToken() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("tok-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

// bind привязывает процесс агента pid к каналу ch и возвращает отвязку.
func (c *agentChannels) bind(pid int, ch agentChannel) (unbind func()) {
	return c.bindWithToken(pid, "", ch)
}

// bindWithToken привязывает pid и/или токен шага token к каналу ch и возвращает
// идемпотентную функцию отвязки, не трогающую более новые привязки.
func (c *agentChannels) bindWithToken(pid int, token string, ch agentChannel) (unbind func()) {
	token = strings.TrimSpace(token)
	if (pid <= 0 && token == "") || ch == nil {
		return func() {}
	}
	c.mu.Lock()
	if c.byPID == nil {
		c.byPID = make(map[int]agentChannel)
	}
	if c.byToken == nil {
		c.byToken = make(map[string]agentChannel)
	}
	if pid > 0 {
		c.byPID[pid] = ch
	}
	if token != "" {
		c.byToken[token] = ch
	}
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		if pid > 0 && c.byPID[pid] == ch {
			delete(c.byPID, pid)
		}
		if token != "" && c.byToken[token] == ch {
			delete(c.byToken, token)
		}
		c.mu.Unlock()
	}
}

// lookup находит канал процесса pid или ближайшего его предка-агента.
func (c *agentChannels) lookup(pid int) (agentChannel, bool) {
	return c.lookupWithToken(pid, "")
}

// lookupWithToken находит канал по явному токену шага или по дереву PID.
func (c *agentChannels) lookupWithToken(pid int, token string) (agentChannel, bool) {
	token = strings.TrimSpace(token)
	if token != "" {
		c.mu.Lock()
		ch, ok := c.byToken[token]
		c.mu.Unlock()
		if ok {
			return ch, true
		}
	}
	for depth := 0; depth < maxAncestorDepth && pid > 1; depth++ {
		c.mu.Lock()
		ch, ok := c.byPID[pid]
		c.mu.Unlock()
		if ok {
			return ch, true
		}
		if c.parentPID == nil {
			return nil, false
		}
		parent, err := c.parentPID(pid)
		if err != nil || parent == pid {
			return nil, false
		}
		pid = parent
	}
	return nil, false
}

// handle выполняет вызов инструмента от процесса peerPID через MCP-мост.
func (c *agentChannels) handle(ctx context.Context, peerPID int, call mcp.BridgeCall) mcp.BridgeResult {
	ch, ok := c.lookupWithToken(peerPID, call.ChannelToken)
	if !ok {
		return bridgeError("no bro-bot task or chat is attached to this agent process; " +
			"the tool works only for agents started by the bot")
	}
	res := executeChannelTool(ctx, ch, call.Tool, call.Arguments)
	return mcp.BridgeResult{
		Text:    res.Text,
		IsError: res.IsError,
	}
}

// channelBotToolHandler связывает in-process API-адаптеры с тем же каналом инструментов бота.
type channelBotToolHandler struct {
	ch agentChannel
}

func newChannelBotToolHandler(ch agentChannel) ports.BotToolHandler {
	return &channelBotToolHandler{ch: ch}
}

func (h *channelBotToolHandler) CallBotTool(ctx context.Context, name string, args map[string]interface{}) ports.BotToolResult {
	if h == nil || h.ch == nil {
		return ports.BotToolResult{
			Text:    "no bro-bot channel is attached to this session",
			IsError: true,
		}
	}
	return executeChannelTool(ctx, h.ch, name, args)
}

func executeChannelTool(ctx context.Context, ch agentChannel, toolName string, args map[string]interface{}) ports.BotToolResult {
	normalized := tools.NormalizeBotToolName(toolName)

	switch normalized {
	case mcp.ToolSendMessage:
		message := stringArg(args, "message")
		if message == "" {
			return botToolError("the message argument is required")
		}
		silent, _ := args["silent"].(bool)
		if err := ch.SendMessage(utils.TruncateString(message, maxToolTextRunes), silent); err != nil {
			return botToolError("failed to deliver the message: " + err.Error())
		}
		return ports.BotToolResult{Text: "The message has been delivered to the user."}

	case mcp.ToolSendFile:
		relPath := stringArg(args, "path")
		filename := stringArg(args, "filename")
		content, hasContent := args["content"].(string)
		caption := utils.TruncateString(stringArg(args, "caption"), maxSendFileCaptionRunes)
		switch {
		case relPath != "":
			if err := ch.SendFile(ctx, relPath, caption); err != nil {
				return botToolError("failed to send the file: " + err.Error())
			}
			return ports.BotToolResult{Text: fmt.Sprintf("File %q has been sent to the user.", relPath)}
		case filename != "" && hasContent:
			if err := ch.SendFileContent(ctx, filename, content, caption); err != nil {
				return botToolError("failed to send the file: " + err.Error())
			}
			return ports.BotToolResult{Text: fmt.Sprintf("File %q has been sent to the user.", filepath.Base(filename))}
		default:
			return botToolError("either path or filename + content arguments are required")
		}

	case mcp.ToolAskUser:
		items := normalizeAskQuestionItems(args)
		if len(items) == 0 {
			return botToolError("the question argument or a non-empty questions array is required")
		}
		if err := ch.AskQuestions(items); err != nil {
			return botToolError("failed to ask the user: " + err.Error())
		}
		return ports.BotToolResult{
			Text: "The question has been shown to the user. " +
				"End your turn now without further actions: the answer will arrive as the next user message.",
			EndTurn: true,
		}

	case mcp.ToolReportProgress:
		status := stringArg(args, "status")
		if status == "" {
			status = stringArg(args, "message")
		}
		if status == "" {
			return botToolError("the status argument is required")
		}
		if percent, ok := args["percent"].(float64); ok && percent >= 0 && percent <= 100 {
			status = fmt.Sprintf("%s (%d%%)", status, int(percent))
		}
		ch.ReportProgress(utils.TruncateString(status, 200))
		return ports.BotToolResult{Text: "Progress recorded."}

	case mcp.ToolGetTaskContext:
		out, err := ch.TaskContext(intArg(args, "task_id"))
		if err != nil {
			return botToolError(err.Error())
		}
		return ports.BotToolResult{Text: out}

	case mcp.ToolListTasks:
		limit := intArg(args, "limit")
		if limit <= 0 {
			limit = 20
		} else if limit > 50 {
			limit = 50
		}
		projFilter := stringArg(args, "project")
		if projFilter == "*" || strings.EqualFold(projFilter, "all") {
			projFilter = ""
		}
		out, err := formatTasksListForAgent(projFilter, limit)
		if err != nil {
			return botToolError(err.Error())
		}
		return ports.BotToolResult{Text: out}

	case mcp.ToolListProjects:
		out, err := formatProjectsListForAgent()
		if err != nil {
			return botToolError(err.Error())
		}
		return ports.BotToolResult{Text: out}

	default:
		return botToolError(fmt.Sprintf("unknown tool %q", toolName))
	}
}

func bridgeError(text string) mcp.BridgeResult {
	return mcp.BridgeResult{Text: text, IsError: true}
}

func botToolError(text string) ports.BotToolResult {
	return ports.BotToolResult{Text: text, IsError: true}
}

func stringArg(args map[string]interface{}, key string) string {
	if args == nil {
		return ""
	}
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func intArg(args map[string]interface{}, key string) int {
	if args == nil {
		return 0
	}
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	}
	return 0
}

func normalizeAskQuestionItems(args map[string]interface{}) []utils.ParsedQuestion {
	raw := utils.ExtractAskQuestionItems(args)
	if len(raw) == 0 {
		return nil
	}
	out := make([]utils.ParsedQuestion, 0, len(raw))
	for _, it := range raw {
		q := utils.TruncateString(strings.TrimSpace(it.Question), maxToolTextRunes)
		if q == "" {
			continue
		}
		opts := normalizeQuestionOptions(it.Options)
		out = append(out, utils.ParsedQuestion{
			Question:    q,
			Options:     opts,
			MultiSelect: it.MultiSelect,
		})
	}
	return out
}

func normalizeQuestionOptions(raw []string) []string {
	var opts []string
	for _, opt := range raw {
		trimmed := strings.TrimSpace(opt)
		if trimmed == "" {
			continue
		}
		opts = append(opts, utils.TruncateString(trimmed, maxQuestionOptionRunes))
		if len(opts) == maxQuestionOptions {
			break
		}
	}
	return opts
}

// buildBotToolInstructions формирует секцию системного промпта с описанием доступных
// инструментов бота. Передаётся через ExecuteArgs.BotInstructions на каждом шаге.
func buildBotToolInstructions(lang string, isPlanning, isChat bool) string {
	var bldr strings.Builder
	bldr.WriteString(i18n.T(lang, "bot.tools_prompt_header"))
	bldr.WriteString("\n")
	bldr.WriteString(i18n.T(lang, "bot.tools_prompt_common"))
	if isChat {
		bldr.WriteString("\n\n")
		bldr.WriteString(i18n.T(lang, "bot.tools_prompt_chat"))
		return bldr.String()
	}
	bldr.WriteString("\n")
	bldr.WriteString(i18n.T(lang, "bot.tools_prompt_task_extra"))
	bldr.WriteString("\n")
	if isPlanning {
		bldr.WriteString(i18n.T(lang, "bot.tools_prompt_planning"))
	} else {
		bldr.WriteString(i18n.T(lang, "bot.tools_prompt_execution"))
	}
	return bldr.String()
}

// taskChannel — канал агента, выполняющего шаг задачи.
//
// Вопросы накапливаются за шаг (можно вызывать ask_user несколько раз или пачкой):
// шаг заканчивается, когда агент завершает ход, и пайплайн показывает их пошагово
// или списком с инлайн-кнопками.
type taskChannel struct {
	m            ports.Messenger
	chat         ports.ChatID
	task         *domain.TaskSession
	workDir      string
	projectsRoot string
	lang         string

	mu        sync.Mutex
	questions []utils.ParsedQuestion
}

func newTaskChannel(m ports.Messenger, chat ports.ChatID, task *domain.TaskSession, workDir, projectsRoot, lang string) *taskChannel {
	return &taskChannel{
		m:            m,
		chat:         chat,
		task:         task,
		workDir:      workDir,
		projectsRoot: projectsRoot,
		lang:         lang,
	}
}

func (c *taskChannel) SendMessage(text string, silent bool) error {
	view := c.task.Snapshot()
	opts := &ports.SendOptions{Format: ports.FormatRich, Silent: silent}
	fullMsg := i18n.Tf(c.lang, "mcp.task_message", view.ID, html.EscapeString(view.Project), utils.MarkdownToTelegramHTML(text))
	if utils.TelegramHTMLVisibleLen(fullMsg) <= utils.DefaultTelegramMaxChunkLen {
		ref, err := c.m.Send(context.Background(), c.chat, fullMsg, opts)
		if err != nil {
			return err
		}
		if ref.ID != "" {
			// Ответ на это сообщение должен попасть в ту же задачу.
			domain.GlobalTaskManager.RegisterMessageTask(ref, view.ID)
		}
		return nil
	}

	chunks := utils.SplitMarkdown(text, utils.DefaultMarkdownMaxChunkLen-maxBridgeHeaderOverhead)
	for i, chunk := range chunks {
		msgHTML := utils.MarkdownToTelegramHTML(chunk)
		if i == 0 {
			msgHTML = i18n.Tf(c.lang, "mcp.task_message", view.ID, html.EscapeString(view.Project), msgHTML)
		}
		ref, err := c.m.Send(context.Background(), c.chat, msgHTML, opts)
		if err != nil {
			return err
		}
		if ref.ID != "" {
			domain.GlobalTaskManager.RegisterMessageTask(ref, view.ID)
		}
	}
	return nil
}

func (c *taskChannel) SendFile(ctx context.Context, relPath, caption string) error {
	view := c.task.Snapshot()
	fallbackCaption := i18n.Tf(c.lang, "mcp.file_caption", view.ID, view.Project, relPath)
	return sendBridgeFile(ctx, c.m, c.chat, c.workDir, relPath, caption, fallbackCaption, view.ID)
}

func (c *taskChannel) SendFileContent(ctx context.Context, filename, content, caption string) error {
	view := c.task.Snapshot()
	base := filepath.Base(strings.TrimSpace(filename))
	fallbackCaption := i18n.Tf(c.lang, "mcp.file_caption", view.ID, view.Project, base)
	return sendBridgeMemoryFile(ctx, c.m, c.chat, filename, content, caption, fallbackCaption, view.ID)
}

func (c *taskChannel) ReportProgress(status string) {
	c.task.AppendLog(i18n.Tf(c.lang, "mcp.progress_log", status))
}

func (c *taskChannel) AskQuestions(items []utils.ParsedQuestion) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, it := range items {
		qText := strings.TrimSpace(it.Question)
		if qText == "" {
			continue
		}
		c.upsertQuestionLocked(it)
	}
	return nil
}

func (c *taskChannel) upsertQuestionLocked(it utils.ParsedQuestion) {
	for i, existing := range c.questions {
		if strings.EqualFold(strings.TrimSpace(existing.Question), strings.TrimSpace(it.Question)) {
			c.questions[i] = it
			return
		}
	}
	c.questions = append(c.questions, it)
}

// pendingQuestions возвращает все вопросы, заданные агентом через ask_user за этот шаг.
func (c *taskChannel) pendingQuestions() []domain.QuestionItem {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.questions) == 0 {
		return nil
	}
	out := make([]domain.QuestionItem, 0, len(c.questions))
	for _, q := range c.questions {
		out = append(out, domain.QuestionItem{
			Question:    q.Question,
			Options:     append([]string(nil), q.Options...),
			MultiSelect: q.MultiSelect,
		})
	}
	return out
}

func (c *taskChannel) TaskContext(taskID int) (string, error) {
	target := c.task
	if taskID > 0 {
		target = domain.GlobalTaskManager.GetTask(taskID)
	}
	if target == nil {
		return "", errors.New("task not found")
	}
	return formatTaskContextJSON(target.Snapshot())
}

// chatChannel — канал агента, отвечающего в разговоре.
type chatChannel struct {
	m            ports.Messenger
	chat         ports.ChatID
	project      string
	workDir      string
	projectsRoot string
	lang         string

	progressMu sync.Mutex
	onProgress func(status string)
}

func (c *chatChannel) setProgressFn(fn func(status string)) {
	c.progressMu.Lock()
	c.onProgress = fn
	c.progressMu.Unlock()
}

func (c *chatChannel) SendMessage(text string, silent bool) error {
	opts := &ports.SendOptions{Format: ports.FormatRich, Silent: silent}
	fullHTML := utils.MarkdownToTelegramHTML(text)
	if utils.TelegramHTMLVisibleLen(fullHTML) <= utils.DefaultTelegramMaxChunkLen {
		_, err := c.m.Send(context.Background(), c.chat, fullHTML, opts)
		return err
	}
	for _, chunk := range utils.SplitMarkdown(text, utils.DefaultMarkdownMaxChunkLen) {
		if _, err := c.m.Send(context.Background(), c.chat, utils.MarkdownToTelegramHTML(chunk), opts); err != nil {
			return err
		}
	}
	return nil
}

func (c *chatChannel) SendFile(ctx context.Context, relPath, caption string) error {
	fallbackCaption := i18n.Tf(c.lang, "mcp.chat_file_caption", c.project, relPath)
	return sendBridgeFile(ctx, c.m, c.chat, c.workDir, relPath, caption, fallbackCaption, 0)
}

func (c *chatChannel) SendFileContent(ctx context.Context, filename, content, caption string) error {
	base := filepath.Base(strings.TrimSpace(filename))
	fallbackCaption := i18n.Tf(c.lang, "mcp.chat_file_caption", c.project, base)
	return sendBridgeMemoryFile(ctx, c.m, c.chat, filename, content, caption, fallbackCaption, 0)
}

func (c *chatChannel) ReportProgress(status string) {
	c.progressMu.Lock()
	fn := c.onProgress
	c.progressMu.Unlock()
	if fn != nil {
		fn(status)
	}
}

func (c *chatChannel) AskQuestions(items []utils.ParsedQuestion) error {
	multiSuffix := i18n.T(c.lang, "markdown.multi_select")
	var bldr strings.Builder
	for idx, it := range items {
		if idx > 0 {
			bldr.WriteString("\n\n")
		}
		header := i18n.Tf(c.lang, "mcp.chat_question", utils.MarkdownToTelegramHTML(it.Question))
		if len(items) > 1 {
			header = fmt.Sprintf("<b>%d.</b> %s", idx+1, header)
		}
		if it.MultiSelect {
			header += html.EscapeString(multiSuffix)
		}
		bldr.WriteString(header)
		if len(it.Options) > 0 {
			if len(items) == 1 {
				bldr.WriteString("\n\n")
				bldr.WriteString(formatQuestionOptionsHTML(it.Options, false, nil))
			} else {
				for optIdx, option := range it.Options {
					bldr.WriteString(fmt.Sprintf("\n   %d) %s", optIdx+1, formatOptionTextHTML(option)))
				}
			}
		}
	}
	_, err := c.m.Send(context.Background(), c.chat, bldr.String(), ports.Rich())
	return err
}

func (c *chatChannel) TaskContext(taskID int) (string, error) {
	if taskID > 0 {
		target := domain.GlobalTaskManager.GetTask(taskID)
		if target == nil {
			return "", errors.New("task not found")
		}
		return formatTaskContextJSON(target.Snapshot())
	}
	if active := domain.GlobalTaskManager.GetActiveTask(); active != nil {
		view := active.Snapshot()
		if c.project == "" || view.Project == c.project {
			return formatTaskContextJSON(view)
		}
	}
	if tasks := domain.GlobalTaskManager.ListTasksByProject(c.project, 1); len(tasks) > 0 {
		return formatTaskContextJSON(tasks[0].Snapshot())
	}
	payload := map[string]interface{}{
		"project": c.project,
		"task":    nil,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func sendBridgeFile(ctx context.Context, m ports.Messenger, chat ports.ChatID, workDir, relPath, caption, fallbackCaption string, registerTaskID int) error {
	if m == nil {
		return errors.New("messenger is not configured")
	}
	if strings.TrimSpace(workDir) == "" {
		return errors.New("project workspace is not configured")
	}
	resolved, err := tools.NewExecutor(workDir).SafeResolvePath(relPath)
	if err != nil {
		return err
	}
	realWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return fmt.Errorf("workspace inaccessible: %w", err)
	}
	realFile, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return fmt.Errorf("file not found or inaccessible: %w", err)
	}
	relReal, err := filepath.Rel(realWorkDir, realFile)
	if err != nil || relReal == ".." || strings.HasPrefix(relReal, ".."+string(filepath.Separator)) {
		return errors.New("the resolved path escapes the project workspace")
	}

	info, err := os.Stat(realFile)
	if err != nil {
		return fmt.Errorf("file not found or inaccessible: %w", err)
	}
	if info.IsDir() {
		return errors.New("path is a directory, not a file")
	}
	if info.Size() == 0 {
		return errors.New("file is empty (0 bytes)")
	}
	if info.Size() > maxSendFileBytes {
		return fmt.Errorf("file is too large (%d bytes, max %d bytes)", info.Size(), maxSendFileBytes)
	}

	data, err := os.ReadFile(realFile)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	mimeType := mime.TypeByExtension(filepath.Ext(realFile))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	return sendBridgeBytes(ctx, m, chat, filepath.Base(realFile), data, mimeType, caption, fallbackCaption, registerTaskID)
}

func sendBridgeMemoryFile(ctx context.Context, m ports.Messenger, chat ports.ChatID, filename, content, caption, fallbackCaption string, registerTaskID int) error {
	if m == nil {
		return errors.New("messenger is not configured")
	}
	base := filepath.Base(strings.TrimSpace(filename))
	if base == "" || base == "." || base == ".." || base == string(filepath.Separator) {
		return errors.New("invalid filename")
	}
	if len(content) == 0 {
		return errors.New("file content is empty")
	}
	if len(content) > maxSendFileBytes {
		return fmt.Errorf("file is too large (%d bytes, max %d bytes)", len(content), maxSendFileBytes)
	}
	mimeType := mime.TypeByExtension(filepath.Ext(base))
	if mimeType == "" {
		mimeType = "text/plain; charset=utf-8"
	}
	return sendBridgeBytes(ctx, m, chat, base, []byte(content), mimeType, caption, fallbackCaption, registerTaskID)
}

func sendBridgeBytes(ctx context.Context, m ports.Messenger, chat ports.ChatID, fileName string, data []byte, mimeType, caption, fallbackCaption string, registerTaskID int) error {
	finalCaption := strings.TrimSpace(caption)
	if finalCaption == "" {
		finalCaption = fallbackCaption
	}
	finalCaption = utils.TruncateString(finalCaption, maxSendFileCaptionRunes)

	ref, err := m.SendDocument(ctx, chat, ports.Document{
		FileName: fileName,
		MIME:     mimeType,
		Caption:  finalCaption,
		Content:  data,
	})
	if err != nil {
		return err
	}
	if registerTaskID > 0 && ref.ID != "" {
		domain.GlobalTaskManager.RegisterMessageTask(ref, registerTaskID)
	}
	return nil
}

func formatTaskContextJSON(view domain.TaskView) (string, error) {
	payload := map[string]interface{}{
		"id":                     view.ID,
		"project":                view.Project,
		"status":                 string(view.Status),
		"agent":                  view.Agent,
		"model":                  view.Model,
		"requires_plan":          view.RequiresPlan,
		"plan_approved":          view.PlanApproved,
		"initial_prompt":         utils.TruncateString(view.InitialPrompt, 4000),
		"current_prompt":         utils.TruncateString(view.CurrentPrompt, 4000),
		"plan":                   utils.TruncateString(view.Plan, 8000),
		"pending_followups":      view.PendingFollowups,
		"last_question":          view.LastQuestion,
		"pending_questions":      view.PendingQuestions,
		"current_question_index": view.CurrentQuestionIndex,
		"last_pr_url":            view.LastPRURL,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func formatTasksListForAgent(project string, limit int) (string, error) {
	tasks := domain.GlobalTaskManager.ListTasksByProject(project, limit)
	type taskSummary struct {
		ID           int      `json:"id"`
		Project      string   `json:"project"`
		Status       string   `json:"status"`
		Agent        string   `json:"agent,omitempty"`
		RequiresPlan bool     `json:"requires_plan"`
		PlanApproved bool     `json:"plan_approved"`
		Prompt       string   `json:"prompt"`
		LastQuestion string   `json:"last_question,omitempty"`
		Followups    int      `json:"pending_followups_count,omitempty"`
		RecentLogs   []string `json:"-"`
	}
	summaries := make([]taskSummary, 0, len(tasks))
	for _, t := range tasks {
		view := t.Snapshot()
		summaries = append(summaries, taskSummary{
			ID:           view.ID,
			Project:      view.Project,
			Status:       string(view.Status),
			Agent:        view.Agent,
			RequiresPlan: view.RequiresPlan,
			PlanApproved: view.PlanApproved,
			Prompt:       utils.TruncateString(view.InitialPrompt, 200),
			LastQuestion: utils.TruncateString(view.LastQuestion, 200),
			Followups:    len(view.PendingFollowups),
		})
	}
	payload := map[string]interface{}{
		"project_filter": project,
		"count":          len(summaries),
		"tasks":          summaries,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func formatProjectsListForAgent() (string, error) {
	root := strings.TrimSpace(config.ProjectsRoot)
	if root == "" {
		return "", errors.New("projects root directory is not configured")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("failed to read projects directory: %w", err)
	}

	config.ProjectState.RLock()
	activeProject := config.ProjectState.CurrentProject
	activeModel := config.ProjectState.CurrentModel
	config.ProjectState.RUnlock()
	activeAgent := ActiveAgentName()

	activeCounts := make(map[string]int)
	waitingCounts := make(map[string]int)
	for _, t := range domain.GlobalTaskManager.ListTasksByProject("", 100) {
		v := t.Snapshot()
		switch v.Status {
		case domain.TaskStatusRunning, domain.TaskStatusPlanning, domain.TaskStatusQueued:
			activeCounts[v.Project]++
		case domain.TaskStatusWaitingInput:
			waitingCounts[v.Project]++
		}
	}

	type projectSummary struct {
		Name         string `json:"name"`
		Active       bool   `json:"active"`
		ActiveTasks  int    `json:"active_tasks"`
		WaitingTasks int    `json:"waiting_tasks"`
	}
	var projects []projectSummary
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		name := e.Name()
		projects = append(projects, projectSummary{
			Name:         name,
			Active:       name == activeProject,
			ActiveTasks:  activeCounts[name],
			WaitingTasks: waitingCounts[name],
		})
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Name < projects[j].Name
	})

	payload := map[string]interface{}{
		"active_project": activeProject,
		"active_agent":   activeAgent,
		"active_model":   activeModel,
		"projects":       projects,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
