package domain

import (
	"bro-bot/internal/i18n"
	"bro-bot/internal/storage"
	"bro-bot/internal/tools"
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
	ToolCallID      string          `json:"tool_call_id,omitempty"`
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

type taskTokenState struct {
	metrics           *TaskTokenMetrics
	stepUsages        map[int]UsageStats
	unnamedStepSeq    int
	liveUsage         *UsageStats
	dbStepDelta       *UsageStats
	accumulatedSteps  UsageStats
	stepDurationTotal float64
}

// TokenTracker управляет метриками токенов активных задач, последнего запуска и всей сессии бота.
type TokenTracker struct {
	sync.RWMutex
	tasks         map[int]*taskTokenState
	activeTaskID  int
	hasActiveTask bool
	lastTask      *TaskTokenMetrics
	totalTasksRun int
	sessionUsage  UsageStats
	totalDuration float64

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
		tasks: make(map[int]*taskTokenState),
	}
}

func (t *TokenTracker) ensureMapLocked() {
	if t.tasks == nil {
		t.tasks = make(map[int]*taskTokenState)
	}
}

func (t *TokenTracker) newTaskState(project, model, agent, prompt string) *taskTokenState {
	if agent == "" {
		agent = "agy"
	}
	return &taskTokenState{
		metrics: &TaskTokenMetrics{
			Project:   project,
			Model:     model,
			Agent:     agent,
			Prompt:    prompt,
			StartedAt: time.Now(),
			Usage:     UsageStats{},
		},
		stepUsages: make(map[int]UsageStats),
	}
}

func (t *TokenTracker) startTaskWithAgentLocked(taskID int, project, model, agent, prompt string) {
	t.ensureMapLocked()
	t.tasks[taskID] = t.newTaskState(project, model, agent, prompt)
	t.activeTaskID = taskID
	t.hasActiveTask = true
}

func (t *TokenTracker) stateForTaskLocked(taskID int, allowLegacyFallback bool) *taskTokenState {
	if t.tasks == nil {
		return nil
	}
	if st, ok := t.tasks[taskID]; ok && st != nil && st.metrics != nil {
		return st
	}
	if allowLegacyFallback {
		if st, ok := t.tasks[0]; ok && st != nil && st.metrics != nil {
			return st
		}
		if t.hasActiveTask {
			if st, ok := t.tasks[t.activeTaskID]; ok && st != nil && st.metrics != nil {
				return st
			}
		}
	}
	return nil
}

func (t *TokenTracker) activeStateLocked() *taskTokenState {
	if t.tasks == nil {
		return nil
	}
	if t.hasActiveTask {
		if st, ok := t.tasks[t.activeTaskID]; ok && st != nil && st.metrics != nil {
			return st
		}
	}
	for id, st := range t.tasks {
		if st != nil && st.metrics != nil {
			t.activeTaskID = id
			t.hasActiveTask = true
			return st
		}
	}
	return nil
}

func (t *TokenTracker) pickNextActiveLocked(removedID int) {
	if t.activeTaskID != removedID {
		return
	}
	bestID := 0
	found := false
	for id, st := range t.tasks {
		if st != nil && st.metrics != nil && (!found || id > bestID) {
			bestID = id
			found = true
		}
	}
	t.activeTaskID = bestID
	t.hasActiveTask = found
}

// StartTaskWithAgentForID инициализирует отслеживание конкретной задачи с указанием агента.
func (t *TokenTracker) StartTaskWithAgentForID(taskID int, project, model, agent, prompt string) {
	t.Lock()
	defer t.Unlock()
	t.startTaskWithAgentLocked(taskID, project, model, agent, prompt)
}

// StartTaskWithAgent инициализирует отслеживание новой задачи с указанием агента.
func (t *TokenTracker) StartTaskWithAgent(project, model, agent, prompt string) {
	t.StartTaskWithAgentForID(0, project, model, agent, prompt)
}

// StartTask инициализирует отслеживание новой задачи (по умолчанию агент agy).
func (t *TokenTracker) StartTask(project, model, prompt string) {
	t.StartTaskWithAgentForID(0, project, model, "agy", prompt)
}

