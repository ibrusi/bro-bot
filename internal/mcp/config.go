package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

type ServerConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// GetBotExecutable возвращает путь к исполняемому файлу бота для запуска MCP-сервера.
func GetBotExecutable() string {
	if exe, err := os.Executable(); err == nil {
		// Проверяем, не запущен ли бот во временной директории go build/test
		if !strings.Contains(exe, "go-build") && !strings.HasPrefix(exe, os.TempDir()) {
			return exe
		}
	}
	if path, err := filepath.Abs("bot"); err == nil {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	if _, err := os.Stat("/home/deploy/bro-bot/bot"); err == nil {
		return "/home/deploy/bro-bot/bot"
	}
	return "bot"
}

// bridgeServerName — имя MCP-сервера бота в конфигурациях агентов. От него зависят
// имена инструментов у агента: например, mcp__bro_bot__ask_user у claude.
const bridgeServerName = "bro_bot"

// bridgeServerArgs — аргументы запуска `bot mcp-serve` с сокетом моста socketPath.
// Без пути mcp-serve найдёт сокет рядом со своим исполняемым файлом.
func bridgeServerArgs(socketPath string) []string {
	if socketPath == "" {
		return []string{"mcp-serve"}
	}
	return []string{"mcp-serve", "--socket", socketPath}
}

// WriteTempMCPConfigFile создает временный файл конфигурации mcp-config.json,
// в котором MCP-сервер бота подключается к мосту на сокете socketPath.
func WriteTempMCPConfigFile(socketPath string) (string, error) {
	cfg := Config{
		MCPServers: map[string]ServerConfig{
			bridgeServerName: {
				Command: GetBotExecutable(),
				Args:    bridgeServerArgs(socketPath),
			},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}

	tmpFile, err := os.CreateTemp("", "bro-bot-mcp-*.json")
	if err != nil {
		return "", err
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return "", err
	}
	_ = tmpFile.Close()
	return tmpFile.Name(), nil
}

// EnsureAgyMCPConfig прописывает MCP-сервер бота в ~/.gemini/config/mcp_config.json.
func EnsureAgyMCPConfig(socketPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cfgPath := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	return ensureServerInConfig(cfgPath, bridgeServerName, GetBotExecutable(), bridgeServerArgs(socketPath))
}

// ensureServerInConfig записывает в файл конфигурации path сервер name с командой
// command и аргументами args. Файл общий с пользователем и другими инструментами,
// поэтому всё остальное в нём сохраняется как есть: другие серверы со всеми их
// полями (env, cwd, url, headers…), прочие ключи верхнего уровня и дополнительные
// поля самой записи name. Повреждённый файл не перезаписывается, а неизменный —
// не трогается вовсе.
func ensureServerInConfig(path, name, command string, args []string) error {
	root := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	case len(bytes.TrimSpace(data)) > 0:
		if err := json.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("mcp: %s is not valid JSON, leaving it untouched: %w", path, err)
		}
	}

	servers := map[string]json.RawMessage{}
	if raw := root["mcpServers"]; len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return fmt.Errorf("mcp: %s has malformed mcpServers, leaving it untouched: %w", path, err)
		}
	}

	entry := map[string]interface{}{}
	if raw := servers[name]; len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &entry); err != nil {
			return fmt.Errorf("mcp: %s has malformed %q entry, leaving it untouched: %w", path, name, err)
		}
	}
	if entry["command"] == command && sameArgs(entry["args"], args) {
		return nil
	}
	entry["command"] = command
	entry["args"] = args

	if servers[name], err = json.Marshal(entry); err != nil {
		return err
	}
	if root["mcpServers"], err = json.Marshal(servers); err != nil {
		return err
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(out, '\n'))
}

// sameArgs сравнивает args из разобранного JSON с ожидаемым списком строк.
func sameArgs(raw interface{}, want []string) bool {
	got, ok := raw.([]interface{})
	if !ok || len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// writeFileAtomic заменяет файл целиком через временный файл в том же каталоге:
// агент, читающий конфигурацию параллельно, не увидит её наполовину записанной.
// Права существующего файла сохраняются.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, ".mcp_config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
