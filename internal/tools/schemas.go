package tools

import (
	"github.com/google/generative-ai-go/genai"
)

// ClaudeReadOnlyToolDefinitions возвращает спецификации инструментов Claude для режима чтения.
func ClaudeReadOnlyToolDefinitions() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"name":        "read_file",
			"description": "Read the contents of a file in the project workspace. Optionally specify start_line and end_line (1-indexed) to read a specific line range.",
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Relative path to the file within the project workspace.",
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
	}
}

// ClaudeWriteToolDefinitions возвращает спецификации инструментов Claude для режима модификации.
func ClaudeWriteToolDefinitions() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"name":        "write_file",
			"description": "Create a new file or completely overwrite an existing file with the specified content in the project workspace.",
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Relative path to the file within the project workspace.",
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
			"description": "Edit an existing file by replacing old_string with new_string in the project workspace.",
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Relative path to the file within the project workspace.",
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
	tools := ClaudeReadOnlyToolDefinitions()
	if !readOnly {
		tools = append(tools, ClaudeWriteToolDefinitions()...)
	}
	return tools
}

// GeminiReadOnlyFunctionDeclarations возвращает декларации функций Gemini для режима чтения.
func GeminiReadOnlyFunctionDeclarations() []*genai.FunctionDeclaration {
	return []*genai.FunctionDeclaration{
		{
			Name:        "read_file",
			Description: "Read the contents of a file in the project workspace. Optionally specify start_line and end_line (1-indexed) to read a specific line range.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"path": {
						Type:        genai.TypeString,
						Description: "Relative path to the file within the project workspace.",
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
	}
}

// GeminiWriteFunctionDeclarations возвращает декларации функций Gemini для режима записи/выполнения.
func GeminiWriteFunctionDeclarations() []*genai.FunctionDeclaration {
	return []*genai.FunctionDeclaration{
		{
			Name:        "write_file",
			Description: "Create a new file or completely overwrite an existing file with the specified content in the project workspace.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"path": {
						Type:        genai.TypeString,
						Description: "Relative path to the file within the project workspace.",
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
			Description: "Edit an existing file by replacing old_string with new_string in the project workspace.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"path": {
						Type:        genai.TypeString,
						Description: "Relative path to the file within the project workspace.",
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
	decls := GeminiReadOnlyFunctionDeclarations()
	if !readOnly {
		decls = append(decls, GeminiWriteFunctionDeclarations()...)
	}
	return []*genai.Tool{
		{
			FunctionDeclarations: decls,
		},
	}
}
