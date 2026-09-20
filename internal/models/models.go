package models

import (
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
	"bufio"
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	// DescriptionKey — ключ каталога i18n для описания известной модели. Пустой,
	// если модель пришла из CLI-агента и своего описания у бота для неё нет:
	// тогда описание собирается из DisplayName.
	DescriptionKey string `json:"description_key"`
}

// Description возвращает описание модели на языке lang.
func (m ModelInfo) Description(lang string) string {
	if m.DescriptionKey != "" {
		return i18n.T(lang, m.DescriptionKey)
	}
	name := m.DisplayName
	if name == "" {
		name = m.ID
	}
	return i18n.Tf(lang, "models.desc_generic", name)
}

// agentMu защищает ссылку на активный агент: её меняют команды /agent и /mode,
// а читает фоновое обновление списка моделей.
var (
	agentMu sync.RWMutex
	agent   ports.AgentFramework
)

// SetAgent задаёт агента, у которого реестр запрашивает список моделей.
func SetAgent(a ports.AgentFramework) {
	agentMu.Lock()
	defer agentMu.Unlock()
	agent = a
}

// CurrentAgent возвращает текущего агента реестра моделей.
func CurrentAgent() ports.AgentFramework {
	agentMu.RLock()
	defer agentMu.RUnlock()
	return agent
}

type ModelRegistry struct {
	sync.RWMutex
	models      []ModelInfo
	modelsByID  map[string]ModelInfo
	aliases     map[string]string
	lastFetched time.Time
	cacheTTL    time.Duration
}

