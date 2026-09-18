package agy

import (
	"context"
	"strings"
	"testing"
)

func TestAgyAPIAdapter_AgentName(t *testing.T) {
	adapter := NewAgyAPIAdapter()
	if adapter.AgentName() != "agy-api" {
		t.Errorf("Expected agy-api, got %s", adapter.AgentName())
	}
}

func TestResolveGeminiModel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "gemini-2.5-pro"},
		{"default", "gemini-2.5-pro"},
		{"flash", "gemini-1.5-flash"},
		{"pro", "gemini-1.5-pro"},
		{"gemini-2.5-flash", "gemini-1.5-flash"}, // fallback mapping logic without registry
	}

	for _, tt := range tests {
		result := resolveGeminiModel(tt.input)
		if result != tt.expected {
			t.Errorf("resolveGeminiModel(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestAgyAPIAdapter_GetModels(t *testing.T) {
	adapter := NewAgyAPIAdapter()
	models, err := adapter.GetModels(context.Background())
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if !strings.Contains(string(models), "gemini-2.5-pro") {
		t.Errorf("Expected models to contain gemini-2.5-pro")
	}
}
