package models

import (
	"bro-bot/internal/i18n"
	"reflect"
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

func TestResolveModel(t *testing.T) {
	reg := NewModelRegistry(10 * time.Minute)

	tests := []struct {
		input    string
		expected string
		found    bool
	}{
		{"flash", "gemini-3.8-flash-medium", true},
		{"FLASH", "gemini-3.8-flash-medium", true},
		{"3.8", "gemini-3.8-flash-medium", true},
		{"pro", "gemini-3.1-pro-high", true},
		{"default", "gemini-3.1-pro-high", true},
		{"sonnet", "claude-sonnet-4-6", true},
		{"claude-sonnet-4.6", "claude-sonnet-4-6", true},
		{"opus", "claude-opus-4-6-thinking", true},
		{"claude-opus-4.6", "claude-opus-4-6-thinking", true},
		{"oss", "gpt-oss-120b-medium", true},
		{"gpt-oss-120b", "gpt-oss-120b-medium", true},
		{"gemini-3.8-flash-low", "gemini-3.8-flash-low", true},
		{"nonexistent-model-xyz", "", false},
	}

	for _, tc := range tests {
		res, ok := reg.ResolveModel(tc.input)
		if ok != tc.found {
			t.Errorf("ResolveModel(%q) found = %v, expected %v", tc.input, ok, tc.found)
		}
		if res != tc.expected {
			t.Errorf("ResolveModel(%q) = %q, expected %q", tc.input, res, tc.expected)
		}
	}
}

func TestBuildAgyModelArgs(t *testing.T) {
	tests := []struct {
		model    string
		expected []string
	}{
		{
			model:    "claude-sonnet-4-6",
			expected: []string{"--model", "claude-sonnet-4-6"},
		},
		{
			model:    "claude-opus-4-6-thinking",
			expected: []string{"--model", "claude-opus-4-6-thinking"},
		},
		{
			model:    "gemini-3.8-flash-medium",
			expected: []string{"--model", "gemini-3.8-flash-medium"},
		},
		{
			model:    "gemini-3.8-flash-high",
			expected: []string{"--model", "gemini-3.8-flash-high"},
		},
		{
			model:    "gemini-3.8-flash",
			expected: []string{"--model", "gemini-3.8-flash", "--effort", "medium"},
		},
		{
			model:    "gemini-3.1-pro",
			expected: []string{"--model", "gemini-3.1-pro", "--effort", "high"},
		},
		{
			model:    "gpt-oss-120b-medium",
			expected: []string{"--model", "gpt-oss-120b-medium"},
		},
		{
			model:    "my-custom-model",
			expected: []string{"--model", "my-custom-model"},
		},
	}

	for _, tc := range tests {
		actual := BuildAgyModelArgs(tc.model)
		if !reflect.DeepEqual(actual, tc.expected) {
			t.Errorf("BuildAgyModelArgs(%q) = %v, expected %v", tc.model, actual, tc.expected)
		}
	}
}

func TestFormatModelsMessage(t *testing.T) {
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
	if !strings.Contains(msg, "/models refresh") {
		t.Errorf("expected refresh instruction in message")
	}

	// Test Claude-only models formatting
	claudeModels := []ModelInfo{
		{ID: "claude-sonnet-5", DisplayName: "Claude Sonnet 5", DescriptionKey: "models.desc_claude_sonnet_5"},
		{ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6", DescriptionKey: "models.desc_claude_sonnet_46"},
		{ID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5", DescriptionKey: "models.desc_claude_haiku_45"},
	}
	reg.setModels(claudeModels)
	msgClaude := reg.FormatModelsMessage("claude-sonnet-4-6", i18n.Default)
	if strings.Contains(msgClaude, "/model flash") {
		t.Errorf("expected claude message not to contain flash alias")
	}
	if !strings.Contains(msgClaude, "/model haiku") {
		t.Errorf("expected claude message to contain haiku alias")
	}
	if !strings.Contains(msgClaude, "👉 <b>claude-sonnet-4-6</b> <i>(active)</i>") {
		t.Errorf("expected active model marker for claude-sonnet-4-6")
	}
}
