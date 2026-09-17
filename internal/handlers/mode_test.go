package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/storage"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionModeState(t *testing.T) {
	ps := &domain.ProjectState{}

	if ps.GetExecutionMode() != "cli" {
		t.Errorf("expected default execution mode to be 'cli', got: %s", ps.GetExecutionMode())
	}

	ps.SetExecutionMode("api")
	if ps.GetExecutionMode() != "api" {
		t.Errorf("expected execution mode to be 'api', got: %s", ps.GetExecutionMode())
	}

	ps.SetExecutionMode("cli")
	if ps.GetExecutionMode() != "cli" {
		t.Errorf("expected execution mode to be 'cli', got: %s", ps.GetExecutionMode())
	}
}

func TestModeCommandAndAgentSwitchWithMode(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_mode.db")
	st, err := storage.NewSQLiteStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create sqlite storage: %v", err)
	}
	defer st.Close()
	domain.GlobalTaskManager.InitWithStorage(st)

	t.Setenv("ANTHROPIC_API_KEY", "test-key-123")

	config.ProjectState.SetExecutionMode("api")
	msg, err := SwitchActiveAgent("claude")
	if err != nil {
		t.Fatalf("unexpected error switching agent: %v", err)
	}

	if !strings.Contains(msg, "[api]") {
		t.Errorf("expected message to mention [api] mode, got: %s", msg)
	}

	config.ProjectState.SetExecutionMode("cli")
	msg, err = SwitchActiveAgent("claude")
	if err != nil {
		t.Fatalf("unexpected error switching agent: %v", err)
	}

	if !strings.Contains(msg, "[cli]") {
		t.Errorf("expected message to mention [cli] mode, got: %s", msg)
	}

	savedMode, err := st.GetSetting(context.Background(), "execution_mode")
	_ = savedMode
}
