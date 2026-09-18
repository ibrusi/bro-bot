package claude

import (
	"strings"
	"testing"
)

func TestBuildClaudeArgs_NewSession(t *testing.T) {
	args := buildClaudeArgs("", "sonnet", "Hello Claude")

	// Проверяем обязательные флаги
	hasVerbose := false
	hasDangerous := false
	hasOutputFormat := false
	hasPrompt := false
	hasResume := false
	hasSessionID := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--verbose":
			hasVerbose = true
		case "--dangerously-skip-permissions":
			hasDangerous = true
		case "--output-format":
			if i+1 < len(args) && args[i+1] == "stream-json" {
				hasOutputFormat = true
			}
		case "-p":
			if i+1 < len(args) && args[i+1] == "Hello Claude" {
				hasPrompt = true
			}
		case "--resume", "-r":
			hasResume = true
		case "--session-id":
			hasSessionID = true
		}
	}

	if !hasVerbose {
		t.Errorf("expected buildClaudeArgs to include --verbose")
	}
	if !hasDangerous {
		t.Errorf("expected buildClaudeArgs to include --dangerously-skip-permissions")
	}
	if !hasOutputFormat {
		t.Errorf("expected buildClaudeArgs to include --output-format stream-json")
	}
	if !hasPrompt {
		t.Errorf("expected buildClaudeArgs to include -p 'Hello Claude'")
	}
	if hasResume {
		t.Errorf("expected buildClaudeArgs NOT to include --resume for new session")
	}
	if hasSessionID {
		t.Errorf("expected buildClaudeArgs NOT to include --session-id for new session")
	}
}

func TestBuildClaudeArgs_ResumeSession(t *testing.T) {
	convID := "38f5a876-319b-4cf3-9716-5bdac15877c7"
	args := buildClaudeArgs(convID, "claude-sonnet-4-6", "continue task")

	hasResume := false
	hasSessionID := false

	for i := 0; i < len(args); i++ {
		if args[i] == "--resume" && i+1 < len(args) && args[i+1] == convID {
			hasResume = true
		}
		if args[i] == "--session-id" {
			hasSessionID = true
		}
	}

	if !hasResume {
		t.Errorf("expected buildClaudeArgs to include --resume %s for existing conversation", convID)
	}
	if hasSessionID {
		t.Errorf("expected buildClaudeArgs NOT to include --session-id for existing conversation")
	}
}

func TestResolveClaudeModel(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty model defaults to sonnet", "", "sonnet"},
		{"claude-sonnet-4-6 preserved", "claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"claude-opus-4-6-thinking preserved", "claude-opus-4-6-thinking", "claude-opus-4-6-thinking"},
		{"claude-haiku-4-5 preserved", "claude-haiku-4-5", "claude-haiku-4-5"},
		{"sonnet preserved", "sonnet", "sonnet"},
		{"gemini model overridden to sonnet", "gemini-3.1-pro-high", "sonnet"},
		{"gemini flash overridden to sonnet", "gemini-3.8-flash-high", "sonnet"},
		{"gpt oss overridden to sonnet", "gpt-oss-120b-medium", "sonnet"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveClaudeModel(tc.input)
			if got != tc.expected {
				t.Errorf("resolveClaudeModel(%q) = %q, expected %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestClaudeAdapter_GetModels(t *testing.T) {
	adapter := NewClaudeAdapter()
	out, err := adapter.GetModels(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "claude-sonnet") {
		t.Errorf("expected GetModels output to contain claude-sonnet, got %s", s)
	}
}

func TestClaudeAdapter_GetCredits(t *testing.T) {
	adapter := NewClaudeAdapter()
	out, err := adapter.GetCredits(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "{}" {
		t.Errorf("expected GetCredits to return '{}', got %s", string(out))
	}
}


// История диалога в CLI-режиме не влияет на аргументы: claude восстанавливает контекст по --resume.
func TestBuildClaudeArgs_IgnoresHistory(t *testing.T) {
	args := buildClaudeArgs("sess-1", "sonnet", "вопрос")
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "--resume sess-1") {
		t.Errorf("ожидали флаг --resume: %v", args)
	}
	if strings.Contains(joined, "history") || strings.Contains(joined, "system") {
		t.Errorf("история и преамбула не должны попадать в аргументы CLI: %v", args)
	}
}