// StartTaskIfNotActiveWithAgentForID инициализирует отслеживание задачи taskID, если она ещё не отслеживается.
func (t *TokenTracker) StartTaskIfNotActiveWithAgentForID(taskID int, project, model, agent, prompt string) {
	t.Lock()
	defer t.Unlock()

	t.ensureMapLocked()
	if st, ok := t.tasks[taskID]; ok && st != nil && st.metrics != nil {
		if model != "" && st.metrics.Model == "" {
			st.metrics.Model = model
		}
		if agent != "" && st.metrics.Agent == "" {
			st.metrics.Agent = agent
		}
		t.activeTaskID = taskID
		t.hasActiveTask = true
		return
	}
	t.startTaskWithAgentLocked(taskID, project, model, agent, prompt)
}

// StartTaskIfNotActiveWithAgent инициализирует отслеживание, если текущая задача не установлена.
func (t *TokenTracker) StartTaskIfNotActiveWithAgent(project, model, agent, prompt string) {
	t.Lock()
	defer t.Unlock()

	if t.hasActiveTask && t.tasks[t.activeTaskID] != nil {
		return
	}
	t.startTaskWithAgentLocked(0, project, model, agent, prompt)
}

// StartTaskIfNotActive инициализирует отслеживание, если текущая задача не установлена.
// Полезно при возобновлении задачи, которая была приостановлена (и слот ещё не активен).
func (t *TokenTracker) StartTaskIfNotActive(project, model, prompt string) {
	t.StartTaskIfNotActiveWithAgent(project, model, "agy", prompt)
}

// StartNextStepForID сохраняет накопленные метрики предыдущего шага в рамках задачи taskID.
func (t *TokenTracker) StartNextStepForID(taskID int, model string) {
	t.Lock()
	defer t.Unlock()

	st := t.stateForTaskLocked(taskID, false)
	if st == nil || st.metrics == nil {
		return
	}
	if model != "" {
		st.metrics.Model = model
	}
	st.stepUsages = make(map[int]UsageStats)
	st.unnamedStepSeq = 0
	st.liveUsage = nil
	st.dbStepDelta = nil
}

// StartNextStep сохраняет накопленные метрики предыдущего шага в рамках текущей задачи.
func (t *TokenTracker) StartNextStep(model string) {
	t.Lock()
	defer t.Unlock()

	st := t.activeStateLocked()
	if st == nil || st.metrics == nil {
		return
	}
	if model != "" {
		st.metrics.Model = model
	}
	st.stepUsages = make(map[int]UsageStats)
	st.unnamedStepSeq = 0
	st.liveUsage = nil
	st.dbStepDelta = nil
}

// SetConversationIDForID сохраняет ID сессии для задачи taskID.
func (t *TokenTracker) SetConversationIDForID(taskID int, convID string) {
	t.Lock()
	defer t.Unlock()

	if convID == "" {
		return
	}
	if st := t.stateForTaskLocked(taskID, false); st != nil && st.metrics != nil {
		st.metrics.ConversationID = convID
	}
}

// SetConversationID сохраняет ID сессии agy для текущей задачи.
func (t *TokenTracker) SetConversationID(convID string) {
	t.Lock()
	defer t.Unlock()

	if convID == "" {
		return
	}
	if st := t.activeStateLocked(); st != nil && st.metrics != nil {
		st.metrics.ConversationID = convID
	}
}

// RecordToolCallForID увеличивает счётчик вызовов инструментов задачи taskID.
func (t *TokenTracker) RecordToolCallForID(taskID int) {
	t.Lock()
	defer t.Unlock()

	if st := t.stateForTaskLocked(taskID, false); st != nil && st.metrics != nil {
		st.metrics.ToolCallsCount++
	}
}

// RecordToolCall увеличивает счётчик вызовов инструментов текущей задачи.
func (t *TokenTracker) RecordToolCall() {
	t.Lock()
	defer t.Unlock()

	if st := t.activeStateLocked(); st != nil && st.metrics != nil {
		st.metrics.ToolCallsCount++
	}
}

