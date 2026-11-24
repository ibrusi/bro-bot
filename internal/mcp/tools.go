package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"bro-bot/internal/ports"
)

// Имена инструментов бота. Их знают обе стороны моста: `mcp-serve` объявляет
// инструменты агенту, бот выполняет вызовы.
const (
	ToolSendMessage    = "telegram_send_message"
	ToolAskUser        = "ask_user"
	ToolReportProgress = "report_progress"
	ToolSendFile       = "send_file"
	ToolGetTaskContext = "get_task_context"
	ToolListTasks      = "list_tasks"
	ToolListProjects   = "list_projects"

	// ChannelTokenEnv — одноразовый криптографический токен шага, передаваемый
	// дочернему процессу агента как резервный идентификатор канала к PID-дереву.
	ChannelTokenEnv = "BRO_BOT_CHANNEL_TOKEN"
)

// ToolHandler — функция-обработчик вызова MCP-инструмента.
type ToolHandler func(ctx context.Context, args map[string]interface{}) (string, error)

// ToolRegistry хранит зарегистрированные инструменты MCP-сервера.
type ToolRegistry struct {
	mu       sync.RWMutex
	tools    []Tool
	handlers map[string]ToolHandler
}

// NewToolRegistry создаёт пустой реестр инструментов.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		handlers: make(map[string]ToolHandler),
	}
}

// NewBridgeTools создаёт реестр инструментов бота, каждый вызов которых уходит
// работающему боту через мост на сокете socketPath. Если бот недоступен, агент
// получает ошибку, а не ложное «доставлено».
func NewBridgeTools(socketPath string) *ToolRegistry {
	return NewBridgeToolsWithToken(socketPath, "")
}

// NewBridgeToolsWithToken создаёт реестр инструментов моста с явным токеном шага
// (либо читает BRO_BOT_CHANNEL_TOKEN из окружения, если channelToken пуст).
func NewBridgeToolsWithToken(socketPath, channelToken string) *ToolRegistry {
	token := strings.TrimSpace(channelToken)
	if token == "" {
		token = strings.TrimSpace(os.Getenv(ChannelTokenEnv))
	}
	r := NewToolRegistry()
	forward := func(name string) ToolHandler {
		return func(ctx context.Context, args map[string]interface{}) (string, error) {
			res, err := CallBridge(ctx, socketPath, BridgeCall{
				Tool:         name,
				Arguments:    args,
				ChannelToken: token,
			})
			if err != nil {
				return "", err
			}
			if res.IsError {
				return "", errors.New(res.Text)
			}
			return res.Text, nil
		}
	}
	for _, tool := range BridgeToolDefinitions() {
		r.Register(tool, forward(tool.Name))
	}
	return r
}

// NewInProcessTools создаёт реестр инструментов бота, выполняющий вызовы напрямую
// в памяти процесса через handler (без Unix-сокета и подпроцесса mcp-serve).
func NewInProcessTools(handler ports.BotToolHandler) *ToolRegistry {
	r := NewToolRegistry()
	for _, tool := range BridgeToolDefinitions() {
		name := tool.Name
		r.Register(tool, func(ctx context.Context, args map[string]interface{}) (string, error) {
			if handler == nil {
				return "", errors.New("bot tool handler is not configured")
			}
			res := handler.CallBotTool(ctx, name, args)
			if res.IsError {
				return "", errors.New(res.Text)
			}
			return res.Text, nil
		})
	}
	return r
}

