package grok

import (
	"encoding/json"
	"strings"
)

// События, которые уже понимают диалог и пайплайн задач (stream-json agy).
// Адаптер переводит в них уведомления ACP, чтобы остальной бот не знал про протокол.

type streamEvent struct {
	Event          string            `json:"event"`
	ConversationID string            `json:"conversation_id,omitempty"`
	StepUpdate     *streamStepUpdate `json:"step_update,omitempty"`
	Result         *streamResult     `json:"result,omitempty"`
}

type streamStepUpdate struct {
	ConversationID string          `json:"conversation_id,omitempty"`
	StepIndex      int             `json:"step_index,omitempty"`
	State          string          `json:"state,omitempty"`
	StepType       string          `json:"step_type,omitempty"`
	ToolName       string          `json:"tool_name,omitempty"`
	ToolInfo       *streamToolInfo `json:"tool_info,omitempty"`
	TextDelta      string          `json:"text_delta,omitempty"`
	Usage          *streamUsage    `json:"usage,omitempty"`
}

type streamToolInfo struct {
	Name       string         `json:"name,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
	Output     string         `json:"output,omitempty"`
}

type streamResult struct {
	ConversationID  string       `json:"conversation_id,omitempty"`
	Status          string       `json:"status"`
	Error           string       `json:"error,omitempty"`
	Response        string       `json:"response,omitempty"`
	DurationSeconds float64      `json:"duration_seconds,omitempty"`
	NumTurns        int          `json:"num_turns,omitempty"`
	Usage           *streamUsage `json:"usage,omitempty"`
}

type streamUsage struct {
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
	RawOutput     json.RawMessage `json:"rawOutput"`
	Locations     []struct {
		Path string `json:"path"`
	} `json:"locations"`
}

// contentText достаёт текст из блока ACP или из массива кусков tool content.
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

func toolLabel(u sessionUpdate) string {
	for _, name := range []string{u.ToolName, u.Name, u.Title, u.Kind} {
		if strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	if u.ToolCallID != "" {
		return u.ToolCallID
	}
	return "tool"
}

// translateUpdate переводит одно уведомление session/update в событие потока.
// Мысли агента в ответ не попадают: их видит только журнал, если текст пуст.
// Второе значение false — событие показывать не нужно.
func translateUpdate(sessionID string, raw json.RawMessage, step int) (streamEvent, bool) {
	var params struct {
		Update sessionUpdate `json:"update"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return streamEvent{}, false
	}
	u := params.Update
	switch u.SessionUpdate {
	case "agent_message_chunk":
		text := contentText(u.Content)
		if text == "" {
			return streamEvent{}, false
		}
		return streamEvent{
			Event:          "step_update",
			ConversationID: sessionID,
			StepUpdate: &streamStepUpdate{
				ConversationID: sessionID,
				StepIndex:      step,
				State:          "ACTIVE",
				StepType:       "agent_response",
				TextDelta:      text,
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
		// Повторные «в работе» не засоряют ленту: важен старт и завершение.
		if u.SessionUpdate == "tool_call_update" && state == "ACTIVE" {
			return streamEvent{}, false
		}
		params := u.RawInput
		if params == nil {
			params = map[string]any{}
		}
		if len(u.Locations) > 0 && params["path"] == nil && u.Locations[0].Path != "" {
			params["path"] = u.Locations[0].Path
		}
		output := contentText(u.Content)
		if output == "" {
			output = contentText(u.RawOutput)
		}
		if len(output) > 4000 {
			output = output[:4000] + "…"
		}
		return streamEvent{
			Event:          "step_update",
			ConversationID: sessionID,
			StepUpdate: &streamStepUpdate{
				ConversationID: sessionID,
				StepIndex:      step,
				State:          state,
				StepType:       "tool",
				ToolName:       toolLabel(u),
				ToolInfo: &streamToolInfo{
					Name:       toolLabel(u),
					Parameters: params,
					Output:     output,
				},
			},
		}, true
	default:
		return streamEvent{}, false
	}
}

func encodeEvent(evt streamEvent) ([]byte, error) {
	data, err := json.Marshal(evt)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
