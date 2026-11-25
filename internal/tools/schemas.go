package tools

import (
	"bro-bot/internal/mcp"
	"strings"

	"github.com/google/generative-ai-go/genai"
)

var botToolPrefixes = []string{
	"mcp__bro_bot__",
	"mcp__bro-bot__",
	"mcp_bro_bot_",
	"mcp_bro-bot_",
	"bro_bot__",
	"bro-bot__",
	"bro_bot:",
	"bro_bot/",
	"bro_bot_",
	"bro-bot:",
	"bro-bot/",
	"bro-bot_",
}

// NormalizeBotToolName приводит имя MCP-инструмента bro-bot (с любым провайдерным префиксом)
// к его каноническому имени из реестра моста.
func NormalizeBotToolName(name string) string {
	target := strings.ToLower(strings.TrimSpace(name))
	if target == "" {
		return ""
	}
	for _, prefix := range botToolPrefixes {
		if strings.HasPrefix(target, prefix) {
			target = strings.TrimPrefix(target, prefix)
			break
		}
	}
	return strings.TrimSpace(target)
}

// IsBotToolName сообщает, является ли имя инструмента одним из инструментов моста bro-bot.
func IsBotToolName(name string) bool {
	target := strings.ToLower(strings.TrimSpace(name))
	if target == "" {
		return false
	}
	for _, t := range mcp.BridgeToolDefinitions() {
		if strings.EqualFold(t.Name, target) {
			return true
		}
	}
	return false
}

// ClaudeReadOnlyToolDefinitions возвращает спецификации инструментов Claude для режима чтения.
func ClaudeReadOnlyToolDefinitions() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"name":        "read_file",
			"description": "Read the contents of a file (relative to workspace, or an absolute path on the system when permitted). Optionally specify start_line and end_line (1-indexed) to read a specific line range.",
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Relative path within the project workspace or an absolute file path.",
					},
					"start_line": map[string]interface{}{
						"type":        "integer",
						"description": "Optional 1-indexed starting line number.",
					},
					"end_line": map[string]interface{}{
						"type":        "integer",
						"description": "Optional 1-indexed ending line number.",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			"name":        "list_dir",
			"description": "List files and subdirectories in a directory path relative to the project workspace.",
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Directory path relative to workspace (defaults to '.' for root).",
					},
				},
			},
		},
		{
			"name":        "grep_search",
			"description": "Search file contents in the workspace by regular expression or substring, returning file:line: content matches (skips .git and binaries).",
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pattern": map[string]interface{}{
						"type":        "string",
						"description": "Regular expression or text substring to search for.",
					},
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Optional relative file or directory path within the workspace (defaults to '.').",
					},
					"glob": map[string]interface{}{
						"type":        "string",
						"description": "Optional filename glob filter (e.g. '*.go').",
					},
					"max_results": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of matching lines to return (default 50, max 200).",
					},
				},
				"required": []string{"pattern"},
			},
		},
		{
			"name":        "find_files",
			"description": "Find files within the project workspace matching a glob pattern or path substring (skips .git).",
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pattern": map[string]interface{}{
						"type":        "string",
						"description": "Glob pattern (e.g. '*.go') or case-insensitive substring of the path.",
					},
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Optional subdirectory path relative to workspace (defaults to '.').",
					},
					"max_results": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of file paths to return (default 100, max 500).",
					},
				},
				"required": []string{"pattern"},
			},
		},
	}
}

// ClaudeWriteToolDefinitions возвращает спецификации инструментов Claude для режима модификации.
func ClaudeWriteToolDefinitions() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"name":        "write_file",
			"description": "Create a new file or completely overwrite an existing file with the specified content.",
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Relative path within the project workspace or an absolute target path explicitly requested by the user.",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "Full file content to write.",
					},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			"name":        "edit_file",
			"description": "Edit an existing file by replacing old_string with new_string.",
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Relative path within the project workspace or an absolute target path explicitly requested by the user.",
					},
					"old_string": map[string]interface{}{
						"type":        "string",
						"description": "Exact text in the file to be replaced.",
					},
					"new_string": map[string]interface{}{
						"type":        "string",
						"description": "New replacement text.",
					},
				},
				"required": []string{"path", "old_string", "new_string"},
			},
		},
		{
			"name":        "run_command",
			"description": "Run a bash shell command in the project workspace directory.",
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{
						"type":        "string",
						"description": "Bash command line to execute.",
					},
				},
				"required": []string{"command"},
			},
		},
	}
}

