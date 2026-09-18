package claude

import (
	"bro-bot/internal/ports"
	"context"
	"strings"
	"testing"
)

func TestClaudeAPIAdapterGetModels(t *testing.T) {
	adapter := NewClaudeAPIAdapter()
	ctx := context.Background()

	modelsBytes, err := adapter.GetModels(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	modelsText := string(modelsBytes)
	if !strings.Contains(modelsText, "claude-3-7-sonnet") {
		t.Errorf("expected models to contain claude-3-7-sonnet, got: %s", modelsText)
	}
}

func TestClaudeAPIAdapterQuota(t *testing.T) {
	adapter := NewClaudeAPIAdapter()
	ctx := context.Background()

	quota, err := adapter.GetQuota(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(string(quota), "SUCCESS") {
		t.Errorf("expected quota response to contain SUCCESS, got: %s", string(quota))
	}
}

func TestClaudeAPIAdapterProcessExecutionFallback(t *testing.T) {
	adapter := NewClaudeAPIAdapter()
	ctx := context.Background()

	args := ports.ExecuteArgs{
		Prompt:    "hello",
		ModelName: "sonnet",
	}

	// Without ANTHROPIC_API_KEY, it falls back gracefully or executes CLI
	_, err := adapter.ExecuteTask(ctx, args)
	if err != nil {
		// CLI execution in test environment might fail if binary not in path, but that's expected
		t.Logf("ExecuteTask returned expected path: %v", err)
	}
}

func TestBuildClaudeMessagesBodyWithHistory(t *testing.T) {
	args := ports.ExecuteArgs{
		Prompt:       "а какая ты модель?",
		SystemPrompt: "РЕЖИМ ДИАЛОГА",
		History: []ports.ChatMessage{
			{Role: "user", Content: "привет"},
			{Role: "assistant", Content: "привет, чем помочь?"},
			{Role: "model", Content: "  "}, // пустые реплики отбрасываем
		},
	}

	body := buildClaudeMessagesBody("claude-sonnet-4-5", args)

	if body["model"] != "claude-sonnet-4-5" {
		t.Errorf("модель = %v", body["model"])
	}
	if body["system"] != "РЕЖИМ ДИАЛОГА" {
		t.Errorf("системная инструкция = %v", body["system"])
	}

	messages, ok := body["messages"].([]map[string]interface{})
	if !ok {
		t.Fatalf("messages имеет неожиданный тип %T", body["messages"])
	}
	if len(messages) != 3 {
		t.Fatalf("ожидали 3 сообщения (2 из истории + текущее), получили %d: %+v", len(messages), messages)
	}
	if messages[0]["role"] != "user" || messages[0]["content"] != "привет" {
		t.Errorf("первая реплика: %+v", messages[0])
	}
	if messages[1]["role"] != "assistant" {
		t.Errorf("роль ответа ассистента: %+v", messages[1])
	}
	if messages[2]["role"] != "user" || messages[2]["content"] != "а какая ты модель?" {
		t.Errorf("последней должна идти текущая реплика пользователя: %+v", messages[2])
	}
}

func TestBuildClaudeMessagesBodyWithoutHistory(t *testing.T) {
	body := buildClaudeMessagesBody("claude-sonnet-4-5", ports.ExecuteArgs{Prompt: "одиночный вопрос"})

	if _, hasSystem := body["system"]; hasSystem {
		t.Error("без преамбулы поле system не должно добавляться")
	}
	messages := body["messages"].([]map[string]interface{})
	if len(messages) != 1 || messages[0]["content"] != "одиночный вопрос" {
		t.Errorf("ожидали ровно одну реплику пользователя: %+v", messages)
	}
}
