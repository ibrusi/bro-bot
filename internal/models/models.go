package models

import (
	"bufio"
	"context"
	"fmt"
	"html"
	"log"
	"sort"
	"strings"
	"sync"
	"tg-agent-bot/internal/ports"
	"tg-agent-bot/internal/utils"
	"time"
)

type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

var Agent ports.AgentFramework

type ModelRegistry struct {
	sync.RWMutex
	models      []ModelInfo
	modelsByID  map[string]ModelInfo
	aliases     map[string]string
	lastFetched time.Time
	cacheTTL    time.Duration
}

var (
	BaseAliases   = baseAliases
	GlobalModelRegistry *ModelRegistry

	fallbackModels = []ModelInfo{
		{ID: "gemini-3.1-pro-high", DisplayName: "Gemini 3.1 Pro (High)", Description: "🧠 По умолчанию: флагман, глубокий рефакторинг, архитектура, сложные алгоритмы"},
		{ID: "gemini-3.1-pro-low", DisplayName: "Gemini 3.1 Pro (Low)", Description: "🧠 Gemini 3.1 Pro: быстрый режим для средних задач"},
		{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash (High)", Description: "⚡ Gemini 3.8 Flash с глубоким рассуждением (High effort)"},
		{ID: "gemini-3.8-flash-medium", DisplayName: "Gemini 3.8 Flash (Medium)", Description: "⚡ Максимальная скорость и свежая база"},
		{ID: "gemini-3.8-flash-low", DisplayName: "Gemini 3.8 Flash (Low)", Description: "⚡ Gemini 3.8 Flash в ультрабыстром режиме (Low effort)"},
		{ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6 (Thinking)", Description: "🎯 Claude Sonnet 4.6 (Thinking): сильный агентный кодинг с пошаговым рассуждением"},
		{ID: "claude-opus-4-6-thinking", DisplayName: "Claude Opus 4.6 (Thinking)", Description: "👑 Claude Opus 4.6 (Thinking): максимальный уровень рассуждений для сложных багов"},
		{ID: "gpt-oss-120b-medium", DisplayName: "GPT-OSS 120B (Medium)", Description: "🌐 GPT-OSS 120B (Medium): открытая весовая архитектура"},
		{ID: "gemini-3.7-flash-high", DisplayName: "Gemini 3.7 Flash (High)", Description: "⚡ Gemini 3.7 Flash с повышенным рассуждением"},
		{ID: "gemini-3.7-flash-medium", DisplayName: "Gemini 3.7 Flash (Medium)", Description: "⚡ Предыдущая быстрая версия"},
		{ID: "gemini-3.7-flash-low", DisplayName: "Gemini 3.7 Flash (Low)", Description: "⚡ Gemini 3.7 Flash в ультрабыстром режиме"},
		{ID: "gemini-3.6-flash-high", DisplayName: "Gemini 3.6 Flash (High)", Description: "⚡ Gemini 3.6 Flash с повышенным рассуждением"},
		{ID: "gemini-3.6-flash-medium", DisplayName: "Gemini 3.6 Flash (Medium)", Description: "⚡ Базовая быстрая модель"},
		{ID: "gemini-3.6-flash-low", DisplayName: "Gemini 3.6 Flash (Low)", Description: "⚡ Gemini 3.6 Flash в ультрабыстром режиме"},
	}

	knownDescriptions = map[string]string{
		"gemini-3.8-flash-medium":  "⚡ Максимальная скорость и свежая база",
		"gemini-3.8-flash-high":    "⚡ Максимальная точность Flash с повышенным рассуждением",
		"gemini-3.8-flash-low":     "⚡ Ультрабыстрый режим Flash с минимальной задержкой",
		"gemini-3.8-flash":         "⚡ Максимальная скорость и свежая база",
		"gemini-3.7-flash-medium":  "⚡ Предыдущая быстрая версия",
		"gemini-3.7-flash-high":    "⚡ Gemini 3.7 Flash с повышенным рассуждением",
		"gemini-3.7-flash-low":     "⚡ Gemini 3.7 Flash в ультрабыстром режиме",
		"gemini-3.7-flash":         "⚡ Предыдущая быстрая версия",
		"gemini-3.6-flash-medium":  "⚡ Базовая быстрая модель",
		"gemini-3.6-flash-high":    "⚡ Gemini 3.6 Flash с повышенным рассуждением",
		"gemini-3.6-flash-low":     "⚡ Gemini 3.6 Flash в ультрабыстром режиме",
		"gemini-3.6-flash":         "⚡ Базовая быстрая модель",
		"gemini-3.1-pro-high":      "🧠 По умолчанию: флагман, глубокий рефакторинг, архитектура, сложные алгоритмы",
		"gemini-3.1-pro-low":       "🧠 Gemini 3.1 Pro с быстрым рассуждением",
		"gemini-3.1-pro":           "🧠 По умолчанию: флагман, глубокий рефакторинг, архитектура, сложные алгоритмы",
		"claude-sonnet-4-6":        "🎯 Claude Sonnet 4.6 (Thinking): сильный агентный кодинг с пошаговым рассуждением",
		"claude-opus-4-6-thinking": "👑 Claude Opus 4.6 (Thinking): максимальный уровень рассуждений для сложных багов",
		"gpt-oss-120b-medium":      "🌐 GPT-OSS 120B (Medium): открытая весовая архитектура",
	}

	modelOrder = map[string]int{
		"gemini-3.1-pro-high":      1,
		"gemini-3.1-pro":           2,
		"gemini-3.1-pro-low":       3,
		"gemini-3.8-flash-medium":  4,
		"gemini-3.8-flash":         5,
		"gemini-3.8-flash-high":    6,
		"gemini-3.8-flash-low":     7,
		"claude-sonnet-4-6":        8,
		"claude-opus-4-6-thinking": 9,
		"gpt-oss-120b-medium":      10,
		"gemini-3.7-flash-medium":  11,
		"gemini-3.7-flash-high":    12,
		"gemini-3.7-flash-low":     13,
		"gemini-3.7-flash":         14,
		"gemini-3.6-flash-medium":  15,
		"gemini-3.6-flash-high":    16,
		"gemini-3.6-flash-low":     17,
		"gemini-3.6-flash":         18,
	}

	baseAliases = map[string]string{
		// Default
		"default": "gemini-3.1-pro-high",

		// Flash 3.8
		"flash":            "gemini-3.8-flash-medium",
		"3.8":              "gemini-3.8-flash-medium",
		"3.8-flash":        "gemini-3.8-flash-medium",
		"gemini-3.8-flash": "gemini-3.8-flash-medium",
		"flash-high":       "gemini-3.8-flash-high",
		"3.8-high":         "gemini-3.8-flash-high",
		"flash-low":        "gemini-3.8-flash-low",
		"3.8-low":          "gemini-3.8-flash-low",

		// Flash 3.7
		"3.7":              "gemini-3.7-flash-medium",
		"3.7-flash":        "gemini-3.7-flash-medium",
		"gemini-3.7-flash": "gemini-3.7-flash-medium",
		"3.7-high":         "gemini-3.7-flash-high",
		"3.7-low":          "gemini-3.7-flash-low",

		// Flash 3.6
		"3.6":              "gemini-3.6-flash-medium",
		"3.6-flash":        "gemini-3.6-flash-medium",
		"gemini-3.6-flash": "gemini-3.6-flash-medium",
		"3.6-high":         "gemini-3.6-flash-high",
		"3.6-low":          "gemini-3.6-flash-low",

		// Pro 3.1
		"pro":            "gemini-3.1-pro-high",
		"3.1":            "gemini-3.1-pro-high",
		"3.1-pro":        "gemini-3.1-pro-high",
		"gemini-3.1-pro": "gemini-3.1-pro-high",
		"pro-low":        "gemini-3.1-pro-low",
		"3.1-low":        "gemini-3.1-pro-low",

		// Claude Sonnet
		"sonnet":            "claude-sonnet-4-6",
		"claude-sonnet":     "claude-sonnet-4-6",
		"sonnet-thinking":   "claude-sonnet-4-6",
		"claude-sonnet-4.6": "claude-sonnet-4-6",
		"claude-sonnet-4-6": "claude-sonnet-4-6",

		// Claude Opus
		"opus":                     "claude-opus-4-6-thinking",
		"claude-opus":              "claude-opus-4-6-thinking",
		"opus-thinking":            "claude-opus-4-6-thinking",
		"claude-opus-4.6":          "claude-opus-4-6-thinking",
		"claude-opus-4-6-thinking": "claude-opus-4-6-thinking",

		// GPT-OSS
		"oss":                 "gpt-oss-120b-medium",
		"gpt-oss":             "gpt-oss-120b-medium",
		"120b":                "gpt-oss-120b-medium",
		"gpt-oss-120b":        "gpt-oss-120b-medium",
		"gpt-oss-120b-medium": "gpt-oss-120b-medium",
	}
)

func NewModelRegistry(cacheTTL time.Duration) *ModelRegistry {
	aliases := make(map[string]string)
	for k, v := range baseAliases {
		aliases[k] = v
	}

	reg := &ModelRegistry{
		aliases:    aliases,
		modelsByID: make(map[string]ModelInfo),
		cacheTTL:   cacheTTL,
	}

	reg.setModels(fallbackModels)

	go func() {
		if _, err := reg.RefreshModels(true); err != nil {
			log.Printf("Предупреждение: начальная синхронизация моделей agy: %v", err)
		}
	}()

	return reg
}

func (m *ModelRegistry) setModels(items []ModelInfo) {
	m.Lock()
	defer m.Unlock()

	sorted := make([]ModelInfo, len(items))
	copy(sorted, items)
	sortModels(sorted)

	m.models = sorted
	m.modelsByID = make(map[string]ModelInfo, len(sorted))
	for _, item := range sorted {
		m.modelsByID[item.ID] = item
	}
	m.lastFetched = time.Now()
}

func parseAgyModelsOutput(raw string) []ModelInfo {
	var items []ModelInfo
	seen := make(map[string]bool)

	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "Fetching") || strings.HasPrefix(line, "Usage:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		id := fields[0]
		if seen[id] {
			continue
		}
		seen[id] = true

		displayName := id
		if len(fields) > 1 {
			displayName = strings.TrimSpace(line[len(id):])
		}

		desc := knownDescriptions[id]
		if desc == "" {
			desc = fmt.Sprintf("✨ %s", displayName)
		}

		items = append(items, ModelInfo{
			ID:          id,
			DisplayName: displayName,
			Description: desc,
		})
	}
	return items
}

func sortModels(items []ModelInfo) {
	sort.SliceStable(items, func(i, j int) bool {
		orderI, okI := modelOrder[items[i].ID]
		if !okI {
			orderI = 1000
		}
		orderJ, okJ := modelOrder[items[j].ID]
		if !okJ {
			orderJ = 1000
		}
		if orderI != orderJ {
			return orderI < orderJ
		}
		return items[i].ID < items[j].ID
	})
}

func (m *ModelRegistry) RefreshModels(force bool) ([]ModelInfo, error) {
	m.RLock()
	if !force && time.Since(m.lastFetched) < m.cacheTTL && len(m.models) > 0 {
		cached := m.models
		m.RUnlock()
		return cached, nil
	}
	m.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var out []byte
	var err error
	if Agent != nil {
		out, err = Agent.GetModels(ctx)
	} else {
		err = fmt.Errorf("Agent is not configured")
	}

	if err != nil {
		m.RLock()
		cached := m.models
		m.RUnlock()
		return cached, fmt.Errorf("вызов получения моделей завершился с ошибкой: %w", err)
	}

	cleanOut := utils.AnsiRegex.ReplaceAllString(string(out), "")
	parsed := parseAgyModelsOutput(cleanOut)
	if len(parsed) == 0 {
		m.RLock()
		cached := m.models
		m.RUnlock()
		return cached, fmt.Errorf("agy models вернул пустой список")
	}

	m.setModels(parsed)
	return parsed, nil
}

func (m *ModelRegistry) GetModels() []ModelInfo {
	m.RLock()
	expired := time.Since(m.lastFetched) >= m.cacheTTL
	models := m.models
	m.RUnlock()

	if expired {
		go func() {
			_, _ = m.RefreshModels(true)
		}()
	}

	return models
}

func (m *ModelRegistry) ResolveModel(input string) (string, bool) {
	target := strings.ToLower(strings.TrimSpace(input))
	if target == "" {
		return "", false
	}

	// 1. Проверяем алиасы
	if canonical, ok := m.aliases[target]; ok {
		return canonical, true
	}

	// 2. Проверяем точное или case-insensitive совпадение среди моделей в кэше
	m.RLock()
	for _, item := range m.models {
		if strings.EqualFold(item.ID, target) {
			m.RUnlock()
			return item.ID, true
		}
	}
	m.RUnlock()

	// 3. Если модель не найдена, пробуем обновить кэш (модель могла появиться только что)
	updated, err := m.RefreshModels(false)
	if err == nil {
		for _, item := range updated {
			if strings.EqualFold(item.ID, target) {
				return item.ID, true
			}
		}
	}

	return "", false
}

func (m *ModelRegistry) IsActive(modelID, currentModel string) bool {
	if strings.EqualFold(modelID, currentModel) {
		return true
	}
	resolvedCurrent, ok := m.ResolveModel(currentModel)
	if ok && strings.EqualFold(modelID, resolvedCurrent) {
		return true
	}
	resolvedModel, ok := m.ResolveModel(modelID)
	if ok && strings.EqualFold(resolvedModel, currentModel) {
		return true
	}
	return false
}

func (m *ModelRegistry) FormatModelsMessage(currentModel string) string {
	models := m.GetModels()
	var bldr strings.Builder
	bldr.WriteString("🤖 <b>Доступные модели:</b>\n\n")

	for _, mod := range models {
		if m.IsActive(mod.ID, currentModel) {
			bldr.WriteString(fmt.Sprintf("👉 <b>%s</b> <i>(активна)</i>\n%s\n\n", html.EscapeString(mod.ID), html.EscapeString(mod.Description)))
		} else {
			bldr.WriteString(fmt.Sprintf("• <code>%s</code>\n%s\n<i>Переключить:</i> <code>/model %s</code>\n\n",
				html.EscapeString(mod.ID), html.EscapeString(mod.Description), html.EscapeString(mod.ID)))
		}
	}

	bldr.WriteString("💡 <i>Короткие алиасы:</i>\n")
	bldr.WriteString("• <code>/model flash</code> — Gemini 3.8 Flash\n")
	bldr.WriteString("• <code>/model pro</code> — Gemini 3.1 Pro\n")
	bldr.WriteString("• <code>/model sonnet</code> — Claude Sonnet 4.6 Thinking\n")
	bldr.WriteString("• <code>/model opus</code> — Claude Opus 4.6 Thinking\n")
	bldr.WriteString("• <code>/model oss</code> — GPT-OSS 120B\n\n")
	bldr.WriteString("🔄 <i>Список моделей синхронизируется динамически с agy (обновить: <code>/models refresh</code>)</i>")

	return bldr.String()
}

func BuildAgyModelArgs(modelName string) []string {
	args := []string{"--model", modelName}

	// Модели Claude не поддерживают флаг --effort
	if strings.Contains(modelName, "claude") {
		return args
	}

	// Если у модели уже указан уровень рассуждений в ID (-high, -medium, -low, -thinking)
	if strings.HasSuffix(modelName, "-high") ||
		strings.HasSuffix(modelName, "-medium") ||
		strings.HasSuffix(modelName, "-low") ||
		strings.HasSuffix(modelName, "-thinking") {
		return args
	}

	// Для базовых Gemini моделей без суффикса effort
	if strings.Contains(modelName, "pro") {
		args = append(args, "--effort", "high")
	} else if strings.Contains(modelName, "gemini") {
		args = append(args, "--effort", "medium")
	}

	return args
}
