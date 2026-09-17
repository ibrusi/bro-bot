package agy

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBuildAgyArgs_NewSession(t *testing.T) {
	args := buildAgyArgs("", "gemini-3.8-flash-high", "Hello Agy")

	hasDangerous := false
	hasOutputFormat := false
	hasPrompt := false
	hasConv := false
	hasModel := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dangerously-skip-permissions":
			hasDangerous = true
		case "--output-format":
			if i+1 < len(args) && args[i+1] == "stream-json" {
				hasOutputFormat = true
			}
		case "-p":
			if i+1 < len(args) && args[i+1] == "Hello Agy" {
				hasPrompt = true
			}
		case "--conversation":
			hasConv = true
		case "--model":
			if i+1 < len(args) && args[i+1] == "gemini-3.8-flash-high" {
				hasModel = true
			}
		}
	}

	if !hasDangerous {
		t.Errorf("expected buildAgyArgs to include --dangerously-skip-permissions")
	}
	if !hasOutputFormat {
		t.Errorf("expected buildAgyArgs to include --output-format stream-json")
	}
	if !hasPrompt {
		t.Errorf("expected buildAgyArgs to include -p 'Hello Agy'")
	}
	if !hasModel {
		t.Errorf("expected buildAgyArgs to include --model gemini-3.8-flash-high")
	}
	if hasConv {
		t.Errorf("expected buildAgyArgs NOT to include --conversation for new session")
	}
}

func TestBuildAgyArgs_ResumeSession(t *testing.T) {
	convID := "test-conversation-uuid-1234"
	args := buildAgyArgs(convID, "gemini-3.1-pro-high", "Continue task")

	hasConv := false
	for i := 0; i < len(args); i++ {
		if args[i] == "--conversation" && i+1 < len(args) && args[i+1] == convID {
			hasConv = true
		}
	}

	if !hasConv {
		t.Errorf("expected buildAgyArgs to include --conversation %s", convID)
	}
}

func skip_TestAgyAdapter_GetModels_Real(t *testing.T) {
	adapter := NewAgyAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := adapter.GetModels(ctx)
	if err != nil {
		t.Fatalf("AgyAdapter.GetModels failed: %v", err)
	}

	outputStr := string(out)
	if !strings.Contains(outputStr, "gemini") {
		t.Errorf("expected GetModels output to contain gemini models, got: %s", outputStr)
	}
}
