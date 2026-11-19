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

// agentMu защищает активного агента и его псевдонимы: их меняют команды /agent и /mode,
// а читают фоновое обновление списка моделей и разрешение псевдонимов.
var (
	agentMu      sync.RWMutex
	agent        ports.AgentFramework
	agentAliases Aliases
)

// SetAgent задаёт агента, у которого реестр запрашивает список моделей, и его
// псевдонимы моделей: ResolveModel разрешает имена в контексте этого агента.
func SetAgent(a ports.AgentFramework, aliases Aliases) {
	agentMu.Lock()
	changed := agent != a
	agent = a
	agentAliases = aliases
	reg := GlobalModelRegistry
	if changed && reg != nil {
		reg.setModels(fallbackModels)
		reg.Lock()
		reg.lastFetched = time.Time{}
		reg.Unlock()
	}
	agentMu.Unlock()
}

// CurrentAgent возвращает текущего агента реестра моделей.
func CurrentAgent() ports.AgentFramework {
	agentMu.RLock()
	defer agentMu.RUnlock()
	return agent
}

// ActiveAliases возвращает псевдонимы моделей текущего агента.
func ActiveAliases() Aliases {
	agentMu.RLock()
	defer agentMu.RUnlock()
	return agentAliases
}

type ModelRegistry struct {
	sync.RWMutex
	models      []ModelInfo
	modelsByID  map[string]ModelInfo
	lastFetched time.Time
	cacheTTL    time.Duration
}

