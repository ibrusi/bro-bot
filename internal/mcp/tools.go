package mcp

import (
	"context"
	"fmt"
	"sync"
)

// ToolHandler — функция-обработчик вызова MCP-инструмента.
type ToolHandler func(ctx context.Context, args map[string]interface{}) (string, error)

// ToolRegistry хранит зарегистрированные инструменты MCP-сервера.
type ToolRegistry struct {
	mu       sync.RWMutex
	tools    []Tool
	handlers map[string]ToolHandler
}

// NewToolRegistry создает новый реестр инструментов с набором разрешенных каналов по умолчанию.
func NewToolRegistry() *ToolRegistry {
	r := &ToolRegistry{
		handlers: make(map[string]ToolHandler),
	}
	r.registerDefaults()
	return r
}

func (r *ToolRegistry) registerDefaults() {
	r.Register(
		Tool{
			Name:        "telegram_send_message",
			Description: "Send a message or response directly to the user in Telegram chat",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"message": map[string]interface{}{
						"type":        "string",
						"description": "Text of the message to deliver to the user",
					},
				},
				"required": []string{"message"},
			},
		},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			msg, _ := args["message"].(string)
			if msg == "" {
				return "", fmt.Errorf("message argument is required")
			}
			return fmt.Sprintf("Message delivered to Telegram user: %s", msg), nil
		},
	)

	r.Register(
		Tool{
			Name:        "ask_user",
			Description: "Ask the user a clarifying question with optional choice buttons in Telegram",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"question": map[string]interface{}{
						"type":        "string",
						"description": "Question text to ask the user",
					},
					"options": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "List of selectable choice buttons",
					},
				},
				"required": []string{"question"},
			},
		},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			q, _ := args["question"].(string)
			if q == "" {
				return "", fmt.Errorf("question argument is required")
			}
			return fmt.Sprintf("Question asked to Telegram user: %s", q), nil
		},
	)

	r.Register(
		Tool{
			Name:        "report_progress",
			Description: "Report intermediate progress status of the current task to the user",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"status": map[string]interface{}{
						"type":        "string",
						"description": "Short description of the current progress or step",
					},
					"percent": map[string]interface{}{
						"type":        "integer",
						"description": "Optional completion percentage (0-100)",
					},
				},
				"required": []string{"status"},
			},
		},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			status, _ := args["status"].(string)
			return fmt.Sprintf("Progress reported: %s", status), nil
		},
	)
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
