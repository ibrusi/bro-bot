package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildClaudeArgs_NewSession(t *testing.T) {
	systemPrompt := "Follow senior developer instructions from AGENTS.md"
	args := buildClaudeArgs("", "sonnet", "Hello Claude", systemPrompt)

	// Проверяем обязательные флаги
	hasVerbose := false
	hasDangerous := false
	hasOutputFormat := false
	hasPrompt := false
	hasResume := false
	hasSessionID := false
	hasAppendSysPrompt := false

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
		case "--append-system-prompt":
			if i+1 < len(args) && args[i+1] == systemPrompt {
				hasAppendSysPrompt = true
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
	if !hasAppendSysPrompt {
		t.Errorf("expected buildClaudeArgs to include --append-system-prompt")
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
	// Even if systemPrompt is passed, for resumed session it must NOT be added to args
	args := buildClaudeArgs(convID, "claude-sonnet-4-6", "continue task", "Ignore this system prompt on resume")

	hasResume := false
	hasSessionID := false
	hasAppendSysPrompt := false

	for i := 0; i < len(args); i++ {
		if args[i] == "--resume" && i+1 < len(args) && args[i+1] == convID {
			hasResume = true
		}
		if args[i] == "--session-id" {
			hasSessionID = true
		}
		if args[i] == "--append-system-prompt" {
			hasAppendSysPrompt = true
		}
	}

	if !hasResume {
		t.Errorf("expected buildClaudeArgs to include --resume %s for existing conversation", convID)
	}
	if hasAppendSysPrompt {
		t.Errorf("expected buildClaudeArgs NOT to include --append-system-prompt on resumed session")
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

// TestClaudeAdapter_GetModelsWithoutKeyReturnsAliases — без ключа API список состоит
// только из псевдонимов CLI: они не протухают, в отличие от зашитых версий.
func TestClaudeAdapter_GetModelsWithoutKeyReturnsAliases(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_API_KEY", "")

	out, err := NewClaudeAdapter().GetModels(context.Background())
	if err != nil {
		t.Fatalf("GetModels: %v", err)
	}
	s := string(out)
	for _, alias := range []string{"sonnet", "opus", "fable"} {
		if !strings.HasPrefix(s, alias+" ") && !strings.Contains(s, "\n"+alias+" ") {
			t.Errorf("в списке нет псевдонима %q: %s", alias, s)
		}
	}
	if strings.Contains(s, "claude-3") || strings.Contains(s, "4-6") || strings.Contains(s, "4-5") {
		t.Errorf("в списке не должно быть зашитых версий: %s", s)
	}
}

// TestClaudeAdapter_GetModelsWithKeyUsesLiveList — с ключом cli-режим показывает тот же
// живой список, что и api-режим.
func TestClaudeAdapter_GetModelsWithKeyUsesLiveList(t *testing.T) {
	resetClaudeModelCache(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": "claude-opus-5", "display_name": "Claude Opus 5"},
				{"id": "claude-sonnet-5", "display_name": "Claude Sonnet 5"},
			},
			"has_more": false,
		})
	}))
	t.Cleanup(srv.Close)

	adapter := &ClaudeAdapter{HTTPClient: srv.Client(), BaseURL: srv.URL}
	out, err := adapter.GetModels(context.Background())
	if err != nil {
		t.Fatalf("GetModels: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "claude-opus-5 Claude Opus 5") || !strings.Contains(s, "claude-sonnet-5 Claude Sonnet 5") {
		t.Errorf("ожидали живой список из /v1/models, получили: %s", s)
	}
	if strings.Contains(s, "актуальная версия") {
		t.Errorf("при живом списке псевдонимы не нужны: %s", s)
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
	args := buildClaudeArgs("sess-1", "sonnet", "вопрос", "")
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "--resume sess-1") {
		t.Errorf("ожидали флаг --resume: %v", args)
	}
	if strings.Contains(joined, "history") || strings.Contains(joined, "system") {
		t.Errorf("история и преамбула не должны попадать в аргументы CLI: %v", args)
	}
}