var (
	GlobalModelRegistry *ModelRegistry

	fallbackModels = []ModelInfo{
		{ID: "gemini-pro-orchestrator-neon", DisplayName: "Orchestrator (Neon)"},
		{ID: "gemini-pro-orchestrator-g1-pro", DisplayName: "Orchestrator (Argon)"},
		{ID: "gemini-4-neon-medium", DisplayName: "Gemini 4 Neon (Medium)"},
		{ID: "gemini-4-neon-high", DisplayName: "Gemini 4 Neon (High)"},
		{ID: "gemini-4-neon-xhigh", DisplayName: "Gemini 4 Neon (XHigh)"},
		{ID: "gemini-4-neon-low", DisplayName: "Gemini 4 Neon (Low)"},
		{ID: "gemini-4-argon-medium", DisplayName: "Gemini 4 Argon (Medium)"},
		{ID: "gemini-4-argon-high", DisplayName: "Gemini 4 Argon (High)"},
		{ID: "gemini-4-argon-low", DisplayName: "Gemini 4 Argon (Low)"},
		{ID: "gemini-4-hydrogen-medium", DisplayName: "Gemini 4 Hydrogen (Medium)"},
		{ID: "gemini-4-hydrogen-high", DisplayName: "Gemini 4 Hydrogen (High)"},
		{ID: "gemini-4-hydrogen-xhigh", DisplayName: "Gemini 4 Hydrogen (XHigh)"},
		{ID: "gemini-4-hydrogen-low", DisplayName: "Gemini 4 Hydrogen (Low)"},
		{ID: "gemini-3.1-pro-high", DisplayName: "Gemini 3.1 Pro (High)", DescriptionKey: "models.desc_gemini_pro_high"},
		{ID: "gemini-3.1-pro-low", DisplayName: "Gemini 3.1 Pro (Low)", DescriptionKey: "models.desc_gemini_pro_low"},
		{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash (High)", DescriptionKey: "models.desc_flash_38_high"},
		{ID: "gemini-3.8-flash-medium", DisplayName: "Gemini 3.8 Flash (Medium)", DescriptionKey: "models.desc_flash_38_medium"},
		{ID: "gemini-3.8-flash-low", DisplayName: "Gemini 3.8 Flash (Low)", DescriptionKey: "models.desc_flash_38_low"},
		{ID: "claude-sonnet-5-5-high", DisplayName: "Claude Sonnet 5.5 (High)", DescriptionKey: "models.desc_claude_sonnet_55"},
		{ID: "claude-sonnet-5-5-medium", DisplayName: "Claude Sonnet 5.5 (Medium)", DescriptionKey: "models.desc_claude_sonnet_55"},
		{ID: "claude-sonnet-5-5-low", DisplayName: "Claude Sonnet 5.5 (Low)", DescriptionKey: "models.desc_claude_sonnet_55"},
		{ID: "claude-opus-5-5-high", DisplayName: "Claude Opus 5.5 (High)", DescriptionKey: "models.desc_claude_opus_55"},
		{ID: "claude-opus-5-5-medium", DisplayName: "Claude Opus 5.5 (Medium)", DescriptionKey: "models.desc_claude_opus_55"},
		{ID: "claude-opus-5-5-low", DisplayName: "Claude Opus 5.5 (Low)", DescriptionKey: "models.desc_claude_opus_55"},
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
		"claude-opus-5-5":          "models.desc_claude_opus_55",
		"claude-opus-5-5-high":     "models.desc_claude_opus_55",
		"claude-opus-5-5-medium":   "models.desc_claude_opus_55",
		"claude-opus-5-5-low":      "models.desc_claude_opus_55",
		"claude-sonnet-5-5":        "models.desc_claude_sonnet_55",
		"claude-sonnet-5-5-high":   "models.desc_claude_sonnet_55",
		"claude-sonnet-5-5-medium": "models.desc_claude_sonnet_55",
		"claude-sonnet-5-5-low":    "models.desc_claude_sonnet_55",
		"claude-haiku-5-5":         "models.desc_claude_haiku_55",
		"claude-sonnet-5":          "models.desc_claude_sonnet_5",
		"claude-sonnet-4-6":        "models.desc_claude_sonnet_46",
		"claude-opus-4-6-thinking": "models.desc_claude_opus_46",
		"claude-haiku-4-5":         "models.desc_claude_haiku_45",
		"gpt-oss-120b-medium":      "models.desc_gpt_oss_120b",
	}

	modelOrder = map[string]int{
		"gemini-pro-orchestrator-neon":   1,
		"gemini-pro-orchestrator-g1-pro": 2,
		"gemini-4-neon-medium":           3,
		"gemini-4-neon-high":             4,
		"gemini-4-neon-xhigh":            5,
		"gemini-4-neon-low":              6,
		"gemini-4-argon-medium":          7,
		"gemini-4-argon-high":            8,
		"gemini-4-argon-low":             9,
		"gemini-4-hydrogen-medium":       10,
		"gemini-4-hydrogen-high":         11,
		"gemini-4-hydrogen-xhigh":        12,
		"gemini-4-hydrogen-low":          13,
		"gemini-3.1-pro-high":            14,
		"gemini-3.1-pro":                 15,
		"gemini-3.1-pro-low":             16,
		"gemini-3.8-flash-medium":        17,
		"gemini-3.8-flash":               18,
		"gemini-3.8-flash-high":          19,
		"gemini-3.8-flash-low":           20,
		"claude-opus-5-5":                21,
		"claude-opus-5-5-high":           22,
		"claude-opus-5-5-medium":         23,
		"claude-opus-5-5-low":            24,
		"claude-sonnet-5-5":              25,
		"claude-sonnet-5-5-high":         26,
		"claude-sonnet-5-5-medium":       27,
		"claude-sonnet-5-5-low":          28,
		"claude-haiku-5-5":               29,
		"claude-sonnet-5":                30,
		"claude-sonnet-4-6":              31,
		"claude-opus-4-6-thinking":       32,
		"claude-haiku-4-5":               33,
		"gpt-oss-120b-medium":            34,
		"gemini-3.7-flash-medium":        35,
		"gemini-3.7-flash-high":          36,
		"gemini-3.7-flash-low":           37,
		"gemini-3.7-flash":               38,
		"gemini-3.6-flash-medium":        39,
		"gemini-3.6-flash-high":          40,
		"gemini-3.6-flash-low":           41,
		"gemini-3.6-flash":               42,
	}
)

func NewModelRegistry(cacheTTL time.Duration) *ModelRegistry {
	reg := &ModelRegistry{
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
	currentAgent := CurrentAgent()
	if currentAgent != nil {
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

	agentMu.RLock()
	if agent != currentAgent {
		agentMu.RUnlock()
		m.RLock()
		cached := m.models
		m.RUnlock()
		return cached, nil
	}
	m.setModels(parsed)
	agentMu.RUnlock()

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

// ResolveModel разрешает имя модели для активного агента: псевдонимы берутся из его
// набора (SetAgent), поэтому, например, haiku известен claude, но не agy.
func (m *ModelRegistry) ResolveModel(input string) (string, bool) {
	return m.ResolveModelWith(ActiveAliases(), input)
}

// ResolveModelWith разрешает имя модели по заданному набору псевдонимов, а затем
// по каталогу моделей активного агента.
func (m *ModelRegistry) ResolveModelWith(aliases Aliases, input string) (string, bool) {
	target := strings.ToLower(strings.TrimSpace(input))
	if target == "" {
		return "", false
	}

	// 1. Проверяем псевдонимы агента
	if canonical, ok := aliases.Resolve(target); ok {
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

	if hints := ActiveAliases().Hints(); len(hints) > 0 {
		bldr.WriteString(i18n.T(lang, "models.aliases_header"))
		for _, hint := range hints {
			bldr.WriteString(i18n.Tf(lang, "models.alias_hint",
				html.EscapeString(hint.Alias), html.EscapeString(hint.Title)))
		}
		bldr.WriteString("\n")
	}
	bldr.WriteString(i18n.T(lang, "models.footer"))

	return bldr.String()
}