// ClaudeToolDefinitions возвращает список инструментов Claude в зависимости от режима readOnly.
func ClaudeToolDefinitions(readOnly bool) []map[string]interface{} {
	return ClaudeToolDefinitionsWithBot(readOnly, false)
}

// ClaudeToolDefinitionsWithBot возвращает инструменты Claude и опционально инструменты бота из mcp.BridgeToolDefinitions.
func ClaudeToolDefinitionsWithBot(readOnly, includeBotTools bool) []map[string]interface{} {
	tools := ClaudeReadOnlyToolDefinitions()
	if !readOnly {
		tools = append(tools, ClaudeWriteToolDefinitions()...)
	}
	if includeBotTools {
		for _, bt := range mcp.BridgeToolDefinitions() {
			tools = append(tools, map[string]interface{}{
				"name":         bt.Name,
				"description":  bt.Description,
				"input_schema": bt.InputSchema,
			})
		}
	}
	return tools
}

// GeminiReadOnlyFunctionDeclarations возвращает декларации функций Gemini для режима чтения.
func GeminiReadOnlyFunctionDeclarations() []*genai.FunctionDeclaration {
	return []*genai.FunctionDeclaration{
		{
			Name:        "read_file",
			Description: "Read the contents of a file (relative to workspace, or an absolute path on the system when permitted). Optionally specify start_line and end_line (1-indexed) to read a specific line range.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"path": {
						Type:        genai.TypeString,
						Description: "Relative path within the project workspace or an absolute file path.",
					},
					"start_line": {
						Type:        genai.TypeInteger,
						Description: "Optional 1-indexed starting line number.",
					},
					"end_line": {
						Type:        genai.TypeInteger,
						Description: "Optional 1-indexed ending line number.",
					},
				},
				Required: []string{"path"},
			},
		},
		{
			Name:        "list_dir",
			Description: "List files and subdirectories in a directory path relative to the project workspace.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"path": {
						Type:        genai.TypeString,
						Description: "Directory path relative to workspace (defaults to '.' for root).",
					},
				},
			},
		},
		{
			Name:        "grep_search",
			Description: "Search file contents in the workspace by regular expression or substring, returning file:line: content matches (skips .git and binaries).",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"pattern": {
						Type:        genai.TypeString,
						Description: "Regular expression or text substring to search for.",
					},
					"path": {
						Type:        genai.TypeString,
						Description: "Optional relative file or directory path within the workspace (defaults to '.').",
					},
					"glob": {
						Type:        genai.TypeString,
						Description: "Optional filename glob filter (e.g. '*.go').",
					},
					"max_results": {
						Type:        genai.TypeInteger,
						Description: "Maximum number of matching lines to return (default 50, max 200).",
					},
				},
				Required: []string{"pattern"},
			},
		},
		{
			Name:        "find_files",
			Description: "Find files within the project workspace matching a glob pattern or path substring (skips .git).",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"pattern": {
						Type:        genai.TypeString,
						Description: "Glob pattern (e.g. '*.go') or case-insensitive substring of the path.",
					},
					"path": {
						Type:        genai.TypeString,
						Description: "Optional subdirectory path relative to workspace (defaults to '.').",
					},
					"max_results": {
						Type:        genai.TypeInteger,
						Description: "Maximum number of file paths to return (default 100, max 500).",
					},
				},
				Required: []string{"pattern"},
			},
		},
	}
}

