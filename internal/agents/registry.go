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

	"bro-bot/internal/i18n"
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
// Error() — служебный текст для логов; пользователю обработчик показывает перевод,
// собранный из полей Agent и Vars.
type MissingAPIKeyError struct {
	Agent string
	Vars  []string
}

func (e *MissingAPIKeyError) Error() string {
	return fmt.Sprintf("agents: set %s in .env to run %s in api mode", strings.Join(e.Vars, " or "), e.Agent)
}

// UnknownAgentError — агента с таким именем в реестре нет.
type UnknownAgentError struct {
	Name  string
	Known []string
}

func (e *UnknownAgentError) Error() string {
	return fmt.Sprintf("agents: unknown agent %q, known: %s", e.Name, strings.Join(e.Known, ", "))
}

// UnsupportedModeError — агент есть, но нужного режима у него нет.
type UnsupportedModeError struct {
	Agent string
	Mode  string
}

func (e *UnsupportedModeError) Error() string {
	return fmt.Sprintf("agents: agent %s does not support %s mode", e.Agent, e.Mode)
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
		panic("agents: empty agent name")
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

// Title — название источника для агента и режима; для неизвестного агента — обобщённое
// название на языке lang.
func (r *Registry) Title(name, mode, lang string) string {
	if spec, ok := r.Lookup(name); ok {
		return spec.Title(mode)
	}
	if NormalizeMode(mode) == ModeAPI {
		return i18n.T(lang, "agent.source_api")
	}
	return i18n.T(lang, "agent.source_cli")
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
			return nil, &UnsupportedModeError{Agent: spec.Name, Mode: ModeAPI}
		}
		return spec.NewAPI(), nil
	}

	if spec.NewCLI == nil {
		return nil, &UnsupportedModeError{Agent: spec.Name, Mode: ModeCLI}
	}
	return spec.NewCLI(), nil
}

func canonical(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// FirstEnv возвращает значение первой заданной переменной из списка, обрезав пробелы.
// Общий помощник нужен, чтобы ключ проверялся одинаково везде: раньше реестр обрезал
// пробелы, а адаптер agy — нет, и ключ из одних пробелов проходил все проверки,
// чтобы упасть уже на запросе к API.
func FirstEnv(vars ...string) string {
	for _, v := range vars {
		if value := strings.TrimSpace(os.Getenv(v)); value != "" {
			return value
		}
	}
	return ""
}

func anyEnvSet(vars []string) bool {
	if len(vars) == 0 {
		return true
	}
	return FirstEnv(vars...) != ""
}
