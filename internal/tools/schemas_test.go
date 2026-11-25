package tools

import (
	"testing"
)

func TestClaudeToolDefinitions(t *testing.T) {
	ro := ClaudeToolDefinitions(true)
	if len(ro) != 4 {
		t.Errorf("expected 4 read-only tools for Claude, got %d", len(ro))
	}
	names := make(map[string]bool)
	for _, tool := range ro {
		names[tool["name"].(string)] = true
	}
	for _, expected := range []string{"read_file", "list_dir", "grep_search", "find_files"} {
		if !names[expected] {
			t.Errorf("read-only tools missing %s: %+v", expected, names)
		}
	}

	rw := ClaudeToolDefinitions(false)
	if len(rw) != 7 {
		t.Errorf("expected 7 tools for Claude in full mode, got %d", len(rw))
	}
	rwNames := make(map[string]bool)
	for _, tool := range rw {
		rwNames[tool["name"].(string)] = true
	}
	for _, expected := range []string{"read_file", "list_dir", "grep_search", "find_files", "write_file", "edit_file", "run_command"} {
		if !rwNames[expected] {
			t.Errorf("full mode tools missing %s: %+v", expected, rwNames)
		}
	}

	withBot := ClaudeToolDefinitionsWithBot(true, true)
	botNames := make(map[string]bool)
	for _, tool := range withBot {
		botNames[tool["name"].(string)] = true
	}
	for _, expected := range []string{"ask_user", "send_file", "telegram_send_message", "get_task_context", "list_tasks", "list_projects", "sqlite_query"} {
		if !botNames[expected] {
			t.Errorf("bot tools missing %s: %+v", expected, botNames)
		}
	}
}

func TestGeminiToolDeclarations(t *testing.T) {
	ro := GeminiToolDeclarations(true)
	if len(ro) != 1 || len(ro[0].FunctionDeclarations) != 4 {
		t.Fatalf("expected 1 tool with 4 read-only decls for Gemini, got %+v", ro)
	}
	roNames := make(map[string]bool)
	for _, decl := range ro[0].FunctionDeclarations {
		roNames[decl.Name] = true
	}
	for _, expected := range []string{"read_file", "list_dir", "grep_search", "find_files"} {
		if !roNames[expected] {
			t.Errorf("read-only decls missing %s: %+v", expected, roNames)
		}
	}

	rw := GeminiToolDeclarations(false)
	if len(rw) != 1 || len(rw[0].FunctionDeclarations) != 7 {
		t.Fatalf("expected 1 tool with 7 decls for Gemini in full mode, got %+v", rw)
	}
	rwNames := make(map[string]bool)
	for _, decl := range rw[0].FunctionDeclarations {
		rwNames[decl.Name] = true
	}
	for _, expected := range []string{"read_file", "list_dir", "grep_search", "find_files", "write_file", "edit_file", "run_command"} {
		if !rwNames[expected] {
			t.Errorf("full mode decls missing %s: %+v", expected, rwNames)
		}
	}

	withBot := GeminiToolDeclarationsWithBot(false, true)
	botNames := make(map[string]bool)
	for _, decl := range withBot[0].FunctionDeclarations {
		botNames[decl.Name] = true
	}
	if !botNames["ask_user"] || !botNames["send_file"] || !botNames["sqlite_query"] || !IsBotToolName("ask_user") || !IsBotToolName("sqlite_query") || IsBotToolName("read_file") {
		t.Errorf("unexpected bot tool flags: botNames=%+v", botNames)
	}
}
