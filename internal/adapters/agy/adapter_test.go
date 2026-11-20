package agy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bro-bot/internal/config"
	"bro-bot/internal/effort"
	"bro-bot/internal/ports"
)

func TestBuildAgyArgs_NewSession(t *testing.T) {
	args := buildAgyArgs(ports.ExecuteArgs{ModelName: "gemini-3.8-flash-high", Prompt: "Hello Agy"})

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

func TestBuildAgyArgs_NewSessionWithSystemPrompt(t *testing.T) {
	sysPrompt := "Senior dev rules from AGENTS.md"
	args := buildAgyArgs(ports.ExecuteArgs{
		ModelName:    "gemini-3.8-flash-high",
		Prompt:       "Do task",
		SystemPrompt: sysPrompt,
		WorkDir:      "/tmp/non-existent-dir",
	})

	promptArg := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "-p" && i+1 < len(args) {
			promptArg = args[i+1]
		}
	}

	if !strings.Contains(promptArg, "ИНСТРУКЦИИ ПРОЕКТА (AGENTS.md):") || !strings.Contains(promptArg, sysPrompt) {
		t.Errorf("expected prompt to contain prepended rules, got: %s", promptArg)
	}
	if !strings.Contains(promptArg, "Do task") {
		t.Errorf("expected prompt to contain user prompt, got: %s", promptArg)
	}
}

func TestBuildAgyArgs_ResumeSession(t *testing.T) {
	convID := "test-conversation-uuid-1234"
	args := buildAgyArgs(ports.ExecuteArgs{
		ConversationID: convID,
		ModelName:      "gemini-3.1-pro-high",
		Prompt:         "Continue task",
		SystemPrompt:   "Ignore this system prompt on resume",
	})

	hasConv := false
	promptArg := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--conversation" && i+1 < len(args) && args[i+1] == convID {
			hasConv = true
		}
		if args[i] == "-p" && i+1 < len(args) {
			promptArg = args[i+1]
		}
	}

	if !hasConv {
		t.Errorf("expected buildAgyArgs to include --conversation %s", convID)
	}
	if strings.Contains(promptArg, "AGENTS.md") || strings.Contains(promptArg, "Ignore this system prompt") {
		t.Errorf("expected resume prompt NOT to contain system prompt, got: %s", promptArg)
	}
}

func TestAgyAdapter_GetModels_Real(t *testing.T) {
	t.Skip("needs auth or agy executable in CI")
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

// История диалога в CLI-режиме не влияет на аргументы: agy восстанавливает контекст
// по --conversation, а History предназначена только для api-режима.
func TestBuildAgyArgs_IgnoresHistory(t *testing.T) {
	base := ports.ExecuteArgs{ConversationID: "conv-1", ModelName: "gemini-3.8-flash-high", Prompt: "вопрос"}
	withoutHistory := buildAgyArgs(base)
	base.History = []ports.ChatMessage{{Role: "user", Content: "раньше"}, {Role: "assistant", Content: "ответ"}}
	withHistory := buildAgyArgs(base)

	if strings.Join(withoutHistory, " ") != strings.Join(withHistory, " ") {
		t.Errorf("аргументы разошлись: %v vs %v", withoutHistory, withHistory)
	}
	if !strings.Contains(strings.Join(withHistory, " "), "--conversation conv-1") {
		t.Errorf("ожидали флаг --conversation: %v", withHistory)
	}
}

// Уровень /effort доходит до agy вариантом модели из каталога, а не отдельным
// --effort: agy отвергает --effort, если он расходится с уровнем в имени модели.
func TestBuildAgyArgs_Effort(t *testing.T) {
	args := ports.ExecuteArgs{ModelName: "gemini-3.1-pro-high", Prompt: "вопрос"}

	joined := strings.Join(buildAgyArgs(args), " ")
	if strings.Contains(joined, "--effort") {
		t.Errorf("auto не должен передавать --effort: %s", joined)
	}

	args.Effort = effort.Low
	joined = strings.Join(buildAgyArgs(args), " ")
	if !strings.Contains(joined, "--model gemini-3.1-pro-low") || strings.Contains(joined, "--effort") {
		t.Errorf("ожидали вариант gemini-3.1-pro-low без --effort: %s", joined)
	}
}

func TestBuildAgyArgs_PrintTimeout(t *testing.T) {
	cases := []struct {
		name    string
		timeout time.Duration
		want    string
	}{
		{"zero uses DefaultStepTimeout", 0, config.DefaultStepTimeout.String()},
		{"negative uses DefaultStepTimeout", -5 * time.Second, config.DefaultStepTimeout.String()},
		{"45m", 45 * time.Minute, "45m0s"},
		{"2h", 2 * time.Hour, "2h0m0s"},
		{"90s", 90 * time.Second, "1m30s"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmdArgs := buildAgyArgs(ports.ExecuteArgs{
				ModelName: "gemini-3.8-flash-high",
				Prompt:    "test",
				Timeout:   tc.timeout,
			})
			got := ""
			for i := 0; i < len(cmdArgs)-1; i++ {
				if cmdArgs[i] == "--print-timeout" {
					got = cmdArgs[i+1]
					break
				}
			}
			if got != tc.want {
				t.Errorf("--print-timeout = %q, want %q (args: %v)", got, tc.want, cmdArgs)
			}
		})
	}
}

func TestBuildAgyArgs_BotInstructionsEvenWithLocalAgentsMD(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "AGENTS.md"), []byte("# Local rules\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args := buildAgyArgs(ports.ExecuteArgs{
		ModelName:       "gemini-3.8-flash-high",
		Prompt:          "Investigate bug",
		WorkDir:         workDir,
		SystemPrompt:    "# Duplicate rules that should be skipped",
		BotInstructions: "BOT_TOOL_PROMPT_MARKER: use send_file",
	})
	var promptArg string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-p" {
			promptArg = args[i+1]
			break
		}
	}
	if !strings.Contains(promptArg, "BOT_TOOL_PROMPT_MARKER") {
		t.Errorf("expected BotInstructions even when local AGENTS.md exists, got: %s", promptArg)
	}
	if strings.Contains(promptArg, "Duplicate rules") {
		t.Errorf("expected SystemPrompt not to duplicate local AGENTS.md, got: %s", promptArg)
	}
}