// RecordStepUsageForID сохраняет метрики конкретного шага задачи taskID.
// Для потоков без step_index (stepIndex <= 0) каждый вызов накапливается под уникальным индексом.
func (t *TokenTracker) RecordStepUsageForID(taskID int, stepIndex int, usage UsageStats) {
	t.Lock()
	defer t.Unlock()

	st := t.stateForTaskLocked(taskID, false)
	if st == nil || st.metrics == nil {
		return
	}
	t.recordStepUsageOnStateLocked(st, stepIndex, usage)
}

// RecordStepUsage сохраняет метрики конкретного шага текущей задачи.
func (t *TokenTracker) RecordStepUsage(stepIndex int, usage UsageStats) {
	t.Lock()
	defer t.Unlock()

	st := t.activeStateLocked()
	if st == nil || st.metrics == nil {
		return
	}
	t.recordStepUsageOnStateLocked(st, stepIndex, usage)
}

func (t *TokenTracker) recordStepUsageOnStateLocked(st *taskTokenState, stepIndex int, usage UsageStats) {
	if st.stepUsages == nil {
		st.stepUsages = make(map[int]UsageStats)
	}
	key := stepIndex
	if key <= 0 {
		st.unnamedStepSeq--
		key = st.unnamedStepSeq
	}
	st.stepUsages[key] = usage
	st.metrics.LastStepUsage = usage
	recalculateStateLocked(st)
}

// RecordLiveSnapshotUsageForID обновляет оперативный снимок токенов (ACP usage_update) без двойного счёта.
func (t *TokenTracker) RecordLiveSnapshotUsageForID(taskID int, usage UsageStats) {
	t.Lock()
	defer t.Unlock()

	st := t.stateForTaskLocked(taskID, false)
	if st == nil || st.metrics == nil {
		return
	}
	uCopy := usage
	st.liveUsage = &uCopy
	if hasDetailedUsage(usage) || !hasDetailedUsage(st.metrics.LastStepUsage) {
		st.metrics.LastStepUsage = usage
	}
	recalculateStateLocked(st)
}

// SyncFromAgyDB считывает актуальную статистику шага из локальной БД agy (без удержания блокировки трекера).
func (t *TokenTracker) SyncFromAgyDB(taskID int, convID string, baseTotal UsageStats) {
	if convID == "" {
		return
	}
	summary, ok := storage.ExtractConversationUsage(convID)
	if !ok || summary.Steps == 0 {
		return
	}

	delta := UsageStats{
		InputTokens:     clampNonNegativeInt64(summary.Total.InputTokens - baseTotal.InputTokens),
		OutputTokens:    clampNonNegativeInt64(summary.Total.OutputTokens - baseTotal.OutputTokens),
		ThinkingTokens:  clampNonNegativeInt64(summary.Total.ThinkingTokens - baseTotal.ThinkingTokens),
		CacheReadTokens: clampNonNegativeInt64(summary.Total.CacheReadTokens - baseTotal.CacheReadTokens),
		TotalTokens:     clampNonNegativeInt64(summary.Total.TotalTokens - baseTotal.TotalTokens),
	}
	if delta.TotalTokens == 0 && (delta.InputTokens > 0 || delta.OutputTokens > 0) {
		delta.TotalTokens = delta.InputTokens + delta.OutputTokens
	}
	lastStep := UsageStats{
		InputTokens:     summary.LastStep.InputTokens,
		OutputTokens:    summary.LastStep.OutputTokens,
		ThinkingTokens:  summary.LastStep.ThinkingTokens,
		CacheReadTokens: summary.LastStep.CacheReadTokens,
		TotalTokens:     summary.LastStep.TotalTokens,
	}

	t.Lock()
	defer t.Unlock()

	st := t.stateForTaskLocked(taskID, false)
	if st == nil || st.metrics == nil {
		return
	}
	if st.metrics.ConversationID == "" {
		st.metrics.ConversationID = convID
	}
	if delta.InputTokens > 0 || delta.OutputTokens > 0 || delta.ThinkingTokens > 0 || delta.CacheReadTokens > 0 || delta.TotalTokens > 0 {
		dCopy := delta
		st.dbStepDelta = &dCopy
	}
	if lastStep.InputTokens > 0 || lastStep.OutputTokens > 0 || lastStep.CacheReadTokens > 0 || lastStep.TotalTokens > 0 {
		st.metrics.LastStepUsage = lastStep
	}
	recalculateStateLocked(st)
}