// BridgeToolDefinitions описывает инструменты бота для агента.
func BridgeToolDefinitions() []Tool {
	return []Tool{
		{
			Name: ToolSendMessage,
			Description: "Send a message to the user in the Telegram chat of the current task or conversation. " +
				"Supports Markdown formatting and automatically splits long messages into multiple Telegram chunks.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"message": map[string]interface{}{
						"type":        "string",
						"description": "Text of the message; Markdown is supported",
					},
					"silent": map[string]interface{}{
						"type":        "boolean",
						"description": "Optional: send without notification sound",
					},
				},
				"required": []string{"message"},
			},
		},
		{
			Name: ToolAskUser,
			Description: "Ask the user one or more clarifying questions in Telegram; options become interactive buttons. " +
				"Pass either a single question (question + optional options + optional is_multi_select) " +
				"OR a batch in questions[] (multiple questions answered in one interactive flow). " +
				"You may call ask_user across as many Q&A rounds as needed until everything is clear. " +
				"In task or plan mode, end your turn after calling this tool so the user's reply arrives as the next message.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"question": map[string]interface{}{
						"type":        "string",
						"description": "Single question text (Markdown supported); omit if passing questions[]",
					},
					"options": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Optional answer choices shown as buttons for the single question",
					},
					"is_multi_select": map[string]interface{}{
						"type":        "boolean",
						"description": "Optional: allow picking multiple options simultaneously for the single question",
					},
					"questions": map[string]interface{}{
						"type":        "array",
						"description": "Batch of questions to ask the user together (up to 10 questions)",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"question": map[string]interface{}{
									"type":        "string",
									"description": "Question text (Markdown supported)",
								},
								"options": map[string]interface{}{
									"type":        "array",
									"items":       map[string]interface{}{"type": "string"},
									"description": "Optional answer choices shown as buttons",
								},
								"is_multi_select": map[string]interface{}{
									"type":        "boolean",
									"description": "Allow selecting multiple options for this question",
								},
							},
							"required": []string{"question"},
						},
					},
				},
			},
		},
		{
			Name:        ToolReportProgress,
			Description: "Report the current step of the task or chat analysis. Shown in the live status message in Telegram.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"status": map[string]interface{}{
						"type":        "string",
						"description": "Short description of the current step",
					},
					"percent": map[string]interface{}{
						"type":        "integer",
						"description": "Optional completion percentage (0-100)",
					},
				},
				"required": []string{"status"},
			},
		},
		{
			Name: ToolSendFile,
			Description: "Send a file to the user in Telegram in the context of the current task, plan, or chat. " +
				"Either pass path to send an existing file from the current project's workspace (max 20 MB), " +
				"OR pass filename + content to send generated text content directly from memory without modifying disk (ideal in read-only /plan or /chat mode).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Relative path to an existing file inside the current task project directory",
					},
					"filename": map[string]interface{}{
						"type":        "string",
						"description": "File name when sending in-memory content (e.g. 'schema.sql' or 'diagram.mmd')",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "UTF-8 file content to send directly from memory (paired with filename)",
					},
					"caption": map[string]interface{}{
						"type":        "string",
						"description": "Optional caption for the Telegram file message",
					},
				},
			},
		},
		{
			Name: ToolGetTaskContext,
			Description: "Read-only tool returning JSON metadata about the current or specified task/chat session: " +
				"id, project, status, agent, model, plan, pending_followups, pending_questions, and last_pr_url.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"task_id": map[string]interface{}{
						"type":        "integer",
						"description": "Optional numeric task ID (defaults to the current task or active/latest task in the project)",
					},
				},
			},
		},
		{
			Name: ToolListTasks,
			Description: "Read-only tool returning active and recent tasks in the current task's project (or another named project): " +
				"task IDs, statuses, agents, prompts, and PR URLs.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"project": map[string]interface{}{
						"type":        "string",
						"description": "Optional project name (defaults to the current task or chat project; pass '*' for all projects)",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of recent tasks to return (1-20, default 10)",
					},
				},
			},
		},
		{
			Name:        ToolListProjects,
			Description: "Read-only tool listing available git projects configured in PROJECTS_ROOT, marking the task project and active bot project.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}
}

// Register добавляет или переопределяет инструмент.
func (r *ToolRegistry) Register(tool Tool, handler ToolHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Если инструмент с таким именем уже есть — заменяем его описание
	found := false
	for i, t := range r.tools {
		if t.Name == tool.Name {
			r.tools[i] = tool
			found = true
			break
		}
	}
	if !found {
		r.tools = append(r.tools, tool)
	}
	r.handlers[tool.Name] = handler
}

// List возвращает список описаний доступных инструментов.
func (r *ToolRegistry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	res := make([]Tool, len(r.tools))
	copy(res, r.tools)
	return res
}

// Call вызывает инструмент по имени с переданными аргументами.
func (r *ToolRegistry) Call(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	r.mu.RLock()
	handler, ok := r.handlers[name]
	r.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("tool %q not found", name)
	}
	return handler(ctx, args)
}
