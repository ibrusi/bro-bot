package domain

import (
	"bro-bot/internal/i18n"
	"bro-bot/internal/models"
	"bro-bot/internal/storage"
	"bro-bot/internal/utils"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// UsageStats содержит статистику токенов от модели.
type UsageStats struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ThinkingTokens  int64 `json:"thinking_tokens"`
	CacheReadTokens int64 `json:"cache_read_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}

// Add суммирует статистику другого шага или вызова.
func (u *UsageStats) Add(other UsageStats) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.ThinkingTokens += other.ThinkingTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.TotalTokens += other.TotalTokens
}

// CacheHitRate возвращает процент попаданий в кэш промпта.
func (u UsageStats) CacheHitRate() float64 {
	totalInput := u.InputTokens + u.CacheReadTokens
	if totalInput <= 0 {
		return 0
	}
	return (float64(u.CacheReadTokens) / float64(totalInput)) * 100
}

// StreamEvent описывает структуру событий agy при --output-format stream-json.
type StreamEvent struct {
	Event          string            `json:"event"`
	ConversationID string            `json:"conversation_id"`
	StepUpdate     *StreamStepUpdate `json:"step_update"`
	Result         *StreamResult     `json:"result"`
}

// ParseStreamEvent разбирает строку NDJSON от agy или claude.
func ParseStreamEvent(line string) (*StreamEvent, error) {
	var evt StreamEvent
	if err := json.Unmarshal([]byte(line), &evt); err == nil && evt.Event != "" {
		return &evt, nil
	}

	// Попытка разобрать как событие Claude Code CLI
	if claudeEvt, err := parseClaudeStreamEvent(line); err == nil && claudeEvt != nil {
		return claudeEvt, nil
	}

	return nil, fmt.Errorf("empty event")
}

// claudeRawEvent описывает NDJSON-событие Claude Code CLI при --output-format stream-json.
type claudeRawEvent struct {
	Type          string         `json:"type"`
	Subtype       string         `json:"subtype"`
	SessionID     string         `json:"session_id"`
	Message       *claudeMessage `json:"message"`
	Result        string         `json:"result"`
	IsError       bool           `json:"is_error"`
	Errors        []string       `json:"errors"`
	NumTurns      int            `json:"num_turns"`
	DurationMs    float64        `json:"duration_ms"`
	DurationAPIMs float64        `json:"duration_api_ms"`
	Usage         *claudeUsage   `json:"usage"`
}

type claudeMessage struct {
	Role    string               `json:"role"`
	Content []claudeContentBlock `json:"content"`
	Usage   *claudeUsage         `json:"usage"`
}

type claudeContentBlock struct {
	Type  string                 `json:"type"` // "text", "tool_use", "thinking"
	Text  string                 `json:"text,omitempty"`
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
}

type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	OutputTokensDetails      *struct {
		ThinkingTokens int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

func (u *claudeUsage) toUsageStats() *UsageStats {
	if u == nil {
		return nil
	}
	stats := &UsageStats{
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		CacheReadTokens: u.CacheReadInputTokens,
		TotalTokens:     u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
	}
	if u.OutputTokensDetails != nil {
		stats.ThinkingTokens = u.OutputTokensDetails.ThinkingTokens
	}
	return stats
}

func parseClaudeStreamEvent(line string) (*StreamEvent, error) {
	var raw claudeRawEvent
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil, err
	}
	if raw.Type == "" {
		return nil, fmt.Errorf("empty type")
	}

	switch raw.Type {
	case "system":
		return &StreamEvent{
			Event:          "init",
			ConversationID: raw.SessionID,
		}, nil

	case "assistant":
		if raw.Message == nil {
			return &StreamEvent{
				Event:          "assistant",
				ConversationID: raw.SessionID,
			}, nil
		}

		usage := raw.Message.Usage.toUsageStats()

		// 1. Проверяем наличие вызова инструмента (tool_use)
		for _, block := range raw.Message.Content {
			if block.Type == "tool_use" {
				return &StreamEvent{
					Event:          "step_update",
					ConversationID: raw.SessionID,
					StepUpdate: &StreamStepUpdate{
						ConversationID: raw.SessionID,
						State:          "ACTIVE",
						StepType:       "tool",
						ToolName:       block.Name,
						ToolInfo: &StreamToolInfo{
							Name:       block.Name,
							Parameters: block.Input,
						},
						Usage: usage,
					},
				}, nil
			}
		}

		// 2. Проверяем текстовые блоки ответа (text)
		var bldr strings.Builder
		for _, block := range raw.Message.Content {
			if block.Type == "text" && block.Text != "" {
				bldr.WriteString(block.Text)
			}
		}
		txt := bldr.String()
		if txt != "" {
			return &StreamEvent{
				Event:          "step_update",
				ConversationID: raw.SessionID,
				StepUpdate: &StreamStepUpdate{
					ConversationID: raw.SessionID,
					State:          "ACTIVE",
					StepType:       "agent_response",
					TextDelta:      txt,
					Usage:          usage,
				},
			}, nil
		}

		// Если нет текста и tool_use (например, только thinking), возвращаем шаг с usage
		return &StreamEvent{
			Event:          "step_update",
			ConversationID: raw.SessionID,
			StepUpdate: &StreamStepUpdate{
				ConversationID: raw.SessionID,
				State:          "ACTIVE",
				StepType:       "agent_response",
				Usage:          usage,
			},
		}, nil

	case "result":
		status := "COMPLETED"
		if raw.IsError || raw.Subtype == "error_during_execution" || len(raw.Errors) > 0 {
			status = "ERROR"
		}
		var errText string
		if len(raw.Errors) > 0 {
			errText = strings.Join(raw.Errors, "; ")
		}

		dur := raw.DurationMs / 1000.0
		if dur <= 0 && raw.DurationAPIMs > 0 {
			dur = raw.DurationAPIMs / 1000.0
		}

		return &StreamEvent{
			Event:          "result",
			ConversationID: raw.SessionID,
			Result: &StreamResult{
				ConversationID:  raw.SessionID,
				Status:          status,
				Error:           errText,
				Response:        raw.Result,
				DurationSeconds: dur,
				NumTurns:        raw.NumTurns,
				Usage:           raw.Usage.toUsageStats(),
			},
		}, nil

	default:
		return &StreamEvent{
			Event:          raw.Type,
			ConversationID: raw.SessionID,
		}, nil
	}
}

// StreamStepUpdate описывает обновление шага выполнения agy.
type StreamStepUpdate struct {
	ConversationID  string          `json:"conversation_id"`
	StepIndex       int             `json:"step_index"`
	State           string          `json:"state"`
	StepType        string          `json:"step_type"`
	ToolName        string          `json:"tool_name"`
	ToolInfo        *StreamToolInfo `json:"tool_info"`
	TextDelta       string          `json:"text_delta"`
	DurationSeconds float64         `json:"duration_seconds"`
	Usage           *UsageStats     `json:"usage"`
}

// StreamToolInfo содержит параметры и вывод вызванного инструмента.
type StreamToolInfo struct {
	Name       string                 `json:"name"`
	Parameters map[string]interface{} `json:"parameters"`
	Output     string                 `json:"output"`
}

// StreamResult описывает итоговый результат выполнения agy.
type StreamResult struct {
	ConversationID  string      `json:"conversation_id"`
	Status          string      `json:"status"`
	Error           string      `json:"error,omitempty"`
	Response        string      `json:"response"`
	DurationSeconds float64     `json:"duration_seconds"`
	NumTurns        int         `json:"num_turns"`
	Usage           *UsageStats `json:"usage"`
}

// IsError возвращает true, если результат содержит статус ошибки или текст ошибки.
func (r *StreamResult) IsError() bool {
	if r == nil {
		return false
	}
	return strings.EqualFold(r.Status, "ERROR") || strings.TrimSpace(r.Error) != ""
}

// TaskTokenMetrics хранит полные метрики токенов задачи или шага.
type TaskTokenMetrics struct {
	Project         string
	Model           string
	Agent           string
	Prompt          string
	PRURL           string
	ConversationID  string
	StartedAt       time.Time
	FinishedAt      time.Time
	DurationSeconds float64
	Usage           UsageStats
	LastStepUsage   UsageStats
	Turns           int
	ToolCallsCount  int
}

// EffectiveDuration возвращает фактическую длительность выполнения в секундах.
func (m TaskTokenMetrics) EffectiveDuration() float64 {
	if m.DurationSeconds > 0 {
		return m.DurationSeconds
	}
	if !m.FinishedAt.IsZero() && !m.StartedAt.IsZero() {
		return m.FinishedAt.Sub(m.StartedAt).Seconds()
	}
	if !m.StartedAt.IsZero() {
		return time.Since(m.StartedAt).Seconds()
	}
	return 0
}

// TokensPerSecond возвращает скорость генерации выходных токенов в секунду.
func (m TaskTokenMetrics) TokensPerSecond() float64 {
	dur := m.EffectiveDuration()
	if dur <= 0 {
		return 0
	}
	return float64(m.Usage.OutputTokens) / dur
}

// TotalTokensPerSecond возвращает общую скорость обработки всех токенов в секунду.
func (m TaskTokenMetrics) TotalTokensPerSecond() float64 {
	dur := m.EffectiveDuration()
	if dur <= 0 {
		return 0
	}
	return float64(m.Usage.TotalTokens) / dur
}

// FormatCompletionSummary формирует блок статистики для завершающего сообщения.
func (m TaskTokenMetrics) FormatCompletionSummary(lang string) string {
	dur := m.EffectiveDuration()
	durStr := FormatDurationHuman(time.Duration(dur * float64(time.Second)))
	tps := m.TokensPerSecond()

	var bldr strings.Builder
	bldr.WriteString(i18n.T(lang, "tokens.summary_header"))
	bldr.WriteString(i18n.Tf(lang, "tokens.summary_total",
		formatThousands(m.Usage.TotalTokens),
		formatCompact(m.Usage.InputTokens),
		formatCompact(m.Usage.OutputTokens),
	))
	if m.Usage.ThinkingTokens > 0 {
		bldr.WriteString(fmt.Sprintf(" | 💭 %s", formatCompact(m.Usage.ThinkingTokens)))
	}
	if m.Usage.CacheReadTokens > 0 {
		bldr.WriteString(fmt.Sprintf(" | 💾 %s", formatCompact(m.Usage.CacheReadTokens)))
	}
	bldr.WriteString(")\n")

	bldr.WriteString(i18n.Tf(lang, "tokens.summary_speed", tps))
	bldr.WriteString(i18n.Tf(lang, "tokens.summary_elapsed", durStr))
	if m.Model != "" {
		bldr.WriteString(i18n.Tf(lang, "tokens.summary_model", html.EscapeString(m.Model)))
	}
	if m.Usage.CacheReadTokens > 0 {
		bldr.WriteString(i18n.Tf(lang, "tokens.summary_cache",
			formatThousands(m.Usage.CacheReadTokens), m.Usage.CacheHitRate()))
	}
	if m.LastStepUsage.InputTokens > 0 || m.LastStepUsage.OutputTokens > 0 {
		window := ModelContextWindow(m.Model)
		actCtx := m.LastStepUsage.InputTokens + m.LastStepUsage.CacheReadTokens + m.LastStepUsage.OutputTokens
		if window > 0 && actCtx > 0 {
			pct := (float64(actCtx) / float64(window)) * 100.0
			bldr.WriteString(i18n.Tf(lang, "tokens.summary_window",
				formatCompact(actCtx), FormatContextLimit(window), pct))
		}
	}
	return bldr.String()
}

// TokenTracker управляет метриками токенов текущей задачи, последнего запуска и всей сессии бота.
type TokenTracker struct {
	sync.RWMutex
	currentTask       *TaskTokenMetrics
	stepUsages        map[int]UsageStats
	accumulatedSteps  UsageStats
	stepDurationTotal float64
	lastTask          *TaskTokenMetrics
	totalTasksRun     int
	sessionUsage      UsageStats
	totalDuration     float64

	// Метрики диалогового режима учитываются отдельно, чтобы не портить статистику задач.
	chatUsage     UsageStats
	chatTurns     int
	chatDuration  float64
	lastChatModel string
	lastChatAt    time.Time
}

var GlobalTokenTracker = NewTokenTracker()

func NewTokenTracker() *TokenTracker {
	return &TokenTracker{
		stepUsages: make(map[int]UsageStats),
	}
}

// StartTaskWithAgent инициализирует отслеживание новой задачи с указанием агента.
func (t *TokenTracker) StartTaskWithAgent(project, model, agent, prompt string) {
	t.Lock()
	defer t.Unlock()

	if agent == "" {
		agent = "agy"
	}
	t.currentTask = &TaskTokenMetrics{
		Project:   project,
		Model:     model,
		Agent:     agent,
		Prompt:    prompt,
		StartedAt: time.Now(),
		Usage:     UsageStats{},
	}
	t.stepUsages = make(map[int]UsageStats)
	t.accumulatedSteps = UsageStats{}
	t.stepDurationTotal = 0
}

// StartTask инициализирует отслеживание новой задачи (по умолчанию агент agy).
func (t *TokenTracker) StartTask(project, model, prompt string) {
	t.StartTaskWithAgent(project, model, "agy", prompt)
}

// StartTaskIfNotActiveWithAgent инициализирует отслеживание, если текущая задача не установлена.
func (t *TokenTracker) StartTaskIfNotActiveWithAgent(project, model, agent, prompt string) {
	t.Lock()
	defer t.Unlock()

	if t.currentTask != nil {
		return
	}

	if agent == "" {
		agent = "agy"
	}
	t.currentTask = &TaskTokenMetrics{
		Project:   project,
		Model:     model,
		Agent:     agent,
		Prompt:    prompt,
		StartedAt: time.Now(),
		Usage:     UsageStats{},
	}
	t.stepUsages = make(map[int]UsageStats)
	t.accumulatedSteps = UsageStats{}
	t.stepDurationTotal = 0
}

// StartTaskIfNotActive инициализирует отслеживание, если текущая задача не установлена.
// Полезно при возобновлении задачи, которая была приостановлена (и currentTask == nil).
func (t *TokenTracker) StartTaskIfNotActive(project, model, prompt string) {
	t.StartTaskIfNotActiveWithAgent(project, model, "agy", prompt)
}

// StartNextStep сохраняет накопленные метрики предыдущего шага в рамках одной задачи (followups).
func (t *TokenTracker) StartNextStep(model string) {
	t.Lock()
	defer t.Unlock()

	if t.currentTask == nil {
		return
	}
	t.currentTask.Model = model
	t.stepUsages = make(map[int]UsageStats)
}

// SetConversationID сохраняет ID сессии agy для текущей задачи.
func (t *TokenTracker) SetConversationID(convID string) {
	t.Lock()
	defer t.Unlock()

	if t.currentTask != nil && convID != "" {
		t.currentTask.ConversationID = convID
	}
}

// RecordToolCall увеличивает счётчик вызовов инструментов текущей задачи.
func (t *TokenTracker) RecordToolCall() {
	t.Lock()
	defer t.Unlock()

	if t.currentTask != nil {
		t.currentTask.ToolCallsCount++
	}
}

// RecordStepUsage сохраняет метрики конкретного шага агента.
func (t *TokenTracker) RecordStepUsage(stepIndex int, usage UsageStats) {
	t.Lock()
	defer t.Unlock()

	if t.currentTask == nil {
		return
	}
	t.stepUsages[stepIndex] = usage
	t.currentTask.LastStepUsage = usage
	t.recalculateCurrentUsage()
}

// RecordResultUsage фиксирует итоговые метрики шага из agy result.
func (t *TokenTracker) RecordResultUsage(usage UsageStats, durationSeconds float64, numTurns int) {
	t.Lock()
	defer t.Unlock()

	if t.currentTask == nil {
		return
	}

	// Добавляем зафиксированные итоги текущего шага к задаче
	t.accumulatedSteps.Add(usage)
	t.stepDurationTotal += durationSeconds
	t.currentTask.Turns += numTurns
	t.stepUsages = make(map[int]UsageStats)

	t.currentTask.Usage = t.accumulatedSteps
	t.currentTask.DurationSeconds = t.stepDurationTotal
}

// recalculateCurrentUsage обновляет суммарную нагрузку с учётом промежуточных шагов.
func (t *TokenTracker) recalculateCurrentUsage() {
	total := t.accumulatedSteps
	for _, u := range t.stepUsages {
		total.Add(u)
	}
	t.currentTask.Usage = total
	if t.stepDurationTotal > 0 {
		t.currentTask.DurationSeconds = t.stepDurationTotal
	}
}

// FinishTask завершает задачу и сохраняет метрики в историю.
func (t *TokenTracker) FinishTask(prURL string) TaskTokenMetrics {
	t.Lock()
	defer t.Unlock()

	if t.currentTask == nil {
		if t.lastTask != nil {
			return *t.lastTask
		}
		return TaskTokenMetrics{}
	}

	t.currentTask.FinishedAt = time.Now()
	t.currentTask.PRURL = prURL
	if t.currentTask.DurationSeconds <= 0 {
		t.currentTask.DurationSeconds = t.currentTask.FinishedAt.Sub(t.currentTask.StartedAt).Seconds()
	}

	completed := *t.currentTask
	t.lastTask = &completed
	t.currentTask = nil
	t.stepUsages = make(map[int]UsageStats)

	t.totalTasksRun++
	t.sessionUsage.Add(completed.Usage)
	t.totalDuration += completed.EffectiveDuration()

	return completed
}

// CancelTask прерывает текущую задачу с сохранением метрик в сессию.
func (t *TokenTracker) CancelTask() {
	t.Lock()
	defer t.Unlock()

	if t.currentTask == nil {
		return
	}

	t.currentTask.FinishedAt = time.Now()
	if t.currentTask.DurationSeconds <= 0 {
		t.currentTask.DurationSeconds = t.currentTask.FinishedAt.Sub(t.currentTask.StartedAt).Seconds()
	}

	completed := *t.currentTask
	t.lastTask = &completed
	t.currentTask = nil
	t.stepUsages = make(map[int]UsageStats)

	t.totalTasksRun++
	t.sessionUsage.Add(completed.Usage)
	t.totalDuration += completed.EffectiveDuration()
}

// GetLiveStatusSnippet возвращает компактную строчку для живого обновления статуса.
func (t *TokenTracker) GetLiveStatusSnippet(lang string) string {
	t.RLock()
	defer t.RUnlock()

	if t.currentTask == nil {
		return ""
	}

	total := t.currentTask.Usage.TotalTokens
	out := t.currentTask.Usage.OutputTokens
	dur := t.currentTask.EffectiveDuration()
	cache := t.currentTask.Usage.CacheReadTokens

	if total == 0 && dur < 1.0 {
		return ""
	}

	var tps float64
	if dur > 0 && out > 0 {
		tps = float64(out) / dur
	}

	cacheStr := "0"
	if cache > 0 {
		cacheStr = formatCompact(cache)
	}

	return i18n.Tf(lang, "tokens.live_snippet", formatCompact(total), tps, cacheStr)
}

// GetCurrentTaskStatusBlock возвращает блок статистики токенов для команды /status.
func (t *TokenTracker) GetCurrentTaskStatusBlock(lang string) string {
	t.RLock()
	defer t.RUnlock()

	if t.currentTask == nil {
		return ""
	}

	u := t.currentTask.Usage
	dur := t.currentTask.EffectiveDuration()
	var tps float64
	if dur > 0 && u.OutputTokens > 0 {
		tps = float64(u.OutputTokens) / dur
	}

	var bldr strings.Builder
	bldr.WriteString(i18n.T(lang, "tokens.task_block_header"))
	bldr.WriteString(i18n.Tf(lang, "tokens.task_block_used",
		formatThousands(u.TotalTokens),
		formatCompact(u.InputTokens),
		formatCompact(u.OutputTokens),
	))
	if u.ThinkingTokens > 0 {
		bldr.WriteString(fmt.Sprintf(" | 💭 %s", formatCompact(u.ThinkingTokens)))
	}
	bldr.WriteString(")\n")

	if u.CacheReadTokens > 0 {
		bldr.WriteString(i18n.Tf(lang, "tokens.task_block_cache",
			formatThousands(u.CacheReadTokens), u.CacheHitRate()))
	} else {
		bldr.WriteString(i18n.T(lang, "tokens.task_block_cache_empty"))
	}

	bldr.WriteString(i18n.Tf(lang, "tokens.task_block_speed", tps))

	return bldr.String()
}

// GetLastTaskStatusBlock возвращает компактный блок о последней завершенной задаче.
func (t *TokenTracker) GetLastTaskStatusBlock(lang string) string {
	t.RLock()
	defer t.RUnlock()

	if t.lastTask == nil {
		return ""
	}

	u := t.lastTask.Usage
	dur := t.lastTask.EffectiveDuration()
	durStr := FormatDurationHuman(time.Duration(dur * float64(time.Second)))
	tps := t.lastTask.TokensPerSecond()

	return i18n.Tf(lang, "tokens.last_task_block",
		html.EscapeString(t.lastTask.Project),
		formatThousands(u.TotalTokens),
		formatCompact(u.InputTokens),
		formatCompact(u.OutputTokens),
		formatCompact(u.CacheReadTokens),
		tps,
		durStr,
	)
}

// RecordChatUsage учитывает расход токенов одного хода диалога.
// Ходы диалога не трогают слот текущей задачи: иначе параллельно идущая задача
// показала бы чужие цифры в /tokens и /context.
func (t *TokenTracker) RecordChatUsage(model string, usage UsageStats, durationSeconds float64) {
	t.Lock()
	defer t.Unlock()

	t.chatUsage.InputTokens += usage.InputTokens
	t.chatUsage.OutputTokens += usage.OutputTokens
	t.chatUsage.ThinkingTokens += usage.ThinkingTokens
	t.chatUsage.CacheReadTokens += usage.CacheReadTokens
	t.chatUsage.TotalTokens += usage.TotalTokens
	t.chatTurns++
	if durationSeconds > 0 {
		t.chatDuration += durationSeconds
	}
	if model != "" {
		t.lastChatModel = model
	}
	t.lastChatAt = time.Now()
}

// ChatSummary возвращает блок статистики диалогового режима для /tokens (пусто, если диалогов не было).
func (t *TokenTracker) ChatSummary(lang string) string {
	t.RLock()
	defer t.RUnlock()
	return t.chatSummaryLocked(lang)
}

func (t *TokenTracker) chatSummaryLocked(lang string) string {
	if t.chatTurns == 0 {
		return ""
	}

	var bldr strings.Builder
	bldr.WriteString(i18n.T(lang, "tokens.chat_header"))
	bldr.WriteString(i18n.Tf(lang, "tokens.chat_turns", t.chatTurns))
	if t.lastChatModel != "" {
		bldr.WriteString(i18n.Tf(lang, "tokens.chat_model", html.EscapeString(t.lastChatModel)))
	}
	if t.chatUsage.TotalTokens > 0 {
		bldr.WriteString(i18n.Tf(lang, "tokens.chat_tokens",
			formatThousands(t.chatUsage.TotalTokens),
			formatCompact(t.chatUsage.InputTokens),
			formatCompact(t.chatUsage.OutputTokens)))
	}
	if t.chatDuration > 0 {
		bldr.WriteString(i18n.Tf(lang, "tokens.chat_duration",
			FormatDurationHuman(time.Duration(t.chatDuration*float64(time.Second)))))
	}
	return bldr.String()
}

// GetTokensCommandMessage формирует полное сообщение для команды /tokens.
func (t *TokenTracker) GetTokensCommandMessage(lang string) string {
	t.RLock()
	defer t.RUnlock()

	var bldr strings.Builder

	// 1. Активная задача, если есть
	if t.currentTask != nil {
		cur := *t.currentTask
		dur := cur.EffectiveDuration()
		durStr := FormatDurationHuman(time.Duration(dur * float64(time.Second)))
		tps := cur.TokensPerSecond()
		totalTps := cur.TotalTokensPerSecond()

		bldr.WriteString(i18n.T(lang, "tokens.active_header"))
		bldr.WriteString(i18n.Tf(lang, "tokens.active_project", html.EscapeString(cur.Project)))
		bldr.WriteString(i18n.Tf(lang, "tokens.active_model", html.EscapeString(cur.Model)))
		bldr.WriteString(i18n.Tf(lang, "tokens.active_elapsed", durStr))

		bldr.WriteString(i18n.T(lang, "tokens.spend_header"))
		bldr.WriteString(i18n.Tf(lang, "tokens.spend_total", formatThousands(cur.Usage.TotalTokens)))
		bldr.WriteString(i18n.Tf(lang, "tokens.spend_input", formatThousands(cur.Usage.InputTokens)))
		bldr.WriteString(i18n.Tf(lang, "tokens.spend_output", formatThousands(cur.Usage.OutputTokens)))
		if cur.Usage.ThinkingTokens > 0 {
			bldr.WriteString(i18n.Tf(lang, "tokens.spend_thinking", formatThousands(cur.Usage.ThinkingTokens)))
		}
		bldr.WriteString(i18n.Tf(lang, "tokens.spend_cache_hits",
			formatThousands(cur.Usage.CacheReadTokens), cur.Usage.CacheHitRate()))

		bldr.WriteString(i18n.T(lang, "tokens.performance_header"))
		bldr.WriteString(i18n.Tf(lang, "tokens.performance_speed", tps))
		if totalTps > 0 {
			bldr.WriteString(i18n.Tf(lang, "tokens.performance_total_speed", totalTps))
		}
		if cur.Turns > 0 {
			bldr.WriteString(i18n.Tf(lang, "tokens.performance_turns", cur.Turns))
		}
		return bldr.String()
	}

	// 2. Если задачи нет, показываем последнюю задачу и общую статистику сессии
	if t.lastTask != nil {
		last := *t.lastTask
		dur := last.EffectiveDuration()
		durStr := FormatDurationHuman(time.Duration(dur * float64(time.Second)))
		tps := last.TokensPerSecond()

		bldr.WriteString(i18n.T(lang, "tokens.no_active"))
		bldr.WriteString(i18n.T(lang, "tokens.last_header"))
		bldr.WriteString(i18n.Tf(lang, "tokens.last_project", html.EscapeString(last.Project)))
		bldr.WriteString(i18n.Tf(lang, "tokens.last_model", html.EscapeString(last.Model)))
		bldr.WriteString(i18n.Tf(lang, "tokens.last_elapsed", durStr))
		bldr.WriteString(i18n.Tf(lang, "tokens.last_total", formatThousands(last.Usage.TotalTokens)))
		bldr.WriteString(i18n.Tf(lang, "tokens.spend_input", formatThousands(last.Usage.InputTokens)))
		bldr.WriteString(i18n.Tf(lang, "tokens.spend_output", formatThousands(last.Usage.OutputTokens)))
		if last.Usage.ThinkingTokens > 0 {
			bldr.WriteString(i18n.Tf(lang, "tokens.spend_thinking", formatThousands(last.Usage.ThinkingTokens)))
		}
		bldr.WriteString(i18n.Tf(lang, "tokens.last_cache",
			formatThousands(last.Usage.CacheReadTokens), last.Usage.CacheHitRate()))
		bldr.WriteString(i18n.Tf(lang, "tokens.performance_speed", tps))
		if last.PRURL != "" {
			bldr.WriteString(i18n.Tf(lang, "tokens.last_pr", html.EscapeString(last.PRURL)))
		}
		bldr.WriteString("\n")
	}

	if t.totalTasksRun > 0 {
		var avgSpeed float64
		if t.totalDuration > 0 && t.sessionUsage.OutputTokens > 0 {
			avgSpeed = float64(t.sessionUsage.OutputTokens) / t.totalDuration
		}
		totalDurStr := FormatDurationHuman(time.Duration(t.totalDuration * float64(time.Second)))

		bldr.WriteString(i18n.T(lang, "tokens.session_header"))
		bldr.WriteString(i18n.Tf(lang, "tokens.session_tasks", t.totalTasksRun))
		bldr.WriteString(i18n.Tf(lang, "tokens.session_total", formatThousands(t.sessionUsage.TotalTokens)))
		bldr.WriteString(i18n.Tf(lang, "tokens.session_prompt", formatCompact(t.sessionUsage.InputTokens)))
		bldr.WriteString(i18n.Tf(lang, "tokens.session_answers", formatCompact(t.sessionUsage.OutputTokens)))
		if t.sessionUsage.ThinkingTokens > 0 {
			bldr.WriteString(i18n.Tf(lang, "tokens.session_thinking", formatCompact(t.sessionUsage.ThinkingTokens)))
		}
		bldr.WriteString("\n")
		bldr.WriteString(i18n.Tf(lang, "tokens.session_cache", formatCompact(t.sessionUsage.CacheReadTokens)))
		bldr.WriteString(i18n.Tf(lang, "tokens.session_duration", totalDurStr))
		bldr.WriteString(i18n.Tf(lang, "tokens.session_avg_speed", avgSpeed))
	} else if GlobalTaskManager != nil && GlobalTaskManager.Storage() != nil {
		if agg, err := GlobalTaskManager.Storage().GetAggregateMetrics(context.Background()); err == nil && agg != nil && agg.TotalTasks > 0 {
			var avgSpeed float64
			if agg.TotalDuration > 0 && agg.OutputTokens > 0 {
				avgSpeed = float64(agg.OutputTokens) / agg.TotalDuration
			}
			totalDurStr := FormatDurationHuman(time.Duration(agg.TotalDuration * float64(time.Second)))

			bldr.WriteString(i18n.T(lang, "tokens.db_header"))
			bldr.WriteString(i18n.Tf(lang, "tokens.db_tasks", agg.TotalTasks))
			bldr.WriteString(i18n.Tf(lang, "tokens.session_total", formatThousands(agg.TotalTokens)))
			bldr.WriteString(i18n.Tf(lang, "tokens.session_prompt", formatCompact(agg.InputTokens)))
			bldr.WriteString(i18n.Tf(lang, "tokens.session_answers", formatCompact(agg.OutputTokens)))
			if agg.ThinkingTokens > 0 {
				bldr.WriteString(i18n.Tf(lang, "tokens.session_thinking", formatCompact(agg.ThinkingTokens)))
			}
			bldr.WriteString("\n")
			bldr.WriteString(i18n.Tf(lang, "tokens.session_cache", formatCompact(agg.CacheReadTokens)))
			bldr.WriteString(i18n.Tf(lang, "tokens.session_duration", totalDurStr))
			bldr.WriteString(i18n.Tf(lang, "tokens.session_avg_speed", avgSpeed))
		} else {
			bldr.WriteString(i18n.T(lang, "tokens.empty"))
		}
	} else {
		bldr.WriteString(i18n.T(lang, "tokens.empty"))
	}

	if chatBlock := t.chatSummaryLocked(lang); chatBlock != "" {
		bldr.WriteString("\n\n")
		bldr.WriteString(chatBlock)
	}

	bldr.WriteString(i18n.T(lang, "tokens.footer"))
	return bldr.String()
}

// FormatShortLastTask возвращает компактную строчку для команды /usage.
func (t *TokenTracker) FormatShortLastTask(lang string) string {
	t.RLock()
	defer t.RUnlock()

	if t.lastTask == nil {
		return ""
	}

	u := t.lastTask.Usage
	tps := t.lastTask.TokensPerSecond()
	return i18n.Tf(lang, "tokens.short_last_task",
		formatThousands(u.TotalTokens),
		formatCompact(u.InputTokens),
		formatCompact(u.OutputTokens),
		tps,
	)
}

// ModelContextWindow возвращает размер окна контекста модели в токенах.
func ModelContextWindow(model string) int64 {
	m := strings.ToLower(model)
	if aliased, ok := models.BaseAliases[m]; ok {
		m = aliased
	}
	switch {
	case strings.Contains(m, "claude"):
		return 200_000
	case strings.Contains(m, "gpt-oss") || strings.Contains(m, "120b"):
		return 131_072
	case strings.Contains(m, "gemini"):
		return 1_048_576
	default:
		return 1_048_576
	}
}

// FormatContextLimit возвращает читаемое строковое представление лимита окна.
func FormatContextLimit(limit int64) string {
	if limit >= 1_000_000 {
		val := float64(limit) / 1_000_000.0
		return fmt.Sprintf("%.1fM", val)
	}
	if limit >= 1_000 {
		return fmt.Sprintf("%dk", limit/1_000)
	}
	return strconv.FormatInt(limit, 10)
}

// renderContextBar формирует текстовый индикатор заполненности (прогресс-бар).
func renderContextBar(percent float64, length int) string {
	if length <= 0 {
		length = 20
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := int(math.Round((percent / 100.0) * float64(length)))
	if filled > length {
		filled = length
	}
	empty := length - filled
	return strings.Repeat("■", filled) + strings.Repeat("□", empty)
}

// resolveStepUsageForContext вычисляет метрики заполнения контекстного окна модели,
// отдавая приоритет метрикам последнего шага сессии agy.
func resolveStepUsageForContext(metrics TaskTokenMetrics, convID string, windowLimit int64) (
	promptTokens, cacheTokens, outputTokens, thinkingTokens, totalPromptTokens, activeContextTokens int64,
	usedPercent float64,
) {
	var hasStepMetrics bool

	// 1. Проверяем, сохранены ли метрики последнего шага (LastStepUsage)
	if metrics.LastStepUsage.InputTokens > 0 || metrics.LastStepUsage.OutputTokens > 0 {
		promptTokens = metrics.LastStepUsage.InputTokens
		cacheTokens = metrics.LastStepUsage.CacheReadTokens
		outputTokens = metrics.LastStepUsage.OutputTokens
		thinkingTokens = metrics.LastStepUsage.ThinkingTokens
		hasStepMetrics = true
	} else if convID != "" {
		// 2. Если в объекте метрик LastStepUsage пуст, пробуем прочитать последний шаг из локальной БД agy
		inp, out, think, cache, _, ok := storage.ExtractConversationLastStepUsage(convID)
		if ok && (inp > 0 || out > 0) {
			promptTokens = inp
			cacheTokens = cache
			outputTokens = out
			thinkingTokens = think
			hasStepMetrics = true
		}
	}

	// 3. Безопасный Fallback при отсутствии пошаговой телеметрии
	if !hasStepMetrics {
		if metrics.Turns > 1 {
			// Кумулятивные метрики делим на количество итераций (turns) для адекватной оценки шага
			promptTokens = metrics.Usage.InputTokens / int64(metrics.Turns)
			cacheTokens = metrics.Usage.CacheReadTokens / int64(metrics.Turns)
			outputTokens = metrics.Usage.OutputTokens / int64(metrics.Turns)
			thinkingTokens = metrics.Usage.ThinkingTokens / int64(metrics.Turns)
		} else {
			promptTokens = metrics.Usage.InputTokens
			outputTokens = metrics.Usage.OutputTokens
			thinkingTokens = metrics.Usage.ThinkingTokens
			if metrics.Usage.InputTokens+metrics.Usage.CacheReadTokens <= windowLimit {
				cacheTokens = metrics.Usage.CacheReadTokens
			}
		}
	}

	totalPromptTokens = promptTokens + cacheTokens
	activeContextTokens = totalPromptTokens + outputTokens
	if activeContextTokens <= 0 {
		activeContextTokens = metrics.Usage.TotalTokens
	}

	if windowLimit > 0 {
		usedPercent = (float64(activeContextTokens) / float64(windowLimit)) * 100.0
	}

	// Защита от выхода за 100% при оценке на основе суммарных кумулятивных метрик
	if usedPercent > 100.0 && !hasStepMetrics {
		usedPercent = 100.0
		activeContextTokens = windowLimit
		totalPromptTokens = windowLimit - outputTokens
		if totalPromptTokens < 0 {
			totalPromptTokens = 0
		}
	}

	return
}

// GetContextCommandMessage формирует подробный отчёт об использовании контекстного окна agy.
func (t *TokenTracker) GetContextCommandMessage(task *TaskSession, defaultProject, defaultModel, lang string) string {
	var (
		taskID       int
		taskProj     string
		taskMod      string
		taskStatus   TaskStatus
		taskConvID   string
		taskMetrics  *TaskTokenMetrics
		taskIsActive bool
	)
	if task != nil {
		task.mu.Lock()
		taskID = task.ID
		taskProj = task.Project
		taskMod = task.Model
		if taskMod == "" {
			taskMod = task.LastModelUsed
		}
		if taskMod == "" {
			taskMod = defaultModel
		}
		taskStatus = task.Status
		taskConvID = task.ConversationID
		taskMetrics = task.TokenMetrics
		taskIsActive = task.isActiveLocked()
		task.mu.Unlock()
	}

	t.RLock()
	defer t.RUnlock()

	var bldr strings.Builder

	// 1. Если передана конкретная задача или есть активная задача
	if task != nil {
		var metrics TaskTokenMetrics
		var hasMetrics bool

		if taskIsActive && t.currentTask != nil {
			metrics = *t.currentTask
			hasMetrics = true
		} else if taskMetrics != nil {
			metrics = *taskMetrics
			hasMetrics = true
		} else if t.lastTask != nil && t.lastTask.Project == taskProj {
			metrics = *t.lastTask
			hasMetrics = true
		}

		if taskConvID == "" && metrics.ConversationID != "" {
			taskConvID = metrics.ConversationID
		}
		if metrics.Model != "" {
			taskMod = metrics.Model
		}

		windowLimit := ModelContextWindow(taskMod)
		windowLimitStr := FormatContextLimit(windowLimit)

		if !hasMetrics || (metrics.Usage.TotalTokens == 0 && metrics.LastStepUsage.TotalTokens == 0) {
			bldr.WriteString(i18n.Tf(lang, "context.task_header", taskID, i18n.TaskStatusTitle(string(taskStatus), lang)))
			bldr.WriteString(i18n.Tf(lang, "context.project", html.EscapeString(taskProj)))
			bldr.WriteString(i18n.Tf(lang, "context.model", html.EscapeString(taskMod)))
			bldr.WriteString(i18n.Tf(lang, "context.window_limit", windowLimitStr, formatThousands(windowLimit)))
			bldr.WriteString(fmt.Sprintf("<code>[%s]</code> <b>0.0%%</b>\n\n", renderContextBar(0, 20)))
			bldr.WriteString(i18n.T(lang, "context.no_metrics"))
			bldr.WriteString(i18n.T(lang, "context.window_hint"))
			return bldr.String()
		}

		_, cacheTokens, outputTokens, thinkingTokens, totalPromptTokens, activeContextTokens, usedPercent :=
			resolveStepUsageForContext(metrics, taskConvID, windowLimit)

		freeTokens := windowLimit - activeContextTokens
		if freeTokens < 0 {
			freeTokens = 0
		}
		freePercent := 100.0 - usedPercent
		if freePercent < 0 {
			freePercent = 0
		}

		if taskIsActive {
			bldr.WriteString(i18n.Tf(lang, "context.active_header", taskID))
		} else {
			bldr.WriteString(i18n.Tf(lang, "context.task_status_header", taskID, i18n.TaskStatusTitle(string(taskStatus), lang)))
		}

		bldr.WriteString(i18n.Tf(lang, "context.project", html.EscapeString(taskProj)))
		bldr.WriteString(i18n.Tf(lang, "context.model", html.EscapeString(taskMod)))
		bldr.WriteString(i18n.Tf(lang, "context.window_usage",
			formatCompact(activeContextTokens), windowLimitStr, usedPercent))
		bldr.WriteString(fmt.Sprintf("<code>[%s]</code> <b>%.1f%%</b>\n\n", renderContextBar(usedPercent, 20), usedPercent))

		bldr.WriteString(i18n.T(lang, "context.breakdown_header"))
		promptPercent := (float64(totalPromptTokens) / float64(windowLimit)) * 100.0
		bldr.WriteString(i18n.Tf(lang, "context.breakdown_input",
			formatThousands(totalPromptTokens), promptPercent))
		if cacheTokens > 0 {
			hitRate := 0.0
			if totalPromptTokens > 0 {
				hitRate = (float64(cacheTokens) / float64(totalPromptTokens)) * 100.0
			}
			bldr.WriteString(i18n.Tf(lang, "context.breakdown_cache",
				formatThousands(cacheTokens), hitRate))
			bldr.WriteString(i18n.T(lang, "context.breakdown_system"))
		} else {
			bldr.WriteString(i18n.T(lang, "context.breakdown_system"))
		}

		respPercent := (float64(outputTokens) / float64(windowLimit)) * 100.0
		bldr.WriteString(i18n.Tf(lang, "context.breakdown_output",
			formatThousands(outputTokens), respPercent))
		if thinkingTokens > 0 {
			bldr.WriteString(i18n.Tf(lang, "context.breakdown_thinking",
				formatThousands(thinkingTokens)))
		}

		if metrics.ToolCallsCount > 0 {
			bldr.WriteString(i18n.Tf(lang, "context.breakdown_tools", metrics.ToolCallsCount))
		}
		if metrics.Turns > 0 {
			bldr.WriteString(i18n.Tf(lang, "context.breakdown_turns", metrics.Turns))
		}
		bldr.WriteString(i18n.Tf(lang, "context.breakdown_free",
			formatThousands(freeTokens), freePercent))

		dur := metrics.EffectiveDuration()
		durStr := FormatDurationHuman(time.Duration(dur * float64(time.Second)))
		tps := metrics.TokensPerSecond()

		bldr.WriteString(i18n.T(lang, "context.session_header"))
		bldr.WriteString(i18n.Tf(lang, "context.session_cumulative", formatThousands(metrics.Usage.TotalTokens)))
		bldr.WriteString(i18n.Tf(lang, "context.session_speed", tps))
		bldr.WriteString(i18n.Tf(lang, "context.session_elapsed", durStr))
		if metrics.PRURL != "" {
			bldr.WriteString(i18n.Tf(lang, "context.session_pr", html.EscapeString(metrics.PRURL)))
		}
		if taskConvID != "" {
			agentName := metrics.Agent
			if agentName == "" && task != nil {
				task.mu.Lock()
				agentName = task.Agent
				task.mu.Unlock()
			}
			if agentName == "" {
				agentName = "agy"
			}
			bldr.WriteString(i18n.Tf(lang, "context.session_id", html.EscapeString(agentName), html.EscapeString(taskConvID)))
		}

		bldr.WriteString(i18n.T(lang, "context.agent_hint"))
		return bldr.String()
	}

	// 2. Если задача не указана и нет активной, но есть последняя завершённая
	if t.lastTask != nil {
		last := *t.lastTask
		mod := last.Model
		if mod == "" {
			mod = defaultModel
		}
		windowLimit := ModelContextWindow(mod)
		windowLimitStr := FormatContextLimit(windowLimit)

		_, cacheTokens, outputTokens, thinkingTokens, totalPromptTokens, activeContextTokens, usedPercent :=
			resolveStepUsageForContext(last, last.ConversationID, windowLimit)

		freeTokens := windowLimit - activeContextTokens
		if freeTokens < 0 {
			freeTokens = 0
		}
		freePercent := 100.0 - usedPercent
		if freePercent < 0 {
			freePercent = 0
		}

		bldr.WriteString(i18n.T(lang, "tokens.no_active"))
		bldr.WriteString(i18n.Tf(lang, "context.last_task_header", html.EscapeString(last.Project)))
		bldr.WriteString(i18n.Tf(lang, "context.model", html.EscapeString(mod)))
		bldr.WriteString(i18n.Tf(lang, "context.window_usage",
			formatCompact(activeContextTokens), windowLimitStr, usedPercent))
		bldr.WriteString(fmt.Sprintf("<code>[%s]</code> <b>%.1f%%</b>\n\n", renderContextBar(usedPercent, 20), usedPercent))

		bldr.WriteString(i18n.T(lang, "context.breakdown_header"))
		promptPercent := (float64(totalPromptTokens) / float64(windowLimit)) * 100.0
		bldr.WriteString(i18n.Tf(lang, "context.breakdown_input",
			formatThousands(totalPromptTokens), promptPercent))
		if cacheTokens > 0 {
			hitRate := 0.0
			if totalPromptTokens > 0 {
				hitRate = (float64(cacheTokens) / float64(totalPromptTokens)) * 100.0
			}
			bldr.WriteString(i18n.Tf(lang, "context.breakdown_cache",
				formatThousands(cacheTokens), hitRate))
			bldr.WriteString(i18n.T(lang, "context.breakdown_system"))
		} else {
			bldr.WriteString(i18n.T(lang, "context.breakdown_system"))
		}

		respPercent := (float64(outputTokens) / float64(windowLimit)) * 100.0
		bldr.WriteString(i18n.Tf(lang, "context.breakdown_output",
			formatThousands(outputTokens), respPercent))
		if thinkingTokens > 0 {
			bldr.WriteString(i18n.Tf(lang, "context.breakdown_thinking",
				formatThousands(thinkingTokens)))
		}
		if last.ToolCallsCount > 0 {
			bldr.WriteString(i18n.Tf(lang, "context.breakdown_tools", last.ToolCallsCount))
		}
		if last.Turns > 0 {
			bldr.WriteString(i18n.Tf(lang, "context.breakdown_turns", last.Turns))
		}
		bldr.WriteString(i18n.Tf(lang, "context.breakdown_free_short",
			formatThousands(freeTokens), freePercent))

		dur := last.EffectiveDuration()
		durStr := FormatDurationHuman(time.Duration(dur * float64(time.Second)))
		tps := last.TokensPerSecond()

		bldr.WriteString(i18n.T(lang, "context.totals_header"))
		bldr.WriteString(i18n.Tf(lang, "context.totals_tokens", formatThousands(last.Usage.TotalTokens)))
		bldr.WriteString(i18n.Tf(lang, "context.session_speed", tps))
		bldr.WriteString(i18n.Tf(lang, "context.totals_elapsed", durStr))
		if last.PRURL != "" {
			bldr.WriteString(i18n.Tf(lang, "context.session_pr", html.EscapeString(last.PRURL)))
		}
		if last.ConversationID != "" {
			agentName := last.Agent
			if agentName == "" {
				agentName = "agy"
			}
			bldr.WriteString(i18n.Tf(lang, "context.session_id", html.EscapeString(agentName), html.EscapeString(last.ConversationID)))
		}

		bldr.WriteString(i18n.T(lang, "context.task_hint"))
		return bldr.String()
	}

	// 3. Если задачи ещё не запускались в этой сессии бота
	mod := defaultModel
	if mod == "" {
		mod = "gemini-3.1-pro-high"
	}
	windowLimit := ModelContextWindow(mod)
	windowLimitStr := FormatContextLimit(windowLimit)

	bldr.WriteString(i18n.T(lang, "context.idle_header"))
	if defaultProject != "" {
		bldr.WriteString(i18n.Tf(lang, "context.idle_project", html.EscapeString(defaultProject)))
	}
	bldr.WriteString(i18n.Tf(lang, "context.idle_model", html.EscapeString(mod)))
	bldr.WriteString(i18n.Tf(lang, "context.idle_window",
		windowLimitStr, formatThousands(windowLimit)))
	bldr.WriteString(i18n.T(lang, "context.idle_used"))
	bldr.WriteString(i18n.Tf(lang, "context.idle_free",
		formatThousands(windowLimit)))
	bldr.WriteString(fmt.Sprintf("<code>[%s]</code> <b>0.0%%</b>\n\n", renderContextBar(0, 20)))

	bldr.WriteString(i18n.T(lang, "context.idle_loaded_header"))
	bldr.WriteString(i18n.T(lang, "context.idle_loaded_system"))
	bldr.WriteString(i18n.T(lang, "context.idle_loaded_skills"))
	bldr.WriteString(i18n.T(lang, "context.idle_loaded_tools"))
	bldr.WriteString(i18n.T(lang, "context.idle_loaded_cache"))
	bldr.WriteString(i18n.T(lang, "context.idle_loaded_history"))

	bldr.WriteString(i18n.T(lang, "context.idle_hint"))
	return bldr.String()
}

// formatToolAction возвращает понятное описание действия инструмента.
func FormatToolAction(name string, info *StreamToolInfo) string {
	if info == nil || info.Parameters == nil {
		return fmt.Sprintf("🔧 %s", name)
	}
	switch strings.ToLower(name) {
	case "run_command":
		if cmd, ok := info.Parameters["CommandLine"].(string); ok && cmd != "" {
			return fmt.Sprintf("⚡ %s", utils.TruncateString(cmd, 70))
		} else if cmd, ok := info.Parameters["command"].(string); ok && cmd != "" {
			return fmt.Sprintf("⚡ %s", utils.TruncateString(cmd, 70))
		}
	case "bash":
		if cmd, ok := info.Parameters["command"].(string); ok && cmd != "" {
			return fmt.Sprintf("⚡ %s", utils.TruncateString(cmd, 70))
		} else if cmd, ok := info.Parameters["CommandLine"].(string); ok && cmd != "" {
			return fmt.Sprintf("⚡ %s", utils.TruncateString(cmd, 70))
		}
	case "replace_file_content", "edit_file":
		if target, ok := info.Parameters["TargetFile"].(string); ok && target != "" {
			return fmt.Sprintf("✏️ edit: %s", filepath.Base(target))
		} else if target, ok := info.Parameters["file_path"].(string); ok && target != "" {
			return fmt.Sprintf("✏️ edit: %s", filepath.Base(target))
		} else if target, ok := info.Parameters["path"].(string); ok && target != "" {
			return fmt.Sprintf("✏️ edit: %s", filepath.Base(target))
		}
	case "edit":
		if target, ok := info.Parameters["file_path"].(string); ok && target != "" {
			return fmt.Sprintf("✏️ edit: %s", filepath.Base(target))
		} else if target, ok := info.Parameters["TargetFile"].(string); ok && target != "" {
			return fmt.Sprintf("✏️ edit: %s", filepath.Base(target))
		} else if target, ok := info.Parameters["path"].(string); ok && target != "" {
			return fmt.Sprintf("✏️ edit: %s", filepath.Base(target))
		}
	case "write_to_file", "write", "write_file":
		if target, ok := info.Parameters["TargetFile"].(string); ok && target != "" {
			return fmt.Sprintf("📝 write: %s", filepath.Base(target))
		} else if target, ok := info.Parameters["file_path"].(string); ok && target != "" {
			return fmt.Sprintf("📝 write: %s", filepath.Base(target))
		} else if target, ok := info.Parameters["path"].(string); ok && target != "" {
			return fmt.Sprintf("📝 write: %s", filepath.Base(target))
		}
	case "view_file", "read", "read_file":
		if path, ok := info.Parameters["AbsolutePath"].(string); ok && path != "" {
			return fmt.Sprintf("👁 view: %s", filepath.Base(path))
		} else if path, ok := info.Parameters["file_path"].(string); ok && path != "" {
			return fmt.Sprintf("👁 view: %s", filepath.Base(path))
		} else if path, ok := info.Parameters["path"].(string); ok && path != "" {
			return fmt.Sprintf("👁 view: %s", filepath.Base(path))
		}
	case "list_dir", "list_directory":
		if path, ok := info.Parameters["path"].(string); ok && path != "" {
			return fmt.Sprintf("📁 list: %s", utils.TruncateString(path, 50))
		}
		return "📁 list: ."
	case "grep_search":
		if q, ok := info.Parameters["Query"].(string); ok && q != "" {
			return fmt.Sprintf("🔍 grep: %s", utils.TruncateString(q, 50))
		}
	case "find_by_name":
		if pat, ok := info.Parameters["Pattern"].(string); ok && pat != "" {
			return fmt.Sprintf("📁 find: %s", utils.TruncateString(pat, 50))
		}
	case "websearch":
		if q, ok := info.Parameters["query"].(string); ok && q != "" {
			return fmt.Sprintf("🔍 search: %s", utils.TruncateString(q, 50))
		}
	case "webfetch":
		if u, ok := info.Parameters["url"].(string); ok && u != "" {
			return fmt.Sprintf("🌐 fetch: %s", utils.TruncateString(u, 50))
		}
	}
	return fmt.Sprintf("🔧 %s", name)
}

func formatThousands(n int64) string {
	if n < 0 {
		return "-" + formatThousands(-n)
	}
	in := strconv.FormatInt(n, 10)
	if len(in) <= 3 {
		return in
	}
	out := make([]byte, 0, len(in)+len(in)/3)
	leading := len(in) % 3
	if leading == 0 {
		leading = 3
	}
	out = append(out, in[:leading]...)
	for i := leading; i < len(in); i += 3 {
		out = append(out, ' ')
		out = append(out, in[i:i+3]...)
	}
	return string(out)
}

func formatCompact(n int64) string {
	if n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	if n < 1000000 {
		val := float64(n) / 1000.0
		if val < 10 {
			return fmt.Sprintf("%.1fk", val)
		}
		return fmt.Sprintf("%.0fk", val)
	}
	val := float64(n) / 1000000.0
	return fmt.Sprintf("%.1fM", val)
}

// FormatDurationHuman форматирует длительность на активном языке интерфейса.
// Это низкоуровневый помощник: он вызывается из глубины отрисовки статусов, куда
// язык параметром не дотянуть, поэтому берёт его из i18n.Active().
func FormatDurationHuman(d time.Duration) string {
	return FormatDuration(d, i18n.Active())
}

// FormatDuration форматирует длительность на заданном языке.
func FormatDuration(d time.Duration, lang string) string {
	d = d.Round(time.Second)
	totalSec := int64(d.Seconds())
	if totalSec < 60 {
		return i18n.Tf(lang, "duration.seconds", totalSec)
	}
	mins := totalSec / 60
	secs := totalSec % 60
	if mins < 60 {
		if secs > 0 {
			return i18n.Tf(lang, "duration.minutes_seconds", mins, secs)
		}
		return i18n.Tf(lang, "duration.minutes", mins)
	}
	hours := mins / 60
	remMins := mins % 60
	return i18n.Tf(lang, "duration.hours_minutes", hours, remMins)
}