var (
	BaseAliases         = baseAliases
	GlobalModelRegistry *ModelRegistry

	fallbackModels = []ModelInfo{
		{ID: "gemini-3.1-pro-high", DisplayName: "Gemini 3.1 Pro (High)", DescriptionKey: "models.desc_gemini_pro_high"},
		{ID: "gemini-3.1-pro-low", DisplayName: "Gemini 3.1 Pro (Low)", DescriptionKey: "models.desc_gemini_pro_low"},
		{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash (High)", DescriptionKey: "models.desc_flash_38_high"},
		{ID: "gemini-3.8-flash-medium", DisplayName: "Gemini 3.8 Flash (Medium)", DescriptionKey: "models.desc_flash_38_medium"},
		{ID: "gemini-3.8-flash-low", DisplayName: "Gemini 3.8 Flash (Low)", DescriptionKey: "models.desc_flash_38_low"},
		{ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6 (Thinking)", DescriptionKey: "models.desc_claude_sonnet_46"},
		{ID: "claude-opus-4-6-thinking", DisplayName: "Claude Opus 4.6 (Thinking)", DescriptionKey: "models.desc_claude_opus_46"},
		{ID: "gpt-oss-120b-medium", DisplayName: "GPT-OSS 120B (Medium)", DescriptionKey: "models.desc_gpt_oss_120b"},
		{ID: "gemini-3.7-flash-high", DisplayName: "Gemini 3.7 Flash (High)", DescriptionKey: "models.desc_flash_37_high"},
		{ID: "gemini-3.7-flash-medium", DisplayName: "Gemini 3.7 Flash (Medium)", DescriptionKey: "models.desc_flash_37_medium"},
		{ID: "gemini-3.7-flash-low", DisplayName: "Gemini 3.7 Flash (Low)", DescriptionKey: "models.desc_flash_37_low"},
		{ID: "gemini-3.6-flash-high", DisplayName: "Gemini 3.6 Flash (High)", DescriptionKey: "models.desc_flash_36_high"},
		{ID: "gemini-3.6-flash-medium", DisplayName: "Gemini 3.6 Flash (Medium)", DescriptionKey: "models.desc_flash_36_medium"},
		{ID: "gemini-3.6-flash-low", DisplayName: "Gemini 3.6 Flash (Low)", DescriptionKey: "models.desc_flash_36_low"},
	}

	// knownDescriptionKeys — описания моделей, которые бот знает сам: ключи каталога,
	// а не готовый текст, поэтому список моделей переводится вместе с интерфейсом.
	knownDescriptionKeys = map[string]string{
		"gemini-3.8-flash-medium":  "models.desc_flash_38_medium",
		"gemini-3.8-flash-high":    "models.desc_flash_38_high_alt",
		"gemini-3.8-flash-low":     "models.desc_flash_38_low_alt",
		"gemini-3.8-flash":         "models.desc_flash_38_medium",
		"gemini-3.7-flash-medium":  "models.desc_flash_37_medium",
		"gemini-3.7-flash-high":    "models.desc_flash_37_high",
		"gemini-3.7-flash-low":     "models.desc_flash_37_low",
		"gemini-3.7-flash":         "models.desc_flash_37_medium",
		"gemini-3.6-flash-medium":  "models.desc_flash_36_medium",
		"gemini-3.6-flash-high":    "models.desc_flash_36_high",
		"gemini-3.6-flash-low":     "models.desc_flash_36_low",
		"gemini-3.6-flash":         "models.desc_flash_36_medium",
		"gemini-3.1-pro-high":      "models.desc_gemini_pro_high",
		"gemini-3.1-pro-low":       "models.desc_gemini_pro_low_alt",
		"gemini-3.1-pro":           "models.desc_gemini_pro_high",
		"claude-sonnet-5":          "models.desc_claude_sonnet_5",
		"claude-sonnet-4-6":        "models.desc_claude_sonnet_46",
		"claude-opus-4-6-thinking": "models.desc_claude_opus_46",
		"claude-haiku-4-5":         "models.desc_claude_haiku_45",
		"gpt-oss-120b-medium":      "models.desc_gpt_oss_120b",
	}

	modelOrder = map[string]int{
		"gemini-3.1-pro-high":      1,
		"gemini-3.1-pro":           2,
		"gemini-3.1-pro-low":       3,
		"gemini-3.8-flash-medium":  4,
		"gemini-3.8-flash":         5,
		"gemini-3.8-flash-high":    6,
		"gemini-3.8-flash-low":     7,
		"claude-sonnet-5":          8,
		"claude-sonnet-4-6":        9,
		"claude-opus-4-6-thinking": 10,
		"claude-haiku-4-5":         11,
		"gpt-oss-120b-medium":      12,
		"gemini-3.7-flash-medium":  13,
		"gemini-3.7-flash-high":    14,
		"gemini-3.7-flash-low":     15,
		"gemini-3.7-flash":         16,
		"gemini-3.6-flash-medium":  17,
		"gemini-3.6-flash-high":    18,
		"gemini-3.6-flash-low":     19,
		"gemini-3.6-flash":         20,
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
		"sonnet-5":          "claude-sonnet-5",
		"claude-sonnet-5":   "claude-sonnet-5",
		"claude-5":          "claude-sonnet-5",

		// Claude Opus
		"opus":                     "claude-opus-4-6-thinking",
		"claude-opus":              "claude-opus-4-6-thinking",
		"opus-thinking":            "claude-opus-4-6-thinking",
		"claude-opus-4.6":          "claude-opus-4-6-thinking",
		"claude-opus-4-6-thinking": "claude-opus-4-6-thinking",

		// Claude Haiku
		"haiku":            "claude-haiku-4-5",
		"claude-haiku":     "claude-haiku-4-5",
		"claude-haiku-4.5": "claude-haiku-4-5",
		"claude-haiku-4-5": "claude-haiku-4-5",

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
			log.Printf("warning: initial model sync: %v", err)
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

		items = append(items, ModelInfo{
			ID:             id,
			DisplayName:    displayName,
			DescriptionKey: knownDescriptionKeys[id],
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
	if currentAgent := CurrentAgent(); currentAgent != nil {
		out, err = currentAgent.GetModels(ctx)
	} else {
		err = fmt.Errorf("Agent is not configured")
	}

	if err != nil {
		m.RLock()
		cached := m.models
		m.RUnlock()
		return cached, fmt.Errorf("models: fetching the model list failed: %w", err)
	}

	cleanOut := utils.AnsiRegex.ReplaceAllString(string(out), "")
	parsed := parseAgyModelsOutput(cleanOut)
	if len(parsed) == 0 {
		m.RLock()
		cached := m.models
		m.RUnlock()
		return cached, errors.New("models: the model list came back empty")
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

func (m *ModelRegistry) FormatModelsMessage(currentModel, lang string) string {
	models := m.GetModels()
	var bldr strings.Builder
	bldr.WriteString(i18n.T(lang, "models.header"))

	for _, mod := range models {
		desc := html.EscapeString(mod.Description(lang))
		if m.IsActive(mod.ID, currentModel) {
			bldr.WriteString(i18n.Tf(lang, "models.active_item", html.EscapeString(mod.ID), desc))
		} else {
			bldr.WriteString(i18n.Tf(lang, "models.item",
				html.EscapeString(mod.ID), desc, html.EscapeString(mod.ID)))
		}
	}

	hasGemini := false
	for _, mod := range models {
		if strings.Contains(strings.ToLower(mod.ID), "gemini") {
			hasGemini = true
			break
		}
	}

	bldr.WriteString(i18n.T(lang, "models.aliases_header"))
	if hasGemini {
		bldr.WriteString("• <code>/model flash</code> — Gemini 3.8 Flash\n")
		bldr.WriteString("• <code>/model pro</code> — Gemini 3.1 Pro\n")
		bldr.WriteString("• <code>/model sonnet</code> — Claude Sonnet 4.6 Thinking\n")
		bldr.WriteString("• <code>/model opus</code> — Claude Opus 4.6 Thinking\n")
		bldr.WriteString("• <code>/model oss</code> — GPT-OSS 120B\n\n")
	} else {
		bldr.WriteString("• <code>/model sonnet</code> — Claude Sonnet 4.6 Thinking\n")
		bldr.WriteString("• <code>/model opus</code> — Claude Opus 4.6 Thinking\n")
		bldr.WriteString("• <code>/model haiku</code> — Claude Haiku 4.5\n")
		bldr.WriteString("• <code>/model sonnet-5</code> — Claude Sonnet 5\n\n")
	}
	bldr.WriteString(i18n.T(lang, "models.footer"))

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
