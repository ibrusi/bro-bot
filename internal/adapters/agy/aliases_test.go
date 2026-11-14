package agy

import "testing"

func TestModelAliases(t *testing.T) {
	tests := []struct {
		alias    string
		expected string
		found    bool
	}{
		{"default", "gemini-pro-orchestrator-g1-pro", true},
		{"orchestrator", "gemini-pro-orchestrator-g1-pro", true},
		{"argon", "gemini-4-argon-medium", true},
		{"argon-high", "gemini-4-argon-high", true},
		{"hydrogen", "gemini-4-hydrogen-medium", true},
		{"hydrogen-xhigh", "gemini-4-hydrogen-xhigh", true},
		{"flash", "gemini-3.8-flash-medium", true},
		{"pro-low", "gemini-3.1-pro-low", true},
		{"sonnet", "claude-sonnet-5-5-medium", true},
		{"claude-sonnet-5.5", "claude-sonnet-5-5-medium", true},
		{"opus", "claude-opus-5-5-medium", true},
		{"Claude-Opus", "claude-opus-5-5-medium", true},
		{"claude-opus-5-5", "claude-opus-5-5-medium", true},
		{"oss", "gpt-oss-120b-medium", true},
		// Haiku в каталоге agy нет: псевдоним принадлежит только claude.
		{"haiku", "", false},
		{"claude-haiku-5-5", "", false},
		{"sonnet-5", "", false},
	}
	for _, tc := range tests {
		got, ok := modelAliases.Resolve(tc.alias)
		if got != tc.expected || ok != tc.found {
			t.Errorf("Resolve(%q) = %q, %v; expected %q, %v", tc.alias, got, ok, tc.expected, tc.found)
		}
	}
}

func TestRejectsModel(t *testing.T) {
	tests := []struct {
		model    string
		rejected bool
	}{
		{"", false},
		{"gemini-pro-orchestrator-g1-pro", false},
		{"argon", false},
		{"hydrogen", false},
		{"gemini-3.1-pro-high", false},
		{"gpt-oss-120b-medium", false},
		{"claude-opus-5-5", true},
		{"claude-opus-5-5-high", true},
		{"claude-sonnet-5-5-low", true},
		{"sonnet", true},
		{"opus", true},
		{"grok-4.7", true},
		{"haiku", true},
		{"claude-haiku-5-5", true},
		{"claude-sonnet-5", true},
		{"claude-opus-5", true},
	}
	for _, tc := range tests {
		if got := rejectsModel(tc.model); got != tc.rejected {
			t.Errorf("rejectsModel(%q) = %v, expected %v", tc.model, got, tc.rejected)
		}
	}
}
