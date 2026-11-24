package acp

import (
	"strings"

	"bro-bot/internal/tools"
)

// PermOption описывает вариант ответа на запрос разрешения session/request_permission.
type PermOption struct {
	OptionID string `json:"optionId"`
	Kind     string `json:"kind"`
}

// ToolCallPermission содержит метаданные вызова инструмента из session/request_permission.
type ToolCallPermission struct {
	Kind     string         `json:"kind"`
	Name     string         `json:"name"`
	Title    string         `json:"title"`
	RawInput map[string]any `json:"rawInput"`
	Args     map[string]any `json:"args"`
}

// PermissionParams — параметры запроса session/request_permission.
type PermissionParams struct {
	ToolCall ToolCallPermission `json:"toolCall"`
	Options  []PermOption       `json:"options"`
}

// ReadOnlyToolKind сообщает, относится ли kind к безопасным операциям чтения.
func ReadOnlyToolKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "read", "search", "think", "fetch":
		return true
	default:
		return false
	}
}

// IsBotOrInteractiveTool разрешает безопасные инструменты bro_bot, поиск MCP-инструментов
// и встроенный инструмент вопросов (ask_question / AskQuestion) в режимах /chat и /plan.
func IsBotOrInteractiveTool(call ToolCallPermission) bool {
	name := strings.ToLower(strings.TrimSpace(call.Name))
	title := strings.ToLower(strings.TrimSpace(call.Title))

	if name == "ask_question" || name == "askquestion" ||
		title == "ask_question" || title == "run ask_question" {
		return true
	}

	// Grok: поиск метаданных подключённых MCP-инструментов безопасен в ReadOnly.
	if name == "search_tool" || title == "search_tool" || strings.HasPrefix(title, "search tools:") {
		return true
	}

	// Проверяем RawInput и Args:
	// - agy: name="call_mcp_tool", rawInput={"ServerName":"bro_bot","ToolName":"list_projects"}
	// - grok: title="use_tool"/"bro_bot__...", rawInput={"tool_name":"bro_bot__list_projects"}
	for _, m := range []map[string]any{call.RawInput, call.Args} {
		if m == nil {
			continue
		}
		if srv := firstString(m, "ServerName", "server_name", "server"); srv != "" {
			// Если сервер указан явно, разрешаем только bro_bot и не падаем в угадывание по заголовку.
			return isBroBotServerName(srv)
		}
		if tn := firstString(m, "ToolName", "tool_name", "tool"); tn != "" {
			return IsBotMCPToolTitle(tn)
		}
	}

	return IsBotMCPToolTitle(call.Name) || IsBotMCPToolTitle(call.Title)
}

func isBroBotServerName(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "bro_bot" || s == "bro-bot"
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok {
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// IsBotMCPToolTitle распознаёт вызовы MCP-инструментов самого bro-bot (bro_bot / bro-bot).
func IsBotMCPToolTitle(toolTitle string) bool {
	lower := strings.ToLower(strings.TrimSpace(toolTitle))
	if lower == "" {
		return false
	}
	for _, prefix := range []string{
		"mcp__bro_bot__", "mcp__bro-bot__",
		"bro_bot__", "bro-bot__",
		"bro_bot:", "bro_bot/", "bro-bot:", "bro-bot/",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	if lower == "bro_bot" || lower == "bro-bot" {
		return true
	}
	// Разбираем формат вида "bro_bot: ask_user" или суффикс после '__', '/', ':', ' '.
	base := lower
	for _, sep := range []string{"__", "/", ":", " "} {
		if idx := strings.LastIndex(base, sep); idx != -1 {
			base = strings.TrimSpace(base[idx+len(sep):])
		}
	}
	return tools.IsBotToolName(base)
}

// ChoosePermission выбирает вариант ответа на session/request_permission
// с учётом режима readOnly (/chat, /plan) и метаданных вызываемого инструмента.
func ChoosePermission(readOnly bool, call ToolCallPermission, options []PermOption) (string, bool) {
	if len(options) == 0 {
		return "", false
	}
	allowedInReadOnly := ReadOnlyToolKind(call.Kind) || IsBotOrInteractiveTool(call)
	var prefer []string
	switch {
	case readOnly && !allowedInReadOnly:
		prefer = []string{"reject_once", "reject_always"}
	case readOnly:
		prefer = []string{"allow_once", "allow_always"}
	default:
		prefer = []string{"allow_always", "allow_once"}
	}
	for _, kind := range prefer {
		for _, opt := range options {
			if strings.EqualFold(opt.Kind, kind) && opt.OptionID != "" {
				return opt.OptionID, true
			}
		}
	}
	return "", false
}

// PermissionResult формирует JSON-ответ на session/request_permission.
func PermissionResult(optionID string, ok bool) any {
	if !ok {
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	}
	return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": optionID}}
}
