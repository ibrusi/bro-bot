package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"bro-bot/internal/adapters/cliproc"
	"bro-bot/internal/mcp"
	"bro-bot/internal/ports"

	"github.com/creack/pty"
)

// ClaudeMCPAdapter запускает Claude Code с подключением к MCP-серверу bro-bot через флаг --mcp-config.
type ClaudeMCPAdapter struct {
	cli *ClaudeAdapter
}

func NewClaudeMCPAdapter() *ClaudeMCPAdapter {
	return &ClaudeMCPAdapter{
		cli: NewClaudeAdapter(),
	}
}

func (a *ClaudeMCPAdapter) ExecutionMode() string {
	return "mcp"
}

func (a *ClaudeMCPAdapter) ExecuteTask(ctx context.Context, args ports.ExecuteArgs) (ports.AgentProcess, error) {
	cfgFile, err := mcp.WriteTempMCPConfigFile()
	if err != nil {
		return nil, fmt.Errorf("claude-mcp: write config: %w", err)
	}

	cmdArgs := []string{
		"--mcp-config", cfgFile,
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose",
	}
	if args.ConversationID != "" {
		cmdArgs = append(cmdArgs, "--resume", args.ConversationID)
	} else if strings.TrimSpace(args.SystemPrompt) != "" {
		cmdArgs = append(cmdArgs, "--append-system-prompt", strings.TrimSpace(args.SystemPrompt))
	}
	claudeModel := resolveClaudeModel(args.ModelName)
	if claudeModel != "" {
		cmdArgs = append(cmdArgs, "--model", claudeModel)
	}
	cmdArgs = append(cmdArgs, "-p", args.Prompt)

	cmd := exec.CommandContext(ctx, "claude", cmdArgs...)
	cmd.Dir = args.WorkDir
	cmd.Env = append(os.Environ(),
		"TERM=dumb",
		"NO_COLOR=1",
		"CI=true",
	)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		_ = os.Remove(cfgFile)
		return nil, fmt.Errorf("claude-mcp: start PTY: %w", err)
	}

	rawProc := &ClaudeProcess{
		cmd:    cmd,
		ptmx:   ptmx,
		stdout: cliproc.NewPTYReader(ptmx),
	}

	return &claudeMCPProcess{
		ClaudeProcess: rawProc,
		cfgFile:       cfgFile,
	}, nil
}

func (a *ClaudeMCPAdapter) GetModels(ctx context.Context) ([]byte, error) {
	return a.cli.GetModels(ctx)
}

func (a *ClaudeMCPAdapter) GetQuota(ctx context.Context, lang string) ([]byte, error) {
	return a.cli.GetQuota(ctx, lang)
}

func (a *ClaudeMCPAdapter) GetQuotaText(ctx context.Context, lang string) ([]byte, error) {
	return a.cli.GetQuotaText(ctx, lang)
}

func (a *ClaudeMCPAdapter) GetCredits(ctx context.Context) ([]byte, error) {
	return a.cli.GetCredits(ctx)
}

type claudeMCPProcess struct {
	*ClaudeProcess
	cfgFile string
	once    sync.Once
}

func (p *claudeMCPProcess) cleanup() {
	p.once.Do(func() {
		if p.cfgFile != "" {
			_ = os.Remove(p.cfgFile)
		}
	})
}

func (p *claudeMCPProcess) Wait() error {
	defer p.cleanup()
	return p.ClaudeProcess.Wait()
}

func (p *claudeMCPProcess) Kill() error {
	defer p.cleanup()
	return p.ClaudeProcess.Kill()
}

func (p *claudeMCPProcess) Close() error {
	defer p.cleanup()
	return p.ClaudeProcess.Close()
}
