package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// bridgeServerArgs — аргументы запуска `bot mcp-serve` с сокетом моста socketPath и токеном шага.
// Без пути mcp-serve найдёт сокет рядом со своим исполняемым файлом.
func bridgeServerArgs(socketPath, channelToken string) []string {
	args := []string{"mcp-serve"}
	if socketPath != "" {
		args = append(args, "--socket", socketPath)
	}
	if strings.TrimSpace(channelToken) != "" {
		args = append(args, "--token", strings.TrimSpace(channelToken))
	}
	return args
}

// WriteTempMCPConfigFile создает временный файл конфигурации mcp-config.json,
// в котором MCP-сервер бота подключается к мосту на сокете socketPath.
func WriteTempMCPConfigFile(socketPath string) (string, error) {
	return WriteTempMCPConfigFileWithToken(socketPath, "")
}

// WriteTempMCPConfigFileWithToken создает временный файл конфигурации mcp-config.json
// с привязкой к одноразовому токену шага channelToken.
func WriteTempMCPConfigFileWithToken(socketPath, channelToken string) (string, error) {
	cfg := Config{
		MCPServers: map[string]ServerConfig{
			bridgeServerName: {
				Command: GetBotExecutable(),
				Args:    bridgeServerArgs(socketPath, channelToken),
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

var agyConfigMu sync.Mutex

// RemoveAgyMCPConfig удаляет устаревшую запись bro_bot из конфигов agy
// (~/.gemini/config/mcp_config.json и ~/.gemini/antigravity-cli/mcp_config.json),
// чтобы она не перебивала встроенный in-process HTTP-эндпоинт сессии agy acp.
// Все остальные серверы и поля сохраняются; повреждённые файлы не трогаются.
func RemoveAgyMCPConfig() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	agyConfigMu.Lock()
	defer agyConfigMu.Unlock()

	paths := []string{
		filepath.Join(home, ".gemini", "config", "mcp_config.json"),
		filepath.Join(home, ".gemini", "antigravity-cli", "mcp_config.json"),
	}
	var errs []error
	for _, p := range paths {
		if err := removeServerFromConfig(p, bridgeServerName); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// removeServerFromConfig удаляет только запись name из mcpServers в файле path.
// Все прочие серверы и ключи верхнего уровня сохраняются. Повреждённый JSON или
// файл без записи name не перезаписываются.
func removeServerFromConfig(path, name string) error {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case len(bytes.TrimSpace(data)) == 0:
		return nil
	}

	root := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("mcp: %s is not valid JSON, leaving it untouched: %w", path, err)
	}

	rawServers := root["mcpServers"]
	if len(rawServers) == 0 || string(rawServers) == "null" {
		return nil
	}

	servers := map[string]json.RawMessage{}
	if err := json.Unmarshal(rawServers, &servers); err != nil {
		return fmt.Errorf("mcp: %s has malformed mcpServers, leaving it untouched: %w", path, err)
	}

	if _, exists := servers[name]; !exists {
		return nil
	}
	delete(servers, name)

	if root["mcpServers"], err = json.Marshal(servers); err != nil {
		return err
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(out, '\n'))
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
