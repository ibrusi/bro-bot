package acp

import (
	"encoding/json"
	"strings"

	"bro-bot/internal/tools"
)

// События, которые понимают диалог и пайплайн задач (stream-json формат).
// Адаптеры ACP переводят в них уведомления session/update и итоги session/prompt.

type StreamEvent struct {
	Event          string            `json:"event"`
	ConversationID string            `json:"conversation_id,omitempty"`
	StepUpdate     *StreamStepUpdate `json:"step_update,omitempty"`
	Result         *StreamResult     `json:"result,omitempty"`
}

type StreamStepUpdate struct {
	ConversationID string          `json:"conversation_id,omitempty"`
	StepIndex      int             `json:"step_index,omitempty"`
	State          string          `json:"state,omitempty"`
	StepType       string          `json:"step_type,omitempty"`
	ToolCallID     string          `json:"tool_call_id,omitempty"`
	ToolName       string          `json:"tool_name,omitempty"`
	ToolInfo       *StreamToolInfo `json:"tool_info,omitempty"`
	TextDelta      string          `json:"text_delta,omitempty"`
	Usage          *StreamUsage    `json:"usage,omitempty"`
}

type StreamToolInfo struct {
	Name       string         `json:"name,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
	Output     string         `json:"output,omitempty"`
}

type StreamResult struct {
	ConversationID  string       `json:"conversation_id,omitempty"`
	Status          string       `json:"status"`
	Error           string       `json:"error,omitempty"`
	Response        string       `json:"response,omitempty"`
	DurationSeconds float64      `json:"duration_seconds,omitempty"`
	NumTurns        int          `json:"num_turns,omitempty"`
	Usage           *StreamUsage `json:"usage,omitempty"`
}

type StreamUsage struct {
	InputTokens     int64 `json:"input_tokens,omitempty"`
	OutputTokens    int64 `json:"output_tokens,omitempty"`
	ThinkingTokens  int64 `json:"thinking_tokens,omitempty"`
	CacheReadTokens int64 `json:"cache_read_tokens,omitempty"`
	TotalTokens     int64 `json:"total_tokens,omitempty"`
}

type sessionUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content"`
	ToolCallID    string          `json:"toolCallId"`
	Title         string          `json:"title"`
	Name          string          `json:"name"`
	ToolName      string          `json:"toolName"`
	Kind          string          `json:"kind"`
	Status        string          `json:"status"`
	RawInput      map[string]any  `json:"rawInput"`
	Args          map[string]any  `json:"args"`
	RawOutput     json.RawMessage `json:"rawOutput"`
	Locations     []struct {
		Path string `json:"path"`
	} `json:"locations"`
	Usage *rpcUsage `json:"usage"`
	Used  int64     `json:"used"`
	Size  int64     `json:"size"`
	Meta  struct {
		Usage *rpcUsage `json:"usage"`
	} `json:"_meta"`
}

type rpcUsage struct {
	InputTokens       int64 `json:"inputTokens"`
	InputTokensSnake  int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"outputTokens"`
	OutputTokensSnake int64 `json:"output_tokens"`
	CachedReadTokens  int64 `json:"cachedReadTokens"`
	CacheReadTokens   int64 `json:"cacheReadTokens"`
	CacheReadSnake    int64 `json:"cache_read_tokens"`
	CacheReadInputSnk int64 `json:"cache_read_input_tokens"`
	CacheWriteTokens  int64 `json:"cacheWriteTokens"`
	CacheWriteSnake   int64 `json:"cache_write_tokens"`
	CacheCreationSnk  int64 `json:"cache_creation_input_tokens"`
	ThoughtTokens     int64 `json:"thoughtTokens"`
	ReasoningTokens   int64 `json:"reasoningTokens"`
	ThinkingSnake     int64 `json:"thinking_tokens"`
	TotalTokens       int64 `json:"totalTokens"`
	TotalTokensSnake  int64 `json:"total_tokens"`
}

func firstPositive(vals ...int64) int64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func (u *rpcUsage) toStreamUsage() *StreamUsage {
	if u == nil {
		return nil
	}
	in := firstPositive(u.InputTokens, u.InputTokensSnake)
	out := firstPositive(u.OutputTokens, u.OutputTokensSnake)
	thinking := firstPositive(u.ThoughtTokens, u.ReasoningTokens, u.ThinkingSnake)
	cacheRead := firstPositive(u.CachedReadTokens, u.CacheReadTokens, u.CacheReadSnake, u.CacheReadInputSnk)
	total := firstPositive(u.TotalTokens, u.TotalTokensSnake)

	if in == 0 && out == 0 && thinking == 0 && cacheRead == 0 && total == 0 {
		return nil
	}
	if total == 0 {
		total = in + out + thinking
	}
	return &StreamUsage{
		InputTokens:     in,
		OutputTokens:    out,
		ThinkingTokens:  thinking,
		CacheReadTokens: cacheRead,
		TotalTokens:     total,
	}
}

// ParseUsage извлекает статистику токенов из результата session/prompt:
// поддерживает как top-level usage (agy), так и _meta.usage (grok).
func ParseUsage(raw json.RawMessage) *StreamUsage {
	if len(bytesTrim(raw)) == 0 {
		return nil
	}
	var res struct {
		Usage *rpcUsage `json:"usage"`
		Meta  struct {
			Usage *rpcUsage `json:"usage"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil
	}
	if su := res.Usage.toStreamUsage(); su != nil {
		return su
	}
	return res.Meta.Usage.toStreamUsage()
}

