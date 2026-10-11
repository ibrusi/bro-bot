package agy

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"

	"bro-bot/internal/adapters/cliproc"
	"bro-bot/internal/config"
	"bro-bot/internal/mcp"
	"bro-bot/internal/ports"

	"github.com/creack/pty"
)

// AgyMCPAdapter запускает agy с подключением к MCP-серверу bro-bot.
type AgyMCPAdapter struct {
	cli *AgyAdapter
}

func NewAgyMCPAdapter() *AgyMCPAdapter {
	return &AgyMCPAdapter{
		cli: NewAgyAdapter(),
	}
}

func (a *AgyMCPAdapter) ExecutionMode() string {
	return "mcp"
}

func (a *AgyMCPAdapter) ExecuteTask(ctx context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	// Гарантируем наличие конфигурации bro_bot в ~/.gemini/config/mcp_config.json.
	// Без неё agy запустится, но инструменты бота будут ему недоступны — это не
	// повод отказываться от задачи, но повод сказать об этом в логе.
	if err := mcp.EnsureAgyMCPConfig(config.MCPSocketPath); err != nil {
		log.Printf("warning: agy-mcp: cannot update the MCP config: %v", err)
	}

	cmdArgs := buildAgyArgs(args)

	cmd := exec.CommandContext(ctx, "agy", cmdArgs...)
	cmd.Dir = args.WorkDir
	cmd.Env = append(os.Environ(),
		"TERM=dumb",
		"NO_COLOR=1",
		"CI=true",
	)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("agy-mcp: start PTY: %w", err)
	}

	return &AgyProcess{
		cmd:    cmd,
		ptmx:   ptmx,
		stdout: cliproc.NewPTYReader(ptmx),
	}, nil
}

func (a *AgyMCPAdapter) GetModels(ctx context.Context) ([]byte, error) {
	return a.cli.GetModels(ctx)
}

func (a *AgyMCPAdapter) GetQuota(ctx context.Context, lang string) ([]byte, error) {
	return a.cli.GetQuota(ctx, lang)
}

func (a *AgyMCPAdapter) GetQuotaText(ctx context.Context, lang string) ([]byte, error) {
	return a.cli.GetQuotaText(ctx, lang)
}

func (a *AgyMCPAdapter) GetCredits(ctx context.Context) ([]byte, error) {
	return a.cli.GetCredits(ctx)
}
