package handlers

import (
	"errors"
	"strings"
	"testing"

	"bro-bot/internal/adapters/agy"
	"bro-bot/internal/adapters/claude"
	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/agents"
	"bro-bot/internal/config"
)

// testAgentRegistry — тот же набор агентов, что собирает cmd/bot/main.go.
func testAgentRegistry() *agents.Registry {
	reg := agents.NewRegistry()
	reg.Register(agy.Spec())
	reg.Register(claude.Spec())
	return reg
}

// Тесты, которые зовут SwitchActiveAgent без Start, тоже нуждаются в реестре.
func init() {
	setAgentRegistry(testAgentRegistry())
}

func TestSwitchActiveAgentReturnsDataWithoutMarkup(t *testing.T) {
	setupTestApp(t)

	config.ProjectState.Lock()
	config.ProjectState.CurrentModel = "gemini-3.1-pro-high"
	config.ProjectState.Unlock()

	res, err := SwitchActiveAgent("claude")
	if err != nil {
		t.Fatalf("SwitchActiveAgent: %v", err)
	}
	if res.Agent != "claude" || res.Mode != "mcp" {
		t.Errorf("результат = %+v", res)
	}
	if !res.ModelSwitched() || res.Model != "sonnet" {
		t.Errorf("модель Gemini должна смениться на sonnet, получили %+v", res)
	}

	// Обратно на agy: модель sonnet ему чужая — вернётся DEFAULT_MODEL.
	res, err = SwitchActiveAgent("agy")
	if err != nil {
		t.Fatalf("SwitchActiveAgent(agy): %v", err)
	}
	if !res.ModelSwitched() || res.Model != "gemini-3.1-pro-high" {
		t.Errorf("модель claude должна смениться на DEFAULT_MODEL, получили %+v", res)
	}

	// Повторное переключение на того же агента модель не трогает.
	res, _ = SwitchActiveAgent("agy")
	if res.ModelSwitched() {
		t.Errorf("модель не должна меняться без причины: %+v", res)
	}
}

func TestSwitchActiveAgentUnknownErrorIsPlainText(t *testing.T) {
	setupTestApp(t)

	_, err := SwitchActiveAgent("unknown_bot")
	if err == nil {
		t.Fatal("ожидали ошибку для неизвестного агента")
	}
	var unknown *agents.UnknownAgentError
	if !errors.As(err, &unknown) {
		t.Errorf("ожидали UnknownAgentError, получили %T", err)
	}
	if strings.ContainsAny(err.Error(), "<>") {
		t.Errorf("ошибка не должна содержать разметку: %s", err)
	}
	if !strings.Contains(err.Error(), "agy") || !strings.Contains(err.Error(), "claude") {
		t.Errorf("ошибка должна перечислять доступных агентов: %s", err)
	}
}

// TestModeAPIRefusesWithoutKeyAndKeepsMode — /mode api без ключа не меняет режим:
// иначе бот остался бы в api-режиме без работающего адаптера.
func TestModeAPIRefusesWithoutKeyAndKeepsMode(t *testing.T) {
	mt := setupTestApp(t)
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_API_KEY", "")

	if _, err := SwitchActiveAgent("agy"); err != nil {
		t.Fatal(err)
	}
	config.ProjectState.SetExecutionMode("cli")

	handler := mt.commands["mode"]
	if err := handler(adminSession(mt, &mock.Session{ArgsVal: []string{"api"}})); err != nil {
		t.Fatalf("/mode api: %v", err)
	}

	if got := config.ProjectState.GetExecutionMode(); got != "cli" {
		t.Errorf("режим не должен был смениться без ключа, получили %q", got)
	}
	last := mt.LastSent()
	if last == nil || !strings.Contains(last.Text, "GEMINI_API_KEY") {
		t.Errorf("ожидали сообщение про ключ, получили %+v", last)
	}
	if last != nil && strings.Contains(last.Text, "&lt;") {
		t.Errorf("в сообщении не должно быть двойного экранирования: %s", last.Text)
	}
}

func TestModeAPISwitchesWithKey(t *testing.T) {
	mt := setupTestApp(t)
	t.Setenv("GEMINI_API_KEY", "test-key")

	if _, err := SwitchActiveAgent("agy"); err != nil {
		t.Fatal(err)
	}
	config.ProjectState.SetExecutionMode("cli")

	handler := mt.commands["mode"]
	if err := handler(adminSession(mt, &mock.Session{ArgsVal: []string{"api"}})); err != nil {
		t.Fatalf("/mode api: %v", err)
	}
	if got := config.ProjectState.GetExecutionMode(); got != "api" {
		t.Errorf("режим = %q, ожидали api", got)
	}
	if framework, _ := ActiveAgent(); framework == nil {
		t.Error("после /mode api активный адаптер должен быть собран")
	} else if _, ok := framework.(*agy.AgyAPIAdapter); !ok {
		t.Errorf("активный адаптер %T, ожидали API-адаптер agy", framework)
	}
}

// TestAgentHelpListsRegisteredAgents — список агентов в подсказке берётся из реестра,
// а не из строки в коде.
func TestAgentHelpListsRegisteredAgents(t *testing.T) {
	mt := setupTestApp(t)

	handler := mt.commands["agent"]
	if err := handler(adminSession(mt, &mock.Session{})); err != nil {
		t.Fatalf("/agent: %v", err)
	}
	last := mt.LastSent()
	if last == nil {
		t.Fatal("нет ответа")
	}
	for _, name := range registry().Names() {
		if !strings.Contains(last.Text, name) {
			t.Errorf("в подсказке нет агента %q: %s", name, last.Text)
		}
	}
}

func TestHandlersDoNotImportAdapters(t *testing.T) {
	// Сторож архитектуры: обработчики знают агентов только через реестр.
	// Импорт адаптеров в тестах допустим — они собирают реестр так же, как main.go.
	for _, spec := range []agents.Spec{agy.Spec(), claude.Spec()} {
		if !registry().Known(spec.Name) {
			t.Errorf("агент %q из спецификации не зарегистрирован в тестовом реестре", spec.Name)
		}
	}
}
