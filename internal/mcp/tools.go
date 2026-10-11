package mcp

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Имена инструментов бота. Их знают обе стороны моста: `mcp-serve` объявляет
// инструменты агенту, бот выполняет вызовы.
const (
	ToolSendMessage    = "telegram_send_message"
	ToolAskUser        = "ask_user"
	ToolReportProgress = "report_progress"
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
	r := NewToolRegistry()
	forward := func(name string) ToolHandler {
		return func(ctx context.Context, args map[string]interface{}) (string, error) {
			res, err := CallBridge(ctx, socketPath, BridgeCall{Tool: name, Arguments: args})
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

// BridgeToolDefinitions описывает инструменты бота для агента.
func BridgeToolDefinitions() []Tool {
	return []Tool{
		{
			Name: ToolSendMessage,
			Description: "Send a message to the user in the Telegram chat of the current task. " +
				"Use it for important notices that should not wait until the end of your turn.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"message": map[string]interface{}{
						"type":        "string",
						"description": "Text of the message; Markdown is supported",
					},
				},
				"required": []string{"message"},
			},
		},
		{
			Name: ToolAskUser,
			Description: "Ask the user a clarifying question in Telegram; options become answer buttons. " +
				"The question is delivered immediately and the user's answer arrives as your next user message, " +
				"so after calling this tool end your turn without doing anything else.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"question": map[string]interface{}{
						"type":        "string",
						"description": "Question text; Markdown is supported",
					},
					"options": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Optional answer choices shown as buttons",
					},
				},
				"required": []string{"question"},
			},
		},
		{
			Name:        ToolReportProgress,
			Description: "Report the current step of the task. It is shown in the live status message in Telegram.",
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
