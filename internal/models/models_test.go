package models

import (
	"bro-bot/internal/i18n"
	"strings"
	"testing"
	"time"
)

func TestParseAgyModels(t *testing.T) {
	raw := `
Fetching available models...
gemini-3.8-flash-high     Gemini 3.8 Flash (High)
gemini-3.8-flash-medium   Gemini 3.8 Flash (Medium)
gemini-3.8-flash-low      Gemini 3.8 Flash (Low)
claude-sonnet-4-6         Claude Sonnet 4.6 (Thinking)
claude-opus-4-6-thinking  Claude Opus 4.6 (Thinking)
gpt-oss-120b-medium       GPT-OSS 120B (Medium)
custom-llm-v1             Custom LLM V1 Model
`
	items := parseAgyModelsOutput(raw)
	if len(items) != 7 {
		t.Fatalf("expected 7 items, got %d", len(items))
	}

	if items[0].ID != "gemini-3.8-flash-high" || items[0].DisplayName != "Gemini 3.8 Flash (High)" {
		t.Errorf("unexpected first item: %+v", items[0])
	}

	last := items[6]
	if last.ID != "custom-llm-v1" || last.DisplayName != "Custom LLM V1 Model" {
		t.Errorf("unexpected custom item: %+v", last)
	}
	if desc := last.Description(i18n.Default); !strings.Contains(desc, "Custom LLM V1 Model") {
		t.Errorf("expected dynamic description for custom item, got %s", desc)
	}
}

// testAliases — набор псевдонимов, по которому реестр разрешает имена в тестах.
var testAliases = NewAliases(map[string]string{
	"flash":  "gemini-3.8-flash-medium",
	"FLASH2": "gemini-3.8-flash-high",
	"sonnet": "claude-sonnet-5-5",
},
	AliasHint{Alias: "flash", Title: "Gemini 3.8 Flash"},
	AliasHint{Alias: "sonnet", Title: "Claude Sonnet 5.5"},
)

// withActiveAliases делает набор псевдонимов активным на время теста.
func withActiveAliases(t *testing.T, aliases Aliases) {
	t.Helper()
	prevAgent, prevAliases := CurrentAgent(), ActiveAliases()
	SetAgent(nil, aliases)
	t.Cleanup(func() { SetAgent(prevAgent, prevAliases) })
}

func TestAliasesResolveIgnoresCaseAndSpaces(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		found    bool
	}{
		{"flash", "gemini-3.8-flash-medium", true},
		{" Flash ", "gemini-3.8-flash-medium", true},
		{"flash2", "gemini-3.8-flash-high", true},
		{"opus", "", false},
	}
	for _, tc := range tests {
		got, ok := testAliases.Resolve(tc.input)
		if got != tc.expected || ok != tc.found {
			t.Errorf("Resolve(%q) = %q, %v; expected %q, %v", tc.input, got, ok, tc.expected, tc.found)
		}
	}

	var empty Aliases
	if _, ok := empty.Resolve("flash"); ok {
		t.Errorf("пустой набор не должен разрешать псевдонимы")
	}
}

func TestResolveModelWith(t *testing.T) {
	reg := NewModelRegistry(10 * time.Minute)

	tests := []struct {
		input    string
		expected string
		found    bool
	}{
		{"flash", "gemini-3.8-flash-medium", true},
		{"FLASH", "gemini-3.8-flash-medium", true},
		{"sonnet", "claude-sonnet-5-5", true},
		{"gemini-3.8-flash-low", "gemini-3.8-flash-low", true}, // точный ID из каталога
		{"GPT-OSS-120B-MEDIUM", "gpt-oss-120b-medium", true},
		{"opus", "", false},
		{"nonexistent-model-xyz", "", false},
	}

	for _, tc := range tests {
		res, ok := reg.ResolveModelWith(testAliases, tc.input)
		if ok != tc.found {
			t.Errorf("ResolveModelWith(%q) found = %v, expected %v", tc.input, ok, tc.found)
		}
		if res != tc.expected {
			t.Errorf("ResolveModelWith(%q) = %q, expected %q", tc.input, res, tc.expected)
		}
	}
}

// ResolveModel разрешает псевдонимы только в наборе активного агента.
func TestResolveModelUsesActiveAgentAliases(t *testing.T) {
	reg := NewModelRegistry(10 * time.Minute)

	withActiveAliases(t, testAliases)
	if got, ok := reg.ResolveModel("sonnet"); !ok || got != "claude-sonnet-5-5" {
		t.Errorf("ResolveModel(sonnet) = %q, %v; expected claude-sonnet-5-5", got, ok)
	}

	withActiveAliases(t, NewAliases(map[string]string{"sonnet": "claude-sonnet-4-6"}))
	if got, ok := reg.ResolveModel("sonnet"); !ok || got != "claude-sonnet-4-6" {
		t.Errorf("после смены агента ResolveModel(sonnet) = %q, %v; expected claude-sonnet-4-6", got, ok)
	}
	if _, ok := reg.ResolveModel("flash"); ok {
		t.Errorf("псевдоним другого агента не должен разрешаться")
	}
}

func TestFormatModelsMessage(t *testing.T) {
	withActiveAliases(t, testAliases)
	reg := NewModelRegistry(10 * time.Minute)
	msg := reg.FormatModelsMessage("gemini-3.8-flash-medium", i18n.Default)

	if !strings.Contains(msg, "Available models:") {
		t.Errorf("expected header in message")
	}
	if !strings.Contains(msg, "(active)") {
		t.Errorf("expected active marker in message")
	}
	if !strings.Contains(msg, "/model flash") {
		t.Errorf("expected aliases in message")
	}
	if !strings.Contains(msg, "/model refresh") {
		t.Errorf("expected refresh instruction in message")
	}

	// Подсказки берутся из набора активного агента.
	if !strings.Contains(msg, "<code>/model sonnet</code> — Claude Sonnet 5.5") {
		t.Errorf("expected sonnet hint from the active aliases, got:\n%s", msg)
	}

	// Без псевдонимов раздела подсказок нет.
	withActiveAliases(t, Aliases{})
	if msgNoAliases := reg.FormatModelsMessage("gemini-3.8-flash-medium", i18n.Default); strings.Contains(msgNoAliases, "/model flash") {
		t.Errorf("expected no alias hints without aliases")
	}

	// Активная модель отмечается и среди моделей Claude.
	claudeModels := []ModelInfo{
		{ID: "claude-sonnet-5-5", DisplayName: "Claude Sonnet 5.5", DescriptionKey: "models.desc_claude_sonnet_55"},
		{ID: "claude-haiku-5-5", DisplayName: "Claude Haiku 5.5", DescriptionKey: "models.desc_claude_haiku_55"},
	}
	reg.setModels(claudeModels)
	msgClaude := reg.FormatModelsMessage("claude-sonnet-5-5", i18n.Default)
	if !strings.Contains(msgClaude, "👉 <b>claude-sonnet-5-5</b> <i>(active)</i>") {
		t.Errorf("expected active model marker for claude-sonnet-5-5")
	}
}
