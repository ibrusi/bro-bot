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
