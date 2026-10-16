package handlers

import (
	"strings"
	"testing"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/config"
)

// runModelCommand выполняет /model с аргументами и возвращает ответ бота.
func runModelCommand(t *testing.T, mt *mockTransport, args ...string) *mock.SentMessage {
	t.Helper()
	handler, ok := mt.commands["model"]
	if !ok {
		t.Fatal("команда /model не зарегистрирована")
	}
	if err := handler(adminSession(mt, &mock.Session{ArgsVal: args})); err != nil {
		t.Fatalf("/model %v: %v", args, err)
	}
	last := mt.LastSent()
	if last == nil {
		t.Fatalf("/model %v ничего не ответил", args)
	}
	return last
}

func currentModel() string {
	config.ProjectState.RLock()
	defer config.ProjectState.RUnlock()
	return config.ProjectState.CurrentModel
}

// Один и тот же псевдоним разрешается в наборе активного агента.
func TestModelCommandResolvesAliasForActiveAgent(t *testing.T) {
	mt := setupTestApp(t)

	tests := []struct {
		agent    string
		alias    string
		expected string
	}{
		{"agy", "flash", "gemini-3.8-flash-medium"},
		{"agy", "opus", "claude-opus-5-5"},
		{"agy", "sonnet", "claude-sonnet-5-5"},
		{"claude", "opus", "claude-opus-5-5"},
		{"claude", "sonnet", "claude-sonnet-5-5"},
		{"claude", "haiku", "claude-haiku-5-5"},
		{"claude", "claude-opus-4.6", "claude-opus-4-6"},
	}
	for _, tc := range tests {
		if _, err := SwitchActiveAgent(tc.agent); err != nil {
			t.Fatalf("SwitchActiveAgent(%s): %v", tc.agent, err)
		}
		runModelCommand(t, mt, tc.alias)
		if got := currentModel(); got != tc.expected {
			t.Errorf("%s: /model %s → %q, ожидали %q", tc.agent, tc.alias, got, tc.expected)
		}
	}
}

// Псевдоним другого агента не применяется, а ответ подсказывает, чей он.
func TestModelCommandRejectsAliasOfOtherAgent(t *testing.T) {
	mt := setupTestApp(t)

	tests := []struct {
		active string
		alias  string
		owner  string
	}{
		{"agy", "haiku", "claude"},
		{"claude", "flash", "agy"},
		{"claude", "oss", "agy"},
	}
	for _, tc := range tests {
		if _, err := SwitchActiveAgent(tc.active); err != nil {
			t.Fatalf("SwitchActiveAgent(%s): %v", tc.active, err)
		}
		before := currentModel()

		msg := runModelCommand(t, mt, tc.alias)
		if got := currentModel(); got != before {
			t.Errorf("%s: /model %s сменил модель на %q", tc.active, tc.alias, got)
		}
		if !strings.Contains(msg.Text, "/agent "+tc.owner) {
			t.Errorf("%s: /model %s должен подсказать агента %s, ответ: %s", tc.active, tc.alias, tc.owner, msg.Text)
		}
	}
}

// /model без аргументов выводит список доступных моделей, активную модель, алиасы и подсказку /model refresh.
func TestModelCommandWithoutArgsListsModels(t *testing.T) {
	mt := setupTestApp(t)

	if _, exists := mt.commands["models"]; exists {
		t.Fatal("команда /models не должна быть зарегистрирована после объединения в /model")
	}

	if _, err := SwitchActiveAgent("agy"); err != nil {
		t.Fatalf("SwitchActiveAgent(agy): %v", err)
	}
	runModelCommand(t, mt, "flash")

	msg := runModelCommand(t, mt)
	if !strings.Contains(msg.Text, "Available models:") {
		t.Errorf("/model без аргументов должен выводить заголовок списка моделей, ответ: %s", msg.Text)
	}
	if !strings.Contains(msg.Text, "👉 <b>gemini-3.8-flash-medium</b> <i>(active)</i>") {
		t.Errorf("/model без аргументов должен отмечать активную модель, ответ: %s", msg.Text)
	}
	if !strings.Contains(msg.Text, "/model flash") {
		t.Errorf("/model без аргументов должен выводить короткие алиасы, ответ: %s", msg.Text)
	}
	if !strings.Contains(msg.Text, "/model refresh") {
		t.Errorf("/model без аргументов должен содержать подсказку /model refresh, ответ: %s", msg.Text)
	}
}

// При передаче неизвестной модели /model подсказывает посмотреть список через /model.
func TestModelCommandUnknownSuggestsModelList(t *testing.T) {
	mt := setupTestApp(t)

	before := currentModel()
	msg := runModelCommand(t, mt, "unknown-model-xyz")
	if got := currentModel(); got != before {
		t.Errorf("/model unknown-model-xyz сменил модель на %q", got)
	}
	if !strings.Contains(msg.Text, "List: /model") || strings.Contains(msg.Text, "List: /models") {
		t.Errorf("ожидали подсказку 'List: /model', ответ: %s", msg.Text)
	}
}