// RecordResultUsageForID фиксирует итоговые метрики завершённого шага задачи taskID.
func (t *TokenTracker) RecordResultUsageForID(taskID int, usage UsageStats, durationSeconds float64, numTurns int) {
	t.Lock()
	defer t.Unlock()

	st := t.stateForTaskLocked(taskID, false)
	if st == nil || st.metrics == nil {
		return
	}
	t.recordResultUsageOnStateLocked(st, usage, durationSeconds, numTurns)
}

// RecordResultUsage фиксирует итоговые метрики шага из результата работы агента.
func (t *TokenTracker) RecordResultUsage(usage UsageStats, durationSeconds float64, numTurns int) {
	t.Lock()
	defer t.Unlock()

	st := t.activeStateLocked()
	if st == nil || st.metrics == nil {
		return
	}
	t.recordResultUsageOnStateLocked(st, usage, durationSeconds, numTurns)
}

func (t *TokenTracker) recordResultUsageOnStateLocked(st *taskTokenState, usage UsageStats, durationSeconds float64, numTurns int) {
	effectiveUsage := usage
	if isZeroUsage(effectiveUsage) {
		effectiveUsage = provisionalStepUsage(st)
	}
	if effectiveUsage.TotalTokens == 0 && (effectiveUsage.InputTokens > 0 || effectiveUsage.OutputTokens > 0) {
		effectiveUsage.TotalTokens = effectiveUsage.InputTokens + effectiveUsage.OutputTokens
	}

	st.accumulatedSteps.Add(effectiveUsage)
	st.stepDurationTotal += durationSeconds
	st.metrics.Turns += numTurns
	if numTurns <= 1 && !isZeroUsage(effectiveUsage) &&
		(isZeroUsage(st.metrics.LastStepUsage) || (!hasDetailedUsage(st.metrics.LastStepUsage) && hasDetailedUsage(effectiveUsage))) {
		st.metrics.LastStepUsage = effectiveUsage
	}
	st.stepUsages = make(map[int]UsageStats)
	st.unnamedStepSeq = 0
	st.liveUsage = nil
	st.dbStepDelta = nil

	st.metrics.Usage = st.accumulatedSteps
	if st.stepDurationTotal > 0 {
		st.metrics.DurationSeconds = st.stepDurationTotal
	}
}

