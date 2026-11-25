package acp

import (
	"strings"

	"bro-bot/internal/tools"
)

// IsSafeReadOnlyShellCall проверяет, является ли запрос разрешения ACP вызовом
// shell-инструмента с исключительно безопасными read-only командами.
func IsSafeReadOnlyShellCall(call ToolCallPermission) bool {
	if srv := firstStringRecursive(0, []string{"ServerName", "server_name", "server", "mcpServer", "mcp_server"}, call.RawInput, call.Args, call.Meta); srv != "" {
		return false
	}
	if !isShellExecutionCall(call) {
		return false
	}
	cmd := extractShellCommand(call)
	if cmd == "" {
		return false
	}
	return IsSafeReadOnlyShellCommand(cmd)
}

func isShellExecutionCall(call ToolCallPermission) bool {
	if isShellToolName(call.Name) || isShellToolName(call.ToolName) {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(call.Kind), "execute") {
		return false
	}
	noExplicitName := strings.TrimSpace(call.Name) == "" && strings.TrimSpace(call.ToolName) == ""
	return noExplicitName || isShellToolName(call.Title)
}

func isShellToolName(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "run_command", "bash", "shell", "execute_command", "terminal", "sh":
		return true
	default:
		return false
	}
}

func extractShellCommand(call ToolCallPermission) string {
	if cmd := firstStringRecursive(0, []string{"CommandLine", "command_line", "command", "cmd"}, call.RawInput, call.Args, call.Meta); cmd != "" {
		return cmd
	}
	title := strings.TrimSpace(call.Title)
	if title == "" || isShellToolName(title) {
		return ""
	}
	return title
}

// IsSafeReadOnlyShellCommand делегирует разбор и проверку read-only shell-команд
// в единую реализацию пакета tools.
func IsSafeReadOnlyShellCommand(cmdLine string) bool {
	return tools.IsSafeReadOnlyShellCommand(cmdLine)
}
