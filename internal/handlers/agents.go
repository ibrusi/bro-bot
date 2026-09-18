package handlers

import (
	"context"
	"fmt"
	"html"
	"log"
	"strings"
	"sync"

	"bro-bot/internal/agents"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
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

// agentFrameworkFor возвращает адаптер для указанного агента с учётом текущего режима выполнения.
// Это единая точка выбора адаптера: она одинаково работает для любого агента и режима.
func agentFrameworkFor(agentName string) (ports.AgentFramework, error) {
	// Адаптер и имя берём одной парой: иначе можно сравнить имя с новым агентом,
	// а вернуть адаптер от предыдущего.
	activeFramework, activeName := ActiveAgent()

	name := normalizeAgentName(agentName)

	// Активный агент уже собран при переключении /agent и /mode — переиспользуем его.
	if activeFramework != nil && strings.EqualFold(name, activeName) {
		return activeFramework, nil
	}
	return registry().Build(name, config.ProjectState.GetExecutionMode())
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

// usageSourceTitle называет источник лимитов в шапке /usage. В cli-режиме это название
// CLI-продукта, в api-режиме работа идёт по ключу API — и подписывать её именем
// CLI-продукта было бы неверно.
func usageSourceTitle(agentName, execMode string) string {
	return registry().Title(agentName, execMode)
}

// AgentSwitch — результат переключения агента: данные без разметки, форматирует их
// обработчик команды.
type AgentSwitch struct {
	Agent string
	Mode  string
	// Model заполняется, если текущая модель агенту не подходила и была заменена.
	Model string
}

// ModelSwitched сообщает, была ли заменена модель.
func (r AgentSwitch) ModelSwitched() bool {
	return r.Model != ""
}

// SwitchActiveAgent переключает активного агента в текущем режиме выполнения
// и сохраняет выбор в базе данных.
func SwitchActiveAgent(name string) (AgentSwitch, error) {
	return switchActiveAgent(name, config.ProjectState.GetExecutionMode())
}

// switchActiveAgent собирает адаптер агента для режима и делает его активным.
// Режим выполнения здесь не меняется: /mode сначала убеждается, что адаптер собирается
// (в api-режиме — что есть ключ), и только потом переключает режим.
func switchActiveAgent(name, mode string) (AgentSwitch, error) {
	reg := registry()
	mode = agents.NormalizeMode(mode)

	spec, ok := reg.Lookup(name)
	if !ok {
		return AgentSwitch{}, &agents.UnknownAgentError{Name: strings.TrimSpace(name), Known: reg.Names()}
	}

	adapter, err := reg.Build(spec.Name, mode)
	if err != nil {
		return AgentSwitch{}, err
	}

	SetActiveAgent(adapter, spec.Name)
	models.SetAgent(adapter)
	config.ProjectState.SetCurrentAgent(spec.Name)

	result := AgentSwitch{Agent: spec.Name, Mode: mode}
	st := domain.GlobalTaskManager.Storage()

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
func formatAgentSwitch(r AgentSwitch) string {
	msg := fmt.Sprintf("✅ Агент переключен на: <b>%s</b> [%s]", html.EscapeString(r.Agent), html.EscapeString(r.Mode))
	if r.ModelSwitched() {
		msg += fmt.Sprintf("\nМодель автоматически переключена на <b>%s</b>.", html.EscapeString(r.Model))
	}
	return msg
}

// initActiveAgent выставляет активного агента при старте: сохранённого в базе либо агента
// по умолчанию. Если для сохранённого режима адаптер не собирается (например, api без
// ключа), бот стартует в cli-режиме, а не без агента.
func initActiveAgent(savedAgent string) {
	reg := registry()
	name := reg.Normalize(savedAgent)
	if name == "" {
		log.Printf("Предупреждение: реестр агентов пуст, команды агента работать не будут")
		return
	}

	if savedAgent != "" {
		_, err := SwitchActiveAgent(savedAgent)
		if err == nil {
			log.Printf("Восстановлен активный агент из SQLite: %s", name)
			return
		}
		log.Printf("Предупреждение: не удалось восстановить агента %q: %v", savedAgent, err)
	}

	mode := config.ProjectState.GetExecutionMode()
	adapter, err := reg.Build(name, mode)
	if err != nil {
		log.Printf("Предупреждение: агент %s в режиме %s недоступен (%v), запускаемся в режиме cli", name, mode, err)
		config.ProjectState.SetExecutionMode(agents.ModeCLI)
		if adapter, err = reg.Build(name, agents.ModeCLI); err != nil {
			log.Printf("Предупреждение: агент %s недоступен: %v", name, err)
			return
		}
	}
	SetActiveAgent(adapter, name)
	models.SetAgent(adapter)
	config.ProjectState.SetCurrentAgent(name)
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