// contentText достаёт текст из блока ACP или из массива фрагментов tool content.
func contentText(raw json.RawMessage) string {
	raw = bytesTrim(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var block struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &block) == nil && block.Text != "" {
		return block.Text
	}
	var parts []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, part := range parts {
		text := part.Text
		if text == "" {
			text = part.Content.Text
		}
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	return b.String()
}

func bytesTrim(raw json.RawMessage) json.RawMessage {
	return json.RawMessage(strings.TrimSpace(string(raw)))
}

func toolLabel(u sessionUpdate, params map[string]any) string {
	if strings.EqualFold(strings.TrimSpace(u.Name), "call_mcp_tool") && params != nil {
		srv := firstString(params, "ServerName", "server_name", "server")
		tn := firstString(params, "ToolName", "tool_name", "tool")
		if isBroBotServerName(srv) && tn != "" {
			if canon := tools.NormalizeBotToolName(tn); tools.IsBotToolName(canon) {
				return "mcp__bro_bot__" + canon
			}
			return "mcp__bro_bot__" + tn
		}
	}
	for _, name := range []string{u.ToolName, u.Name, u.Title, u.Kind} {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		if canon := tools.NormalizeBotToolName(trimmed); canon != strings.ToLower(trimmed) && tools.IsBotToolName(canon) {
			return "mcp__bro_bot__" + canon
		}
		return trimmed
	}
	if u.ToolCallID != "" {
		return u.ToolCallID
	}
	return "tool"
}

// TranslateUpdate переводит одно уведомление session/update в событие потока.
// Мысли агента в ответ не попадают; второе значение false означает, что событие не отображается.
func TranslateUpdate(sessionID string, raw json.RawMessage, step int) (StreamEvent, bool) {
	var wrapper struct {
		Update sessionUpdate `json:"update"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return StreamEvent{}, false
	}
	u := wrapper.Update
	switch u.SessionUpdate {
	case "agent_message_chunk":
		text := contentText(u.Content)
		if text == "" {
			return StreamEvent{}, false
		}
		return StreamEvent{
			Event:          "step_update",
			ConversationID: sessionID,
			StepUpdate: &StreamStepUpdate{
				ConversationID: sessionID,
				StepIndex:      step,
				State:          "ACTIVE",
				StepType:       "agent_response",
				TextDelta:      text,
			},
		}, true

	case "usage_update":
		su := u.Usage.toStreamUsage()
		if su == nil {
			su = u.Meta.Usage.toStreamUsage()
		}
		if su == nil && u.Used > 0 {
			su = &StreamUsage{TotalTokens: u.Used}
		}
		if su == nil {
			return StreamEvent{}, false
		}
		return StreamEvent{
			Event:          "step_update",
			ConversationID: sessionID,
			StepUpdate: &StreamStepUpdate{
				ConversationID: sessionID,
				StepIndex:      step,
				State:          "DONE",
				StepType:       "usage",
				Usage:          su,
			},
		}, true

	case "tool_call", "tool_call_update":
		state := "ACTIVE"
		switch strings.ToLower(u.Status) {
		case "completed":
			state = "DONE"
		case "failed":
			state = "FAILED"
		}
		hasPayload := len(u.RawInput) > 0 || len(u.Args) > 0 || len(u.Locations) > 0 ||
			strings.TrimSpace(u.ToolName) != "" || strings.TrimSpace(u.Name) != ""
		// Повторные «в работе» без входных параметров не засоряют ленту,
		// но обновления с параметрами (например, вопросы ask_user/ask_question) сохраняем.
		if u.SessionUpdate == "tool_call_update" && state == "ACTIVE" && !hasPayload {
			return StreamEvent{}, false
		}
		params := make(map[string]any)
		for k, v := range u.Args {
			params[k] = v
		}
		for k, v := range u.RawInput {
			params[k] = v
		}
		if len(u.Locations) > 0 && params["path"] == nil && u.Locations[0].Path != "" {
			params["path"] = u.Locations[0].Path
		}
		label := toolLabel(u, params)
		output := contentText(u.Content)
		if output == "" {
			output = contentText(u.RawOutput)
		}
		if len(output) > 4000 {
			output = output[:4000] + "…"
		}
		su := u.Usage.toStreamUsage()
		if su == nil {
			su = u.Meta.Usage.toStreamUsage()
		}
		return StreamEvent{
			Event:          "step_update",
			ConversationID: sessionID,
			StepUpdate: &StreamStepUpdate{
				ConversationID: sessionID,
				StepIndex:      step,
				State:          state,
				StepType:       "tool",
				ToolCallID:     u.ToolCallID,
				ToolName:       label,
				ToolInfo: &StreamToolInfo{
					Name:       label,
					Parameters: params,
					Output:     output,
				},
				Usage: su,
			},
		}, true

	default:
		return StreamEvent{}, false
	}
}

// EncodeEvent сериализует StreamEvent в одну строку NDJSON.
func EncodeEvent(evt StreamEvent) ([]byte, error) {
	data, err := json.Marshal(evt)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
