package domain

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"tg-agent-bot/internal/models"
	"tg-agent-bot/internal/utils"
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

// ParseStreamEvent разбирает строку NDJSON от agy.
func ParseStreamEvent(line string) (*StreamEvent, error) {
	var evt StreamEvent
	if err := json.Unmarshal([]byte(line), &evt); err != nil {
		return nil, err
	}
	if evt.Event == "" {
		return nil, fmt.Errorf("empty event")
	}
	return &evt, nil
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
	Response        string      `json:"response"`
	DurationSeconds float64     `json:"duration_seconds"`
	NumTurns        int         `json:"num_turns"`
	Usage           *UsageStats `json:"usage"`
}

// TaskTokenMetrics хранит полные метрики токенов задачи или шага.
type TaskTokenMetrics struct {
	Project         string
	Model           string
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
func (m TaskTokenMetrics) FormatCompletionSummary() string {
	dur := m.EffectiveDuration()
	durStr := FormatDurationHuman(time.Duration(dur * float64(time.Second)))
	tps := m.TokensPerSecond()

	var bldr strings.Builder
	bldr.WriteString("📊 <b>Статистика использования токенов:</b>\n")
	bldr.WriteString(fmt.Sprintf("• 🔢 <b>Всего:</b> <code>%s</code> (📥 %s | 📤 %s",
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

	bldr.WriteString(fmt.Sprintf("• ⚡ <b>Скорость генерации:</b> <code>%.1f токенов/сек</code>\n", tps))
	bldr.WriteString(fmt.Sprintf("• ⏱ <b>Время работы:</b> <code>%s</code>\n", durStr))
	if m.Model != "" {
		bldr.WriteString(fmt.Sprintf("• 🧠 <b>Модель:</b> <code>%s</code>\n", html.EscapeString(m.Model)))
	}
	if m.Usage.CacheReadTokens > 0 {
		bldr.WriteString(fmt.Sprintf("• 💾 <b>Кэш промпта:</b> <code>%s</code> (%.1f%% экономии)\n",
			formatThousands(m.Usage.CacheReadTokens), m.Usage.CacheHitRate()))
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
}

var GlobalTokenTracker = NewTokenTracker()

func NewTokenTracker() *TokenTracker {
	return &TokenTracker{
		stepUsages: make(map[int]UsageStats),
	}
}

// StartTask инициализирует отслеживание новой задачи.
func (t *TokenTracker) StartTask(project, model, prompt string) {
	t.Lock()
	defer t.Unlock()

	t.currentTask = &TaskTokenMetrics{
		Project:   project,
		Model:     model,
		Prompt:    prompt,
		StartedAt: time.Now(),
		Usage:     UsageStats{},
	}
	t.stepUsages = make(map[int]UsageStats)
	t.accumulatedSteps = UsageStats{}
	t.stepDurationTotal = 0
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
	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		t.currentTask.LastStepUsage = usage
	}
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
func (t *TokenTracker) GetLiveStatusSnippet() string {
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

	return fmt.Sprintf("⚡ <b>Токены:</b> <code>%s</code> (⚡ %.1f т/с | 💾 кэш: %s)",
		formatCompact(total), tps, cacheStr)
}

// GetCurrentTaskStatusBlock возвращает блок статистики токенов для команды /status.
func (t *TokenTracker) GetCurrentTaskStatusBlock() string {
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
	bldr.WriteString("📊 <b>Токены задачи:</b>\n")
	bldr.WriteString(fmt.Sprintf("• Использовано: <code>%s</code> (📥 %s | 📤 %s",
		formatThousands(u.TotalTokens),
		formatCompact(u.InputTokens),
		formatCompact(u.OutputTokens),
	))
	if u.ThinkingTokens > 0 {
		bldr.WriteString(fmt.Sprintf(" | 💭 %s", formatCompact(u.ThinkingTokens)))
	}
	bldr.WriteString(")\n")

	if u.CacheReadTokens > 0 {
		bldr.WriteString(fmt.Sprintf("• 💾 Кэш токенов: <code>%s</code> (%.1f%% экономии)\n",
			formatThousands(u.CacheReadTokens), u.CacheHitRate()))
	} else {
		bldr.WriteString("• 💾 Кэш токенов: <code>0</code> (0.0%)\n")
	}

	bldr.WriteString(fmt.Sprintf("• ⚡ Скорость генерации: <code>%.1f токенов/сек</code>", tps))

	return bldr.String()
}

// GetLastTaskStatusBlock возвращает компактный блок о последней завершенной задаче.
func (t *TokenTracker) GetLastTaskStatusBlock() string {
	t.RLock()
	defer t.RUnlock()

	if t.lastTask == nil {
		return ""
	}

	u := t.lastTask.Usage
	dur := t.lastTask.EffectiveDuration()
	durStr := FormatDurationHuman(time.Duration(dur * float64(time.Second)))
	tps := t.lastTask.TokensPerSecond()

	return fmt.Sprintf("📊 <b>Последняя задача (<code>%s</code>):</b>\n"+
		"• Токены: <code>%s</code> (📥 %s | 📤 %s | 💾 %s)\n"+
		"• Скорость: <code>%.1f т/с</code> | Время: <code>%s</code>",
		html.EscapeString(t.lastTask.Project),
		formatThousands(u.TotalTokens),
		formatCompact(u.InputTokens),
		formatCompact(u.OutputTokens),
		formatCompact(u.CacheReadTokens),
		tps,
		durStr,
	)
}

// GetTokensCommandMessage формирует полное сообщение для команды /tokens.
func (t *TokenTracker) GetTokensCommandMessage() string {
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

		bldr.WriteString("⚡ <b>Активная задача в работе</b>\n\n")
		bldr.WriteString(fmt.Sprintf("📁 Проект: <code>%s</code>\n", html.EscapeString(cur.Project)))
		bldr.WriteString(fmt.Sprintf("🧠 Модель: <code>%s</code>\n", html.EscapeString(cur.Model)))
		bldr.WriteString(fmt.Sprintf("⏱ Время в работе: <code>%s</code>\n\n", durStr))

		bldr.WriteString("📊 <b>Расход токенов:</b>\n")
		bldr.WriteString(fmt.Sprintf("• 🔢 Всего: <code>%s</code>\n", formatThousands(cur.Usage.TotalTokens)))
		bldr.WriteString(fmt.Sprintf("• 📥 Входные (промпт): <code>%s</code>\n", formatThousands(cur.Usage.InputTokens)))
		bldr.WriteString(fmt.Sprintf("• 📤 Выходные (ответ): <code>%s</code>\n", formatThousands(cur.Usage.OutputTokens)))
		if cur.Usage.ThinkingTokens > 0 {
			bldr.WriteString(fmt.Sprintf("• 💭 Рассуждения (thinking): <code>%s</code>\n", formatThousands(cur.Usage.ThinkingTokens)))
		}
		bldr.WriteString(fmt.Sprintf("• 💾 Из кэша (cache read): <code>%s</code> (%.1f%% попаданий)\n\n",
			formatThousands(cur.Usage.CacheReadTokens), cur.Usage.CacheHitRate()))

		bldr.WriteString("🚀 <b>Производительность:</b>\n")
		bldr.WriteString(fmt.Sprintf("• ⚡ Скорость генерации: <code>%.1f токенов/сек</code>\n", tps))
		if totalTps > 0 {
			bldr.WriteString(fmt.Sprintf("• 🏎 Общая скорость (вход+выход): <code>%.1f т/с</code>\n", totalTps))
		}
		if cur.Turns > 0 {
			bldr.WriteString(fmt.Sprintf("• 🔄 Итераций (turns): <code>%d</code>\n", cur.Turns))
		}
		return bldr.String()
	}

	// 2. Если задачи нет, показываем последнюю задачу и общую статистику сессии
	if t.lastTask != nil {
		last := *t.lastTask
		dur := last.EffectiveDuration()
		durStr := FormatDurationHuman(time.Duration(dur * float64(time.Second)))
		tps := last.TokensPerSecond()

		bldr.WriteString("💤 <i>Сейчас нет активных задач.</i>\n\n")
		bldr.WriteString("📊 <b>Статистика последней задачи:</b>\n")
		bldr.WriteString(fmt.Sprintf("• 📁 Проект: <code>%s</code>\n", html.EscapeString(last.Project)))
		bldr.WriteString(fmt.Sprintf("• 🧠 Модель: <code>%s</code>\n", html.EscapeString(last.Model)))
		bldr.WriteString(fmt.Sprintf("• ⏱ Время работы: <code>%s</code>\n", durStr))
		bldr.WriteString(fmt.Sprintf("• 🔢 Всего токенов: <code>%s</code>\n", formatThousands(last.Usage.TotalTokens)))
		bldr.WriteString(fmt.Sprintf("• 📥 Входные (промпт): <code>%s</code>\n", formatThousands(last.Usage.InputTokens)))
		bldr.WriteString(fmt.Sprintf("• 📤 Выходные (ответ): <code>%s</code>\n", formatThousands(last.Usage.OutputTokens)))
		if last.Usage.ThinkingTokens > 0 {
			bldr.WriteString(fmt.Sprintf("• 💭 Рассуждения (thinking): <code>%s</code>\n", formatThousands(last.Usage.ThinkingTokens)))
		}
		bldr.WriteString(fmt.Sprintf("• 💾 Из кэша (cache read): <code>%s</code> (%.1f%%)\n",
			formatThousands(last.Usage.CacheReadTokens), last.Usage.CacheHitRate()))
		bldr.WriteString(fmt.Sprintf("• ⚡ Скорость генерации: <code>%.1f токенов/сек</code>\n", tps))
		if last.PRURL != "" {
			bldr.WriteString(fmt.Sprintf("• 🔗 PR: <a href=\"%s\">Открыть Pull Request</a>\n", html.EscapeString(last.PRURL)))
		}
		bldr.WriteString("\n")
	}

	if t.totalTasksRun > 0 {
		var avgSpeed float64
		if t.totalDuration > 0 && t.sessionUsage.OutputTokens > 0 {
			avgSpeed = float64(t.sessionUsage.OutputTokens) / t.totalDuration
		}
		totalDurStr := FormatDurationHuman(time.Duration(t.totalDuration * float64(time.Second)))

		bldr.WriteString("📈 <b>Общая статистика сессии бота:</b>\n")
		bldr.WriteString(fmt.Sprintf("• Выполнено задач: <code>%d</code>\n", t.totalTasksRun))
		bldr.WriteString(fmt.Sprintf("• Всего токенов: <code>%s</code>\n", formatThousands(t.sessionUsage.TotalTokens)))
		bldr.WriteString(fmt.Sprintf("  ├ 📥 Промпт: <code>%s</code>\n", formatCompact(t.sessionUsage.InputTokens)))
		bldr.WriteString(fmt.Sprintf("  ├ 📤 Ответы: <code>%s</code>", formatCompact(t.sessionUsage.OutputTokens)))
		if t.sessionUsage.ThinkingTokens > 0 {
			bldr.WriteString(fmt.Sprintf(" (💭 %s thinking)", formatCompact(t.sessionUsage.ThinkingTokens)))
		}
		bldr.WriteString("\n")
		bldr.WriteString(fmt.Sprintf("  └ 💾 Кэш: <code>%s</code>\n", formatCompact(t.sessionUsage.CacheReadTokens)))
		bldr.WriteString(fmt.Sprintf("• Общее время работы: <code>%s</code>\n", totalDurStr))
		bldr.WriteString(fmt.Sprintf("• Средняя скорость: <code>%.1f токенов/сек</code>\n", avgSpeed))
	} else {
		bldr.WriteString("📊 <b>Статистика использования токенов</b>\n\n")
		bldr.WriteString("💤 Задачи ещё не запускались в этой сессии.\n")
		bldr.WriteString("Отправьте задачу боту сообщением в чат, чтобы начать работу!")
	}

	bldr.WriteString("\n\n💡 <i>Детализация контекстного окна модели: /context</i>")
	return bldr.String()
}

// FormatShortLastTask возвращает компактную строчку для команды /usage.
func (t *TokenTracker) FormatShortLastTask() string {
	t.RLock()
	defer t.RUnlock()

	if t.lastTask == nil {
		return ""
	}

	u := t.lastTask.Usage
	tps := t.lastTask.TokensPerSecond()
	return fmt.Sprintf("%s (📥 %s, 📤 %s, ⚡ %.1f т/с)",
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

// GetContextCommandMessage формирует подробный отчёт об использовании контекстного окна agy.
func (t *TokenTracker) GetContextCommandMessage(task *TaskSession, defaultProject, defaultModel string) string {
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
		task.Lock()
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
		task.Unlock()
	}

	t.RLock()
	defer t.RUnlock()

	var bldr strings.Builder

	// 1. Если передана конкретная задача или есть активная задача
	if task != nil {
		var metrics TaskTokenMetrics
		var hasMetrics bool

		if t.currentTask != nil && (taskIsActive || t.currentTask.Project == taskProj) {
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
			bldr.WriteString(fmt.Sprintf("📋 <b>Контекст задачи #%d</b> [%s]\n\n", taskID, taskStatus.RussianTitle()))
			bldr.WriteString(fmt.Sprintf("📁 <b>Проект:</b> <code>%s</code>\n", html.EscapeString(taskProj)))
			bldr.WriteString(fmt.Sprintf("🧠 <b>Модель:</b> <code>%s</code>\n", html.EscapeString(taskMod)))
			bldr.WriteString(fmt.Sprintf("📏 <b>Окно контекста:</b> <code>%s</code> токенов (%s)\n\n", windowLimitStr, formatThousands(windowLimit)))
			bldr.WriteString(fmt.Sprintf("<code>[%s]</code> <b>0.0%%</b>\n\n", renderContextBar(0, 20)))
			bldr.WriteString("⏳ <i>Метрики контекста ещё не поступили от agy. Они появятся после первого шага выполнения.</i>\n\n")
			bldr.WriteString("💡 <i>Окно контекста модели определяет максимальный объём промпта, истории и файлов (1.0M для Gemini, 200k для Claude).</i>")
			return bldr.String()
		}

		var promptTokens int64
		var cacheTokens int64
		var outputTokens int64
		var thinkingTokens int64

		if metrics.LastStepUsage.InputTokens > 0 || metrics.LastStepUsage.OutputTokens > 0 {
			promptTokens = metrics.LastStepUsage.InputTokens
			cacheTokens = metrics.LastStepUsage.CacheReadTokens
			outputTokens = metrics.LastStepUsage.OutputTokens
			thinkingTokens = metrics.LastStepUsage.ThinkingTokens
		} else {
			promptTokens = metrics.Usage.InputTokens
			cacheTokens = metrics.Usage.CacheReadTokens
			outputTokens = metrics.Usage.OutputTokens
			thinkingTokens = metrics.Usage.ThinkingTokens
		}

		totalPromptTokens := promptTokens + cacheTokens
		activeContextTokens := totalPromptTokens + outputTokens
		if activeContextTokens <= 0 {
			activeContextTokens = metrics.Usage.TotalTokens
		}

		usedPercent := (float64(activeContextTokens) / float64(windowLimit)) * 100.0
		freeTokens := windowLimit - activeContextTokens
		if freeTokens < 0 {
			freeTokens = 0
		}
		freePercent := 100.0 - usedPercent
		if freePercent < 0 {
			freePercent = 0
		}

		if taskIsActive {
			bldr.WriteString(fmt.Sprintf("⚡ <b>Контекст активной задачи #%d</b>\n\n", taskID))
		} else {
			bldr.WriteString(fmt.Sprintf("📊 <b>Контекст задачи #%d</b> [%s]\n\n", taskID, taskStatus.RussianTitle()))
		}

		bldr.WriteString(fmt.Sprintf("📁 <b>Проект:</b> <code>%s</code>\n", html.EscapeString(taskProj)))
		bldr.WriteString(fmt.Sprintf("🧠 <b>Модель:</b> <code>%s</code>\n", html.EscapeString(taskMod)))
		bldr.WriteString(fmt.Sprintf("📏 <b>Окно контекста:</b> <code>%s / %s</code> токенов (<b>%.1f%%</b>)\n",
			formatCompact(activeContextTokens), windowLimitStr, usedPercent))
		bldr.WriteString(fmt.Sprintf("<code>[%s]</code> <b>%.1f%%</b>\n\n", renderContextBar(usedPercent, 20), usedPercent))

		bldr.WriteString("📊 <b>Распределение контекста (Context Breakdown):</b>\n")
		promptPercent := (float64(totalPromptTokens) / float64(windowLimit)) * 100.0
		bldr.WriteString(fmt.Sprintf("• 📥 <b>Входной контекст (Prompt / Files):</b> <code>%s</code> (%.2f%% окна)\n",
			formatThousands(totalPromptTokens), promptPercent))
		if cacheTokens > 0 {
			hitRate := 0.0
			if totalPromptTokens > 0 {
				hitRate = (float64(cacheTokens) / float64(totalPromptTokens)) * 100.0
			}
			bldr.WriteString(fmt.Sprintf("  ├ 💾 <b>Кэш промпта:</b> <code>%s</code> (%.1f%% экономии)\n",
				formatThousands(cacheTokens), hitRate))
			bldr.WriteString("  └ ⚙️ <b>Системный контекст:</b> инструкции, правила, схемы инструментов\n")
		} else {
			bldr.WriteString("  └ ⚙️ <b>Системный контекст:</b> инструкции, правила, схемы инструментов\n")
		}

		respPercent := (float64(outputTokens) / float64(windowLimit)) * 100.0
		bldr.WriteString(fmt.Sprintf("• 📤 <b>Ответы агента (Responses):</b> <code>%s</code> токенов (%.2f%%)\n",
			formatThousands(outputTokens), respPercent))
		if thinkingTokens > 0 {
			bldr.WriteString(fmt.Sprintf("  └ 💭 <b>Рассуждения (Thinking):</b> <code>%s</code> токенов\n",
				formatThousands(thinkingTokens)))
		}

		if metrics.ToolCallsCount > 0 {
			bldr.WriteString(fmt.Sprintf("• 🔧 <b>Вызовы инструментов:</b> <code>%d</code> шагов\n", metrics.ToolCallsCount))
		}
		if metrics.Turns > 0 {
			bldr.WriteString(fmt.Sprintf("• 🔄 <b>Итераций диалога (Turns):</b> <code>%d</code>\n", metrics.Turns))
		}
		bldr.WriteString(fmt.Sprintf("• 🆓 <b>Свободно в окне контекста:</b> <code>%s</code> (<b>%.1f%%</b>)\n\n",
			formatThousands(freeTokens), freePercent))

		dur := metrics.EffectiveDuration()
		durStr := FormatDurationHuman(time.Duration(dur * float64(time.Second)))
		tps := metrics.TokensPerSecond()

		bldr.WriteString("🚀 <b>Динамика сессии:</b>\n")
		bldr.WriteString(fmt.Sprintf("• 🔢 Кумулятивно за задачу: <code>%s</code> токенов\n", formatThousands(metrics.Usage.TotalTokens)))
		bldr.WriteString(fmt.Sprintf("• ⚡ Скорость генерации: <code>%.1f токенов/сек</code>\n", tps))
		bldr.WriteString(fmt.Sprintf("• ⏱ Время работы: <code>%s</code>\n", durStr))
		if metrics.PRURL != "" {
			bldr.WriteString(fmt.Sprintf("• 🔗 <b>PR:</b> <a href=\"%s\">Открыть Pull Request</a>\n", html.EscapeString(metrics.PRURL)))
		}
		if taskConvID != "" {
			bldr.WriteString(fmt.Sprintf("• 🧵 <b>Сессия agy:</b> <code>%s</code>\n", html.EscapeString(taskConvID)))
		}

		bldr.WriteString("\n💡 <i>В agy команда /context визуализирует распределение контекстного окна. Бот получает эту статистику в реальном времени из потока событий agy.</i>")
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

		var promptTokens int64
		var cacheTokens int64
		var outputTokens int64
		var thinkingTokens int64

		if last.LastStepUsage.InputTokens > 0 || last.LastStepUsage.OutputTokens > 0 {
			promptTokens = last.LastStepUsage.InputTokens
			cacheTokens = last.LastStepUsage.CacheReadTokens
			outputTokens = last.LastStepUsage.OutputTokens
			thinkingTokens = last.LastStepUsage.ThinkingTokens
		} else {
			promptTokens = last.Usage.InputTokens
			cacheTokens = last.Usage.CacheReadTokens
			outputTokens = last.Usage.OutputTokens
			thinkingTokens = last.Usage.ThinkingTokens
		}

		totalPromptTokens := promptTokens + cacheTokens
		activeContextTokens := totalPromptTokens + outputTokens
		if activeContextTokens <= 0 {
			activeContextTokens = last.Usage.TotalTokens
		}

		usedPercent := (float64(activeContextTokens) / float64(windowLimit)) * 100.0
		freeTokens := windowLimit - activeContextTokens
		if freeTokens < 0 {
			freeTokens = 0
		}
		freePercent := 100.0 - usedPercent
		if freePercent < 0 {
			freePercent = 0
		}

		bldr.WriteString("💤 <i>Сейчас нет активных задач.</i>\n\n")
		bldr.WriteString(fmt.Sprintf("📊 <b>Контекст последней задачи</b> (<code>%s</code>)\n\n", html.EscapeString(last.Project)))
		bldr.WriteString(fmt.Sprintf("🧠 <b>Модель:</b> <code>%s</code>\n", html.EscapeString(mod)))
		bldr.WriteString(fmt.Sprintf("📏 <b>Окно контекста:</b> <code>%s / %s</code> токенов (<b>%.1f%%</b>)\n",
			formatCompact(activeContextTokens), windowLimitStr, usedPercent))
		bldr.WriteString(fmt.Sprintf("<code>[%s]</code> <b>%.1f%%</b>\n\n", renderContextBar(usedPercent, 20), usedPercent))

		bldr.WriteString("📊 <b>Распределение контекста (Context Breakdown):</b>\n")
		promptPercent := (float64(totalPromptTokens) / float64(windowLimit)) * 100.0
		bldr.WriteString(fmt.Sprintf("• 📥 <b>Входной контекст (Prompt / Files):</b> <code>%s</code> (%.2f%% окна)\n",
			formatThousands(totalPromptTokens), promptPercent))
		if cacheTokens > 0 {
			hitRate := 0.0
			if totalPromptTokens > 0 {
				hitRate = (float64(cacheTokens) / float64(totalPromptTokens)) * 100.0
			}
			bldr.WriteString(fmt.Sprintf("  ├ 💾 <b>Кэш промпта:</b> <code>%s</code> (%.1f%% экономии)\n",
				formatThousands(cacheTokens), hitRate))
			bldr.WriteString("  └ ⚙️ <b>Системный контекст:</b> инструкции, правила, схемы инструментов\n")
		} else {
			bldr.WriteString("  └ ⚙️ <b>Системный контекст:</b> инструкции, правила, схемы инструментов\n")
		}

		respPercent := (float64(outputTokens) / float64(windowLimit)) * 100.0
		bldr.WriteString(fmt.Sprintf("• 📤 <b>Ответы агента (Responses):</b> <code>%s</code> токенов (%.2f%%)\n",
			formatThousands(outputTokens), respPercent))
		if thinkingTokens > 0 {
			bldr.WriteString(fmt.Sprintf("  └ 💭 <b>Рассуждения (Thinking):</b> <code>%s</code> токенов\n",
				formatThousands(thinkingTokens)))
		}
		if last.ToolCallsCount > 0 {
			bldr.WriteString(fmt.Sprintf("• 🔧 <b>Вызовы инструментов:</b> <code>%d</code> шагов\n", last.ToolCallsCount))
		}
		if last.Turns > 0 {
			bldr.WriteString(fmt.Sprintf("• 🔄 <b>Итераций диалога (Turns):</b> <code>%d</code>\n", last.Turns))
		}
		bldr.WriteString(fmt.Sprintf("• 🆓 <b>Свободно в окне:</b> <code>%s</code> (<b>%.1f%%</b>)\n\n",
			formatThousands(freeTokens), freePercent))

		dur := last.EffectiveDuration()
		durStr := FormatDurationHuman(time.Duration(dur * float64(time.Second)))
		tps := last.TokensPerSecond()

		bldr.WriteString("🚀 <b>Итоги:</b>\n")
		bldr.WriteString(fmt.Sprintf("• 🔢 Всего токенов за задачу: <code>%s</code>\n", formatThousands(last.Usage.TotalTokens)))
		bldr.WriteString(fmt.Sprintf("• ⚡ Скорость генерации: <code>%.1f токенов/сек</code>\n", tps))
		bldr.WriteString(fmt.Sprintf("• ⏱ Время выполнения: <code>%s</code>\n", durStr))
		if last.PRURL != "" {
			bldr.WriteString(fmt.Sprintf("• 🔗 <b>PR:</b> <a href=\"%s\">Открыть Pull Request</a>\n", html.EscapeString(last.PRURL)))
		}
		if last.ConversationID != "" {
			bldr.WriteString(fmt.Sprintf("• 🧵 <b>Сессия agy:</b> <code>%s</code>\n", html.EscapeString(last.ConversationID)))
		}

		bldr.WriteString("\n💡 <i>Контекст конкретной задачи: /context &lt;id&gt; (список: /tasks).</i>")
		return bldr.String()
	}

	// 3. Если задачи ещё не запускались в этой сессии бота
	mod := defaultModel
	if mod == "" {
		mod = "gemini-3.1-pro-high"
	}
	windowLimit := ModelContextWindow(mod)
	windowLimitStr := FormatContextLimit(windowLimit)

	bldr.WriteString("🧠 <b>Контекстное окно модели agy</b>\n\n")
	if defaultProject != "" {
		bldr.WriteString(fmt.Sprintf("📁 <b>Текущий проект:</b> <code>%s</code>\n", html.EscapeString(defaultProject)))
	}
	bldr.WriteString(fmt.Sprintf("🧠 <b>Активная модель:</b> <code>%s</code>\n", html.EscapeString(mod)))
	bldr.WriteString(fmt.Sprintf("📏 <b>Размер окна контекста:</b> <code>%s</code> токенов (%s)\n",
		windowLimitStr, formatThousands(windowLimit)))
	bldr.WriteString(fmt.Sprintf("• 📥 Использовано: <code>0</code> токенов (<b>0.0%%</b>)\n"))
	bldr.WriteString(fmt.Sprintf("• 🆓 Свободно в окне: <code>%s</code> токенов (<b>100.0%%</b>)\n\n",
		formatThousands(windowLimit)))
	bldr.WriteString(fmt.Sprintf("<code>[%s]</code> <b>0.0%%</b>\n\n", renderContextBar(0, 20)))

	bldr.WriteString("📊 <b>Что загружается в контекст agy при старте задачи:</b>\n")
	bldr.WriteString("• ⚙️ <b>Системные инструкции:</b> базовый промпт агента (~3-5k токенов)\n")
	bldr.WriteString("• 📚 <b>Скиллы и правила:</b> AGENT.md, встроенные навыки agy (~5-10k токенов)\n")
	bldr.WriteString("• 🔧 <b>Схемы инструментов:</b> run_command, view_file, write_to_file (~4-8k токенов)\n")
	bldr.WriteString("• 💾 <b>Кэш промпта:</b> повторные префиксы автоматически кэшируются\n")
	bldr.WriteString("• 💬 <b>История сообщений:</b> диалог и вызовы инструментов передаются на каждом шаге\n\n")

	bldr.WriteString("💡 <i>В agy команда /context визуализирует распределение контекстного окна модели. Бот собирает эти метрики в реальном времени из потока телеметрии agy.\nОтправьте задачу сообщением в чат, чтобы начать работу!</i>")
	return bldr.String()
}

// formatToolAction возвращает понятное описание действия инструмента.
func FormatToolAction(name string, info *StreamToolInfo) string {
	if info == nil || info.Parameters == nil {
		return fmt.Sprintf("🔧 %s", name)
	}
	switch name {
	case "run_command":
		if cmd, ok := info.Parameters["CommandLine"].(string); ok && cmd != "" {
			return fmt.Sprintf("⚡ %s", utils.TruncateString(cmd, 70))
		}
	case "replace_file_content":
		if target, ok := info.Parameters["TargetFile"].(string); ok && target != "" {
			return fmt.Sprintf("✏️ edit: %s", filepath.Base(target))
		}
	case "write_to_file":
		if target, ok := info.Parameters["TargetFile"].(string); ok && target != "" {
			return fmt.Sprintf("📝 write: %s", filepath.Base(target))
		}
	case "view_file":
		if path, ok := info.Parameters["AbsolutePath"].(string); ok && path != "" {
			return fmt.Sprintf("👁 view: %s", filepath.Base(path))
		}
	case "grep_search":
		if q, ok := info.Parameters["Query"].(string); ok && q != "" {
			return fmt.Sprintf("🔍 grep: %s", utils.TruncateString(q, 50))
		}
	case "find_by_name":
		if pat, ok := info.Parameters["Pattern"].(string); ok && pat != "" {
			return fmt.Sprintf("📁 find: %s", utils.TruncateString(pat, 50))
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

func FormatDurationHuman(d time.Duration) string {
	d = d.Round(time.Second)
	totalSec := int64(d.Seconds())
	if totalSec < 60 {
		return fmt.Sprintf("%dс", totalSec)
	}
	mins := totalSec / 60
	secs := totalSec % 60
	if mins < 60 {
		if secs > 0 {
			return fmt.Sprintf("%dм %02dс", mins, secs)
		}
		return fmt.Sprintf("%dм", mins)
	}
	hours := mins / 60
	remMins := mins % 60
	return fmt.Sprintf("%dч %02dм", hours, remMins)
}
