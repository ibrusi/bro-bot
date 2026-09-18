// Package agents описывает, какие агенты умеет запускать бот и как собрать адаптер
// для каждого из них в каждом режиме выполнения.
//
// Реестр заполняется в корне композиции (cmd/bot/main.go) спецификациями из пакетов
// адаптеров; обработчики знают агентов только через него. Добавить нового агента —
// значит описать одну Spec и зарегистрировать её, не трогая обработчики.
package agents

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"bro-bot/internal/ports"
)

// Режимы выполнения агента.
const (
	ModeCLI = "cli"
	ModeAPI = "api"
)

// NormalizeMode приводит режим к каноническому виду: всё, что не api, считается cli.
func NormalizeMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), ModeAPI) {
		return ModeAPI
	}
	return ModeCLI
}

// Spec описывает одного агента.
type Spec struct {
	// Name — каноническое имя в нижнем регистре, по которому агента выбирают командой /agent.
	Name string
	// CLITitle и APITitle — как называть источник данных в отчётах (/usage) в каждом режиме.
	CLITitle string
	APITitle string
	// APIKeyEnv — переменные окружения, любая из которых даёт ключ для api-режима.
	APIKeyEnv []string
	// DefaultModel — модель, на которую переключается бот, если текущая агенту не подходит.
	DefaultModel func() string
	// RejectsModel сообщает, что модель принадлежит другому семейству и агент её не запустит.
	RejectsModel func(model string) bool
	// NewCLI и NewAPI собирают адаптер для соответствующего режима.
	NewCLI func() ports.AgentFramework
	NewAPI func() ports.AgentFramework
}

// Title возвращает название источника для режима.
func (s Spec) Title(mode string) string {
	if NormalizeMode(mode) == ModeAPI {
		return s.APITitle
	}
	return s.CLITitle
}

// MissingAPIKeyError — для api-режима не задан ни один из ключей.
// Текст без разметки: обработчик сам решает, как его показать.
type MissingAPIKeyError struct {
	Agent string
	Vars  []string
}

func (e *MissingAPIKeyError) Error() string {
	return fmt.Sprintf("для работы %s в режиме api задайте %s в .env", e.Agent, strings.Join(e.Vars, " или "))
}

// UnknownAgentError — агента с таким именем в реестре нет.
type UnknownAgentError struct {
	Name  string
	Known []string
}

func (e *UnknownAgentError) Error() string {
	return fmt.Sprintf("неизвестный агент: %s. Доступны: %s", e.Name, strings.Join(e.Known, ", "))
}

// Registry — набор известных агентов в порядке регистрации; первый — агент по умолчанию.
type Registry struct {
	mu    sync.RWMutex
	specs map[string]Spec
	order []string
}

// NewRegistry создаёт пустой реестр.
func NewRegistry() *Registry {
	return &Registry{specs: make(map[string]Spec)}
}

// Register добавляет агента. Повторная регистрация с тем же именем заменяет описание,
// но сохраняет место в порядке.
func (r *Registry) Register(spec Spec) {
	name := canonical(spec.Name)
	if name == "" {
		panic("agents: пустое имя агента")
	}
	spec.Name = name

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.specs[name]; !exists {
		r.order = append(r.order, name)
	}
	r.specs[name] = spec
}

// Names возвращает имена агентов в порядке регистрации.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
}

// Default — агент по умолчанию: первый зарегистрированный. Пустая строка, если реестр пуст.
func (r *Registry) Default() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.order) == 0 {
		return ""
	}
	return r.order[0]
}

// Lookup возвращает описание агента по имени (регистр и пробелы не важны).
func (r *Registry) Lookup(name string) (Spec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	spec, ok := r.specs[canonical(name)]
	return spec, ok
}

// Known сообщает, зарегистрирован ли агент.
func (r *Registry) Known(name string) bool {
	_, ok := r.Lookup(name)
	return ok
}

// Normalize приводит имя к каноническому; неизвестное или пустое имя превращается в агента
// по умолчанию.
func (r *Registry) Normalize(name string) string {
	if spec, ok := r.Lookup(name); ok {
		return spec.Name
	}
	return r.Default()
}

// Title — название источника для агента и режима; для неизвестного агента — обобщённое.
func (r *Registry) Title(name, mode string) string {
	if spec, ok := r.Lookup(name); ok {
		return spec.Title(mode)
	}
	if NormalizeMode(mode) == ModeAPI {
		return "API агента"
	}
	return "CLI агента"
}

// Build собирает адаптер агента для режима. В api-режиме сначала проверяет наличие
// ключа: без него адаптер не имеет смысла, и пользователь должен узнать об этом сразу.
func (r *Registry) Build(name, mode string) (ports.AgentFramework, error) {
	spec, ok := r.Lookup(name)
	if !ok {
		return nil, &UnknownAgentError{Name: strings.TrimSpace(name), Known: r.Names()}
	}

	if NormalizeMode(mode) == ModeAPI {
		if !anyEnvSet(spec.APIKeyEnv) {
			return nil, &MissingAPIKeyError{Agent: spec.Name, Vars: spec.APIKeyEnv}
		}
		if spec.NewAPI == nil {
			return nil, fmt.Errorf("агент %s не поддерживает режим api", spec.Name)
		}
		return spec.NewAPI(), nil
	}

	if spec.NewCLI == nil {
		return nil, fmt.Errorf("агент %s не поддерживает режим cli", spec.Name)
	}
	return spec.NewCLI(), nil
}

func canonical(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func anyEnvSet(vars []string) bool {
	for _, v := range vars {
		if strings.TrimSpace(os.Getenv(v)) != "" {
			return true
		}
	}
	return len(vars) == 0
}
