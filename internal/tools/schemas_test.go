package tools

import (
	"testing"
)

func TestClaudeToolDefinitions(t *testing.T) {
	ro := ClaudeToolDefinitions(true)
	if len(ro) != 2 {
		t.Errorf("expected 2 read-only tools for Claude, got %d", len(ro))
	}
	names := make(map[string]bool)
	for _, tool := range ro {
		names[tool["name"].(string)] = true
	}
	if !names["read_file"] || !names["list_dir"] {
		t.Errorf("read-only tools missing expected tools: %+v", names)
	}

	rw := ClaudeToolDefinitions(false)
	if len(rw) != 5 {
		t.Errorf("expected 5 tools for Claude in full mode, got %d", len(rw))
	}
	rwNames := make(map[string]bool)
	for _, tool := range rw {
		rwNames[tool["name"].(string)] = true
	}
	for _, expected := range []string{"read_file", "list_dir", "write_file", "edit_file", "run_command"} {
		if !rwNames[expected] {
			t.Errorf("full mode tools missing %s: %+v", expected, rwNames)
		}
	}
}

func TestGeminiToolDeclarations(t *testing.T) {
	ro := GeminiToolDeclarations(true)
	if len(ro) != 1 || len(ro[0].FunctionDeclarations) != 2 {
		t.Fatalf("expected 1 tool with 2 read-only decls for Gemini, got %+v", ro)
	}
	roNames := make(map[string]bool)
	for _, decl := range ro[0].FunctionDeclarations {
		roNames[decl.Name] = true
	}
	if !roNames["read_file"] || !roNames["list_dir"] {
		t.Errorf("read-only decls missing expected: %+v", roNames)
	}

	rw := GeminiToolDeclarations(false)
	if len(rw) != 1 || len(rw[0].FunctionDeclarations) != 5 {
		t.Fatalf("expected 1 tool with 5 decls for Gemini in full mode, got %+v", rw)
	}
	rwNames := make(map[string]bool)
	for _, decl := range rw[0].FunctionDeclarations {
		rwNames[decl.Name] = true
	}
	for _, expected := range []string{"read_file", "list_dir", "write_file", "edit_file", "run_command"} {
		if !rwNames[expected] {
			t.Errorf("full mode decls missing %s: %+v", expected, rwNames)
		}
	}
}
