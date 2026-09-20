package handlers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bro-bot/internal/adapters/mock"
	"bro-bot/internal/agents"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/ports"
)

func TestExecuteStepForTask_InjectsSystemPromptOnlyOnNewSession(t *testing.T) {
	mt := setupTestApp(t)

	// Создаем тестовую структуру проектов и AGENTS.md
	tempDir := t.TempDir()
	projDir := filepath.Join(tempDir, "testproj")
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	rulesContent := "# AGENTS.md Rules\nSenior developer persona and clean code."
	if err := os.WriteFile(filepath.Join(projDir, "AGENTS.md"), []byte(rulesContent), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	config.ProjectsRoot = tempDir

	mockFramework := &mock.AgentFramework{
		Name:           "mock-agent",
		ConversationID: "session-uuid-1",
		Response:       "Step completed successfully",
	}

	reg := testAgentRegistry()
	reg.Register(agents.Spec{
		Name:   "mock-agent",
		NewCLI: func() ports.AgentFramework { return mockFramework },
		NewMCP: func() ports.AgentFramework { return mockFramework },
		NewAPI: func() ports.AgentFramework { return mockFramework },
	})
	setAgentRegistry(reg)
	if _, err := SwitchActiveAgent("mock-agent"); err != nil {
		t.Fatalf("switch active agent: %v", err)
	}

	task := domain.GlobalTaskManager.CreateTaskWithPlanAndAgent("testproj", "mock-model", "mock-agent", "initial task", testChatID, false)

	// 1. Первый шаг: convID == "" (новая сессия)
	res1 := executeStepForTask(mt.Messenger, testChatID, task, projDir, "initial prompt", "mock-model", "ru")
	if res1.Outcome != StepOutcomeSuccess {
		t.Fatalf("first step failed: %v", res1.Error)
	}

	calls := mockFramework.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].ConversationID != "" {
		t.Errorf("expected empty ConversationID on step 1, got %q", calls[0].ConversationID)
	}
	if calls[0].SystemPrompt != rulesContent {
		t.Errorf("expected SystemPrompt %q on step 1, got %q", rulesContent, calls[0].SystemPrompt)
	}

	// 2. Второй шаг: задача уже имеет ConversationID != "" (возобновленная сессия)
	// В памяти и БД task.ConversationID установлен как "session-uuid-1"
	task.Update(func(ts *domain.TaskSession) {
		ts.ConversationID = "session-uuid-1"
	})

	res2 := executeStepForTask(mt.Messenger, testChatID, task, projDir, "follow-up prompt", "mock-model", "ru")
	if res2.Outcome != StepOutcomeSuccess {
		t.Fatalf("second step failed: %v", res2.Error)
	}

	calls = mockFramework.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[1].ConversationID != "session-uuid-1" {
		t.Errorf("expected ConversationID 'session-uuid-1' on step 2, got %q", calls[1].ConversationID)
	}
	if calls[1].SystemPrompt != "" {
		t.Errorf("expected EMPTY SystemPrompt on step 2 to prevent burning tokens, got %q", calls[1].SystemPrompt)
	}
}

func TestRunChatTurn_InjectsRulesOnlyOnFirstTurn(t *testing.T) {
	mt := setupTestApp(t)

	tempDir := t.TempDir()
	projDir := filepath.Join(tempDir, "chatproj")
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	rulesContent := "# Project Chat Rules\nAuthenticity marker: talk honestly."
	if err := os.WriteFile(filepath.Join(projDir, "AGENTS.md"), []byte(rulesContent), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	config.ProjectsRoot = tempDir

	mockFramework := &mock.AgentFramework{
		Name:           "chat-mock-agent",
		ConversationID: "chat-conv-1",
		Response:       "Answer to user question",
	}

	reg := testAgentRegistry()
	reg.Register(agents.Spec{
		Name:   "chat-mock-agent",
		NewCLI: func() ports.AgentFramework { return mockFramework },
		NewMCP: func() ports.AgentFramework { return mockFramework },
		NewAPI: func() ports.AgentFramework { return mockFramework },
	})
	setAgentRegistry(reg)
	if _, err := SwitchActiveAgent("chat-mock-agent"); err != nil {
		t.Fatalf("switch active agent: %v", err)
	}

	session := domain.GlobalChatManager.GetOrCreate("chatproj", "mock-model")

	setup := chatTurnSetup{
		Project:   "chatproj",
		Agent:     "chat-mock-agent",
		Mode:      "cli",
		Model:     "mock-model",
		Framework: mockFramework,
	}

	// 1. Первый ход диалога (firstTurn == true)
	ctx := context.Background()
	runChatTurn(ctx, mt.Messenger, testChatID, session, setup, "Привет, расскажи о проекте", "")

	calls := mockFramework.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Prompt, rulesContent) && calls[0].SystemPrompt != rulesContent {
		t.Errorf("expected turn 1 to include rulesContent, got Prompt=%q, SystemPrompt=%q", calls[0].Prompt, calls[0].SystemPrompt)
	}

	// 2. Второй ход диалога (firstTurn == false, ConversationID установлен)
	runChatTurn(ctx, mt.Messenger, testChatID, session, setup, "А где точка входа?", "")

	calls = mockFramework.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if strings.Contains(calls[1].Prompt, rulesContent) {
		t.Errorf("expected turn 2 prompt NOT to contain rulesContent (token save), got %q", calls[1].Prompt)
	}
	if calls[1].SystemPrompt != "" {
		t.Errorf("expected turn 2 SystemPrompt to be empty in CLI mode, got %q", calls[1].SystemPrompt)
	}
}
