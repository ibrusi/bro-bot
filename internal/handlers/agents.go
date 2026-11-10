package handlers

import (
	"context"
	"errors"
	"html"
	"log"
	"strings"
	"sync"

	"bro-bot/internal/agents"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/models"
	"bro-bot/internal/ports"
)

// agentRegistry — реестр агентов, полученный от корня композиции в Start.
// Обработчики не знают конкретных адаптеров: только имена, названия и способ сборки.
var (
	agentRegistryMu sync.RWMutex
	agentRegistry   *agents.Registry
)

// setAgentRegistry запоминает реестр агентов.
func setAgentRegistry(reg *agents.Registry) {
	agentRegistryMu.Lock()
	defer agentRegistryMu.Unlock()
	agentRegistry = reg
}

// registry возвращает реестр агентов; до инициализации — пустой, чтобы вызывающий код
// получал понятную ошибку «неизвестный агент», а не панику.
func registry() *agents.Registry {
	agentRegistryMu.RLock()
	defer agentRegistryMu.RUnlock()
	if agentRegistry == nil {
		return agents.NewRegistry()
	}
	return agentRegistry
}

// adapterMatchesMode проверяет, совместим ли адаптер с целевым режимом выполнения.
// Если адаптер сообщает свой режим через ModeAwareFramework, режим должен строго совпадать.
// Универсальные адаптеры и моки в тестах, не реализующие интерфейс, считаются совместимыми.
func adapterMatchesMode(framework ports.AgentFramework, targetMode string) bool {
	if framework == nil {
		return false
	}
	if aware, ok := framework.(ports.ModeAwareFramework); ok {
		if mode := aware.ExecutionMode(); mode != "" {
			return strings.EqualFold(mode, targetMode)
		}
	}
	return true
}

// agentFrameworkFor возвращает адаптер для указанного агента с учётом текущего режима выполнения.
// Это единая точка выбора адаптера: она одинаково работает для любого агента и режима.
func agentFrameworkFor(agentName string) (ports.AgentFramework, error) {
	activeFramework, activeName := ActiveAgent()
	targetMode := config.ProjectState.GetExecutionMode()

	name := normalizeAgentName(agentName)

	// Активный агент уже собран при переключении /agent и /mode — переиспользуем его,
	// только если совпадают имя агента и режим выполнения адаптера.
	if activeFramework != nil && strings.EqualFold(name, activeName) && adapterMatchesMode(activeFramework, targetMode) {
		return activeFramework, nil
	}
	adapter, err := registry().Build(name, targetMode)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(name, activeName) {
		SetActiveAgent(adapter, name)
	}
	return adapter, nil
}

// normalizeAgentName приводит имя агента к каноническому виду: пустое — активный агент,
// неизвестное — агент по умолчанию из реестра.
func normalizeAgentName(agentName string) string {
	name := strings.TrimSpace(agentName)
	if name == "" {
		name = ActiveAgentName()
	}
	return registry().Normalize(name)
}

// isKnownAgent сообщает, зарегистрирован ли агент с таким именем.
func isKnownAgent(name string) bool {
	return registry().Known(name)
}

// usageSourceTitle называет источник лимитов в шапке /usage. В mcp-режиме это название
// агента с суффиксом (MCP), в api-режиме работа идёт по ключу API — и подписывать её именем
// CLI-продукта было бы неверно.
func usageSourceTitle(agentName, execMode, lang string) string {
	return registry().Title(agentName, execMode, lang)
}

// AgentSwitch — результат переключения агента: данные без разметки, форматирует их
// обработчик команды.
type AgentSwitch struct {
	Agent string
	Mode  string
	// Model заполняется, если текущая модель агенту не подходила и была заменена.
	Model string
	// ModeAdjusted — агент не запускается в прежнем режиме, и бот сам перешёл
	// на режим, который агент поддерживает (для grok это acp).
	ModeAdjusted bool
}

// ModelSwitched сообщает, была ли заменена модель.
func (r AgentSwitch) ModelSwitched() bool {
	return r.Model != ""
}

// SwitchActiveAgent переключает активного агента и сохраняет выбор в базе данных.
// Если агент не запускается в текущем режиме, берётся режим, который он поддерживает,
// и он тоже сохраняется. Явная команда /mode этот запасной путь не использует.
func SwitchActiveAgent(name string) (AgentSwitch, error) {
	return switchActiveAgent(name, config.ProjectState.GetExecutionMode(), true)
}

