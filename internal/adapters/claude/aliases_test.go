package claude

import "testing"

func TestModelAliases(t *testing.T) {
	tests := []struct {
		alias    string
		expected string
		found    bool
	}{
		{"default", "claude-sonnet-5-5", true},
		{"sonnet", "claude-sonnet-5-5", true},
		{"opus", "claude-opus-5-5", true},
		{"haiku", "claude-haiku-5-5", true},
		{"claude-haiku", "claude-haiku-5-5", true},
		{"claude-haiku-5.5", "claude-haiku-5-5", true},
		{"claude-haiku-5-5", "claude-haiku-5-5", true},
		{"sonnet-5", "claude-sonnet-5", true},
		{"claude-opus-4.6", "claude-opus-4-6", true},
		{"claude-haiku-4.5", "claude-haiku-4-5", true},
		// Модели Gemini и GPT claude не запускает.
		{"flash", "", false},
		{"pro", "", false},
		{"oss", "", false},
	}
	for _, tc := range tests {
		got, ok := modelAliases.Resolve(tc.alias)
		if got != tc.expected || ok != tc.found {
			t.Errorf("Resolve(%q) = %q, %v; expected %q, %v", tc.alias, got, ok, tc.expected, tc.found)
		}
	}
}
