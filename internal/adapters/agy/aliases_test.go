package agy

import "testing"

func TestModelAliases(t *testing.T) {
	tests := []struct {
		alias    string
		expected string
		found    bool
	}{
		{"default", "gemini-3.1-pro-high", true},
		{"flash", "gemini-3.8-flash-medium", true},
		{"pro-low", "gemini-3.1-pro-low", true},
		{"sonnet", "claude-sonnet-5-5", true},
		{"claude-sonnet-5.5", "claude-sonnet-5-5", true},
		{"opus", "claude-opus-5-5", true},
		{"Claude-Opus", "claude-opus-5-5", true},
		{"claude-opus-5-5", "claude-opus-5-5", true},
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
		{"gemini-3.1-pro-high", false},
		{"gpt-oss-120b-medium", false},
		{"claude-opus-5-5", false},
		{"claude-opus-5-5-high", false},
		{"claude-sonnet-5-5-low", false},
		{"sonnet", false}, // псевдоним agy
		{"opus", false},
		{"haiku", true},
		{"claude-haiku-5-5", true},
		{"claude-sonnet-5", true},
		{"claude-opus-5", true}, // не путать с семейством claude-opus-5-5
	}
	for _, tc := range tests {
		if got := rejectsModel(tc.model); got != tc.rejected {
			t.Errorf("rejectsModel(%q) = %v, expected %v", tc.model, got, tc.rejected)
		}
	}
}
