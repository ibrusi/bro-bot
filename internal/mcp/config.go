package mcp

import (
	"encoding/json"
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

// WriteTempMCPConfigFile создает временный файл конфигурации mcp-config.json.
func WriteTempMCPConfigFile() (string, error) {
	cfg := Config{
		MCPServers: map[string]ServerConfig{
			"bro_bot": {
				Command: GetBotExecutable(),
				Args:    []string{"mcp-serve"},
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

// EnsureAgyMCPConfig проверяет и добавляет bro_bot в конфигурацию ~/.gemini/config/mcp_config.json.
func EnsureAgyMCPConfig() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cfgDir := filepath.Join(home, ".gemini", "config")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		return err
	}
	cfgPath := filepath.Join(cfgDir, "mcp_config.json")

	var cfg Config
	if data, err := os.ReadFile(cfgPath); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &cfg)
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = make(map[string]ServerConfig)
	}
	cfg.MCPServers["bro_bot"] = ServerConfig{
		Command: GetBotExecutable(),
		Args:    []string{"mcp-serve"},
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, out, 0644)
}
