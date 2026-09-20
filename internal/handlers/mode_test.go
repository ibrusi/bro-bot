package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/storage"
	"context"
	"path/filepath"
	"testing"
)

func TestExecutionModeState(t *testing.T) {
	ps := &domain.ProjectState{}

	if ps.GetExecutionMode() != "mcp" {
		t.Errorf("expected default execution mode to be 'mcp', got: %s", ps.GetExecutionMode())
	}

	ps.SetExecutionMode("api")
	if ps.GetExecutionMode() != "api" {
		t.Errorf("expected execution mode to be 'api', got: %s", ps.GetExecutionMode())
	}

	ps.SetExecutionMode("cli")
	if ps.GetExecutionMode() != "cli" {
		t.Errorf("expected execution mode to be 'cli', got: %s", ps.GetExecutionMode())
	}

	ps.SetExecutionMode("mcp")
	if ps.GetExecutionMode() != "mcp" {
		t.Errorf("expected execution mode to be 'mcp', got: %s", ps.GetExecutionMode())
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

	config.ProjectState.SetExecutionMode("mcp")
	res, err := SwitchActiveAgent("claude")
	if err != nil {
		t.Fatalf("unexpected error switching agent: %v", err)
	}

	if res.Mode != "mcp" {
		t.Errorf("expected result to carry mcp mode, got: %+v", res)
	}

	config.ProjectState.SetExecutionMode("api")
	res, err = SwitchActiveAgent("claude")
	if err != nil {
		t.Fatalf("unexpected error switching agent: %v", err)
	}

	if res.Mode != "api" {
		t.Errorf("expected result to carry api mode, got: %+v", res)
	}

	config.ProjectState.SetExecutionMode("cli")
	res, err = SwitchActiveAgent("claude")
	if err != nil {
		t.Fatalf("unexpected error switching agent: %v", err)
	}

	if res.Mode != "cli" {
		t.Errorf("expected result to carry cli mode, got: %+v", res)
	}

	savedMode, err := st.GetSetting(context.Background(), "execution_mode")
	_ = savedMode
}