// switchActiveAgent собирает адаптер агента для режима и делает его активным.
// allowFallback разрешает сменить режим на PreferredMode, когда сборка не удалась
// из-за неподдерживаемого режима. /mode передаёт false: опечатка и чужой режим
// остаются ошибкой, а не молчаливой подменой.
func switchActiveAgent(name, mode string, allowFallback bool) (AgentSwitch, error) {
	reg := registry()
	requested := agents.NormalizeMode(mode)

	spec, ok := reg.Lookup(name)
	if !ok {
		return AgentSwitch{}, &agents.UnknownAgentError{Name: strings.TrimSpace(name), Known: reg.Names()}
	}

	chosen := requested
	adapter, err := reg.Build(spec.Name, chosen)
	if err != nil && allowFallback {
		var unsupported *agents.UnsupportedModeError
		if errors.As(err, &unsupported) {
			if pref, ok := spec.PreferredMode(); ok && pref != chosen {
				if next, buildErr := reg.Build(spec.Name, pref); buildErr == nil {
					adapter = next
					err = nil
					chosen = pref
				}
			}
		}
	}
	if err != nil {
		return AgentSwitch{}, err
	}

	SetActiveAgent(adapter, spec.Name)
	models.SetAgent(adapter, spec.ModelAliases)
	config.ProjectState.SetCurrentAgent(spec.Name)

	result := AgentSwitch{Agent: spec.Name, Mode: chosen, ModeAdjusted: chosen != requested}
	st := domain.GlobalTaskManager.Storage()
	if result.ModeAdjusted {
		config.ProjectState.SetExecutionMode(chosen)
		if st != nil {
			_ = st.SetSetting(context.Background(), "execution_mode", chosen)
		}
	}

	// Модель другого семейства агент не запустит — подменяем на его модель по умолчанию.
	config.ProjectState.Lock()
	if spec.RejectsModel != nil && spec.DefaultModel != nil && spec.RejectsModel(config.ProjectState.CurrentModel) {
		result.Model = spec.DefaultModel()
		config.ProjectState.CurrentModel = result.Model
		if st != nil {
			_ = st.SetSetting(context.Background(), "current_model", result.Model)
		}
	}
	config.ProjectState.Unlock()

	if st != nil {
		_ = st.SetSetting(context.Background(), "current_agent", spec.Name)
	}

	go func() {
		if models.GlobalModelRegistry != nil {
			_, _ = models.GlobalModelRegistry.RefreshModels(true)
		}
	}()

	return result, nil
}

// formatAgentSwitch — сообщение пользователю о переключении агента.
func formatAgentSwitch(r AgentSwitch, lang string) string {
	msg := i18n.Tf(lang, "agent.switched", html.EscapeString(r.Agent), html.EscapeString(r.Mode))
	if r.ModeAdjusted {
		msg += i18n.Tf(lang, "agent.mode_adjusted", html.EscapeString(r.Mode))
	}
	if r.ModelSwitched() {
		msg += i18n.Tf(lang, "agent.model_switched", html.EscapeString(r.Model))
	}
	return msg
}

// initActiveAgent выставляет активного агента при старте: сохранённого в базе либо агента
// по умолчанию. Если для сохранённого режима адаптер не собирается (например, api без
// ключа), бот стартует в mcp-режиме, а не без агента.
func initActiveAgent(savedAgent string) {
	reg := registry()
	name := reg.Normalize(savedAgent)
	if name == "" {
		log.Printf("warning: the agent registry is empty, agent commands will not work")
		return
	}

	if savedAgent != "" {
		_, err := SwitchActiveAgent(savedAgent)
		if err == nil {
			log.Printf("restored the active agent from SQLite: %s", name)
			return
		}
		log.Printf("warning: cannot restore agent %q: %v", savedAgent, err)
	}

	mode := config.ProjectState.GetExecutionMode()
	adapter, err := reg.Build(name, mode)
	if err != nil {
		pref := agents.ModeMCP
		if spec, ok := reg.Lookup(name); ok {
			if p, has := spec.PreferredMode(); has {
				pref = p
			}
		}
		log.Printf("warning: agent %s is unavailable in %s mode (%v), falling back to %s mode", name, mode, err, pref)
		config.ProjectState.SetExecutionMode(pref)
		if st := domain.GlobalTaskManager.Storage(); st != nil {
			_ = st.SetSetting(context.Background(), "execution_mode", pref)
		}
		if adapter, err = reg.Build(name, pref); err != nil {
			log.Printf("warning: agent %s is unavailable: %v", name, err)
			return
		}
	}
	SetActiveAgent(adapter, name)
	models.SetAgent(adapter, modelAliasesOf(name))
	config.ProjectState.SetCurrentAgent(name)
}

// modelAliasesOf возвращает псевдонимы моделей агента; для неизвестного — пустой набор.
func modelAliasesOf(name string) models.Aliases {
	spec, _ := registry().Lookup(name)
	return spec.ModelAliases
}

// aliasOwner ищет агента, у которого есть псевдоним alias, кроме агента except.
// Нужен, чтобы на /model haiku при активном agy подсказать, чей это псевдоним.
func aliasOwner(alias, except string) (agent, model string, ok bool) {
	reg := registry()
	for _, name := range reg.Names() {
		if strings.EqualFold(name, except) {
			continue
		}
		if model, ok := modelAliasesOf(name).Resolve(alias); ok {
			return name, model, true
		}
	}
	return "", "", false
}

// formatAgentNames перечисляет зарегистрированных агентов для подсказок.
func formatAgentNames() string {
	names := registry().Names()
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, "<b>"+html.EscapeString(name)+"</b>")
	}
	return strings.Join(parts, ", ")
}