func clampNonNegativeInt64(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func isZeroUsage(u UsageStats) bool {
	return u.TotalTokens == 0 && u.InputTokens == 0 && u.OutputTokens == 0 && u.ThinkingTokens == 0 && u.CacheReadTokens == 0
}

func hasDetailedUsage(u UsageStats) bool {
	return u.InputTokens > 0 || u.OutputTokens > 0
}

func usageMagnitude(u UsageStats) int64 {
	m := u.InputTokens + u.OutputTokens + u.ThinkingTokens
	if u.TotalTokens > m {
		return u.TotalTokens
	}
	return m
}

func pickRicherUsage(a, b UsageStats) UsageStats {
	if isZeroUsage(a) {
		return b
	}
	if isZeroUsage(b) {
		return a
	}
	aDet, bDet := hasDetailedUsage(a), hasDetailedUsage(b)
	if aDet != bDet {
		if aDet {
			return a
		}
		return b
	}
	if usageMagnitude(b) > usageMagnitude(a) {
		return b
	}
	return a
}

func provisionalStepUsage(st *taskTokenState) UsageStats {
	var stepSum UsageStats
	for _, u := range st.stepUsages {
		stepSum.Add(u)
	}
	best := stepSum
	if st.dbStepDelta != nil {
		best = pickRicherUsage(best, *st.dbStepDelta)
	}
	if st.liveUsage != nil {
		best = pickRicherUsage(best, *st.liveUsage)
	}
	return best
}

// recalculateStateLocked обновляет суммарную нагрузку задачи с учётом промежуточных данных текущего шага.
func recalculateStateLocked(st *taskTokenState) {
	total := st.accumulatedSteps
	total.Add(provisionalStepUsage(st))
	st.metrics.Usage = total
	if st.stepDurationTotal > 0 {
		st.metrics.DurationSeconds = st.stepDurationTotal
	}
}

// SnapshotForID возвращает копию текущих метрик задачи taskID (или nil, если задача не отслеживается).
func (t *TokenTracker) SnapshotForID(taskID int) *TaskTokenMetrics {
	t.RLock()
	defer t.RUnlock()

	st := t.stateForTaskLocked(taskID, false)
	if st == nil || st.metrics == nil {
		return nil
	}
	cp := *st.metrics
	return &cp
}

// DetachTaskForID снимает задачу с активного трекинга (например при /pause) без учёта в завершённой истории
// и возвращает снимок накопленных метрик для сохранения.
func (t *TokenTracker) DetachTaskForID(taskID int) *TaskTokenMetrics {
	t.Lock()
	defer t.Unlock()

	st := t.stateForTaskLocked(taskID, false)
	if st == nil || st.metrics == nil {
		return nil
	}
	cp := *st.metrics
	delete(t.tasks, taskID)
	t.pickNextActiveLocked(taskID)
	return &cp
}

// HydrateFromTasks восстанавливает активные/ожидающие/поставленные на паузу задачи после перезапуска бота.
func (t *TokenTracker) HydrateFromTasks(tasks []*TaskSession) {
	type item struct {
		id      int
		status  TaskStatus
		metrics TaskTokenMetrics
	}
	var items []item
	for _, task := range tasks {
		if task == nil {
			continue
		}
		view := task.Snapshot()
		if view.TokenMetrics == nil {
			continue
		}
		switch view.Status {
		case TaskStatusPlanning, TaskStatusRunning, TaskStatusWaitingInput, TaskStatusWaitingApproval, TaskStatusPaused:
		default:
			continue
		}
		mCopy := *view.TokenMetrics
		if mCopy.Project == "" {
			mCopy.Project = view.Project
		}
		if mCopy.Model == "" {
			mCopy.Model = view.Model
		}
		if mCopy.Agent == "" {
			mCopy.Agent = view.Agent
		}
		if mCopy.ConversationID == "" {
			mCopy.ConversationID = view.ConversationID
		}
		if mCopy.StartedAt.IsZero() {
			mCopy.StartedAt = view.StartedAt
		}
		items = append(items, item{id: view.ID, status: view.Status, metrics: mCopy})
	}
	if len(items) == 0 {
		return
	}

	t.Lock()
	defer t.Unlock()
	t.ensureMapLocked()

	for _, it := range items {
		if existing, ok := t.tasks[it.id]; ok && existing != nil && existing.metrics != nil {
			continue
		}
		mCopy := it.metrics
		t.tasks[it.id] = &taskTokenState{
			metrics:           &mCopy,
			stepUsages:        make(map[int]UsageStats),
			accumulatedSteps:  mCopy.Usage,
			stepDurationTotal: mCopy.DurationSeconds,
		}
		if it.status != TaskStatusPaused || !t.hasActiveTask {
			t.activeTaskID = it.id
			t.hasActiveTask = true
		}
	}
}

// FinishTaskForID завершает отслеживание задачи taskID и переносит её метрики в общую статистику сессии.
func (t *TokenTracker) FinishTaskForID(taskID int, prURL string) TaskTokenMetrics {
	t.Lock()
	defer t.Unlock()

	st := t.stateForTaskLocked(taskID, false)
	if st == nil || st.metrics == nil {
		if t.lastTask != nil {
			return *t.lastTask
		}
		return TaskTokenMetrics{}
	}
	return t.completeTaskStateLocked(taskID, st, prURL, true)
}

// FinishTask завершает активную задачу и сохраняет метрики в историю.
func (t *TokenTracker) FinishTask(prURL string) TaskTokenMetrics {
	t.Lock()
	defer t.Unlock()

	st := t.activeStateLocked()
	if st == nil || st.metrics == nil {
		if t.lastTask != nil {
			return *t.lastTask
		}
		return TaskTokenMetrics{}
	}
	return t.completeTaskStateLocked(t.activeTaskID, st, prURL, true)
}

// CancelTaskForID прерывает отслеживание задачи taskID с сохранением накопленных метрик в сессию.
func (t *TokenTracker) CancelTaskForID(taskID int) {
	t.Lock()
	defer t.Unlock()

	st := t.stateForTaskLocked(taskID, false)
	if st == nil || st.metrics == nil {
		return
	}
	t.completeTaskStateLocked(taskID, st, "", false)
}

// CancelAllTasks прерывает все отслеживаемые задачи с сохранением метрик в сессию.
func (t *TokenTracker) CancelAllTasks() {
	t.Lock()
	defer t.Unlock()

	for id, st := range t.tasks {
		if st != nil && st.metrics != nil {
			t.completeTaskStateLocked(id, st, "", false)
		}
	}
	t.tasks = make(map[int]*taskTokenState)
	t.activeTaskID = 0
	t.hasActiveTask = false
}

// CancelTask прерывает текущую задачу с сохранением метрик в сессию.
func (t *TokenTracker) CancelTask() {
	t.Lock()
	defer t.Unlock()

	st := t.activeStateLocked()
	if st == nil || st.metrics == nil {
		return
	}
	t.completeTaskStateLocked(t.activeTaskID, st, "", false)
}

func (t *TokenTracker) completeTaskStateLocked(taskID int, st *taskTokenState, prURL string, setPR bool) TaskTokenMetrics {
	st.metrics.FinishedAt = time.Now()
	if setPR {
		st.metrics.PRURL = prURL
	}
	if st.metrics.DurationSeconds <= 0 && !st.metrics.StartedAt.IsZero() {
		st.metrics.DurationSeconds = st.metrics.FinishedAt.Sub(st.metrics.StartedAt).Seconds()
	}

	completed := *st.metrics
	t.lastTask = &completed
	delete(t.tasks, taskID)
	t.pickNextActiveLocked(taskID)

	t.totalTasksRun++
	t.sessionUsage.Add(completed.Usage)
	t.totalDuration += completed.EffectiveDuration()
	return completed
}

// GetLiveStatusSnippetForID возвращает компактную строчку токенов для задачи taskID.
func (t *TokenTracker) GetLiveStatusSnippetForID(taskID int, lang string) string {
	t.RLock()
	defer t.RUnlock()

	st := t.stateForTaskLocked(taskID, false)
	return formatLiveStatusSnippet(st, lang)
}

// GetLiveStatusSnippet возвращает компактную строчку для живого обновления статуса.
func (t *TokenTracker) GetLiveStatusSnippet(lang string) string {
	t.RLock()
	defer t.RUnlock()

	return formatLiveStatusSnippet(t.activeStateLocked(), lang)
}

func formatLiveStatusSnippet(st *taskTokenState, lang string) string {
	if st == nil || st.metrics == nil {
		return ""
	}
	u := st.metrics.Usage
	if u.TotalTokens == 0 && u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadTokens == 0 {
		return ""
	}
	dur := st.metrics.EffectiveDuration()
	var tps float64
	if dur > 0 && u.OutputTokens > 0 {
		tps = float64(u.OutputTokens) / dur
	}
	cacheStr := "0"
	if u.CacheReadTokens > 0 {
		cacheStr = formatCompact(u.CacheReadTokens)
	}
	return i18n.Tf(lang, "tokens.live_snippet", formatCompact(u.TotalTokens), tps, cacheStr)
}

// GetCurrentTaskStatusBlock возвращает блок статистики токенов для активной задачи команды /status.
func (t *TokenTracker) GetCurrentTaskStatusBlock(lang string) string {
	t.RLock()
	st := t.activeStateLocked()
	var mCopy *TaskTokenMetrics
	if st != nil && st.metrics != nil {
		cp := *st.metrics
		mCopy = &cp
	}
	t.RUnlock()
	return formatTaskStatusBlock(mCopy, lang)
}

// GetTaskStatusBlock возвращает блок статистики токенов для конкретной задачи команды /status.
func (t *TokenTracker) GetTaskStatusBlock(task *TaskSession, lang string) string {
	if task == nil {
		return t.GetCurrentTaskStatusBlock(lang)
	}
	view := task.Snapshot()

	var metrics *TaskTokenMetrics
	t.RLock()
	if st := t.stateForTaskLocked(view.ID, view.IsActive); st != nil && st.metrics != nil {
		cp := *st.metrics
		metrics = &cp
	}
	t.RUnlock()

	if metrics == nil && view.TokenMetrics != nil {
		cp := *view.TokenMetrics
		metrics = &cp
	}

	if (metrics == nil || isZeroUsage(metrics.Usage)) && view.ConversationID != "" {
		if summary, ok := storage.ExtractConversationUsage(view.ConversationID); ok && summary.Steps > 0 {
			if metrics == nil {
				metrics = &TaskTokenMetrics{
					Project:        view.Project,
					Model:          view.Model,
					Agent:          view.Agent,
					ConversationID: view.ConversationID,
					StartedAt:      view.StartedAt,
					FinishedAt:     view.FinishedAt,
				}
			}
			metrics.Usage = UsageStats{
				InputTokens:     summary.Total.InputTokens,
				OutputTokens:    summary.Total.OutputTokens,
				ThinkingTokens:  summary.Total.ThinkingTokens,
				CacheReadTokens: summary.Total.CacheReadTokens,
				TotalTokens:     summary.Total.TotalTokens,
			}
			metrics.LastStepUsage = UsageStats{
				InputTokens:     summary.LastStep.InputTokens,
				OutputTokens:    summary.LastStep.OutputTokens,
				ThinkingTokens:  summary.LastStep.ThinkingTokens,
				CacheReadTokens: summary.LastStep.CacheReadTokens,
				TotalTokens:     summary.LastStep.TotalTokens,
			}
		}
	}

	return formatTaskStatusBlock(metrics, lang)
}

func formatTaskStatusBlock(m *TaskTokenMetrics, lang string) string {
	if m == nil {
		return ""
	}
	u := m.Usage
	dur := m.EffectiveDuration()
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
	if st := t.activeStateLocked(); st != nil && st.metrics != nil {
		cur := *st.metrics
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
	switch {
	// Псевдонимы sonnet/opus/haiku слова claude не содержат.
	case strings.Contains(m, "claude"), strings.Contains(m, "sonnet"),
		strings.Contains(m, "opus"), strings.Contains(m, "haiku"):
		return 200_000
	case strings.Contains(m, "gpt-oss") || strings.Contains(m, "120b"):
		return 131_072
	case strings.Contains(m, "grok"):
		return 256_000
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

func (t *TokenTracker) lookupTaskMetricsForContext(taskID int, taskProj string, taskMetrics *TaskTokenMetrics, taskIsActive bool) (metrics TaskTokenMetrics, hasMetrics bool) {
	t.RLock()
	defer t.RUnlock()

	if st := t.stateForTaskLocked(taskID, taskIsActive); st != nil && st.metrics != nil {
		return *st.metrics, true
	}
	if taskMetrics != nil {
		return *taskMetrics, true
	}
	if t.lastTask != nil && t.lastTask.Project == taskProj {
		return *t.lastTask, true
	}
	return TaskTokenMetrics{}, false
}

// GetContextCommandMessage формирует подробный отчёт об использовании контекстного окна агента.
func (t *TokenTracker) GetContextCommandMessage(task *TaskSession, defaultProject, defaultModel, lang string) string {
	var (
		taskID       int
		taskProj     string
		taskMod      string
		taskStatus   TaskStatus
		taskConvID   string
		taskMetrics  *TaskTokenMetrics
		taskIsActive bool
		taskStarted  time.Time
		taskFinished time.Time
		taskAgent    string
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
		taskStarted = task.StartedAt
		taskFinished = task.FinishedAt
		taskAgent = task.Agent
		task.mu.Unlock()
	}

	var bldr strings.Builder

	// 1. Если передана конкретная задача или есть активная задача
	if task != nil {
		metrics, hasMetrics := t.lookupTaskMetricsForContext(taskID, taskProj, taskMetrics, taskIsActive)

		if taskConvID == "" && metrics.ConversationID != "" {
			taskConvID = metrics.ConversationID
		}
		if metrics.Model != "" {
			taskMod = metrics.Model
		}

		// Если в памяти/БД метрики нулевые или нет LastStepUsage, пробуем прочитать из локальной БД agy (без блокировки трекера).
		if taskConvID != "" && (!hasMetrics || isZeroUsage(metrics.Usage) || (metrics.LastStepUsage.InputTokens == 0 && metrics.LastStepUsage.OutputTokens == 0)) {
			if summary, ok := storage.ExtractConversationUsage(taskConvID); ok && summary.Steps > 0 {
				if isZeroUsage(metrics.Usage) {
					metrics.Usage = UsageStats{
						InputTokens:     summary.Total.InputTokens,
						OutputTokens:    summary.Total.OutputTokens,
						ThinkingTokens:  summary.Total.ThinkingTokens,
						CacheReadTokens: summary.Total.CacheReadTokens,
						TotalTokens:     summary.Total.TotalTokens,
					}
					if metrics.StartedAt.IsZero() {
						metrics.StartedAt = taskStarted
					}
					if metrics.FinishedAt.IsZero() {
						metrics.FinishedAt = taskFinished
					}
					if metrics.Agent == "" {
						metrics.Agent = taskAgent
					}
				}
				if metrics.LastStepUsage.InputTokens == 0 && metrics.LastStepUsage.OutputTokens == 0 {
					metrics.LastStepUsage = UsageStats{
						InputTokens:     summary.LastStep.InputTokens,
						OutputTokens:    summary.LastStep.OutputTokens,
						ThinkingTokens:  summary.LastStep.ThinkingTokens,
						CacheReadTokens: summary.LastStep.CacheReadTokens,
						TotalTokens:     summary.LastStep.TotalTokens,
					}
				}
				hasMetrics = true
			}
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
			if agentName == "" {
				agentName = taskAgent
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
	t.RLock()
	var lastCopy *TaskTokenMetrics
	if t.lastTask != nil {
		cp := *t.lastTask
		lastCopy = &cp
	}
	t.RUnlock()
	if lastCopy != nil {
		last := *lastCopy
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

// FormatToolAction возвращает понятное описание действия инструмента.
func FormatToolAction(name string, info *StreamToolInfo) string {
	if info == nil || info.Parameters == nil {
		return fmt.Sprintf("🔧 %s", name)
	}
	normalized := tools.NormalizeBotToolName(name)
	switch normalized {
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
		} else if q, ok := info.Parameters["query"].(string); ok && q != "" {
			return fmt.Sprintf("🔍 grep: %s", utils.TruncateString(q, 50))
		} else if q, ok := info.Parameters["pattern"].(string); ok && q != "" {
			return fmt.Sprintf("🔍 grep: %s", utils.TruncateString(q, 50))
		}
	case "find_by_name", "find_files":
		if pat, ok := info.Parameters["Pattern"].(string); ok && pat != "" {
			return fmt.Sprintf("📁 find: %s", utils.TruncateString(pat, 50))
		} else if pat, ok := info.Parameters["pattern"].(string); ok && pat != "" {
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
	case "send_file":
		if p, ok := info.Parameters["path"].(string); ok && p != "" {
			return fmt.Sprintf("📤 send_file: %s", filepath.Base(p))
		}
		return "📤 send_file"
	case "ask_user":
		if items := utils.ExtractAskQuestionItems(info.Parameters); len(items) > 0 {
			if len(items) > 1 {
				return fmt.Sprintf("❓ ask_user (%d): %s", len(items), utils.TruncateString(items[0].Question, 40))
			}
			return fmt.Sprintf("❓ ask_user: %s", utils.TruncateString(items[0].Question, 50))
		}
		return "❓ ask_user"
	case "telegram_send_message":
		return "💬 send_message"
	case "report_progress":
		if msg, ok := info.Parameters["status"].(string); ok && msg != "" {
			return fmt.Sprintf("📍 progress: %s", utils.TruncateString(msg, 50))
		} else if msg, ok := info.Parameters["message"].(string); ok && msg != "" {
			return fmt.Sprintf("📍 progress: %s", utils.TruncateString(msg, 50))
		}
		return "📍 progress"
	case "get_task_context":
		return "📋 get_task_context"
	case "list_tasks":
		return "📋 list_tasks"
	case "list_projects":
		return "📁 list_projects"
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