// GeminiWriteFunctionDeclarations возвращает декларации функций Gemini для режима записи/выполнения.
func GeminiWriteFunctionDeclarations() []*genai.FunctionDeclaration {
	return []*genai.FunctionDeclaration{
		{
			Name:        "write_file",
			Description: "Create a new file or completely overwrite an existing file with the specified content.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"path": {
						Type:        genai.TypeString,
						Description: "Relative path within the project workspace or an absolute target path explicitly requested by the user.",
					},
					"content": {
						Type:        genai.TypeString,
						Description: "Full file content to write.",
					},
				},
				Required: []string{"path", "content"},
			},
		},
		{
			Name:        "edit_file",
			Description: "Edit an existing file by replacing old_string with new_string.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"path": {
						Type:        genai.TypeString,
						Description: "Relative path within the project workspace or an absolute target path explicitly requested by the user.",
					},
					"old_string": {
						Type:        genai.TypeString,
						Description: "Exact text in the file to be replaced.",
					},
					"new_string": {
						Type:        genai.TypeString,
						Description: "New replacement text.",
					},
				},
				Required: []string{"path", "old_string", "new_string"},
			},
		},
		{
			Name:        "run_command",
			Description: "Run a bash shell command in the project workspace directory.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"command": {
						Type:        genai.TypeString,
						Description: "Bash command line to execute.",
					},
				},
				Required: []string{"command"},
			},
		},
	}
}

// GeminiToolDeclarations возвращает структуру []*genai.Tool для регистрации в Gemini GenerativeModel.
func GeminiToolDeclarations(readOnly bool) []*genai.Tool {
	return GeminiToolDeclarationsWithBot(readOnly, false)
}

// GeminiToolDeclarationsWithBot возвращает декларации Gemini, опционально включая инструменты бота.
func GeminiToolDeclarationsWithBot(readOnly, includeBotTools bool) []*genai.Tool {
	decls := GeminiReadOnlyFunctionDeclarations()
	if !readOnly {
		decls = append(decls, GeminiWriteFunctionDeclarations()...)
	}
	if includeBotTools {
		for _, bt := range mcp.BridgeToolDefinitions() {
			schemaMap, _ := bt.InputSchema.(map[string]interface{})
			decls = append(decls, &genai.FunctionDeclaration{
				Name:        bt.Name,
				Description: bt.Description,
				Parameters:  jsonSchemaToGenaiSchema(schemaMap),
			})
		}
	}
	return []*genai.Tool{
		{
			FunctionDeclarations: decls,
		},
	}
}

func jsonSchemaToGenaiSchema(m map[string]interface{}) *genai.Schema {
	if len(m) == 0 {
		return &genai.Schema{Type: genai.TypeObject}
	}
	s := &genai.Schema{}
	if t, _ := m["type"].(string); t != "" {
		switch strings.ToLower(t) {
		case "object":
			s.Type = genai.TypeObject
		case "string":
			s.Type = genai.TypeString
		case "integer":
			s.Type = genai.TypeInteger
		case "number":
			s.Type = genai.TypeNumber
		case "boolean":
			s.Type = genai.TypeBoolean
		case "array":
			s.Type = genai.TypeArray
		}
	}
	if desc, _ := m["description"].(string); desc != "" {
		s.Description = desc
	}
	if props, ok := m["properties"].(map[string]interface{}); ok && len(props) > 0 {
		s.Properties = make(map[string]*genai.Schema, len(props))
		for k, v := range props {
			if sub, ok := v.(map[string]interface{}); ok {
				s.Properties[k] = jsonSchemaToGenaiSchema(sub)
			}
		}
	}
	if items, ok := m["items"].(map[string]interface{}); ok {
		s.Items = jsonSchemaToGenaiSchema(items)
	}
	switch req := m["required"].(type) {
	case []string:
		s.Required = append([]string(nil), req...)
	case []interface{}:
		for _, item := range req {
			if str, ok := item.(string); ok && str != "" {
				s.Required = append(s.Required, str)
			}
		}
	}
	return s
}
